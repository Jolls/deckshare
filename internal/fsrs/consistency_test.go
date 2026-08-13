package fsrs

import (
	"math/rand/v2"
	"testing"
	"time"

	gofsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

// CLAUDE.md §10.2, the top testing priority for this package: batch-preview branches precomputed
// at fetch time must match what the server independently recomputes at grade time for the same
// prior state, property-based over random review sequences. "Same prior state" means the
// identical CardState value *and* the identical now passed to both calls -- Schedule and
// PreviewAll are exercised at the same instant here, which is the property that matters; a
// preview going stale as wall-clock advances between fetch and grade is an expected UX property
// (architecture.md §6), not a bug this test polices.
//
// Manual random-sequence generator, not testing/quick: quick.Check fills structs by reflection,
// so nearly every generated CardState would violate go-fsrs's own validity bounds and the test
// would mostly exercise the rejection path instead of the parity property. A valid *sequence*
// also has cross-field invariants (LastReview monotone non-decreasing, State reachable only via
// an actual prior Schedule call, Stability/Difficulty only ever the library's own output) that a
// reflection-based Generator can't express without being hand-written anyway -- and quick.Check
// gives no seed-based reproduction on failure. A fixed-seed manual generator does.
const (
	seedA uint64 = 0x5253_4653
	seedB uint64 = 0x0000_0001
)

func jitteredDefaultWeights(rng *rand.Rand) []float64 {
	w := gofsrs.DefaultWeights()
	out := make([]float64, len(w))
	for i, v := range w {
		// +-1% jitter, small enough to stay inside every clampRanges bound in go-fsrs's
		// clipParameters (params.go's engine comment) so this generator never accidentally
		// produces a set the library would clamp.
		out[i] = v * (1 + (rng.Float64()-0.5)*0.02)
	}
	return out
}

// applyOutcome writes an Outcome back into a CardState the way internal/review will eventually
// write a graded Outcome into user_card_state: this is what makes each step of the sequence a
// prior state derived from a real prior Schedule call, not a synthetic one.
func applyOutcome(out Outcome, now time.Time) CardState {
	return CardState{
		Due:           out.Due,
		Stability:     out.Stability,
		Difficulty:    out.Difficulty,
		State:         out.State,
		Reps:          out.Reps,
		Lapses:        out.Lapses,
		ScheduledDays: out.ScheduledDays,
		LearningSteps: out.LearningSteps,
		LastReview:    now,
	}
}

func outcomesEqual(a, b Outcome) bool {
	return a.Due.Equal(b.Due) &&
		a.Due.Location() == time.UTC && b.Due.Location() == time.UTC &&
		a.Stability == b.Stability &&
		a.Difficulty == b.Difficulty &&
		a.State == b.State &&
		a.Reps == b.Reps &&
		a.Lapses == b.Lapses &&
		a.ScheduledDays == b.ScheduledDays &&
		a.LearningSteps == b.LearningSteps
}

// randomDelta mixes minute-scale deltas (to exercise learning steps) with day-scale deltas well
// past the 2.5-day fuzz threshold (to exercise review/relearning intervals) -- fuzz is off
// regardless (params.go's engine), but the interval math itself has different code paths at
// short vs. long elapsed times and this generator needs to reach both.
func randomDelta(rng *rand.Rand) time.Duration {
	if rng.IntN(2) == 0 {
		return time.Duration(1+rng.IntN(90)) * time.Minute
	}
	return time.Duration(1+rng.IntN(400)) * 24 * time.Hour
}

func TestPreviewMatchesRecompute(t *testing.T) {
	rng := rand.New(rand.NewPCG(seedA, seedB))
	t.Logf("seed = (%#x, %#x)", seedA, seedB)

	const sequences = 500

	for seq := 0; seq < sequences; seq++ {
		var p Params
		var err error
		if seq%2 == 0 {
			p, err = NewDefaultParams(0.9)
		} else {
			retention := 0.7 + rng.Float64()*0.28
			p, err = NewParams(6, jitteredDefaultWeights(rng), retention)
		}
		if err != nil {
			t.Fatalf("sequence %d: build params: %v", seq, err)
		}

		state := CardState{State: New}
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		reviews := 1 + rng.IntN(20)

		for step := 0; step < reviews; step++ {
			now = now.Add(randomDelta(rng))

			preview, err := PreviewAll(p, state, now)
			if err != nil {
				t.Fatalf("sequence %d step %d: PreviewAll: %v", seq, step, err)
			}

			for _, r := range Ratings {
				want, err := preview.For(r)
				if err != nil {
					t.Fatalf("sequence %d step %d: Preview.For(%v): %v", seq, step, r, err)
				}
				got, err := Schedule(p, state, r, now)
				if err != nil {
					t.Fatalf("sequence %d step %d: Schedule(%v): %v", seq, step, r, err)
				}
				if !outcomesEqual(want, got) {
					t.Fatalf("sequence %d step %d rating %v: PreviewAll = %+v, Schedule = %+v (prior state = %+v, now = %v)",
						seq, step, r, want, got, state, now)
				}
			}

			chosen := Ratings[rng.IntN(len(Ratings))]
			out, err := preview.For(chosen)
			if err != nil {
				t.Fatalf("sequence %d step %d: Preview.For(%v): %v", seq, step, chosen, err)
			}
			state = applyOutcome(out, now)
		}
	}
}

func TestScheduleIsDeterministic(t *testing.T) {
	rng := rand.New(rand.NewPCG(seedA, seedB+1))
	p := mustDefaultParams(t)
	state := CardState{State: New}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for step := 0; step < 20; step++ {
		now = now.Add(randomDelta(rng))
		r := Ratings[rng.IntN(len(Ratings))]

		first, err := Schedule(p, state, r, now)
		if err != nil {
			t.Fatalf("step %d: Schedule: %v", step, err)
		}
		second, err := Schedule(p, state, r, now)
		if err != nil {
			t.Fatalf("step %d: Schedule (repeat): %v", step, err)
		}
		if !outcomesEqual(first, second) {
			t.Fatalf("step %d: Schedule(%v, %v) not deterministic: %+v != %+v -- fuzz may have been re-enabled", step, state, now, first, second)
		}
		state = applyOutcome(first, now)
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	rng := rand.New(rand.NewPCG(seedA, seedB+2))
	p := mustDefaultParams(t)

	type step struct {
		delta  time.Duration
		rating Rating
	}
	var steps []step
	for i := 0; i < 15; i++ {
		steps = append(steps, step{randomDelta(rng), Ratings[rng.IntN(len(Ratings))]})
	}

	replay := func() CardState {
		state := CardState{State: New}
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for _, s := range steps {
			now = now.Add(s.delta)
			out, err := Schedule(p, state, s.rating, now)
			if err != nil {
				t.Fatalf("Schedule: %v", err)
			}
			state = applyOutcome(out, now)
		}
		return state
	}

	first := replay()
	second := replay()
	if first != second {
		t.Fatalf("replaying the same sequence twice diverged: %+v != %+v", first, second)
	}
}
