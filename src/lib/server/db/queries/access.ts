/**
 * Deck access control (CLAUDE.md §9, §5 "Access control at the query layer").
 *
 * Every query touching a deck resolves the caller's role here first. Route guards alone are
 * not sufficient — a shared deck means "readable by some users" is the normal case, not the
 * exception. `deck_access` is the only source of truth for who may read or write a deck's
 * content, with one carve-out: `decks.visibility = 'public'` grants read of *content* to
 * anyone, but never read of another user's `user_card_state`, and never write.
 */
import { and, eq } from 'drizzle-orm';
import { decks, deckAccess } from '../schema';
import type { DbClient } from './types';

export type DeckRole = 'owner' | 'editor' | 'viewer';

const WRITE_ROLES: ReadonlySet<DeckRole> = new Set(['owner', 'editor']);

/** Thrown by `requireDeckAccess`. Route handlers map this to 404 (unknown) or 403 (denied). */
export class DeckAccessError extends Error {
	constructor(
		public readonly deckId: string,
		public readonly reason: 'not_found' | 'forbidden'
	) {
		super(`deck ${deckId}: ${reason}`);
		this.name = 'DeckAccessError';
	}
}

/**
 * The caller's effective role on a deck, or `null` if the deck doesn't exist or they have no
 * access to it. A public deck with no explicit `deck_access` row resolves to an implicit
 * `viewer` — read of content only, never a basis for write.
 */
export async function getDeckRole(
	db: DbClient,
	userId: string,
	deckId: string
): Promise<DeckRole | null> {
	const [row] = await db
		.select({ role: deckAccess.role, visibility: decks.visibility })
		.from(decks)
		.leftJoin(deckAccess, and(eq(deckAccess.deckId, decks.id), eq(deckAccess.userId, userId)))
		.where(eq(decks.id, deckId));

	if (!row) return null;
	if (row.role) return row.role;
	if (row.visibility === 'public') return 'viewer';
	return null;
}

export function canRead(role: DeckRole | null): role is DeckRole {
	return role !== null;
}

export function canWrite(role: DeckRole | null): boolean {
	return role !== null && WRITE_ROLES.has(role);
}

export function isOwner(role: DeckRole | null): boolean {
	return role === 'owner';
}

/**
 * Resolves the role and throws `DeckAccessError` if it doesn't meet `min`. Centralises the
 * not-found-vs-forbidden distinction so every query and route makes the same call: a deck
 * that exists but the user can't see should look like 404, not leak existence via a 403.
 */
export async function requireDeckAccess(
	db: DbClient,
	userId: string,
	deckId: string,
	min: 'read' | 'write' | 'owner'
): Promise<DeckRole> {
	const role = await getDeckRole(db, userId, deckId);
	if (role === null) throw new DeckAccessError(deckId, 'not_found');

	const satisfied =
		min === 'read' ? canRead(role) : min === 'write' ? canWrite(role) : isOwner(role);
	if (!satisfied) throw new DeckAccessError(deckId, 'forbidden');

	return role;
}
