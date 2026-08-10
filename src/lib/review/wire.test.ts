/**
 * The `/api/reviews/batch` parse boundary. Under CLAUDE.md §2.7 the server no longer stores
 * anything the client says about memory state — but `rating` and `reviewedAt` are still
 * *inputs* to the server's own scheduling, and they land in `review_log` verbatim, so this is
 * where they stop being the client's word for it.
 */
import { describe, it, expect } from 'vitest';
import { State } from '$lib/fsrs';
import { parseWireReviewEvent } from './wire';
import type { WireCardState } from './types';

const STATE: WireCardState = {
	due: '2026-08-08T10:00:00.000Z',
	stability: 1,
	difficulty: 5,
	state: State.Learning,
	reps: 1,
	lapses: 0,
	elapsedDays: 0,
	scheduledDays: 0,
	learningSteps: 1,
	lastReview: '2026-08-08T09:00:00.000Z'
};

const event = (over: Record<string, unknown> = {}) => ({
	id: 'ev-1',
	cardId: 'card-1',
	rating: 3,
	reviewedAt: '2026-08-08T09:00:00.000Z',
	durationMs: 1200,
	predicted: { fsrsVersion: 6, state: STATE },
	...over
});

describe('parseWireReviewEvent', () => {
	it('accepts a well-formed event', () => {
		expect(parseWireReviewEvent(event())).toMatchObject({ cardId: 'card-1', rating: 3 });
	});

	it('requires `predicted`, so the divergence check cannot be opted out of', () => {
		expect(parseWireReviewEvent(event({ predicted: undefined }))).toBeNull();
		expect(parseWireReviewEvent(event({ predicted: { state: STATE } }))).toBeNull();
		expect(parseWireReviewEvent(event({ predicted: { fsrsVersion: 6, state: null } }))).toBeNull();
	});

	it('rejects a non-finite number anywhere in the prediction', () => {
		expect(
			parseWireReviewEvent(
				event({ predicted: { fsrsVersion: 6, state: { ...STATE, stability: 'x' } } })
			)
		).toBeNull();
	});

	it('rejects a malformed rating, id, or timestamp', () => {
		expect(parseWireReviewEvent(event({ rating: 5 }))).toBeNull();
		expect(parseWireReviewEvent(event({ id: 42 }))).toBeNull();
		expect(parseWireReviewEvent(event({ reviewedAt: 'not-a-date' }))).toBeNull();
		expect(parseWireReviewEvent(event({ durationMs: -1 }))).toBeNull();
	});

	it('takes exactly one argument, so `array.map(parseWireReviewEvent)` cannot misfire', () => {
		// `map` passes the index as the second argument. A second parameter here — an ambient
		// clock was the tempting one — would silently be fed 0, 1, 2… from the route handler.
		expect(parseWireReviewEvent.length).toBe(1);
	});
});
