package review

// RevOrder is the per-deck review-card ordering (#116, decks.preset "rev.order"), one of Anki's
// own review-order dconf options.
type RevOrder string

const (
	RevOrderDue          RevOrder = "due"
	RevOrderRandom       RevOrder = "random"
	RevOrderIntervalAsc  RevOrder = "intervalAsc"
	RevOrderIntervalDesc RevOrder = "intervalDesc"
)

// Priority is the per-deck review prioritization (#118, decks.preset top-level "priority"): which
// side of the new/due split gets filled first when the deck's combined daily total (rev.perDay,
// #115) binds, with the other side backfilling the remainder. "mixed" leaves the two sides
// independently capped with no cross-awareness, truncated to the total (see PriorityAllocate,
// BuildBatch). Not an ordering setting -- rev.order already owns sort order within due cards.
type Priority string

const (
	PriorityDue   Priority = "due"
	PriorityNew   Priority = "new"
	PriorityMixed Priority = "mixed"
)

// Valid reports whether o is one of the four recognised rev.order values -- used by the deck
// settings form to reject an unrecognised value with 400 rather than silently degrading it.
func (o RevOrder) Valid() bool {
	switch o {
	case RevOrderDue, RevOrderRandom, RevOrderIntervalAsc, RevOrderIntervalDesc:
		return true
	default:
		return false
	}
}

// Valid reports whether p is one of the three recognised priority values, Priority's counterpart
// to RevOrder.Valid.
func (p Priority) Valid() bool {
	switch p {
	case PriorityDue, PriorityNew, PriorityMixed:
		return true
	default:
		return false
	}
}

// ParseRevOrder reads decks.preset. Absent, malformed, or unrecognised -> RevOrderDue, the
// existing (and Anki's own classic) default -- same degrade-to-default philosophy as
// NewPerDay/RevPerDay: a bad preset should never fail a study fetch.
func ParseRevOrder(preset []byte) RevOrder {
	p, ok := parseDeckPreset(preset)
	if !ok || p.Rev == nil || p.Rev.Order == nil {
		return RevOrderDue
	}
	switch RevOrder(*p.Rev.Order) {
	case RevOrderDue, RevOrderRandom, RevOrderIntervalAsc, RevOrderIntervalDesc:
		return RevOrder(*p.Rev.Order)
	default:
		return RevOrderDue
	}
}

// ParsePriority reads decks.preset's top-level "priority" key. Absent, malformed, or
// unrecognised falls back to translating a pre-#118 "new.mix" value if the deck has one (a deck
// edited before #118 shipped never got a priority key written, and defaulting it to PriorityDue
// would silently reverse a deck deliberately set to beforeReviews); with neither key present,
// PriorityDue matches the pre-#118 default ordering (due cards before new).
func ParsePriority(preset []byte) Priority {
	p, ok := parseDeckPreset(preset)
	if !ok {
		return PriorityDue
	}
	if p.Priority != nil {
		switch Priority(*p.Priority) {
		case PriorityDue, PriorityNew, PriorityMixed:
			return Priority(*p.Priority)
		}
		return PriorityDue
	}
	if p.New != nil && p.New.Mix != nil {
		switch *p.New.Mix {
		case "beforeReviews":
			return PriorityNew
		case "mixed":
			return PriorityMixed
		}
	}
	return PriorityDue
}
