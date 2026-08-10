/**
 * The wire shapes shared by the reviewer (client) and `/api/reviews/*` (server).
 *
 * Everything here is JSON — `Date`s are ISO 8601 strings. `wire.ts` converts to and from the
 * `CardState` that `$lib/fsrs` actually schedules with.
 */
import type { FsrsParams, Grade, State } from '$lib/fsrs';

/** A `user_card_state` row's scheduling half, JSON-encoded. Mirrors `fsrs`' `CardState`. */
export interface WireCardState {
	due: string;
	stability: number;
	difficulty: number;
	state: State;
	reps: number;
	lapses: number;
	elapsedDays: number;
	scheduledDays: number;
	learningSteps: number;
	lastReview: string | null;
}

/** One card in the session batch: pre-rendered, pre-sanitised, plus the caller's own state. */
export interface WireReviewCard {
	cardId: string;
	noteId: string;
	/** Template ordinal, or cloze ordinal for cloze note types. */
	ordinal: number;
	/** Sanitised HTML — `sanitiseCardHtml` has already run server-side. */
	front: string;
	back: string;
	state: WireCardState;
}

/**
 * The single session-start payload (CLAUDE.md §6): the whole batch of due cards plus the
 * user's FSRS parameters. Never one request per card.
 */
export interface WireReviewSession {
	deckId: string;
	cards: WireReviewCard[];
	params: FsrsParams;
	/**
	 * End of the user's current study day, computed in the query from `users.timezone` +
	 * `users.day_start_hour` (CLAUDE.md §5). The client uses it for one decision only:
	 * whether a just-graded card comes back round in this session.
	 */
	studyDayEnd: string;
}

/**
 * The client's own FSRS result for one grade. Compared against what the server computes,
 * never stored and never authoritative (CLAUDE.md §2.7).
 */
export interface WirePrediction {
	/** The client's `user_fsrs_params.fsrs_version`. Named in the divergence log line (§6). */
	fsrsVersion: number;
	state: WireCardState;
}

/**
 * One entry of the write queue's POST body. The shape is fixed by CLAUDE.md §6.
 *
 * `id`, `cardId`, `rating`, `reviewedAt` and `durationMs` are the only fields the client is
 * entitled to assert — *which card, which rating, when*. Everything written to `review_log`
 * and `user_card_state` is derived server-side from state the server already holds.
 */
export interface WireReviewEvent {
	/** Client-generated UUIDv7. The idempotency key: `ON CONFLICT (id) DO NOTHING`. */
	id: string;
	cardId: string;
	rating: Grade;
	reviewedAt: string;
	durationMs: number | null;
	predicted: WirePrediction;
}

export interface WireReviewBatch {
	events: WireReviewEvent[];
}

/** One event's authoritative outcome, echoed back so the client can reconcile its queue. */
export interface WireReviewResult {
	id: string;
	cardId: string;
	state: WireCardState;
}

export interface WireReviewBatchResult {
	/** Events applied by this call. A pure retry applies nothing and is still a 200. */
	applied: number;
	results: WireReviewResult[];
}
