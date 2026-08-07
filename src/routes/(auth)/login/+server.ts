import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { checkRateLimit } from '$lib/server/auth/rate-limit';
import { LoginError, logIn } from '$lib/server/auth/login';
import { setSessionCookie } from '$lib/server/auth/session';

// Two limiters: a broad per-IP cap (catches credential stuffing / distributed guessing from
// one source regardless of which email it targets) and a tighter per-(IP, email) cap (catches
// repeated guesses against one account without punishing everyone sharing an IP — e.g. NAT,
// campus wifi). CSRF is checked centrally in hooks.server.ts.
const IP_LIMIT = 30;
const IP_WINDOW_MS = 5 * 60 * 1000; // 5 minutes
const IP_EMAIL_LIMIT = 5;
const IP_EMAIL_WINDOW_MS = 15 * 60 * 1000; // 15 minutes

export const POST: RequestHandler = async (event) => {
	const ip = event.getClientAddress();
	if (!checkRateLimit(`login:ip:${ip}`, IP_LIMIT, IP_WINDOW_MS)) {
		return json({ error: 'Too many attempts. Try again later.' }, { status: 429 });
	}

	const body = await event.request.json().catch(() => null);
	if (!body || typeof body.email !== 'string' || typeof body.password !== 'string') {
		return json({ error: 'email and password are required' }, { status: 400 });
	}

	if (
		!checkRateLimit(
			`login:ip-email:${ip}:${body.email.toLowerCase()}`,
			IP_EMAIL_LIMIT,
			IP_EMAIL_WINDOW_MS
		)
	) {
		return json({ error: 'Too many attempts. Try again later.' }, { status: 429 });
	}

	try {
		const { user, token, expiresAt } = await logIn(body.email, body.password);
		setSessionCookie(event, token, expiresAt);
		return json({ user: { id: user.id, email: user.email, displayName: user.displayName } });
	} catch (err) {
		if (err instanceof LoginError) return json({ error: err.message }, { status: err.status });
		throw err;
	}
};
