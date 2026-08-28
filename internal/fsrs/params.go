package fsrs

import (
	"fmt"
	"math"

	gofsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

// weightCountFor is the parameter count each FSRS version requires (CLAUDE.md §2.3): 17 for
// FSRS-4.5, 19 for FSRS-5, 21 for FSRS-6.
var weightCountFor = map[int]int{45: 17, 5: 19, 6: 21}

// Params is a validated parameter set. The zero value is not usable; the only ways to obtain
// one are NewParams and NewDefaultParams, so no unvalidated parameter set can reach go-fsrs.
// valid guards exactly that: without it, a zero-value Params{} (retention 0, version 0) would
// still build a runnable go-fsrs engine, because the library's own NewFSRS falls back to its
// built-in defaults on an invalid RequestRetention rather than erroring (see docs/plans/
// 53-fsrs-wrapper.md Resolved Decision 5) -- which would let Schedule/PreviewAll silently
// succeed on an unconstructed Params and record fsrs_version 0.
type Params struct {
	version          int
	weights          gofsrs.Weights
	desiredRetention float64
	valid            bool
}

// Version is the FSRS version these weights were fit for -- stored in review_log.fsrs_version.
func (p Params) Version() int { return p.version }

// DesiredRetention is the target retrievability used to compute intervals.
func (p Params) DesiredRetention() float64 { return p.desiredRetention }

// supportedVersion is the only fsrs_version this package will schedule with: FSRS-6, 21 weights
// (docs/plans/53-fsrs-wrapper.md Resolved Decision 4). 4.5 and 5 stay in weightCountFor so a row
// declaring one gets ErrUnsupportedVersion rather than ErrUnknownVersion.
const supportedVersion = 6

// NewParams validates a stored user_fsrs_params row. weights is the decoded params JSON array;
// fsrsVersion is the row's fsrs_version. Only FSRS-6 (21 weights) is currently supported for
// scheduling -- see docs/plans/53-fsrs-wrapper.md Resolved Decision 4.
func NewParams(fsrsVersion int, weights []float64, desiredRetention float64) (Params, error) {
	wantCount, known := weightCountFor[fsrsVersion]
	if !known {
		return Params{}, fmt.Errorf("%w: %d", ErrUnknownVersion, fsrsVersion)
	}
	if len(weights) != wantCount {
		return Params{}, fmt.Errorf("%w: fsrs_version %d wants %d weights, got %d", ErrWeightCount, fsrsVersion, wantCount, len(weights))
	}
	if fsrsVersion != supportedVersion {
		return Params{}, fmt.Errorf("%w: fsrs_version %d", ErrUnsupportedVersion, fsrsVersion)
	}
	for i, w := range weights {
		if math.IsNaN(w) || math.IsInf(w, 0) {
			return Params{}, fmt.Errorf("%w: W[%d] = %v", ErrNonFiniteWeight, i, w)
		}
	}
	if math.IsNaN(desiredRetention) || math.IsInf(desiredRetention, 0) || desiredRetention <= 0 || desiredRetention > 1 {
		return Params{}, fmt.Errorf("%w: got %v", ErrRetentionRange, desiredRetention)
	}

	var w gofsrs.Weights
	copy(w[:], weights)
	return Params{version: fsrsVersion, weights: w, desiredRetention: desiredRetention, valid: true}, nil
}

// NewDefaultParams is the empty-params-array case: migration 00012 documents `params = '[]'` as
// "use the library defaults", which is the MVP state (architecture.md §11 step 10 defers fitting
// entirely).
func NewDefaultParams(desiredRetention float64) (Params, error) {
	w := gofsrs.DefaultWeights()
	return NewParams(supportedVersion, w[:], desiredRetention)
}

// maxLearningSteps bounds CardState.LearningSteps: go-fsrs's Card.RemainingSteps is only ever
// set to a value in [0, len(steps)] for whichever step list (LearningSteps or RelearningSteps)
// applies, by go-fsrs itself. A value above this -- which can only reach toLibCard via a
// corrupted user_card_state row or a caller bug, since our own Outcome values are always
// derived from a prior go-fsrs result -- silently sends goodDelayMinutes negative-indexing
// logic (scheduler_basic.go) down its "no next step" branch, which graduates the card straight
// to Review instead of erroring or advancing normally. Rejecting it here, rather than letting a
// corrupted count silently reschedule wrong, is the same trust-boundary posture as every other
// toLibCard check.
var maxLearningSteps = max(len(gofsrs.DefaultLearningSteps()), len(gofsrs.DefaultRelearningSteps()))

// engine builds the go-fsrs scheduler over p. Fuzz is always off. The library's seed is
// fmt.Sprintf("%d_%d_%f", now.UnixMilli(), reps, difficulty*stability) (scheduler.go's initSeed)
// -- a pure function of the caller-supplied now, not of the process clock, so it is reproducible
// given identical inputs and replay would stay deterministic either way. What breaks is preview
// parity: the batch-fetch precompute passes the fetch instant and the grade-time recompute passes
// the event's reviewedAt, never the same millisecond, so with fuzz on the two would disagree by
// design -- which would make the CLAUDE.md §10.2 parity property flaky rather than meaningful.
// Pinned by TestLibraryFuzzSeedIsPureInItsInputs and TestLibraryFuzzVariesWithNow
// (schedule_test.go). There is no exported way to turn it on.
func (p Params) engine() *gofsrs.FSRS {
	return gofsrs.NewFSRS(gofsrs.Parameters{
		RequestRetention: p.desiredRetention,
		MaximumInterval:  gofsrs.DefaultParam().MaximumInterval,
		W:                p.weights,
		EnableShortTerm:  true,
		EnableFuzz:       false,
		LearningSteps:    gofsrs.DefaultLearningSteps(),
		RelearningSteps:  gofsrs.DefaultRelearningSteps(),
	})
}
