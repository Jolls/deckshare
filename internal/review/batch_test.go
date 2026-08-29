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

// dueCardOffset is how far before every test's now (window.Start.Add(time.Hour)) insertDueCard
// et al. place a card's due -- eligibility is due <= now with no look-ahead, so due must land at
// or before now, not merely "inside the study day" the way it could when the query still allowed
// due < study_day_end. 30 minutes leaves headroom under insertDueCards' per-card staggering
// (up to a few dozen cards, one minute apart) without crossing now.
const dueCardOffset = 30 * time.Minute

// insertDueCard gives cardID a review-state user_card_state row already due (at or before this
// test file's now) and last reviewed the day before, so it is eligible today under every filter
// except the new-card cap (which does not apply to it -- it already has a user_card_state row).
// Stability/difficulty are non-zero: fsrs.PreviewAll rejects a non-New card below its minimum
// (schedule.go), which a zero-value review-state row would otherwise trip.
func insertDueCard(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID, window StudyDay) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO user_card_state (user_id, card_id, due, state, reps, stability, difficulty, last_review)
		 VALUES ($1, $2, $3, 2, 1, 2.5, 5.0, $4)`,
		userID, cardID, window.Start.Add(dueCardOffset), window.Start.Add(-24*time.Hour),
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
			userID, cardID, window.Start.Add(dueCardOffset+time.Duration(i)*time.Minute), window.Start.Add(-24*time.Hour),
		); err != nil {
			t.Fatalf("insert due card %d: %v", i, err)
		}
	}
}

// insertDueCardWithSchedule is insertDueCard with an explicit scheduled_days, for rev.order
// intervalAsc/intervalDesc tests (#116) where the ordering has to be driven by something other
// than due date.
func insertDueCardWithSchedule(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID, window StudyDay, scheduledDays int32) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO user_card_state (user_id, card_id, due, state, reps, stability, difficulty, last_review, scheduled_days)
		 VALUES ($1, $2, $3, 2, 1, 2.5, 5.0, $4, $5)`,
		userID, cardID, window.Start.Add(dueCardOffset), window.Start.Add(-24*time.Hour), scheduledDays,
	); err != nil {
		t.Fatalf("insert due card with schedule: %v", err)
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
		batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, cur, 7, now, 0)
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

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
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

	batch1, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", day1, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now1, 0)
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

	batch1b, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", day1, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now1, 0)
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
	batch2, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", day2, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now2, 0)
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

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, 0, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
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

	batch1, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 20, now, 0)
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
	batch2, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, NewMixAfterReviews, cur, 20, now, 0)
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

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, 0, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
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

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, 3, RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
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
		batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, 3, RevOrderDue, NewMixAfterReviews, cur, 2, now, 0)
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

// TestBuildBatch_RevOrderIntervalAsc: three due cards with distinct scheduled_days, seeded out of
// id order -- rev.order=intervalAsc serves them shortest interval first regardless of id or due
// date (#116).
func TestBuildBatch_RevOrderIntervalAsc(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 2)
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)

	// f.CardID=30d, extra[0]=10d, extra[1]=20d -- ascending order is extra[0], extra[1], f.CardID.
	insertDueCardWithSchedule(t, tx, f.UserID, f.CardID, window, 30)
	insertDueCardWithSchedule(t, tx, f.UserID, extra[0], window, 10)
	insertDueCardWithSchedule(t, tx, f.UserID, extra[1], window, 20)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay,
		RevOrderIntervalAsc, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	want := []pgtype.UUID{extra[0], extra[1], f.CardID}
	if len(batch.Cards) != len(want) {
		t.Fatalf("got %d cards, want %d", len(batch.Cards), len(want))
	}
	for i, c := range batch.Cards {
		if c.CardID != want[i] {
			t.Errorf("position %d: got card %s, want %s", i, c.CardID.String(), want[i].String())
		}
	}
}

// TestBuildBatch_RevOrderIntervalDesc: the same three cards, longest interval first.
func TestBuildBatch_RevOrderIntervalDesc(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 2)
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)

	insertDueCardWithSchedule(t, tx, f.UserID, f.CardID, window, 30)
	insertDueCardWithSchedule(t, tx, f.UserID, extra[0], window, 10)
	insertDueCardWithSchedule(t, tx, f.UserID, extra[1], window, 20)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay,
		RevOrderIntervalDesc, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	want := []pgtype.UUID{f.CardID, extra[1], extra[0]}
	if len(batch.Cards) != len(want) {
		t.Fatalf("got %d cards, want %d", len(batch.Cards), len(want))
	}
	for i, c := range batch.Cards {
		if c.CardID != want[i] {
			t.Errorf("position %d: got card %s, want %s", i, c.CardID.String(), want[i].String())
		}
	}
}

// TestBuildBatch_RevOrderRandom_StableWithinDay: rev.order=random reshuffles per study day but
// two fetches within the same study day (the same hash_seed, #116) return the identical order --
// the property pagination across refills depends on.
func TestBuildBatch_RevOrderRandom_StableWithinDay(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 7)
	allDue := append([]pgtype.UUID{f.CardID}, extra...)
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCards(t, tx, f.UserID, allDue, window)

	fetch := func() []pgtype.UUID {
		batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay,
			RevOrderRandom, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
		if err != nil {
			t.Fatalf("BuildBatch: %v", err)
		}
		ids := make([]pgtype.UUID, len(batch.Cards))
		for i, c := range batch.Cards {
			ids[i] = c.CardID
		}
		return ids
	}

	order1 := fetch()
	order2 := fetch()
	if len(order1) != len(allDue) {
		t.Fatalf("got %d cards, want %d", len(order1), len(allDue))
	}
	for i := range order1 {
		if order1[i] != order2[i] {
			t.Errorf("position %d: order1=%s order2=%s, want the same order within one study day", i, order1[i].String(), order2[i].String())
		}
	}
}

// TestBuildBatch_RevOrderRandom_ReshufflesNextDay: the same deck, fetched on two different study
// days, gets two different orderings (overwhelmingly likely with 8 cards and an md5-derived key --
// a false failure here would need every one of 8 cards to land in the same relative position by
// chance across an independent hash_seed).
func TestBuildBatch_RevOrderRandom_ReshufflesNextDay(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 7)
	allDue := append([]pgtype.UUID{f.CardID}, extra...)
	day1 := testStudyDay(0)
	insertDueCards(t, tx, f.UserID, allDue, day1)

	fetch := func(window StudyDay) []pgtype.UUID {
		batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay,
			RevOrderRandom, NewMixAfterReviews, Cursor{AtStart: true}, 30, window.Start.Add(time.Hour), 0)
		if err != nil {
			t.Fatalf("BuildBatch: %v", err)
		}
		ids := make([]pgtype.UUID, len(batch.Cards))
		for i, c := range batch.Cards {
			ids[i] = c.CardID
		}
		return ids
	}

	order1 := fetch(day1)
	order2 := fetch(testStudyDay(1))
	if len(order1) != len(order2) {
		t.Fatalf("got %d and %d cards, want equal counts", len(order1), len(order2))
	}
	same := true
	for i := range order1 {
		if order1[i] != order2[i] {
			same = false
			break
		}
	}
	if same {
		t.Errorf("day1 and day2 orders are identical, want a reshuffle across study days: %v", order1)
	}
}

// TestBuildBatch_NewMixBeforeReviews: new.mix=beforeReviews serves the unseen card ahead of the
// due card, the reverse of the afterReviews default (#116).
func TestBuildBatch_NewMixBeforeReviews(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 1)
	unseenCard := extra[0]
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCard(t, tx, f.UserID, f.CardID, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay,
		RevOrderDue, NewMixBeforeReviews, Cursor{AtStart: true}, 30, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if len(batch.Cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(batch.Cards))
	}
	if batch.Cards[0].CardID != unseenCard || !batch.Cards[0].Unseen {
		t.Errorf("position 0: got card %s unseen=%v, want the unseen card first", batch.Cards[0].CardID.String(), batch.Cards[0].Unseen)
	}
	if batch.Cards[1].CardID != f.CardID || batch.Cards[1].Unseen {
		t.Errorf("position 1: got card %s unseen=%v, want the due card second", batch.Cards[1].CardID.String(), batch.Cards[1].Unseen)
	}
}

// TestBuildBatch_NewMixMixed_ServesBoth: new.mix=mixed serves both due and unseen cards in one
// batch (the two-query interleave path, #116) without losing or duplicating either.
func TestBuildBatch_NewMixMixed_ServesBoth(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 5) // + f.CardID = 6 total
	dueCards := append([]pgtype.UUID{f.CardID}, extra[:2]...)
	unseenCards := extra[2:]
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCards(t, tx, f.UserID, dueCards, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay,
		RevOrderDue, NewMixMixed, Cursor{AtStart: true}, 30, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if !batch.Exhausted {
		t.Errorf("Exhausted = false, want true (both sub-fetches undersized)")
	}
	seenDue, seenUnseen := map[pgtype.UUID]bool{}, map[pgtype.UUID]bool{}
	for _, c := range batch.Cards {
		if c.Unseen {
			seenUnseen[c.CardID] = true
		} else {
			seenDue[c.CardID] = true
		}
	}
	if len(seenDue) != len(dueCards) {
		t.Errorf("got %d distinct due cards, want %d", len(seenDue), len(dueCards))
	}
	if len(seenUnseen) != len(unseenCards) {
		t.Errorf("got %d distinct unseen cards, want %d", len(seenUnseen), len(unseenCards))
	}
	if len(batch.Cards) != len(dueCards)+len(unseenCards) {
		t.Errorf("got %d total cards, want %d (no loss or duplication)", len(batch.Cards), len(dueCards)+len(unseenCards))
	}
}

// TestBuildBatch_NewMixMixed_RevCapBlocksReviewSide: rev.perDay=0 under mixed mode blocks the
// review sub-query entirely while the new sub-query keeps flowing -- the mixed-mode counterpart
// of TestBuildBatch_RevZeroPerDay, exercising ListReviewCardsForStudy's own rev_remaining check
// rather than ListDueCardsForStudy's.
func TestBuildBatch_NewMixMixed_RevCapBlocksReviewSide(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 2) // + f.CardID = 3 total
	unseenCards := extra
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCard(t, tx, f.UserID, f.CardID, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, 0,
		RevOrderDue, NewMixMixed, Cursor{AtStart: true}, 30, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if len(batch.Cards) != len(unseenCards) {
		t.Fatalf("got %d cards, want %d (the unseen cards only)", len(batch.Cards), len(unseenCards))
	}
	for _, c := range batch.Cards {
		if !c.Unseen {
			t.Errorf("card %s: unseen=false, want true (due cards must be fully blocked)", c.CardID.String())
		}
	}
}

// TestBuildBatch_NewMixMixed_NewCapBlocksNewSide: new.perDay=0 under mixed mode blocks the new
// sub-query entirely while the review sub-query keeps flowing -- the mixed-mode counterpart of
// TestBuildBatch_ZeroPerDay, exercising ListNewCardsForStudy's own new_remaining check.
func TestBuildBatch_NewMixMixed_NewCapBlocksNewSide(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 2) // + f.CardID = 3 total
	dueCards := append([]pgtype.UUID{f.CardID}, extra[0])
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCards(t, tx, f.UserID, dueCards, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, 0, DefaultRevPerDay,
		RevOrderDue, NewMixMixed, Cursor{AtStart: true}, 30, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if len(batch.Cards) != len(dueCards) {
		t.Fatalf("got %d cards, want %d (the due cards only)", len(batch.Cards), len(dueCards))
	}
	for _, c := range batch.Cards {
		if c.Unseen {
			t.Errorf("card %s: unseen=true, want false (new cards must be fully blocked)", c.CardID.String())
		}
	}
}

// TestBuildBatch_NewMixMixed_ExhaustedRequiresNoTruncation: 15 due cards + 15 unseen cards against
// a limit of 20 -- each sub-fetch individually comes back undersized (15 < 20), but their combined
// count (30) exceeds limit, so interleave truncates the display to 20 and 10 fetched-but-unpicked
// rows remain for the next refill. Exhausted must be false (a bug caught in review: marking it
// true here silently drops those 10 cards for the rest of the session, since the client never
// refills once Exhausted is true).
func TestBuildBatch_NewMixMixed_ExhaustedRequiresNoTruncation(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	extra := seedCards(t, tx, f, 29) // + f.CardID = 30 total unseen
	dueCards := append([]pgtype.UUID{f.CardID}, extra[:14]...)
	unseenCards := extra[14:]
	if len(dueCards) != 15 || len(unseenCards) != 15 {
		t.Fatalf("test setup: got %d due, %d unseen, want 15 and 15", len(dueCards), len(unseenCards))
	}
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	insertDueCards(t, tx, f.UserID, dueCards, window)

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, DefaultNewPerDay, DefaultRevPerDay,
		RevOrderDue, NewMixMixed, Cursor{AtStart: true}, 20, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if batch.Exhausted {
		t.Error("Exhausted = true, want false (combined 30 rows truncated to the 20-card limit, 10 remain unserved)")
	}
	if len(batch.Cards) != 20 {
		t.Fatalf("got %d cards, want 20 (the limit)", len(batch.Cards))
	}
	if batch.Cursor == "" {
		t.Error("Cursor is empty, want a non-empty cursor so the remaining 10 cards can be fetched on refill")
	}
}

// TestBuildBatch_DueLookAhead: a review-state card due 20 minutes after now is excluded at the
// default zero look-ahead (#154, due<=now) and served once lookAheadMinutes widens past it.
// newPerDay is passed 0 so the fixture's own card can't also qualify as unseen once given a
// user_card_state row -- the only candidate is the future-due one.
func TestBuildBatch_DueLookAhead(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	p := mustDefaultParams(t)
	f := seedFixture(t, tx)
	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)

	if _, err := tx.Exec(ctx,
		`INSERT INTO user_card_state (user_id, card_id, due, state, reps, stability, difficulty, last_review)
		 VALUES ($1, $2, $3, 2, 1, 2.5, 5.0, $4)`,
		f.UserID, f.CardID, now.Add(20*time.Minute), window.Start.Add(-24*time.Hour),
	); err != nil {
		t.Fatalf("insert future-due card: %v", err)
	}

	batch, err := BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, 0, DefaultRevPerDay,
		RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 0)
	if err != nil {
		t.Fatalf("BuildBatch (zero look-ahead): %v", err)
	}
	if len(batch.Cards) != 0 {
		t.Fatalf("zero look-ahead: got %d cards, want 0 (card is due 20m from now)", len(batch.Cards))
	}

	batch, err = BuildBatch(ctx, tx, p, f.UserID, f.DeckID, "D", window, 0, DefaultRevPerDay,
		RevOrderDue, NewMixAfterReviews, Cursor{AtStart: true}, 30, now, 30)
	if err != nil {
		t.Fatalf("BuildBatch (30m look-ahead): %v", err)
	}
	if len(batch.Cards) != 1 || batch.Cards[0].CardID != f.CardID {
		t.Fatalf("30m look-ahead: got %d cards, want 1 (the future-due card)", len(batch.Cards))
	}
}

// TestCountQueueForUser_PerDeckLookAhead: two decks owned by the same user, each with one card
// due 20 minutes from now -- deckA configured with 0 look-ahead, deckB with 30 (#154). The
// unnest/ordinality array join in CountQueueForUser must apply each deck's own value, not the
// same scalar to every row: deckA's card stays excluded, deckB's is counted as due.
func TestCountQueueForUser_PerDeckLookAhead(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	fA := seedFixture(t, tx)

	var deckB, cardB pgtype.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO decks (owner_id, name, preset) VALUES ($1, 'D2', '{"due":{"lookAheadMinutes":30}}'::jsonb) RETURNING id`,
		fA.UserID,
	).Scan(&deckB); err != nil {
		t.Fatalf("insert second deck: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content, can_edit_settings, can_manage_access, can_delete)
		 VALUES ($1, $2, true, true, true, true, true, true)`,
		deckB, fA.UserID,
	); err != nil {
		t.Fatalf("insert deck_access for second deck: %v", err)
	}
	var noteB pgtype.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, checksum) VALUES ($1, $2, $3, $4, '["Q","A"]'::jsonb, 1) RETURNING id`,
		fmt.Sprintf("guid-%d", nextSeq()), fA.UserID, fA.NoteTypeID, deckB,
	).Scan(&noteB); err != nil {
		t.Fatalf("insert note for second deck: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO cards (note_id, template_id, ordinal, deck_id) VALUES ($1, $2, 0, $3) RETURNING id`,
		noteB, fA.TemplateID, deckB,
	).Scan(&cardB); err != nil {
		t.Fatalf("insert card for second deck: %v", err)
	}

	window := testStudyDay(0)
	now := window.Start.Add(time.Hour)
	for _, c := range []pgtype.UUID{fA.CardID, cardB} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_card_state (user_id, card_id, due, state, reps, stability, difficulty, last_review)
			 VALUES ($1, $2, $3, 2, 1, 2.5, 5.0, $4)`,
			fA.UserID, c, now.Add(20*time.Minute), window.Start.Add(-24*time.Hour),
		); err != nil {
			t.Fatalf("insert future-due card: %v", err)
		}
	}

	rows, err := db.New(tx).CountQueueForUser(ctx, db.CountQueueForUserParams{
		UserID:           fA.UserID,
		StudyDayStart:    pgtype.Timestamptz{Time: window.Start, Valid: true},
		Now:              pgtype.Timestamptz{Time: now, Valid: true},
		DeckIds:          []pgtype.UUID{fA.DeckID, deckB},
		LookAheadMinutes: []int32{0, 30},
	})
	if err != nil {
		t.Fatalf("CountQueueForUser: %v", err)
	}
	got := make(map[pgtype.UUID]int64, len(rows))
	for _, r := range rows {
		got[r.DeckID] = r.DueCount
	}
	if got[fA.DeckID] != 0 {
		t.Errorf("deckA (0 look-ahead) due_count = %d, want 0", got[fA.DeckID])
	}
	if got[deckB] != 1 {
		t.Errorf("deckB (30 look-ahead) due_count = %d, want 1", got[deckB])
	}
}
