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
