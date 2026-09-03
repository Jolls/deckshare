package apkg

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
)

// Export reads ONE deck's decks/note types/notes/cards row set, plus callerID's own scheduling
// state and review history on that deck's cards, into an IrCollection (db -> IR,
// architecture.md §4). Must be called inside a transaction it does not own; it only reads.
//
// Deck-scoped and caller-scoped, matching GET /decks/{id}/export (docs/routes.md, #140): a
// collaborator with can_view on someone else's shared deck exports that deck's content with
// THEIR OWN progress on it. Authorisation lives in export.sql's deck_access joins, not here --
// a caller without can_view gets pgx.ErrNoRows from GetDeckForExport and nothing else runs.
//
// Lossy in one direction by definition (apkg-format.md's Export section): a shared deck's other
// users' progress cannot fit in a single Anki collection. Media is never exported: #60 built the
// blob store for import only, so col.Media is always empty.
func Export(ctx context.Context, tx pgx.Tx, deckID, callerID pgtype.UUID, now time.Time) (*IrCollection, error) {
	q := db.New(tx)

	deck, err := q.GetDeckForExport(ctx, db.GetDeckForExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: reading deck for export: %w", err)
	}
	noteTypeRows, err := q.ListNoteTypesForDeckExport(ctx, db.ListNoteTypesForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing note types for export: %w", err)
	}
	fieldRows, err := q.ListFieldsForDeckExport(ctx, db.ListFieldsForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing fields for export: %w", err)
	}
	templateRows, err := q.ListTemplatesForDeckExport(ctx, db.ListTemplatesForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing templates for export: %w", err)
	}
	noteRows, err := q.ListNotesForDeckExport(ctx, db.ListNotesForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing notes for export: %w", err)
	}
	cardRows, err := q.ListCardsForDeckExport(ctx, db.ListCardsForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing cards for export: %w", err)
	}
	stateRows, err := q.ListUserCardStateForDeckExport(ctx, db.ListUserCardStateForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing scheduling state for export: %w", err)
	}
	reviewRows, err := q.ListReviewLogForDeckExport(ctx, db.ListReviewLogForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing review history for export: %w", err)
	}

	deckAnkiID := exportAnkiID(deck.AnkiID, deck.CreatedAt.Time.UnixMilli())
	decks := []IrDeck{{AnkiID: deckAnkiID, Name: deck.Name, Description: deck.Description}}

	noteTypeAnkiID := make(map[pgtype.UUID]int64, len(noteTypeRows))
	noteTypeIdx := make(map[pgtype.UUID]int, len(noteTypeRows))
	noteTypes := make([]IrNoteType, 0, len(noteTypeRows))
	for _, nt := range noteTypeRows {
		id := exportAnkiID(nt.AnkiID, uuidFallbackID(nt.ID))
		noteTypeAnkiID[nt.ID] = id
		noteTypeIdx[nt.ID] = len(noteTypes)
		noteTypes = append(noteTypes, IrNoteType{
			AnkiID: id, Name: nt.Name, CSS: nt.Css, IsCloze: nt.IsCloze, SortFieldIdx: nt.SortFieldIdx,
		})
	}
	for _, f := range fieldRows {
		i, ok := noteTypeIdx[f.NoteTypeID]
		if !ok {
			continue
		}
		noteTypes[i].Fields = append(noteTypes[i].Fields, IrField{
			Ordinal: f.Ordinal, Name: f.Name, Font: f.Font, Size: f.Size, IsRTL: f.IsRtl, Sticky: f.Sticky,
		})
	}
	for _, t := range templateRows {
		i, ok := noteTypeIdx[t.NoteTypeID]
		if !ok {
			continue
		}
		noteTypes[i].Templates = append(noteTypes[i].Templates, IrTemplate{
			Ordinal: t.Ordinal, Name: t.Name, Qfmt: t.Qfmt, Afmt: t.Afmt,
			BrowserQfmt: t.BrowserQfmt, BrowserAfmt: t.BrowserAfmt,
		})
	}

	noteAnkiID := make(map[pgtype.UUID]int64, len(noteRows))
	notes := make([]IrNote, 0, len(noteRows))
	for _, n := range noteRows {
		id := exportAnkiID(n.AnkiID, n.CreatedAt.Time.UnixMilli())
		noteAnkiID[n.ID] = id
		var fields []string
		if err := json.Unmarshal(n.Fields, &fields); err != nil {
			return nil, fmt.Errorf("apkg: decoding fields of note %q: %w", n.Guid, err)
		}
		notes = append(notes, IrNote{
			AnkiID: id, Guid: n.Guid, NoteTypeAnkiID: noteTypeAnkiID[n.NoteTypeID],
			Fields: fields, Tags: append([]string(nil), n.Tags...), Checksum: n.Checksum,
			Created: n.CreatedAt.Time.UTC(), Modified: n.ModifiedAt.Time.UTC(),
			// The package contains exactly one deck, so that is the only home deck a note can
			// have here. Notably NOT n.DeckID: a note whose cards span decks keeps its home deck
			// elsewhere, and dbwrite.go's importNotes SKIPS any note whose home deck does not
			// resolve in the package.
			HomeDeckAnkiID: deckAnkiID,
		})
	}

	stateByCard := make(map[pgtype.UUID]db.UserCardState, len(stateRows))
	for _, s := range stateRows {
		stateByCard[s.CardID] = s
	}

	cardAnkiID := make(map[pgtype.UUID]int64, len(cardRows))
	cards := make([]IrCard, 0, len(cardRows))
	var reviewDueAts []time.Time
	var position int32
	for _, c := range cardRows {
		id := exportAnkiID(c.AnkiID, uuidFallbackID(c.ID))
		cardAnkiID[c.ID] = id
		position++

		// A card imported with an Anki queue position (#82) exports with that same position,
		// round-tripping the order the user actually saw rather than re-deriving one from export
		// row order. Manually created cards (import_due_position NULL) keep the row-order fallback.
		cardPosition := position
		if c.ImportDuePosition.Valid {
			cardPosition = c.ImportDuePosition.Int32
		}

		state, hasState := stateByCard[c.ID]
		sched := deriveCardScheduling(state, hasState, now, cardPosition)
		if sched.Type == ankiTypeReview && sched.Due.Kind == DueAt {
			reviewDueAts = append(reviewDueAts, sched.Due.At)
		}

		cards = append(cards, IrCard{
			AnkiID: id, NoteAnkiID: noteAnkiID[c.NoteID], DeckAnkiID: deckAnkiID,
			Ordinal: c.Ordinal, Type: sched.Type, Queue: sched.Queue, Due: sched.Due,
			IntervalSeconds: sched.IntervalSeconds, Factor: ankiDefaultFactor,
			Reps: sched.Reps, Lapses: sched.Lapses, Flag: sched.Flag,
			Suspended: sched.Queue == ankiQueueSuspended,
			Buried:    sched.Queue == ankiQueueSchedBuried || sched.Queue == ankiQueueUserBuried,
			FSRS:      sched.FSRS,
		})
	}
	crt := deriveCrt(reviewDueAts, now)

	reviews := make([]IrReview, 0, len(reviewRows))
	for _, r := range reviewRows {
		cid, ok := cardAnkiID[r.CardID]
		if !ok {
			continue
		}
		id := exportAnkiID(r.AnkiID, r.ReviewedAt.Time.UnixMilli())
		reviews = append(reviews, IrReview{
			AnkiID: id, CardAnkiID: cid, ReviewedAt: r.ReviewedAt.Time.UTC(),
			Rating: r.Rating, IntervalSeconds: int64(r.ScheduledDaysAfter) * secondsPerDay,
			DurationMs: durationMsValue(r.DurationMs), Kind: r.ReviewKind,
		})
	}

	return &IrCollection{
		Crt: crt, SchemaVersion: 11,
		NoteTypes: noteTypes, Decks: decks, Notes: notes, Cards: cards, Reviews: reviews,
	}, nil
}

// cardScheduling is what deriveCardScheduling reconstructs from a (possibly absent)
// user_card_state row -- everything IrCard needs beyond identity and content linkage.
type cardScheduling struct {
	Queue           int32
	Type            int32
	Due             IrDue
	IntervalSeconds int64
	Reps            int32
	Lapses          int32
	Flag            int16
	FSRS            *IrFSRSState
}

// deriveCardScheduling reconstructs the Anki type/queue/due triple a card would need to write.
// DeckShare keeps no separate "queue" column -- FSRS state, suspended, and buried_until stand in for
// it, which loses Anki's five-way queue split (short-term learning vs. day-learning vs. preview
// all collapse to one epoch-seconds "learning" queue on export). That is the same "lossy in this
// direction by definition" apkg-format.md's Export section already documents for user_card_state
// generally, not a new gap.
//
// A card with no row at all is the common case dbwrite.go's seedCardStates leaves behind: new,
// unsuspended, unflagged, never reviewed.
//
// position is the new-card queue position to write into a never-seen card's Due: the caller
// resolves it to cards.import_due_position when the card carries one (#82), else an export-order
// counter.
func deriveCardScheduling(state db.UserCardState, hasState bool, now time.Time, position int32) cardScheduling {
	if !hasState {
		return cardScheduling{Queue: ankiQueueNew, Type: ankiTypeNew, Due: IrDue{Kind: DuePosition, Position: position}}
	}

	typ := int32(state.State) // go-fsrs's State enum and Anki's cards.type agree: 0 new, 1
	// learning, 2 review, 3 relearning (dbwrite.go's seedCardStates relies on the same identity).
	buried := state.BuriedUntil.Valid && state.BuriedUntil.Time.After(now)

	var queue int32
	switch {
	case state.Suspended:
		queue = ankiQueueSuspended
	case buried:
		queue = ankiQueueUserBuried
	case typ == ankiTypeNew:
		queue = ankiQueueNew
	case typ == ankiTypeReview:
		queue = ankiQueueReview
	default: // learning (1) or relearning (3)
		queue = ankiQueueLearning
	}

	due := IrDue{Kind: DueAt, At: state.Due.Time.UTC()}
	if typ == ankiTypeNew {
		due = IrDue{Kind: DuePosition, Position: position}
	}

	return cardScheduling{
		Queue: queue, Type: typ, Due: due,
		IntervalSeconds: int64(state.ScheduledDays) * secondsPerDay,
		Reps:            state.Reps, Lapses: state.Lapses, Flag: state.Flag,
		FSRS: &IrFSRSState{Stability: state.Stability, Difficulty: state.Difficulty},
	}
}

// deriveCrt picks a synthetic col.crt anchor. DeckShare persists no per-package creation instant
// (apkg-format.md's Export section), so this uses the earliest review-state due date being
// exported, floored to midnight UTC: every review-state card's day-since-crt offset comes out
// non-negative, and for cards that share one real original anchor (the common case -- they all
// came from one earlier import) the offsets are exact, because their due dates already differ
// from each other by whole days.
func deriveCrt(reviewDueAts []time.Time, fallback time.Time) time.Time {
	earliest := fallback
	if len(reviewDueAts) > 0 {
		earliest = reviewDueAts[0]
		for _, t := range reviewDueAts[1:] {
			if t.Before(earliest) {
				earliest = t
			}
		}
	}
	return time.Date(earliest.Year(), earliest.Month(), earliest.Day(), 0, 0, 0, 0, time.UTC)
}

func durationMsValue(v pgtype.Int4) int32 {
	if !v.Valid {
		return 0
	}
	return v.Int32
}

// exportAnkiID reuses a row's original Anki id when it has one (export fidelity, CLAUDE.md's
// "anki_id... export fidelity only, never a key") and synthesises one otherwise.
func exportAnkiID(existing pgtype.Int8, fallback int64) int64 {
	if existing.Valid {
		return existing.Int64
	}
	return fallback
}

// uuidFallbackID derives a stable positive int64 from a row's own id for tables (note_types,
// cards) that carry no timestamp column an Anki id could otherwise be drawn from. Collisions
// within one table would require two different UUIDs sharing 8 leading bytes -- not a case worth
// guarding against for a single owner's export.
func uuidFallbackID(id pgtype.UUID) int64 {
	v := int64(binary.BigEndian.Uint64(id.Bytes[:8]))
	if v < 0 {
		v = -v
	}
	if v == 0 {
		v = 1
	}
	return v
}
