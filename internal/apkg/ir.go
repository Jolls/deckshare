package apkg

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// IrCollection is one package, fully normalised. Both schema readers converge on this before any
// database code runs (architecture.md §4).
type IrCollection struct {
	Crt           time.Time // col.crt, UTC. An opaque anchor: used verbatim, nothing added.
	SchemaVersion int       // 11 or 18, from table presence -- never from col.ver
	NoteTypes     []IrNoteType
	Decks         []IrDeck
	Notes         []IrNote
	Cards         []IrCard
	Reviews       []IrReview
	Media         []IrMedia
	Warnings      []string // non-fatal findings, surfaced to the importing user
}

type IrNoteType struct {
	AnkiID       int64
	Name         string
	CSS          string
	IsCloze      bool
	SortFieldIdx int32
	Fields       []IrField    // sorted by Ordinal; ord is authoritative, array order is not
	Templates    []IrTemplate // sorted by Ordinal, same rule
}

type IrField struct {
	Ordinal int32
	Name    string
	Font    string
	Size    int32
	IsRTL   bool
	Sticky  bool
}

type IrTemplate struct {
	Ordinal     int32
	Name        string
	Qfmt        string
	Afmt        string
	BrowserQfmt string
	BrowserAfmt string
}

type IrDeck struct {
	AnkiID      int64
	Name        string // full path, "::"-separated in BOTH schemas -- schema 18's \x1f is normalised
	Description string
	IsFiltered  bool // dyn != 0. We have no filtered decks; cards are filed under their home deck
}

type IrNote struct {
	AnkiID         int64
	Guid           string
	NoteTypeAnkiID int64
	Fields         []string  // split on \x1f, indexed by IrField.Ordinal
	Tags           []string  // space-surrounded source string, empties dropped
	Checksum       int64     // notes.csum
	Created        time.Time // notes.id, epoch MILLISECONDS
	Modified       time.Time // notes.mod, epoch SECONDS
	// HomeDeckAnkiID is the resolved home deck of this note's LOWEST-numbered card
	// (architecture.md §20). It fills notes.deck_id: where the note was first filed, where the
	// notes list shows it, and the default for cards generated later. It is NOT what any card's
	// deck_id comes from -- that is IrCard.DeckAnkiID, per card.
	HomeDeckAnkiID int64
}

type IrDueKind uint8

const (
	DueNone     IrDueKind = iota // no meaningful due value
	DuePosition                  // new-card queue position; no calendar meaning
	DueAt                        // an absolute instant
)

// IrDue is cards.due after the three-way disambiguation in apkg-format.md, resolved from odue
// when the card sits in a filtered deck. The discriminator is `queue`, not `type`.
type IrDue struct {
	Kind     IrDueKind
	Position int32
	At       time.Time // UTC; valid only when Kind == DueAt
}

type IrCard struct {
	AnkiID     int64
	NoteAnkiID int64
	// DeckAnkiID is the card's real HOME deck: odid when odid != 0, else did. This is what
	// cards.deck_id is filed from (architecture.md §20's resolution -- the whole point of #58's
	// second bullet).
	DeckAnkiID         int64
	FilteredDeckAnkiID int64 // did when odid != 0, else 0. Carried for export fidelity only
	Ordinal            int32 // template ordinal, or cloze ordinal on a cloze note type
	Type               int32 // Anki cards.type, verbatim
	Queue              int32 // Anki cards.queue, verbatim
	Due                IrDue
	IntervalSeconds    int64 // cards.ivl normalised: days*86400 when positive, |ivl| when negative
	Factor             int32 // SM-2 ease x1000. NEVER mapped to FSRS difficulty (apkg-format.md)
	Reps               int32
	Lapses             int32
	Flag               int16 // cards.flags & 0x7
	Suspended          bool  // queue == -1
	Buried             bool  // queue == -2 or -3
	FSRS               *IrFSRSState // nil when cards.data carries no usable FSRS state
}

type IrFSRSState struct {
	Stability        float64
	Difficulty       float64
	DesiredRetention float64 // 0 when absent
	Position         int32   // cards.data "pos", the preserved new-card position; 0 when absent
}

type IrReview struct {
	AnkiID              int64     // revlog.id, epoch MILLISECONDS -- also the review instant
	CardAnkiID          int64
	ReviewedAt          time.Time // UTC, from AnkiID
	Rating              int16     // revlog.ease, 1..4. ease == 0 is a manual reschedule, dropped
	IntervalSeconds     int64     // revlog.ivl, same sign convention as cards.ivl
	LastIntervalSeconds int64     // revlog.lastIvl, same convention
	Factor              int32
	DurationMs          int32 // revlog.time
	Kind                int16 // revlog.type: 0 learning, 1 review, 2 relearning, 3 cram, 4 manual
}

type IrMedia struct {
	Index     string // the zip member name -- the numeric index the media index keyed it by
	Filename  string // NFC-normalised
	SHA256    string // lowercase hex
	SizeBytes int64
	Data      []byte
}

// intervalSeconds normalises Anki's dual-unit interval: days when positive, seconds when
// negative (apkg-format.md). Applies to cards.ivl, revlog.ivl and revlog.lastIvl alike -- the IR
// carries seconds throughout so the distinction cannot reappear downstream.
func intervalSeconds(ivl int64) int64 {
	if ivl < 0 {
		return -ivl
	}
	return ivl * 86400
}

// splitFields splits notes.flds on \x1f (unit separator).
func splitFields(flds string) []string {
	return strings.Split(flds, "\x1f")
}

// splitTags splits notes.tags, which is space-separated AND space-surrounded, dropping empties.
func splitTags(tags string) []string {
	fields := strings.Fields(tags)
	out := make([]string, 0, len(fields))
	out = append(out, fields...)
	return out
}

// normaliseDeckName converts schema 18's \x1f hierarchy separator to schema 11's "::".
// Idempotent, so it is safe to call on both readers' output.
func normaliseDeckName(name string) string {
	return strings.ReplaceAll(name, "\x1f", "::")
}

// resolveDue applies apkg-format.md's queue/odid/odue table.
//
// 1. If odid != 0, the value to interpret is odue, not due. Otherwise it is due.
// 2. Switch on queue, not type:
//   - 0 (new) -> DuePosition.
//   - 1 (learning), 4 (preview) -> DueAt, epoch seconds.
//   - 2 (review), 3 (day-learning) -> DueAt, days since crt. users.day_start_hour is not applied.
//   - negative (-1, -2, -3, held) -> the discriminating queue is gone. If type == 0, DuePosition.
//     Else if v >= epochSecondsThreshold, epoch seconds; else days since crt.
//   - anything else -> DueNone.
func resolveDue(queue, typ int32, due, odue, odid int64, crt time.Time) IrDue {
	v := due
	if odid != 0 {
		v = odue
	}
	switch queue {
	case ankiQueueNew:
		return IrDue{Kind: DuePosition, Position: int32(v)}
	case ankiQueueLearning, ankiQueuePreview:
		return IrDue{Kind: DueAt, At: time.Unix(v, 0).UTC()}
	case ankiQueueReview, ankiQueueDayLearning:
		return IrDue{Kind: DueAt, At: crt.Add(time.Duration(v) * 24 * time.Hour)}
	case ankiQueueSuspended, ankiQueueSchedBuried, ankiQueueUserBuried:
		if typ == ankiTypeNew {
			return IrDue{Kind: DuePosition, Position: int32(v)}
		}
		if v >= epochSecondsThreshold {
			return IrDue{Kind: DueAt, At: time.Unix(v, 0).UTC()}
		}
		return IrDue{Kind: DueAt, At: crt.Add(time.Duration(v) * 24 * time.Hour)}
	default:
		return IrDue{Kind: DueNone}
	}
}

// resolveHomeDecks fills every IrNote.HomeDeckAnkiID from the note's lowest-AnkiID card's
// DeckAnkiID (architecture.md §20). Notes with no cards keep 0 and are reported as a warning.
func resolveHomeDecks(notes []IrNote, cards []IrCard) []string {
	lowestCard := make(map[int64]IrCard, len(notes))
	for _, c := range cards {
		existing, ok := lowestCard[c.NoteAnkiID]
		if !ok || c.AnkiID < existing.AnkiID {
			lowestCard[c.NoteAnkiID] = c
		}
	}
	var warnings []string
	for i := range notes {
		c, ok := lowestCard[notes[i].AnkiID]
		if !ok {
			warnings = append(warnings, "note "+guidOrAnki(notes[i])+" has no cards; skipped")
			continue
		}
		notes[i].HomeDeckAnkiID = c.DeckAnkiID
	}
	return warnings
}

func guidOrAnki(n IrNote) string {
	if n.Guid != "" {
		return n.Guid
	}
	return "anki_id " + strconv.FormatInt(n.AnkiID, 10)
}

// sortFieldsByOrdinal sorts in place by Ordinal -- ord is authoritative, array order is not.
func sortFieldsByOrdinal(fields []IrField) {
	sort.Slice(fields, func(i, j int) bool { return fields[i].Ordinal < fields[j].Ordinal })
}

// sortTemplatesByOrdinal sorts in place by Ordinal -- ord is authoritative, array order is not.
func sortTemplatesByOrdinal(templates []IrTemplate) {
	sort.Slice(templates, func(i, j int) bool { return templates[i].Ordinal < templates[j].Ordinal })
}
