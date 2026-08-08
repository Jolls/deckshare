import { json, error } from '@sveltejs/kit';
import { getNote, updateNote, deleteNote } from '$lib/server/db/queries/notes';
import { requireUserId, respondToAccessError } from '../../../../_util';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async ({ locals, params }) => {
	const userId = requireUserId(locals);
	try {
		const note = await getNote(userId, params.deckId, params.noteId);
		if (!note) throw error(404, 'Not found');
		return json(note);
	} catch (err) {
		respondToAccessError(err);
	}
};

export const PATCH: RequestHandler = async ({ locals, params, request }) => {
	const userId = requireUserId(locals);
	const body = await request.json();
	try {
		const note = await updateNote(userId, params.deckId, params.noteId, {
			fields: Array.isArray(body?.fields) ? body.fields : undefined,
			tags: Array.isArray(body?.tags) ? body.tags : undefined
		});
		if (!note) throw error(404, 'Not found');
		return json(note);
	} catch (err) {
		respondToAccessError(err);
	}
};

export const DELETE: RequestHandler = async ({ locals, params }) => {
	const userId = requireUserId(locals);
	try {
		const deleted = await deleteNote(userId, params.deckId, params.noteId);
		if (!deleted) throw error(404, 'Not found');
		return new Response(null, { status: 204 });
	} catch (err) {
		respondToAccessError(err);
	}
};
