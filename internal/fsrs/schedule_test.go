package fsrs

import (
	"errors"
	"math"
	"testing"
	"time"

	gofsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

func fixedNow() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

func mustDefaultParams(t *testing.T) Params {
	t.Helper()
	p, err := NewDefaultParams(0.9)
	if err != nil {
		t.Fatalf("NewDefaultParams: %v", err)
	}
	return p
}

func TestFuzzIsOff(t *testing.T) {
	p := mustDefaultParams(t)
	if p.engine().EnableFuzz {
		t.Fatal("engine EnableFuzz should be false")
	}

	// A card whose Good interval will exceed the 2.5-day fuzz threshold: previewing it many
	// times with now jittered by sub-millisecond amounts must always produce the same
	// ScheduledDays, since fuzz (which would vary with the seed) is off.
	prior := CardState{State: New}
	first, err := PreviewAll(p, prior, fixedNow())
	if err != nil {
		t.Fatalf("PreviewAll: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := PreviewAll(p, prior, fixedNow().Add(time.Duration(i)*time.Millisecond))
		if err != nil {
			t.Fatalf("PreviewAll: %v", err)
		}
		if got.Good.ScheduledDays != first.Good.ScheduledDays {
			t.Errorf("iteration %d: ScheduledDays = %d, want %d (fuzz should be off)", i, got.Good.ScheduledDays, first.Good.ScheduledDays)
		}
	}
}

// The two tests below characterise go-fsrs itself, not our wrapper. They pin what architecture.md
// §3 flagged as unverified: exactly what the library's fuzz seed depends on. scheduler.go's
// initSeed builds it as fmt.Sprintf("%d_%d_%f", now.UnixMilli(), reps, difficulty*stability), so
// it is deterministic -- §3's worry about a "non-reproducible source" does not hold -- but now is
// one of its inputs, and preview (fetch instant) and grade (reviewedAt) never pass the same now.
// That, and only that, is why params.go's engine hard-codes EnableFuzz: false. If a go-fsrs
// upgrade changes the seeding, one of these two fails, and that is the signal to revisit the
// fuzz-off decision -- not a reason to skip or delete them.

// fuzzOnEngine is what params.go's engine() would build with fuzz turned on: the one
// configuration our wrapper deliberately refuses to produce, so it is constructed here by hand.
func fuzzOnEngine() *gofsrs.FSRS {
	return gofsrs.NewFSRS(gofsrs.Parameters{
		RequestRetention: 0.9,
		MaximumInterval:  36500,
		W:                gofsrs.DefaultWeights(),
		EnableShortTerm:  true,
		EnableFuzz:       true,
		LearningSteps:    gofsrs.DefaultLearningSteps(),
		RelearningSteps:  gofsrs.DefaultRelearningSteps(),
	})
}

// fuzzableCard is a Review-state card whose intervals land far past the 2.5-day threshold below
// which fuzz is a no-op (arithmetic.go's nextInterval), so fuzz actually engages.
func fuzzableCard() gofsrs.Card {
	return gofsrs.Card{
		State:      gofsrs.Review,
		Stability:  50,
		Difficulty: 5,
		LastReview: fixedNow().Add(-30 * 24 * time.Hour),
	}
}

func TestLibraryFuzzSeedIsPureInItsInputs(t *testing.T) {
	engine := fuzzOnEngine()
	card := fuzzableCard()

	first, err := engine.Repeat(card, fixedNow())
	if err != nil {
		t.Fatalf("Repeat: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := engine.Repeat(card, fixedNow())
		if err != nil {
			t.Fatalf("Repeat: %v", err)
		}
		for _, r := range []gofsrs.Rating{gofsrs.Again, gofsrs.Hard, gofsrs.Good, gofsrs.Easy} {
			if again[r].Card.ScheduledDays != first[r].Card.ScheduledDays {
				t.Fatalf("iteration %d rating %v: ScheduledDays = %d, want %d -- even with fuzz on, the same (card, now) must be reproducible; a process-clock or crypto/rand seed would break replay determinism, not just preview parity",
					i, r, again[r].Card.ScheduledDays, first[r].Card.ScheduledDays)
			}
		}
	}
}

func TestLibraryFuzzVariesWithNow(t *testing.T) {
	engine := fuzzOnEngine()
	card := fuzzableCard()

	distinct := map[uint64]struct{}{}
	for i := 0; i < 200; i++ {
		log, err := engine.Repeat(card, fixedNow().Add(time.Duration(i)*time.Millisecond))
		if err != nil {
			t.Fatalf("Repeat: %v", err)
		}
		distinct[log[gofsrs.Good].Card.ScheduledDays] = struct{}{}
	}
	if len(distinct) < 2 {
		t.Fatalf("Good.ScheduledDays took %d distinct value(s) across 200 one-millisecond shifts of now, want >= 2 -- now is a seed input (scheduler.go's initSeed), which is the whole reason EnableFuzz stays false in params.go's engine", len(distinct))
	}
}

func TestInvalidRating(t *testing.T) {
	p := mustDefaultParams(t)
	prior := CardState{State: New}

	if _, err := Schedule(p, prior, Rating(0), fixedNow()); !errors.Is(err, ErrInvalidRating) {
		t.Errorf("Schedule with Rating(0) error = %v, want ErrInvalidRating", err)
	}
	if _, err := Schedule(p, prior, Rating(5), fixedNow()); !errors.Is(err, ErrInvalidRating) {
		t.Errorf("Schedule with Rating(5) error = %v, want ErrInvalidRating", err)
	}

	preview, err := PreviewAll(p, prior, fixedNow())
	if err != nil {
		t.Fatalf("PreviewAll: %v", err)
	}
	if _, err := preview.For(Rating(0)); !errors.Is(err, ErrInvalidRating) {
		t.Errorf("Preview.For(Rating(0)) error = %v, want ErrInvalidRating", err)
	}
}

func TestInvalidCardState(t *testing.T) {
	p := mustDefaultParams(t)
	now := fixedNow()

	tests := []struct {
		name  string
		prior CardState
	}{
		{"negative Reps", CardState{State: New, Reps: -1}},
		{"negative Lapses", CardState{State: New, Lapses: -1}},
		{"negative ScheduledDays", CardState{State: New, ScheduledDays: -1}},
		{"negative LearningSteps", CardState{State: New, LearningSteps: -1}},
		{"LearningSteps above the configured step count", CardState{State: Learning, LearningSteps: int16(maxLearningSteps + 1)}},
		{"LearningSteps wildly out of range", CardState{State: Learning, LearningSteps: 32767}},
		{"invalid state", CardState{State: State(4)}},
		{"NaN stability", CardState{State: Review, Stability: math.NaN(), Difficulty: 5}},
		{"below-minimum stability for Review", CardState{State: Review, Stability: 0, Difficulty: 5}},
		{"below-minimum difficulty for Review", CardState{State: Review, Stability: 10, Difficulty: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Schedule(p, tt.prior, Good, now); !errors.Is(err, ErrInvalidCardState) {
				t.Errorf("Schedule error = %v, want ErrInvalidCardState", err)
			}
			if _, err := PreviewAll(p, tt.prior, now); !errors.Is(err, ErrInvalidCardState) {
				t.Errorf("PreviewAll error = %v, want ErrInvalidCardState", err)
			}
		})
	}
}

func TestLastReviewAfterNow(t *testing.T) {
	p := mustDefaultParams(t)
	now := fixedNow()
	prior := CardState{
		State:      Review,
		Stability:  10,
		Difficulty: 5,
		LastReview: now.Add(time.Hour),
	}

	if _, err := Schedule(p, prior, Good, now); !errors.Is(err, ErrSchedule) {
		t.Errorf("Schedule error = %v, want ErrSchedule", err)
	}
}

func TestRetrievability(t *testing.T) {
	p := mustDefaultParams(t)
	now := fixedNow()

	t.Run("new card is 0", func(t *testing.T) {
		r, err := Retrievability(p, CardState{State: New}, now)
		if err != nil {
			t.Fatalf("Retrievability: %v", err)
		}
		if r != 0 {
			t.Errorf("r = %v, want 0", r)
		}
	})

	t.Run("no LastReview is 0", func(t *testing.T) {
		r, err := Retrievability(p, CardState{State: Review, Stability: 10, Difficulty: 5}, now)
		if err != nil {
			t.Fatalf("Retrievability: %v", err)
		}
		if r != 0 {
			t.Errorf("r = %v, want 0", r)
		}
	})

	t.Run("at LastReview is approximately 1", func(t *testing.T) {
		prior := CardState{State: Review, Stability: 10, Difficulty: 5, LastReview: now}
		r, err := Retrievability(p, prior, now)
		if err != nil {
			t.Fatalf("Retrievability: %v", err)
		}
		if r < 0.99 || r > 1.0 {
			t.Errorf("r = %v, want ~1.0", r)
		}
	})

	t.Run("decays below desiredRetention past the scheduled interval", func(t *testing.T) {
		prior := CardState{State: Review, Stability: 10, Difficulty: 5, LastReview: now}
		outcome, err := Schedule(p, prior, Good, now)
		if err != nil {
			t.Fatalf("Schedule: %v", err)
		}
		past := outcome.CardStateAt(now).Due.Add(24 * time.Hour)
		r, err := Retrievability(p, outcome.CardStateAt(now), past)
		if err != nil {
			t.Fatalf("Retrievability: %v", err)
		}
		if r >= p.DesiredRetention() {
			t.Errorf("r = %v, want < desiredRetention %v (a day past the scheduled due date)", r, p.DesiredRetention())
		}
	})
}

func TestNewCardOutcomes(t *testing.T) {
	p := mustDefaultParams(t)
	now := fixedNow()
	prior := CardState{State: New}

	preview, err := PreviewAll(p, prior, now)
	if err != nil {
		t.Fatalf("PreviewAll: %v", err)
	}

	if preview.Again.State != Learning {
		t.Errorf("Again.State = %v, want Learning", preview.Again.State)
	}
	if preview.Easy.State != Review {
		t.Errorf("Easy.State = %v, want Review", preview.Easy.State)
	}
	if preview.Good.State != Learning {
		t.Errorf("Good.State = %v, want Learning -- go-fsrs's default {1,10}-minute learning "+
			"steps mean a New card's first Good does not graduate straight to Review; the "+
			"client-side same-session requeue suppression (#136) assumes this", preview.Good.State)
	}
	if d := preview.Good.Due.Sub(now); d < 0 || d >= 24*time.Hour {
		t.Errorf("Good.Due = %v (%.1f hours after now), want a same-day short-term interval "+
			"(#136 depends on this being short, not multi-day)", preview.Good.Due, d.Hours())
	}
	if preview.Easy.ScheduledDays < 1 {
		t.Errorf("Easy.ScheduledDays = %d, want >= 1 -- Easy always graduates a New card "+
			"straight to Review with a multi-day interval, which is why it was the only "+
			"same-session workaround before #136", preview.Easy.ScheduledDays)
	}

	for _, r := range Ratings {
		out, err := preview.For(r)
		if err != nil {
			t.Fatalf("For(%v): %v", r, err)
		}
		if !out.Due.After(now) {
			t.Errorf("rating %v: Due = %v, want after %v", r, out.Due, now)
		}
		if out.Due.Location() != time.UTC {
			t.Errorf("rating %v: Due location = %v, want UTC", r, out.Due.Location())
		}
		if out.Reps != 1 {
			t.Errorf("rating %v: Reps = %d, want 1", r, out.Reps)
		}
	}

	// A New card has never lapsed -- go-fsrs only increments Lapses when a Review-state card
	// is rated Again (scheduler_basic.go's reviewState), not on a brand-new card's first
	// Again (which transitions New -> Learning, not Review -> Relearning).
	for _, r := range Ratings {
		out, _ := preview.For(r)
		if out.Lapses != 0 {
			t.Errorf("rating %v: Lapses = %d, want 0 (New card, never lapsed)", r, out.Lapses)
		}
	}
}

func TestReviewCardAgainIncrementsLapses(t *testing.T) {
	p := mustDefaultParams(t)
	now := fixedNow()
	prior := CardState{
		State:      Review,
		Stability:  20,
		Difficulty: 5,
		Reps:       3,
		LastReview: now.Add(-10 * 24 * time.Hour),
	}

	out, err := Schedule(p, prior, Again, now)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if out.Lapses != 1 {
		t.Errorf("Lapses = %d, want 1", out.Lapses)
	}
	if out.State != Relearning {
		t.Errorf("State = %v, want Relearning", out.State)
	}
}

func TestElapsedDays(t *testing.T) {
	// Noon UTC, not midnight, so there is room to express a same-calendar-day case (no boundary
	// crossed) distinctly from a midnight-crossing one.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		prior CardState
		want  int32
	}{
		{"never reviewed", CardState{}, 0},
		{"now before LastReview", CardState{LastReview: now.Add(time.Hour)}, 0},
		{"1h earlier, same calendar day", CardState{LastReview: now.Add(-1 * time.Hour)}, 0},
		{"23h earlier, crosses one UTC midnight", CardState{LastReview: now.Add(-23 * time.Hour)}, 1},
		{"47h earlier, crosses two UTC midnights", CardState{LastReview: now.Add(-47 * time.Hour)}, 2},
		{"73h earlier, crosses three UTC midnights", CardState{LastReview: now.Add(-73 * time.Hour)}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ElapsedDays(tt.prior, now); got != tt.want {
				t.Errorf("ElapsedDays() = %d, want %d", got, tt.want)
			}
		})
	}
}
