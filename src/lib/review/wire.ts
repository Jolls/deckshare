/**
 * `CardState` <-> JSON. Isomorphic: the server encodes on the way out and decodes on the way
 * back in, and the client round-trips through `localStorage` in between.
 */
import type { CardState, Grade } from '$lib/fsrs';
import type { WireCardState, WireReviewEvent } from './types';

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

/**
 * Validates one entry of a `/api/reviews/batch` body.
 *
 * This is the boundary where `review_log` — unrecoverable training data (CLAUDE.md §2.5) —
 * stops being the client's word for it. A malformed number that reached the table would be
 * invisible until an optimiser fit came out wrong, so nothing is coerced: it parses or it is
 * rejected.
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
	if (!isWireCardState(e.stateBefore) || !isWireCardState(e.stateAfter)) return null;

	return {
		id: e.id,
		cardId: e.cardId,
		rating: e.rating as Grade,
		reviewedAt: e.reviewedAt,
		durationMs: typeof e.durationMs === 'number' ? e.durationMs : null,
		stateBefore: e.stateBefore,
		stateAfter: e.stateAfter
	};
}
