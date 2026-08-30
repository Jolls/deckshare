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

	// DefaultDueLookAheadMinutes 0 matches the due<=now tightening (#154, split off #155): a card
	// is only ever offered at or after its own due instant, never early. MaxDueLookAheadMinutes
	// caps it at 24h -- the same span the old study_day_end-based window implicitly allowed before
	// #155 removed it -- so this setting can restore that old behaviour at most, not exceed it.
	DefaultDueLookAheadMinutes int32 = 0
	MaxDueLookAheadMinutes     int32 = 1440
)

// deckPreset is the whole of decks.preset (#101, #115, #116, #118, #154): per-deck daily caps plus
// review order, review prioritization, and the due-date look-ahead window. One struct so every
// reader (NewPerDay, RevPerDay, ParseRevOrder, ParsePriority, DueLookAheadMinutes) shares one
// JSON-unmarshal/degrade-on-error path. Priority is top-level, not nested under New, since it
// governs the whole day's new/due split (#118) rather than describing new-card mixing the way its
// predecessor (new.mix) did. new.Mix itself stays here, read-only, purely so ParsePriority can
// translate an existing deck's pre-#118 choice instead of silently discarding it -- see
// ParsePriority's doc comment.
type deckPreset struct {
	New *struct {
		PerDay *int32  `json:"perDay"`
		Mix    *string `json:"mix"` // legacy; superseded by top-level Priority, #118
	} `json:"new"`
	Rev *struct {
		PerDay *int32  `json:"perDay"`
		Order  *string `json:"order"`
	} `json:"rev"`
	Priority *string `json:"priority"`
	Due      *struct {
		LookAheadMinutes *int32 `json:"lookAheadMinutes"`
	} `json:"due"`
}

// parseDeckPreset unmarshals preset, ok=false on malformed JSON (all fields degrade to their
// defaults in that case, same as a nil/empty preset).
func parseDeckPreset(preset []byte) (deckPreset, bool) {
	var p deckPreset
	if err := json.Unmarshal(preset, &p); err != nil {
		return deckPreset{}, false
	}
	return p, true
}

// NewPerDay reads decks.preset. Absent, malformed, or out of range -> DefaultNewPerDay; a value
// inside 0..MaxNewPerDay is returned as written, 0 included (no new cards from this deck).
func NewPerDay(preset []byte) int32 {
	p, ok := parseDeckPreset(preset)
	if !ok || p.New == nil || p.New.PerDay == nil {
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
// inside 0..MaxRevPerDay is returned as written, 0 included (no cards from this deck at all).
// Originally an independent due-only cap (#115); redefined by #118 as the deck's combined
// new+due daily total -- new.perDay still separately ceilings how many of that total can be new,
// via PriorityAllocate.
func RevPerDay(preset []byte) int32 {
	p, ok := parseDeckPreset(preset)
	if !ok || p.Rev == nil || p.Rev.PerDay == nil {
		return DefaultRevPerDay
	}
	v := *p.Rev.PerDay
	if v < 0 || v > MaxRevPerDay {
		return DefaultRevPerDay
	}
	return v
}

// RevRemaining is the deck's combined new+due allowance left in the current study day (#118),
// never negative. studiedToday is introducedToday+reviewedToday summed by the caller -- both
// new and due consumption count against this one shared budget.
func RevRemaining(perDay int32, studiedToday int64) int32 {
	remaining := perDay - int32(studiedToday)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// PriorityAllocate splits totalRemaining (RevRemaining's return value) between new and due cards
// for the current fetch, per the deck's priority mode (#118). newCeiling is NewRemaining's return
// value -- new cards are never allowed past it regardless of priority. newAvailable/dueAvailable
// are the actual eligible counts (e.g. CountQueueForDeck), so a scarce side never over-reports its
// allowance.
//
// due/new: fill the priority side first up to totalRemaining (bounded by its own ceiling/
// availability), the other side backfills whatever's left of totalRemaining -- so the two
// allowances never sum past totalRemaining. mixed: each side capped independently with no
// cross-awareness -- #115's own spec for "no priority" is "whatever the merge produces, simply
// truncated to the total" -- so their sum can exceed totalRemaining; LeftToStudy, the only
// caller, clamps the sum itself rather than this function doing it, since BuildBatch's actual
// fetch path (effectiveLimit + interleave) truncates independently and doesn't call this
// function at all.
func PriorityAllocate(priority Priority, newCeiling, totalRemaining int32, newAvailable, dueAvailable int64) (newAllowance, dueAllowance int32) {
	ceiling, total := int64(newCeiling), int64(totalRemaining)
	switch priority {
	case PriorityNew:
		newAllowance = int32(min(ceiling, newAvailable, total))
		dueAllowance = int32(min(total-int64(newAllowance), dueAvailable))
	case PriorityDue:
		dueAllowance = int32(min(total, dueAvailable))
		newAllowance = int32(min(ceiling, total-int64(dueAllowance), newAvailable))
	default: // PriorityMixed
		newAllowance = int32(min(ceiling, newAvailable))
		dueAllowance = int32(min(total, dueAvailable))
	}
	return newAllowance, dueAllowance
}

// DueLookAheadMinutes reads decks.preset (#154). Absent, malformed, or out of range ->
// DefaultDueLookAheadMinutes (0, i.e. due<=now only); a value inside 0..MaxDueLookAheadMinutes is
// returned as written, 0 included.
func DueLookAheadMinutes(preset []byte) int32 {
	p, ok := parseDeckPreset(preset)
	if !ok || p.Due == nil || p.Due.LookAheadMinutes == nil {
		return DefaultDueLookAheadMinutes
	}
	v := *p.Due.LookAheadMinutes
	if v < 0 || v > MaxDueLookAheadMinutes {
		return DefaultDueLookAheadMinutes
	}
	return v
}

// LeftToStudy is how many of a deck's New/Learning/Due cards are actually left to study today
// (#137, #118): New and Due share the combined totalRemaining budget, split per priority via
// PriorityAllocate; Learning/relearning cards are never capped by the daily-limit system (same
// rule BuildBatch and ListDueCardsForStudy enforce -- state 1/3 rows are excluded from both the
// new-card and review-card cap checks).
//
// PriorityAllocate's mixed mode deliberately doesn't cap its own new+due sum against
// totalRemaining (that's BuildBatch's job, via effectiveLimit, for the actual fetch path -- see
// PriorityAllocate's doc comment). This is the one caller that isn't BuildBatch, so it applies
// that same cap itself: over a full study day every priority mode converges to serving at most
// totalRemaining combined new+due cards (BuildBatch's effectiveLimit eventually hits 0 regardless
// of mode), so "left to study" must never report more than that, in any mode.
func LeftToStudy(newCount, learningCount, dueCount int64, priority Priority, newRemaining, totalRemaining int32) int64 {
	newAllowance, dueAllowance := PriorityAllocate(priority, newRemaining, totalRemaining, newCount, dueCount)
	total := int64(newAllowance) + int64(dueAllowance)
	if total > int64(totalRemaining) {
		total = int64(totalRemaining)
	}
	return total + learningCount
}
