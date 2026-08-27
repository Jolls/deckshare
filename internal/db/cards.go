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
// survives are left completely untouched, so their id -- and therefore their user_card_state and
// review_log history -- survives the edit. Dropping and recreating a note's cards instead is the
// data-loss trap docs/schema.md names and #51 §0.4 made live.
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

	existingByOrdinal := make(map[int32]struct{}, len(existing))
	for _, c := range existing {
		existingByOrdinal[c.Ordinal] = struct{}{}
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

	return nil
}
