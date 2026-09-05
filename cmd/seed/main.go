// Command seed populates a freshly-migrated local database with a "messy classroom" (#206):
// two teachers with overlapping rosters, a deck they co-own, and decks owned outside any
// teacher's control -- so multiuser access control has something realistic to exercise instead
// of a single owner's decks. Signup already seeds each user's own Basic/Cloze note types
// (internal/auth/notetypes.go); this adds decks, notes, and deck_access grants on top:
//
//   - Teacher A has students C & D; Teacher B has students D & E. Student D is the overlap;
//     Student E has no deck of their own.
//   - "Shared Classroom" is owned by Teacher A, with Teacher B granted full deck_access
//     (co-owner in every respect that matters -- can_edit_content, can_manage_access,
//     can_delete, can_view_progress) and Students C, D, E granted can_view+can_study (the
//     union of both teachers' rosters). Teacher B's can_edit_content here, on a note type
//     ("Basic") Teacher B does not own, is the #192 note-type-authority demo: WRITABLE via
//     deck access, not ownership. It also carries the #87 instructor-dashboard demo: two
//     instructors, four reviewers' due counts and lapse-hotspot history.
//   - "Teacher A Solo" is owned by Teacher A alone, using the same "Basic" note type as Shared
//     Classroom -- invisible to every student. A student's view+study-only grant on Shared
//     Classroom makes "Basic" a named, visible-but-not-editable blocker, while Teacher A Solo
//     is an invisible, counted-only one: #192's denial reason naming one blocking deck and
//     counting another in the same message.
//   - "Student C Personal" and "Student D Personal" are owned outright by their students, no
//     grants to anyone -- the no-cross-user-reads invariant (CLAUDE.md §2) has fixture data to
//     violate if it ever regresses. Cloze/Basic respectively, so both note types stay in play.
//   - Card flags (#207) on each side of the sharing seam, one open and one already resolved per
//     side: Students D and E report on Shared Classroom, where the reader holds can_view_flags
//     (Teacher A as creator, Teacher B via the co-owner grant) and is never the reporter, while
//     Student C reports on their own personal deck, where reporter and reader are the same
//     person and no grant is involved.
//
// Safe to re-run: an already-seeded deck (non-zero card count) is left alone, and an existing
// access grant or note type is left alone too.
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
	teacherAEmail       = "teachera@ds.com"
	teacherADisplayName = "Teacher A"
	teacherBEmail       = "teacherb@ds.com"
	teacherBDisplayName = "Teacher B"
	studentCEmail       = "studentc@ds.com"
	studentCDisplayName = "Student C"
	studentDEmail       = "studentd@ds.com"
	studentDDisplayName = "Student D"
	studentEEmail       = "studente@ds.com"
	studentEDisplayName = "Student E"
	seedPassword        = "password"

	sharedClassroomDeckName = "Shared Classroom"
	teacherASoloDeckName    = "Teacher A Solo"
	studentCDeckName        = "Student C Personal"
	studentDDeckName        = "Student D Personal"

	// Small caps so a fresh seed exercises #172's daily-cap / "Keep studying" path without
	// requiring dozens of reviews first.
	seedNewPerDay = 2
	seedRevPerDay = 5

	// seedDueCardCount is how many of each freshly-seeded deck's cards start already in Review
	// state and due, rather than New.
	seedDueCardCount = 2

	// baseCardCSS/clozeBoldCSS: signup seeds each user's Basic/Cloze note types with empty CSS
	// (internal/auth/notetypes.go), so a reseed has nothing to visually check note-type CSS
	// against (e.g. #194: the reviewer's visible card missing the deckshare-card scope class,
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

	teacherA, err := ensureUser(ctx, pool, authSvc, teacherAEmail, seedPassword, teacherADisplayName)
	if err != nil {
		return fmt.Errorf("ensure teacher A: %w", err)
	}
	teacherB, err := ensureUser(ctx, pool, authSvc, teacherBEmail, seedPassword, teacherBDisplayName)
	if err != nil {
		return fmt.Errorf("ensure teacher B: %w", err)
	}
	studentC, err := ensureUser(ctx, pool, authSvc, studentCEmail, seedPassword, studentCDisplayName)
	if err != nil {
		return fmt.Errorf("ensure student C: %w", err)
	}
	studentD, err := ensureUser(ctx, pool, authSvc, studentDEmail, seedPassword, studentDDisplayName)
	if err != nil {
		return fmt.Errorf("ensure student D: %w", err)
	}
	studentE, err := ensureUser(ctx, pool, authSvc, studentEEmail, seedPassword, studentEDisplayName)
	if err != nil {
		return fmt.Errorf("ensure student E: %w", err)
	}

	if !teacherA.AvatarSha256.Valid {
		if err := ensureAvatar(ctx, pool, teacherA.ID); err != nil {
			return fmt.Errorf("ensure teacher A avatar: %w", err)
		}
	} else {
		log.Print("teacher A already has an avatar, skipping avatar seeding")
	}

	for _, name := range []string{sharedClassroomDeckName, teacherASoloDeckName} {
		if err := ensureDeck(ctx, pool, teacherA.ID, name); err != nil {
			return fmt.Errorf("ensure deck %q: %w", name, err)
		}
	}
	if err := ensureDeck(ctx, pool, studentC.ID, studentCDeckName); err != nil {
		return fmt.Errorf("ensure deck %q: %w", studentCDeckName, err)
	}
	if err := ensureDeck(ctx, pool, studentD.ID, studentDDeckName); err != nil {
		return fmt.Errorf("ensure deck %q: %w", studentDDeckName, err)
	}

	q := db.New(pool)

	teacherADecks, err := q.ListDecksForUser(ctx, teacherA.ID)
	if err != nil {
		return fmt.Errorf("list teacher A decks: %w", err)
	}
	sharedClassroomDeck, ok := findDeck(teacherADecks, sharedClassroomDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", sharedClassroomDeckName)
	}
	teacherASoloDeck, ok := findDeck(teacherADecks, teacherASoloDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", teacherASoloDeckName)
	}

	studentCDecks, err := q.ListDecksForUser(ctx, studentC.ID)
	if err != nil {
		return fmt.Errorf("list student C decks: %w", err)
	}
	studentCDeck, ok := findDeck(studentCDecks, studentCDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", studentCDeckName)
	}

	studentDDecks, err := q.ListDecksForUser(ctx, studentD.ID)
	if err != nil {
		return fmt.Errorf("list student D decks: %w", err)
	}
	studentDDeck, ok := findDeck(studentDDecks, studentDDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", studentDDeckName)
	}

	teacherANoteTypes, err := q.ListNoteTypesForUser(ctx, teacherA.ID)
	if err != nil {
		return fmt.Errorf("list teacher A note types: %w", err)
	}
	teacherABasic, ok := findNoteType(teacherANoteTypes, "Basic")
	if !ok {
		return errors.New(`note type "Basic" not found for teacher A -- signup seeding may have failed`)
	}
	if err := ensureNoteTypeCSS(ctx, q, teacherABasic, baseCardCSS); err != nil {
		return fmt.Errorf("ensure teacher A Basic note type css: %w", err)
	}

	studentCNoteTypes, err := q.ListNoteTypesForUser(ctx, studentC.ID)
	if err != nil {
		return fmt.Errorf("list student C note types: %w", err)
	}
	studentCCloze, ok := findNoteType(studentCNoteTypes, "Cloze")
	if !ok {
		return errors.New(`note type "Cloze" not found for student C -- signup seeding may have failed`)
	}
	if err := ensureNoteTypeCSS(ctx, q, studentCCloze, baseCardCSS+clozeBoldCSS); err != nil {
		return fmt.Errorf("ensure student C Cloze note type css: %w", err)
	}

	studentDNoteTypes, err := q.ListNoteTypesForUser(ctx, studentD.ID)
	if err != nil {
		return fmt.Errorf("list student D note types: %w", err)
	}
	studentDBasic, ok := findNoteType(studentDNoteTypes, "Basic")
	if !ok {
		return errors.New(`note type "Basic" not found for student D -- signup seeding may have failed`)
	}
	if err := ensureNoteTypeCSS(ctx, q, studentDBasic, baseCardCSS); err != nil {
		return fmt.Errorf("ensure student D Basic note type css: %w", err)
	}

	if sharedClassroomDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, teacherA.ID, sharedClassroomDeck.ID, teacherABasic.ID, "Basic", basicSamples); err != nil {
			return fmt.Errorf("seed shared classroom notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", sharedClassroomDeckName)
		if err := seedDueCards(ctx, pool, teacherA.ID, sharedClassroomDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards in %s: %w", sharedClassroomDeckName, err)
		}
	} else {
		log.Printf("%s already has cards, skipping note seeding", sharedClassroomDeckName)
	}

	if teacherASoloDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, teacherA.ID, teacherASoloDeck.ID, teacherABasic.ID, "Basic", basicSamples); err != nil {
			return fmt.Errorf("seed teacher A solo notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", teacherASoloDeckName)
		if err := seedDueCards(ctx, pool, teacherA.ID, teacherASoloDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards in %s: %w", teacherASoloDeckName, err)
		}
	} else {
		log.Printf("%s already has cards, skipping note seeding", teacherASoloDeckName)
	}

	if studentCDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, studentC.ID, studentCDeck.ID, studentCCloze.ID, "Cloze", clozeSamples); err != nil {
			return fmt.Errorf("seed student C notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", studentCDeckName)
		if err := seedDueCards(ctx, pool, studentC.ID, studentCDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards in %s: %w", studentCDeckName, err)
		}
	} else {
		log.Printf("%s already has cards, skipping note seeding", studentCDeckName)
	}

	if studentDDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, studentD.ID, studentDDeck.ID, studentDBasic.ID, "Basic", basicSamples); err != nil {
			return fmt.Errorf("seed student D notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", studentDDeckName)
		if err := seedDueCards(ctx, pool, studentD.ID, studentDDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards in %s: %w", studentDDeckName, err)
		}
	} else {
		log.Printf("%s already has cards, skipping note seeding", studentDDeckName)
	}

	// Students, too, so the instructor dashboard's (#87) Due column isn't owner-only. Each call
	// below has its own idempotency guard (seedDueCards' ON CONFLICT, seedLapseHotspotReviews'
	// own existence check), so -- unlike the notes above -- these run on every seed invocation,
	// not just the first: this package's doc comment promises the whole script is safe to
	// re-run, and Shared Classroom may already have had cards on this dev database before these
	// students existed here.
	for _, student := range []db.User{studentC, studentD, studentE} {
		if err := seedDueCards(ctx, pool, student.ID, sharedClassroomDeck.ID, seedDueCardCount); err != nil {
			return fmt.Errorf("seed due cards for student in %s: %w", sharedClassroomDeckName, err)
		}
	}
	if err := seedLapseHotspotReviews(ctx, pool, sharedClassroomDeck.ID,
		[]pgtype.UUID{teacherA.ID, studentC.ID, studentD.ID, studentE.ID}); err != nil {
		return fmt.Errorf("seed lapse hotspot reviews in %s: %w", sharedClassroomDeckName, err)
	}

	// Co-owner grant: Teacher B gets every deck_access flag Teacher A (the creator) has, making
	// Teacher B a co-owner in every respect that matters -- content, settings, access
	// management, deletion, progress visibility (#87, #206) and flag review (#207). Teacher B's
	// can_edit_content here, on "Basic" (which Teacher B does not own), is the #192
	// WRITABLE-via-access demo.
	if err := ensureDeckAccess(ctx, pool, db.GrantDeckAccessParams{
		DeckID: sharedClassroomDeck.ID, CallerUserID: teacherA.ID, TargetUserID: teacherB.ID,
		CanView: true, CanStudy: true, CanEditContent: true, CanEditSettings: true,
		CanManageAccess: true, CanDelete: true, CanViewProgress: true, CanViewFlags: true,
	}); err != nil {
		return fmt.Errorf("ensure teacher B co-owner access to %q: %w", sharedClassroomDeckName, err)
	}
	// Roster grants: the union of both teachers' students (A: C & D, B: D & E) can view+study
	// the shared deck, but not edit it.
	for _, student := range []db.User{studentC, studentD, studentE} {
		if err := ensureDeckAccess(ctx, pool, db.GrantDeckAccessParams{
			DeckID: sharedClassroomDeck.ID, CallerUserID: teacherA.ID, TargetUserID: student.ID,
			CanView: true, CanStudy: true,
		}); err != nil {
			return fmt.Errorf("ensure student access to %q: %w", sharedClassroomDeckName, err)
		}
	}

	// Flags on each side of the sharing seam (#207), one open and one already resolved per side
	// so both of the flags page's status tabs have rows. On the shared deck the reader (Teacher
	// A, or Teacher B via the co-owner grant above) is someone other than the reporter, and two
	// different students report, so the list is not single-reporter; on Student C's personal
	// deck reporter and reader are the same person and no grant is involved at all. Seeded after
	// the grants, so every flag is written by a student who already has can_study on the deck --
	// the same order the app enforces (GetCardForFlag, flags.sql).
	if err := ensureCardFlag(ctx, pool, sharedClassroomDeck.ID, studentD.ID, 1,
		"The answer here doesn't match what we covered in class -- can you double-check it?",
		pgtype.UUID{}); err != nil {
		return fmt.Errorf("ensure open flag in %s: %w", sharedClassroomDeckName, err)
	}
	if err := ensureCardFlag(ctx, pool, sharedClassroomDeck.ID, studentE.ID, 2,
		"Typo in the question -- it was missing a word.", teacherA.ID); err != nil {
		return fmt.Errorf("ensure resolved flag in %s: %w", sharedClassroomDeckName, err)
	}
	if err := ensureCardFlag(ctx, pool, studentCDeck.ID, studentC.ID, 1,
		"Reword this one -- the cloze deletion gives the answer away.",
		pgtype.UUID{}); err != nil {
		return fmt.Errorf("ensure open flag in %s: %w", studentCDeckName, err)
	}
	if err := ensureCardFlag(ctx, pool, studentCDeck.ID, studentC.ID, 2,
		"Too easy now -- rewrote it as two separate cards.", studentC.ID); err != nil {
		return fmt.Errorf("ensure resolved flag in %s: %w", studentCDeckName, err)
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
	log.Print("seeded avatar")
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

// seedLapseHotspotReviews records every one of reviewerIDs failing the shared deck's first due
// card, so the instructor dashboard (#87, GET /decks/{id}/progress) has a lapse hotspot to show
// without a manual review session: the first reviewer answers twice and the rest once each, so
// len(reviewerIDs)+1 "Again" answers clear the ≥5-review/≥3-student floor (internal/http/progress.go's
// hotspotMinReviews/hotspotMinStudents) as long as reviewerIDs has at least 3 distinct entries.
// review_log has no ON CONFLICT re-run guard the way seedDueCards' user_card_state upsert does
// (a client-generated id is what makes a real grade idempotent, and this seed data has none), so
// this checks for its own prior work instead and skips if found -- safe to call on every seed
// invocation, matching this package's "safe to re-run" promise. Timestamps ride seedDueCards'
// fixed seedDueLastReview date, so they stay inside the dashboard's rolling 30-day window for as
// long as that date does.
func seedLapseHotspotReviews(ctx context.Context, pool *pgxpool.Pool, deckID pgtype.UUID, reviewerIDs []pgtype.UUID) error {
	if len(reviewerIDs) < 3 {
		return fmt.Errorf("need at least 3 distinct reviewers to clear the lapse hotspot floor, got %d", len(reviewerIDs))
	}

	var cardID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM cards WHERE deck_id = $1 ORDER BY id LIMIT 1`, deckID).Scan(&cardID); err != nil {
		return fmt.Errorf("find hotspot card: %w", err)
	}
	var alreadySeeded bool
	if err := pool.QueryRow(ctx,
		`SELECT count(*) > 0 FROM review_log WHERE card_id = $1 AND user_id = $2`,
		cardID, reviewerIDs[len(reviewerIDs)-1],
	).Scan(&alreadySeeded); err != nil {
		return fmt.Errorf("check for existing hotspot reviews: %w", err)
	}
	if alreadySeeded {
		log.Print("lapse hotspot review history already seeded, skipping")
		return nil
	}

	type review struct {
		userID pgtype.UUID
		offset time.Duration
	}
	reviews := []review{{reviewerIDs[0], 0}, {reviewerIDs[0], time.Minute}}
	for _, id := range reviewerIDs[1:] {
		reviews = append(reviews, review{id, 0})
	}
	for _, rv := range reviews {
		if _, err := pool.Exec(ctx, `
			INSERT INTO review_log
				(user_id, card_id, rating, reviewed_at, state_before, learning_steps_before,
				 elapsed_days_before, scheduled_days_after, review_kind)
			VALUES ($1, $2, 1, $3, 2, 0, 7, 1, 0)`,
			rv.userID, cardID, seedDueLastReview.Add(rv.offset)); err != nil {
			return fmt.Errorf("seed hotspot review for user %s: %w", rv.userID.String(), err)
		}
	}
	log.Print("seeded lapse hotspot review history")
	return nil
}

// ensureCardFlag records one flag from flaggerID on the card at cardOffset within deckID, so a
// fresh seed has student feedback waiting on GET /decks/{id}/flags and a non-zero nav badge on
// the deck page (#207) without a manual review session. Cards are offset past the first, which
// is seedLapseHotspotReviews' hotspot; giving each flag its own card keeps the list readable and
// is what lets the open and resolved fixtures on one deck coexist, since the open-flag partial
// index (migration 00019) is per (card, user).
//
// A valid resolvedBy makes the flag resolved rather than open, populating the resolved filter's
// tab; it must be a user holding can_view_flags on the deck, since that is who the app would let
// press Resolve. Resolved fixtures are backdated to seedDueLastReview so they sort as history
// behind the open ones, on the same fixed-date reasoning as seedDueDate -- a relative timestamp
// would drift with each reseed.
//
// Skips if this (card, user) has ever been flagged, resolved flags included -- so re-running
// seed after resolving the seeded flag by hand during manual testing does not silently reopen
// it. Same self-check shape as seedLapseHotspotReviews, and for the same reason: card_flags'
// only uniqueness is that open-flag partial index, which by design says nothing about resolved
// rows.
func ensureCardFlag(ctx context.Context, pool *pgxpool.Pool, deckID, flaggerID pgtype.UUID, cardOffset int, comment string, resolvedBy pgtype.UUID) error {
	var cardID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM cards WHERE deck_id = $1 ORDER BY id OFFSET $2 LIMIT 1`, deckID, cardOffset,
	).Scan(&cardID); err != nil {
		return fmt.Errorf("find card to flag: %w", err)
	}

	status := "open"
	createdAt := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	var resolvedAt pgtype.Timestamptz
	if resolvedBy.Valid {
		status = "resolved"
		createdAt = pgtype.Timestamptz{Time: seedDueLastReview, Valid: true}
		resolvedAt = pgtype.Timestamptz{Time: seedDueLastReview.AddDate(0, 0, 1), Valid: true}
	}

	tag, err := pool.Exec(ctx, `
		INSERT INTO card_flags (card_id, deck_id, flagged_by_user_id, comment, status,
		                        created_at, resolved_at, resolved_by_user_id)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8
		WHERE NOT EXISTS (
			SELECT 1 FROM card_flags WHERE card_id = $1 AND flagged_by_user_id = $3
		)`, cardID, deckID, flaggerID, comment, status, createdAt, resolvedAt, resolvedBy)
	if err != nil {
		return fmt.Errorf("insert card flag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		log.Printf("%s card flag already seeded, skipping", status)
		return nil
	}
	log.Printf("seeded %s card flag", status)
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
func ensureNoteTypeCSS(ctx context.Context, q *db.Queries, nt db.ListNoteTypesForUserRow, wantCSS string) error {
	if nt.Css != "" {
		return nil
	}
	if _, err := q.UpdateNoteTypeRow(ctx, db.UpdateNoteTypeRowParams{
		Name: nt.Name, Css: wantCSS, SortFieldIdx: nt.SortFieldIdx, ID: nt.ID, UserID: nt.OwnerID,
	}); err != nil {
		return fmt.Errorf("update note type css: %w", err)
	}
	return nil
}

func findNoteType(noteTypes []db.ListNoteTypesForUserRow, name string) (db.ListNoteTypesForUserRow, bool) {
	for _, nt := range noteTypes {
		if nt.Name == name {
			return nt, true
		}
	}
	return db.ListNoteTypesForUserRow{}, false
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
