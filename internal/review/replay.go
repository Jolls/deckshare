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

// outcomeToCardState carries an Outcome forward into the next CardState a later Schedule/Repeat
// call reads. Outcome has no LastReview field of its own (architecture.md §6: review_log, not
// user_card_state, is what "when" belongs to during a single Schedule call) -- the caller always
// knows it, since it is the reviewedAt that produced the Outcome.
func outcomeToCardState(o fsrs.Outcome, reviewedAt time.Time) fsrs.CardState {
	return fsrs.CardState{
		Due:           o.Due,
		Stability:     o.Stability,
		Difficulty:    o.Difficulty,
		State:         o.State,
		Reps:          o.Reps,
		Lapses:        o.Lapses,
		ScheduledDays: o.ScheduledDays,
		LearningSteps: o.LearningSteps,
		LastReview:    reviewedAt,
	}
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
		state = outcomeToCardState(outcome, row.ReviewedAt)
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

	logged := make([]LoggedReview, len(rows))
	for i, r := range rows {
		logged[i] = LoggedReview{ID: r.ID, Rating: fsrs.Rating(r.Rating), ReviewedAt: r.ReviewedAt.Time.UTC()}
	}

	priors, final, err := replayStates(p, logged)
	if err != nil {
		return fsrs.CardState{}, err
	}

	lastReviewedAt := logged[len(logged)-1].ReviewedAt
	elapsedDays := fsrs.ElapsedDays(priors[len(priors)-1], lastReviewedAt)

	if err := q.UpsertUserCardStateFromReplay(ctx, db.UpsertUserCardStateFromReplayParams{
		UserID:        userID,
		CardID:        cardID,
		Due:           pgtype.Timestamptz{Time: final.Due, Valid: true},
		Stability:     final.Stability,
		Difficulty:    final.Difficulty,
		State:         int16(final.State),
		Reps:          final.Reps,
		Lapses:        final.Lapses,
		ElapsedDays:   elapsedDays,
		ScheduledDays: final.ScheduledDays,
		LearningSteps: final.LearningSteps,
		LastReview:    pgtype.Timestamptz{Time: lastReviewedAt, Valid: true},
	}); err != nil {
		return fsrs.CardState{}, err
	}

	return final, nil
}
