package review

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
)

// testStudyDay returns a fixed, deterministic study-day window, offsetDays after a fixed epoch --
// arithmetic-free so tests can compute the "next day" window without depending on wall-clock DST
// rules (that arithmetic is GetStudyDayWindow's own concern, tested elsewhere).
func testStudyDay(offsetDays int) StudyDay {
	start := time.Date(2026, 6, 1, 4, 0, 0, 0, time.UTC).AddDate(0, 0, offsetDays)
	return StudyDay{Start: start, End: start.Add(24 * time.Hour)}
}

var batchTestEventSeq int64

func newBatchTestEventID() pgtype.UUID {
	batchTestEventSeq++
	var id pgtype.UUID
	_ = id.Scan(fmt.Sprintf("018f5b3e-0000-7000-8000-%012d", nextSeq()+batchTestEventSeq))
	return id
}

// gradeCards grades each card Good at reviewedAt, the way production's GradeBatch call does --
// real review_log and user_card_state rows, not hand-crafted fixture rows.
func gradeCards(t *testing.T, tx pgx.Tx, userID pgtype.UUID, reviewedAt time.Time, cardIDs []pgtype.UUID) {
	t.Helper()
	events := make([]Event, len(cardIDs))
	for i, id := range cardIDs {
		events[i] = Event{ID: newBatchTestEventID(), CardID: id, Rating: fsrs.Good, ReviewedAt: reviewedAt}
	}
	results, err := GradeBatch(context.Background(), tx, userID, reviewedAt, events)
	if err != nil {
		t.Fatalf("GradeBatch: %v", err)
	}
	for _, r := range results {
		if r.Status != StatusApplied {
			t.Fatalf("card %s: status %s, want applied", r.CardID.String(), r.Status)
		}
	}
}

// insertDueCard gives cardID a review-state user_card_state row due inside window and last
// reviewed the day before, so it is eligible today under every filter except the new-card cap
// (which does not apply to it -- it already has a user_card_state row). Stability/difficulty are
// non-zero: fsrs.PreviewAll rejects a non-New card below its minimum (schedule.go), which a
// zero-value review-state row would otherwise trip.
func insertDueCard(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID, window StudyDay) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO user_card_state (user_id, card_id, due, state, reps, stability, difficulty, last_review)
		 VALUES ($1, $2, $3, 2, 1, 2.5, 5.0, $4)`,
		userID, cardID, window.Start.Add(2*time.Hour), window.Start.Add(-24*time.Hour),
	); err != nil {
		t.Fatalf("insert due card: %v", err)
	}
}

// insertDueCards is insertDueCard for several cards at once, staggered one minute apart so the
// (due, id) ranking the rev cap orders by is deterministic across the set.
func insertDueCards(t *testing.T, tx pgx.Tx, userID pgtype.UUID, cardIDs []pgtype.UUID, window StudyDay) {
	t.Helper()
	for i, cardID := range cardIDs {
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO user_card_state (user_id, card_id, due, state, reps, stability, difficulty, last_review)
			 VALUES ($1, $2, $3, 2, 1, 2.5, 5.0, $4)`,
			userID, cardID, window.Start.Add(2*time.Hour+time.Duration(i)*time.Minute), window.Start.Add(-24*time.Hour),
		); err != nil {
			t.Fatalf("insert due card %d: %v", i, err)
		}
	}
}

// insertReviewLogRow writes one review_log row directly with an explicit state_before, bypassing
// GradeBatch -- used to test CountNewIntroducedToday's marker logic in isolation (#101 plan §0.2,
// §0.3). Mirrors insertReviewLogFixtureRow (replay_test.go) but state_before is a parameter.
func insertReviewLogRow(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID, rating fsrs.Rating, reviewedAt time.Time, stateBefore int16) {
	t.Helper()
	_, err := tx.Exec(context.Background(), `
		INSERT INTO review_log (user_id, card_id, rating, reviewed_at, state_before,
			learning_steps_before, stability_before, difficulty_before, elapsed_days_before,
			scheduled_days_after, fsrs_version, review_kind)
		VALUES ($1, $2, $3, $4, $5, 0, 0, 0, 0, 0, 6, 0)`,
		userID, cardID, int16(rating), reviewedAt, stateBefore,
	)
	if err != nil {
		t.Fatalf("insert review_log row: %v", err)
	}
}

func countNewIntroducedToday(t *testing.T, tx pgx.Tx, userID, deckID pgtype.UUID, window StudyDay) int64 {
	t.Helper()
	got, err := db.New(tx).CountNewIntroducedToday(context.Background(), db.CountNewIntroducedTodayParams{
		UserID:        userID,
		DeckID:        deckID,
		StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
		StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
	})
	if err != nil {
		t.Fatalf("CountNewIntroducedToday: %v", err)
	}
	return got
}

// TestBuildBatch_DefaultCap: 30 unseen cards, no preset override (DefaultNewPerDay) -- across the
// initial fetch and refills, exactly 20 distinct new cards are ever served (#101 plan §7.1).
func TestBuildBatch_DefaultCap(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	seedCards(t, tx, f, 29) // + f.CardID = 30 unseen total
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)

	served := map[pgtype.UUID]bool{}
	cur := Cursor{AtStart: true}
	exhausted := false
	for i := 0; i < 10 && !exhausted; i++ {
		batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, cur, 7, now)
		if err != nil {
			t.Fatalf("BuildBatch: %v", err)
		}
		for _, c := range batch.Cards {
			served[c.CardID] = true
		}
		exhausted = batch.Exhausted
		if !exhausted {
			cur, err = DecodeCursor(batch.Cursor)
			if err != nil {
				t.Fatalf("DecodeCursor: %v", err)
			}
		}
	}
	if !exhausted {
		t.Fatalf("did not reach Exhausted within 10 fetches")
	}
	if len(served) != 20 {
		t.Errorf("served %d distinct new cards, want 20", len(served))
	}
}

// TestBuildBatch_AtLimitDueCardsStillFlow: 20 introductions already logged today (via real
// GradeBatch calls) plus 3 further unseen cards plus 1 due card -- zero unseen cards are served,
// the due card is. This is the issue's core assertion (#101 plan §7.2).
func TestBuildBatch_AtLimitDueCardsStillFlow(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 23) // + f.CardID = 24 total
	all := append([]pgtype.UUID{f.CardID}, extra...)
	toIntroduce, dueCard := all[0:20], all[23] // all[20:23] stay genuinely unseen, untouched

	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	gradeCards(t, tx, f.UserID, now, toIntroduce)
	insertDueCard(t, tx, f.UserID, dueCard, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, Cursor{AtStart: true}, 30, now)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if len(batch.Cards) != 1 {
		t.Fatalf("got %d cards, want 1 (the due card only)", len(batch.Cards))
	}
	if batch.Cards[0].CardID != dueCard || batch.Cards[0].Unseen {
		t.Errorf("got card %s unseen=%v, want the due card with unseen=false", batch.Cards[0].CardID.String(), batch.Cards[0].Unseen)
	}
}

// TestBuildBatch_FreshStudyDayResets: the same 25-card deck, capped out on day one, serves its
// remaining unseen cards once the study day rolls over (#101 plan §7.3).
func TestBuildBatch_FreshStudyDayResets(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	seedCards(t, tx, f, 24) // + f.CardID = 25 total

	day1 := testStudyDay(0)
	now1 := day1.Start.Add(time.Hour)

	batch1, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", day1, DefaultNewPerDay, DefaultRevPerDay, Cursor{AtStart: true}, 30, now1)
	if err != nil {
		t.Fatalf("BuildBatch day1: %v", err)
	}
	if len(batch1.Cards) != 20 {
		t.Fatalf("day1: got %d cards, want 20", len(batch1.Cards))
	}
	introducedDay1 := make([]pgtype.UUID, len(batch1.Cards))
	served := map[pgtype.UUID]bool{}
	for i, c := range batch1.Cards {
		introducedDay1[i] = c.CardID
		served[c.CardID] = true
	}
	gradeCards(t, tx, f.UserID, now1, introducedDay1)

	batch1b, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", day1, DefaultNewPerDay, DefaultRevPerDay, Cursor{AtStart: true}, 30, now1)
	if err != nil {
		t.Fatalf("BuildBatch day1 refetch: %v", err)
	}
	if len(batch1b.Cards) != 0 {
		t.Fatalf("day1 refetch: got %d cards, want 0 (allowance exhausted)", len(batch1b.Cards))
	}

	// day2's fetch may legitimately also return some of the 20 day1 cards as due reviews (FSRS
	// can schedule a first "Good" rating's next interval as soon as tomorrow) -- that's correct
	// scheduling, not a cap concern. What resetting the allowance means is specifically that the
	// 5 never-introduced cards are servable again as unseen.
	day2 := testStudyDay(1)
	now2 := day2.Start.Add(time.Hour)
	batch2, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", day2, DefaultNewPerDay, DefaultRevPerDay, Cursor{AtStart: true}, 30, now2)
	if err != nil {
		t.Fatalf("BuildBatch day2: %v", err)
	}
	unseenDay2 := map[pgtype.UUID]bool{}
	for _, c := range batch2.Cards {
		if c.Unseen {
			unseenDay2[c.CardID] = true
			if served[c.CardID] {
				t.Errorf("day2: card %s was already introduced on day1 but reappeared as unseen", c.CardID.String())
			}
		}
	}
	if len(unseenDay2) != 5 {
		t.Errorf("day2: got %d unseen cards, want 5 (the remaining never-introduced cards)", len(unseenDay2))
	}
}

// TestBuildBatch_ZeroPerDay: perDay=0 blocks every unseen card while a due card still flows
// (#101 plan §7.4).
func TestBuildBatch_ZeroPerDay(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 5) // + f.CardID = 6 total
	dueCard := extra[len(extra)-1]

	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCard(t, tx, f.UserID, dueCard, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, 0, DefaultRevPerDay, Cursor{AtStart: true}, 30, now)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if len(batch.Cards) != 1 || batch.Cards[0].CardID != dueCard || batch.Cards[0].Unseen {
		t.Fatalf("got %d cards, want exactly the due card", len(batch.Cards))
	}
}

// TestBuildBatch_RefillDoesNotOvershoot: serve 20, grade 12, refill -- zero further new cards,
// even though 5 further never-served unseen cards exist (#101 plan §7.5, plan finding §0.4).
func TestBuildBatch_RefillDoesNotOvershoot(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	seedCards(t, tx, f, 24) // + f.CardID = 25 total unseen
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)

	batch1, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, Cursor{AtStart: true}, 20, now)
	if err != nil {
		t.Fatalf("BuildBatch initial: %v", err)
	}
	if len(batch1.Cards) != 20 {
		t.Fatalf("initial: got %d cards, want 20", len(batch1.Cards))
	}
	graded := make([]pgtype.UUID, 12)
	for i := 0; i < 12; i++ {
		graded[i] = batch1.Cards[i].CardID
	}
	gradeCards(t, tx, f.UserID, now, graded)

	cur, err := DecodeCursor(batch1.Cursor)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	batch2, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, cur, 20, now)
	if err != nil {
		t.Fatalf("BuildBatch refill: %v", err)
	}
	if len(batch2.Cards) != 0 {
		t.Errorf("refill: got %d cards, want 0", len(batch2.Cards))
	}
	if !batch2.Exhausted {
		t.Errorf("refill: Exhausted = false, want true")
	}
}

// TestBuildBatch_RevZeroPerDay: rev.perDay=0 blocks every due card while new cards still flow,
// independent of the new-card cap (#115).
func TestBuildBatch_RevZeroPerDay(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 5) // + f.CardID = 6 unseen total
	dueCards := extra[:3]
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCards(t, tx, f.UserID, dueCards, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, 0, Cursor{AtStart: true}, 30, now)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if len(batch.Cards) != 3 {
		t.Fatalf("got %d cards, want 3 (the unseen cards only)", len(batch.Cards))
	}
	for _, c := range batch.Cards {
		if !c.Unseen {
			t.Errorf("card %s: unseen=false, want true (due cards must be fully blocked)", c.CardID.String())
		}
	}
}

// TestBuildBatch_RevAtLimitNewCardsStillFlow: rev.perDay=3 with 3 already reviewed today plus 2
// further due cards plus 1 unseen card -- zero further due cards are served, the unseen card is
// (the #115 counterpart of TestBuildBatch_AtLimitDueCardsStillFlow).
func TestBuildBatch_RevAtLimitNewCardsStillFlow(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 5) // + f.CardID = 6 total
	allDue := append([]pgtype.UUID{f.CardID}, extra[:4]...)
	unseenCard := extra[4]

	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCards(t, tx, f.UserID, allDue, window)
	toReview, blocked := allDue[0:3], allDue[3:5]
	gradeCards(t, tx, f.UserID, now, toReview)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, 3, Cursor{AtStart: true}, 30, now)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	served := map[pgtype.UUID]bool{}
	for _, c := range batch.Cards {
		served[c.CardID] = true
	}
	if len(batch.Cards) != 1 || !served[unseenCard] {
		t.Fatalf("got %d cards (served=%v), want exactly the unseen card", len(batch.Cards), served)
	}
	for _, id := range blocked {
		if served[id] {
			t.Errorf("blocked due card %s was served past the rev cap", id.String())
		}
	}
}

// TestBuildBatch_RevCapHoldsAcrossRefills: rev.perDay=3 with 5 due cards available -- exactly 3
// distinct due cards are ever served across the initial fetch and refills, mirroring
// TestBuildBatch_DefaultCap's new-card cap assertion for the review side.
func TestBuildBatch_RevCapHoldsAcrossRefills(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 4) // + f.CardID = 5 total, all due
	allDue := append([]pgtype.UUID{f.CardID}, extra...)
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCards(t, tx, f.UserID, allDue, window)

	served := map[pgtype.UUID]bool{}
	cur := Cursor{AtStart: true}
	exhausted := false
	for i := 0; i < 10 && !exhausted; i++ {
		batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, 3, cur, 2, now)
		if err != nil {
			t.Fatalf("BuildBatch: %v", err)
		}
		for _, c := range batch.Cards {
			served[c.CardID] = true
		}
		exhausted = batch.Exhausted
		if !exhausted {
			cur, err = DecodeCursor(batch.Cursor)
			if err != nil {
				t.Fatalf("DecodeCursor: %v", err)
			}
		}
	}
	if !exhausted {
		t.Fatalf("did not reach Exhausted within 10 fetches")
	}
	if len(served) != 3 {
		t.Errorf("served %d distinct due cards, want 3", len(served))
	}
}

// TestCountNewIntroducedToday_LapseNotCounted: a review_log row carrying state_before = 2 (a
// lapse) leaves the day's introduction count untouched (#101 plan §7.6, plan finding §0.2).
func TestCountNewIntroducedToday_LapseNotCounted(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	window := testStudyDay(0)
	insertReviewLogRow(t, tx, f.UserID, f.CardID, fsrs.Again, window.Start.Add(time.Hour), 2)

	if got := countNewIntroducedToday(t, tx, f.UserID, f.DeckID, window); got != 0 {
		t.Errorf("CountNewIntroducedToday = %d, want 0", got)
	}
}

// TestCountNewIntroducedToday_DuplicateRowsCountOnce: two state_before = 0 rows for the same card
// (the out-of-order-replay shape, plan finding §0.3) consume exactly one of the allowance (#101
// plan §7.7).
func TestCountNewIntroducedToday_DuplicateRowsCountOnce(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	window := testStudyDay(0)
	insertReviewLogRow(t, tx, f.UserID, f.CardID, fsrs.Good, window.Start.Add(time.Hour), 0)
	insertReviewLogRow(t, tx, f.UserID, f.CardID, fsrs.Again, window.Start.Add(2*time.Hour), 0)

	if got := countNewIntroducedToday(t, tx, f.UserID, f.DeckID, window); got != 1 {
		t.Errorf("CountNewIntroducedToday = %d, want 1", got)
	}
}
