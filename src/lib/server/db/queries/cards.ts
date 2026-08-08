/**
 * Card reads. Cards are content addressing only (`docs/schema.md`) — generated from a note
 * by `notes.ts`'s fan-out, never created or edited directly. Access is deck-gated the same
 * way as everything else touching a deck (CLAUDE.md §9).
 */
import { eq } from 'drizzle-orm';
import { cards, notes } from '../schema';
import { db } from '../index';
import { requireDeckAccess } from './access';
import type { DbClient } from './types';

/** Re-verifies the note is actually in `deckId` after the access check, the same way
 *  `getCard` and `notes.ts::getNote` do — access to `deckId` is not access to an arbitrary
 *  `noteId` the caller happens to guess. */
export async function listCardsForNote(
	userId: string,
	deckId: string,
	noteId: string,
	client: DbClient = db
) {
	await requireDeckAccess(client, userId, deckId, 'read');
	const [note] = await client
		.select({ deckId: notes.deckId })
		.from(notes)
		.where(eq(notes.id, noteId));
	if (!note || note.deckId !== deckId) return [];
	return client.select().from(cards).where(eq(cards.noteId, noteId));
}

export async function listCardsForDeck(userId: string, deckId: string, client: DbClient = db) {
	await requireDeckAccess(client, userId, deckId, 'read');
	return client.select().from(cards).where(eq(cards.deckId, deckId));
}

/** `null` if the card doesn't exist or the user can't read its deck. */
export async function getCard(
	userId: string,
	deckId: string,
	cardId: string,
	client: DbClient = db
) {
	await requireDeckAccess(client, userId, deckId, 'read');
	const [card] = await client.select().from(cards).where(eq(cards.id, cardId));
	return card && card.deckId === deckId ? card : null;
}
