import { error } from '@sveltejs/kit';
import { getReviewSession } from '$lib/server/db/queries/review';
import { respondToAccessError } from '../../../api/_util';
import type { PageServerLoad } from './$types';

/**
 * Session start (CLAUDE.md §6): one request fetches the whole batch — rendered, sanitised
 * cards, the caller's `user_card_state`, and their FSRS parameters. The reviewer never fetches
 * again during a session.
 *
 * There is no login *page* yet (auth ships as API endpoints, issue #7), so an anonymous
 * request is a 401 rather than a redirect.
 */
export const load: PageServerLoad = async ({ locals, params }) => {
	const userId = locals.user?.id;
	if (!userId) throw error(401, 'Unauthorized');

	try {
		return { session: await getReviewSession(userId, params.deckId) };
	} catch (err) {
		respondToAccessError(err);
	}
};
