// Command seed populates a freshly-migrated local database with a test user, two test decks,
// and a few sample notes/cards in each -- so a manually-tested or reset dev DB has something to
// review, not just empty decks. Signup already seeds the user's Basic/Cloze note types
// (internal/auth/notetypes.go); this adds decks and notes on top. Safe to re-run: an
// already-seeded deck (non-zero card count) is left alone.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // Anki csum compatibility, not a security use of SHA-1
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
)

const (
	testEmail       = "test@test.com"
	testPassword    = "password"
	testDisplayName = "Test User"

	basicDeckName = "Test Deck A"
	clozeDeckName = "Test Deck B"
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	authSvc, err := auth.New(pool, auth.Config{})
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
	}

	user, _, err := authSvc.Signup(ctx, "127.0.0.1", testEmail, testPassword, testDisplayName)
	if err != nil {
		if errors.Is(err, auth.ErrEmailTaken) {
			q := db.New(pool)
			user, err = q.GetUserByEmail(ctx, testEmail)
			if err != nil {
				return fmt.Errorf("look up existing test user: %w", err)
			}
			log.Printf("test user already exists: %s", testEmail)
		} else {
			return fmt.Errorf("create test user: %w", err)
		}
	} else {
		log.Printf("created test user: %s / %s", testEmail, testPassword)
	}

	for _, name := range []string{basicDeckName, clozeDeckName} {
		if err := ensureDeck(ctx, pool, user.ID, name); err != nil {
			return fmt.Errorf("ensure deck %q: %w", name, err)
		}
	}

	q := db.New(pool)
	decks, err := q.ListDecksForUser(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("list decks: %w", err)
	}
	noteTypes, err := q.ListNoteTypesForOwner(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("list note types: %w", err)
	}

	basicDeck, ok := findDeck(decks, basicDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", basicDeckName)
	}
	clozeDeck, ok := findDeck(decks, clozeDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", clozeDeckName)
	}
	basicType, ok := findNoteType(noteTypes, "Basic")
	if !ok {
		return errors.New(`note type "Basic" not found -- signup seeding may have failed`)
	}
	clozeType, ok := findNoteType(noteTypes, "Cloze")
	if !ok {
		return errors.New(`note type "Cloze" not found -- signup seeding may have failed`)
	}

	if basicDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, user.ID, basicDeck.ID, basicType.ID, "Basic", basicSamples); err != nil {
			return fmt.Errorf("seed basic notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", basicDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", basicDeckName)
	}

	if clozeDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, user.ID, clozeDeck.ID, clozeType.ID, "Cloze", clozeSamples); err != nil {
			return fmt.Errorf("seed cloze notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", clozeDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", clozeDeckName)
	}

	return nil
}

func ensureDeck(ctx context.Context, pool *pgxpool.Pool, ownerID pgtype.UUID, name string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := db.CreateDeckWithAccess(ctx, tx, ownerID, name, ""); err != nil {
		if db.IsUniqueViolation(err, "decks_owner_id_name_key") {
			log.Printf("deck already exists: %s", name)
			return nil
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	log.Printf("created deck: %s", name)
	return nil
}

func findDeck(decks []db.ListDecksForUserRow, name string) (db.ListDecksForUserRow, bool) {
	for _, d := range decks {
		if d.Name == name {
			return d, true
		}
	}
	return db.ListDecksForUserRow{}, false
}

func findNoteType(noteTypes []db.ListNoteTypesForOwnerRow, name string) (db.ListNoteTypesForOwnerRow, bool) {
	for _, nt := range noteTypes {
		if nt.Name == name {
			return nt, true
		}
	}
	return db.ListNoteTypesForOwnerRow{}, false
}

var basicSamples = [][2]string{
	{"Capital of France", "Paris"},
	{"2 + 2", "4"},
	{"Author of Hamlet", "William Shakespeare"},
}

var clozeSamples = [][2]string{
	{"The mitochondria is the {{c1::powerhouse}} of the cell", ""},
	{"Water freezes at {{c1::0}} degrees Celsius", ""},
}

func seedSampleNotes(ctx context.Context, pool *pgxpool.Pool, userID, deckID, noteTypeID pgtype.UUID, typeName string, samples [][2]string) error {
	q := db.New(pool)
	templates, err := q.ListTemplatesForNoteType(ctx, noteTypeID)
	if err != nil {
		return err
	}
	if len(templates) == 0 {
		return fmt.Errorf("note type %q has no templates", typeName)
	}

	for _, pair := range samples {
		fieldsJSON, checksum, err := fieldsAndChecksum(pair[0], pair[1])
		if err != nil {
			return err
		}
		guid, err := randomGuid()
		if err != nil {
			return err
		}
		if err := createNote(ctx, pool, db.CreateNoteParams{
			Guid:       guid,
			Fields:     fieldsJSON,
			Tags:       []string{},
			Checksum:   checksum,
			UserID:     userID,
			NoteTypeID: noteTypeID,
			DeckID:     deckID,
		}, []db.DesiredCard{{Ordinal: 0, TemplateID: templates[0].ID}}); err != nil {
			return err
		}
	}
	return nil
}

func createNote(ctx context.Context, pool *pgxpool.Pool, arg db.CreateNoteParams, desired []db.DesiredCard) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := db.CreateNoteWithCards(ctx, tx, arg, desired); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// fieldsAndChecksum mirrors internal/http/notes.go's validateNoteFields: notes.fields is a
// positional jsonb array, and the checksum is Anki's truncated SHA-1 of the first field with
// HTML tags stripped.
func fieldsAndChecksum(first, second string) ([]byte, int64, error) {
	fieldsJSON, err := json.Marshal([]string{first, second})
	if err != nil {
		return nil, 0, err
	}
	stripped := htmlTagRe.ReplaceAllString(first, "")
	sum := sha1.Sum([]byte(stripped))
	checksum := int64(binary.BigEndian.Uint32(sum[:4]))
	return fieldsJSON, checksum, nil
}

func randomGuid() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
