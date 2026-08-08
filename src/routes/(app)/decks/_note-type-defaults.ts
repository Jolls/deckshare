import {
	createNoteType,
	getNoteType,
	listNoteTypesForUser
} from '$lib/server/db/queries/note-types';

const BASIC_NOTE_TYPE_INPUT = {
	name: 'Basic',
	fields: [{ name: 'Front' }, { name: 'Back' }],
	templates: [{ name: 'Card 1', qfmt: '{{Front}}', afmt: '{{FrontSide}}<hr id=answer>{{Back}}' }]
};

/**
 * Returns the user's one note type, creating the default "Basic" type on first use.
 *
 * The list-then-create isn't atomic, so two concurrent first calls (e.g. two tabs) can each
 * see an empty list and both create a "Basic" type. Rather than locking against that, this
 * always resolves to the lowest id (UUIDv7 ids are time-ordered, CLAUDE.md §5), so every
 * caller converges on the same note type regardless of how many got created — a harmless
 * duplicate row, not a correctness problem.
 */
export async function resolveNoteType(userId: string) {
	let existing = await listNoteTypesForUser(userId);
	if (existing.length === 0) {
		await createNoteType(userId, BASIC_NOTE_TYPE_INPUT);
		existing = await listNoteTypesForUser(userId);
	}
	const canonical = existing.slice().sort((a, b) => (a.id < b.id ? -1 : 1))[0];
	if (!canonical) throw new Error('note type vanished immediately after creation');
	const full = await getNoteType(userId, canonical.id);
	if (!full) throw new Error('note type vanished between list and get');
	return full;
}
