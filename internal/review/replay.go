package review

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
)

// LoggedReview is one review_log row reduced to the two columns a replay reads.
type LoggedReview struct {
	ID         pgtype.UUID
	Rating     fsrs.Rating
	ReviewedAt time.Time
}

// loggedReviews reduces ListReviewLogForCard's rows to what a replay folds. The query's
// ORDER BY reviewed_at, id is what makes the result already sorted the way replayStates requires;
// UTC() normalises pgtype's location so a later comparison against an incoming event's timestamp
// is on one clock.
func loggedReviews(rows []db.ListReviewLogForCardRow) []LoggedReview {
	out := make([]LoggedReview, len(rows))
	for i, r := range rows {
		out[i] = LoggedReview{ID: r.ID, Rating: fsrs.Rating(r.Rating), ReviewedAt: r.ReviewedAt.Time.UTC()}
	}
	return out
}

// writeReplayedState persists a replay's tail: final is the state after the last row, lastPrior
// the state immediately before it, lastReviewedAt its reviewed_at. It writes through the
// UNGUARDED UpsertUserCardStateFromReplay -- a rebuild from review_log is the newest truth for
// the card by construction (architecture.md §6) -- so it must only ever be reached with the
// (user, card) advisory lock held. Both callers (ReplayCard, GradeBatch's out-of-order branch)
// satisfy that.
func writeReplayedState(ctx context.Context, q *db.Queries, userID, cardID pgtype.UUID,
	lastPrior, final fsrs.CardState, lastReviewedAt time.Time) error {
	return q.UpsertUserCardStateFromReplay(ctx, db.UpsertUserCardStateFromReplayParams{
		UserID:        userID,
		CardID:        cardID,
		Due:           pgtype.Timestamptz{Time: final.Due, Valid: true},
		Stability:     final.Stability,
		Difficulty:    final.Difficulty,
		State:         int16(final.State),
		Reps:          final.Reps,
		Lapses:        final.Lapses,
		ElapsedDays:   fsrs.ElapsedDays(lastPrior, lastReviewedAt),
		ScheduledDays: final.ScheduledDays,
		LearningSteps: final.LearningSteps,
		LastReview:    pgtype.Timestamptz{Time: lastReviewedAt, Valid: true},
	})
}

// replayStates folds rows (which MUST already be sorted by (reviewed_at, id)) through
// fsrs.Schedule from the zero CardState. priors[i] is the state the server would have held
// immediately before rows[i] -- which is the only truthful source of a *_before for a review
// arriving out of order (architecture.md §6: fabricating one writes permanently wrong training
// data that no recompute repairs). final is the state after the last row. Pure: no DB, no clock.
func replayStates(p fsrs.Params, rows []LoggedReview) (priors []fsrs.CardState, final fsrs.CardState, err error) {
	priors = make([]fsrs.CardState, len(rows))
	state := fsrs.CardState{}
	for i, row := range rows {
		priors[i] = state
		outcome, err := fsrs.Schedule(p, state, row.Rating, row.ReviewedAt)
		if err != nil {
			return nil, fsrs.CardState{}, err
		}
		state = outcome.CardStateAt(row.ReviewedAt)
	}
	return priors, state, nil
}

// ReplayCard rebuilds user_card_state for (user, card) from the card's full review_log history
// and writes it. This is the server-side recompute path CLAUDE.md §17 forbids deleting as
// unused: live grading's out-of-order branch calls it, and import backfill, parameter refits, and
// post-incident repair are its other callers. Must run inside a transaction it does not own,
// under the (user, card) advisory lock; the caller commits. A card with no review_log rows yet is
// a caller error -- ReplayCard has nothing to rebuild from and returns db.ErrNoRows-free zero
// state without writing anything.
func ReplayCard(ctx context.Context, tx pgx.Tx, p fsrs.Params, userID, cardID pgtype.UUID) (fsrs.CardState, error) {
	q := db.New(tx)
	rows, err := q.ListReviewLogForCard(ctx, db.ListReviewLogForCardParams{UserID: userID, CardID: cardID})
	if err != nil {
		return fsrs.CardState{}, err
	}
	if len(rows) == 0 {
		return fsrs.CardState{}, nil
	}

	logged := loggedReviews(rows)
	priors, final, err := replayStates(p, logged)
	if err != nil {
		return fsrs.CardState{}, err
	}
	lastReviewedAt := logged[len(logged)-1].ReviewedAt
	if err := writeReplayedState(ctx, q, userID, cardID, priors[len(priors)-1], final, lastReviewedAt); err != nil {
		return fsrs.CardState{}, err
	}
	return final, nil
}
