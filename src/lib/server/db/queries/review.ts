/**
 * The review loop's server half (CLAUDE.md §6): the session-start batch, the idempotent write
 * queue endpoint's query, and the server-side recompute path.
 *
 * Four things here are load-bearing and should not be "simplified" away:
 *
 * 1. `getReviewSession` is *one* query for the whole batch. Never per card.
 * 2. `applyReviewBatch` schedules the grade itself and stores *its own* answer — invariant
 *    §2.7. The client's `predicted` block is compared and discarded; it never reaches a
 *    column. Do not "save a call to `$lib/fsrs`" by trusting it.
 * 3. That same function's `last_review <` guard is what makes a retrying client queue safe:
 *    a write only lands when it is newer *by review time* than what is stored.
 * 4. `recomputeUserCardState` replays `review_log` through `$lib/fsrs`. It is the bulk form of
 *    what (2) does one event at a time, and it exists for import backfill, client-bug repair,
 *    and parameter refits; CLAUDE.md §17 forbids deleting it as unused.
 */
import { and, asc, eq, inArray, sql } from 'drizzle-orm';
import {
	cards,
	fields,
	notes,
	reviewLog,
	templates,
	userCardState,
	userFsrsParams,
	users
} from '../schema';
import { db } from '../index';
import { studyDayEnd } from '../day-boundary';
import { requireDeckAccess, DeckAccessError } from './access';
import type { DbClient } from './types';
import {
	defaultFsrsParams,
	newCardState,
	replayReviews,
	replayReviewSteps,
	type CardState,
	type FsrsParams,
	type Grade,
	type ReviewEvent
} from '$lib/fsrs';
import { renderCard, sanitiseCardHtml } from '$lib/render';
import { toWireCardState } from '$lib/review/wire';
import type {
	WireReviewBatchResult,
	WireReviewCard,
	WireReviewEvent,
	WireReviewResult,
	WireReviewSession
} from '$lib/review/types';
import { compareToPrediction, reportDivergence } from '$lib/server/fsrs/divergence';

/** Cards per session batch. Big enough that a session never refetches, small enough to render. */
const DEFAULT_BATCH_SIZE = 100;

/**
 * `user_fsrs_params` for a deck, falling back to the user's global row (`deck_id IS NULL`) and
 * then to the `ts-fsrs` defaults. The deck-specific row wins when both exist.
 */
export async function getFsrsParams(
	userId: string,
	deckId: string,
	client: DbClient = db
): Promise<FsrsParams> {
	const rows = await client.select().from(userFsrsParams).where(eq(userFsrsParams.userId, userId));

	const row = rows.find((r) => r.deckId === deckId) ?? rows.find((r) => r.deckId === null);
	if (!row) return defaultFsrsParams();
	return {
		fsrsVersion: row.fsrsVersion,
		params: row.params,
		desiredRetention: row.desiredRetention
	};
}

/**
 * The session-start payload: every card due in `deckId` for `userId` today, rendered and
 * sanitised, with the caller's own `user_card_state` attached, plus their FSRS parameters.
 *
 * "Due today" is the study-day window from `day-boundary.ts` — a per-user rollover hour,
 * computed here in the query rather than trusted from the client (CLAUDE.md §5). A card with
 * no `user_card_state` row has never been seen by this user and is new, hence the LEFT JOIN.
 */
export async function getReviewSession(
	userId: string,
	deckId: string,
	limit: number = DEFAULT_BATCH_SIZE,
	client: DbClient = db
): Promise<WireReviewSession> {
	await requireDeckAccess(client, userId, deckId, 'read');

	const dayEnd = studyDayEnd();
	const rows = await client
		.select({
			cardId: cards.id,
			noteId: cards.noteId,
			ordinal: cards.ordinal,
			noteFields: notes.fields,
			noteTypeId: notes.noteTypeId,
			qfmt: templates.qfmt,
			afmt: templates.afmt,
			state: userCardState,
			studyDayEnd: sql<string>`${dayEnd}`.as('study_day_end')
		})
		.from(cards)
		.innerJoin(notes, eq(notes.id, cards.noteId))
		.innerJoin(templates, eq(templates.id, cards.templateId))
		// Only to bring `users.timezone` / `users.day_start_hour` into scope for `studyDayEnd()`:
		// the day boundary is resolved in the query, never trusted from the client (CLAUDE.md §5).
		.innerJoin(users, eq(users.id, userId))
		.leftJoin(
			userCardState,
			and(eq(userCardState.cardId, cards.id), eq(userCardState.userId, userId))
		)
		.where(
			and(
				eq(cards.deckId, deckId),
				sql`(${userCardState.userId} is null or (not ${userCardState.suspended} and ${userCardState.due} < ${dayEnd}))`
			)
		)
		// Cards with scheduling state first, earliest due first; new cards after, in creation
		// order. `cards.id` is a UUIDv7, so ordering by it is ordering by creation time.
		.orderBy(sql`${userCardState.userId} is null`, asc(userCardState.due), asc(cards.id))
		.limit(limit);

	// One extra query for the field names — `notes.fields` is a positional array and the
	// template language addresses fields by name.
	const noteTypeIds = [...new Set(rows.map((r) => r.noteTypeId))];
	const fieldRows =
		noteTypeIds.length > 0
			? await client
					.select({
						noteTypeId: fields.noteTypeId,
						ordinal: fields.ordinal,
						name: fields.name
					})
					.from(fields)
					.where(inArray(fields.noteTypeId, noteTypeIds))
					.orderBy(asc(fields.ordinal))
			: [];

	const fieldNames = new Map<string, string[]>();
	for (const f of fieldRows) {
		const names = fieldNames.get(f.noteTypeId) ?? [];
		names[f.ordinal] = f.name;
		fieldNames.set(f.noteTypeId, names);
	}

	// The study day is a per-user constant, so every row carries the same value; take it from
	// the first row and fall back to a direct read when the batch is empty.
	const dayEndValue = rows[0]?.studyDayEnd
		? new Date(rows[0].studyDayEnd)
		: await readStudyDayEnd(userId, client);

	const batch = rows.map((row): WireReviewCard => {
		const names = fieldNames.get(row.noteTypeId) ?? [];
		const values: Record<string, string> = {};
		names.forEach((name, i) => {
			if (name !== undefined) values[name] = row.noteFields[i] ?? '';
		});

		const { front, back } = renderCard(row.qfmt, row.afmt, values, row.ordinal);
		return {
			cardId: row.cardId,
			noteId: row.noteId,
			ordinal: row.ordinal,
			// CLAUDE.md §8: card content is other users' HTML in the multiuser model. Sanitised
			// here, on render, so the reviewer's `{@html}` never sees raw field content.
			front: sanitiseCardHtml(front),
			back: sanitiseCardHtml(back),
			state: toWireCardState(row.state ? stateFromRow(row.state) : newCardState(dayEndValue))
		};
	});

	return {
		deckId,
		cards: batch,
		params: await getFsrsParams(userId, deckId, client),
		studyDayEnd: dayEndValue.toISOString()
	};
}

async function readStudyDayEnd(userId: string, client: DbClient): Promise<Date> {
	const [row] = await client
		.select({ dayEnd: sql<string>`${studyDayEnd()}` })
		.from(users)
		.where(eq(users.id, userId));
	if (!row) throw new Error(`user ${userId} not found`);
	return new Date(row.dayEnd);
}

type UserCardStateRow = typeof userCardState.$inferSelect;

/** The scheduling half of a `user_card_state` row. Exported for tests to assert against. */
export function stateFromRow(row: UserCardStateRow): CardState {
	return {
		due: row.due,
		stability: row.stability,
		difficulty: row.difficulty,
		state: row.state,
		reps: row.reps,
		lapses: row.lapses,
		elapsedDays: row.elapsedDays,
		scheduledDays: row.scheduledDays,
		learningSteps: row.learningSteps,
		lastReview: row.lastReview
	};
}

/** Anki's revlog `type`, derived from the state the card was in when it was answered. */
function reviewKindFor(stateBefore: number): number {
	if (stateBefore === 2) return 1; // review
	if (stateBefore === 3) return 2; // relearn
	return 0; // learn (new / learning)
}

/**
 * Ascending review time, ties broken by id so an identically-timed pair always folds the same
 * way. The one ordering rule for a card's answers — the SQL reads use `ORDER BY reviewed_at,
 * id` to match, because a fold that reorders is a fold that produces different state.
 */
function byReviewTime(
	a: { reviewedAt: Date | string; id: string },
	b: { reviewedAt: Date | string; id: string }
): number {
	return (
		new Date(a.reviewedAt).getTime() - new Date(b.reviewedAt).getTime() || a.id.localeCompare(b.id)
	);
}

/**
 * Upserts scheduling state for a set of cards in one statement. The only writer of
 * `user_card_state`'s FSRS columns, so the paths that produce a state — the live grade and the
 * bulk replay — cannot drift into writing different column sets.
 *
 * `guarded` applies the §6 rule: the write lands only where the stored row is older *by review
 * time* than the one replacing it. The comparison is against `excluded.last_review` rather
 * than a bound parameter because they are the same instant — `ts-fsrs` sets `last_review` to
 * the answer's timestamp — which is what lets the whole set go in one statement. `false` is
 * for a repair, which outranks whatever is stored.
 */
async function writeCardStates(
	tx: DbClient,
	userId: string,
	rows: readonly { cardId: string; state: CardState }[],
	{ guarded }: { guarded: boolean }
) {
	if (rows.length === 0) return [];
	return tx
		.insert(userCardState)
		.values(
			rows.map(({ cardId, state }) => ({
				userId,
				cardId,
				due: state.due,
				stability: state.stability,
				difficulty: state.difficulty,
				state: state.state,
				reps: state.reps,
				lapses: state.lapses,
				elapsedDays: state.elapsedDays,
				scheduledDays: state.scheduledDays,
				learningSteps: state.learningSteps,
				lastReview: state.lastReview
			}))
		)
		.onConflictDoUpdate({
			target: [userCardState.userId, userCardState.cardId],
			set: {
				due: sql`excluded.due`,
				stability: sql`excluded.stability`,
				difficulty: sql`excluded.difficulty`,
				state: sql`excluded.state`,
				reps: sql`excluded.reps`,
				lapses: sql`excluded.lapses`,
				elapsedDays: sql`excluded.elapsed_days`,
				scheduledDays: sql`excluded.scheduled_days`,
				learningSteps: sql`excluded.learning_steps`,
				lastReview: sql`excluded.last_review`
			},
			setWhere: guarded
				? sql`${userCardState.lastReview} is null or ${userCardState.lastReview} < excluded.last_review`
				: undefined
		})
		.returning();
}

/**
 * One answer in a card's fold. `event` is set for an answer this batch is applying and `null`
 * for a `review_log` row being replayed to reach it — history, already stored, nothing to write.
 */
interface Step extends ReviewEvent {
	id: string;
	event: WireReviewEvent | null;
}

const stepOf = (event: WireReviewEvent): Step => ({
	id: event.id,
	rating: event.rating,
	reviewedAt: new Date(event.reviewedAt),
	event
});

/**
 * Applies a write-queue batch. **This is where CLAUDE.md §2.7 is enforced**, and it is the
 * idempotency contract of §6.
 *
 * Per card, in ascending review-time order: read the caller's stored `user_card_state`,
 * schedule each grade here through the same `$lib/fsrs` the client ran, compare the result
 * against `event.predicted` and log a divergence if they disagree — without ever letting
 * `predicted` change the answer — then append the *server's* numbers to `review_log` and
 * upsert `user_card_state`.
 *
 * The client's only inputs are `cardId`, `rating`, `reviewedAt` and `durationMs`. Nothing it
 * sends about memory state reaches a column, which is what makes the stored history
 * verifiable rather than merely asserted.
 *
 * Three things keep that correct under a retrying, concurrent, occasionally reordering client:
 *
 * - **An advisory lock per card.** Scheduling server-side turns this into a read-modify-write,
 *   and READ COMMITTED would otherwise let two batches for the same card (two tabs, a
 *   redelivered POST) both schedule from the same `before` and lose one review. Advisory
 *   rather than `SELECT … FOR UPDATE` because a card the user has never seen has no row to
 *   lock, and that is precisely the case two concurrent first-grades hit.
 * - **An event already in `review_log` is skipped outright**, so a retry neither reschedules
 *   from the row it already advanced nor raises a spurious divergence against its own
 *   prediction. The lookup is not scoped to `user_id`: `review_log.id` is a global primary
 *   key, so an id already taken by anyone must count as taken here too, or the row would be
 *   silently dropped by `ON CONFLICT` while its state write landed anyway.
 * - **A card whose batch predates its stored `last_review` is replayed from `review_log`
 *   instead**, because the server cannot derive a truthful `*_before` for a review it is
 *   seeing out of order. Fabricating one would write permanently wrong training data (§2.5),
 *   which no later recompute repairs — `recomputeUserCardState` only rewrites
 *   `user_card_state`. The rows this cannot fix are the ones already written *before* the gap
 *   became visible: they record the state the server genuinely held then, and `review_log` is
 *   append-only (§17), so they stand. Their `rating` and `reviewed_at` are still exact, which
 *   is what a replay and an optimiser fit actually need.
 *
 * Everything runs in one transaction, and every distinct deck is access-checked first: a user
 * may only submit reviews for cards in decks they can read (CLAUDE.md §9). An unknown card id
 * is a `DeckAccessError`, so a caller cannot probe for card existence.
 */
export async function applyReviewBatch(
	userId: string,
	events: readonly WireReviewEvent[],
	client: DbClient = db
): Promise<WireReviewBatchResult> {
	// `ON CONFLICT` cannot affect the same row twice in one statement, and a client retry can
	// legitimately resend an id it already sent in this very batch.
	const unique = [...new Map(events.map((e) => [e.id, e])).values()].sort(byReviewTime);
	if (unique.length === 0) return { applied: 0, results: [] };

	return client.transaction(async (tx) => {
		const cardIds = [...new Set(unique.map((e) => e.cardId))];
		const owned = await tx
			.select({ cardId: cards.id, deckId: cards.deckId })
			.from(cards)
			.where(inArray(cards.id, cardIds));

		const deckOf = new Map(owned.map((c) => [c.cardId, c.deckId]));
		const paramsOf = new Map<string, FsrsParams>();
		const paramsByDeck = new Map<string, FsrsParams>();
		for (const cardId of cardIds) {
			const deckId = deckOf.get(cardId);
			// A card that does not exist and a card in a deck the caller cannot see are the same
			// answer, for the same reason `access.ts` collapses them: no existence oracle.
			if (!deckId) throw new DeckAccessError(cardId, 'not_found');
			let params = paramsByDeck.get(deckId);
			if (!params) {
				await requireDeckAccess(tx, userId, deckId, 'read');
				params = await getFsrsParams(userId, deckId, tx);
				paramsByDeck.set(deckId, params);
			}
			paramsOf.set(cardId, params);
		}

		// One statement, one bound parameter per key, evaluated left to right — so the pre-sort is
		// what stops two batches sharing cards from deadlocking against each other. Held to
		// commit. See the doc comment for why this is not `FOR UPDATE`.
		//
		// Deliberately not `unnest($1::text[])`: drizzle hands postgres-js the JS array as a
		// single `text[]` parameter and it arrives as a bare string, which Postgres rejects as a
		// malformed array literal. A list of scalar params has no serialisation to get wrong.
		await tx.execute(
			sql`select ${sql.join(
				cardIds
					.map((cardId) => `${userId}:${cardId}`)
					.sort()
					.map((key) => sql`pg_advisory_xact_lock(hashtextextended(${key}, 0))`),
				sql`, `
			)}`
		);

		const logged = await tx
			.select({ id: reviewLog.id })
			.from(reviewLog)
			.where(
				inArray(
					reviewLog.id,
					unique.map((e) => e.id)
				)
			);
		const seen = new Set(logged.map((r) => r.id));
		const fresh = unique.filter((e) => !seen.has(e.id));
		if (fresh.length === 0) return { applied: 0, results: [] };

		const stored = new Map(
			(
				await tx
					.select()
					.from(userCardState)
					.where(and(eq(userCardState.userId, userId), inArray(userCardState.cardId, cardIds)))
			).map((row) => [row.cardId, row])
		);

		const byCard = new Map<string, Step[]>();
		for (const e of fresh) {
			const steps = byCard.get(e.cardId);
			if (steps) steps.push(stepOf(e));
			else byCard.set(e.cardId, [stepOf(e)]);
		}

		const logRows: (typeof reviewLog.$inferInsert)[] = [];
		const results: WireReviewResult[] = [];
		// Split by whether the §6 guard applies, so each group goes in as one statement.
		const advanced: { cardId: string; state: CardState }[] = [];
		const repaired: { cardId: string; state: CardState }[] = [];

		for (const [cardId, cardSteps] of byCard) {
			const params = paramsOf.get(cardId)!;
			const row = stored.get(cardId);
			const lastReview = row?.lastReview ?? null;
			const earliest = cardSteps[0]!.reviewedAt;

			// `user_card_state` is a memo of this fold's prefix, and it is valid exactly when
			// every answer in the batch happened after the one it records. When it isn't, the
			// memo is unusable: the server would have to invent a `before` for an answer it is
			// seeing out of order, and a fabricated `*_before` is training data no recompute
			// repairs (§2.5). So the prefix gets rebuilt from `review_log` instead — the same
			// assumption `recomputeUserCardState` makes, that the log is complete for the card.
			const inOrder = lastReview === null || lastReview < earliest;
			let initial: CardState;
			let steps: Step[];

			if (inOrder) {
				// No row means this user has never seen the card; `due` is the only free field on a
				// new state and `next()` overwrites it, so seeding it with the review instant is safe.
				initial = row ? stateFromRow(row) : newCardState(earliest);
				steps = cardSteps;
			} else {
				const history = await tx
					.select({ id: reviewLog.id, rating: reviewLog.rating, reviewedAt: reviewLog.reviewedAt })
					.from(reviewLog)
					.where(and(eq(reviewLog.cardId, cardId), eq(reviewLog.userId, userId)));
				steps = [
					...history.map((r) => ({ ...r, rating: r.rating as Grade, event: null })),
					...cardSteps
				].sort(byReviewTime);
				initial = newCardState(steps[0]!.reviewedAt);
			}

			const stepResults = replayReviewSteps(steps, params, initial);
			for (const [i, step] of steps.entries()) {
				const event = step.event;
				const result = stepResults[i]!;
				if (!event) continue;

				const fields = compareToPrediction(result.state, event.predicted.state);
				if (fields.length > 0) {
					reportDivergence({
						userId,
						cardId,
						eventId: event.id,
						serverFsrsVersion: params.fsrsVersion,
						clientFsrsVersion: event.predicted.fsrsVersion,
						fields
					});
				}
				logRows.push({
					id: event.id,
					userId,
					cardId,
					rating: event.rating,
					reviewedAt: step.reviewedAt,
					durationMs: event.durationMs,
					// Straight off `ts-fsrs`' own review log, which carries the pre-review memory state
					// and the elapsed interval it measured. No projection, no client input.
					stateBefore: result.log.stateBefore,
					learningStepsBefore: result.log.learningStepsBefore,
					stabilityBefore: result.log.stabilityBefore,
					difficultyBefore: result.log.difficultyBefore,
					elapsedDaysBefore: result.log.elapsedDaysBefore,
					scheduledDaysAfter: result.log.scheduledDaysAfter,
					fsrsVersion: result.log.fsrsVersion,
					reviewKind: reviewKindFor(result.log.stateBefore)
				});
				results.push({ id: event.id, cardId, state: toWireCardState(result.state) });
			}

			const state = stepResults.at(-1)!.state;
			(inOrder ? advanced : repaired).push({ cardId, state });
		}

		await tx.insert(reviewLog).values(logRows).onConflictDoNothing();
		await writeCardStates(tx, userId, advanced, { guarded: true });
		await writeCardStates(tx, userId, repaired, { guarded: false });

		return { applied: fresh.length, results };
	});
}

/**
 * Rebuilds one card's `user_card_state` by replaying `review_log` through `$lib/fsrs`
 * (CLAUDE.md §6, §17) — the bulk form of what `applyReviewBatch` does one event at a time.
 * It is what `.apkg` import backfill, post-incident repair, and parameter refits call.
 *
 * Unlike `applyReviewBatch`'s fast path this writes unconditionally, ignoring the
 * `last_review` guard: it is the repair path, so the replay result is authoritative over
 * whatever is stored.
 *
 * Returns `null` when the card has no reviews for this user — there is nothing to rebuild.
 */
export async function recomputeUserCardState(
	userId: string,
	cardId: string,
	client: DbClient = db
) {
	const [card] = await client
		.select({ deckId: cards.deckId })
		.from(cards)
		.where(eq(cards.id, cardId));
	if (!card) throw new DeckAccessError(cardId, 'not_found');
	await requireDeckAccess(client, userId, card.deckId, 'read');

	const history = await client
		.select({ id: reviewLog.id, rating: reviewLog.rating, reviewedAt: reviewLog.reviewedAt })
		.from(reviewLog)
		.where(and(eq(reviewLog.cardId, cardId), eq(reviewLog.userId, userId)))
		// The `byReviewTime` ordering, in SQL. Both must agree or the two paths fold differently.
		.orderBy(asc(reviewLog.reviewedAt), asc(reviewLog.id));
	if (history.length === 0) return null;

	const params = await getFsrsParams(userId, card.deckId, client);
	const first = history[0];
	if (!first) return null;
	const replayed = replayReviews(
		history.map((r) => ({ rating: r.rating as 1 | 2 | 3 | 4, reviewedAt: r.reviewedAt })),
		params,
		// The card was new before its first review; `due` is the only free field and it is
		// overwritten by the first `next()` anyway.
		newCardState(first.reviewedAt)
	);

	return (
		(await writeCardStates(client, userId, [{ cardId, state: replayed }], { guarded: false }))[0] ??
		null
	);
}
