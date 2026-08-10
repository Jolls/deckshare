import { json, error } from '@sveltejs/kit';
import { applyReviewBatch } from '$lib/server/db/queries/review';
import { parseWireReviewEvent } from '$lib/review/wire';
import { requireUserId, respondToAccessError } from '../../_util';
import type { RequestHandler } from './$types';

/** Matches `WriteQueue`'s default `batchSize`, with headroom for a client that batches harder. */
const MAX_EVENTS = 500;

/**
 * How far ahead of this server a client's clock may be before its events are refused. The
 * tolerance is for ordinary skew, nothing more.
 */
const FUTURE_SKEW_MS = 5 * 60_000;

/**
 * The §6 write-queue endpoint. Thin by design: parse, authorise, delegate. The scheduling —
 * and the decision to ignore everything the client claims about memory state (§2.7) — is
 * `applyReviewBatch`'s, so there is nothing here to get wrong twice.
 */
export const POST: RequestHandler = async ({ locals, request }) => {
	const userId = requireUserId(locals);

	const body: unknown = await request.json();
	const raw = (body as { events?: unknown })?.events;
	if (!Array.isArray(raw)) throw error(400, 'events must be an array');
	if (raw.length > MAX_EVENTS) throw error(413, `at most ${MAX_EVENTS} events per batch`);

	const events = raw.map((value) => parseWireReviewEvent(value));
	const badIndex = events.findIndex((e) => e === null);
	if (badIndex !== -1) throw error(400, `malformed event at index ${badIndex}`);

	// A review cannot have happened in the future. Unbounded, `reviewedAt` is a live weapon: the
	// server schedules from it and it becomes `last_review`, so one event dated 2099 would freeze
	// that card behind the §6 `last_review <` guard for the rest of the user's life. Checked here
	// rather than in the parser: it is a judgement against *this* server's clock, and `wire.ts`
	// is shared with the client.
	const horizon = Date.now() + FUTURE_SKEW_MS;
	const futureIndex = events.findIndex((e) => e !== null && Date.parse(e.reviewedAt) > horizon);
	if (futureIndex !== -1) throw error(400, `event at index ${futureIndex} is dated in the future`);

	try {
		return json(await applyReviewBatch(userId, events as NonNullable<(typeof events)[number]>[]));
	} catch (err) {
		respondToAccessError(err);
	}
};
