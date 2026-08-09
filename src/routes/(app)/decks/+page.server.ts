import { fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { createDeck, listDecksForUser } from '$lib/server/db/queries/decks';
import { DuplicateNameError } from '$lib/server/db/queries/errors';

export const load: PageServerLoad = async ({ locals }) => {
	if (!locals.user) throw redirect(303, '/login');
	const decks = await listDecksForUser(locals.user.id);
	return { decks };
};

export const actions: Actions = {
	create: async ({ request, locals }) => {
		if (!locals.user) throw redirect(303, '/login');
		const form = await request.formData();
		const name = form.get('name');
		if (typeof name !== 'string' || !name.trim()) {
			return fail(400, { error: 'Deck name is required' });
		}
		const description = form.get('description');
		let deck;
		try {
			deck = await createDeck(locals.user.id, {
				name: name.trim(),
				description:
					typeof description === 'string' && description.trim() ? description.trim() : undefined
			});
		} catch (err) {
			if (!(err instanceof DuplicateNameError)) throw err;
			return fail(409, { error: `You already have a deck called "${name.trim()}"` });
		}
		throw redirect(303, `/decks/${deck.id}`);
	}
};
