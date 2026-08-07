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
import * as schema from './schema';
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
	await db
		.insert(schema.users)
		.values({ id: ids.user, email: `${label}@example.test`, displayName: label });
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

describe.skipIf(!url)('notes (deck_id, guid)', () => {
	let ids: Awaited<ReturnType<typeof fixture>>;

	beforeAll(async () => {
		if (!url) return;
		ids = await fixture('guid');
	});

	const note = (deckId: string, guid: string, fields: string[]) => ({
		guid,
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

	it('rejects a bare duplicate (deck_id, guid) insert', async () => {
		await db.insert(schema.notes).values(note(ids.deck, 'dup-guid', ['a']));
		// Not `onConflict` — this is what proves the unique index actually covers exactly
		// (deck_id, guid). A unique index on (deck_id, guid, id) would let this through.
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
					target: [schema.notes.deckId, schema.notes.guid],
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

	it('scopes the guid to the deck, so the same note in two decks is two rows', async () => {
		const otherDeck = uuidv7();
		await db.insert(schema.decks).values({ id: otherDeck, ownerId: ids.user, name: 'Other' });
		await db.insert(schema.notes).values(note(otherDeck, 'dup-guid', ['elsewhere']));

		expect(await countIn(otherDeck)).toBe(1);
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
				dayStartHour: hour
			})
		);
	});

	it('accepts every valid rollover hour', async () => {
		for (const hour of [0, 4, 23]) {
			await db.insert(schema.users).values({
				email: `ok-hour${hour}@example.test`,
				displayName: 'x',
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
		await db.insert(schema.users).values({ email: 'Case@Example.test', displayName: 'a' });
		await rejectsWith(UNIQUE_VIOLATION, () =>
			db.insert(schema.users).values({ email: 'case@example.TEST', displayName: 'b' })
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
