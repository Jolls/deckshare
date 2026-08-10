/**
 * The sending half of CLAUDE.md §6: enqueue never blocks grading, batches drain with
 * exponential backoff, and entries are only dropped once the server has acknowledged them.
 *
 * There is no durability test any more, because there is no durability: the queue is in
 * memory by design (architecture.md §11 rules out offline study). Losing unsent events to a
 * hard crash is acceptable; storing a wrong one is not, and under §2.7 the client cannot.
 */
import { describe, it, expect, vi } from 'vitest';
import { WriteQueue } from './write-queue';
import type { WireCardState, WireReviewEvent } from './types';
import { State } from '$lib/fsrs';

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

const event = (id: string): WireReviewEvent => ({
	id,
	cardId: 'card-1',
	rating: 3,
	reviewedAt: '2026-08-08T09:00:00.000Z',
	durationMs: 1200,
	predicted: { fsrsVersion: 6, state: STATE }
});

describe('WriteQueue', () => {
	it('enqueues synchronously and drops entries only after the POST resolves', async () => {
		const sent: WireReviewEvent[][] = [];
		let resolvePost: (() => void) | undefined;
		const queue = new WriteQueue({
			post: (events) => {
				sent.push(events);
				return new Promise<void>((resolve) => (resolvePost = resolve));
			}
		});

		queue.enqueue(event('a'));
		// enqueue returned without awaiting the in-flight request — invariant §2.6.
		expect(queue.size).toBe(1);
		expect(sent).toEqual([[event('a')]]);

		resolvePost?.();
		await vi.waitFor(() => expect(queue.size).toBe(0));
	});

	it('keeps entries and backs off exponentially while the network is down', async () => {
		const delays: number[] = [];
		let attempts = 0;
		const queue = new WriteQueue({
			backoffMs: [10, 20, 40],
			setTimer: (fn, ms) => {
				delays.push(ms);
				// Run at most three retries, then stop, so the test terminates.
				if (delays.length < 4) fn();
			},
			post: () => {
				attempts += 1;
				return Promise.reject(new Error('offline'));
			}
		});

		queue.enqueue(event('a'));
		await vi.waitFor(() => expect(delays.length).toBe(4));

		expect(delays).toEqual([10, 20, 40, 40]);
		expect(attempts).toBe(4);
		// Nothing was dropped: the server never acknowledged any of it.
		expect(queue.size).toBe(1);
	});

	it('batches up to batchSize per request', async () => {
		const sent: WireReviewEvent[][] = [];
		const queue = new WriteQueue({
			batchSize: 2,
			post: (events) => {
				sent.push(events);
				return Promise.resolve();
			}
		});

		for (const id of ['a', 'b', 'c']) queue.enqueue(event(id));
		await queue.drain();

		expect(
			sent
				.flat()
				.map((e) => e.id)
				.sort()
		).toEqual(['a', 'b', 'c']);
		expect(sent.every((batch) => batch.length <= 2)).toBe(true);
		expect(queue.size).toBe(0);
	});

	it('does not bypass the backoff delay when enqueue() fires during the wait', async () => {
		const sent: string[][] = [];
		let timerFn: (() => void) | undefined;
		const queue = new WriteQueue({
			backoffMs: [1000],
			// Captures the retry instead of firing it, so we control exactly when the delay elapses.
			setTimer: (fn) => {
				timerFn = fn;
			},
			post: (events) => {
				sent.push(events.map((e) => e.id));
				return Promise.reject(new Error('offline'));
			}
		});

		queue.enqueue(event('a'));
		await vi.waitFor(() => expect(sent).toHaveLength(1));

		// Still "offline" and waiting on the backoff timer: a new enqueue must not jump the delay.
		queue.enqueue(event('b'));
		await Promise.resolve();
		await Promise.resolve();
		expect(sent).toHaveLength(1);

		// The backoff delay elapses: now it retries, picking up both entries.
		timerFn?.();
		await vi.waitFor(() => expect(sent).toHaveLength(2));
		expect(sent[1]).toEqual(['a', 'b']);
	});
});
