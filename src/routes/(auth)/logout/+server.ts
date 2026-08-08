import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { deleteSessionCookie, invalidateSessionById } from '$lib/server/auth/session';

// CSRF is checked centrally in hooks.server.ts.
export const POST: RequestHandler = async (event) => {
	if (event.locals.sessionId) {
		await invalidateSessionById(event.locals.sessionId);
	}
	deleteSessionCookie(event);

	return json({ ok: true });
};
