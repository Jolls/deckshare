package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Migration 00015's composite FK regression: notes.(deck_id, owner_id) must reference
// decks(id, owner_id) -- a note whose owner_id doesn't match its deck's owner is rejected at
// the database level, not just by convention (docs/schema.md, Identifiers).
func TestNotesOwnerMatchesDeck_RejectsMismatch(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	deckOwner := mustUser(t, tx)
	otherUser := mustUser(t, tx)
	deck := mustDeck(t, tx, deckOwner)
	noteType := mustNoteType(t, tx, deckOwner)

	_, err := tx.Exec(ctx,
		`INSERT INTO notes (guid, owner_id, note_type_id, deck_id, checksum) VALUES ('g', $1, $2, $3, 0)`,
		otherUser, noteType, deck,
	)
	if !isForeignKeyViolation(err) {
		t.Fatalf("insert with mismatched owner_id error = %v, want foreign_key_violation", err)
	}
}

func TestMoveNote_UpdatesOwnerAndCards(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	owner1 := mustUser(t, tx)
	owner2 := mustUser(t, tx)
	d1 := mustDeck(t, tx, owner1)
	d2 := mustDeck(t, tx, owner2)
	mustDeckAccess(t, tx, d1, owner1, fullAccess())
	mustDeckAccess(t, tx, d2, owner1, fullAccess())
	noteType := mustNoteType(t, tx, owner1)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, owner1, noteType, d1)
	card := mustCard(t, tx, note, template, d1, 0)

	if err := MoveNote(ctx, tx, owner1, note, d2); err != nil {
		t.Fatalf("MoveNote: %v", err)
	}

	var deckID, ownerID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT deck_id, owner_id FROM notes WHERE id = $1`, note).Scan(&deckID, &ownerID); err != nil {
		t.Fatalf("select note: %v", err)
	}
	if deckID != d2 {
		t.Errorf("note.deck_id = %v, want %v", deckID, d2)
	}
	if ownerID != owner2 {
		t.Errorf("note.owner_id = %v, want %v", ownerID, owner2)
	}
	var cardDeckID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT deck_id FROM cards WHERE id = $1`, card).Scan(&cardDeckID); err != nil {
		t.Fatalf("select card: %v", err)
	}
	if cardDeckID != d2 {
		t.Errorf("card.deck_id = %v, want %v", cardDeckID, d2)
	}
}

func TestMoveNote_DeniedWithoutTargetAccess(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	owner1 := mustUser(t, tx)
	owner2 := mustUser(t, tx)
	d1 := mustDeck(t, tx, owner1)
	d2 := mustDeck(t, tx, owner2)
	mustDeckAccess(t, tx, d1, owner1, fullAccess())
	noteType := mustNoteType(t, tx, owner1)
	note := mustNote(t, tx, owner1, noteType, d1)

	err := MoveNote(ctx, tx, owner1, note, d2)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("MoveNote error = %v, want pgx.ErrNoRows", err)
	}
}
