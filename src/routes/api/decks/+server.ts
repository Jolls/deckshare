import { json, error } from '@sveltejs/kit';
import { createDeck, listDecksForUser } from '$lib/server/db/queries/decks';
import { requireUserId, respondToQueryError } from '../_util';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async ({ locals }) => {
	const userId = requireUserId(locals);
	const decks = await listDecksForUser(userId);
	return json(decks);
};

export const POST: RequestHandler = async ({ locals, request }) => {
	const userId = requireUserId(locals);
	const body = await request.json();
	if (typeof body?.name !== 'string' || body.name.length === 0) {
		throw error(400, 'name is required');
	}
	try {
		const deck = await createDeck(userId, {
			name: body.name,
			description: typeof body.description === 'string' ? body.description : undefined,
			visibility: body.visibility === 'public' ? 'public' : undefined,
			preset: body.preset
		});
		return json(deck, { status: 201 });
	} catch (err) {
		respondToQueryError(err);
	}
};
