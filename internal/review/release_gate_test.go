package review

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
)

// The release-day gate (#242, part of #238): a note assigned to class day N is not introduced
// until the Nth class meeting has arrived on the student's own clock. Every test here drives the
// gate through BuildBatch's classDay parameter directly -- resolving a calendar into that number
// is CurrentClassDay's job and is tested purely in calendar_test.go, so these tests are about what
// the QUERIES do with the resolved number.

// servedIDs runs one full-size fetch and returns the card ids it served.
func servedIDs(t *testing.T, tx pgx.Tx, f fixture, window StudyDay, newPerDay, extraRounds, classDay int32) map[pgtype.UUID]bool {
	t.Helper()
	batch, err := BuildBatch(context.Background(), tx, mustDefaultParams(t), f.UserID, f.DeckID, "D",
		window, newPerDay, DefaultRevPerDay, RevOrderDue, PriorityDue, Cursor{AtStart: true}, 100,
		window.Start.Add(time.Hour), 0, extraRounds, classDay)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	return servedSet(batch)
}

func servedSet(batch Batch) map[pgtype.UUID]bool {
	served := make(map[pgtype.UUID]bool, len(batch.Cards))
	for _, c := range batch.Cards {
		served[c.CardID] = true
	}
	return served
}

// TestBuildBatch_ReleaseGateWithholdsLaterLessons: on class day 3, lessons 1 through 3 are served
// and lesson 4 is not; the same deck on class day 4 serves lesson 4 as well. Cumulative, so
// nothing that was unlocked earlier drops out.
func TestBuildBatch_ReleaseGateWithholdsLaterLessons(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 3)
	unassigned, lesson1, lesson4 := f.CardID, cards[0], cards[1]
	lesson3 := cards[2]
	setReleaseDay(t, tx, lesson1, 1)
	setReleaseDay(t, tx, lesson3, 3)
	setReleaseDay(t, tx, lesson4, 4)

	window := testStudyDay(0)
	served := servedIDs(t, tx, f, window, DefaultNewPerDay, 0, 3)
	for _, c := range []struct {
		id   pgtype.UUID
		name string
	}{{unassigned, "the unassigned note"}, {lesson1, "lesson 1"}, {lesson3, "lesson 3"}} {
		if !served[c.id] {
			t.Errorf("class day 3: %s was withheld, want it served", c.name)
		}
	}
	if served[lesson4] {
		t.Error("class day 3: lesson 4 was served, want it withheld until the 4th meeting")
	}

	if served := servedIDs(t, tx, f, window, DefaultNewPerDay, 0, 4); !served[lesson4] {
		t.Error("class day 4: lesson 4 was still withheld, want it served")
	}
}

// TestBuildBatch_ReleaseGateOffServesEverything: a deck with no calendar (ReleaseGateOff) ignores
// release_day entirely -- the near-totality of decks, and the reason the gate is a sentinel rather
// than a flag.
func TestBuildBatch_ReleaseGateOffServesEverything(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 2)
	setReleaseDay(t, tx, cards[0], 1)
	setReleaseDay(t, tx, cards[1], 999)

	served := servedIDs(t, tx, f, testStudyDay(0), DefaultNewPerDay, 0, ReleaseGateOff)
	if len(served) != 3 {
		t.Fatalf("served %d cards, want all 3 -- an unpaced deck gates nothing", len(served))
	}
}

// TestBuildBatch_ReleaseGateUnassignedAlwaysServed: NULL and 0 both mean "available immediately",
// even before the term starts (class day 0). The permissive failure mode: a note the teacher
// forgot to assign shows up rather than silently never unlocking.
func TestBuildBatch_ReleaseGateUnassignedAlwaysServed(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 2)
	zero, lesson1 := cards[0], cards[1]
	setReleaseDay(t, tx, zero, 0)
	setReleaseDay(t, tx, lesson1, 1)

	served := servedIDs(t, tx, f, testStudyDay(0), DefaultNewPerDay, 0, 0)
	if !served[f.CardID] {
		t.Error("class day 0: the NULL-release_day note was withheld, want it served")
	}
	if !served[zero] {
		t.Error("class day 0: the release_day=0 note was withheld, want it served")
	}
	if served[lesson1] {
		t.Error("class day 0: lesson 1 was served before the first meeting, want it withheld")
	}
}

// TestBuildBatch_ReleaseGateIsCumulative: a student who has studied nothing arrives at class day
// 10 to the whole backlog -- lessons 1 through 10 all eligible at once, not just the current one.
func TestBuildBatch_ReleaseGateIsCumulative(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 12)
	for i, c := range cards {
		setReleaseDay(t, tx, c, int32(i+1)) // lessons 1..12
	}

	served := servedIDs(t, tx, f, testStudyDay(0), 100, 0, 10)
	for i, c := range cards {
		lesson := int32(i + 1)
		if got, want := served[c], lesson <= 10; got != want {
			t.Errorf("class day 10: lesson %d served = %v, want %v", lesson, got, want)
		}
	}
	if !served[f.CardID] {
		t.Error("class day 10: the unassigned note was withheld, want it served")
	}
}

// TestBuildBatch_ReleaseGateNeverRegatesIntroducedCards: the gate is an INTRODUCTION gate. A card
// that already has a user_card_state row has been introduced and keeps coming back on schedule,
// whatever its note's release_day says -- otherwise a re-assigned lesson number would strand a
// student's in-progress card, and the whole point of "cumulative" is that nothing re-locks.
func TestBuildBatch_ReleaseGateNeverRegatesIntroducedCards(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	window := testStudyDay(0)
	setReleaseDay(t, tx, f.CardID, 9) // a lesson far past the current class day
	insertDueCard(t, tx, f.UserID, f.CardID, window)

	served := servedIDs(t, tx, f, window, DefaultNewPerDay, 0, 3)
	if !served[f.CardID] {
		t.Error("an already-introduced card was re-gated by its note's release_day, want it served")
	}
}

// TestBuildBatch_ExtraRoundsCannotCrossTheGate: extraRounds (#172) re-grants the deck's daily
// ALLOWANCE, so it can burn down an unlocked backlog -- and must never reach a locked lesson,
// because the gate is an eligibility predicate rather than an allowance. This falls out of the
// design; the assertion is here so it stays true.
func TestBuildBatch_ExtraRoundsCannotCrossTheGate(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 9)
	unlocked, locked := cards[:4], cards[4:]
	for _, c := range unlocked {
		setReleaseDay(t, tx, c, 1)
	}
	for _, c := range locked {
		setReleaseDay(t, tx, c, 9)
	}
	setReleaseDay(t, tx, f.CardID, 1)

	// newPerDay of 2 with 20 extra rounds is an allowance of 42 against 10 cards -- more than
	// enough to serve every locked card too, if the gate were an allowance.
	served := servedIDs(t, tx, f, testStudyDay(0), 2, 20, 1)
	for _, c := range locked {
		if served[c] {
			t.Fatal("extraRounds served a card from a lesson that has not opened yet")
		}
	}
	if len(served) != len(unlocked)+1 {
		t.Errorf("served %d cards, want all %d unlocked ones (extraRounds must still burn down the backlog)",
			len(served), len(unlocked)+1)
	}
}

// TestBuildBatch_GateDoesNotConsumeTheNewCardAllowance: the day's new-card cap is ranked over
// ELIGIBLE cards only. A locked lesson sitting early in import_due_position order must not occupy
// a rank in that ranking -- if it did, a deck whose next lesson is locked would advertise and
// serve nothing while unlocked cards sat behind it.
func TestBuildBatch_GateDoesNotConsumeTheNewCardAllowance(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 4)
	setReleaseDay(t, tx, f.CardID, 5)
	setImportDuePosition(t, tx, f.CardID, 1) // locked, and first in gather order
	for i, c := range cards {
		setReleaseDay(t, tx, c, 1)
		setImportDuePosition(t, tx, c, int32(i+2))
	}

	served := servedIDs(t, tx, f, testStudyDay(0), 2, 0, 1)
	if len(served) != 2 {
		t.Fatalf("served %d cards, want 2 -- the whole new-card allowance, none of it eaten by the locked note", len(served))
	}
	if served[f.CardID] {
		t.Error("the locked note was served")
	}
}

// TestBuildBatch_ReleaseGateInMixedPriority: priority "mixed" (#116) fetches new cards through
// ListNewCardsForStudy instead of ListDueCardsForStudy -- a second query, so a second copy of the
// gate. Both must agree, or the deck's pacing would depend on its review-priority setting.
func TestBuildBatch_ReleaseGateInMixedPriority(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 2)
	open, locked := cards[0], cards[1]
	setReleaseDay(t, tx, f.CardID, 1)
	setReleaseDay(t, tx, open, 2)
	setReleaseDay(t, tx, locked, 3)

	window := testStudyDay(0)
	batch, err := BuildBatch(context.Background(), tx, mustDefaultParams(t), f.UserID, f.DeckID, "D",
		window, DefaultNewPerDay, DefaultRevPerDay, RevOrderDue, PriorityMixed, Cursor{AtStart: true},
		100, window.Start.Add(time.Hour), 0, 0, 2)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	served := servedSet(batch)
	if !served[f.CardID] || !served[open] {
		t.Error("mixed priority withheld an unlocked lesson")
	}
	if served[locked] {
		t.Error("mixed priority served lesson 3 on class day 2")
	}
}

// TestCountQueueForDeck_MatchesGatedFetch: the deck page's "New" count is what a full drain
// actually serves. Gating the fetch without gating the count is the #101/#106 divergence -- a
// paced deck advertising "300 new" while serving 20 -- and CLAUDE.md §10.2's preview/grade rule is
// the same principle: the number shown and the number served come from one definition of eligible.
func TestCountQueueForDeck_MatchesGatedFetch(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 9)
	for i, c := range cards {
		setReleaseDay(t, tx, c, int32(i+1)) // lessons 1..9
	}

	window := testStudyDay(0)
	const classDay = 4
	row, err := db.New(tx).CountQueueForDeck(ctx, db.CountQueueForDeckParams{
		UserID:          f.UserID,
		DeckID:          f.DeckID,
		StudyDayStart:   pgtype.Timestamptz{Time: window.Start, Valid: true},
		Now:             pgtype.Timestamptz{Time: window.Start.Add(time.Hour), Valid: true},
		CurrentClassDay: classDay,
	})
	if err != nil {
		t.Fatalf("CountQueueForDeck: %v", err)
	}
	served := servedIDs(t, tx, f, window, 100, 0, classDay)
	if int64(len(served)) != row.NewCount {
		t.Errorf("CountQueueForDeck reported %d new, a full drain served %d", row.NewCount, len(served))
	}
	if row.NewCount != 5 { // lessons 1..4, plus the unassigned fixture note
		t.Errorf("new_count = %d, want 5", row.NewCount)
	}
}

// nextLockedLesson runs the #243 query for f's user and deck at the given class day, mapping the
// query's "nothing locked" answer -- no rows -- to 0 so the tests below can read as a table.
func nextLockedLesson(t *testing.T, tx pgx.Tx, f fixture, classDay int32) int32 {
	t.Helper()
	lesson, err := db.New(tx).NextLockedLesson(context.Background(), db.NextLockedLessonParams{
		UserID: f.UserID, DeckID: f.DeckID, CurrentClassDay: classDay,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatalf("NextLockedLesson: %v", err)
	}
	return lesson
}

// TestNextLockedLesson: the student-facing unlock line (#243) names the LOWEST lesson still shut,
// so a student who finishes today's material is told about the next thing rather than the last.
func TestNextLockedLesson(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	cards := seedCards(t, tx, f, 9)
	for i, c := range cards {
		setReleaseDay(t, tx, c, int32(i+1)) // lessons 1..9; f.CardID stays unassigned
	}

	if got := nextLockedLesson(t, tx, f, 4); got != 5 {
		t.Errorf("class day 4: next locked lesson = %d, want 5", got)
	}
	// The count of what is servable and the lesson that is not are two halves of one partition:
	// nothing may fall into neither. Every lesson reached -> nothing pending, and the reviewer
	// keeps the ordinary empty state.
	if got := nextLockedLesson(t, tx, f, 9); got != 0 {
		t.Errorf("class day 9: next locked lesson = %d, want 0 (nothing left to unlock)", got)
	}
	if got := nextLockedLesson(t, tx, f, 0); got != 1 {
		t.Errorf("before the first meeting: next locked lesson = %d, want 1", got)
	}
	if got := nextLockedLesson(t, tx, f, ReleaseGateOff); got != 0 {
		t.Errorf("unpaced gate day: next locked lesson = %d, want 0", got)
	}
}

// TestNextLockedLesson_IgnoresIntroducedCards: the gate is an INTRODUCTION gate, so a card the
// student has already seen is not waiting on anything, whatever its lesson number says. Otherwise
// a re-assigned lesson would advertise an unlock date for material already in the student's hands.
func TestNextLockedLesson_IgnoresIntroducedCards(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	later := seedCards(t, tx, f, 1)[0]
	setReleaseDay(t, tx, f.CardID, 6)
	setReleaseDay(t, tx, later, 8)
	insertDueCard(t, tx, f.UserID, f.CardID, testStudyDay(0))

	if got := nextLockedLesson(t, tx, f, 2); got != 8 {
		t.Errorf("next locked lesson = %d, want 8 -- lesson 6 is already introduced", got)
	}
}

// TestNextLockedLesson_UnassignedNotesNeverLock: release_day 0 means "available immediately", so an
// unassigned note can never be the thing a student is waiting for.
func TestNextLockedLesson_UnassignedNotesNeverLock(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	if got := nextLockedLesson(t, tx, f, 0); got != 0 {
		t.Errorf("next locked lesson = %d, want 0 for a deck of unassigned notes", got)
	}
}

// TestNextLockedLesson_IsDeckScoped: a locked lesson in another of the user's decks is not this
// deck's unlock date.
func TestNextLockedLesson_IsDeckScoped(t *testing.T) {
	tx := beginTx(t)
	f := seedFixture(t, tx)
	second := seedSecondDeck(t, tx, f)
	setReleaseDay(t, tx, second.CardID, 3)

	if got := nextLockedLesson(t, tx, f, 1); got != 0 {
		t.Errorf("first deck: next locked lesson = %d, want 0 -- lesson 3 belongs to the other deck", got)
	}
	if got := nextLockedLesson(t, tx, second, 1); got != 3 {
		t.Errorf("second deck: next locked lesson = %d, want 3", got)
	}
}

// TestCountQueueForUser_GatesPerDeck: the /decks list resolves each deck's class day separately,
// so one paced deck's gate never leaks into another's counts.
func TestCountQueueForUser_GatesPerDeck(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	paced := seedFixture(t, tx)
	unpaced := seedSecondDeck(t, tx, paced)
	setReleaseDay(t, tx, paced.CardID, 5)
	setReleaseDay(t, tx, unpaced.CardID, 5)

	window := testStudyDay(0)
	rows, err := db.New(tx).CountQueueForUser(ctx, db.CountQueueForUserParams{
		UserID:           paced.UserID,
		StudyDayStart:    pgtype.Timestamptz{Time: window.Start, Valid: true},
		Now:              pgtype.Timestamptz{Time: window.Start.Add(time.Hour), Valid: true},
		DeckIds:          []pgtype.UUID{paced.DeckID, unpaced.DeckID},
		LookAheadMinutes: []int32{0, 0},
		CurrentClassDays: []int32{1, ReleaseGateOff},
	})
	if err != nil {
		t.Fatalf("CountQueueForUser: %v", err)
	}
	got := make(map[pgtype.UUID]int64, len(rows))
	for _, r := range rows {
		got[r.DeckID] = r.NewCount
	}
	if got[paced.DeckID] != 0 {
		t.Errorf("paced deck new_count = %d, want 0 (lesson 5 has not opened)", got[paced.DeckID])
	}
	if got[unpaced.DeckID] != 1 {
		t.Errorf("unpaced deck new_count = %d, want 1 (no calendar gates nothing)", got[unpaced.DeckID])
	}
}
