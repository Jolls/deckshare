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
