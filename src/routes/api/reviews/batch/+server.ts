import { json, error } from '@sveltejs/kit';
import { applyReviewBatch } from '$lib/server/db/queries/review';
import { parseWireReviewEvent } from '$lib/review/wire';
import { requireUserId, respondToAccessError } from '../../_util';
import type { RequestHandler } from './$types';

/** Matches `WriteQueue`'s default `batchSize`, with headroom for a client that batches harder. */
const MAX_EVENTS = 500;

export const POST: RequestHandler = async ({ locals, request }) => {
	const userId = requireUserId(locals);

	const body: unknown = await request.json();
	const raw = (body as { events?: unknown })?.events;
	if (!Array.isArray(raw)) throw error(400, 'events must be an array');
	if (raw.length > MAX_EVENTS) throw error(413, `at most ${MAX_EVENTS} events per batch`);

	const events = raw.map(parseWireReviewEvent);
	const badIndex = events.findIndex((e) => e === null);
	if (badIndex !== -1) throw error(400, `malformed event at index ${badIndex}`);

	try {
		return json(await applyReviewBatch(userId, events as NonNullable<(typeof events)[number]>[]));
	} catch (err) {
		respondToAccessError(err);
	}
};
