/**
 * The review loop's server half (CLAUDE.md §6): the session-start batch, the idempotent write
 * queue endpoint's query, and the server-side recompute path.
 *
 * Three things here are load-bearing and should not be "simplified" away:
 *
 * 1. `getReviewSession` is *one* query for the whole batch. Never per card.
 * 2. `applyReviewBatch`'s `last_review <` guard is what makes a retrying, reordering client
 *    queue safe: application becomes order-independent and last-write-wins by *review* time,
 *    not arrival time. Same batch twice is a no-op.
 * 3. `recomputeUserCardState` replays `review_log` through `$lib/fsrs`. Nothing calls it in
 *    Phase 1; it exists for import backfill, client-bug repair, and parameter refits, and
 *    CLAUDE.md §17 forbids deleting it as unused.
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
import { defaultFsrsParams, newCardState, replayReviews, type FsrsParams } from '$lib/fsrs';
import { renderCard, sanitiseCardHtml } from '$lib/render';
import { fromWireCardState, toWireCardState } from '$lib/review/wire';
import type { WireReviewCard, WireReviewEvent, WireReviewSession } from '$lib/review/types';

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

function stateFromRow(row: UserCardStateRow) {
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
 * Applies a write-queue batch. **This is the idempotency contract of CLAUDE.md §6.**
 *
 * Per event: append to `review_log` on conflict do nothing (the id is client-generated, so a
 * retry collides with itself), then upsert `user_card_state` guarded by
 * `last_review IS NULL OR last_review < reviewedAt`.
 *
 * That guard is the whole property. Replaying a batch, reordering it, or interleaving it with
 * a later review all converge to the same row, because a write only lands when it is newer
 * *by review time* than what is already stored.
 *
 * Everything runs in one transaction, and every distinct deck is access-checked first: a user
 * may only submit reviews for cards in decks they can read (CLAUDE.md §9). An unknown card id
 * is a `DeckAccessError`, so a caller cannot probe for card existence.
 */
export async function applyReviewBatch(
	userId: string,
	events: readonly WireReviewEvent[],
	client: DbClient = db
): Promise<{ applied: number }> {
	// `ON CONFLICT` cannot affect the same row twice in one statement, and a client retry can
	// legitimately resend an id it already sent in this very batch.
	const unique = [...new Map(events.map((e) => [e.id, e])).values()];
	if (unique.length === 0) return { applied: 0 };

	return client.transaction(async (tx) => {
		const cardIds = [...new Set(unique.map((e) => e.cardId))];
		const owned = await tx
			.select({ cardId: cards.id, deckId: cards.deckId })
			.from(cards)
			.where(inArray(cards.id, cardIds));

		const deckOf = new Map(owned.map((c) => [c.cardId, c.deckId]));
		for (const cardId of cardIds) {
			const deckId = deckOf.get(cardId);
			// A card that does not exist and a card in a deck the caller cannot see are the same
			// answer, for the same reason `access.ts` collapses them: no existence oracle.
			if (!deckId) throw new DeckAccessError(cardId, 'not_found');
		}
		for (const deckId of new Set(deckOf.values())) {
			await requireDeckAccess(tx, userId, deckId, 'read');
		}

		const fsrsVersionByDeck = new Map<string, number>();
		for (const deckId of new Set(deckOf.values())) {
			fsrsVersionByDeck.set(deckId, (await getFsrsParams(userId, deckId, tx)).fsrsVersion);
		}

		await tx
			.insert(reviewLog)
			.values(
				unique.map((e) => {
					const before = e.stateBefore;
					const after = e.stateAfter;
					return {
						id: e.id,
						userId,
						cardId: e.cardId,
						rating: e.rating,
						reviewedAt: new Date(e.reviewedAt),
						durationMs: e.durationMs,
						// Every `*_before` column is a projection of `stateBefore`, and
						// `elapsed_days_before` of `stateAfter`: `ts-fsrs` copies the elapsed interval
						// it computed at review time onto the card it returns, so the client does not
						// have to send the review log twice in two shapes.
						stateBefore: before.state,
						learningStepsBefore: before.learningSteps,
						stabilityBefore: before.stability,
						difficultyBefore: before.difficulty,
						elapsedDaysBefore: after.elapsedDays,
						scheduledDaysAfter: after.scheduledDays,
						fsrsVersion: fsrsVersionByDeck.get(deckOf.get(e.cardId) ?? '') ?? null,
						reviewKind: reviewKindFor(before.state)
					};
				})
			)
			.onConflictDoNothing();

		for (const e of unique) {
			const after = fromWireCardState(e.stateAfter);
			const reviewedAt = new Date(e.reviewedAt);
			await tx
				.insert(userCardState)
				.values({
					userId,
					cardId: e.cardId,
					due: after.due,
					stability: after.stability,
					difficulty: after.difficulty,
					state: after.state,
					reps: after.reps,
					lapses: after.lapses,
					elapsedDays: after.elapsedDays,
					scheduledDays: after.scheduledDays,
					learningSteps: after.learningSteps,
					lastReview: reviewedAt
				})
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
					// The §6 guard, verbatim. Without it, a retried or out-of-order batch would
					// overwrite a newer review with an older one and silently corrupt the row.
					// The bound value is ISO text, not a `Date`: postgres-js cannot bind a `Date` to a
					// cast placeholder (the same constraint `day-boundary.ts` documents).
					setWhere: sql`${userCardState.lastReview} is null or ${userCardState.lastReview} < ${e.reviewedAt}::timestamptz`
				});
		}

		return { applied: unique.length };
	});
}

/**
 * Rebuilds one card's `user_card_state` by replaying `review_log` through `$lib/fsrs`
 * (CLAUDE.md §6, §17). The server is not the scheduling authority in Phase 1 — but it must be
 * able to be, for `.apkg` import backfill, for repairing a client bug, and after a parameter
 * refit.
 *
 * Unlike `applyReviewBatch` this writes unconditionally: it is the repair path, so the replay
 * result is authoritative over whatever is currently stored.
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
		.select({ rating: reviewLog.rating, reviewedAt: reviewLog.reviewedAt })
		.from(reviewLog)
		.where(and(eq(reviewLog.cardId, cardId), eq(reviewLog.userId, userId)))
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

	const [row] = await client
		.insert(userCardState)
		.values({
			userId,
			cardId,
			due: replayed.due,
			stability: replayed.stability,
			difficulty: replayed.difficulty,
			state: replayed.state,
			reps: replayed.reps,
			lapses: replayed.lapses,
			elapsedDays: replayed.elapsedDays,
			scheduledDays: replayed.scheduledDays,
			learningSteps: replayed.learningSteps,
			lastReview: replayed.lastReview
		})
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
			}
		})
		.returning();

	return row ?? null;
}
