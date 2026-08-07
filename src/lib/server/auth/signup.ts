import { createUser, getUserByEmail } from '$lib/server/db/queries/users';
import { hashPassword } from './password';
import { createSession, generateSessionToken } from './session';

export class SignupError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const EMAIL_MAX_LENGTH = 255;
const DISPLAY_NAME_MAX_LENGTH = 255;
const PASSWORD_MIN_LENGTH = 8;
const PASSWORD_MAX_LENGTH = 200;

const DUPLICATE_EMAIL_MESSAGE = 'An account with that email already exists';

/** Postgres SQLSTATE for a unique-constraint violation, wrapped by drizzle-orm's postgres-js driver. */
const UNIQUE_VIOLATION = '23505';

function isUniqueViolation(err: unknown): boolean {
	return (
		typeof err === 'object' &&
		err !== null &&
		'cause' in err &&
		typeof err.cause === 'object' &&
		err.cause !== null &&
		'code' in err.cause &&
		err.cause.code === UNIQUE_VIOLATION
	);
}

/** Validates input, hashes the password, creates the user, and starts a session. */
export async function signUp(email: string, password: string, displayName: string) {
	if (!EMAIL_RE.test(email) || email.length > EMAIL_MAX_LENGTH) {
		throw new SignupError(400, 'Invalid email address');
	}
	if (password.length < PASSWORD_MIN_LENGTH || password.length > PASSWORD_MAX_LENGTH) {
		throw new SignupError(
			400,
			`Password must be between ${PASSWORD_MIN_LENGTH} and ${PASSWORD_MAX_LENGTH} characters`
		);
	}
	if (!displayName.trim() || displayName.length > DISPLAY_NAME_MAX_LENGTH) {
		throw new SignupError(400, 'Display name is required');
	}

	// Both run unconditionally, in parallel, so a duplicate-email rejection takes the same
	// time as a real signup — otherwise the ~16ms saved by skipping the hash is an oracle an
	// attacker can use to enumerate registered emails (worse once combined with any gap in
	// rate limiting).
	const [existing, passwordHash] = await Promise.all([
		getUserByEmail(email),
		hashPassword(password)
	]);
	if (existing) throw new SignupError(409, DUPLICATE_EMAIL_MESSAGE);

	let user;
	try {
		user = await createUser({ email, passwordHash, displayName: displayName.trim() });
	} catch (err) {
		// The `getUserByEmail` check above isn't atomic with this insert — a concurrent signup
		// for the same email can race it. The `lower(email)` unique index is the actual
		// guarantee; this just turns its violation into the same clean 409 instead of a 500.
		if (isUniqueViolation(err)) throw new SignupError(409, DUPLICATE_EMAIL_MESSAGE);
		throw err;
	}

	const token = generateSessionToken();
	const session = await createSession(token, user.id);

	return { user, token, expiresAt: session.expiresAt };
}
