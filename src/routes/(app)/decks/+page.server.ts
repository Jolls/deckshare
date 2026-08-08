import { fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { createDeck, listDecksForUser } from '$lib/server/db/queries/decks';

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
		const deck = await createDeck(locals.user.id, {
			name: name.trim(),
			description:
				typeof description === 'string' && description.trim() ? description.trim() : undefined
		});
		throw redirect(303, `/decks/${deck.id}`);
	}
};
