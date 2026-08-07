import { json, error } from '@sveltejs/kit';
import { createNote, listNotesForDeck } from '$lib/server/db/queries/notes';
import { requireUserId, respondToAccessError } from '../../../_util';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async ({ locals, params }) => {
	const userId = requireUserId(locals);
	try {
		return json(await listNotesForDeck(userId, params.deckId));
	} catch (err) {
		respondToAccessError(err);
	}
};

export const POST: RequestHandler = async ({ locals, params, request }) => {
	const userId = requireUserId(locals);
	const body = await request.json();
	if (
		typeof body?.noteTypeId !== 'string' ||
		typeof body?.guid !== 'string' ||
		!Array.isArray(body?.fields)
	) {
		throw error(400, 'noteTypeId, guid, and fields are required');
	}
	try {
		const result = await createNote(userId, params.deckId, {
			noteTypeId: body.noteTypeId,
			guid: body.guid,
			fields: body.fields,
			tags: Array.isArray(body.tags) ? body.tags : undefined
		});
		return json(result, { status: 201 });
	} catch (err) {
		respondToAccessError(err);
	}
};
