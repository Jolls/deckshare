package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// The trap's regression test (docs/schema.md, #51 §0.4): editing a note must not drop and
// recreate its cards -- a surviving ordinal's card id, and therefore its user_card_state, must
// be untouched.
func TestSyncNoteCards_KeepsSurvivingOrdinalsUntouched(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)

	c1 := mustCard(t, tx, note, template, deck, 0)
	c2 := mustCard(t, tx, note, template, deck, 1)
	c3 := mustCard(t, tx, note, template, deck, 2)
	mustUserCardState(t, tx, user, c1)

	// Edit: keep ordinal 0 and 1, remove ordinal 2, add ordinal 3.
	desired := []DesiredCard{
		{Ordinal: 0, TemplateID: template},
		{Ordinal: 1, TemplateID: template},
		{Ordinal: 3, TemplateID: template},
	}
	if err := SyncNoteCards(ctx, tx, note, deck, desired); err != nil {
		t.Fatalf("SyncNoteCards: %v", err)
	}

	var gotID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE note_id = $1 AND ordinal = 0`, note).Scan(&gotID); err != nil {
		t.Fatalf("select ordinal 0: %v", err)
	}
	if gotID != c1 {
		t.Errorf("ordinal 0 card id changed: got %v, want %v (original)", gotID, c1)
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, c1) != 1 {
		t.Error("user_card_state for the surviving card should still exist")
	}
	if !rowExists(t, tx, "cards", c2) {
		t.Error("ordinal 1's original card should survive untouched")
	}
	if rowExists(t, tx, "cards", c3) {
		t.Error("removed ordinal 2's card should be gone")
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1 AND ordinal = 3`, note) != 1 {
		t.Error("new ordinal 3 card should have been created")
	}
}

// The concrete regression test for #138's "surviving ordinal, new note type" case: a card whose
// ordinal survives a note-type change must keep its id (and therefore its user_card_state/
// review_log) but have template_id repointed to the new note type's template at that ordinal --
// the study batch query reads qfmt/afmt from cards.template_id, not notes.note_type_id.
func TestSyncNoteCards_RepointsTemplateIDOnSurvivingOrdinal(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteTypeA := mustNoteType(t, tx, user)
	noteTypeB := mustNoteType(t, tx, user)
	templateA := mustTemplate(t, tx, noteTypeA, 0)
	templateB := mustTemplate(t, tx, noteTypeB, 0)
	note := mustNote(t, tx, user, noteTypeA, deck)

	card := mustCard(t, tx, note, templateA, deck, 0)

	desired := []DesiredCard{
		{Ordinal: 0, TemplateID: templateB},
	}
	if err := SyncNoteCards(ctx, tx, note, deck, desired); err != nil {
		t.Fatalf("SyncNoteCards: %v", err)
	}

	var gotID, gotTemplateID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id, template_id FROM cards WHERE note_id = $1 AND ordinal = 0`, note).Scan(&gotID, &gotTemplateID); err != nil {
		t.Fatalf("select ordinal 0: %v", err)
	}
	if gotID != card {
		t.Errorf("ordinal 0 card id changed: got %v, want %v (original)", gotID, card)
	}
	if gotTemplateID != templateB {
		t.Errorf("ordinal 0 card template_id = %v, want %v (new note type's template)", gotTemplateID, templateB)
	}
}

// The concrete regression test for the "card deletion vs. review_log" question docs/plans/
// 138-edit-note-type.md resolves as already-decided: SyncNoteCards already hard-deletes a card
// whose ordinal is dropped (e.g. a note-type change that removes a template), and that delete
// cascades away user_card_state but leaves review_log orphaned, not deleted.
func TestSyncNoteCards_RemovedOrdinal_ReviewLogSurvivesOrphaned(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)

	// A second, surviving ordinal so dropping ordinal 1 doesn't leave the note card-less
	// (SyncNoteCards would reject an empty desired set with ErrNoCards before touching anything).
	mustCard(t, tx, note, template, deck, 0)
	card := mustCard(t, tx, note, template, deck, 1)
	mustUserCardState(t, tx, user, card)
	reviewLogID := mustReviewLog(t, tx, user, card)

	desired := []DesiredCard{
		{Ordinal: 0, TemplateID: template},
	}
	if err := SyncNoteCards(ctx, tx, note, deck, desired); err != nil {
		t.Fatalf("SyncNoteCards: %v", err)
	}

	if rowExists(t, tx, "cards", card) {
		t.Error("removed ordinal's card should be gone")
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, card) != 0 {
		t.Error("user_card_state for the removed card should have cascaded away")
	}
	if countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1 AND card_id = $2`, reviewLogID, card) != 1 {
		t.Error("review_log row should survive, orphaned, with card_id unchanged")
	}
}

func TestSyncNoteCards_EmptyDesiredReturnsErrNoCards(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	note := mustNote(t, tx, user, noteType, deck)

	err := SyncNoteCards(ctx, tx, note, deck, nil)
	if !errors.Is(err, ErrNoCards) {
		t.Fatalf("SyncNoteCards error = %v, want ErrNoCards", err)
	}
}

func TestSyncNoteCards_SequentialSyncsAccumulate(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)
	mustCard(t, tx, note, template, deck, 0)

	nested := beginSavepoint(t, tx)
	if err := SyncNoteCards(ctx, nested, note, deck, []DesiredCard{
		{Ordinal: 0, TemplateID: template},
		{Ordinal: 1, TemplateID: template},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := nested.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1 AND ordinal = 1`, note) != 1 {
		t.Fatal("newly created card should exist")
	}
}
