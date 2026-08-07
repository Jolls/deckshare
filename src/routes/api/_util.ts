/**
 * Shared helpers for the `api/` route handlers. Not a route itself (SvelteKit only treats
 * `+server.ts` / `+page.ts` etc. as routes).
 *
 * Route handlers stay thin (CLAUDE.md §4): parse, authorise, delegate to
 * `src/lib/server/db/queries/`, respond. This file is the "authorise" and "respond with the
 * right status" part shared across all of them.
 */
import { error } from '@sveltejs/kit';
import { DeckAccessError } from '$lib/server/db/queries/access';
import { NoteTypeAccessError } from '$lib/server/db/queries/note-types';

/**
 * `locals.user.id` is set by the auth layer's hooks.server.ts (issue #7) from the session
 * cookie. Throws 401 if there's no session.
 */
export function requireUserId(locals: App.Locals): string {
	const userId = locals.user?.id;
	if (!userId) throw error(401, 'Unauthorized');
	return userId;
}

/**
 * Maps `DeckAccessError` / `NoteTypeAccessError` to the right HTTP status and re-throws
 * anything else unchanged. `DeckAccessError`'s `not_found` and `forbidden` both come from the
 * same access check (`access.ts`) so a denied user can't distinguish "doesn't exist" from
 * "exists but you can't see it".
 */
export function respondToAccessError(err: unknown): never {
	if (err instanceof DeckAccessError) {
		throw error(
			err.reason === 'not_found' ? 404 : 403,
			err.reason === 'not_found' ? 'Not found' : 'Forbidden'
		);
	}
	if (err instanceof NoteTypeAccessError) {
		throw error(403, 'Forbidden');
	}
	throw err;
}
