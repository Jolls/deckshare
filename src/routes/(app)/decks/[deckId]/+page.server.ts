import { error, fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { getDeck } from '$lib/server/db/queries/decks';
import { DeckAccessError } from '$lib/server/db/queries/access';
import { deleteNote, listNotesForDeck } from '$lib/server/db/queries/notes';
import { resolveNoteType } from '../_note-type-defaults';

export const load: PageServerLoad = async ({ locals, params }) => {
	if (!locals.user) throw redirect(303, '/login');
	let deck;
	try {
		({ deck } = await getDeck(locals.user.id, params.deckId));
	} catch (err) {
		if (err instanceof DeckAccessError) {
			throw error(err.reason === 'not_found' ? 404 : 403, 'Not found');
		}
		throw err;
	}
	const notes = await listNotesForDeck(locals.user.id, params.deckId);
	const noteType = await resolveNoteType(locals.user.id);
	return { deck, notes, noteType };
};

export const actions: Actions = {
	deleteNote: async ({ request, locals, params }) => {
		if (!locals.user) throw redirect(303, '/login');
		const form = await request.formData();
		const noteId = form.get('noteId');
		if (typeof noteId !== 'string') return fail(400, { error: 'noteId is required' });
		try {
			await deleteNote(locals.user.id, params.deckId, noteId);
		} catch (err) {
			if (err instanceof DeckAccessError) {
				throw error(err.reason === 'not_found' ? 404 : 403, 'Not found');
			}
			throw err;
		}
		return { success: true };
	}
};
