/**
 * The comparison rules of architecture.md §6, in isolation from the database. The tolerances
 * are the whole substance of the check: too tight and the counter alarms on float noise until
 * someone loosens it out of existence, too loose and a real skew slips past.
 */
import { describe, it, expect, vi } from 'vitest';
import { State, type CardState } from '$lib/fsrs';
import { toWireCardState } from '$lib/review/wire';
import { compareToPrediction, divergenceCount, reportDivergence } from './divergence';

const SERVER: CardState = {
	due: new Date('2026-08-12T09:00:00.400Z'),
	stability: 3.12345678,
	difficulty: 5.5,
	state: State.Review,
	reps: 4,
	lapses: 1,
	elapsedDays: 2,
	scheduledDays: 4,
	learningSteps: 0,
	lastReview: new Date('2026-08-08T09:00:00.000Z')
};

const predicted = (over: Partial<ReturnType<typeof toWireCardState>> = {}) => ({
	...toWireCardState(SERVER),
	...over
});

describe('compareToPrediction', () => {
	it('accepts an identical prediction', () => {
		expect(compareToPrediction(SERVER, predicted())).toEqual([]);
	});

	it('tolerates float noise inside 1e-6 on stability and difficulty', () => {
		expect(
			compareToPrediction(
				SERVER,
				predicted({ stability: SERVER.stability + 5e-7, difficulty: SERVER.difficulty - 5e-7 })
			)
		).toEqual([]);
	});

	it('flags stability and difficulty past 1e-6', () => {
		const fields = compareToPrediction(SERVER, predicted({ stability: SERVER.stability + 1e-5 }));
		expect(fields.map((f) => f.field)).toEqual(['stability']);
	});

	it('tolerates sub-second disagreement on due, and flags a whole second', () => {
		expect(compareToPrediction(SERVER, predicted({ due: '2026-08-12T09:00:00.900Z' }))).toEqual([]);
		expect(
			compareToPrediction(SERVER, predicted({ due: '2026-08-12T09:00:01.400Z' })).map(
				(f) => f.field
			)
		).toEqual(['due']);
	});

	it('compares state, reps and lapses exactly, and names every field that moved', () => {
		const fields = compareToPrediction(
			SERVER,
			predicted({ state: State.Learning, reps: 99, lapses: 0 })
		);
		expect(fields.map((f) => f.field)).toEqual(['state', 'reps', 'lapses']);
		expect(fields[1]).toEqual({ field: 'reps', server: 4, client: 99 });
	});
});

describe('reportDivergence', () => {
	it('increments the counter and emits one structured line', () => {
		const before = divergenceCount();
		const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
		let lines: string[];
		try {
			reportDivergence({
				userId: 'u',
				cardId: 'c',
				eventId: 'e',
				serverFsrsVersion: 6,
				clientFsrsVersion: 5,
				fields: [{ field: 'reps', server: 4, client: 99 }]
			});
			lines = warn.mock.calls.map((call) => String(call[0]));
		} finally {
			warn.mockRestore();
		}

		expect(divergenceCount()).toBe(before + 1);
		expect(lines).toHaveLength(1);
		expect(JSON.parse(lines[0] ?? '{}')).toMatchObject({
			event: 'fsrs_divergence',
			cardId: 'c',
			serverFsrsVersion: 6,
			clientFsrsVersion: 5
		});
	});
});
