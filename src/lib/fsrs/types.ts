import type { Grade, State } from 'ts-fsrs';

export { Rating, State } from 'ts-fsrs';
export type { Grade } from 'ts-fsrs';

/**
 * The scheduling half of a `user_card_state` row — everything FSRS reads or writes.
 * The row's identity (`user_id`, `card_id`) and its non-scheduling columns
 * (`suspended`, `buried_until`, `flag`) are deliberately absent: this module never
 * decides those.
 */
export interface CardState {
	due: Date;
	stability: number;
	difficulty: number;
	state: State;
	reps: number;
	lapses: number;
	elapsedDays: number;
	scheduledDays: number;
	/**
	 * Position on the (re)learning steps ladder. Load-bearing: FSRS-6 short-term
	 * scheduling reads it, so it must be persisted or a card's next interval changes
	 * across a reload. `docs/schema.md` has no column for it yet.
	 */
	learningSteps: number;
	lastReview: Date | null;
}

/**
 * A `user_fsrs_params` row. `params` is a JSON array — never fixed-width columns.
 */
export interface FsrsParams {
	/**
	 * Which parameter generation was *stored*: 4, 5 or 6, for 17, 19 or 21 weights.
	 * Not which algorithm runs — `ts-fsrs` 5.4.1 migrates 17- and 19-length arrays up
	 * to 21 and always schedules as FSRS-6. The field's job is to keep an old fit
	 * readable after an upgrade, and to catch a params array that has drifted out of
	 * agreement with the version recorded next to it.
	 */
	fsrsVersion: number;
	params: readonly number[];
	/** Target recall probability, in (0, 1]. */
	desiredRetention: number;
}

/** The minimum a `review_log` row must supply to be replayable. */
export interface ReviewEvent {
	rating: Grade;
	reviewedAt: Date;
}

/** The FSRS-derived columns of the `review_log` row this review produces. */
export interface ReviewLogEntry {
	rating: Grade;
	reviewedAt: Date;
	stateBefore: State;
	stabilityBefore: number;
	difficultyBefore: number;
	elapsedDaysBefore: number;
	learningStepsBefore: number;
	scheduledDaysAfter: number;
	fsrsVersion: number;
}

export interface ScheduleResult {
	state: CardState;
	log: ReviewLogEntry;
}
