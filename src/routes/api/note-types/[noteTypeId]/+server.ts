import { json, error } from '@sveltejs/kit';
import { getNoteType, updateNoteType, deleteNoteType } from '$lib/server/db/queries/note-types';
import { requireUserId, respondToQueryError } from '../../_util';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async ({ locals, params }) => {
	const userId = requireUserId(locals);
	const noteType = await getNoteType(userId, params.noteTypeId);
	if (!noteType) throw error(404, 'Not found');
	return json(noteType);
};

export const PATCH: RequestHandler = async ({ locals, params, request }) => {
	const userId = requireUserId(locals);
	const body = await request.json();
	let noteType;
	try {
		noteType = await updateNoteType(userId, params.noteTypeId, {
			name: typeof body?.name === 'string' ? body.name : undefined,
			css: typeof body?.css === 'string' ? body.css : undefined,
			isCloze: typeof body?.isCloze === 'boolean' ? body.isCloze : undefined,
			sortFieldIdx: typeof body?.sortFieldIdx === 'number' ? body.sortFieldIdx : undefined
		});
	} catch (err) {
		respondToQueryError(err);
	}
	if (!noteType) throw error(404, 'Not found');
	return json(noteType);
};

export const DELETE: RequestHandler = async ({ locals, params }) => {
	const userId = requireUserId(locals);
	const deleted = await deleteNoteType(userId, params.noteTypeId);
	if (!deleted) throw error(404, 'Not found');
	return new Response(null, { status: 204 });
};
