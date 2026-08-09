/**
 * Note CRUD, including the card fan-out (CLAUDE.md §4: cards are generated from a note
 * through its note type's templates, never authored directly).
 *
 * Standard note types generate one card per template. Cloze note types generate one card per
 * distinct `{{cN::...}}` ordinal found across the note's fields, using the note type's first
 * template for all of them (Anki cloze note types have exactly one).
 *
 * Every query takes `userId` + `deckId` and resolves access via `access.ts` before touching a
 * row (CLAUDE.md §9).
 */
import { asc, eq } from 'drizzle-orm';
import { notes, cards, templates } from '../schema';
import { db } from '../index';
import { requireDeckAccess } from './access';
import { requireNoteTypeOwner } from './note-types';
import { getClozeNumbers } from '$lib/render';
import type { DbClient } from './types';

export interface CreateNoteInput {
	noteTypeId: string;
	guid: string;
	fields: string[];
	tags?: string[];
}

/** Also verifies the note type is owned by `userId` — a deck a user can write to doesn't
 *  grant them the right to attach notes to someone else's note type. */
async function cardRowsFor(
	client: DbClient,
	userId: string,
	noteId: string,
	deckId: string,
	noteTypeId: string,
	fieldValues: string[]
) {
	const noteType = await requireNoteTypeOwner(client, userId, noteTypeId);

	const templateRows = await client
		.select()
		.from(templates)
		.where(eq(templates.noteTypeId, noteTypeId))
		.orderBy(asc(templates.ordinal));

	if (noteType.isCloze) {
		const firstTemplate = templateRows[0];
		if (!firstTemplate) throw new Error(`cloze note type ${noteTypeId} has no template`);
		const clozeNumbers = new Set<number>();
		for (const value of fieldValues) {
			for (const n of getClozeNumbers(value)) clozeNumbers.add(n);
		}
		return [...clozeNumbers]
			.sort((a, b) => a - b)
			.map((n) => ({ noteId, templateId: firstTemplate.id, ordinal: n - 1, deckId }));
	}

	return templateRows.map((t) => ({ noteId, templateId: t.id, ordinal: t.ordinal, deckId }));
}

/** Creates a note and its generated cards atomically. */
export async function createNote(
	userId: string,
	deckId: string,
	input: CreateNoteInput,
	client: DbClient = db
) {
	return client.transaction(async (tx) => {
		// `notes.owner_id` is the deck's owner, not the caller: an editor on someone else's deck
		// adds notes to *that* owner's collection, and it's their `(owner_id, guid)` namespace
		// the import key lives in. The access check already read the row, so this is free.
		const { ownerId } = await requireDeckAccess(tx, userId, deckId, 'write');

		const [note] = await tx
			.insert(notes)
			.values({
				deckId,
				ownerId,
				noteTypeId: input.noteTypeId,
				guid: input.guid,
				fields: input.fields,
				tags: input.tags ?? []
			})
			.returning();
		if (!note) throw new Error('note insert returned no row');

		const rows = await cardRowsFor(tx, userId, note.id, deckId, input.noteTypeId, input.fields);
		const insertedCards = rows.length > 0 ? await tx.insert(cards).values(rows).returning() : [];

		return { note, cards: insertedCards };
	});
}

export async function listNotesForDeck(userId: string, deckId: string, client: DbClient = db) {
	await requireDeckAccess(client, userId, deckId, 'read');
	return client.select().from(notes).where(eq(notes.deckId, deckId));
}

/** `null` if the note doesn't exist, isn't in `deckId`, or the user can't read the deck. */
export async function getNote(
	userId: string,
	deckId: string,
	noteId: string,
	client: DbClient = db
) {
	await requireDeckAccess(client, userId, deckId, 'read');
	const [note] = await client.select().from(notes).where(eq(notes.id, noteId));
	return note && note.deckId === deckId ? note : null;
}

export interface UpdateNoteInput {
	fields?: string[];
	tags?: string[];
}

/**
 * Updates a note's fields/tags. If `fields` changes, cards are regenerated: existing cards
 * for the note are dropped and recreated from the new field values, so a cloze note's card
 * count stays in sync with which `{{cN::...}}` ordinals are still present.
 */
export async function updateNote(
	userId: string,
	deckId: string,
	noteId: string,
	patch: UpdateNoteInput,
	client: DbClient = db
) {
	return client.transaction(async (tx) => {
		await requireDeckAccess(tx, userId, deckId, 'write');

		const [existing] = await tx.select().from(notes).where(eq(notes.id, noteId));
		if (!existing || existing.deckId !== deckId) return null;

		const [updated] = await tx
			.update(notes)
			.set({ ...patch, modifiedAt: new Date() })
			.where(eq(notes.id, noteId))
			.returning();
		if (!updated) return null;

		if (patch.fields) {
			// KNOWN LIMITATION (flagged for whoever builds the reviewer, CLAUDE.md §6/§17): this
			// drops and recreates the note's cards, which is fine while nothing populates
			// `user_card_state` or `review_log` yet. Once the reviewer or apkg import exists,
			// this either aborts on the FK-restrict from an existing `review_log` row for a
			// dropped card, or — for a card with no reviews yet but scheduling state — silently
			// cascade-deletes that `user_card_state` row. Revisit the regen strategy (e.g. diff
			// old/new cloze ordinals and only touch what changed) before either path is reachable.
			await tx.delete(cards).where(eq(cards.noteId, noteId));
			const rows = await cardRowsFor(tx, userId, noteId, deckId, updated.noteTypeId, patch.fields);
			if (rows.length > 0) await tx.insert(cards).values(rows);
		}

		return updated;
	});
}

export async function deleteNote(
	userId: string,
	deckId: string,
	noteId: string,
	client: DbClient = db
) {
	return client.transaction(async (tx) => {
		await requireDeckAccess(tx, userId, deckId, 'write');
		const [existing] = await tx.select().from(notes).where(eq(notes.id, noteId));
		if (!existing || existing.deckId !== deckId) return false;

		await tx.delete(notes).where(eq(notes.id, noteId));
		return true;
	});
}
