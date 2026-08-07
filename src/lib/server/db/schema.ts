/**
 * Drizzle schema — the source of truth for the data model.
 *
 * Rationale and the DDL sketch this implements: `docs/schema.md`. Keep the two in sync in
 * the same PR.
 *
 * The invariant this file exists to protect (CLAUDE.md §2.1): **scheduling state never lives
 * on the `cards` row.** It lives in `user_card_state`, primary-keyed `(user_id, card_id)`.
 */
import {
	pgTable,
	pgEnum,
	uuid,
	text,
	boolean,
	smallint,
	integer,
	bigint,
	doublePrecision,
	jsonb,
	timestamp,
	date,
	index,
	uniqueIndex,
	unique,
	check,
	primaryKey,
	type AnyPgColumn
} from 'drizzle-orm/pg-core';
import { sql } from 'drizzle-orm';
import { uuidv7 } from '$lib/uuid';

/** UUIDv7 primary key, generated in TS so the client can mint ids before they reach us. */
const id = () => uuid('id').primaryKey().$defaultFn(uuidv7);
const createdAt = () => timestamp('created_at', { withTimezone: true }).notNull().defaultNow();
const modifiedAt = () => timestamp('modified_at', { withTimezone: true }).notNull().defaultNow();

/** Anki's original numeric id, kept only for round-trip export fidelity. Never a key. */
const ankiId = (name = 'anki_id') => bigint(name, { mode: 'number' });

export const deckVisibility = pgEnum('deck_visibility', ['private', 'public']);
export const deckRole = pgEnum('deck_role', ['owner', 'editor', 'viewer']);

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

export const users = pgTable(
	'users',
	{
		id: id(),
		email: text('email').notNull(),
		displayName: text('display_name').notNull(),
		/** argon2id hash (`@node-rs/argon2`). Never the plaintext, never a weaker algorithm. */
		passwordHash: text('password_hash').notNull(),
		/** IANA zone name, e.g. `Europe/London`. Drives the day boundary (see `day-boundary.ts`). */
		timezone: text('timezone').notNull().default('UTC'),
		/** Local hour at which the study day rolls over. 04:00 by default, never midnight UTC. */
		dayStartHour: smallint('day_start_hour').notNull().default(4),
		createdAt: createdAt()
	},
	(t) => [
		// Case-insensitive: `A@x.com` and `a@x.com` are one account. The stored value keeps its
		// original casing; only uniqueness is folded.
		uniqueIndex('users_email_lower_idx').on(sql`lower(${t.email})`),
		// `day-boundary.ts` feeds this straight into `make_interval` with no validation, so an
		// out-of-range value would silently shift every queue query for that user.
		check('users_day_start_hour_range', sql`${t.dayStartHour} between 0 and 23`)
	]
);

/**
 * A logged-in session. `id` is the SHA-256 hash (hex) of the random token issued in the
 * session cookie — the raw token never touches the database, so a read of this table (a
 * backup, a compromised replica) discloses nothing usable. See `src/lib/server/auth/`.
 */
export const sessions = pgTable(
	'sessions',
	{
		id: text('id').primaryKey(),
		userId: uuid('user_id')
			.notNull()
			.references(() => users.id, { onDelete: 'cascade' }),
		expiresAt: timestamp('expires_at', { withTimezone: true }).notNull(),
		createdAt: createdAt()
	},
	(t) => [index('sessions_user_idx').on(t.userId)]
);

// ---------------------------------------------------------------------------
// Content — shared, one copy per deck. No scheduling state anywhere below.
// ---------------------------------------------------------------------------

export const decks = pgTable('decks', {
	id: id(),
	ownerId: uuid('owner_id')
		.notNull()
		.references(() => users.id, { onDelete: 'restrict' }),
	name: text('name').notNull(),
	description: text('description').notNull().default(''),
	visibility: deckVisibility('visibility').notNull().default('private'),
	forkedFromDeckId: uuid('forked_from_deck_id').references((): AnyPgColumn => decks.id, {
		onDelete: 'set null'
	}),
	/** Deck options (new/day limits, learning steps, …). Shape owned by the app, not the DB. */
	preset: jsonb('preset'),
	createdAt: createdAt(),
	modifiedAt: modifiedAt()
});

export const deckAccess = pgTable(
	'deck_access',
	{
		deckId: uuid('deck_id')
			.notNull()
			.references(() => decks.id, { onDelete: 'cascade' }),
		userId: uuid('user_id')
			.notNull()
			.references(() => users.id, { onDelete: 'cascade' }),
		role: deckRole('role').notNull(),
		createdAt: createdAt()
	},
	(t) => [primaryKey({ columns: [t.deckId, t.userId] }), index('deck_access_user_idx').on(t.userId)]
);

export const noteTypes = pgTable('note_types', {
	id: id(),
	ownerId: uuid('owner_id')
		.notNull()
		.references(() => users.id, { onDelete: 'restrict' }),
	name: text('name').notNull(),
	css: text('css').notNull().default(''),
	isCloze: boolean('is_cloze').notNull().default(false),
	sortFieldIdx: smallint('sort_field_idx').notNull().default(0),
	ankiId: ankiId()
});

export const fields = pgTable(
	'fields',
	{
		id: id(),
		noteTypeId: uuid('note_type_id')
			.notNull()
			.references(() => noteTypes.id, { onDelete: 'cascade' }),
		/** Index into `notes.fields`. */
		ordinal: smallint('ordinal').notNull(),
		name: text('name').notNull(),
		font: text('font').notNull().default('Arial'),
		size: smallint('size').notNull().default(20),
		isRtl: boolean('is_rtl').notNull().default(false),
		sticky: boolean('sticky').notNull().default(false)
	},
	(t) => [uniqueIndex('fields_note_type_ordinal_idx').on(t.noteTypeId, t.ordinal)]
);

export const templates = pgTable(
	'templates',
	{
		id: id(),
		noteTypeId: uuid('note_type_id')
			.notNull()
			.references(() => noteTypes.id, { onDelete: 'cascade' }),
		ordinal: smallint('ordinal').notNull(),
		name: text('name').notNull(),
		qfmt: text('qfmt').notNull(),
		afmt: text('afmt').notNull(),
		browserQfmt: text('browser_qfmt'),
		browserAfmt: text('browser_afmt')
	},
	(t) => [uniqueIndex('templates_note_type_ordinal_idx').on(t.noteTypeId, t.ordinal)]
);

export const notes = pgTable(
	'notes',
	{
		id: id(),
		/** Anki's stable per-note identifier. The idempotency key for import. */
		guid: text('guid').notNull(),
		noteTypeId: uuid('note_type_id')
			.notNull()
			.references(() => noteTypes.id, { onDelete: 'restrict' }),
		deckId: uuid('deck_id')
			.notNull()
			.references(() => decks.id, { onDelete: 'cascade' }),
		/** Ordered array of field values, indexed by `fields.ordinal`. */
		fields: jsonb('fields').$type<string[]>().notNull(),
		tags: text('tags')
			.array()
			.notNull()
			.default(sql`'{}'::text[]`),
		/** Anki's field checksum, preserved for export fidelity. */
		checksum: bigint('checksum', { mode: 'number' }),
		createdAt: createdAt(),
		modifiedAt: modifiedAt(),
		ankiId: ankiId()
	},
	// This is what makes re-import idempotent. Retrofitting it duplicates every early user's
	// decks on their next import (CLAUDE.md §2.2).
	(t) => [
		uniqueIndex('notes_deck_guid_idx').on(t.deckId, t.guid),
		// Backs the RESTRICT check when a note type is deleted.
		index('notes_note_type_idx').on(t.noteTypeId)
	]
);

/**
 * Content addressing ONLY: which note, rendered through which template.
 * No `due`, no `ivl`, no `factor`, no `state` — those live in `user_card_state`.
 */
export const cards = pgTable(
	'cards',
	{
		id: id(),
		noteId: uuid('note_id')
			.notNull()
			.references(() => notes.id, { onDelete: 'cascade' }),
		templateId: uuid('template_id')
			.notNull()
			.references(() => templates.id, { onDelete: 'restrict' }),
		/** Template ordinal, or cloze ordinal for cloze note types. */
		ordinal: smallint('ordinal').notNull(),
		deckId: uuid('deck_id')
			.notNull()
			.references(() => decks.id, { onDelete: 'cascade' }),
		ankiId: ankiId()
	},
	(t) => [
		uniqueIndex('cards_note_ordinal_idx').on(t.noteId, t.ordinal),
		index('cards_deck_idx').on(t.deckId),
		// Backs the RESTRICT check when a template is deleted.
		index('cards_template_idx').on(t.templateId)
	]
);

// ---------------------------------------------------------------------------
// Per-user state
// ---------------------------------------------------------------------------

/**
 * The scheduling state of one card for one user. Keyed `(user_id, card_id)` — this is the
 * table every multiuser feature is a consequence of (CLAUDE.md §2.1).
 *
 * `stability` and `difficulty` are `double precision`, not `real`: `ts-fsrs` rounds them to
 * 8 decimal places, and `real` (~7 significant digits) silently truncates that. A client and
 * server disagreeing about intervals is the worst failure mode this system has.
 */
export const userCardState = pgTable(
	'user_card_state',
	{
		userId: uuid('user_id')
			.notNull()
			.references(() => users.id, { onDelete: 'cascade' }),
		cardId: uuid('card_id')
			.notNull()
			.references(() => cards.id, { onDelete: 'cascade' }),
		due: timestamp('due', { withTimezone: true }).notNull(),
		stability: doublePrecision('stability').notNull().default(0),
		difficulty: doublePrecision('difficulty').notNull().default(0),
		/** `ts-fsrs` State: 0 new, 1 learning, 2 review, 3 relearning. */
		state: smallint('state').notNull().default(0),
		reps: integer('reps').notNull().default(0),
		lapses: integer('lapses').notNull().default(0),
		elapsedDays: integer('elapsed_days').notNull().default(0),
		scheduledDays: integer('scheduled_days').notNull().default(0),
		/**
		 * `ts-fsrs` `Card.learning_steps` — how far through the (re)learning steps the card is.
		 * FSRS-6 short-term scheduling reads it, so it has to survive a reload or a replay;
		 * dropping it silently changes a card's next interval.
		 */
		learningSteps: smallint('learning_steps').notNull().default(0),
		/** Guards the write queue's last-write-wins-by-review-time update (CLAUDE.md §6). */
		lastReview: timestamp('last_review', { withTimezone: true }),
		suspended: boolean('suspended').notNull().default(false),
		buriedUntil: date('buried_until'),
		flag: smallint('flag').notNull().default(0)
	},
	(t) => [
		primaryKey({ columns: [t.userId, t.cardId] }),
		// The queue query.
		index('user_card_state_due_idx')
			.on(t.userId, t.due)
			.where(sql`NOT ${t.suspended}`),
		index('user_card_state_card_idx').on(t.cardId),
		check('user_card_state_state_range', sql`${t.state} between 0 and 3`)
	]
);

/**
 * Append-only. This is the optimiser's training set, not an audit trail — no `DELETE` path
 * without a written decision (CLAUDE.md §2.5). Hence `onDelete: 'restrict'` on both foreign
 * keys: deleting a card or a user that has reviews has to be a deliberate act.
 *
 * `id` is client-generated (UUIDv7) so `ON CONFLICT (id) DO NOTHING` makes retries free.
 * The `*_before` columns and `fsrs_version` are what keep old rows replayable after a
 * parameter refit or an algorithm upgrade.
 */
export const reviewLog = pgTable(
	'review_log',
	{
		id: uuid('id').primaryKey(),
		userId: uuid('user_id')
			.notNull()
			.references(() => users.id, { onDelete: 'restrict' }),
		cardId: uuid('card_id')
			.notNull()
			.references(() => cards.id, { onDelete: 'restrict' }),
		/** `ts-fsrs` Rating: 1 again, 2 hard, 3 good, 4 easy. */
		rating: smallint('rating').notNull(),
		reviewedAt: timestamp('reviewed_at', { withTimezone: true }).notNull(),
		durationMs: integer('duration_ms'),
		stateBefore: smallint('state_before').notNull(),
		learningStepsBefore: smallint('learning_steps_before').notNull().default(0),
		// Nullable: reviews imported from an Anki collection with no FSRS history have no
		// known memory state. Rows produced by our own reviewer always set them.
		stabilityBefore: doublePrecision('stability_before'),
		difficultyBefore: doublePrecision('difficulty_before'),
		elapsedDaysBefore: integer('elapsed_days_before').notNull(),
		scheduledDaysAfter: integer('scheduled_days_after').notNull(),
		/** `ts-fsrs` major version the row was scheduled with. Null for imported history. */
		fsrsVersion: smallint('fsrs_version'),
		/** 0 learn, 1 review, 2 relearn, 3 filtered, 4 manual — Anki's revlog `type`. */
		reviewKind: smallint('review_kind').notNull()
	},
	(t) => [
		index('review_log_user_reviewed_idx').on(t.userId, t.reviewedAt),
		// `card_id` leads deliberately: Postgres's RESTRICT check on a card or deck delete runs
		// `SELECT 1 FROM review_log WHERE card_id = $1`, and only a leading `card_id` keeps that
		// off a seq scan of the largest table in the system. The trailing columns also serve the
		// per-card replay path (§6), which reads `WHERE card_id = ? AND user_id = ?
		// ORDER BY reviewed_at`.
		index('review_log_card_user_reviewed_idx').on(t.cardId, t.userId, t.reviewedAt),
		check('review_log_rating_range', sql`${t.rating} between 1 and 4`),
		check('review_log_state_before_range', sql`${t.stateBefore} between 0 and 3`),
		check('review_log_review_kind_range', sql`${t.reviewKind} between 0 and 4`)
	]
);

/**
 * Per-user FSRS parameters, optionally per `(user, deck)`. `deck_id IS NULL` is the user's
 * global default.
 *
 * `params` is a JSON array plus an explicit `fsrs_version` integer, never fixed-width
 * columns — the parameter count changes upstream (17 in FSRS-4.5, 19 in FSRS-5, 21 in
 * FSRS-6). Never fit one parameter set across a cohort (CLAUDE.md §2.4).
 */
export const userFsrsParams = pgTable(
	'user_fsrs_params',
	{
		id: id(),
		userId: uuid('user_id')
			.notNull()
			.references(() => users.id, { onDelete: 'cascade' }),
		deckId: uuid('deck_id').references(() => decks.id, { onDelete: 'cascade' }),
		fsrsVersion: smallint('fsrs_version').notNull(),
		params: jsonb('params').$type<number[]>().notNull(),
		desiredRetention: doublePrecision('desired_retention').notNull().default(0.9),
		optimisedAt: timestamp('optimised_at', { withTimezone: true }),
		reviewCountAtFit: integer('review_count_at_fit')
	},
	(t) => [
		// NULL deck_id is the global default, so NULLs must collide rather than be distinct.
		unique('user_fsrs_params_user_deck_key').on(t.userId, t.deckId).nullsNotDistinct(),
		// Exclusive: 0 and 1 are both degenerate targets that produce nonsense intervals.
		check(
			'user_fsrs_params_desired_retention_range',
			sql`${t.desiredRetention} > 0 and ${t.desiredRetention} < 1`
		)
	]
);

// ---------------------------------------------------------------------------
// Media — content-addressed, deduplicated across all users
// ---------------------------------------------------------------------------

export const mediaBlobs = pgTable('media_blobs', {
	/** Lowercase hex SHA-256. Also the storage path of the bytes. */
	sha256: text('sha256').primaryKey(),
	sizeBytes: integer('size_bytes').notNull(),
	mime: text('mime').notNull(),
	createdAt: createdAt()
});

/** Anki references media by filename inside note fields; this is what rendering resolves. */
export const mediaRefs = pgTable(
	'media_refs',
	{
		deckId: uuid('deck_id')
			.notNull()
			.references(() => decks.id, { onDelete: 'cascade' }),
		filename: text('filename').notNull(),
		sha256: text('sha256')
			.notNull()
			.references(() => mediaBlobs.sha256, { onDelete: 'restrict' })
	},
	(t) => [
		primaryKey({ columns: [t.deckId, t.filename] }),
		index('media_refs_sha256_idx').on(t.sha256)
	]
);
