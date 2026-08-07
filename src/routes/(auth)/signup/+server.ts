import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { checkRateLimit } from '$lib/server/auth/rate-limit';
import { SignupError, signUp } from '$lib/server/auth/signup';
import { setSessionCookie } from '$lib/server/auth/session';

// Signup runs an argon2 hash on every call (including duplicate-email attempts, for constant
// timing — see signup.ts), and argon2 shares Node's 4-slot libuv threadpool with fs/dns/zlib.
// A burst of concurrent signups is a whole-process DoS, not just an auth problem, hence the
// tight per-IP limit. (CSRF is checked centrally in hooks.server.ts.)
const IP_LIMIT = 10;
const IP_WINDOW_MS = 60 * 60 * 1000; // 1 hour

export const POST: RequestHandler = async (event) => {
	const ip = event.getClientAddress();
	if (!checkRateLimit(`signup:ip:${ip}`, IP_LIMIT, IP_WINDOW_MS)) {
		return json({ error: 'Too many signup attempts. Try again later.' }, { status: 429 });
	}

	const body = await event.request.json().catch(() => null);
	if (
		!body ||
		typeof body.email !== 'string' ||
		typeof body.password !== 'string' ||
		typeof body.displayName !== 'string'
	) {
		return json({ error: 'email, password, and displayName are required' }, { status: 400 });
	}

	try {
		const { user, token, expiresAt } = await signUp(body.email, body.password, body.displayName);
		setSessionCookie(event, token, expiresAt);
		return json(
			{ user: { id: user.id, email: user.email, displayName: user.displayName } },
			{ status: 201 }
		);
	} catch (err) {
		if (err instanceof SignupError) return json({ error: err.message }, { status: err.status });
		throw err;
	}
};
