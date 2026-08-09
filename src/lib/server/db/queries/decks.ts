/**
 * Deck CRUD. Every query here resolves the caller's role via `access.ts` before touching a
 * row (CLAUDE.md §9) — route guards alone are not sufficient.
 */
import { eq, and } from 'drizzle-orm';
import { decks, deckAccess, type deckVisibility } from '../schema';
import { db } from '../index';
import { requireDeckAccess } from './access';
import { rethrowDuplicateName } from './errors';
import type { DbClient } from './types';

type Visibility = (typeof deckVisibility.enumValues)[number];

export interface CreateDeckInput {
	name: string;
	description?: string;
	visibility?: Visibility;
	preset?: unknown;
}

/** Creates a deck and grants its creator the `owner` role, atomically. Throws
 *  `DuplicateNameError` if the user already owns a deck of that name — the name is the key
 *  import dedups on, so it can't be reused. */
export async function createDeck(userId: string, input: CreateDeckInput, client: DbClient = db) {
	return rethrowDuplicateName('deck', input.name, () =>
		client.transaction(async (tx) => {
			const [deck] = await tx
				.insert(decks)
				.values({
					ownerId: userId,
					name: input.name,
					description: input.description ?? '',
					visibility: input.visibility ?? 'private',
					preset: input.preset
				})
				.returning();
			if (!deck) throw new Error('deck insert returned no row');

			await tx.insert(deckAccess).values({ deckId: deck.id, userId, role: 'owner' });
			return deck;
		})
	);
}

/** Every deck the user has an explicit `deck_access` row for. Does not include the public
 *  directory of decks they haven't joined — that's a separate, not-yet-built feature. */
export async function listDecksForUser(userId: string, client: DbClient = db) {
	return client
		.select({ deck: decks, role: deckAccess.role })
		.from(deckAccess)
		.innerJoin(decks, eq(decks.id, deckAccess.deckId))
		.where(eq(deckAccess.userId, userId));
}

/** Throws `DeckAccessError` if the deck doesn't exist or the user can't read it. */
export async function getDeck(userId: string, deckId: string, client: DbClient = db) {
	const { role } = await requireDeckAccess(client, userId, deckId, 'read');
	const [deck] = await client.select().from(decks).where(eq(decks.id, deckId));
	if (!deck) throw new Error(`deck ${deckId} vanished after access check`);
	return { deck, role };
}

export interface UpdateDeckInput {
	name?: string;
	description?: string;
	visibility?: Visibility;
	preset?: unknown;
}

export async function updateDeck(
	userId: string,
	deckId: string,
	patch: UpdateDeckInput,
	client: DbClient = db
) {
	await requireDeckAccess(client, userId, deckId, 'write');
	// A unique violation here can only be the rename colliding with another of the user's decks.
	const [deck] = await rethrowDuplicateName('deck', patch.name ?? '', () =>
		client
			.update(decks)
			.set({ ...patch, modifiedAt: new Date() })
			.where(eq(decks.id, deckId))
			.returning()
	);
	return deck ?? null;
}

/** Only the owner may delete a deck. */
export async function deleteDeck(userId: string, deckId: string, client: DbClient = db) {
	await requireDeckAccess(client, userId, deckId, 'owner');
	await client.delete(decks).where(eq(decks.id, deckId));
}

/** Membership lookup used by tests and by the (future) sharing UI. Not access-gated itself —
 *  callers must already know they're allowed to see the deck's member list. */
export async function getDeckAccessRow(userId: string, deckId: string, client: DbClient = db) {
	const [row] = await client
		.select()
		.from(deckAccess)
		.where(and(eq(deckAccess.deckId, deckId), eq(deckAccess.userId, userId)));
	return row ?? null;
}
