package fsrs

import (
	"errors"
	"math"
	"testing"

	gofsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

func defaultWeights() []float64 {
	w := gofsrs.DefaultWeights()
	return w[:]
}

func TestNewParams_Rejections(t *testing.T) {
	tests := []struct {
		name             string
		fsrsVersion      int
		weights          []float64
		desiredRetention float64
		want             error
	}{
		{"unknown version", 7, defaultWeights(), 0.9, ErrUnknownVersion},
		{"version 6 wrong count (19)", 6, defaultWeights()[:19], 0.9, ErrWeightCount},
		{"version 5 wrong count (21)", 5, defaultWeights(), 0.9, ErrWeightCount},
		{"version 45 wrong count (21)", 45, defaultWeights(), 0.9, ErrWeightCount},
		{"version 5 unsupported", 5, defaultWeights()[:19], 0.9, ErrUnsupportedVersion},
		{"version 45 unsupported", 45, defaultWeights()[:17], 0.9, ErrUnsupportedVersion},
		{"NaN weight", 6, withWeight(defaultWeights(), 3, math.NaN()), 0.9, ErrNonFiniteWeight},
		{"+Inf weight", 6, withWeight(defaultWeights(), 3, math.Inf(1)), 0.9, ErrNonFiniteWeight},
		{"-Inf weight", 6, withWeight(defaultWeights(), 3, math.Inf(-1)), 0.9, ErrNonFiniteWeight},
		{"retention 0", 6, defaultWeights(), 0, ErrRetentionRange},
		{"retention negative", 6, defaultWeights(), -0.1, ErrRetentionRange},
		{"retention above 1", 6, defaultWeights(), 1.5, ErrRetentionRange},
		{"retention NaN", 6, defaultWeights(), math.NaN(), ErrRetentionRange},
		{"retention +Inf", 6, defaultWeights(), math.Inf(1), ErrRetentionRange},
		{"nil weights", 6, nil, 0.9, ErrWeightCount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewParams(tt.fsrsVersion, tt.weights, tt.desiredRetention)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewParams() error = %v, want %v", err, tt.want)
			}
			if p != (Params{}) {
				t.Error("Params should be the zero value on error")
			}
		})
	}
}

func TestNewParams_Accepted(t *testing.T) {
	p, err := NewParams(6, defaultWeights(), 0.9)
	if err != nil {
		t.Fatalf("NewParams: %v", err)
	}
	if p.Version() != 6 {
		t.Errorf("Version() = %d, want 6", p.Version())
	}
	if p.DesiredRetention() != 0.9 {
		t.Errorf("DesiredRetention() = %v, want 0.9", p.DesiredRetention())
	}
}

func TestNewParams_RetentionOne(t *testing.T) {
	if _, err := NewParams(6, defaultWeights(), 1.0); err != nil {
		t.Errorf("NewParams with retention 1.0: %v, want accepted", err)
	}
}

func TestNewDefaultParams(t *testing.T) {
	p, err := NewDefaultParams(0.9)
	if err != nil {
		t.Fatalf("NewDefaultParams: %v", err)
	}
	if p.Version() != 6 {
		t.Errorf("Version() = %d, want 6", p.Version())
	}
}

func TestZeroParamsRejected(t *testing.T) {
	now := fixedNow()
	prior := CardState{State: New}

	if _, err := Schedule(Params{}, prior, Good, now); err == nil {
		t.Error("Schedule with zero Params should error")
	}
	if _, err := PreviewAll(Params{}, prior, now); err == nil {
		t.Error("PreviewAll with zero Params should error")
	}
}

func withWeight(weights []float64, idx int, v float64) []float64 {
	out := make([]float64, len(weights))
	copy(out, weights)
	out[idx] = v
	return out
}

// The four tests below characterise go-fsrs itself, not our wrapper. They pin the second thing
// architecture.md §3 flagged as unverified, so the reason NewParams validates before calling
// NewFSRS is an executable fact rather than only prose. NewFSRS (fsrs.go) clips every weight into
// a hard-coded per-index range and then, if Validate() still fails, silently replaces the entire
// Parameters value with DefaultParam(). Neither path returns an error.

func libParams(w gofsrs.Weights, retention float64) gofsrs.Parameters {
	return gofsrs.Parameters{
		RequestRetention: retention,
		MaximumInterval:  36500,
		W:                w,
		EnableShortTerm:  true,
		LearningSteps:    gofsrs.DefaultLearningSteps(),
		RelearningSteps:  gofsrs.DefaultRelearningSteps(),
	}
}

func TestLibraryClipsOutOfRangeWeightSilently(t *testing.T) {
	w := gofsrs.DefaultWeights()
	w[4] = 999 // clipParameters' range for W[4] is {1.0, 10.0} (parameters.go)

	engine := gofsrs.NewFSRS(libParams(w, 0.9))
	if engine.W[4] != 10.0 {
		t.Errorf("W[4] = %v, want 10 -- silently clipped, no error returned; if this now errors or preserves the value, revisit enshu#67", engine.W[4])
	}
}

func TestLibraryReplacesTheWholeSetOnNonFiniteWeight(t *testing.T) {
	w := gofsrs.DefaultWeights()
	w[3] = math.NaN() // clamp(NaN, lo, hi) is NaN, so this survives clipping and Validate still fails

	engine := gofsrs.NewFSRS(libParams(w, 0.75))
	if engine.W != gofsrs.DefaultWeights() {
		t.Errorf("W = %v, want DefaultWeights() -- one NaN weight discards the entire fitted set, silently", engine.W)
	}
	if engine.RequestRetention != 0.9 {
		t.Errorf("RequestRetention = %v, want 0.9 -- the fallback replaces the whole Parameters value, so the user's desired_retention goes with the weights", engine.RequestRetention)
	}
}

func TestLibraryReplacesRetentionOutOfRange(t *testing.T) {
	engine := gofsrs.NewFSRS(libParams(gofsrs.DefaultWeights(), 1.5))
	if engine.RequestRetention != 0.9 {
		t.Errorf("RequestRetention = %v, want 0.9 -- a hand-edited desired_retention of 1.5 schedules at 0.9 with no error, which is what ErrRetentionRange catches first", engine.RequestRetention)
	}
}

// TestOurValidationDoesNotCatchClipping records the gap enshu#67 tracks as a test rather than a
// paragraph nobody re-reads: a finite but out-of-range weight passes NewParams (which checks
// count, finiteness and retention -- not per-index ranges) and is then silently clipped by the
// library. Behaviour unchanged by #125; if a later change makes NewParams reject this, the test
// fails and is the place to record the new decision.
func TestOurValidationDoesNotCatchClipping(t *testing.T) {
	p, err := NewParams(6, withWeight(defaultWeights(), 4, 999), 0.9)
	if err != nil {
		t.Fatalf("NewParams: %v, want accepted (enshu#67: no clip detection)", err)
	}
	if got := p.engine().W[4]; got != 10.0 {
		t.Errorf("engine W[4] = %v, want 10 -- accepted by us, clipped by the library", got)
	}
}
