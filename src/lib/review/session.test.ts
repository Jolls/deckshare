/**
 * The synchronous, local grading path (CLAUDE.md §6 steps 1–4, invariant §2.6).
 */
import { describe, it, expect } from 'vitest';
import { defaultFsrsParams, newCardState, Rating, scheduleReview, State } from '$lib/fsrs';
import { grade, currentCard, startSession } from './session';
import { toWireCardState, fromWireCardState } from './wire';
import type { WireReviewCard, WireReviewSession } from './types';

const PARAMS = defaultFsrsParams();
const NOW = new Date('2026-08-08T09:00:00.000Z');
/** 04:00 local the next day, the default rollover (CLAUDE.md §5). */
const DAY_END = new Date('2026-08-09T04:00:00.000Z');

function card(id: string): WireReviewCard {
	return {
		cardId: id,
		noteId: `note-${id}`,
		ordinal: 0,
		front: '<p>front</p>',
		back: '<p>back</p>',
		state: toWireCardState(newCardState(NOW))
	};
}

function session(ids: string[]): WireReviewSession {
	return {
		deckId: 'deck-1',
		cards: ids.map(card),
		params: PARAMS,
		studyDayEnd: DAY_END.toISOString()
	};
}

describe('review session', () => {
	it('produces the same state the isomorphic wrapper does, and a matching event', () => {
		const s = startSession(session(['a']));
		const result = grade(s, Rating.Good, NOW, 1500);
		if (!result) throw new Error('expected a graded card');

		const expected = scheduleReview(newCardState(NOW), Rating.Good, NOW, PARAMS).state;
		expect(fromWireCardState(result.event.stateAfter)).toEqual(expected);
		expect(result.event.stateBefore).toEqual(toWireCardState(newCardState(NOW)));
		expect(result.event).toMatchObject({
			cardId: 'a',
			rating: Rating.Good,
			reviewedAt: NOW.toISOString(),
			durationMs: 1500
		});
		// A client-generated UUIDv7 — the write queue's idempotency key.
		expect(result.event.id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-/);
	});

	it('advances to the next card immediately', () => {
		const s = startSession(session(['a', 'b']));
		const result = grade(s, Rating.Easy, NOW);
		if (!result) throw new Error('expected a graded card');

		// Easy on a new card schedules days out, so `a` leaves the session entirely.
		expect(currentCard(result.session)?.cardId).toBe('b');
		expect(result.session.queue).toHaveLength(1);
		expect(result.session.graded).toBe(1);
	});

	it('brings a card still due inside the study day back round', () => {
		const s = startSession(session(['a', 'b']));
		const result = grade(s, Rating.Again, NOW);
		if (!result) throw new Error('expected a graded card');

		// Again puts a new card into learning with a minutes-scale interval.
		expect(fromWireCardState(result.event.stateAfter).state).toBe(State.Learning);
		expect(result.session.queue.map((c) => c.cardId)).toEqual(['b', 'a']);
		// The requeued copy carries the updated state, not the stale one.
		expect(result.session.queue[1]?.state).toEqual(result.event.stateAfter);
	});

	it('does not mutate the session it was given', () => {
		const s = startSession(session(['a', 'b']));
		const before = structuredClone(s.queue);
		grade(s, Rating.Good, NOW);
		expect(s.queue).toEqual(before);
	});

	it('returns null once the queue is empty', () => {
		const s = startSession(session([]));
		expect(currentCard(s)).toBeNull();
		expect(grade(s, Rating.Good, NOW)).toBeNull();
	});
});
