import fc from 'fast-check';
import { default_w, Rating } from 'ts-fsrs';
import { describe, expect, it } from 'vitest';
import { newCardState, replayReviews, scheduleReview } from './schedule';
import type { CardState, FsrsParams, Grade, ReviewEvent } from './types';

const EPOCH = new Date('2026-01-01T09:00:00.000Z');
const FSRS6_PARAMS: readonly number[] = default_w;

function paramsFor(desiredRetention: number): FsrsParams {
	return { fsrsVersion: 6, params: FSRS6_PARAMS, desiredRetention };
}

/**
 * The client never holds a `CardState` across reviews in memory alone — it goes to
 * the write queue and to `user_card_state` and comes back as JSON. Round-tripping it
 * here is what makes the parity property non-trivial: it catches state that only
 * survives as a live object reference, and Date fields that lose fidelity in transit.
 */
function throughTheWire(state: CardState): CardState {
	const raw: unknown = JSON.parse(JSON.stringify(state));
	const row = raw as Record<keyof CardState, unknown>;
	return {
		due: new Date(row.due as string),
		stability: row.stability as number,
		difficulty: row.difficulty as number,
		state: row.state as CardState['state'],
		reps: row.reps as number,
		lapses: row.lapses as number,
		elapsedDays: row.elapsedDays as number,
		scheduledDays: row.scheduledDays as number,
		learningSteps: row.learningSteps as number,
		lastReview: row.lastReview === null ? null : new Date(row.lastReview as string)
	};
}

/** Byte-identical means byte-identical: compare the serialisations, not the objects. */
function canonical(state: CardState): string {
	return JSON.stringify(state);
}

const gradeArb: fc.Arbitrary<Grade> = fc.constantFrom<Grade>(
	Rating.Again,
	Rating.Hard,
	Rating.Good,
	Rating.Easy
);

/**
 * Two scales, mixed. A uniform draw over "up to 400 days" puts P(gap ≤ 10 min) at
 * ~2e-5, which never exercises the (re)learning ladder — the only place
 * `learningSteps` does anything. The minutes-scale branch is what reaches it.
 */
const gapMinutesArb: fc.Arbitrary<number> = fc.oneof(
	fc.integer({ min: 0, max: 120 }),
	fc.integer({ min: 0, max: 60 * 24 * 400 })
);

/** Gaps span "graded twice in a minute" to "came back after a year". */
const sequenceArb: fc.Arbitrary<ReviewEvent[]> = fc
	.array(fc.record({ rating: gradeArb, gapMinutes: gapMinutesArb }), {
		minLength: 1,
		maxLength: 40
	})
	.map((steps) => {
		let at = EPOCH.getTime();
		return steps.map((step) => {
			at += step.gapMinutes * 60_000;
			return { rating: step.rating, reviewedAt: new Date(at) };
		});
	});

describe('client / server scheduling parity', () => {
	// CLAUDE.md §10.1. If this fails, everything else stops.
	it('grading incrementally and replaying review_log converge on the same state', () => {
		fc.assert(
			fc.property(
				sequenceArb,
				fc.double({ min: 0.7, max: 0.97, noNaN: true }),
				(events, desiredRetention) => {
					const params = paramsFor(desiredRetention);

					let client = newCardState(EPOCH);
					for (const event of events) {
						client = throughTheWire(
							scheduleReview(client, event.rating, event.reviewedAt, params).state
						);
					}

					const server = replayReviews(events, params, newCardState(EPOCH));

					expect(canonical(client)).toBe(canonical(server));
				}
			),
			{ numRuns: 300 }
		);
	});

	it('replays in prefixes to the same place it replays in one pass', () => {
		fc.assert(
			fc.property(sequenceArb, fc.integer({ min: 0, max: 40 }), (events, cut) => {
				const params = paramsFor(0.9);
				const split = Math.min(cut, events.length);

				const checkpoint = replayReviews(events.slice(0, split), params, newCardState(EPOCH));
				const resumed = replayReviews(events.slice(split), params, checkpoint);
				const whole = replayReviews(events, params, newCardState(EPOCH));

				expect(canonical(resumed)).toBe(canonical(whole));
			}),
			{ numRuns: 200 }
		);
	});
});

/**
 * Golden vectors. The parity property is self-referential by construction — both
 * paths fold the same primitive, so they agree whether the ts-fsrs <-> CardState
 * mapping is right or wrong. These pin the mapping itself, and they are what fails
 * loudly if a ts-fsrs upgrade changes behaviour (CLAUDE.md §3: the version must match
 * on client and server, and a silent change here is the worst failure this system
 * has). Produced with ts-fsrs 5.4.1 at `default_w` and retention 0.9.
 *
 * The first three are hand-checkable against FSRS-6's initial-state definitions:
 * S0(Again) = w[0] = 0.212, S0(Good) = w[2] = 2.3065, S0(Easy) = w[3] = 8.2956;
 * D0(Again) = w[4] = 6.4133, and D0(Easy) clamps to the floor of 1.
 */
const GOLDEN: ReadonlyArray<{
	name: string;
	steps: ReadonlyArray<{ rating: Grade; afterMinutes: number }>;
	expected: Record<string, unknown>;
}> = [
	{
		name: 'a new card rated Good waits out the second learning step',
		steps: [{ rating: Rating.Good, afterMinutes: 0 }],
		expected: {
			due: '2026-01-01T09:10:00.000Z',
			stability: 2.3065,
			difficulty: 2.11810397,
			state: 1,
			reps: 1,
			lapses: 0,
			elapsedDays: 0,
			scheduledDays: 0,
			learningSteps: 1,
			lastReview: '2026-01-01T09:00:00.000Z'
		}
	},
	{
		name: 'a new card rated Again stays on the first learning step',
		steps: [{ rating: Rating.Again, afterMinutes: 0 }],
		expected: {
			due: '2026-01-01T09:01:00.000Z',
			stability: 0.212,
			difficulty: 6.4133,
			state: 1,
			reps: 1,
			lapses: 0,
			elapsedDays: 0,
			scheduledDays: 0,
			learningSteps: 0,
			lastReview: '2026-01-01T09:00:00.000Z'
		}
	},
	{
		name: 'a new card rated Easy graduates straight to review',
		steps: [{ rating: Rating.Easy, afterMinutes: 0 }],
		expected: {
			due: '2026-01-09T09:00:00.000Z',
			stability: 8.2956,
			difficulty: 1,
			state: 2,
			reps: 1,
			lapses: 0,
			elapsedDays: 0,
			scheduledDays: 8,
			learningSteps: 0,
			lastReview: '2026-01-01T09:00:00.000Z'
		}
	},
	{
		name: 'Good then Good ten minutes later graduates off the ladder',
		steps: [
			{ rating: Rating.Good, afterMinutes: 0 },
			{ rating: Rating.Good, afterMinutes: 10 }
		],
		expected: {
			due: '2026-01-03T09:10:00.000Z',
			stability: 2.3065,
			difficulty: 2.11121424,
			state: 2,
			reps: 2,
			lapses: 0,
			elapsedDays: 0,
			scheduledDays: 2,
			learningSteps: 0,
			lastReview: '2026-01-01T09:10:00.000Z'
		}
	},
	{
		name: 'Good then Again a minute later drops back down the ladder',
		steps: [
			{ rating: Rating.Good, afterMinutes: 0 },
			{ rating: Rating.Again, afterMinutes: 1 }
		],
		expected: {
			due: '2026-01-01T09:02:00.000Z',
			stability: 0.77508398,
			difficulty: 7.39450274,
			state: 1,
			reps: 2,
			lapses: 0,
			elapsedDays: 0,
			scheduledDays: 0,
			learningSteps: 0,
			lastReview: '2026-01-01T09:01:00.000Z'
		}
	},
	{
		name: 'a graduated card reviewed four days later stretches its interval',
		steps: [
			{ rating: Rating.Good, afterMinutes: 0 },
			{ rating: Rating.Good, afterMinutes: 10 },
			{ rating: Rating.Good, afterMinutes: 4 * 24 * 60 }
		],
		expected: {
			due: '2026-01-21T09:00:00.000Z',
			stability: 16.18802274,
			difficulty: 2.1043314,
			state: 2,
			reps: 3,
			lapses: 0,
			elapsedDays: 4,
			scheduledDays: 16,
			learningSteps: 0,
			lastReview: '2026-01-05T09:00:00.000Z'
		}
	},
	{
		name: 'Again on a review card counts a lapse and enters relearning',
		steps: [
			{ rating: Rating.Good, afterMinutes: 0 },
			{ rating: Rating.Good, afterMinutes: 10 },
			{ rating: Rating.Good, afterMinutes: 4 * 24 * 60 },
			{ rating: Rating.Again, afterMinutes: 14 * 24 * 60 }
		],
		expected: {
			due: '2026-01-15T09:10:00.000Z',
			stability: 1.77029762,
			difficulty: 7.38997579,
			state: 3,
			reps: 4,
			lapses: 1,
			elapsedDays: 10,
			scheduledDays: 0,
			learningSteps: 0,
			lastReview: '2026-01-15T09:00:00.000Z'
		}
	},
	{
		name: 'Easy then Hard after two hundred days',
		steps: [
			{ rating: Rating.Easy, afterMinutes: 0 },
			{ rating: Rating.Hard, afterMinutes: 200 * 24 * 60 }
		],
		expected: {
			due: '2026-10-19T09:00:00.000Z',
			stability: 91.27715234,
			difficulty: 4.01060897,
			state: 2,
			reps: 2,
			lapses: 0,
			elapsedDays: 200,
			scheduledDays: 91,
			learningSteps: 0,
			lastReview: '2026-07-20T09:00:00.000Z'
		}
	}
];

describe('golden vectors (ts-fsrs 5.4.1, default_w, retention 0.9)', () => {
	const params: FsrsParams = { fsrsVersion: 6, params: FSRS6_PARAMS, desiredRetention: 0.9 };

	for (const vector of GOLDEN) {
		const events: ReviewEvent[] = vector.steps.map((step) => ({
			rating: step.rating,
			reviewedAt: new Date(EPOCH.getTime() + step.afterMinutes * 60_000)
		}));

		it(vector.name, () => {
			let incremental = newCardState(EPOCH);
			for (const event of events) {
				incremental = scheduleReview(incremental, event.rating, event.reviewedAt, params).state;
			}
			const replayed = replayReviews(events, params, newCardState(EPOCH));

			expect(JSON.parse(canonical(incremental))).toEqual(vector.expected);
			expect(JSON.parse(canonical(replayed))).toEqual(vector.expected);
		});
	}
});

describe('scheduleReview', () => {
	it('is deterministic for identical inputs', () => {
		const params = paramsFor(0.9);
		const state = newCardState(EPOCH);
		const first = scheduleReview(state, Rating.Good, EPOCH, params);
		const second = scheduleReview(state, Rating.Good, EPOCH, params);
		expect(canonical(first.state)).toBe(canonical(second.state));
		expect(JSON.stringify(first.log)).toBe(JSON.stringify(second.log));
	});

	it('does not mutate the state it is given', () => {
		const params = paramsFor(0.9);
		const state = Object.freeze(newCardState(EPOCH));
		const before = canonical(state);
		scheduleReview(state, Rating.Good, new Date(EPOCH.getTime() + 86_400_000), params);
		expect(canonical(state)).toBe(before);
	});

	it('records the pre-review memory state in the log and the post-review interval', () => {
		const params = paramsFor(0.9);
		const first = scheduleReview(newCardState(EPOCH), Rating.Good, EPOCH, params);
		const later = new Date(EPOCH.getTime() + 3 * 86_400_000);
		const second = scheduleReview(first.state, Rating.Good, later, params);

		expect(second.log.stabilityBefore).toBe(first.state.stability);
		expect(second.log.difficultyBefore).toBe(first.state.difficulty);
		expect(second.log.stateBefore).toBe(first.state.state);
		expect(second.log.learningStepsBefore).toBe(first.state.learningSteps);
		expect(second.log.scheduledDaysAfter).toBe(second.state.scheduledDays);
		expect(second.log.fsrsVersion).toBe(6);
	});

	// ts-fsrs migrates 17- and 19-length arrays up to 21 internally, so an older fit
	// still schedules. These are truncated FSRS-6 weights, not genuine FSRS-4.5/5
	// fits — the assertion is only that the length is accepted, not that the numbers
	// mean anything.
	it('accepts 17- and 19-length parameter arrays when fsrs_version agrees', () => {
		const now = new Date(EPOCH.getTime() + 86_400_000);
		for (const [fsrsVersion, length] of [
			[4, 17],
			[5, 19]
		] as const) {
			const params: FsrsParams = {
				fsrsVersion,
				params: FSRS6_PARAMS.slice(0, length),
				desiredRetention: 0.9
			};
			expect(() => scheduleReview(newCardState(EPOCH), Rating.Good, now, params)).not.toThrow();
		}
	});

	// The critical one. ts-fsrs clamps `w[i] || 0` rather than refusing, and NaN
	// survives the params jsonb column as null, so without this the card schedules
	// plausibly and wrongly and nothing is ever raised. The parity property cannot
	// see it: both paths share the coercion.
	it('rejects a non-finite weight instead of coercing it to zero', () => {
		const now = new Date(EPOCH.getTime() + 86_400_000);
		for (const bad of [NaN, Infinity, null, undefined]) {
			const weights = [...FSRS6_PARAMS];
			weights[7] = bad as unknown as number;
			const params: FsrsParams = { fsrsVersion: 6, params: weights, desiredRetention: 0.9 };
			expect(() => scheduleReview(newCardState(EPOCH), Rating.Good, now, params)).toThrow(
				/Non-finite FSRS parameter at index 7/
			);
		}
	});

	it('rejects a desired_retention outside (0, 1] rather than defaulting to 0.9', () => {
		const now = new Date(EPOCH.getTime() + 86_400_000);
		for (const bad of [0, -0.5, 1.5, NaN]) {
			const params: FsrsParams = {
				fsrsVersion: 6,
				params: FSRS6_PARAMS,
				desiredRetention: bad
			};
			expect(() => scheduleReview(newCardState(EPOCH), Rating.Good, now, params)).toThrow(
				/desired_retention must be in/
			);
		}
	});

	it('rejects a parameter array whose length disagrees with fsrs_version', () => {
		const params: FsrsParams = {
			fsrsVersion: 6,
			params: FSRS6_PARAMS.slice(0, 19),
			desiredRetention: 0.9
		};
		expect(() => scheduleReview(newCardState(EPOCH), Rating.Good, EPOCH, params)).toThrow(
			/expects 21 parameters/
		);
	});

	it('rejects an unknown fsrs_version', () => {
		const params: FsrsParams = { fsrsVersion: 3, params: FSRS6_PARAMS, desiredRetention: 0.9 };
		expect(() => scheduleReview(newCardState(EPOCH), Rating.Good, EPOCH, params)).toThrow(
			/Unsupported fsrs_version/
		);
	});
});
