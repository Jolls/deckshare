package review

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/fsrs"
)

// Status is the per-event outcome of a graded review (architecture.md §6, plan §0.7).
type Status string

const (
	StatusApplied   Status = "applied"
	StatusDuplicate Status = "duplicate"
	StatusForbidden Status = "forbidden"
	StatusRejected  Status = "rejected"
)

// Event is exactly what the client is entitled to assert (CLAUDE.md §2.7, architecture.md §6):
// which card, which rating, when, and how long it took. Nothing else exists on this type, so no
// client-supplied scheduling value has anywhere to be stored.
type Event struct {
	ID         pgtype.UUID
	CardID     pgtype.UUID
	Rating     fsrs.Rating
	ReviewedAt time.Time // UTC, microsecond-truncated, already clamped
	DurationMs *int32
}

// Result is one event's outcome from GradeBatch.
type Result struct {
	ID     pgtype.UUID
	CardID pgtype.UUID
	Status Status
	After  *CardStateDTO // nil for forbidden/rejected
}

// CardStateDTO is the JSON shape of a graded card's resulting state, and the source of the four
// data-* branch attribute sets on a hidden card. One type, so the preview the client applies and
// the state the server returns can never describe a card differently.
type CardStateDTO struct {
	Due           time.Time  `json:"due"`
	State         uint8      `json:"state"`
	Stability     float64    `json:"stability"`
	Difficulty    float64    `json:"difficulty"`
	Reps          int32      `json:"reps"`
	Lapses        int32      `json:"lapses"`
	ScheduledDays int32      `json:"scheduledDays"`
	LearningSteps int16      `json:"learningSteps"`
	LastReview    *time.Time `json:"lastReview,omitempty"`
}

// ErrMalformedCursor is returned by DecodeCursor for an unparseable string.
var ErrMalformedCursor = errors.New("review: malformed cursor")

// Cursor is the opaque keyset position over the reviewer's queue (architecture.md §6, plan §0.10).
// The zero Cursor (AtStart true) is the start of the queue. Infinite stands for the queue_key a
// never-seen card carries ('infinity'::timestamptz, since it has no user_card_state row); Due is
// only meaningful when neither AtStart nor Infinite.
type Cursor struct {
	AtStart  bool
	Infinite bool
	Due      time.Time
	CardID   pgtype.UUID
}

// EncodeCursor renders c as an opaque string safe to round-trip through a URL query parameter.
// AtStart encodes as "" so a fresh page load needs no cursor at all.
func EncodeCursor(c Cursor) string {
	if c.AtStart {
		return ""
	}
	micros := int64(math.MaxInt64)
	if !c.Infinite {
		micros = c.Due.UTC().UnixMicro()
	}
	raw := strconv.FormatInt(micros, 10) + ":" + c.CardID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a string produced by EncodeCursor. "" decodes to the start-of-queue Cursor.
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{AtStart: true}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	due, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return Cursor{}, fmt.Errorf("%w: missing separator", ErrMalformedCursor)
	}
	micros, err := strconv.ParseInt(due, 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	var cardID pgtype.UUID
	if err := cardID.Scan(id); err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	if micros == math.MaxInt64 {
		return Cursor{Infinite: true, CardID: cardID}, nil
	}
	return Cursor{Due: time.UnixMicro(micros).UTC(), CardID: cardID}, nil
}

// dueArg and cardIDArg convert c into the two query parameters ListDueCardsForStudy compares the
// keyset against: pgtype.Timestamptz's native infinity modifiers express -infinity (start of
// queue) and +infinity (resume after a never-seen card) without a magic sentinel time.Time.
func (c Cursor) dueArg() pgtype.Timestamptz {
	switch {
	case c.AtStart:
		return pgtype.Timestamptz{InfinityModifier: pgtype.NegativeInfinity, Valid: true}
	case c.Infinite:
		return pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
	default:
		return pgtype.Timestamptz{Time: c.Due, Valid: true}
	}
}

func (c Cursor) cardIDArg() pgtype.UUID {
	if c.AtStart {
		return pgtype.UUID{Valid: true} // all-zero bytes: less than every real (UUIDv7) card id
	}
	return c.CardID
}
