import { error, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { getDeck } from '$lib/server/db/queries/decks';
import { DeckAccessError } from '$lib/server/db/queries/access';
import { createNote } from '$lib/server/db/queries/notes';
import { uuidv7 } from '$lib/uuid';
import { resolveNoteType } from '../../../_note-type-defaults';

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
	const noteType = await resolveNoteType(locals.user.id);
	return { deck, noteType };
};

export const actions: Actions = {
	create: async ({ request, locals, params }) => {
		if (!locals.user) throw redirect(303, '/login');
		const noteType = await resolveNoteType(locals.user.id);
		const form = await request.formData();
		const fields = noteType.fields
			.slice()
			.sort((a, b) => a.ordinal - b.ordinal)
			.map((field) => {
				const value = form.get(field.name);
				return typeof value === 'string' ? value : '';
			});
		const guid = uuidv7();
		try {
			await createNote(locals.user.id, params.deckId, { noteTypeId: noteType.id, guid, fields });
		} catch (err) {
			if (err instanceof DeckAccessError) {
				throw error(err.reason === 'not_found' ? 404 : 403, 'Not found');
			}
			throw err;
		}
		throw redirect(303, `/decks/${params.deckId}`);
	}
};
