package apkg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
)

func TestImport_FilesCardDeckFromCardsOwnDeck(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	result, err := Import(ctx, tx, ownerID, col, time.Now())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.CardsUpserted != 5 {
		t.Fatalf("CardsUpserted = %d, want 5", result.CardsUpserted)
	}

	deckByName := map[string]db.Deck{}
	for _, name := range []string{"Default", "Default::Sub"} {
		d, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: name})
		if err != nil {
			t.Fatalf("GetDeckByOwnerAndName(%q): %v", name, err)
		}
		deckByName[name] = d
	}

	// note 101's cards: 201 in deck 1 (Default), 202 in deck 2 (Default::Sub) -- must NOT both
	// land in the note's home deck (Default, from card 201, the lowest-id card).
	n, err := q.GetNoteByOwnerAndGuid(ctx, db.GetNoteByOwnerAndGuidParams{OwnerID: ownerID, Guid: "guid-note-1"})
	if err != nil {
		t.Fatalf("GetNoteByOwnerAndGuid: %v", err)
	}
	if n.DeckID != deckByName["Default"].ID {
		t.Errorf("note's own deck_id = %v, want Default (%v)", n.DeckID, deckByName["Default"].ID)
	}

	card201, err := findCardByAnkiID(ctx, tx, 201)
	if err != nil {
		t.Fatalf("card 201: %v", err)
	}
	if card201.DeckID != deckByName["Default"].ID {
		t.Errorf("card 201 deck_id = %v, want Default", card201.DeckID)
	}
	card202, err := findCardByAnkiID(ctx, tx, 202)
	if err != nil {
		t.Fatalf("card 202: %v", err)
	}
	if card202.DeckID != deckByName["Default::Sub"].ID {
		t.Errorf("card 202 deck_id = %v, want Default::Sub -- must be filed from the card's OWN deck, not the note's home deck", card202.DeckID)
	}
}

func findCardByAnkiID(ctx context.Context, tx pgx.Tx, ankiID int64) (db.Card, error) {
	var c db.Card
	row := tx.QueryRow(ctx, `SELECT id, note_id, template_id, ordinal, deck_id, anki_id FROM cards WHERE anki_id = $1`, ankiID)
	if err := row.Scan(&c.ID, &c.NoteID, &c.TemplateID, &c.Ordinal, &c.DeckID, &c.AnkiID); err != nil {
		return db.Card{}, err
	}
	return c, nil
}

func TestImport_IdempotentOnOwnerGuid(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	r1, err := Import(ctx, tx, ownerID, col, time.Now())
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	r2, err := Import(ctx, tx, ownerID, col, time.Now())
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}

	if r2.NotesInserted != 0 || r2.NotesUpdated != len(spec.Notes) {
		t.Errorf("second import: NotesInserted=%d NotesUpdated=%d, want 0, %d", r2.NotesInserted, r2.NotesUpdated, len(spec.Notes))
	}
	if r2.CardsUpserted != r1.CardsUpserted {
		t.Errorf("second import: CardsUpserted=%d, want %d (same set, re-upserted)", r2.CardsUpserted, r1.CardsUpserted)
	}
	if r2.ReviewsInserted != 0 {
		t.Errorf("second import: ReviewsInserted=%d, want 0 (dedup on (user,card,anki_id))", r2.ReviewsInserted)
	}
}

func TestImport_ReimportPreservesCardIDsAndState(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	card, err := findCardByAnkiID(ctx, tx, 203) // a card with no revlog -- gets seeded state
	if err != nil {
		t.Fatalf("card 203: %v", err)
	}
	firstID := card.ID

	// Simulate live grading progress on this card between imports.
	if _, err := q.SeedImportedUserCardState(ctx, db.SeedImportedUserCardStateParams{
		UserID: ownerID, CardID: card.ID, Due: pgtype.Timestamptz{Time: time.Now(), Valid: true}, Stability: 9, Difficulty: 4, State: 2,
	}); err != nil {
		t.Fatalf("seeding progress: %v", err)
	}

	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("second Import: %v", err)
	}
	card2, err := findCardByAnkiID(ctx, tx, 203)
	if err != nil {
		t.Fatalf("card 203 after reimport: %v", err)
	}
	if card2.ID != firstID {
		t.Errorf("card id changed across reimport: %v -> %v", firstID, card2.ID)
	}
	state, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: ownerID, CardID: card2.ID})
	if err != nil {
		t.Fatalf("user_card_state gone after reimport: %v", err)
	}
	if state.Stability != 9 {
		t.Errorf("stability = %v, want 9 (reimport must not clobber existing progress)", state.Stability)
	}
}

func TestImport_ReimportDoesNotMoveNotes(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("first Import: %v", err)
	}

	other, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default::Sub"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName: %v", err)
	}
	n, err := q.GetNoteByOwnerAndGuid(ctx, db.GetNoteByOwnerAndGuidParams{OwnerID: ownerID, Guid: "guid-note-1"})
	if err != nil {
		t.Fatalf("GetNoteByOwnerAndGuid: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET deck_id = $1 WHERE id = $2`, other.ID, n.ID); err != nil {
		t.Fatalf("moving note: %v", err)
	}

	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("second Import: %v", err)
	}
	n2, err := q.GetNoteByOwnerAndGuid(ctx, db.GetNoteByOwnerAndGuidParams{OwnerID: ownerID, Guid: "guid-note-1"})
	if err != nil {
		t.Fatalf("GetNoteByOwnerAndGuid after reimport: %v", err)
	}
	if n2.DeckID != other.ID {
		t.Errorf("note's deck_id after reimport = %v, want unchanged %v", n2.DeckID, other.ID)
	}
}

func TestImport_ReusesDeckAndNoteTypeByName(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	r1, err := Import(ctx, tx, ownerID, col, time.Now())
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if r1.DecksCreated != 2 || r1.NoteTypesCreated != 2 {
		t.Fatalf("first import: DecksCreated=%d NoteTypesCreated=%d, want 2, 2", r1.DecksCreated, r1.NoteTypesCreated)
	}

	r2, err := Import(ctx, tx, ownerID, col, time.Now())
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if r2.DecksCreated != 0 || r2.DecksReused != 2 {
		t.Errorf("second import: DecksCreated=%d DecksReused=%d, want 0, 2", r2.DecksCreated, r2.DecksReused)
	}
	if r2.NoteTypesCreated != 0 || r2.NoteTypesReused != 2 {
		t.Errorf("second import: NoteTypesCreated=%d NoteTypesReused=%d, want 0, 2", r2.NoteTypesCreated, r2.NoteTypesReused)
	}
}

func TestImport_RejectsNoteTypeFieldCountMismatch(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("first Import: %v", err)
	}

	spec2 := defaultSynthSpec(t)
	spec2.NoteTypes[0].Fields = append(spec2.NoteTypes[0].Fields, IrField{Ordinal: 3, Name: "Extra2"})
	pkg2 := buildSchema11Package(t, spec2)
	col2 := readBytes(t, pkg2)

	_, err := Import(ctx, tx, ownerID, col2, time.Now())
	if !errors.Is(err, ErrNoteTypeMismatch) {
		t.Fatalf("err = %v, want ErrNoteTypeMismatch", err)
	}
}

func TestImport_RevlogBecomesReviewLogAndReplays(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	result, err := Import(ctx, tx, ownerID, col, time.Now())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.ReviewsInserted != 2 {
		t.Fatalf("ReviewsInserted = %d, want 2", result.ReviewsInserted)
	}
	if result.CardStatesReplayed != 1 {
		t.Fatalf("CardStatesReplayed = %d, want 1", result.CardStatesReplayed)
	}

	card, err := findCardByAnkiID(ctx, tx, 201)
	if err != nil {
		t.Fatalf("card 201: %v", err)
	}

	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM review_log WHERE card_id = $1`, card.ID).Scan(&count); err != nil {
		t.Fatalf("counting review_log: %v", err)
	}
	if count != 2 {
		t.Errorf("review_log rows for card 201 = %d, want 2", count)
	}

	var stabilityBeforeNull, fsrsVersionNull bool
	if err := tx.QueryRow(ctx, `SELECT stability_before IS NULL, fsrs_version IS NULL FROM review_log WHERE card_id = $1 LIMIT 1`, card.ID).
		Scan(&stabilityBeforeNull, &fsrsVersionNull); err != nil {
		t.Fatalf("checking imported history nullability: %v", err)
	}
	if !stabilityBeforeNull || !fsrsVersionNull {
		t.Error("imported review_log row should have NULL stability_before and fsrs_version")
	}

	q := db.New(tx)
	state, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: ownerID, CardID: card.ID})
	if err != nil {
		t.Fatalf("user_card_state after replay: %v", err)
	}
	if state.Reps == 0 {
		t.Error("user_card_state.reps = 0 after replaying 2 reviews, want > 0")
	}
}

func TestImport_SeedsStateOnlyWhenCardHasState(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	// Card 205 (new, unsuspended, unflagged, no reviews) should get NO row.
	// Card 204 (also plain new, no reviews) -- set Flags to mark it suspended so it DOES get a row.
	for i := range spec.Cards {
		if spec.Cards[i].AnkiID == 204 {
			spec.Cards[i].Queue = ankiQueueSuspended
		}
	}
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("Import: %v", err)
	}

	card205, err := findCardByAnkiID(ctx, tx, 205)
	if err != nil {
		t.Fatalf("card 205: %v", err)
	}
	if _, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: ownerID, CardID: card205.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("card 205 (no state) should have no user_card_state row, err=%v", err)
	}

	card204, err := findCardByAnkiID(ctx, tx, 204)
	if err != nil {
		t.Fatalf("card 204: %v", err)
	}
	state, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: ownerID, CardID: card204.ID})
	if err != nil {
		t.Fatalf("card 204 (suspended) should have a user_card_state row: %v", err)
	}
	if !state.Suspended {
		t.Error("card 204 user_card_state.suspended should be true")
	}
}

func TestImport_NeverWritesFactor(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	for i := range spec.Cards {
		if spec.Cards[i].AnkiID == 205 {
			spec.Cards[i].Queue = ankiQueueSuspended
			spec.Cards[i].Factor = 9999 // a high SM-2 ease that must never leak into difficulty
		}
	}
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	card, err := findCardByAnkiID(ctx, tx, 205)
	if err != nil {
		t.Fatalf("card 205: %v", err)
	}
	state, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: ownerID, CardID: card.ID})
	if err != nil {
		t.Fatalf("user_card_state: %v", err)
	}
	if state.Difficulty != 0 {
		t.Errorf("difficulty = %v, want 0 (SM-2 factor must never map onto FSRS difficulty)", state.Difficulty)
	}
}

func TestImport_MediaDeferred(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	col.Media = []IrMedia{{Index: "0", Filename: "cat.jpg", SHA256: "abc", SizeBytes: 3}}

	result, err := Import(ctx, tx, ownerID, col, time.Now())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.MediaDeferred != 1 {
		t.Errorf("MediaDeferred = %d, want 1", result.MediaDeferred)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM media_blobs`).Scan(&count); err != nil {
		t.Fatalf("counting media_blobs: %v", err)
	}
	if count != 0 {
		t.Errorf("media_blobs rows = %d, want 0 (media import is #60)", count)
	}
}

func TestImport_FilteredDeckNotCreated(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	pkg := buildFilteredDeckPackage(t)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now()); err != nil {
		t.Fatalf("Import: %v", err)
	}

	_, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Filtered"})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("filtered deck should not have been created, err=%v", err)
	}

	card, err := findCardByAnkiID(ctx, tx, 206)
	if err != nil {
		t.Fatalf("card 206: %v", err)
	}
	home, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName(Default): %v", err)
	}
	if card.DeckID != home.ID {
		t.Errorf("filtered-deck card's deck_id = %v, want its home deck %v", card.DeckID, home.ID)
	}
}
