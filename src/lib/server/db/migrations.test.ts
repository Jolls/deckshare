/**
 * Applies the committed migrations to a genuinely fresh database, then asserts the behaviour
 * they produce — against the real server, not against Drizzle's builder config, so a
 * hand-edited migration that dropped a constraint would fail here.
 *
 * Skipped when `DATABASE_URL` is unset; CI always has one.
 */
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { drizzle, type PostgresJsDatabase } from 'drizzle-orm/postgres-js';
import { migrate } from 'drizzle-orm/postgres-js/migrator';
import postgres from 'postgres';
import { eq, sql } from 'drizzle-orm';
import { readFileSync } from 'node:fs';
import * as schema from './schema';
import { DuplicateNameError, rethrowDuplicateName } from './queries/errors';
import { uuidv7 } from '$lib/uuid';

const url = process.env.DATABASE_URL;
const testDbName = `enshu_test_${Date.now().toString(36)}`;

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

/** Migration tags in application order, straight from the journal drizzle-kit maintains. */
const migrationTags = () =>
	(
		JSON.parse(readFileSync('drizzle/meta/_journal.json', 'utf8')) as { entries: { tag: string }[] }
	).entries.map((e) => e.tag);

/** Applies one migration file statement by statement, the way drizzle's migrator does. */
async function applyMigration(client: postgres.Sql, tag: string) {
	for (const statement of readFileSync(`drizzle/${tag}.sql`, 'utf8').split(
		'--> statement-breakpoint'
	)) {
		const trimmed = statement.trim();
		if (trimmed) await client.unsafe(trimmed);
	}
}

/** Postgres SQLSTATEs the assertions below distinguish between. */
const UNIQUE_VIOLATION = '23505';
const FK_VIOLATION = '23503';
const CHECK_VIOLATION = '23514';

/** Drizzle wraps driver failures in a `DrizzleQueryError`; the SQLSTATE is on the cause. */
const rejectsWith = (code: string, run: () => Promise<unknown>) =>
	expect(run()).rejects.toMatchObject({ cause: { code } });

const tableNames = () =>
	db
		.execute<{ table_name: string }>(
			sql`select table_name from information_schema.tables
			    where table_schema = 'public' and table_type = 'BASE TABLE'`
		)
		.then((rows) => rows.map((r) => r.table_name).sort());

/** A user + deck + note type + template, enough to hang notes and cards off. */
async function fixture(label: string) {
	const ids = {
		user: uuidv7(),
		deck: uuidv7(),
		noteType: uuidv7(),
		template: uuidv7()
	};
	await db.insert(schema.users).values({
		id: ids.user,
		email: `${label}@example.test`,
		displayName: label,
		passwordHash: 'test-hash'
	});
	await db.insert(schema.decks).values({ id: ids.deck, ownerId: ids.user, name: label });
	await db.insert(schema.noteTypes).values({ id: ids.noteType, ownerId: ids.user, name: 'Basic' });
	await db.insert(schema.templates).values({
		id: ids.template,
		noteTypeId: ids.noteType,
		ordinal: 0,
		name: 'Card 1',
		qfmt: '{{Front}}',
		afmt: '{{Back}}'
	});
	return ids;
}

describe.skipIf(!url)('migrations on a fresh database', () => {
	it('creates every table in the data model and nothing else', async () => {
		expect(await tableNames()).toEqual([
			'cards',
			'deck_access',
			'decks',
			'fields',
			'media_blobs',
			'media_refs',
			'note_types',
			'notes',
			'review_log',
			'sessions',
			'templates',
			'user_card_state',
			'user_fsrs_params',
			'users'
		]);
	});

	it('drops the scaffold placeholder table', async () => {
		expect(await tableNames()).not.toContain('task');
	});

	it('leaves the cards table with no scheduling columns', async () => {
		const rows = await db.execute<{ column_name: string }>(
			sql`select column_name from information_schema.columns
			    where table_schema = 'public' and table_name = 'cards'`
		);
		expect(rows.map((r) => r.column_name).sort()).toEqual([
			'anki_id',
			'deck_id',
			'id',
			'note_id',
			'ordinal',
			'template_id'
		]);
	});

	it('creates the queue index as a partial index on (user_id, due)', async () => {
		const rows = await db.execute<{ indexdef: string }>(
			sql`select indexdef from pg_indexes
			    where tablename = 'user_card_state' and indexname = 'user_card_state_due_idx'`
		);
		expect(rows[0]?.indexdef).toMatch(/\(user_id, due\)\s+WHERE \(NOT suspended\)/);
	});

	it('indexes review_log by card so the RESTRICT check never seq-scans it', async () => {
		const rows = await db.execute<{ indexdef: string }>(
			sql`select indexdef from pg_indexes
			    where tablename = 'review_log' and indexname = 'review_log_card_user_reviewed_idx'`
		);
		// Leading column has to be card_id: the RI check is `WHERE card_id = $1`.
		expect(rows[0]?.indexdef).toMatch(/\(card_id, user_id, reviewed_at\)/);
	});
});

/**
 * `migrations on a fresh database` can't catch a bad backfill: it always starts empty, so an
 * `ADD COLUMN ... NOT NULL` with no default passes there and fails on every database that has
 * rows. 0004 adds `notes.owner_id`, so it adds the column nullable, fills it from the note's
 * deck, and only then constrains it — this applies it to a database that already has notes.
 */
describe.skipIf(!url)('0004 against a populated database', () => {
	const MIGRATION = '0004_cooing_caretaker';
	const populatedDbName = `${testDbName}_populated`;
	let populated: postgres.Sql;
	const ownerId = uuidv7();

	beforeAll(async () => {
		if (!url) return;
		await admin.unsafe(`CREATE DATABASE "${populatedDbName}"`);
		const populatedUrl = new URL(url);
		populatedUrl.pathname = `/${populatedDbName}`;
		populated = postgres(populatedUrl.toString(), { max: 1 });

		const tags = migrationTags();
		for (const tag of tags.slice(0, tags.indexOf(MIGRATION))) {
			await applyMigration(populated, tag);
		}

		const deckId = uuidv7();
		const noteTypeId = uuidv7();
		await populated`insert into users (id, email, display_name, password_hash)
		                values (${ownerId}, 'backfill@example.test', 'backfill', 'test-hash')`;
		await populated`insert into decks (id, owner_id, name)
		                values (${deckId}, ${ownerId}, 'Pre-existing')`;
		await populated`insert into note_types (id, owner_id, name)
		                values (${noteTypeId}, ${ownerId}, 'Basic')`;
		await populated`insert into notes (id, guid, note_type_id, deck_id, fields)
		                values (${uuidv7()}, 'pre-existing-note', ${noteTypeId}, ${deckId},
		                        '["front","back"]'::jsonb)`;

		await applyMigration(populated, MIGRATION);
	}, 60_000);

	afterAll(async () => {
		if (!url) return;
		await populated?.end();
		await admin?.unsafe(`DROP DATABASE IF EXISTS "${populatedDbName}" WITH (FORCE)`);
	});

	it("backfills owner_id from each note's deck", async () => {
		const rows = await populated<{ owner_id: string }[]>`select owner_id from notes`;
		expect(rows.map((r) => r.owner_id)).toEqual([ownerId]);
	});

	it('leaves the column NOT NULL, so later inserts must supply it', async () => {
		const rows = await populated<{ is_nullable: string }[]>`
			select is_nullable from information_schema.columns
			where table_name = 'notes' and column_name = 'owner_id'`;
		expect(rows[0]?.is_nullable).toBe('NO');
	});
});

describe.skipIf(!url)('notes (owner_id, guid)', () => {
	let ids: Awaited<ReturnType<typeof fixture>>;

	beforeAll(async () => {
		if (!url) return;
		ids = await fixture('guid');
	});

	const note = (deckId: string, guid: string, fields: string[]) => ({
		guid,
		ownerId: ids.user,
		noteTypeId: ids.noteType,
		deckId,
		fields
	});

	const countIn = (deckId: string) =>
		db
			.select()
			.from(schema.notes)
			.where(eq(schema.notes.deckId, deckId))
			.then((r) => r.length);

	it('rejects a bare duplicate (owner_id, guid) insert', async () => {
		await db.insert(schema.notes).values(note(ids.deck, 'dup-guid', ['a']));
		// Not `onConflict` — this is what proves the unique index actually covers exactly
		// (owner_id, guid). A unique index on (owner_id, guid, id) would let this through.
		await rejectsWith(UNIQUE_VIOLATION, () =>
			db.insert(schema.notes).values(note(ids.deck, 'dup-guid', ['b']))
		);
	});

	it('updates rather than duplicating on re-import', async () => {
		const upsert = (fields: string[]) =>
			db
				.insert(schema.notes)
				.values(note(ids.deck, 'reimport-guid', fields))
				.onConflictDoUpdate({
					target: [schema.notes.ownerId, schema.notes.guid],
					set: { fields }
				})
				.returning({ id: schema.notes.id });

		const [first] = await upsert(['front v1', 'back v1']);
		const [second] = await upsert(['front v2', 'back v2']);

		expect(second?.id).toBe(first?.id);
		const rows = await db.select().from(schema.notes).where(eq(schema.notes.guid, 'reimport-guid'));
		expect(rows).toHaveLength(1);
		expect(rows[0]?.fields).toEqual(['front v2', 'back v2']);
	});

	// The §2.2 amendment: one guid is one note per *owner*, not per deck. Re-importing the same
	// collection into a second deck must find the existing note, not fork its identity.
	it('rejects the same guid in a second deck of the same owner', async () => {
		const otherDeck = uuidv7();
		await db.insert(schema.decks).values({ id: otherDeck, ownerId: ids.user, name: 'Other' });

		await rejectsWith(UNIQUE_VIOLATION, () =>
			db.insert(schema.notes).values(note(otherDeck, 'dup-guid', ['elsewhere']))
		);
		expect(await countIn(otherDeck)).toBe(0);
	});

	it('scopes the guid to the owner, so two users can hold the same note', async () => {
		const other = await fixture('guid-other');
		await db.insert(schema.notes).values({
			guid: 'dup-guid',
			ownerId: other.user,
			noteTypeId: other.noteType,
			deckId: other.deck,
			fields: ['theirs']
		});

		expect(await countIn(other.deck)).toBe(1);
	});
});

/**
 * The re-import dedup keys. Decks and note types key on *name*, the way Anki's own importer
 * does — `anki_id` is per-collection, so deck id 1 is `Default` everywhere and deduping on it
 * merges unrelated collections. Only `revlog.id` is unique enough to key on.
 */
describe.skipIf(!url)('re-import dedup keys', () => {
	let ids: Awaited<ReturnType<typeof fixture>>;
	const cardId = uuidv7();
	/** `revlog.id` is epoch-millis and identifies the row within its collection. */
	const REVLOG_ANKI_ID = 1_700_000_000_003;

	beforeAll(async () => {
		if (!url) return;
		ids = await fixture('dedup');
		const noteId = uuidv7();
		await db.insert(schema.notes).values({
			id: noteId,
			guid: 'dedup-note',
			ownerId: ids.user,
			noteTypeId: ids.noteType,
			deckId: ids.deck,
			fields: ['front', 'back']
		});
		await db.insert(schema.cards).values({
			id: cardId,
			noteId,
			templateId: ids.template,
			ordinal: 0,
			deckId: ids.deck
		});
	});

	const review = (ankiId: number | null) => ({
		id: uuidv7(),
		userId: ids.user,
		cardId,
		rating: 3,
		reviewedAt: new Date('2026-08-01T10:00:00Z'),
		stateBefore: 0,
		elapsedDaysBefore: 0,
		scheduledDaysAfter: 1,
		reviewKind: 0,
		ankiId
	});

	it('rejects a second deck with the same (owner_id, name)', async () => {
		await db.insert(schema.decks).values({ ownerId: ids.user, name: 'Kanji' });
		await rejectsWith(UNIQUE_VIOLATION, () =>
			db.insert(schema.decks).values({ ownerId: ids.user, name: 'Kanji' })
		);
	});

	// Pins the driver error shape `rethrowDuplicateName` matches on against a real violation —
	// if Drizzle stops nesting the SQLSTATE under `.cause`, the routes silently go back to 500.
	it('raises DuplicateNameError, not a bare driver error, through the query layer', async () => {
		await expect(
			rethrowDuplicateName('deck', 'Kanji', () =>
				db.insert(schema.decks).values({ ownerId: ids.user, name: 'Kanji' })
			)
		).rejects.toBeInstanceOf(DuplicateNameError);
	});

	// A 409 saying "you already have a deck called X" for a conflict that had nothing to do
	// with the name would be a lie the client can't act on, and would bury the real error.
	it('leaves a unique violation on a different constraint alone', async () => {
		await expect(
			rethrowDuplicateName('deck', 'Kanji', () =>
				db.insert(schema.notes).values({
					guid: 'dedup-note', // already taken by this owner
					ownerId: ids.user,
					noteTypeId: ids.noteType,
					deckId: ids.deck,
					fields: ['a', 'b']
				})
			)
		).rejects.not.toBeInstanceOf(DuplicateNameError);
	});

	it('rejects a second note type with the same (owner_id, name)', async () => {
		await db.insert(schema.noteTypes).values({ ownerId: ids.user, name: 'Vocab' });
		await rejectsWith(UNIQUE_VIOLATION, () =>
			db.insert(schema.noteTypes).values({ ownerId: ids.user, name: 'Vocab' })
		);
	});

	it('rejects a second review with the same (user_id, card_id, anki_id)', async () => {
		await db.insert(schema.reviewLog).values(review(REVLOG_ANKI_ID));
		await rejectsWith(UNIQUE_VIOLATION, () =>
			db.insert(schema.reviewLog).values(review(REVLOG_ANKI_ID))
		);
	});

	it('scopes name dedup to the owner, so two users can hold a deck of one name', async () => {
		const other = await fixture('dedup-other');
		await db.insert(schema.decks).values({ ownerId: other.user, name: 'Kanji' });

		const rows = await db.execute<{ n: string }>(
			sql`select count(*) as n from decks where name = 'Kanji'`
		);
		expect(Number(rows[0]?.n)).toBe(2);
	});

	// The defect the amended spec exists to prevent: deck id 1 is `Default` in every collection
	// Anki has ever produced. Keying dedup on it would land both packages' notes in one deck —
	// and `notes (owner_id, guid)` would then reject the second package's notes outright.
	it('keeps two imports sharing an Anki deck id as two decks', async () => {
		const first = uuidv7();
		const second = uuidv7();
		await db
			.insert(schema.decks)
			.values({ id: first, ownerId: ids.user, name: 'From package A', ankiId: 1 });
		await db
			.insert(schema.decks)
			.values({ id: second, ownerId: ids.user, name: 'From package B', ankiId: 1 });

		for (const [deckId, guid] of [
			[first, 'package-a-note'],
			[second, 'package-b-note']
		] as const) {
			await db.insert(schema.notes).values({
				guid,
				ownerId: ids.user,
				noteTypeId: ids.noteType,
				deckId,
				fields: ['front', 'back']
			});
		}

		const rows = await db.execute<{ deck_id: string }>(
			sql`select deck_id from notes where guid in ('package-a-note', 'package-b-note')`
		);
		expect(new Set(rows.map((r) => r.deck_id)).size).toBe(2);
	});

	// The live write-queue path (`queries/review.ts`) never sets `anki_id`, so ordinary reviews
	// must collide on nothing. NULLs are distinct in the index for exactly this reason.
	it('leaves NULL anki_id reviews free to coexist', async () => {
		await db.insert(schema.reviewLog).values(review(null));
		await db.insert(schema.reviewLog).values(review(null));

		const rows = await db.execute<{ n: string }>(
			sql`select count(*) as n from review_log where card_id = ${cardId} and anki_id is null`
		);
		expect(Number(rows[0]?.n)).toBe(2);
	});
});

describe.skipIf(!url)('review_log is protected from cascading deletes', () => {
	let ids: Awaited<ReturnType<typeof fixture>>;
	const cardId = uuidv7();

	beforeAll(async () => {
		if (!url) return;
		ids = await fixture('revlog');
		const noteId = uuidv7();
		await db.insert(schema.notes).values({
			id: noteId,
			guid: 'revlog-note',
			ownerId: ids.user,
			noteTypeId: ids.noteType,
			deckId: ids.deck,
			fields: ['front', 'back']
		});
		await db.insert(schema.cards).values({
			id: cardId,
			noteId,
			templateId: ids.template,
			ordinal: 0,
			deckId: ids.deck
		});
		await db.insert(schema.reviewLog).values({
			id: uuidv7(),
			userId: ids.user,
			cardId,
			rating: 3,
			reviewedAt: new Date('2026-08-01T10:00:00Z'),
			stateBefore: 0,
			elapsedDaysBefore: 0,
			scheduledDaysAfter: 1,
			reviewKind: 0
		});
	});

	// §2.5: it is training data, not an audit trail. Nothing may cascade it away.
	it('refuses to delete a card that has reviews', async () => {
		await rejectsWith(FK_VIOLATION, () =>
			db.delete(schema.cards).where(eq(schema.cards.id, cardId))
		);
	});

	it('refuses to delete a user that has reviews', async () => {
		await rejectsWith(FK_VIOLATION, () =>
			db.delete(schema.users).where(eq(schema.users.id, ids.user))
		);
	});

	it('refuses to delete a deck, because its cards would cascade away underneath', async () => {
		await rejectsWith(FK_VIOLATION, () =>
			db.delete(schema.decks).where(eq(schema.decks.id, ids.deck))
		);
	});

	it('accepts the same review id twice as a no-op, for write-queue retries', async () => {
		const event = {
			id: uuidv7(),
			userId: ids.user,
			cardId,
			rating: 2,
			reviewedAt: new Date('2026-08-02T10:00:00Z'),
			stateBefore: 1,
			elapsedDaysBefore: 1,
			scheduledDaysAfter: 3,
			reviewKind: 1
		};
		await db.insert(schema.reviewLog).values(event).onConflictDoNothing();
		await db.insert(schema.reviewLog).values(event).onConflictDoNothing();

		const rows = await db.select().from(schema.reviewLog).where(eq(schema.reviewLog.id, event.id));
		expect(rows).toHaveLength(1);
	});
});

describe.skipIf(!url)('value constraints', () => {
	let ids: Awaited<ReturnType<typeof fixture>>;

	beforeAll(async () => {
		if (!url) return;
		ids = await fixture('checks');
	});

	it.each([-1, 24, 99])('rejects day_start_hour %i', async (hour) => {
		// day-boundary.ts feeds this straight into make_interval with no validation.
		await rejectsWith(CHECK_VIOLATION, () =>
			db.insert(schema.users).values({
				email: `hour${hour}@example.test`,
				displayName: 'x',
				passwordHash: 'test-hash',
				dayStartHour: hour
			})
		);
	});

	it('accepts every valid rollover hour', async () => {
		for (const hour of [0, 4, 23]) {
			await db.insert(schema.users).values({
				email: `ok-hour${hour}@example.test`,
				displayName: 'x',
				passwordHash: 'test-hash',
				dayStartHour: hour
			});
		}
		const rows = await db.execute<{ n: string }>(
			sql`select count(*) as n from users where email like 'ok-hour%'`
		);
		expect(Number(rows[0]?.n)).toBe(3);
	});

	const review = (over: Partial<typeof schema.reviewLog.$inferInsert>) => ({
		id: uuidv7(),
		userId: ids.user,
		cardId: uuidv7(),
		rating: 3,
		reviewedAt: new Date(),
		stateBefore: 0,
		elapsedDaysBefore: 0,
		scheduledDaysAfter: 1,
		reviewKind: 0,
		...over
	});

	it.each([
		['rating', { rating: 0 }],
		['rating', { rating: 5 }],
		['state_before', { stateBefore: 4 }],
		['review_kind', { reviewKind: 5 }]
	])('rejects an out-of-range review_log %s', async (_label, over) => {
		// A CHECK is evaluated before the FK, so the bogus card_id above is irrelevant here.
		await rejectsWith(CHECK_VIOLATION, () => db.insert(schema.reviewLog).values(review(over)));
	});

	it.each([0, 1, 1.5, -0.1])('rejects desired_retention %s', async (retention) => {
		await rejectsWith(CHECK_VIOLATION, () =>
			db.insert(schema.userFsrsParams).values({
				userId: ids.user,
				fsrsVersion: 6,
				params: [0.4, 0.6],
				desiredRetention: retention
			})
		);
	});

	it('rejects an out-of-range user_card_state.state', async () => {
		await rejectsWith(CHECK_VIOLATION, () =>
			db.insert(schema.userCardState).values({
				userId: ids.user,
				cardId: uuidv7(),
				due: new Date(),
				state: 7
			})
		);
	});
});

describe.skipIf(!url)('users.email', () => {
	it('is unique case-insensitively', async () => {
		await db
			.insert(schema.users)
			.values({ email: 'Case@Example.test', displayName: 'a', passwordHash: 'test-hash' });
		await rejectsWith(UNIQUE_VIOLATION, () =>
			db
				.insert(schema.users)
				.values({ email: 'case@example.TEST', displayName: 'b', passwordHash: 'test-hash' })
		);
	});

	it('preserves the casing the user typed', async () => {
		const rows = await db
			.select({ email: schema.users.email })
			.from(schema.users)
			.where(sql`lower(${schema.users.email}) = 'case@example.test'`);
		expect(rows[0]?.email).toBe('Case@Example.test');
	});
});
