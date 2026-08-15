package apkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/media"
	"github.com/Jolls/enshu/internal/review"
)

// ImportResult is the per-import tally the import UI (#62) reports.
type ImportResult struct {
	DecksCreated       int
	DecksReused        int
	NoteTypesCreated   int
	NoteTypesReused    int
	NotesInserted      int
	NotesUpdated       int
	CardsUpserted      int
	ReviewsInserted    int
	CardStatesSeeded   int
	CardStatesReplayed int
	MediaImported      int
	Warnings           []string
}

// Import files col into the database under ownerID, writing media bytes into blobs. Must be
// called inside a transaction it does not own; the caller commits. Idempotent: re-importing the
// same package updates rather than duplicates (CLAUDE.md §2.2).
func Import(ctx context.Context, tx pgx.Tx, ownerID pgtype.UUID, col *IrCollection, now time.Time, blobs *media.Store) (ImportResult, error) {
	q := db.New(tx)
	result := ImportResult{Warnings: append([]string(nil), col.Warnings...)}

	deckByAnkiID, err := importDecks(ctx, tx, q, ownerID, col.Decks, &result)
	if err != nil {
		return ImportResult{}, err
	}

	noteTypeByAnkiID, templatesByNoteType, isClozeByNoteType, err := importNoteTypes(ctx, q, ownerID, col.NoteTypes, &result)
	if err != nil {
		return ImportResult{}, err
	}

	noteByAnkiID, err := importNotes(ctx, q, ownerID, col.Notes, deckByAnkiID, noteTypeByAnkiID, &result)
	if err != nil {
		return ImportResult{}, err
	}

	cardByAnkiID, err := importCards(ctx, q, col.Cards, noteByAnkiID, noteTypeByAnkiID, templatesByNoteType, isClozeByNoteType, deckByAnkiID, col.Notes, &result)
	if err != nil {
		return ImportResult{}, err
	}

	lockIDs := make([]pgtype.UUID, 0, len(cardByAnkiID))
	for _, id := range cardByAnkiID {
		lockIDs = append(lockIDs, id)
	}
	if len(lockIDs) > 0 {
		if err := review.LockCards(ctx, q, ownerID, lockIDs); err != nil {
			return ImportResult{}, err
		}
	}

	cardHasReviews, err := importReviews(ctx, tx, q, ownerID, col.Reviews, cardByAnkiID, &result)
	if err != nil {
		return ImportResult{}, err
	}

	if err := seedCardStates(ctx, q, ownerID, col.Cards, cardByAnkiID, cardHasReviews, now, &result); err != nil {
		return ImportResult{}, err
	}

	deckIDs := make([]pgtype.UUID, 0, len(deckByAnkiID))
	for _, id := range deckByAnkiID {
		deckIDs = append(deckIDs, id)
	}
	if err := importMedia(ctx, q, blobs, deckIDs, col.Media, &result); err != nil {
		return ImportResult{}, err
	}

	return result, nil
}

// importMedia writes each media file's bytes into blobs under its sha256, records the blob's
// metadata, and refs it from every deck this import touched. The package format does not
// attribute individual media files to individual decks -- Anki's own exporter only ever bundles
// the media actually referenced by the notes it exports, so crediting every deck in the import is
// the closest available approximation without parsing filename references out of note HTML.
func importMedia(ctx context.Context, q *db.Queries, blobs *media.Store, deckIDs []pgtype.UUID, mediaFiles []IrMedia, result *ImportResult) error {
	for _, m := range mediaFiles {
		if err := blobs.Put(m.SHA256, m.Data); err != nil {
			return fmt.Errorf("apkg: writing media blob %q (%s): %w", m.Filename, m.SHA256, err)
		}
		if err := q.CreateMediaBlob(ctx, db.CreateMediaBlobParams{
			Sha256:    m.SHA256,
			SizeBytes: m.SizeBytes,
			Mime:      http.DetectContentType(m.Data),
		}); err != nil {
			return fmt.Errorf("apkg: recording media blob %q (%s): %w", m.Filename, m.SHA256, err)
		}
		for _, deckID := range deckIDs {
			if err := q.CreateMediaRef(ctx, db.CreateMediaRefParams{
				DeckID:   deckID,
				Filename: m.Filename,
				Sha256:   m.SHA256,
			}); err != nil {
				return fmt.Errorf("apkg: recording media ref %q (%s): %w", m.Filename, m.SHA256, err)
			}
		}
		result.MediaImported++
	}
	return nil
}

// importDecks creates or reuses each deck by (owner, name). A filtered deck is never created --
// no card is ever filed in one (IrCard.DeckAnkiID is always the home deck).
func importDecks(ctx context.Context, tx pgx.Tx, q *db.Queries, ownerID pgtype.UUID, decks []IrDeck, result *ImportResult) (map[int64]pgtype.UUID, error) {
	deckByAnkiID := make(map[int64]pgtype.UUID, len(decks))
	for _, d := range decks {
		if d.IsFiltered {
			result.Warnings = append(result.Warnings, fmt.Sprintf("deck %q is a filtered deck and was not created; its cards are filed under their home deck", d.Name))
			continue
		}

		ankiID := pgtype.Int8{Int64: d.AnkiID, Valid: true}

		existing, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: d.Name})
		if errors.Is(err, pgx.ErrNoRows) {
			created, err := db.CreateDeckWithAccess(ctx, tx, ownerID, d.Name, d.Description)
			if err != nil {
				return nil, fmt.Errorf("apkg: creating deck %q: %w", d.Name, err)
			}
			if _, err := q.SetDeckAnkiID(ctx, db.SetDeckAnkiIDParams{DeckID: created.ID, AnkiID: ankiID}); err != nil {
				return nil, fmt.Errorf("apkg: setting anki_id on deck %q: %w", d.Name, err)
			}
			deckByAnkiID[d.AnkiID] = created.ID
			result.DecksCreated++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("apkg: looking up deck %q: %w", d.Name, err)
		}

		if _, err := q.GetDeckForContentEdit(ctx, db.GetDeckForContentEditParams{UserID: ownerID, DeckID: existing.ID}); err != nil {
			return nil, fmt.Errorf("apkg: owner lacks edit access to their own deck %q: %w", d.Name, err)
		}
		if _, err := q.SetDeckAnkiID(ctx, db.SetDeckAnkiIDParams{DeckID: existing.ID, AnkiID: ankiID}); err != nil {
			return nil, fmt.Errorf("apkg: setting anki_id on deck %q: %w", d.Name, err)
		}
		deckByAnkiID[d.AnkiID] = existing.ID
		result.DecksReused++
	}
	return deckByAnkiID, nil
}

// importNoteTypes creates or reuses each note type by (owner, name). Reuse requires the same
// field count -- notes.fields is a positional array indexed by fields.ordinal, so importing into
// a note type of a different width renders every field into the wrong slot.
func importNoteTypes(ctx context.Context, q *db.Queries, ownerID pgtype.UUID, noteTypes []IrNoteType, result *ImportResult) (map[int64]pgtype.UUID, map[int64]map[int32]pgtype.UUID, map[int64]bool, error) {
	noteTypeByAnkiID := make(map[int64]pgtype.UUID, len(noteTypes))
	templatesByNoteType := make(map[int64]map[int32]pgtype.UUID, len(noteTypes))
	isClozeByNoteType := make(map[int64]bool, len(noteTypes))

	for _, nt := range noteTypes {
		ankiID := pgtype.Int8{Int64: nt.AnkiID, Valid: true}

		existing, err := q.GetNoteTypeByOwnerAndName(ctx, db.GetNoteTypeByOwnerAndNameParams{OwnerID: ownerID, Name: nt.Name})
		if errors.Is(err, pgx.ErrNoRows) {
			created, err := q.CreateImportedNoteType(ctx, db.CreateImportedNoteTypeParams{
				OwnerID:      ownerID,
				Name:         nt.Name,
				Css:          nt.CSS,
				IsCloze:      nt.IsCloze,
				SortFieldIdx: nt.SortFieldIdx,
				AnkiID:       ankiID,
			})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("apkg: creating note type %q: %w", nt.Name, err)
			}
			for _, f := range nt.Fields {
				if _, err := q.CreateImportedField(ctx, db.CreateImportedFieldParams{
					NoteTypeID: created.ID,
					Ordinal:    f.Ordinal,
					Name:       f.Name,
					Font:       f.Font,
					Size:       f.Size,
					IsRtl:      f.IsRTL,
					Sticky:     f.Sticky,
				}); err != nil {
					return nil, nil, nil, fmt.Errorf("apkg: creating field %q of note type %q: %w", f.Name, nt.Name, err)
				}
			}
			templates := make(map[int32]pgtype.UUID, len(nt.Templates))
			for _, t := range nt.Templates {
				createdT, err := q.CreateImportedTemplate(ctx, db.CreateImportedTemplateParams{
					NoteTypeID:  created.ID,
					Ordinal:     t.Ordinal,
					Name:        t.Name,
					Qfmt:        t.Qfmt,
					Afmt:        t.Afmt,
					BrowserQfmt: t.BrowserQfmt,
					BrowserAfmt: t.BrowserAfmt,
				})
				if err != nil {
					return nil, nil, nil, fmt.Errorf("apkg: creating template %q of note type %q: %w", t.Name, nt.Name, err)
				}
				templates[t.Ordinal] = createdT.ID
			}
			noteTypeByAnkiID[nt.AnkiID] = created.ID
			templatesByNoteType[nt.AnkiID] = templates
			isClozeByNoteType[nt.AnkiID] = nt.IsCloze
			result.NoteTypesCreated++
			continue
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("apkg: looking up note type %q: %w", nt.Name, err)
		}

		existingFields, err := q.ListFieldsForNoteType(ctx, existing.ID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("apkg: listing fields of note type %q: %w", nt.Name, err)
		}
		if len(existingFields) != len(nt.Fields) {
			return nil, nil, nil, fmt.Errorf("apkg: note type %q: %w", nt.Name, ErrNoteTypeMismatch)
		}
		existingTemplates, err := q.ListTemplatesForNoteType(ctx, existing.ID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("apkg: listing templates of note type %q: %w", nt.Name, err)
		}
		templates := make(map[int32]pgtype.UUID, len(existingTemplates))
		for _, t := range existingTemplates {
			templates[t.Ordinal] = t.ID
		}
		noteTypeByAnkiID[nt.AnkiID] = existing.ID
		templatesByNoteType[nt.AnkiID] = templates
		isClozeByNoteType[nt.AnkiID] = existing.IsCloze
		result.NoteTypesReused++
	}
	return noteTypeByAnkiID, templatesByNoteType, isClozeByNoteType, nil
}

// importNotes creates or updates each note by (owner, guid). A re-import never moves deck_id --
// a note the user has since filed elsewhere must stay there.
func importNotes(ctx context.Context, q *db.Queries, ownerID pgtype.UUID, notes []IrNote, deckByAnkiID map[int64]pgtype.UUID, noteTypeByAnkiID map[int64]pgtype.UUID, result *ImportResult) (map[int64]pgtype.UUID, error) {
	noteByAnkiID := make(map[int64]pgtype.UUID, len(notes))
	for _, n := range notes {
		noteTypeID, ok := noteTypeByAnkiID[n.NoteTypeAnkiID]
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("note %q: note type did not resolve; skipped", n.Guid))
			continue
		}
		deckID, ok := deckByAnkiID[n.HomeDeckAnkiID]
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("note %q: home deck did not resolve; skipped", n.Guid))
			continue
		}

		fieldsJSON, err := json.Marshal(n.Fields)
		if err != nil {
			return nil, fmt.Errorf("apkg: marshalling fields of note %q: %w", n.Guid, err)
		}
		ankiID := pgtype.Int8{Int64: n.AnkiID, Valid: true}

		existing, err := q.GetNoteByOwnerAndGuid(ctx, db.GetNoteByOwnerAndGuidParams{OwnerID: ownerID, Guid: n.Guid})
		if errors.Is(err, pgx.ErrNoRows) {
			created, err := q.CreateImportedNote(ctx, db.CreateImportedNoteParams{
				Guid:       n.Guid,
				UserID:     ownerID,
				NoteTypeID: noteTypeID,
				DeckID:     deckID,
				Fields:     fieldsJSON,
				Tags:       n.Tags,
				Checksum:   n.Checksum,
				CreatedAt:  pgtype.Timestamptz{Time: n.Created, Valid: true},
				ModifiedAt: pgtype.Timestamptz{Time: n.Modified, Valid: true},
				AnkiID:     ankiID,
			})
			if err != nil {
				return nil, fmt.Errorf("apkg: creating note %q: %w", n.Guid, err)
			}
			noteByAnkiID[n.AnkiID] = created.ID
			result.NotesInserted++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("apkg: looking up note %q: %w", n.Guid, err)
		}

		if _, err := q.UpdateImportedNote(ctx, db.UpdateImportedNoteParams{
			Fields:     fieldsJSON,
			Tags:       n.Tags,
			Checksum:   n.Checksum,
			NoteTypeID: noteTypeID,
			ModifiedAt: pgtype.Timestamptz{Time: n.Modified, Valid: true},
			AnkiID:     ankiID,
			NoteID:     existing.ID,
			UserID:     ownerID,
		}); err != nil {
			return nil, fmt.Errorf("apkg: updating note %q: %w", n.Guid, err)
		}
		noteByAnkiID[n.AnkiID] = existing.ID
		result.NotesUpdated++
	}
	return noteByAnkiID, nil
}

// importCards upserts each card, filing deck_id from the card's OWN home deck (architecture.md
// §20), never the note's. ON CONFLICT (note_id, ordinal) keeps the existing card id and, with it,
// its user_card_state and review_log history.
func importCards(ctx context.Context, q *db.Queries, cards []IrCard, noteByAnkiID map[int64]pgtype.UUID, noteTypeByAnkiID map[int64]pgtype.UUID, templatesByNoteType map[int64]map[int32]pgtype.UUID, isClozeByNoteType map[int64]bool, deckByAnkiID map[int64]pgtype.UUID, notes []IrNote, result *ImportResult) (map[int64]pgtype.UUID, error) {
	noteTypeAnkiIDByNoteAnkiID := make(map[int64]int64, len(notes))
	homeDeckByNoteAnkiID := make(map[int64]int64, len(notes))
	for _, n := range notes {
		noteTypeAnkiIDByNoteAnkiID[n.AnkiID] = n.NoteTypeAnkiID
		homeDeckByNoteAnkiID[n.AnkiID] = n.HomeDeckAnkiID
	}

	cardByAnkiID := make(map[int64]pgtype.UUID, len(cards))
	for _, c := range cards {
		noteID, ok := noteByAnkiID[c.NoteAnkiID]
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("card (anki_id %d): note did not resolve; skipped", c.AnkiID))
			continue
		}
		noteTypeAnkiID := noteTypeAnkiIDByNoteAnkiID[c.NoteAnkiID]
		templates := templatesByNoteType[noteTypeAnkiID]

		var templateID pgtype.UUID
		if isClozeByNoteType[noteTypeAnkiID] {
			id, ok := templates[0]
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("card (anki_id %d): cloze note type has no template 0; skipped", c.AnkiID))
				continue
			}
			templateID = id
		} else {
			id, ok := templates[c.Ordinal]
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("card (anki_id %d): no template at ordinal %d; skipped", c.AnkiID, c.Ordinal))
				continue
			}
			templateID = id
		}

		deckID, ok := deckByAnkiID[c.DeckAnkiID]
		if !ok {
			// Fall back to the note's home deck only when the card's own deck id does not
			// resolve (a package referencing a deck it does not define).
			deckID, ok = deckByAnkiID[homeDeckByNoteAnkiID[c.NoteAnkiID]]
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("card (anki_id %d): neither its own deck nor its note's home deck resolved; skipped", c.AnkiID))
				continue
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("card (anki_id %d): its own deck (anki_id %d) did not resolve; filed under its note's home deck instead", c.AnkiID, c.DeckAnkiID))
		}

		created, err := q.UpsertImportedCard(ctx, db.UpsertImportedCardParams{
			NoteID:     noteID,
			TemplateID: templateID,
			Ordinal:    c.Ordinal,
			DeckID:     deckID,
			AnkiID:     pgtype.Int8{Int64: c.AnkiID, Valid: true},
		})
		if err != nil {
			return nil, fmt.Errorf("apkg: upserting card (anki_id %d): %w", c.AnkiID, err)
		}
		cardByAnkiID[c.AnkiID] = created.ID
		result.CardsUpserted++
	}
	return cardByAnkiID, nil
}

// reviewKindToState maps revlog.type onto review_log.state_before: 0 learning -> Learning,
// 1 review -> Review, 2 relearning -> Relearning, 3 cram -> Review (closest existing state;
// review_kind itself carries 3 verbatim, per docs/plans/58-apkg-import.md §10.4).
func reviewKindToState(kind int16) int16 {
	switch kind {
	case 0:
		return int16(reviewStateLearning)
	case 2:
		return int16(reviewStateRelearning)
	default: // 1 (review), 3 (cram)
		return int16(reviewStateReview)
	}
}

const (
	reviewStateLearning   = 1
	reviewStateReview     = 2
	reviewStateRelearning = 3
)

// importReviews inserts each review_log row and replays the affected card's history through the
// scheduler -- apkg-format.md's preferred warm-start over seeding from a snapshot. Returns which
// cards received at least one review, so seedCardStates knows which ones to skip.
func importReviews(ctx context.Context, tx pgx.Tx, q *db.Queries, ownerID pgtype.UUID, reviews []IrReview, cardByAnkiID map[int64]pgtype.UUID, result *ImportResult) (map[int64]bool, error) {
	byCard := make(map[int64][]IrReview, len(reviews))
	for _, r := range reviews {
		byCard[r.CardAnkiID] = append(byCard[r.CardAnkiID], r)
	}

	cardHasReviews := make(map[int64]bool, len(byCard))

	for cardAnkiID, rs := range byCard {
		cardID, ok := cardByAnkiID[cardAnkiID]
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("review log for card (anki_id %d): card did not resolve; skipped", cardAnkiID))
			continue
		}

		for _, r := range rs {
			n, err := q.InsertImportedReviewLog(ctx, db.InsertImportedReviewLogParams{
				UserID:              ownerID,
				CardID:              cardID,
				Rating:              r.Rating,
				ReviewedAt:          pgtype.Timestamptz{Time: r.ReviewedAt, Valid: true},
				DurationMs:          durationMsParam(r.DurationMs),
				StateBefore:         reviewKindToState(r.Kind),
				LearningStepsBefore: 0,
				ElapsedDaysBefore:   0,
				ScheduledDaysAfter:  maxInt32(0, int32(r.IntervalSeconds/86400)),
				ReviewKind:          r.Kind,
				AnkiID:              pgtype.Int8{Int64: r.AnkiID, Valid: true},
			})
			if err != nil {
				return nil, fmt.Errorf("apkg: inserting review log row (anki_id %d): %w", r.AnkiID, err)
			}
			result.ReviewsInserted += int(n)
		}

		cardRow, err := q.GetCard(ctx, cardID)
		if err != nil {
			return nil, fmt.Errorf("apkg: reading deck of card (anki_id %d): %w", cardAnkiID, err)
		}
		params, err := review.EffectiveParams(ctx, q, ownerID, cardRow.DeckID)
		if err != nil {
			return nil, fmt.Errorf("apkg: resolving fsrs params for card (anki_id %d): %w", cardAnkiID, err)
		}
		if _, err := review.ReplayCard(ctx, tx, params, ownerID, cardID); err != nil {
			return nil, fmt.Errorf("apkg: replaying review history for card (anki_id %d): %w", cardAnkiID, err)
		}
		result.CardStatesReplayed++
		cardHasReviews[cardAnkiID] = true
	}
	return cardHasReviews, nil
}

func durationMsParam(ms int32) pgtype.Int4 {
	if ms <= 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: ms, Valid: true}
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

// seedCardStates writes user_card_state for a card that carries scheduling state but has zero
// imported reviews. ir.Factor is never written anywhere -- SM-2 ease is meaningless under FSRS.
func seedCardStates(ctx context.Context, q *db.Queries, ownerID pgtype.UUID, cards []IrCard, cardByAnkiID map[int64]pgtype.UUID, cardHasReviews map[int64]bool, now time.Time, result *ImportResult) error {
	for _, c := range cards {
		if cardHasReviews[c.AnkiID] {
			continue
		}
		cardID, ok := cardByAnkiID[c.AnkiID]
		if !ok {
			continue
		}
		hasState := c.FSRS != nil || c.Type != ankiTypeNew || c.Suspended || c.Flag != 0
		if !hasState {
			continue
		}

		due := now
		var lastReview pgtype.Timestamptz
		if c.Due.Kind == DueAt {
			due = c.Due.At
			if c.IntervalSeconds > 0 {
				lastReview = pgtype.Timestamptz{Time: due.Add(-time.Duration(c.IntervalSeconds) * time.Second), Valid: true}
			}
		}

		var stability, difficulty float64
		if c.FSRS != nil {
			stability = c.FSRS.Stability
			difficulty = c.FSRS.Difficulty
		}

		n, err := q.SeedImportedUserCardState(ctx, db.SeedImportedUserCardStateParams{
			UserID:        ownerID,
			CardID:        cardID,
			Due:           pgtype.Timestamptz{Time: due, Valid: true},
			Stability:     stability,
			Difficulty:    difficulty,
			State:         int16(c.Type),
			Reps:          c.Reps,
			Lapses:        c.Lapses,
			ElapsedDays:   0,
			ScheduledDays: maxInt32(0, int32(c.IntervalSeconds/86400)),
			LearningSteps: 0,
			LastReview:    lastReview,
			Suspended:     c.Suspended,
			Flag:          c.Flag,
		})
		if err != nil {
			return fmt.Errorf("apkg: seeding user_card_state for card (anki_id %d): %w", c.AnkiID, err)
		}
		result.CardStatesSeeded += int(n)
	}
	return nil
}
