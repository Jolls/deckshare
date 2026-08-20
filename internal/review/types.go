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
// The zero Cursor (AtStart true) is the start of the queue.
//
// Single-query mode (rev.order due/random/interval, new.mix afterReviews/beforeReviews): GroupBit,
// Key, and CardID are the (group_bit, sort_key, card_id) position ListDueCardsForStudy's keyset
// compares against -- see #116 doc comment on that query for why group_bit is a separate column
// rather than folded into Key.
//
// Mixed mode (new.mix = "mixed"): Mixed is true and the position is the pair of independent
// sub-cursors over ListReviewCardsForStudy (RevAtStart/RevKey/RevCardID -- no group bit, this
// query only ever serves one group) and ListNewCardsForStudy (NewAtStart/NewCardID, ordered by
// id alone).
type Cursor struct {
	AtStart  bool
	GroupBit int32
	Key      float64
	CardID   pgtype.UUID

	Mixed      bool
	RevAtStart bool
	RevKey     float64
	RevCardID  pgtype.UUID
	NewAtStart bool
	NewCardID  pgtype.UUID
}

// EncodeCursor renders c as an opaque string safe to round-trip through a URL query parameter.
// AtStart encodes as "" so a fresh page load needs no cursor at all.
func EncodeCursor(c Cursor) string {
	if c.AtStart {
		return ""
	}
	var raw string
	if c.Mixed {
		raw = "m:" + encodeSubCursor(c.RevAtStart, c.RevKey, c.RevCardID) + "|" + encodeIDCursor(c.NewAtStart, c.NewCardID)
	} else {
		raw = strconv.Itoa(int(c.GroupBit)) + ":" + strconv.FormatUint(math.Float64bits(c.Key), 10) + ":" + c.CardID.String()
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// encodeSubCursor renders a (key, card_id) position; atStart encodes as "-" (used only by the
// mixed-mode sub-cursors -- the top-level Cursor.AtStart short-circuits before this is called).
// The key is carried as math.Float64bits' decimal digits, not a formatted float, so it round-trips
// bit-exact regardless of rev.order mode.
func encodeSubCursor(atStart bool, key float64, cardID pgtype.UUID) string {
	if atStart {
		return "-"
	}
	return strconv.FormatUint(math.Float64bits(key), 10) + ":" + cardID.String()
}

func encodeIDCursor(atStart bool, cardID pgtype.UUID) string {
	if atStart {
		return "-"
	}
	return cardID.String()
}

// DecodeCursor parses a string produced by EncodeCursor. "" decodes to the start-of-queue Cursor.
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{AtStart: true}, nil
	}
	rawBytes, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	raw := string(rawBytes)

	if rest, ok := strings.CutPrefix(raw, "m:"); ok {
		revPart, newPart, ok := strings.Cut(rest, "|")
		if !ok {
			return Cursor{}, fmt.Errorf("%w: missing mixed-cursor separator", ErrMalformedCursor)
		}
		revAtStart, revKey, revID, err := decodeSubCursor(revPart)
		if err != nil {
			return Cursor{}, err
		}
		newAtStart, newID, err := decodeIDCursor(newPart)
		if err != nil {
			return Cursor{}, err
		}
		return Cursor{
			Mixed:      true,
			RevAtStart: revAtStart, RevKey: revKey, RevCardID: revID,
			NewAtStart: newAtStart, NewCardID: newID,
		}, nil
	}

	groupBitPart, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return Cursor{}, fmt.Errorf("%w: missing group-bit separator", ErrMalformedCursor)
	}
	groupBit, err := strconv.Atoi(groupBitPart)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	_, key, id, err := decodeSubCursor(rest)
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{GroupBit: int32(groupBit), Key: key, CardID: id}, nil
}

// decodeSubCursor parses a (key, card_id) sub-cursor payload ("-" for a fresh one), shared by the
// single-query top-level cursor and the mixed-mode review sub-cursor.
func decodeSubCursor(s string) (atStart bool, key float64, cardID pgtype.UUID, err error) {
	if s == "-" {
		return true, 0, pgtype.UUID{}, nil
	}
	keyPart, idPart, ok := strings.Cut(s, ":")
	if !ok {
		return false, 0, pgtype.UUID{}, fmt.Errorf("%w: missing separator", ErrMalformedCursor)
	}
	bits, err := strconv.ParseUint(keyPart, 10, 64)
	if err != nil {
		return false, 0, pgtype.UUID{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	var id pgtype.UUID
	if err := id.Scan(idPart); err != nil {
		return false, 0, pgtype.UUID{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	return false, math.Float64frombits(bits), id, nil
}

func decodeIDCursor(s string) (atStart bool, cardID pgtype.UUID, err error) {
	if s == "-" {
		return true, pgtype.UUID{}, nil
	}
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return false, pgtype.UUID{}, fmt.Errorf("%w: %v", ErrMalformedCursor, err)
	}
	return false, id, nil
}

// groupBitArg, keyArg, and cardIDArg convert c into the query parameters the single-query path
// (ListDueCardsForStudy) compares the keyset against. AtStart uses group_bit -1 (below both real
// values 0/1) so the group_bit comparison alone puts it before every real row; Key still uses
// -Inf for good measure (sort_key can be negative -- intervalDesc's raw_key is -scheduled_days --
// so only -Inf is guaranteed to sort before every real sort_key within a group).
func (c Cursor) groupBitArg() int32 {
	if c.AtStart {
		return -1
	}
	return c.GroupBit
}

func (c Cursor) keyArg() float64 {
	if c.AtStart {
		return math.Inf(-1)
	}
	return c.Key
}

func (c Cursor) cardIDArg() pgtype.UUID {
	if c.AtStart {
		return pgtype.UUID{Valid: true} // all-zero bytes: less than every real (UUIDv7) card id
	}
	return c.CardID
}

// revKeyArg, revCardIDArg, and newCardIDArg are keyArg/cardIDArg's mixed-mode counterparts, for
// the two independent ListReviewCardsForStudy/ListNewCardsForStudy sub-cursors. c.AtStart (the
// zero Cursor, produced by Cursor{AtStart: true} without setting Mixed/RevAtStart/NewAtStart)
// means "fresh" for both sub-cursors too, not just RevAtStart/NewAtStart individually.
func (c Cursor) revKeyArg() float64 {
	if c.AtStart || c.RevAtStart {
		return math.Inf(-1)
	}
	return c.RevKey
}

func (c Cursor) revCardIDArg() pgtype.UUID {
	if c.AtStart || c.RevAtStart {
		return pgtype.UUID{Valid: true}
	}
	return c.RevCardID
}

func (c Cursor) newCardIDArg() pgtype.UUID {
	if c.AtStart || c.NewAtStart {
		return pgtype.UUID{Valid: true}
	}
	return c.NewCardID
}
