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

// NewMix is the per-deck new/review interleaving (#116, decks.preset "new.mix"), one of Anki's
// own new-review-order dconf options.
type NewMix string

const (
	NewMixAfterReviews  NewMix = "afterReviews"
	NewMixBeforeReviews NewMix = "beforeReviews"
	NewMixMixed         NewMix = "mixed"
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

// Valid reports whether m is one of the three recognised new.mix values, the NewMix counterpart
// to RevOrder.Valid.
func (m NewMix) Valid() bool {
	switch m {
	case NewMixAfterReviews, NewMixBeforeReviews, NewMixMixed:
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

// ParseNewMix reads decks.preset. Absent, malformed, or unrecognised -> NewMixAfterReviews, the
// existing default (reviews before new cards).
func ParseNewMix(preset []byte) NewMix {
	p, ok := parseDeckPreset(preset)
	if !ok || p.New == nil || p.New.Mix == nil {
		return NewMixAfterReviews
	}
	switch NewMix(*p.New.Mix) {
	case NewMixAfterReviews, NewMixBeforeReviews, NewMixMixed:
		return NewMix(*p.New.Mix)
	default:
		return NewMixAfterReviews
	}
}
