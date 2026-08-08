import type { Handle } from '@sveltejs/kit';
import { assertSameOrigin } from '$lib/server/auth/csrf';
import {
	SESSION_COOKIE_NAME,
	validateSessionToken,
	setSessionCookie,
	deleteSessionCookie
} from '$lib/server/auth/session';

/**
 * Runs on every request: CSRF-checks state-changing requests, then populates
 * `event.locals.user` from the session cookie.
 *
 * The Origin check lives here rather than per-route so it's impossible for a future
 * state-changing route (CLAUDE.md §6's `/api/reviews/batch`, deck/note/card writes, …) to
 * ship without it by forgetting to call it — it applies to every non-GET/HEAD request
 * automatically.
 */
export const handle: Handle = async ({ event, resolve }) => {
	if (event.request.method !== 'GET' && event.request.method !== 'HEAD') {
		assertSameOrigin(event);
	}

	const token = event.cookies.get(SESSION_COOKIE_NAME) ?? null;

	if (!token) {
		event.locals.user = null;
		event.locals.sessionId = null;
		return resolve(event);
	}

	const result = await validateSessionToken(token);
	if (result) {
		event.locals.user = result.user;
		// The session's primary key, never the raw token — a route that accidentally leaked
		// `locals` into an SSR payload (e.g. a `+layout.server.ts` spreading `locals`) would
		// leak a replayable session if this were the raw token instead.
		event.locals.sessionId = result.session.id;
		if (result.renewed) setSessionCookie(event, token, result.session.expiresAt);
	} else {
		event.locals.user = null;
		event.locals.sessionId = null;
		deleteSessionCookie(event);
	}

	return resolve(event);
};
