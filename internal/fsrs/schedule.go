package fsrs

import (
	"fmt"
	"math"
	"time"

	gofsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

// Rating is a grade, wire-identical to review_log.rating (CHECK 1..4).
type Rating uint8

const (
	Again Rating = 1
	Hard  Rating = 2
	Good  Rating = 3
	Easy  Rating = 4
)

// Ratings is the canonical iteration order for the four branches.
var Ratings = [4]Rating{Again, Hard, Good, Easy}

func (r Rating) Valid() bool {
	return r >= Again && r <= Easy
}

func (r Rating) String() string {
	switch r {
	case Again:
		return "Again"
	case Hard:
		return "Hard"
	case Good:
		return "Good"
	case Easy:
		return "Easy"
	default:
		return "unknown"
	}
}

// State is wire-identical to user_card_state.state (CHECK 0..3).
type State uint8

const (
	New        State = 0
	Learning   State = 1
	Review     State = 2
	Relearning State = 3
)

func (s State) Valid() bool {
	return s >= New && s <= Relearning
}

func (s State) String() string {
	switch s {
	case New:
		return "New"
	case Learning:
		return "Learning"
	case Review:
		return "Review"
	case Relearning:
		return "Relearning"
	default:
		return "unknown"
	}
}

// CardState is the prior scheduling state of one card for one user. Field-for-field the subset
// of user_card_state that FSRS reads; the caller maps the row, this package never sees a
// database.
type CardState struct {
	Due           time.Time // zero for a never-scheduled card
	Stability     float64
	Difficulty    float64
	State         State
	Reps          int32
	Lapses        int32
	ScheduledDays int32
	LearningSteps int16 // go-fsrs v4 Card.RemainingSteps
	LastReview    time.Time
}

// Outcome is what one rating produces. Every field is the value to store in user_card_state;
// nothing here is advisory.
type Outcome struct {
	Due           time.Time // always UTC
	Stability     float64
	Difficulty    float64
	State         State
	Reps          int32
	Lapses        int32
	ScheduledDays int32
	LearningSteps int16
}

// Preview holds the four branches invariant §2.6 requires the server to precompute at
// batch-fetch time. A struct, not a map: fixed arity, no iteration-order surprises, and every
// branch is statically reachable.
type Preview struct {
	Again Outcome
	Hard  Outcome
	Good  Outcome
	Easy  Outcome
}

// For returns the branch matching r, or ErrInvalidRating.
func (p Preview) For(r Rating) (Outcome, error) {
	switch r {
	case Again:
		return p.Again, nil
	case Hard:
		return p.Hard, nil
	case Good:
		return p.Good, nil
	case Easy:
		return p.Easy, nil
	default:
		return Outcome{}, fmt.Errorf("%w: %d", ErrInvalidRating, r)
	}
}

// ElapsedDays is the number of UTC calendar-day boundaries crossed between prior.LastReview and
// now (0 if never reviewed, 0 if now precedes LastReview) -- mirrors go-fsrs's own
// dateDiffInDays (steps.go) exactly, including its truncate-to-midnight-UTC behaviour, so
// review_log.elapsed_days_before matches what the scheduler used. This is a calendar-day count,
// not a raw-duration one: 23 hours that cross a UTC midnight count as 1, while 23 hours that
// don't count as 0.
func ElapsedDays(prior CardState, now time.Time) int32 {
	if prior.LastReview.IsZero() {
		return 0
	}
	lr := prior.LastReview.UTC()
	day1 := time.Date(lr.Year(), lr.Month(), lr.Day(), 0, 0, 0, 0, time.UTC)
	n := now.UTC()
	day2 := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	hours := day2.Sub(day1).Hours()
	if hours < 0 {
		return 0
	}
	return int32(math.Floor(hours / 24))
}

// Schedule is the grade-time recompute: the authoritative path (CLAUDE.md §2.7). It is what the
// live grading path and replayReviews call.
func Schedule(p Params, prior CardState, rating Rating, now time.Time) (Outcome, error) {
	if !p.valid {
		return Outcome{}, ErrUnvalidatedParams
	}
	if !rating.Valid() {
		return Outcome{}, fmt.Errorf("%w: %d", ErrInvalidRating, rating)
	}
	card, err := toLibCard(prior)
	if err != nil {
		return Outcome{}, err
	}
	info, err := p.engine().Next(card, now, gofsrs.Rating(rating))
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrSchedule, err)
	}
	return fromLibCard(info.Card), nil
}

// PreviewAll is the batch-fetch precompute: all four branches for one card under one prior
// state (CLAUDE.md §2.6).
func PreviewAll(p Params, prior CardState, now time.Time) (Preview, error) {
	if !p.valid {
		return Preview{}, ErrUnvalidatedParams
	}
	card, err := toLibCard(prior)
	if err != nil {
		return Preview{}, err
	}
	log, err := p.engine().Repeat(card, now)
	if err != nil {
		return Preview{}, fmt.Errorf("%w: %v", ErrSchedule, err)
	}
	return Preview{
		Again: fromLibCard(log[gofsrs.Again].Card),
		Hard:  fromLibCard(log[gofsrs.Hard].Card),
		Good:  fromLibCard(log[gofsrs.Good].Card),
		Easy:  fromLibCard(log[gofsrs.Easy].Card),
	}, nil
}

func toLibCard(prior CardState) (gofsrs.Card, error) {
	if !prior.State.Valid() {
		return gofsrs.Card{}, fmt.Errorf("%w: State = %d", ErrInvalidCardState, prior.State)
	}
	if prior.Reps < 0 {
		return gofsrs.Card{}, fmt.Errorf("%w: Reps = %d", ErrInvalidCardState, prior.Reps)
	}
	if prior.Lapses < 0 {
		return gofsrs.Card{}, fmt.Errorf("%w: Lapses = %d", ErrInvalidCardState, prior.Lapses)
	}
	if prior.ScheduledDays < 0 {
		return gofsrs.Card{}, fmt.Errorf("%w: ScheduledDays = %d", ErrInvalidCardState, prior.ScheduledDays)
	}
	if prior.LearningSteps < 0 || int(prior.LearningSteps) > maxLearningSteps {
		return gofsrs.Card{}, fmt.Errorf("%w: LearningSteps = %d (must be 0..%d)", ErrInvalidCardState, prior.LearningSteps, maxLearningSteps)
	}
	if math.IsNaN(prior.Stability) || math.IsInf(prior.Stability, 0) {
		return gofsrs.Card{}, fmt.Errorf("%w: Stability = %v", ErrInvalidCardState, prior.Stability)
	}
	if math.IsNaN(prior.Difficulty) || math.IsInf(prior.Difficulty, 0) {
		return gofsrs.Card{}, fmt.Errorf("%w: Difficulty = %v", ErrInvalidCardState, prior.Difficulty)
	}
	if prior.State != New && (prior.Stability < 0.001 || prior.Difficulty < 1.0) {
		return gofsrs.Card{}, fmt.Errorf("%w: Stability = %v, Difficulty = %v below minimum for non-New state", ErrInvalidCardState, prior.Stability, prior.Difficulty)
	}

	return gofsrs.Card{
		Due:            prior.Due,
		Stability:      prior.Stability,
		Difficulty:     prior.Difficulty,
		ScheduledDays:  uint64(prior.ScheduledDays),
		Reps:           uint64(prior.Reps),
		Lapses:         uint64(prior.Lapses),
		State:          gofsrs.State(prior.State),
		LastReview:     prior.LastReview,
		RemainingSteps: int(prior.LearningSteps),
	}, nil
}

func fromLibCard(card gofsrs.Card) Outcome {
	return Outcome{
		Due:           card.Due.UTC(),
		Stability:     card.Stability,
		Difficulty:    card.Difficulty,
		State:         State(card.State),
		Reps:          clampToInt32(card.Reps),
		Lapses:        clampToInt32(card.Lapses),
		ScheduledDays: clampToInt32(card.ScheduledDays),
		LearningSteps: int16(card.RemainingSteps), // bounded by maxLearningSteps well within int16
	}
}

// clampToInt32 saturates at math.MaxInt32 instead of wrapping, so a uint64 counter that somehow
// exceeded that range (Reps/Lapses/ScheduledDays -- ~2.1 billion reviews on one card) never turns
// into a negative int32. A silent wrap would fail closed anyway on the next call (toLibCard
// rejects negative values), but saturating is cheap and avoids surprising fromLibCard callers
// with a sign flip in the meantime.
func clampToInt32(v uint64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}
