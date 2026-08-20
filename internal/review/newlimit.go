package review

import "encoding/json"

// The per-deck daily new-card limit (#101) and review-card limit (#115), stored under
// decks.preset as Anki's own dconf shape: {"new":{"perDay":20},"rev":{"perDay":200}}. Parsed here
// rather than in SQL so a malformed preset degrades to the default instead of failing every study
// fetch (Postgres has no safe cast).
const (
	DefaultNewPerDay int32 = 20   // Anki's classic default
	MaxNewPerDay     int32 = 9999 // Anki's own upper bound

	DefaultRevPerDay int32 = 200  // Anki's classic default
	MaxRevPerDay     int32 = 9999 // Anki's own upper bound
)

type deckPreset struct {
	New *struct {
		PerDay *int32 `json:"perDay"`
	} `json:"new"`
	Rev *struct {
		PerDay *int32 `json:"perDay"`
	} `json:"rev"`
}

// NewPerDay reads decks.preset. Absent, malformed, or out of range -> DefaultNewPerDay; a value
// inside 0..MaxNewPerDay is returned as written, 0 included (no new cards from this deck).
func NewPerDay(preset []byte) int32 {
	var p deckPreset
	if err := json.Unmarshal(preset, &p); err != nil || p.New == nil || p.New.PerDay == nil {
		return DefaultNewPerDay
	}
	v := *p.New.PerDay
	if v < 0 || v > MaxNewPerDay {
		return DefaultNewPerDay
	}
	return v
}

// NewRemaining is the deck's new-card allowance left in the current study day, never negative.
func NewRemaining(perDay int32, introducedToday int64) int32 {
	remaining := perDay - int32(introducedToday)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RevPerDay reads decks.preset. Absent, malformed, or out of range -> DefaultRevPerDay; a value
// inside 0..MaxRevPerDay is returned as written, 0 included (no review-state cards from this
// deck). Enforced completely independently of NewPerDay (#115), same as Anki's own model.
func RevPerDay(preset []byte) int32 {
	var p deckPreset
	if err := json.Unmarshal(preset, &p); err != nil || p.Rev == nil || p.Rev.PerDay == nil {
		return DefaultRevPerDay
	}
	v := *p.Rev.PerDay
	if v < 0 || v > MaxRevPerDay {
		return DefaultRevPerDay
	}
	return v
}

// RevRemaining is the deck's review-card allowance left in the current study day, never negative.
func RevRemaining(perDay int32, reviewedToday int64) int32 {
	remaining := perDay - int32(reviewedToday)
	if remaining < 0 {
		return 0
	}
	return remaining
}
