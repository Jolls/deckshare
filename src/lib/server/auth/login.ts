import { getUserByEmail } from '$lib/server/db/queries/users';
import { DUMMY_PASSWORD_HASH, verifyPassword } from './password';
import { createSession, generateSessionToken } from './session';

export class LoginError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

const EMAIL_MAX_LENGTH = 255;
const PASSWORD_MAX_LENGTH = 200;

/**
 * Verifies credentials and starts a session. Always runs a password verification, even when
 * no account matches the email, so the response time doesn't reveal which case occurred.
 */
export async function logIn(email: string, password: string) {
	// Deterministic on the attacker's own input, not on account existence, so fast-rejecting
	// oversized input doesn't add a timing oracle — it only reveals what they already know.
	if (email.length > EMAIL_MAX_LENGTH || password.length > PASSWORD_MAX_LENGTH) {
		throw new LoginError(401, 'Invalid email or password');
	}

	const user = await getUserByEmail(email);
	const valid = await verifyPassword(user?.passwordHash ?? DUMMY_PASSWORD_HASH, password);

	if (!user || !valid) throw new LoginError(401, 'Invalid email or password');

	const token = generateSessionToken();
	const session = await createSession(token, user.id);

	return { user, token, expiresAt: session.expiresAt };
}
