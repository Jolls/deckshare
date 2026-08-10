/**
 * The in-memory review queue and the synchronous grading step (CLAUDE.md §6).
 *
 * Pure functions over a plain state value: `grade` takes the session and a rating and returns
 * the next session plus the `ReviewEvent` to queue. There is no `await` and no network call
 * anywhere in this path — that is invariant §2.6, and keeping the whole thing pure is what
 * makes it testable without a browser.
 *
 * The component owns the reactivity: it holds the session in `$state` and reassigns it.
 */
import { scheduleReview, type FsrsParams, type Grade } from '$lib/fsrs';
import type { WireReviewCard, WireReviewEvent, WireReviewSession } from './types';
import { fromWireCardState, toWireCardState } from './wire';
import { uuidv7 } from '$lib/uuid';

export interface ReviewSession {
	deckId: string;
	params: FsrsParams;
	/** Cards still to answer, in order. The head is the card on screen. */
	queue: WireReviewCard[];
	/** End of the study day: a card scheduled past it leaves the session (CLAUDE.md §5). */
	studyDayEnd: Date;
	/** Cards graded this session, counted once per grade including repeats. */
	graded: number;
}

export function startSession(payload: WireReviewSession): ReviewSession {
	return {
		deckId: payload.deckId,
		params: payload.params,
		queue: payload.cards,
		studyDayEnd: new Date(payload.studyDayEnd),
		graded: 0
	};
}

export function currentCard(session: ReviewSession): WireReviewCard | null {
	return session.queue[0] ?? null;
}

export interface GradeResult {
	session: ReviewSession;
	/** Append this to the write queue. Never sent from here. */
	event: WireReviewEvent;
}

/**
 * Grade the card at the head of the queue.
 *
 * A card whose new `due` still falls inside the study day goes back to the tail — that is how
 * learning steps come round again within a session. Anything scheduled past the day boundary
 * leaves, which is the same window the server's queue query uses, so the client and the
 * server agree on what "due today" means without the client owning the definition.
 *
 * The computed state drives the queue and the UI, and rides along on the event as `predicted`.
 * It is not what gets stored: the server recomputes the same grade from its own copy of the
 * card state and stores that, comparing `predicted` for divergence (CLAUDE.md §2.7, §6).
 */
export function grade(
	session: ReviewSession,
	rating: Grade,
	now: Date,
	durationMs: number | null = null
): GradeResult | null {
	const card = session.queue[0];
	if (!card) return null;

	const before = fromWireCardState(card.state);
	const { state: after } = scheduleReview(before, rating, now, session.params);

	const next: WireReviewCard = { ...card, state: toWireCardState(after) };
	const rest = session.queue.slice(1);

	return {
		session: {
			...session,
			queue: after.due < session.studyDayEnd ? [...rest, next] : rest,
			graded: session.graded + 1
		},
		event: {
			id: uuidv7(now.getTime()),
			cardId: card.cardId,
			rating,
			reviewedAt: now.toISOString(),
			durationMs,
			predicted: { fsrsVersion: session.params.fsrsVersion, state: next.state }
		}
	};
}
