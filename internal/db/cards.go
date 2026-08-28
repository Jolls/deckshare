package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrNoCards is returned when a sync would leave a note with zero cards -- a cloze note whose
// last {{cN::}} marker was removed. Callers answer 400 and roll back: a card-less note breaks
// the post-condition DeleteDeck depends on (docs/schema.md).
var ErrNoCards = errors.New("db: note would be left with no cards")

// DesiredCard is one (ordinal, template) pair the note should have a card for.
type DesiredCard struct {
	Ordinal    int32
	TemplateID pgtype.UUID
}

// SyncNoteCards reconciles a note's cards to desired by DIFFING ordinals: cards whose ordinal
// survives are left completely untouched (other than a possible template_id repoint, below), so
// their id -- and therefore their user_card_state and review_log history -- survives the edit.
// Dropping and recreating a note's cards instead is the data-loss trap docs/schema.md names and
// #51 §0.4 made live.
//
// A surviving ordinal whose desired template differs from what the card currently points at has
// its template_id repointed in place (#138): this is what lets a note-type change reuse this same
// diff instead of a bespoke path. It's a no-op for every other caller -- a same-note-type field
// edit always desires the same template at a surviving ordinal, since desiredCards derives it
// from the note's own note type -- and it matters because the study batch query reads qfmt/afmt
// from cards.template_id, not from notes.note_type_id, so a stale template_id would render the
// old note type's template while everything else reports the new one.
//
// Must be called inside a transaction it does not own; the caller commits. tx must already hold
// a lock on the note row (e.g. via LockNoteForContentEdit) so the read below is race-free
// against a concurrent edit of the same note.
func SyncNoteCards(ctx context.Context, tx pgx.Tx, noteID, homeDeckID pgtype.UUID, desired []DesiredCard) error {
	q := New(tx)

	if len(desired) == 0 {
		return ErrNoCards
	}

	existing, err := q.ListCardsForNoteForUpdate(ctx, noteID)
	if err != nil {
		return err
	}

	existingByOrdinal := make(map[int32]ListCardsForNoteForUpdateRow, len(existing))
	for _, c := range existing {
		existingByOrdinal[c.Ordinal] = c
	}
	desiredByOrdinal := make(map[int32]DesiredCard, len(desired))
	for _, d := range desired {
		desiredByOrdinal[d.Ordinal] = d
	}

	// Create before destroy, so the note is never transiently card-less.
	for ord, d := range desiredByOrdinal {
		if _, ok := existingByOrdinal[ord]; ok {
			continue
		}
		if _, err := q.CreateCard(ctx, CreateCardParams{
			NoteID:     noteID,
			TemplateID: d.TemplateID,
			Ordinal:    ord,
			DeckID:     homeDeckID,
		}); err != nil {
			return err
		}
	}

	var destroy []int32
	for _, c := range existing {
		if _, ok := desiredByOrdinal[c.Ordinal]; !ok {
			destroy = append(destroy, c.Ordinal)
		}
	}
	if len(destroy) > 0 {
		if _, err := q.DeleteCardsByOrdinals(ctx, DeleteCardsByOrdinalsParams{
			NoteID:   noteID,
			Ordinals: destroy,
		}); err != nil {
			return err
		}
	}

	// Repoint template_id on any surviving ordinal whose desired template differs from its
	// current one -- the new step a note-type change needs (see doc comment above).
	for ord, c := range existingByOrdinal {
		d, ok := desiredByOrdinal[ord]
		if !ok || d.TemplateID == c.TemplateID {
			continue
		}
		if _, err := q.UpdateCardTemplate(ctx, UpdateCardTemplateParams{
			ID:         c.ID,
			TemplateID: d.TemplateID,
		}); err != nil {
			return err
		}
	}

	return nil
}
