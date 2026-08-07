/**
 * Isomorphic scheduling wrapper over `ts-fsrs`. Pure functions only: no DB, no
 * `fetch`, no browser globals. That is what makes the client's optimistic grade and
 * the server's replay produce the same state — enforced by the `src/lib/fsrs/**`
 * block in `eslint.config.js`, not by convention.
 *
 * Fuzz is off. Fuzz would still be deterministic (ts-fsrs seeds it from the card),
 * but off is one fewer thing that can silently differ between the two paths.
 */
import { checkParameters, FSRS, fsrs, type Card, type FSRSParameters, State } from 'ts-fsrs';
import type { CardState, FsrsParams, Grade, ReviewEvent, ScheduleResult } from './types';

/** Parameter-array length per FSRS generation. See CLAUDE.md §2.3. */
const PARAM_LENGTH = new Map<number, number>([
	[4, 17],
	[5, 19],
	[6, 21]
]);

/** A never-reviewed card. `due` is when the card first becomes available. */
export function newCardState(due: Date): CardState {
	return {
		due,
		stability: 0,
		difficulty: 0,
		state: State.New,
		reps: 0,
		lapses: 0,
		elapsedDays: 0,
		scheduledDays: 0,
		learningSteps: 0,
		lastReview: null
	};
}

/**
 * The client path: one card, one rating, one instant. Synchronous and local — §6
 * forbids any network work here.
 */
export function scheduleReview(
	state: CardState,
	rating: Grade,
	now: Date,
	params: FsrsParams
): ScheduleResult {
	return applyReview(schedulerFor(params), state, rating, now, params.fsrsVersion);
}

/**
 * The server replay path: rebuild `user_card_state` from `review_log` alone. Needed
 * for `.apkg` import backfill, for repairing a client bug, and after a parameter
 * refit (CLAUDE.md §6). `events` must be in ascending `reviewed_at` order.
 *
 * Deliberately a fold over the same `applyReview` the client uses — a second
 * scheduling implementation is exactly the divergence this module exists to prevent.
 */
export function replayReviews(
	events: readonly ReviewEvent[],
	params: FsrsParams,
	initial: CardState
): CardState {
	const scheduler = schedulerFor(params);
	let state = initial;
	for (const event of events) {
		state = applyReview(scheduler, state, event.rating, event.reviewedAt, params.fsrsVersion).state;
	}
	return state;
}

function applyReview(
	scheduler: FSRS,
	state: CardState,
	rating: Grade,
	now: Date,
	fsrsVersion: number
): ScheduleResult {
	const { card, log } = scheduler.next(toFsrsCard(state), now, rating);
	return {
		state: fromFsrsCard(card),
		log: {
			rating,
			reviewedAt: log.review,
			// ts-fsrs' log carries the pre-review memory state; `scheduled_days` is the
			// one field we want from after, so it comes off the card.
			stateBefore: log.state,
			stabilityBefore: log.stability,
			difficultyBefore: log.difficulty,
			elapsedDaysBefore: log.elapsed_days,
			learningStepsBefore: log.learning_steps,
			scheduledDaysAfter: card.scheduled_days,
			fsrsVersion
		}
	};
}

function schedulerFor(params: FsrsParams): FSRS {
	return fsrs(toFsrsParameters(params));
}

/**
 * Everything not derived from `user_fsrs_params` is left at the `ts-fsrs` default:
 * learning steps, relearning steps, maximum interval, short-term scheduling. Those
 * are deck-preset concerns and there is no column for them yet.
 *
 * Validation is not defensive padding. `ts-fsrs` coerces a bad weight rather than
 * refusing it — `clipParameters` does `clamp(w[i] || 0, min, max)`, and a `NaN`
 * weight survives the `params` jsonb column as `null` — so a corrupt optimiser fit
 * would schedule plausibly and wrongly, forever, with nothing raised. Likewise
 * `generatorParameters` silently substitutes the 0.9 default for a falsy
 * `request_retention`, so a `0` or `NaN` retention would diverge stored config from
 * actual scheduling. Both are the CLAUDE.md §15 "silently corrupts
 * `user_card_state`" bucket, and both are invisible to the parity test because the
 * client and server paths share the same coercion.
 */
function toFsrsParameters(params: FsrsParams): Partial<FSRSParameters> {
	const expected = PARAM_LENGTH.get(params.fsrsVersion);
	if (expected === undefined) {
		throw new Error(`Unsupported fsrs_version ${params.fsrsVersion}: expected 4, 5 or 6.`);
	}
	if (params.params.length !== expected) {
		throw new Error(
			`fsrs_version ${params.fsrsVersion} expects ${expected} parameters, got ${params.params.length}.`
		);
	}
	// ts-fsrs' own checkParameters misses one case — it does
	// `find((w) => !Number.isFinite(w)) !== undefined`, so an `undefined` weight
	// finds as undefined and passes. Check here, then hand off.
	const badWeight = params.params.findIndex((weight) => !Number.isFinite(weight));
	if (badWeight !== -1) {
		throw new Error(
			`Non-finite FSRS parameter at index ${badWeight}: ${String(params.params[badWeight])}.`
		);
	}
	if (
		!Number.isFinite(params.desiredRetention) ||
		params.desiredRetention <= 0 ||
		params.desiredRetention > 1
	) {
		throw new Error(`desired_retention must be in (0, 1], got ${String(params.desiredRetention)}.`);
	}
	return {
		// checkParameters throws on any non-finite weight instead of clamping it.
		w: checkParameters([...params.params]),
		request_retention: params.desiredRetention,
		enable_fuzz: false
	};
}

function toFsrsCard(state: CardState): Card {
	return {
		due: state.due,
		stability: state.stability,
		difficulty: state.difficulty,
		elapsed_days: state.elapsedDays,
		scheduled_days: state.scheduledDays,
		learning_steps: state.learningSteps,
		reps: state.reps,
		lapses: state.lapses,
		state: state.state,
		last_review: state.lastReview ?? undefined
	};
}

function fromFsrsCard(card: Card): CardState {
	return {
		due: card.due,
		stability: card.stability,
		difficulty: card.difficulty,
		state: card.state,
		reps: card.reps,
		lapses: card.lapses,
		elapsedDays: card.elapsed_days,
		scheduledDays: card.scheduled_days,
		learningSteps: card.learning_steps,
		lastReview: card.last_review ?? null
	};
}
