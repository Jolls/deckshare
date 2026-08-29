// Command seed populates a freshly-migrated local database with a test user, two test decks,
// and a few sample notes/cards in each -- so a manually-tested or reset dev DB has something to
// review, not just empty decks. Signup already seeds the user's Basic/Cloze note types
// (internal/auth/notetypes.go); this adds decks and notes on top. It also creates a second test
// user and a third deck shared between them, so the deck access management page (#83) has a
// collaborator row to show without granting access by hand. Safe to re-run: an already-seeded
// deck (non-zero card count) is left alone, and an existing access grant is left alone too.
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

	collaboratorEmail       = "collaborator@test.com"
	collaboratorPassword    = "password"
	collaboratorDisplayName = "Test Collaborator"

	basicDeckName  = "Test Deck A"
	clozeDeckName  = "Test Deck B"
	sharedDeckName = "Test Deck C (Shared)"
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

	user, err := ensureUser(ctx, pool, authSvc, testEmail, testPassword, testDisplayName)
	if err != nil {
		return fmt.Errorf("ensure test user: %w", err)
	}
	collaborator, err := ensureUser(ctx, pool, authSvc, collaboratorEmail, collaboratorPassword, collaboratorDisplayName)
	if err != nil {
		return fmt.Errorf("ensure collaborator user: %w", err)
	}

	for _, name := range []string{basicDeckName, clozeDeckName, sharedDeckName} {
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

	sharedDeck, ok := findDeck(decks, sharedDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", sharedDeckName)
	}
	if sharedDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, user.ID, sharedDeck.ID, basicType.ID, "Basic", basicSamples); err != nil {
			return fmt.Errorf("seed shared deck notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", sharedDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", sharedDeckName)
	}

	// Read-only-ish grant: enough to exercise the access page's varied flags (#83) without
	// also handing the collaborator can_manage_access or can_delete.
	if err := ensureDeckAccess(ctx, pool, db.GrantDeckAccessParams{
		DeckID: sharedDeck.ID, CallerUserID: user.ID, TargetUserID: collaborator.ID,
		CanView: true, CanStudy: true,
	}); err != nil {
		return fmt.Errorf("ensure collaborator access to %q: %w", sharedDeckName, err)
	}

	return nil
}

// ensureUser signs up the given account, or looks it up if a prior seed run already created it.
func ensureUser(ctx context.Context, pool *pgxpool.Pool, authSvc *auth.Service, email, password, displayName string) (db.User, error) {
	user, _, err := authSvc.Signup(ctx, "127.0.0.1", email, password, displayName)
	if err == nil {
		log.Printf("created test user: %s / %s", email, password)
		return user, nil
	}
	if !errors.Is(err, auth.ErrEmailTaken) {
		return db.User{}, fmt.Errorf("create user: %w", err)
	}
	user, err = db.New(pool).GetUserByEmail(ctx, email)
	if err != nil {
		return db.User{}, fmt.Errorf("look up existing user: %w", err)
	}
	log.Printf("test user already exists: %s", email)
	return user, nil
}

// ensureDeckAccess grants deck access, tolerating a grant left over from a prior seed run.
func ensureDeckAccess(ctx context.Context, pool *pgxpool.Pool, arg db.GrantDeckAccessParams) error {
	rows, err := db.New(pool).GrantDeckAccess(ctx, arg)
	if err != nil {
		if db.IsUniqueViolation(err, "deck_access_pkey") {
			log.Printf("deck access already granted")
			return nil
		}
		return err
	}
	if rows == 0 {
		return fmt.Errorf("caller lacks can_view+can_manage_access on deck %v", arg.DeckID)
	}
	log.Printf("granted deck access")
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
