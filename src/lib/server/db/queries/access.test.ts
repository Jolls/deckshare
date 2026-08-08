/**
 * Table-driven access-control test (CLAUDE.md §10.4): for each (role, resource, operation),
 * assert allow/deny. Add a row here for every new endpoint from here on.
 *
 * Runs against a real, freshly migrated Postgres database — skipped when `DATABASE_URL` is
 * unset, matching `migrations.test.ts`.
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { drizzle, type PostgresJsDatabase } from 'drizzle-orm/postgres-js';
import { migrate } from 'drizzle-orm/postgres-js/migrator';
import postgres from 'postgres';
import * as schema from '../schema';
import { uuidv7 } from '$lib/uuid';
import { DeckAccessError } from './access';
import { getDeck, updateDeck, deleteDeck } from './decks';
import { createNote, getNote, listNotesForDeck, updateNote, deleteNote } from './notes';
import {
	createNoteType,
	getNoteType,
	updateNoteType,
	deleteNoteType,
	NoteTypeAccessError
} from './note-types';
import { listCardsForNote } from './cards';

const url = process.env.DATABASE_URL;
const testDbName = `enshu_test_access_${Date.now().toString(36)}`;
/** Not a real hash — these fixture users never authenticate, only NOT NULL matters. */
const TEST_PASSWORD_HASH = '$argon2id$v=19$m=19456,t=2,p=1$test$test';

let admin: postgres.Sql;
let client: postgres.Sql;
let db: PostgresJsDatabase<typeof schema>;

beforeAll(async () => {
	if (!url) return;
	admin = postgres(url, { max: 1 });
	await admin.unsafe(`CREATE DATABASE "${testDbName}"`);

	const testUrl = new URL(url);
	testUrl.pathname = `/${testDbName}`;
	client = postgres(testUrl.toString(), { max: 1 });
	db = drizzle(client, { schema });

	await migrate(db, { migrationsFolder: 'drizzle' });
}, 60_000);

afterAll(async () => {
	if (!url) return;
	await client?.end();
	await admin?.unsafe(`DROP DATABASE IF EXISTS "${testDbName}" WITH (FORCE)`);
	await admin?.end();
});

type Role = 'owner' | 'editor' | 'viewer' | 'outsider' | 'public-outsider';

interface Fixture {
	users: Record<Role, string>;
	privateDeckId: string;
	publicDeckId: string;
	publicNoteId: string;
	/** Owned solely by `outsider` — used to prove read access to *this* deck doesn't leak an
	 *  arbitrary noteId's cards from a different, private deck (the IDOR regression). */
	attackerDeckId: string;
	/** Owned by `owner`. */
	noteTypeId: string;
	/** Owned by `editor` — note types are owned per-user, not shared by deck (`note-types.ts`),
	 *  so an editor collaborator creating a note needs their own note type, not the deck
	 *  owner's. */
	editorNoteTypeId: string;
}

async function buildFixture(): Promise<Fixture> {
	const users = {
		owner: uuidv7(),
		editor: uuidv7(),
		viewer: uuidv7(),
		outsider: uuidv7(),
		'public-outsider': uuidv7()
	} as Record<Role, string>;

	for (const [label, id] of Object.entries(users)) {
		await db.insert(schema.users).values({
			id,
			email: `${label}@access.test`,
			displayName: label,
			passwordHash: TEST_PASSWORD_HASH
		});
	}

	const noteTypeId = uuidv7();
	await db.insert(schema.noteTypes).values({ id: noteTypeId, ownerId: users.owner, name: 'Basic' });
	await db.insert(schema.templates).values({
		id: uuidv7(),
		noteTypeId,
		ordinal: 0,
		name: 'Card 1',
		qfmt: '{{Front}}',
		afmt: '{{Back}}'
	});

	const editorNoteTypeId = uuidv7();
	await db
		.insert(schema.noteTypes)
		.values({ id: editorNoteTypeId, ownerId: users.editor, name: "Editor's own" });
	await db.insert(schema.templates).values({
		id: uuidv7(),
		noteTypeId: editorNoteTypeId,
		ordinal: 0,
		name: 'Card 1',
		qfmt: '{{Front}}',
		afmt: '{{Back}}'
	});

	const privateDeckId = uuidv7();
	await db
		.insert(schema.decks)
		.values({ id: privateDeckId, ownerId: users.owner, name: 'private', visibility: 'private' });
	await db.insert(schema.deckAccess).values([
		{ deckId: privateDeckId, userId: users.owner, role: 'owner' },
		{ deckId: privateDeckId, userId: users.editor, role: 'editor' },
		{ deckId: privateDeckId, userId: users.viewer, role: 'viewer' }
	]);

	const publicDeckId = uuidv7();
	await db
		.insert(schema.decks)
		.values({ id: publicDeckId, ownerId: users.owner, name: 'public', visibility: 'public' });
	await db
		.insert(schema.deckAccess)
		.values({ deckId: publicDeckId, userId: users.owner, role: 'owner' });
	const [publicNote] = await db
		.insert(schema.notes)
		.values({
			guid: 'public-seed-note',
			noteTypeId,
			deckId: publicDeckId,
			fields: ['public front', 'public back']
		})
		.returning();
	if (!publicNote) throw new Error('public note insert returned no row');

	const attackerDeckId = uuidv7();
	await db
		.insert(schema.decks)
		.values({ id: attackerDeckId, ownerId: users.outsider, name: 'attacker-owned' });
	await db
		.insert(schema.deckAccess)
		.values({ deckId: attackerDeckId, userId: users.outsider, role: 'owner' });

	return {
		users,
		privateDeckId,
		publicDeckId,
		publicNoteId: publicNote.id,
		attackerDeckId,
		noteTypeId,
		editorNoteTypeId
	};
}

const ALLOW = 'allow';
const DENY = 'deny';
type Expectation = typeof ALLOW | typeof DENY;

// Thin wrappers binding every query call to the test's isolated, migrated database — the
// query modules default to the app's `db` singleton (pointing at `DATABASE_URL`'s primary
// database) when no client is passed, which is not this test's throwaway database.
const q = {
	getDeck: (u: string, d: string) => getDeck(u, d, db),
	updateDeck: (u: string, d: string, patch: Parameters<typeof updateDeck>[2]) =>
		updateDeck(u, d, patch, db),
	deleteDeck: (u: string, d: string) => deleteDeck(u, d, db),
	createNote: (u: string, d: string, input: Parameters<typeof createNote>[2]) =>
		createNote(u, d, input, db),
	listNotesForDeck: (u: string, d: string) => listNotesForDeck(u, d, db),
	getNote: (u: string, d: string, noteId: string) => getNote(u, d, noteId, db),
	updateNote: (u: string, d: string, noteId: string, patch: Parameters<typeof updateNote>[3]) =>
		updateNote(u, d, noteId, patch, db),
	deleteNote: (u: string, d: string, noteId: string) => deleteNote(u, d, noteId, db),
	listCardsForNote: (u: string, d: string, noteId: string) => listCardsForNote(u, d, noteId, db),
	createNoteType: (u: string, input: Parameters<typeof createNoteType>[1]) =>
		createNoteType(u, input, db),
	getNoteType: (u: string, id: string) => getNoteType(u, id, db),
	updateNoteType: (u: string, id: string, patch: Parameters<typeof updateNoteType>[2]) =>
		updateNoteType(u, id, patch, db),
	deleteNoteType: (u: string, id: string) => deleteNoteType(u, id, db)
};

async function expectOutcome(expectation: Expectation, run: () => Promise<unknown>) {
	if (expectation === ALLOW) {
		await expect(run()).resolves.not.toThrow();
	} else {
		await expect(run()).rejects.toBeInstanceOf(DeckAccessError);
	}
}

describe.skipIf(!url)('deck access control', () => {
	let fx: Fixture;
	let existingNoteId: string;

	beforeAll(async () => {
		if (!url) return;
		fx = await buildFixture();
		const { note } = await q.createNote(fx.users.owner, fx.privateDeckId, {
			noteTypeId: fx.noteTypeId,
			guid: 'seed-note',
			fields: ['front', 'back']
		});
		existingNoteId = note.id;
	});

	// One row per (role, resource, operation). `deckId` selects which fixture deck the
	// scenario runs against — the private deck for the owner/editor/viewer/outsider rows,
	// the public deck for the public-read carve-out.
	const cases: Array<{
		role: Role;
		resource: 'deck' | 'note';
		operation: string;
		expect: Expectation;
		deck: 'private' | 'public';
		run: (userId: string, deckId: string) => Promise<unknown>;
	}> = [
		// --- deck, private ---
		{
			role: 'owner',
			resource: 'deck',
			operation: 'read',
			expect: ALLOW,
			deck: 'private',
			run: q.getDeck
		},
		{
			role: 'editor',
			resource: 'deck',
			operation: 'read',
			expect: ALLOW,
			deck: 'private',
			run: q.getDeck
		},
		{
			role: 'viewer',
			resource: 'deck',
			operation: 'read',
			expect: ALLOW,
			deck: 'private',
			run: q.getDeck
		},
		{
			role: 'outsider',
			resource: 'deck',
			operation: 'read',
			expect: DENY,
			deck: 'private',
			run: q.getDeck
		},
		{
			role: 'owner',
			resource: 'deck',
			operation: 'update',
			expect: ALLOW,
			deck: 'private',
			run: (u, d) => q.updateDeck(u, d, { name: 'renamed-by-owner' })
		},
		{
			role: 'editor',
			resource: 'deck',
			operation: 'update',
			expect: ALLOW,
			deck: 'private',
			run: (u, d) => q.updateDeck(u, d, { name: 'renamed-by-editor' })
		},
		{
			role: 'viewer',
			resource: 'deck',
			operation: 'update',
			expect: DENY,
			deck: 'private',
			run: (u, d) => q.updateDeck(u, d, { name: 'nope' })
		},
		{
			role: 'outsider',
			resource: 'deck',
			operation: 'update',
			expect: DENY,
			deck: 'private',
			run: (u, d) => q.updateDeck(u, d, { name: 'nope' })
		},
		{
			role: 'editor',
			resource: 'deck',
			operation: 'delete',
			expect: DENY,
			deck: 'private',
			run: q.deleteDeck
		},
		{
			role: 'viewer',
			resource: 'deck',
			operation: 'delete',
			expect: DENY,
			deck: 'private',
			run: q.deleteDeck
		},
		{
			role: 'outsider',
			resource: 'deck',
			operation: 'delete',
			expect: DENY,
			deck: 'private',
			run: q.deleteDeck
		},

		// --- deck, public: content is readable by anyone, write still isn't ---
		{
			role: 'public-outsider',
			resource: 'deck',
			operation: 'read',
			expect: ALLOW,
			deck: 'public',
			run: q.getDeck
		},
		{
			role: 'public-outsider',
			resource: 'deck',
			operation: 'update',
			expect: DENY,
			deck: 'public',
			run: (u, d) => q.updateDeck(u, d, { name: 'nope' })
		},

		// --- note, public deck: content read is the same public carve-out as the deck itself ---
		{
			role: 'public-outsider',
			resource: 'note',
			operation: 'read (list)',
			expect: ALLOW,
			deck: 'public',
			run: (u, d) => q.listNotesForDeck(u, d)
		},
		{
			role: 'public-outsider',
			resource: 'note',
			operation: 'read (get)',
			expect: ALLOW,
			deck: 'public',
			run: (u, d) => q.getNote(u, d, fx.publicNoteId)
		},

		// --- note, private deck ---
		{
			role: 'owner',
			resource: 'note',
			operation: 'create',
			expect: ALLOW,
			deck: 'private',
			run: (u, d) =>
				q.createNote(u, d, { noteTypeId: fx.noteTypeId, guid: uuidv7(), fields: ['a', 'b'] })
		},
		{
			role: 'editor',
			resource: 'note',
			operation: 'create',
			expect: ALLOW,
			deck: 'private',
			// Uses the editor's own note type, not the deck owner's: note types are owned
			// per-user (`note-types.ts`), so deck write access alone doesn't grant use of a
			// note type someone else owns (see the IDOR-adjacent test below).
			run: (u, d) =>
				q.createNote(u, d, { noteTypeId: fx.editorNoteTypeId, guid: uuidv7(), fields: ['a', 'b'] })
		},
		{
			role: 'viewer',
			resource: 'note',
			operation: 'create',
			expect: DENY,
			deck: 'private',
			run: (u, d) =>
				q.createNote(u, d, { noteTypeId: fx.noteTypeId, guid: uuidv7(), fields: ['a', 'b'] })
		},
		{
			role: 'outsider',
			resource: 'note',
			operation: 'create',
			expect: DENY,
			deck: 'private',
			run: (u, d) =>
				q.createNote(u, d, { noteTypeId: fx.noteTypeId, guid: uuidv7(), fields: ['a', 'b'] })
		},
		{
			role: 'owner',
			resource: 'note',
			operation: 'read',
			expect: ALLOW,
			deck: 'private',
			run: (u, d) => q.listNotesForDeck(u, d)
		},
		{
			role: 'viewer',
			resource: 'note',
			operation: 'read',
			expect: ALLOW,
			deck: 'private',
			run: (u, d) => q.listNotesForDeck(u, d)
		},
		{
			role: 'outsider',
			resource: 'note',
			operation: 'read',
			expect: DENY,
			deck: 'private',
			run: (u, d) => q.listNotesForDeck(u, d)
		},
		{
			role: 'owner',
			resource: 'note',
			operation: 'update',
			expect: ALLOW,
			deck: 'private',
			run: (u, d) => q.updateNote(u, d, existingNoteId, { fields: ['front2', 'back'] })
		},
		{
			role: 'viewer',
			resource: 'note',
			operation: 'update',
			expect: DENY,
			deck: 'private',
			run: (u, d) => q.updateNote(u, d, existingNoteId, { fields: ['nope', 'back'] })
		},
		{
			role: 'viewer',
			resource: 'note',
			operation: 'delete',
			expect: DENY,
			deck: 'private',
			run: (u, d) => q.deleteNote(u, d, existingNoteId)
		}
	];

	it.each(cases.map((c) => [`${c.role} / ${c.resource} / ${c.operation}`, c] as const))(
		'%s',
		async (_label, c) => {
			const userId = fx.users[c.role];
			const deckId = c.deck === 'private' ? fx.privateDeckId : fx.publicDeckId;
			await expectOutcome(c.expect, () => c.run(userId, deckId));
		}
	);

	it('owner / deck / delete: allow (on a disposable deck, not the shared fixture)', async () => {
		const deckId = uuidv7();
		await db
			.insert(schema.decks)
			.values({ id: deckId, ownerId: fx.users.owner, name: 'disposable' });
		await db.insert(schema.deckAccess).values({ deckId, userId: fx.users.owner, role: 'owner' });
		await expect(q.deleteDeck(fx.users.owner, deckId)).resolves.not.toThrow();
	});

	it('generates one card per template for a standard note type', async () => {
		const { cards } = await q.createNote(fx.users.owner, fx.privateDeckId, {
			noteTypeId: fx.noteTypeId,
			guid: uuidv7(),
			fields: ['front', 'back']
		});
		expect(cards).toHaveLength(1);
		expect(cards[0]?.ordinal).toBe(0);
	});

	// Regression for the IDOR the reviewer caught: `listCardsForNote` checked access to
	// `deckId` but fetched cards by `noteId` alone, so a caller with legitimate read access to
	// *any* deck could pass a guessed/arbitrary `noteId` belonging to a different, private deck
	// they have no access to, and get that deck's cards back.
	it("does not leak a different private deck's cards via a guessed noteId", async () => {
		// `outsider` legitimately owns `attackerDeckId` — the access check on it passes — but
		// `existingNoteId` belongs to `privateDeckId`, which `outsider` cannot read.
		const leaked = await q.listCardsForNote(fx.users.outsider, fx.attackerDeckId, existingNoteId);
		expect(leaked).toEqual([]);
	});

	// Low-severity gap the reviewer also flagged: write access to a deck must not imply the
	// right to attach a note to *any* note type — only ones the caller owns.
	it('refuses to create a note against a note type the caller does not own', async () => {
		const otherOwnerId = uuidv7();
		await db.insert(schema.users).values({
			id: otherOwnerId,
			email: 'other-note-type-owner@access.test',
			displayName: 'x',
			passwordHash: TEST_PASSWORD_HASH
		});
		const otherNoteType = await q.createNoteType(otherOwnerId, {
			name: 'Not yours',
			fields: [{ name: 'Front' }],
			templates: [{ name: 'Card 1', qfmt: '{{Front}}', afmt: '{{Front}}' }]
		});

		// `fx.users.owner` has full write access to `privateDeckId` — that must not be enough.
		await expect(
			q.createNote(fx.users.owner, fx.privateDeckId, {
				noteTypeId: otherNoteType.id,
				guid: uuidv7(),
				fields: ['x']
			})
		).rejects.toBeInstanceOf(NoteTypeAccessError);
	});
});

// note_types have no deck_access join — access is plain ownership (see note-types.ts). A
// separate table because the (role, resource, operation) shape doesn't apply: there's no
// editor/viewer for a note type, only "owner" and "not owner".
describe.skipIf(!url)('note-type access control (ownership, not deck_access)', () => {
	let ownerId: string;
	let otherId: string;
	let noteTypeId: string;

	beforeAll(async () => {
		if (!url) return;
		ownerId = uuidv7();
		otherId = uuidv7();
		await db.insert(schema.users).values([
			{
				id: ownerId,
				email: 'nt-owner@access.test',
				displayName: 'nt-owner',
				passwordHash: TEST_PASSWORD_HASH
			},
			{
				id: otherId,
				email: 'nt-other@access.test',
				displayName: 'nt-other',
				passwordHash: TEST_PASSWORD_HASH
			}
		]);
		const created = await q.createNoteType(ownerId, {
			name: 'Basic',
			fields: [{ name: 'Front' }, { name: 'Back' }],
			templates: [{ name: 'Card 1', qfmt: '{{Front}}', afmt: '{{Back}}' }]
		});
		noteTypeId = created.id;
	});

	it.each([
		['owner', 'read', true],
		['other', 'read', false],
		['owner', 'update', true],
		['other', 'update', false]
	] as const)('%s / note-type / %s -> %s', async (who, op, allowed) => {
		const userId = who === 'owner' ? ownerId : otherId;
		const result =
			op === 'read'
				? await q.getNoteType(userId, noteTypeId)
				: await q.updateNoteType(userId, noteTypeId, { name: 'renamed' });
		if (allowed) expect(result).not.toBeNull();
		else expect(result).toBeNull();
	});

	it('other cannot delete a note type they do not own', async () => {
		expect(await q.deleteNoteType(otherId, noteTypeId)).toBe(false);
	});

	it('owner can delete their own note type', async () => {
		const disposable = await q.createNoteType(ownerId, {
			name: 'Disposable',
			fields: [{ name: 'Front' }],
			templates: [{ name: 'Card 1', qfmt: '{{Front}}', afmt: '{{Front}}' }]
		});
		expect(await q.deleteNoteType(ownerId, disposable.id)).toBe(true);
	});
});
