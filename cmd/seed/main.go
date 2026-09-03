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
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/media"
)

// seedAvatarJPEG is a real (manually tested via the upload UI) avatar image, so a fresh seed
// shows a populated account header/settings avatar instead of the initials-only placeholder.
//
//go:embed avatar.jpg
var seedAvatarJPEG []byte

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

	// Small caps so a fresh seed exercises #172's daily-cap / "Keep studying" path without
	// requiring dozens of reviews first.
	seedNewPerDay = 2
	seedRevPerDay = 5

	// seedDueCardCount is how many of each freshly-seeded deck's cards start already in Review
	// state and due, rather than New.
	seedDueCardCount = 2

	// baseCardCSS/clozeBoldCSS: signup seeds the test user's Basic/Cloze note types with empty
	// CSS (internal/auth/notetypes.go), so a reseed has nothing to visually check note-type
	// CSS against (e.g. #194: the reviewer's visible card missing the deckshare-card scope class,
	// which silently drops every note-type CSS rule). Backfilled here rather than by changing
	// what signup seeds, since this is test-fixture content, not a product default.
	baseCardCSS = ".card {\n" +
		"    font-family: arial;\n" +
		"    font-size: 20px;\n" +
		"    text-align: center;\n" +
		"    color: black;\n" +
		"    background-color: white;\n" +
		"}\n"
	clozeBoldCSS = ".cloze {\n    font-weight: bold;\n}\n"
)

// seedDueDate is a fixed calendar date, not relative to time.Now -- so the "couple of due cards"
// stay due after every reseed for as long as this stays in the past, rather than only on the day
// the seed happens to run.
var seedDueDate = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
var seedDueLastReview = seedDueDate.AddDate(0, 0, -7)

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

	if !user.AvatarSha256.Valid {
		if err := ensureAvatar(ctx, pool, user.ID); err != nil {
			return fmt.Errorf("ensure test user avatar: %w", err)
		}
	} else {
		log.Print("test user already has an avatar, skipping avatar seeding")
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

	if err := ensureNoteTypeCSS(ctx, q, basicType, baseCardCSS); err != nil {
		return fmt.Errorf("ensure Basic note type css: %w", err)
	}
	if err := ensureNoteTypeCSS(ctx, q, clozeType, baseCardCSS+clozeBoldCSS); err != nil {
		return fmt.Errorf("ensure Cloze note type css: %w", err)
	}

	if basicDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, user.ID, basicDeck.ID, basicType.ID, "Basic", basicSamples); err != nil {
			return fmt.Errorf("seed basic notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", basicDeckName)
		if err := seedDueCards(ctx, pool, user.ID, basicDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards in %s: %w", basicDeckName, err)
		}
	} else {
		log.Printf("%s already has cards, skipping note seeding", basicDeckName)
	}

	if clozeDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, user.ID, clozeDeck.ID, clozeType.ID, "Cloze", clozeSamples); err != nil {
			return fmt.Errorf("seed cloze notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", clozeDeckName)
		if err := seedDueCards(ctx, pool, user.ID, clozeDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards in %s: %w", clozeDeckName, err)
		}
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
		if err := seedDueCards(ctx, pool, user.ID, sharedDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards in %s: %w", sharedDeckName, err)
		}
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

// ensureAvatar stores seedAvatarJPEG as a content-addressed blob and points userID's
// avatar_sha256 at it, using the same single-transaction shape as POST /settings/avatar
// (internal/http/settings.go) so the media GC sweep never observes the blob row committed but
// not yet referenced. MEDIA_ROOT defaults to "./media" (matching .env.example) rather than
// failing like cmd/deckshare's requirement, since a missing avatar is not fatal to seeding decks/notes.
func ensureAvatar(ctx context.Context, pool *pgxpool.Pool, userID pgtype.UUID) error {
	mediaRoot := os.Getenv("MEDIA_ROOT")
	if mediaRoot == "" {
		mediaRoot = "./media"
	}
	blobs := media.New(mediaRoot)

	sum := sha256.Sum256(seedAvatarJPEG)
	sha := hex.EncodeToString(sum[:])
	if err := blobs.Put(sha, seedAvatarJPEG); err != nil {
		return fmt.Errorf("write avatar blob: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := db.New(tx)
	if err := q.CreateMediaBlob(ctx, db.CreateMediaBlobParams{
		Sha256: sha, SizeBytes: int64(len(seedAvatarJPEG)), Mime: "image/jpeg",
	}); err != nil {
		return fmt.Errorf("create media blob: %w", err)
	}
	if err := q.UpdateUserAvatar(ctx, db.UpdateUserAvatarParams{
		ID: userID, AvatarSha256: pgtype.Text{String: sha, Valid: true},
	}); err != nil {
		return fmt.Errorf("update user avatar: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	log.Print("seeded test user avatar")
	return nil
}

func ensureDeck(ctx context.Context, pool *pgxpool.Pool, ownerID pgtype.UUID, name string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	deck, err := db.CreateDeckWithAccess(ctx, tx, ownerID, name, "")
	if err != nil {
		if db.IsUniqueViolation(err, "decks_owner_id_name_key") {
			log.Printf("deck already exists: %s", name)
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE decks SET preset = jsonb_build_object(
			'new', jsonb_build_object('perDay', $1::int),
			'rev', jsonb_build_object('perDay', $2::int)
		) WHERE id = $3`, seedNewPerDay, seedRevPerDay, deck.ID); err != nil {
		return fmt.Errorf("set preset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	log.Printf("created deck: %s", name)
	return nil
}

// seedDueCards puts the first n of deckID's cards into Review state, due on seedDueDate, for
// userID -- so a fresh deck has a couple of genuinely due cards to review, not just New ones.
// ON CONFLICT DO NOTHING: never overwrites real progress from manual testing on a deck that
// already had cards before this seed run.
func seedDueCards(ctx context.Context, pool *pgxpool.Pool, userID, deckID pgtype.UUID, n int) error {
	rows, err := pool.Query(ctx, `SELECT id FROM cards WHERE deck_id = $1 ORDER BY id LIMIT $2`, deckID, n)
	if err != nil {
		return fmt.Errorf("list cards to mark due: %w", err)
	}
	var cardIDs []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		cardIDs = append(cardIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, cardID := range cardIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_card_state
				(user_id, card_id, due, stability, difficulty, state, reps, lapses,
				 elapsed_days, scheduled_days, learning_steps, last_review)
			VALUES ($1, $2, $3, 3, 5, 2, 1, 0, 7, 7, 0, $4)
			ON CONFLICT (user_id, card_id) DO NOTHING`,
			userID, cardID, seedDueDate, seedDueLastReview); err != nil {
			return fmt.Errorf("mark card %s due: %w", cardID.String(), err)
		}
	}
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

// ensureNoteTypeCSS backfills a note type's CSS the first time seed runs against it, then leaves
// it alone -- re-running seed must not clobber CSS someone edited by hand in the running app.
func ensureNoteTypeCSS(ctx context.Context, q *db.Queries, nt db.ListNoteTypesForOwnerRow, wantCSS string) error {
	if nt.Css != "" {
		return nil
	}
	if _, err := q.UpdateNoteTypeRow(ctx, db.UpdateNoteTypeRowParams{
		Name: nt.Name, Css: wantCSS, SortFieldIdx: nt.SortFieldIdx, ID: nt.ID, OwnerID: nt.OwnerID,
	}); err != nil {
		return fmt.Errorf("update note type css: %w", err)
	}
	return nil
}

func findNoteType(noteTypes []db.ListNoteTypesForOwnerRow, name string) (db.ListNoteTypesForOwnerRow, bool) {
	for _, nt := range noteTypes {
		if nt.Name == name {
			return nt, true
		}
	}
	return db.ListNoteTypesForOwnerRow{}, false
}

// basicSamples has more than DefaultNewPerDay (20, internal/review/preset.go) entries so a fresh
// seed exercises #172's "Keep studying" past the daily cap without any manual preset editing.
var basicSamples = [][2]string{
	{"Capital of France", "Paris"},
	{"2 + 2", "4"},
	{"Author of Hamlet", "William Shakespeare"},
	{"Capital of Japan", "Tokyo"},
	{"Largest planet in the solar system", "Jupiter"},
	{"Chemical symbol for gold", "Au"},
	{"Speed of light (approx, km/s)", "300,000"},
	{"Author of Pride and Prejudice", "Jane Austen"},
	{"Capital of Australia", "Canberra"},
	{"Number of continents", "7"},
	{"Smallest prime number", "2"},
	{"Capital of Canada", "Ottawa"},
	{"Chemical symbol for oxygen", "O"},
	{"Longest river in the world", "The Nile"},
	{"Capital of Egypt", "Cairo"},
	{"Freezing point of water in Fahrenheit", "32"},
	{"Author of War and Peace", "Leo Tolstoy"},
	{"Capital of Brazil", "Brasília"},
	{"Number of strings on a standard guitar", "6"},
	{"Chemical symbol for sodium", "Na"},
	{"Capital of Germany", "Berlin"},
	{"Square root of 144", "12"},
	{"Capital of Italy", "Rome"},
	{"Author of The Odyssey", "Homer"},
	{"Capital of Russia", "Moscow"},
}

// clozeSamples similarly exceeds the 20/day cap.
var clozeSamples = [][2]string{
	{"The mitochondria is the {{c1::powerhouse}} of the cell", ""},
	{"Water freezes at {{c1::0}} degrees Celsius", ""},
	{"The human body has {{c1::206}} bones", ""},
	{"DNA stands for {{c1::deoxyribonucleic acid}}", ""},
	{"The speed of sound is about {{c1::343}} meters per second", ""},
	{"Photosynthesis converts {{c1::light energy}} into chemical energy", ""},
	{"The largest organ in the human body is the {{c1::skin}}", ""},
	{"A triangle has {{c1::three}} sides", ""},
	{"The chemical formula for water is {{c1::H2O}}", ""},
	{"The sun is a {{c1::star}}", ""},
	{"There are {{c1::seven}} days in a week", ""},
	{"The Earth orbits the {{c1::Sun}}", ""},
	{"A hexagon has {{c1::six}} sides", ""},
	{"The powerhouse of a cell's energy is {{c1::ATP}}", ""},
	{"Humans have {{c1::23}} pairs of chromosomes", ""},
	{"The boiling point of water is {{c1::100}} degrees Celsius", ""},
	{"Light travels faster than {{c1::sound}}", ""},
	{"The capital of France is {{c1::Paris}}", ""},
	{"A leap year has {{c1::366}} days", ""},
	{"The plural of mouse is {{c1::mice}}", ""},
	{"Gravity pulls objects toward {{c1::Earth}}", ""},
	{"The freezing point of water is {{c1::0}} degrees Celsius", ""},
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
