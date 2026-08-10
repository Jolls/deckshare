/**
 * Two properties, in priority order:
 *
 * - **CLAUDE.md §10.1, the top testing priority in the repo: the client cannot write
 *   scheduling state.** A grade whose `predicted` block claims a stability, difficulty or
 *   `due` other than what the server computes stores the *server's* value and raises a
 *   divergence — including when `predicted` is hostile rather than merely stale.
 * - **§10.4, send idempotency:** replaying a batch and reordering it converge to the same
 *   `user_card_state`, and a redelivered earlier batch never overwrites a later review.
 *
 * Runs against a real, freshly migrated Postgres database — skipped when `DATABASE_URL` is
 * unset, matching `migrations.test.ts` and `access.test.ts`.
 *
 * Events are produced by the *real* client path (`$lib/review`'s `grade`, which calls
 * `$lib/fsrs`), not by hand-written fixtures — except where a test needs a dishonest one.
 */
import { describe, it, expect, beforeAll, afterAll, vi } from 'vitest';
import { drizzle, type PostgresJsDatabase } from 'drizzle-orm/postgres-js';
import { migrate } from 'drizzle-orm/postgres-js/migrator';
import postgres from 'postgres';
import { and, eq } from 'drizzle-orm';
import * as schema from '../schema';
import { uuidv7 } from '$lib/uuid';
import {
	defaultFsrsParams,
	newCardState,
	Rating,
	scheduleReview,
	State,
	type CardState,
	type Grade,
	type ScheduleResult
} from '$lib/fsrs';
import { grade, startSession, toWireCardState } from '$lib/review';
import type { WireReviewEvent, WireReviewSession } from '$lib/review/types';
import { divergenceCount } from '$lib/server/fsrs/divergence';
import { DeckAccessError } from './access';
import { applyReviewBatch, getReviewSession, recomputeUserCardState, stateFromRow } from './review';

const url = process.env.DATABASE_URL;
const testDbName = `enshu_test_review_${Date.now().toString(36)}`;
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

interface Deck {
	userId: string;
	deckId: string;
	cardIds: string[];
}

/** A user with a deck of `cardCount` basic cards, and `deck_access` to match. */
async function makeDeck(label: string, cardCount = 2): Promise<Deck> {
	const userId = uuidv7();
	const deckId = uuidv7();
	const noteTypeId = uuidv7();
	const templateId = uuidv7();

	await db.insert(schema.users).values({
		id: userId,
		email: `${label}@review.test`,
		displayName: label,
		passwordHash: TEST_PASSWORD_HASH
	});
	await db.insert(schema.decks).values({ id: deckId, ownerId: userId, name: label });
	await db.insert(schema.deckAccess).values({ deckId, userId, role: 'owner' });
	await db.insert(schema.noteTypes).values({ id: noteTypeId, ownerId: userId, name: 'Basic' });
	await db.insert(schema.fields).values([
		{ noteTypeId, ordinal: 0, name: 'Front' },
		{ noteTypeId, ordinal: 1, name: 'Back' }
	]);
	await db.insert(schema.templates).values({
		id: templateId,
		noteTypeId,
		ordinal: 0,
		name: 'Card 1',
		qfmt: '{{Front}}',
		afmt: '{{FrontSide}}<hr>{{Back}}'
	});

	const cardIds: string[] = [];
	for (let i = 0; i < cardCount; i++) {
		const noteId = uuidv7();
		const cardId = uuidv7();
		await db.insert(schema.notes).values({
			id: noteId,
			guid: `${label}-note-${i}`,
			ownerId: userId,
			noteTypeId,
			deckId,
			fields: [`front ${i}`, `back ${i}`]
		});
		await db.insert(schema.cards).values({ id: cardId, noteId, templateId, ordinal: 0, deckId });
		cardIds.push(cardId);
	}

	return { userId, deckId, cardIds };
}

const stateOf = (userId: string, cardId: string) =>
	db
		.select()
		.from(schema.userCardState)
		.where(and(eq(schema.userCardState.userId, userId), eq(schema.userCardState.cardId, cardId)))
		.then((rows) => rows[0] ?? null);

/** The instants `clientGrade` and `serverChain` both grade at: 10 minutes apart. */
const at = (start: Date, i: number) => new Date(start.getTime() + i * 600_000);

/**
 * Grades one card `ratings.length` times through the real client path and returns the events
 * the write queue would be holding.
 *
 * Each grade runs through a one-card session seeded with the previous grade's prediction,
 * which is exactly the threading the reviewer does in memory. Deterministic: fuzz is off, so
 * the same ratings at the same instants always produce the same states — that is what lets the
 * scenarios below compare separate decks against each other.
 */
function clientGrade(cardId: string, ratings: Grade[], start: Date): WireReviewEvent[] {
	let state = toWireCardState(newCardState(start));
	const events: WireReviewEvent[] = [];

	ratings.forEach((rating, i) => {
		const payload: WireReviewSession = {
			deckId: 'deck',
			params: defaultFsrsParams(),
			// Ends the study day immediately so the card is never requeued: this helper produces
			// one event per rating, and the requeue rule is `session.test.ts`'s business.
			studyDayEnd: start.toISOString(),
			cards: [{ cardId, noteId: 'note', ordinal: 0, front: 'f', back: 'b', state }]
		};
		const result = grade(startSession(payload), rating, at(start, i), 1000);
		if (!result) throw new Error('ran out of cards');
		events.push(result.event);
		state = result.event.predicted.state;
	});

	return events;
}

/**
 * The same sequence scheduled the way the *server* schedules it: from a new card, forward
 * through `$lib/fsrs`, with no client input at all. This is the authority the stored rows are
 * checked against — deriving the expectation from the events would only prove the server
 * copied them, which is the thing §2.7 forbids.
 */
function serverChain(ratings: Grade[], start: Date): ScheduleResult[] {
	const params = defaultFsrsParams();
	let state: CardState = newCardState(start);
	return ratings.map((rating, i) => {
		const result = scheduleReview(state, rating, at(start, i), params);
		state = result.state;
		return result;
	});
}

/**
 * The scheduling columns, as the wire shape. Routed through the production `stateFromRow` +
 * `toWireCardState` rather than listed here, so a column added to `user_card_state` joins
 * every assertion below instead of quietly dropping out of all of them.
 */
const scheduling = (row: Awaited<ReturnType<typeof stateOf>>) =>
	row && toWireCardState(stateFromRow(row));

const START = new Date('2026-08-08T09:00:00.000Z');

/** The rating sequence every idempotency scenario replays. */
const RATINGS: Grade[] = [Rating.Good, Rating.Again, Rating.Good];

describe.skipIf(!url)('the client cannot write scheduling state (CLAUDE.md §10.1, §2.7)', () => {
	/** A `predicted` block that is not stale but a lie: every field moved somewhere flattering. */
	function tamper(event: WireReviewEvent): WireReviewEvent {
		return {
			...event,
			predicted: {
				fsrsVersion: 4,
				state: {
					...event.predicted.state,
					due: '2030-01-01T00:00:00.000Z',
					stability: 9_999,
					difficulty: 1,
					state: State.Review,
					reps: 500,
					lapses: 0
				}
			}
		};
	}

	it('stores the server’s state and raises a divergence for a hostile prediction', async () => {
		const honest = await makeDeck('hostile-honest', 1);
		const hostile = await makeDeck('hostile', 1);
		const honestCard = honest.cardIds[0];
		const hostileCard = hostile.cardIds[0];
		if (!honestCard || !hostileCard) throw new Error('no card');

		await applyReviewBatch(honest.userId, clientGrade(honestCard, RATINGS, START), db);
		const truth = scheduling(await stateOf(honest.userId, honestCard));

		const before = divergenceCount();
		const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
		let lines: string[];
		try {
			await applyReviewBatch(
				hostile.userId,
				clientGrade(hostileCard, RATINGS, START).map(tamper),
				db
			);
			lines = warn.mock.calls.map((call) => String(call[0]));
		} finally {
			warn.mockRestore();
		}

		// Byte for byte the honest result: nothing the client claimed reached a column.
		expect(scheduling(await stateOf(hostile.userId, hostileCard))).toEqual(truth);
		// One divergence per tampered event, each naming both fsrs_versions.
		expect(divergenceCount()).toBe(before + RATINGS.length);
		expect(lines).toHaveLength(RATINGS.length);
		expect(JSON.parse(lines[0] ?? '{}')).toMatchObject({
			event: 'fsrs_divergence',
			cardId: hostileCard,
			serverFsrsVersion: defaultFsrsParams().fsrsVersion,
			clientFsrsVersion: 4
		});
	});

	it('keeps review_log free of the hostile numbers too', async () => {
		const deck = await makeDeck('hostile-log', 1);
		const cardId = deck.cardIds[0];
		if (!cardId) throw new Error('no card');

		const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
		try {
			await applyReviewBatch(deck.userId, clientGrade(cardId, RATINGS, START).map(tamper), db);
		} finally {
			warn.mockRestore();
		}

		const rows = await db
			.select()
			.from(schema.reviewLog)
			.where(eq(schema.reviewLog.cardId, cardId))
			.orderBy(schema.reviewLog.reviewedAt);

		const expected = serverChain(RATINGS, START);
		expect(rows).toHaveLength(RATINGS.length);
		for (const [i, row] of rows.entries()) {
			const log = expected[i]?.log;
			if (!log) throw new Error('missing expectation');
			expect(row.stateBefore).toBe(log.stateBefore);
			expect(row.stabilityBefore).toBe(log.stabilityBefore);
			expect(row.difficultyBefore).toBe(log.difficultyBefore);
			expect(row.learningStepsBefore).toBe(log.learningStepsBefore);
			expect(row.elapsedDaysBefore).toBe(log.elapsedDaysBefore);
			expect(row.scheduledDaysAfter).toBe(log.scheduledDaysAfter);
			// The client's `fsrs_version` is diagnostic only — the stored one is the server's.
			expect(row.fsrsVersion).toBe(defaultFsrsParams().fsrsVersion);
		}
	});

	it('an honest client raises no divergence at all', async () => {
		const deck = await makeDeck('honest', 1);
		const cardId = deck.cardIds[0];
		if (!cardId) throw new Error('no card');

		const before = divergenceCount();
		await applyReviewBatch(deck.userId, clientGrade(cardId, RATINGS, START), db);
		// Never firing is the point (CLAUDE.md §17): if this ever fails, client and server
		// have stopped scheduling identically and every user is seeing wrong predictions.
		expect(divergenceCount()).toBe(before);
	});

	it('responds with the server’s state so the client can reconcile', async () => {
		const deck = await makeDeck('reconcile', 1);
		const cardId = deck.cardIds[0];
		if (!cardId) throw new Error('no card');

		const events = clientGrade(cardId, [Rating.Good], START);
		const { applied, results } = await applyReviewBatch(deck.userId, events, db);

		expect(applied).toBe(1);
		expect(results).toHaveLength(1);
		expect(results[0]).toMatchObject({ id: events[0]?.id, cardId });
		expect(results[0]?.state).toEqual(toWireCardState(serverChain([Rating.Good], START)[0]!.state));
	});
});

describe.skipIf(!url)('write-queue idempotency (CLAUDE.md §10.4)', () => {
	/**
	 * A fresh user, deck, card, and the three events the client would have queued for grading
	 * it. Every scenario gets its own — event ids are the idempotency key, so reusing one
	 * scenario's ids in another would make the second scenario's `review_log` writes no-ops
	 * and quietly stop testing anything. The *states* are still directly comparable across
	 * scenarios because the ratings and instants are fixed and fuzz is off.
	 */
	async function scenario(label: string) {
		const deck = await makeDeck(label, 1);
		const cardId = deck.cardIds[0];
		if (!cardId) throw new Error('no card');
		return { deck, cardId, events: clientGrade(cardId, RATINGS, START) };
	}

	const logCount = (cardId: string) =>
		db
			.select()
			.from(schema.reviewLog)
			.where(eq(schema.reviewLog.cardId, cardId))
			.then((rows) => rows.length);

	/**
	 * The baseline: the batch applied once, in order. Every other scenario must land here.
	 */
	async function expectedState(label: string) {
		const { deck, cardId, events } = await scenario(`baseline-${label}`);
		await applyReviewBatch(deck.userId, events, db);
		return scheduling(await stateOf(deck.userId, cardId));
	}

	it('applies a batch in order (the baseline every other case is compared against)', async () => {
		const { deck, cardId, events } = await scenario('in-order');
		await applyReviewBatch(deck.userId, events, db);

		const row = await stateOf(deck.userId, cardId);
		// Three reviews of one card, and the newest by review time is the one that survives.
		expect(row?.reps).toBe(3);
		expect(row?.lastReview?.toISOString()).toBe(events[2]?.reviewedAt);
		expect(await logCount(cardId)).toBe(3);
	});

	it('REPLAY: applying the same batch twice is a no-op', async () => {
		const { deck, cardId, events } = await scenario('replay');

		await applyReviewBatch(deck.userId, events, db);
		const once = scheduling(await stateOf(deck.userId, cardId));

		await applyReviewBatch(deck.userId, events, db);

		expect(scheduling(await stateOf(deck.userId, cardId))).toEqual(once);
		// review_log is append-only training data (§2.5) — the retry must not duplicate rows.
		expect(await logCount(cardId)).toBe(3);
	});

	it('REPLAY: a batch containing the same event twice is accepted once', async () => {
		const { deck, cardId, events } = await scenario('replay-inline');
		const first = events[0];
		if (!first) throw new Error('no event');

		await applyReviewBatch(deck.userId, [...events, first], db);

		expect(await logCount(cardId)).toBe(3);
		expect((await stateOf(deck.userId, cardId))?.reps).toBe(3);
	});

	it('REORDER: every permutation converges on the ordered result', async () => {
		const expected = await expectedState('reorder');

		// `applyReviewBatch` sorts by review time before scheduling, so a shuffled batch is
		// applied in the order the reviews actually happened. That sort is load-bearing now
		// that the server threads state forward itself rather than reading it off the events.
		const permutations = [
			[2, 1, 0],
			[1, 0, 2],
			[2, 0, 1],
			[0, 2, 1],
			[1, 2, 0]
		];
		for (const [n, order] of permutations.entries()) {
			const { deck, cardId, events } = await scenario(`reorder-${n}`);
			const shuffled = order.map((i) => {
				const e = events[i];
				if (!e) throw new Error('bad permutation');
				return e;
			});
			await applyReviewBatch(deck.userId, shuffled, db);

			expect(scheduling(await stateOf(deck.userId, cardId))).toEqual(expected);
			expect(await logCount(cardId)).toBe(3);
		}
	});

	it('INTERLEAVE: a redelivered earlier batch never overwrites a later review', async () => {
		const expected = await expectedState('interleave');
		const { deck, cardId, events } = await scenario('interleave');
		const [e0, e1, e2] = events;
		if (!e0 || !e1 || !e2) throw new Error('missing events');

		// The realistic failure: a batch is acknowledged, its response is lost, the client
		// keeps retrying it, and the retries land after later reviews have been accepted.
		await applyReviewBatch(deck.userId, [e0, e1], db);
		await applyReviewBatch(deck.userId, [e2], db);
		await applyReviewBatch(deck.userId, [e0, e1], db);
		await applyReviewBatch(deck.userId, [e1, e2, e0], db);

		expect(scheduling(await stateOf(deck.userId, cardId))).toEqual(expected);
		expect(await logCount(cardId)).toBe(3);
	});

	/**
	 * The case that forces the replay path. Scheduling server-side makes each grade depend on
	 * the one before it, so an event arriving *ahead* of its predecessor cannot be folded onto
	 * the stored row — the server would have to invent a `before` that never existed, and a
	 * fabricated `*_before` in `review_log` is training data no later recompute repairs (§2.5).
	 * Rebuilding the card's history instead is what keeps this convergent.
	 */
	it('REORDER: split into per-event batches, delivered backwards', async () => {
		const expected = await expectedState('split');
		const { deck, cardId, events } = await scenario('split-backwards');

		for (const e of [...events].reverse()) {
			await applyReviewBatch(deck.userId, [e], db);
		}

		expect(scheduling(await stateOf(deck.userId, cardId))).toEqual(expected);
		expect(await logCount(cardId)).toBe(3);
	});

	/**
	 * What backwards delivery must leave behind: a `review_log` whose *primary* columns — the
	 * answer and its instant — are complete and correct, so a replay reproduces the state.
	 * Those are what an optimiser fits against (§2.5).
	 *
	 * The derived `*_before` columns are a different matter, and this is the honest limit: a
	 * row written *before* the server had any way to know a review was missing records the
	 * state it genuinely held at that moment, and nothing retro-corrects it — `review_log` is
	 * append-only (§17). Rows written once the disorder is visible are correct, which is the
	 * part that stops wrong data accumulating.
	 */
	it('REORDER: backwards delivery leaves a review_log that replays to the same state', async () => {
		const expected = await expectedState('split-replay');
		const { deck, cardId, events } = await scenario('split-columns');
		for (const e of [...events].reverse()) {
			await applyReviewBatch(deck.userId, [e], db);
		}

		const rows = await db
			.select()
			.from(schema.reviewLog)
			.where(eq(schema.reviewLog.cardId, cardId))
			.orderBy(schema.reviewLog.reviewedAt);
		expect(rows.map((r) => [r.rating, r.reviewedAt.toISOString()])).toEqual(
			events.map((e) => [e.rating, e.reviewedAt])
		);

		await recomputeUserCardState(deck.userId, cardId, db);
		expect(scheduling(await stateOf(deck.userId, cardId))).toEqual(expected);
	});

	it('records the review_log columns the optimiser and the replay path need', async () => {
		const { deck, cardId, events } = await scenario('columns');
		await applyReviewBatch(deck.userId, events, db);

		const rows = await db
			.select()
			.from(schema.reviewLog)
			.where(eq(schema.reviewLog.cardId, cardId))
			.orderBy(schema.reviewLog.reviewedAt);

		expect(rows).toHaveLength(3);
		expect(rows[0]).toMatchObject({
			userId: deck.userId,
			rating: Rating.Good,
			stateBefore: State.New,
			reviewKind: 0,
			fsrsVersion: defaultFsrsParams().fsrsVersion,
			durationMs: 1000
		});
		// Every FSRS column comes off `ts-fsrs`' own review log on the server, not off the event.
		const expectedChain = serverChain(RATINGS, START);
		for (const [i, row] of rows.entries()) {
			const log = expectedChain[i]?.log;
			if (!log) throw new Error('missing expectation');
			expect(row.stateBefore).toBe(log.stateBefore);
			expect(row.stabilityBefore).toBe(log.stabilityBefore);
			expect(row.difficultyBefore).toBe(log.difficultyBefore);
			expect(row.learningStepsBefore).toBe(log.learningStepsBefore);
			expect(row.elapsedDaysBefore).toBe(log.elapsedDaysBefore);
			expect(row.scheduledDaysAfter).toBe(log.scheduledDaysAfter);
		}
	});
});

describe.skipIf(!url)('write-queue access control (CLAUDE.md §9)', () => {
	it('refuses reviews for a card in a deck the caller cannot read', async () => {
		const owner = await makeDeck('acl-owner', 1);
		const outsider = await makeDeck('acl-outsider', 1);
		const cardId = owner.cardIds[0];
		if (!cardId) throw new Error('no card');

		const events = clientGrade(cardId, [Rating.Good], START);
		await expect(applyReviewBatch(outsider.userId, events, db)).rejects.toBeInstanceOf(
			DeckAccessError
		);
		expect(await stateOf(outsider.userId, cardId)).toBeNull();
		expect(await stateOf(owner.userId, cardId)).toBeNull();
	});

	it('refuses an unknown card id without revealing whether it exists', async () => {
		const deck = await makeDeck('acl-unknown', 1);
		const events = clientGrade(uuidv7(), [Rating.Good], START);
		await expect(applyReviewBatch(deck.userId, events, db)).rejects.toMatchObject({
			reason: 'not_found'
		});
	});

	it('rolls the whole batch back if any event in it is unauthorised', async () => {
		const owner = await makeDeck('acl-atomic', 1);
		const other = await makeDeck('acl-atomic-other', 1);
		const mine = owner.cardIds[0];
		const theirs = other.cardIds[0];
		if (!mine || !theirs) throw new Error('no card');

		const events = [
			...clientGrade(mine, [Rating.Good], START),
			...clientGrade(theirs, [Rating.Good], START)
		];
		await expect(applyReviewBatch(owner.userId, events, db)).rejects.toBeInstanceOf(
			DeckAccessError
		);
		// Not even the authorised half landed.
		expect(await stateOf(owner.userId, mine)).toBeNull();
		expect(
			await db.select().from(schema.reviewLog).where(eq(schema.reviewLog.cardId, mine))
		).toHaveLength(0);
	});
});

describe.skipIf(!url)('server-side recompute path (CLAUDE.md §6, §17)', () => {
	it('replays review_log to exactly the state the live path wrote', async () => {
		const deck = await makeDeck('recompute', 1);
		const cardId = deck.cardIds[0];
		if (!cardId) throw new Error('no card');

		const events = clientGrade(
			cardId,
			[Rating.Good, Rating.Again, Rating.Hard, Rating.Easy],
			START
		);
		await applyReviewBatch(deck.userId, events, db);
		const fromLivePath = scheduling(await stateOf(deck.userId, cardId));

		// Corrupt the row the way a bad migration or a bulk repair would, then rebuild it from
		// the log alone. The live path and the bulk path must agree — they are the same fold.
		await db
			.update(schema.userCardState)
			.set({ stability: 999, difficulty: 1, reps: 0, state: State.New })
			.where(eq(schema.userCardState.cardId, cardId));

		const rebuilt = await recomputeUserCardState(deck.userId, cardId, db);
		expect(rebuilt).not.toBeNull();
		expect(scheduling(await stateOf(deck.userId, cardId))).toEqual(fromLivePath);
	});

	it('returns null for a card with no reviews', async () => {
		const deck = await makeDeck('recompute-empty', 1);
		const cardId = deck.cardIds[0];
		if (!cardId) throw new Error('no card');
		expect(await recomputeUserCardState(deck.userId, cardId, db)).toBeNull();
	});

	it('is access-controlled like every other deck query', async () => {
		const owner = await makeDeck('recompute-acl', 1);
		const outsider = await makeDeck('recompute-acl-out', 1);
		const cardId = owner.cardIds[0];
		if (!cardId) throw new Error('no card');
		await expect(recomputeUserCardState(outsider.userId, cardId, db)).rejects.toBeInstanceOf(
			DeckAccessError
		);
	});
});

describe.skipIf(!url)('session start (CLAUDE.md §6)', () => {
	it('returns every never-seen card as new, rendered and sanitised, in one call', async () => {
		const deck = await makeDeck('session', 3);
		const session = await getReviewSession(deck.userId, deck.deckId, 100, db);

		expect(session.cards).toHaveLength(3);
		expect(session.params).toEqual(defaultFsrsParams());
		expect(new Date(session.studyDayEnd).getTime()).toBeGreaterThan(Date.now());
		expect(session.cards.every((c) => c.state.state === State.New)).toBe(true);
		expect(session.cards[0]?.front).toBe('front 0');
		// {{FrontSide}} resolved, and the template's own markup survived sanitisation.
		expect(session.cards[0]?.back).toBe('front 0<hr />back 0');
	});

	it('drops a card scheduled past the end of the study day, and keeps one still due', async () => {
		const deck = await makeDeck('session-due', 2);
		const [dueCard, futureCard] = deck.cardIds;
		if (!dueCard || !futureCard) throw new Error('no cards');

		await db.insert(schema.userCardState).values([
			{
				userId: deck.userId,
				cardId: dueCard,
				due: new Date(Date.now() - 3_600_000),
				state: State.Review,
				reps: 1,
				lastReview: new Date(Date.now() - 86_400_000)
			},
			{
				userId: deck.userId,
				cardId: futureCard,
				due: new Date(Date.now() + 30 * 86_400_000),
				state: State.Review,
				reps: 1
			}
		]);

		const session = await getReviewSession(deck.userId, deck.deckId, 100, db);
		expect(session.cards.map((c) => c.cardId)).toEqual([dueCard]);
	});

	it('excludes suspended cards', async () => {
		const deck = await makeDeck('session-suspended', 1);
		const cardId = deck.cardIds[0];
		if (!cardId) throw new Error('no card');
		await db.insert(schema.userCardState).values({
			userId: deck.userId,
			cardId,
			due: new Date(Date.now() - 3_600_000),
			suspended: true
		});
		expect((await getReviewSession(deck.userId, deck.deckId, 100, db)).cards).toEqual([]);
	});

	it("never leaks another user's scheduling state", async () => {
		const owner = await makeDeck('session-shared', 1);
		const cardId = owner.cardIds[0];
		if (!cardId) throw new Error('no card');

		const viewerId = uuidv7();
		await db.insert(schema.users).values({
			id: viewerId,
			email: 'session-viewer@review.test',
			displayName: 'viewer',
			passwordHash: TEST_PASSWORD_HASH
		});
		await db
			.insert(schema.deckAccess)
			.values({ deckId: owner.deckId, userId: viewerId, role: 'viewer' });
		await db.insert(schema.userCardState).values({
			userId: owner.userId,
			cardId,
			due: new Date(Date.now() - 3_600_000),
			state: State.Review,
			reps: 7,
			stability: 42
		});

		const session = await getReviewSession(viewerId, owner.deckId, 100, db);
		// The viewer sees the shared *content* and their own (absent, hence new) state.
		expect(session.cards[0]?.state).toMatchObject({ state: State.New, reps: 0, stability: 0 });
	});

	it('refuses a deck the caller cannot read', async () => {
		const owner = await makeDeck('session-acl', 1);
		const outsider = await makeDeck('session-acl-out', 1);
		await expect(getReviewSession(outsider.userId, owner.deckId, 100, db)).rejects.toBeInstanceOf(
			DeckAccessError
		);
	});

	it('round-trips: a session graded on the client lands where the next session expects it', async () => {
		const deck = await makeDeck('session-roundtrip', 1);
		const first = await getReviewSession(deck.userId, deck.deckId, 100, db);
		const result = grade(startSession(first), Rating.Easy, new Date(), 900);
		if (!result) throw new Error('no card');
		expect(result.session.queue).toEqual([]);

		await applyReviewBatch(deck.userId, [result.event], db);

		// Easy on a new card schedules days out, so the next session is empty.
		const second = await getReviewSession(deck.userId, deck.deckId, 100, db);
		expect(second.cards).toEqual([]);
		// The client and the server agreed, so the stored `due` is also the one the reviewer
		// showed — but it is stored because the server computed it, not because the client sent it.
		expect(scheduling(await stateOf(deck.userId, deck.cardIds[0]!))?.due).toBe(
			result.event.predicted.state.due
		);
	});
});
