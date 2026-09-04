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

// TemplateOrdinalChange is a kept template whose ordinal moved (#89's reorder case).
type TemplateOrdinalChange struct {
	TemplateID pgtype.UUID
	NewOrdinal int32
}

// AddedTemplate is a template created by this same note-type edit.
type AddedTemplate struct {
	TemplateID pgtype.UUID
	Ordinal    int32
}

// RemapNoteTypeCards reconciles cards.ordinal/existence for every note of a note type against a
// structural change to that note type's templates (#89). Unlike SyncNoteCards (per-note, called on
// an ordinary note edit), this is note-type-scoped: it costs a small, fixed number of SQL
// statements -- bounded by how many templates changed, never by how many notes the note type has --
// not one round trip per note.
//
// A kept template whose ordinal moved keeps its id and its cards' template_id (card content
// identity must not change on a reorder -- flipping template_id on a swap would silently render a
// different template on every existing card, mid-schedule); only cards.ordinal moves, in two
// phases (stage to negative, then finalize) because cards carries a genuine UNIQUE (note_id,
// ordinal) constraint a same-statement permutation could otherwise violate mid-write. A removed
// template's cards are hard-deleted -- cascading user_card_state, orphaning but not deleting
// review_log, per the #51/#138 precedent -- before the now-cardless template row itself is deleted
// (cards.template_id is ON DELETE RESTRICT). An added template's cards are created via the
// existing CreateCardsForNewTemplate, one call per added template.
//
// Ordering is load-bearing: offset -> delete-cards-then-templates -> add -> finalize. Offset must
// precede finalize (that's the whole point of staging). Offset and delete must precede add, in
// case an added template's target ordinal coincides with a changed-kept template's old ordinal
// (still occupied until offset runs) or a just-removed template's ordinal (occupied until delete
// runs) -- both are guaranteed vacated by the time add runs. Finalize must be last, once every
// other phase has settled which ordinals are actually free.
//
// Must be called inside a transaction that already holds LockNoteTypeForEdit's row lock on the
// note type. The caller commits.
func RemapNoteTypeCards(ctx context.Context, tx pgx.Tx, noteTypeID pgtype.UUID, changed []TemplateOrdinalChange, removed []pgtype.UUID, added []AddedTemplate) error {
	q := New(tx)

	if len(changed) > 0 {
		ids := make([]pgtype.UUID, len(changed))
		for i, c := range changed {
			ids[i] = c.TemplateID
		}
		if _, err := q.OffsetCardOrdinalsForTemplates(ctx, ids); err != nil {
			return err
		}
	}

	if len(removed) > 0 {
		if _, err := q.DeleteCardsForTemplates(ctx, removed); err != nil {
			return err
		}
		if _, err := q.DeleteTemplatesByIDs(ctx, DeleteTemplatesByIDsParams{NoteTypeID: noteTypeID, Ids: removed}); err != nil {
			return err
		}
	}

	for _, a := range added {
		if _, err := q.CreateCardsForNewTemplate(ctx, CreateCardsForNewTemplateParams{
			TemplateID: a.TemplateID,
			Ordinal:    a.Ordinal,
			NoteTypeID: noteTypeID,
		}); err != nil {
			return err
		}
	}

	if len(changed) > 0 {
		ids := make([]pgtype.UUID, len(changed))
		ords := make([]int32, len(changed))
		for i, c := range changed {
			ids[i] = c.TemplateID
			ords[i] = c.NewOrdinal
		}
		if _, err := q.FinalizeCardOrdinalsForTemplates(ctx, FinalizeCardOrdinalsForTemplatesParams{
			TemplateIds: ids,
			NewOrdinals: ords,
		}); err != nil {
			return err
		}
	}

	return nil
}
