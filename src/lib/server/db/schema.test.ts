/**
 * Structural assertions on the schema. These need no database — they guard the invariants in
 * CLAUDE.md §2 against an innocent-looking edit.
 */
import { describe, it, expect } from 'vitest';
import { getTableConfig } from 'drizzle-orm/pg-core';
import {
	cards,
	decks,
	noteTypes,
	notes,
	userCardState,
	reviewLog,
	userFsrsParams,
	users,
	sessions
} from './schema';

const columnNames = (table: Parameters<typeof getTableConfig>[0]) =>
	getTableConfig(table).columns.map((c) => c.name);

/** Index columns are typed `SQL | IndexedColumn`; only the latter carries a name. */
const indexColumnNames = (columns: readonly unknown[]) =>
	columns.map((c) => (c && typeof c === 'object' && 'name' in c ? c.name : undefined));

const uniqueIndexColumns = (table: Parameters<typeof getTableConfig>[0]) =>
	getTableConfig(table)
		.indexes.map((i) => i.config)
		.filter((c) => c.unique)
		.map((c) => indexColumnNames(c.columns));

describe('cards', () => {
	// CLAUDE.md §2.1: scheduling state never lives on the cards row. Adopting Anki's schema
	// and bolting multiuser on afterwards is a rewrite, and it starts with one of these
	// columns looking convenient.
	it('carries no scheduling state', () => {
		const forbidden = [
			'due',
			'ivl',
			'interval',
			'factor',
			'ease_factor',
			'state',
			'queue',
			'type',
			'reps',
			'lapses',
			'stability',
			'difficulty',
			'elapsed_days',
			'scheduled_days',
			'last_review',
			'suspended',
			'buried_until',
			'flag',
			'left',
			'odue',
			'odid'
		];
		expect(columnNames(cards).filter((n) => forbidden.includes(n))).toEqual([]);
	});

	it('is content addressing only', () => {
		expect(columnNames(cards).sort()).toEqual([
			'anki_id',
			'deck_id',
			'id',
			'note_id',
			'ordinal',
			'template_id'
		]);
	});
});

describe('notes', () => {
	// Owner-scoped, not deck-scoped: `guid` is globally unique in Anki, and keying it to the
	// deck would make note idempotency depend on deck dedup landing first.
	it('has a unique index on (owner_id, guid) — the import idempotency key', () => {
		expect(uniqueIndexColumns(notes)).toContainEqual(['owner_id', 'guid']);
	});

	// `notes_owner_guid_idx` subsumes the old `(deck_id, guid)` unique for uniqueness, but not
	// for lookups: without this, every deck-scoped note query and the deck-delete RI check
	// seq-scans.
	it('still indexes deck_id on its own', () => {
		const idx = getTableConfig(notes).indexes.find((i) => i.config.name === 'notes_deck_idx');
		expect(indexColumnNames(idx?.config.columns ?? [])).toEqual(['deck_id']);
	});

	it('keeps anki_id for export fidelity', () => {
		expect(columnNames(notes)).toContain('anki_id');
	});
});

describe('re-import dedup keys', () => {
	it('dedups decks on (owner_id, name), the way Anki imports do', () => {
		expect(uniqueIndexColumns(decks)).toContainEqual(['owner_id', 'name']);
	});

	it('dedups note types on (owner_id, name)', () => {
		expect(uniqueIndexColumns(noteTypes)).toContainEqual(['owner_id', 'name']);
	});

	// Anki deck id 1 is `Default` in every collection ever made, and note-type ids are creation
	// timestamps unique only within one profile — deduping on either merges unrelated content,
	// and `notes.fields` is positional, so a merged note type renders fields into wrong slots.
	it.each([
		['decks', decks],
		['note_types', noteTypes]
	])('never makes %s.anki_id unique — Anki ids are per-collection', (_label, table) => {
		expect(uniqueIndexColumns(table).flat()).not.toContain('anki_id');
	});

	// `revlog.id` is the exception: epoch-millis, genuinely identifying within a collection.
	// `user_id` leads because cards are shared content but review_log is per-user training
	// data, so a card-scoped key would let one user's imported history block another's (§2.5).
	it('dedups reviews on (user_id, card_id, anki_id)', () => {
		expect(uniqueIndexColumns(reviewLog)).toContainEqual(['user_id', 'card_id', 'anki_id']);
	});
});

describe('user_card_state', () => {
	it('is primary-keyed (user_id, card_id)', () => {
		const pk = getTableConfig(userCardState).primaryKeys[0];
		expect(pk?.columns.map((c) => c.name)).toEqual(['user_id', 'card_id']);
	});

	it('has the queue index (user_id, due) WHERE NOT suspended', () => {
		const queue = getTableConfig(userCardState).indexes.find(
			(i) => i.config.name === 'user_card_state_due_idx'
		)?.config;
		expect(indexColumnNames(queue?.columns ?? [])).toEqual(['user_id', 'due']);
		expect(queue?.where).toBeDefined();
	});

	it('persists learning_steps, which FSRS-6 short-term scheduling reads', () => {
		expect(columnNames(userCardState)).toContain('learning_steps');
	});

	it('stores memory state at double precision, not real', () => {
		const cols = getTableConfig(userCardState).columns;
		const types = ['stability', 'difficulty'].map((n) =>
			cols.find((c) => c.name === n)?.getSQLType()
		);
		// `ts-fsrs` rounds to 8 decimal places; float4 holds ~7 significant digits, so `real`
		// would make client and server disagree about intervals — silently.
		expect(types).toEqual(['double precision', 'double precision']);
	});
});

describe('review_log', () => {
	it('keeps the *_before values and fsrs_version that make old rows replayable', () => {
		expect(columnNames(reviewLog)).toEqual(
			expect.arrayContaining([
				'state_before',
				'learning_steps_before',
				'stability_before',
				'difficulty_before',
				'elapsed_days_before',
				'scheduled_days_after',
				'fsrs_version'
			])
		);
	});

	// The `ON DELETE RESTRICT` behaviour that protects it (§2.5) is asserted against a real
	// database in `migrations.test.ts` — reading it back off the builder here would pass even
	// if the generated SQL had lost the constraint.
});

describe('user_fsrs_params', () => {
	it('stores params as JSON alongside an explicit fsrs_version', () => {
		const cols = getTableConfig(userFsrsParams).columns;
		expect(cols.find((c) => c.name === 'params')?.getSQLType()).toBe('jsonb');
		expect(cols.find((c) => c.name === 'fsrs_version')?.getSQLType()).toBe('smallint');
	});

	it('treats a NULL deck_id as a value in the uniqueness constraint', () => {
		const uq = getTableConfig(userFsrsParams).uniqueConstraints[0];
		expect(uq?.columns.map((c) => c.name)).toEqual(['user_id', 'deck_id']);
		expect(uq?.nullsNotDistinct).toBe(true);
	});
});

describe('users', () => {
	it('drives the day boundary from timezone + day_start_hour, defaulting to 04:00', () => {
		const cols = getTableConfig(users).columns;
		expect(cols.find((c) => c.name === 'timezone')?.notNull).toBe(true);
		expect(cols.find((c) => c.name === 'day_start_hour')?.default).toBe(4);
	});

	it('requires a password hash', () => {
		const cols = getTableConfig(users).columns;
		expect(cols.find((c) => c.name === 'password_hash')?.notNull).toBe(true);
	});
});

describe('sessions', () => {
	// CLAUDE.md §12: the session id is the SHA-256 hash of the cookie token, never the token
	// itself — a leaked table read must not be a leaked, replayable session.
	it('keys on a text id, not a database-generated uuid', () => {
		const idCol = getTableConfig(sessions).columns.find((c) => c.name === 'id');
		expect(idCol?.getSQLType()).toBe('text');
		expect(idCol?.primary).toBe(true);
	});

	it("is indexed by user, so all of a user's sessions can be invalidated", () => {
		const idx = getTableConfig(sessions).indexes.find((i) => i.config.name === 'sessions_user_idx');
		expect(indexColumnNames(idx?.config.columns ?? [])).toEqual(['user_id']);
	});
});
