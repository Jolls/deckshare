package apkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
)

func TestImport_FilesCardDeckFromCardsOwnDeck(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	result, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
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

// TestImport_ResultReportsDeckCardCounts asserts ImportResult.Decks (the import UI's #62
// redirect target) tallies cards per deck correctly: defaultSynthSpec files 3 cards under
// "Default" (Did 1) and 2 under "Default::Sub" (Did 2).
func TestImport_ResultReportsDeckCardCounts(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	result, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	defaultDeck, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName(Default): %v", err)
	}
	subDeck, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default::Sub"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName(Default::Sub): %v", err)
	}

	if len(result.Decks) != 2 {
		t.Fatalf("len(result.Decks) = %d, want 2", len(result.Decks))
	}
	countByID := map[pgtype.UUID]int{}
	for _, d := range result.Decks {
		countByID[d.ID] = d.CardCount
	}
	if countByID[defaultDeck.ID] != 3 {
		t.Errorf("Default card count = %d, want 3", countByID[defaultDeck.ID])
	}
	if countByID[subDeck.ID] != 2 {
		t.Errorf("Default::Sub card count = %d, want 2", countByID[subDeck.ID])
	}
}

func findCardByAnkiID(ctx context.Context, tx pgx.Tx, ankiID int64) (db.Card, error) {
	var c db.Card
	row := tx.QueryRow(ctx, `SELECT id, note_id, template_id, ordinal, deck_id, anki_id, import_due_position FROM cards WHERE anki_id = $1`, ankiID)
	if err := row.Scan(&c.ID, &c.NoteID, &c.TemplateID, &c.Ordinal, &c.DeckID, &c.AnkiID, &c.ImportDuePosition); err != nil {
		return db.Card{}, err
	}
	return c, nil
}

// TestImport_PersistsNewCardDuePosition (#82) asserts a new card's Anki queue position survives
// import into cards.import_due_position, using defaultSynthSpec's own new cards (202->7, 203->1,
// 204->2, 205->3) -- already out of AnkiID/card-table order, so this also guards against a
// regression that happens to work only when import order matches Anki's order.
func TestImport_PersistsNewCardDuePosition(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
		t.Fatalf("Import: %v", err)
	}

	wantPosition := map[int64]int32{202: 7, 203: 1, 204: 2, 205: 3}
	for ankiID, want := range wantPosition {
		card, err := findCardByAnkiID(ctx, tx, ankiID)
		if err != nil {
			t.Fatalf("card %d: %v", ankiID, err)
		}
		if !card.ImportDuePosition.Valid || card.ImportDuePosition.Int32 != want {
			t.Errorf("card %d import_due_position = %+v, want %d", ankiID, card.ImportDuePosition, want)
		}
	}

	// Card 201 is a review card (DueAt, not DuePosition) -- must stay NULL, not accidentally pick
	// up its due-days value as a queue position.
	card201, err := findCardByAnkiID(ctx, tx, 201)
	if err != nil {
		t.Fatalf("card 201: %v", err)
	}
	if card201.ImportDuePosition.Valid {
		t.Errorf("card 201 (review card) import_due_position = %+v, want NULL", card201.ImportDuePosition)
	}
}

func TestImport_IdempotentOnOwnerGuid(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	r1, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	r2, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
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

	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
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

	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
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
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
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

	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
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

	r1, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if r1.DecksCreated != 2 || r1.NoteTypesCreated != 2 {
		t.Fatalf("first import: DecksCreated=%d NoteTypesCreated=%d, want 2, 2", r1.DecksCreated, r1.NoteTypesCreated)
	}

	r2, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
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
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
		t.Fatalf("first Import: %v", err)
	}

	spec2 := defaultSynthSpec(t)
	spec2.NoteTypes[0].Fields = append(spec2.NoteTypes[0].Fields, IrField{Ordinal: 3, Name: "Extra2"})
	pkg2 := buildSchema11Package(t, spec2)
	col2 := readBytes(t, pkg2)

	_, err := Import(ctx, tx, ownerID, col2, time.Now(), testMediaStore(t))
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
	result, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
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
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
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
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
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

func syntheticMedia(filename string, data []byte) IrMedia {
	sum := sha256.Sum256(data)
	return IrMedia{Index: "0", Filename: filename, SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)), Data: data}
}

// The package format doesn't attribute media files to individual decks, so every deck the import
// touches gets a ref (dbwrite.go's importMedia doc comment) -- asserted here against BOTH of
// defaultSynthSpec's decks, not just one.
func TestImport_MediaWrittenToStoreAndDB(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	store := testMediaStore(t)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	m := syntheticMedia("cat.jpg", []byte("a fake jpeg"))
	col.Media = []IrMedia{m}

	result, err := Import(ctx, tx, ownerID, col, time.Now(), store)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.MediaImported != 1 {
		t.Errorf("MediaImported = %d, want 1", result.MediaImported)
	}

	blob, err := q.GetMediaBlob(ctx, m.SHA256)
	if err != nil {
		t.Fatalf("GetMediaBlob: %v", err)
	}
	if blob.SizeBytes != m.SizeBytes {
		t.Errorf("SizeBytes = %d, want %d", blob.SizeBytes, m.SizeBytes)
	}

	f, err := store.Open(m.SHA256)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading blob: %v", err)
	}
	if string(got) != string(m.Data) {
		t.Errorf("blob bytes = %q, want %q", got, m.Data)
	}

	for _, name := range []string{"Default", "Default::Sub"} {
		d, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: name})
		if err != nil {
			t.Fatalf("GetDeckByOwnerAndName(%q): %v", name, err)
		}
		ref, err := q.GetMediaRef(ctx, db.GetMediaRefParams{DeckID: d.ID, Filename: m.Filename})
		if err != nil {
			t.Fatalf("GetMediaRef for deck %q: %v", name, err)
		}
		if ref.Sha256 != m.SHA256 {
			t.Errorf("deck %q media ref sha256 = %q, want %q", name, ref.Sha256, m.SHA256)
		}
	}
}

// http.DetectContentType has no signature for SVG (XML, no fixed magic bytes) and falls back to
// text/plain, which browsers refuse to render inside <img>. detectMediaMime must prefer the
// original filename's extension instead.
func TestImport_MediaMimeFromExtension(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	store := testMediaStore(t)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	m := syntheticMedia("flag.svg", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"))
	col.Media = []IrMedia{m}

	if _, err := Import(ctx, tx, ownerID, col, time.Now(), store); err != nil {
		t.Fatalf("Import: %v", err)
	}

	blob, err := q.GetMediaBlob(ctx, m.SHA256)
	if err != nil {
		t.Fatalf("GetMediaBlob: %v", err)
	}
	if blob.Mime != "image/svg+xml" {
		t.Errorf("Mime = %q, want image/svg+xml", blob.Mime)
	}
}

// Re-importing the same package a second time must not duplicate the blob row or error on the
// already-written file (CLAUDE.md §2.2's re-import idempotency, extended to media).
func TestImport_MediaReimportIsIdempotent(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	store := testMediaStore(t)

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	m := syntheticMedia("cat.jpg", []byte("a fake jpeg"))

	col1 := readBytes(t, pkg)
	col1.Media = []IrMedia{m}
	if _, err := Import(ctx, tx, ownerID, col1, time.Now(), store); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	col2 := readBytes(t, pkg)
	col2.Media = []IrMedia{m}
	if _, err := Import(ctx, tx, ownerID, col2, time.Now(), store); err != nil {
		t.Fatalf("second Import: %v", err)
	}

	// Scoped to this test's own blob, not table-wide: a dev database seeded by cmd/seed already
	// holds the avatar blob, and DB-backed tests must tolerate that (CLAUDE.md §16, #141).
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM media_blobs WHERE sha256 = $1`, m.SHA256).Scan(&count); err != nil {
		t.Fatalf("counting media_blobs: %v", err)
	}
	if count != 1 {
		t.Errorf("media_blobs rows = %d, want 1", count)
	}
}

func TestImport_FilteredDeckNotCreated(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	pkg := buildFilteredDeckPackage(t)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
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

// setCardData mutates spec.Cards in place, setting the raw cards.data JSON on the card with the
// given AnkiID. t.Fatal's if no such card exists -- a typo'd AnkiID should fail loudly, not
// silently leave the card's data at its default.
func setCardData(t *testing.T, spec *synthSpec, ankiID int64, data string) {
	t.Helper()
	for i := range spec.Cards {
		if spec.Cards[i].AnkiID == ankiID {
			spec.Cards[i].Data = data
			return
		}
	}
	t.Fatalf("setCardData: no card with AnkiID %d in spec", ankiID)
}

// TestImport_SeedsDesiredRetentionFromCardData covers the issue #81 fix's zero-review path: a
// card with no revlog rows goes through seedCardStates, not importReviews/ReplayCard, but the
// deck's desired retention must still be seeded from its imported cards.data.dr rather than
// falling back to review.DefaultDesiredRetention (0.9).
func TestImport_SeedsDesiredRetentionFromCardData(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	// Card 204 (Did 1 / "Default", no revlog rows) -- mark suspended so seedCardStates also
	// writes it a user_card_state row, and give it FSRS state including desired retention.
	for i := range spec.Cards {
		if spec.Cards[i].AnkiID == 204 {
			spec.Cards[i].Queue = ankiQueueSuspended
		}
	}
	setCardData(t, &spec, 204, `{"s":30,"d":6,"dr":0.85}`)

	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
		t.Fatalf("Import: %v", err)
	}

	deck, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName(Default): %v", err)
	}
	got, err := q.GetDeckFsrsRetention(ctx, db.GetDeckFsrsRetentionParams{UserID: ownerID, DeckID: deck.ID})
	if err != nil {
		t.Fatalf("GetDeckFsrsRetention: %v", err)
	}
	if got != 0.85 {
		t.Errorf("Default deck desired_retention = %v, want 0.85 (the imported dr, not the 0.9 default)", got)
	}
}

// TestImport_SeedsDesiredRetentionFromReplayedCard covers the path the issue's own proposed fix
// (wiring DesiredRetention only into SeedImportedUserCardState) would have missed: card 201 has
// revlog rows, so it goes through importReviews/ReplayCard, never seedCardStates. The deck's
// retention must still be seeded, and it must happen before ReplayCard runs so the replay itself
// resolves the imported retention rather than the default.
func TestImport_SeedsDesiredRetentionFromReplayedCard(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	setCardData(t, &spec, 201, `{"s":30,"d":6,"dr":0.85}`)

	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	result, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.CardStatesReplayed != 1 {
		t.Fatalf("CardStatesReplayed = %d, want 1 (sanity check: card 201 must go through the replay path)", result.CardStatesReplayed)
	}

	deck, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName(Default): %v", err)
	}
	got, err := q.GetDeckFsrsRetention(ctx, db.GetDeckFsrsRetentionParams{UserID: ownerID, DeckID: deck.ID})
	if err != nil {
		t.Fatalf("GetDeckFsrsRetention: %v", err)
	}
	if got != 0.85 {
		t.Errorf("Default deck desired_retention = %v, want 0.85 (seeding must run before importReviews replays card 201)", got)
	}
}

// TestImport_ReimportDoesNotOverwriteChangedRetention asserts SeedDeckFsrsRetention's ON CONFLICT
// DO NOTHING actually protects a retention the user has since changed via /settings: a re-import
// of the same package must not clobber it back to the package's original value.
func TestImport_ReimportDoesNotOverwriteChangedRetention(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	setCardData(t, &spec, 204, `{"s":30,"d":6,"dr":0.85}`)
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)

	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
		t.Fatalf("Import (first): %v", err)
	}

	deck, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName(Default): %v", err)
	}

	params, err := fsrs.NewDefaultParams(0.95)
	if err != nil {
		t.Fatalf("NewDefaultParams: %v", err)
	}
	if _, err := q.UpsertDeckFsrsRetention(ctx, db.UpsertDeckFsrsRetentionParams{
		UserID: ownerID, DeckID: deck.ID, FsrsVersion: int16(params.Version()), DesiredRetention: 0.95,
	}); err != nil {
		t.Fatalf("UpsertDeckFsrsRetention (simulating a /settings change): %v", err)
	}

	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
		t.Fatalf("Import (re-import): %v", err)
	}

	got, err := q.GetDeckFsrsRetention(ctx, db.GetDeckFsrsRetentionParams{UserID: ownerID, DeckID: deck.ID})
	if err != nil {
		t.Fatalf("GetDeckFsrsRetention: %v", err)
	}
	if got != 0.95 {
		t.Errorf("Default deck desired_retention = %v, want 0.95 (re-import must not overwrite a user-changed retention)", got)
	}
}

// TestImport_DesiredRetentionMajorityWinsWithinDeck covers a deck whose cards.data.dr values
// disagree, the case a mid-collection retention change in Anki produces: the majority value
// across the deck's cards should win, not the first or last one seen.
func TestImport_DesiredRetentionMajorityWinsWithinDeck(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	// Cards 201, 204, 205 all file under Did 1 ("Default"). Two vote 0.85, one votes 0.80.
	setCardData(t, &spec, 201, `{"s":30,"d":6,"dr":0.85}`)
	setCardData(t, &spec, 204, `{"s":30,"d":6,"dr":0.85}`)
	setCardData(t, &spec, 205, `{"s":30,"d":6,"dr":0.80}`)

	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	if _, err := Import(ctx, tx, ownerID, col, time.Now(), testMediaStore(t)); err != nil {
		t.Fatalf("Import: %v", err)
	}

	deck, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName(Default): %v", err)
	}
	got, err := q.GetDeckFsrsRetention(ctx, db.GetDeckFsrsRetentionParams{UserID: ownerID, DeckID: deck.ID})
	if err != nil {
		t.Fatalf("GetDeckFsrsRetention: %v", err)
	}
	if got != 0.85 {
		t.Errorf("Default deck desired_retention = %v, want 0.85 (the majority value, 2 votes vs 1)", got)
	}
}
