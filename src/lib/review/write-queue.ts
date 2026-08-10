/**
 * The write queue (CLAUDE.md §6).
 *
 * `enqueue` is synchronous and returns immediately — it appends and nudges a background
 * drain. Grading never awaits any of this (invariant §2.6).
 *
 * The drain POSTs whole batches to `/api/reviews/batch` with exponential backoff, and only
 * drops entries the server has acknowledged. The server contract is idempotent on the
 * client-generated event id, so a retry after an ambiguous failure (response lost, tab
 * closed mid-flight) is always safe — which is what lets this drop entries on success and
 * keep them on anything else, with no other bookkeeping.
 *
 * **In memory only, deliberately.** Batching here is to be kind to mobile radios, not to
 * build a durable offline log: architecture.md §11 rules out offline study, so there is
 * nothing for a `localStorage` copy to serve. Losing a handful of unsent events to a hard
 * crash is acceptable — missing rows weaken an optimiser fit, where a wrong row would
 * corrupt it (§2.5), and under §2.7 a wrong row is not something the client can produce.
 *
 * Transport is injected so the whole thing is testable in Node.
 */
import type { WireReviewEvent } from './types';

export interface WriteQueueOptions {
	/** Sends one batch. Resolves on 2xx, rejects on anything else. */
	post: (events: WireReviewEvent[]) => Promise<void>;
	/** Largest batch per request. */
	batchSize?: number;
	/** Backoff delays in ms, indexed by consecutive-failure count; the last one repeats. */
	backoffMs?: readonly number[];
	/** Injected so tests don't wait in real time. */
	setTimer?: (fn: () => void, ms: number) => void;
	/**
	 * Called whenever the number of unacknowledged events changes. The queue owns that number,
	 * so a UI showing it subscribes rather than re-reading `size` at moments it happens to
	 * think are interesting — a batch that drains during a backoff wait, with no further
	 * grading, is exactly when a polled counter goes stale and exactly when it matters.
	 */
	onPendingChange?: (pending: number) => void;
}

const DEFAULT_BACKOFF = [1_000, 2_000, 5_000, 15_000, 30_000, 60_000] as const;

export class WriteQueue {
	private readonly options: Required<WriteQueueOptions>;
	private pending: WireReviewEvent[] = [];
	/** The in-flight drain, so concurrent callers await the same one instead of racing. */
	private draining: Promise<void> | null = null;
	private retryPending = false;
	/** True while waiting out a backoff delay, so an `enqueue()` in that window doesn't jump it. */
	private retryScheduled = false;
	private failures = 0;

	constructor(options: WriteQueueOptions) {
		this.options = {
			batchSize: 50,
			backoffMs: DEFAULT_BACKOFF,
			setTimer: (fn, ms) => void setTimeout(fn, ms),
			onPendingChange: () => {},
			...options
		};
	}

	/** Events accepted but not yet acknowledged by the server. */
	get size(): number {
		return this.pending.length;
	}

	/** Synchronous. Appends, then schedules a drain. Never throws on a network problem. */
	enqueue(event: WireReviewEvent): void {
		this.pending.push(event);
		this.options.onPendingChange(this.pending.length);
		void this.drain();
	}

	/**
	 * Sends as many batches as it can. Resolves once the queue is empty or a send failed; on
	 * failure it schedules its own retry, so callers never need to.
	 *
	 * A second call while one is in flight returns the same promise rather than starting a
	 * parallel drain — two drains would send the same batch twice. (Harmless, because the
	 * server contract is idempotent, but pointless.)
	 */
	drain(): Promise<void> {
		if (this.draining) return this.draining;
		// A backoff wait is in progress: let the scheduled retry fire on its own timer instead
		// of letting this call jump the delay.
		if (this.retryScheduled) return Promise.resolve();
		return this.startRun();
	}

	private startRun(): Promise<void> {
		const done = this.run().finally(() => {
			this.draining = null;
			if (this.retryPending) {
				this.retryPending = false;
				this.scheduleRetry();
			}
		});
		this.draining = done;
		return done;
	}

	private async run(): Promise<void> {
		while (this.pending.length > 0) {
			const batch = this.pending.slice(0, this.options.batchSize);
			try {
				await this.options.post(batch);
			} catch {
				this.failures += 1;
				this.retryPending = true;
				return;
			}
			// Re-read `pending`: `enqueue` may have appended while the request was in flight.
			const ids = new Set(batch.map((e) => e.id));
			this.pending = this.pending.filter((e) => !ids.has(e.id));
			this.options.onPendingChange(this.pending.length);
			this.failures = 0;
		}
	}

	private scheduleRetry(): void {
		this.retryScheduled = true;
		const delays = this.options.backoffMs;
		const delay = delays[Math.min(this.failures - 1, delays.length - 1)] ?? 1_000;
		this.options.setTimer(() => {
			this.retryScheduled = false;
			void this.startRun();
		}, delay);
	}
}

/** POSTs a batch to the §6 endpoint. Rejects on any non-2xx so the queue keeps the entries. */
export async function postReviewBatch(events: WireReviewEvent[]): Promise<void> {
	const response = await fetch('/api/reviews/batch', {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		body: JSON.stringify({ events })
	});
	if (!response.ok) throw new Error(`/api/reviews/batch responded ${response.status}`);
}
