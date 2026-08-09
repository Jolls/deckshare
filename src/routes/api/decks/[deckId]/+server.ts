import { json } from '@sveltejs/kit';
import { getDeck, updateDeck, deleteDeck } from '$lib/server/db/queries/decks';
import { requireUserId, respondToAccessError, respondToQueryError } from '../../_util';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async ({ locals, params }) => {
	const userId = requireUserId(locals);
	try {
		return json(await getDeck(userId, params.deckId));
	} catch (err) {
		respondToAccessError(err);
	}
};

export const PATCH: RequestHandler = async ({ locals, params, request }) => {
	const userId = requireUserId(locals);
	const body = await request.json();
	try {
		const deck = await updateDeck(userId, params.deckId, {
			name: typeof body?.name === 'string' ? body.name : undefined,
			description: typeof body?.description === 'string' ? body.description : undefined,
			visibility:
				body?.visibility === 'public' || body?.visibility === 'private'
					? body.visibility
					: undefined,
			preset: body?.preset
		});
		return json(deck);
	} catch (err) {
		respondToQueryError(err);
	}
};

export const DELETE: RequestHandler = async ({ locals, params }) => {
	const userId = requireUserId(locals);
	try {
		await deleteDeck(userId, params.deckId);
		return new Response(null, { status: 204 });
	} catch (err) {
		respondToAccessError(err);
	}
};
