/**
 * The durable half of CLAUDE.md §6: entries survive a tab close, drain in batches with
 * exponential backoff, and are only dropped once the server has acknowledged them.
 */
import { describe, it, expect, vi } from 'vitest';
import { WriteQueue, type QueueStorage } from './write-queue';
import type { WireCardState, WireReviewEvent } from './types';
import { State } from '$lib/fsrs';

function memoryStorage(initial: Record<string, string> = {}): QueueStorage & {
	data: Record<string, string>;
} {
	const data = { ...initial };
	return {
		data,
		getItem: (k) => data[k] ?? null,
		setItem: (k, v) => void (data[k] = v)
	};
}

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
	stateBefore: STATE,
	stateAfter: STATE
});

const KEY = 'enshu:review-write-queue';

describe('WriteQueue', () => {
	it('enqueues synchronously and drops entries only after the POST resolves', async () => {
		const sent: WireReviewEvent[][] = [];
		let resolvePost: (() => void) | undefined;
		const queue = new WriteQueue({
			storage: memoryStorage(),
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

	it('persists to storage so a tab closed mid-session loses nothing', async () => {
		const storage = memoryStorage();
		const first = new WriteQueue({ storage, post: () => Promise.reject(new Error('offline')) });
		first.enqueue(event('a'));
		first.enqueue(event('b'));
		await vi.waitFor(() => expect(JSON.parse(storage.data[KEY] ?? '[]')).toHaveLength(2));

		// A fresh page load, same storage: the queue picks the events back up and sends them.
		const sent: WireReviewEvent[][] = [];
		const second = new WriteQueue({
			storage,
			post: (events) => {
				sent.push(events);
				return Promise.resolve();
			}
		});
		expect(second.size).toBe(2);

		await second.drain();
		expect(sent).toEqual([[event('a'), event('b')]]);
		expect(second.size).toBe(0);
		expect(JSON.parse(storage.data[KEY] ?? 'null')).toEqual([]);
	});

	it('keeps entries and backs off exponentially while the network is down', async () => {
		const delays: number[] = [];
		let attempts = 0;
		const queue = new WriteQueue({
			storage: memoryStorage(),
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
			storage: memoryStorage(),
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
			storage: memoryStorage(),
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

	it('survives unusable storage without losing the in-memory queue', async () => {
		const sent: WireReviewEvent[][] = [];
		const queue = new WriteQueue({
			storage: {
				getItem: () => {
					throw new Error('blocked');
				},
				setItem: () => {
					throw new Error('quota exceeded');
				}
			},
			post: (events) => {
				sent.push(events);
				return Promise.resolve();
			}
		});

		queue.enqueue(event('a'));
		await queue.drain();
		expect(sent).toEqual([[event('a')]]);
	});
});
