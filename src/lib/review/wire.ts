/**
 * `CardState` <-> JSON. Isomorphic: the server encodes the session batch on the way out and
 * decodes the write queue's events on the way back in.
 */
import type { CardState, Grade } from '$lib/fsrs';
import type { WireCardState, WirePrediction, WireReviewEvent } from './types';

export function toWireCardState(state: CardState): WireCardState {
	return {
		due: state.due.toISOString(),
		stability: state.stability,
		difficulty: state.difficulty,
		state: state.state,
		reps: state.reps,
		lapses: state.lapses,
		elapsedDays: state.elapsedDays,
		scheduledDays: state.scheduledDays,
		learningSteps: state.learningSteps,
		lastReview: state.lastReview?.toISOString() ?? null
	};
}

export function fromWireCardState(wire: WireCardState): CardState {
	return {
		due: new Date(wire.due),
		stability: wire.stability,
		difficulty: wire.difficulty,
		state: wire.state,
		reps: wire.reps,
		lapses: wire.lapses,
		elapsedDays: wire.elapsedDays,
		scheduledDays: wire.scheduledDays,
		learningSteps: wire.learningSteps,
		lastReview: wire.lastReview === null ? null : new Date(wire.lastReview)
	};
}

const FINITE_NUMBER_KEYS = [
	'stability',
	'difficulty',
	'reps',
	'lapses',
	'elapsedDays',
	'scheduledDays',
	'learningSteps'
] as const;

function isIsoInstant(value: unknown): value is string {
	return typeof value === 'string' && Number.isFinite(Date.parse(value));
}

function isWireCardState(value: unknown): value is WireCardState {
	if (typeof value !== 'object' || value === null) return false;
	const s = value as Record<string, unknown>;
	if (!isIsoInstant(s.due)) return false;
	if (s.lastReview !== null && !isIsoInstant(s.lastReview)) return false;
	if (typeof s.state !== 'number' || s.state < 0 || s.state > 3) return false;
	return FINITE_NUMBER_KEYS.every((key) => typeof s[key] === 'number' && Number.isFinite(s[key]));
}

function isWirePrediction(value: unknown): value is WirePrediction {
	if (typeof value !== 'object' || value === null) return false;
	const p = value as Record<string, unknown>;
	return Number.isInteger(p.fsrsVersion) && isWireCardState(p.state);
}

/**
 * Validates one entry of a `/api/reviews/batch` body.
 *
 * Nothing here is coerced: it parses or it is rejected. Under CLAUDE.md §2.7 none of it is
 * *stored* — the server recomputes the scheduling state itself — but `rating` and
 * `reviewedAt` are still inputs to that recompute, and they feed `review_log`, which is
 * unrecoverable training data (§2.5).
 *
 * `predicted` is required rather than optional on purpose: the client always has one (§2.6
 * makes it compute locally anyway), and an optional field would let a caller opt out of the
 * divergence check simply by omitting it.
 *
 * **Shape only.** Whether a well-formed event is *believable* — that its `reviewedAt` is not
 * in the future, that the caller may review that card — is a trust decision measured against
 * server state and a server clock, and it lives with the other trust checks in the route
 * handler and `applyReviewBatch`. This module is imported by the client too and has no
 * business holding either.
 */
export function parseWireReviewEvent(value: unknown): WireReviewEvent | null {
	if (typeof value !== 'object' || value === null) return null;
	const e = value as Record<string, unknown>;

	if (typeof e.id !== 'string' || typeof e.cardId !== 'string') return null;
	if (typeof e.rating !== 'number' || ![1, 2, 3, 4].includes(e.rating)) return null;
	if (!isIsoInstant(e.reviewedAt)) return null;
	if (e.durationMs !== null && e.durationMs !== undefined) {
		if (typeof e.durationMs !== 'number' || !Number.isInteger(e.durationMs) || e.durationMs < 0) {
			return null;
		}
	}
	if (!isWirePrediction(e.predicted)) return null;

	return {
		id: e.id,
		cardId: e.cardId,
		rating: e.rating as Grade,
		reviewedAt: e.reviewedAt,
		durationMs: typeof e.durationMs === 'number' ? e.durationMs : null,
		predicted: e.predicted
	};
}
