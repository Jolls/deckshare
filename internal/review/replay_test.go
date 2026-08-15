package review

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
)

func mustDefaultParams(t *testing.T) fsrs.Params {
	t.Helper()
	p, err := fsrs.NewDefaultParams(0.9)
	if err != nil {
		t.Fatalf("NewDefaultParams: %v", err)
	}
	return p
}

// TestReplayCard_ArrivalOrderConverges inserts the same five review_log rows, in several
// permutations, across separate cards, and asserts ReplayCard produces identical user_card_state
// for every permutation -- the pure-DB complement to the client-visible out-of-order test in
// internal/http/review_test.go.
func TestReplayCard_ArrivalOrderConverges(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	type row struct {
		rating     fsrs.Rating
		reviewedAt time.Time
	}
	rows := []row{
		{fsrs.Good, base},
		{fsrs.Again, base.Add(24 * time.Hour)},
		{fsrs.Good, base.Add(48 * time.Hour)},
		{fsrs.Hard, base.Add(96 * time.Hour)},
		{fsrs.Easy, base.Add(200 * time.Hour)},
	}
	permutations := [][]int{
		{0, 1, 2, 3, 4}, // in order
		{4, 3, 2, 1, 0}, // fully reversed insert order (reviewed_at still governs replay order)
		{2, 0, 4, 1, 3}, // shuffled
	}

	var want db.UserCardState
	for pi, perm := range permutations {
		f := seedFixture(t, tx)
		for _, i := range perm {
			r := rows[i]
			id := insertReviewLogFixtureRow(t, tx, f.UserID, f.CardID, r.rating, r.reviewedAt)
			_ = id
		}

		got, err := ReplayCard(ctx, tx, p, f.UserID, f.CardID)
		if err != nil {
			t.Fatalf("permutation %d: ReplayCard: %v", pi, err)
		}
		if got.Reps != int32(len(rows)) {
			t.Fatalf("permutation %d: Reps = %d, want %d", pi, got.Reps, len(rows))
		}

		row := getUserCardState(t, tx, f.UserID, f.CardID)
		if pi == 0 {
			want = row
			continue
		}
		if row.Due.Time != want.Due.Time || row.Stability != want.Stability ||
			row.Difficulty != want.Difficulty || row.State != want.State ||
			row.Reps != want.Reps || row.Lapses != want.Lapses ||
			row.ScheduledDays != want.ScheduledDays || row.LearningSteps != want.LearningSteps {
			t.Errorf("permutation %d diverged from permutation 0:\n got  %+v\n want %+v", pi, row, want)
		}
	}
}

// insertReviewLogFixtureRow writes one already-graded review_log row directly (bypassing
// GradeBatch), the way an out-of-order or imported history row would already exist before a
// replay reads it. *_before values are zeroed -- ReplayCard never reads them, only rating and
// reviewed_at (architecture.md §6).
func insertReviewLogFixtureRow(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID, rating fsrs.Rating, reviewedAt time.Time) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := tx.QueryRow(context.Background(), `
		INSERT INTO review_log (user_id, card_id, rating, reviewed_at, state_before,
			learning_steps_before, stability_before, difficulty_before, elapsed_days_before,
			scheduled_days_after, fsrs_version, review_kind)
		VALUES ($1, $2, $3, $4, 0, 0, 0, 0, 0, 0, 6, 0)
		RETURNING id`,
		userID, cardID, int16(rating), reviewedAt,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert review_log fixture row: %v", err)
	}
	return id
}
