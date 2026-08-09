import {
	createNoteType,
	getNoteType,
	listNoteTypesForUser
} from '$lib/server/db/queries/note-types';
import { DuplicateNameError } from '$lib/server/db/queries/errors';

const BASIC_NOTE_TYPE_INPUT = {
	name: 'Basic',
	fields: [{ name: 'Front' }, { name: 'Back' }],
	templates: [{ name: 'Card 1', qfmt: '{{Front}}', afmt: '{{FrontSide}}<hr id=answer>{{Back}}' }]
};

/**
 * Returns the user's one note type, creating the default "Basic" type on first use.
 *
 * The list-then-create isn't atomic, so two concurrent first calls (e.g. two tabs) can each
 * see an empty list and both try to create a "Basic" type. `note_types (owner_id, name)` is
 * unique, so the loser gets a `DuplicateNameError` and simply re-reads the winner's row.
 * Callers then converge on the lowest id (UUIDv7 ids are time-ordered, CLAUDE.md §5).
 */
export async function resolveNoteType(userId: string) {
	let existing = await listNoteTypesForUser(userId);
	if (existing.length === 0) {
		try {
			await createNoteType(userId, BASIC_NOTE_TYPE_INPUT);
		} catch (err) {
			if (!(err instanceof DuplicateNameError)) throw err;
		}
		existing = await listNoteTypesForUser(userId);
	}
	const canonical = existing.slice().sort((a, b) => (a.id < b.id ? -1 : 1))[0];
	if (!canonical) throw new Error('note type vanished immediately after creation');
	const full = await getNoteType(userId, canonical.id);
	if (!full) throw new Error('note type vanished between list and get');
	return full;
}
