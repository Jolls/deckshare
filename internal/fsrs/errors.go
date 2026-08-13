package fsrs

import "errors"

var (
	ErrUnknownVersion     = errors.New("fsrs: unknown fsrs_version")
	ErrUnsupportedVersion = errors.New("fsrs: unsupported fsrs_version (FSRS-6 only)")
	ErrWeightCount        = errors.New("fsrs: weight count does not match fsrs_version")
	ErrNonFiniteWeight    = errors.New("fsrs: non-finite weight")
	ErrRetentionRange     = errors.New("fsrs: desired_retention outside (0, 1]")
	ErrInvalidRating      = errors.New("fsrs: invalid rating")
	ErrInvalidCardState   = errors.New("fsrs: invalid card state")
	ErrSchedule           = errors.New("fsrs: go-fsrs rejected the input")
	ErrUnvalidatedParams  = errors.New("fsrs: zero-value Params -- use NewParams or NewDefaultParams")
)
