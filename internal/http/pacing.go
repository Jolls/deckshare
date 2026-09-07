package http

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/review"
)

// The student's half of release pacing (#243, part of #238): what a paced deck tells the person it
// acts on. The teacher's half -- the calendar form and the deck page's term view -- lives in
// decks.go with the rest of that page. This is its own file because the reviewer's empty state
// needs it too, and a shared view helper should not have to be reached for inside the deck page.

// unlockDateLayout is how the next-unlock line spells a meeting date. Longer than the deck page's
// calendar table (decks.go, "Mon 2 Jan 2006") on purpose -- that one is a column of dates read
// together, this one is a single date in a sentence.
//
// The year is here where the issue's copy has only "Thursday, 9 October": a course can run a
// lesson past a New Year, and "Thursday, 9 January" would then name a day ten months gone.
const unlockDateLayout = "Monday, 2 January 2006"

// nextUnlockView is the student-facing "Lesson 4 unlocks Thursday, 9 October 2026" line, shown on
// the deck page and in the reviewer's exhausted state. Nil means there is nothing to say -- either
// the deck is unpaced, or nothing is waiting behind the gate -- and both templates key the line on
// that with {{with}}, so an unpaced deck's empty state stays exactly what it is today. A pointer
// rather than a bool-plus-value because a struct value is always truthy to html/template.
//
// The line says only when the next lesson opens, deliberately dropping the issue's trailing
// "nothing new until then": the gate is not the only thing that withholds a card. A student who
// has hit the deck's new.perDay cap on lessons already open gets more of them tomorrow, well
// before the next meeting, so that clause would be a claim this query cannot support.
type nextUnlockView struct {
	Lesson int32
	Date   string
}

// nextUnlock resolves the lowest lesson still locked for this user in this deck, and the date it
// opens on. The two halves are deliberately separate: which lesson is pending is a fact about this
// user's own cards (NextLockedLesson), while when it opens is pure calendar arithmetic
// (review.MeetingDate) -- the same walk the deck page's calendar view and CurrentClassDay use, so
// the date shown here cannot drift from the day the gate actually opens.
//
// Takes the student's own study-day date and resolves the class day itself, rather than taking a
// class day a caller worked out: the calendar and the day it is read on have to describe the same
// deck on the same date, and that is one fewer thing for each call site to get right.
//
// An unpaced deck doesn't reach the query at all, which is what keeps the near-totality of decks
// paying nothing for this line.
func nextUnlock(ctx context.Context, q *db.Queries, userID, deckID pgtype.UUID,
	calendar review.Calendar, localDate time.Time) (*nextUnlockView, error) {
	if !calendar.Configured() {
		return nil, nil
	}
	lesson, err := q.NextLockedLesson(ctx, db.NextLockedLessonParams{
		UserID: userID, DeckID: deckID,
		CurrentClassDay: review.CurrentClassDay(calendar, localDate),
	})
	if errors.Is(err, pgx.ErrNoRows) { // nothing locked: the ordinary empty state is the honest one
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	date, ok := review.MeetingDate(calendar, lesson)
	if !ok {
		return nil, nil
	}
	return &nextUnlockView{Lesson: lesson, Date: date.Format(unlockDateLayout)}, nil
}
