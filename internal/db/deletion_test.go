package db

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deletion policy tests (#51, docs/plans/51-deletion-policy.md §6). DB-backed: skipped unless
// DATABASE_URL is set. Every test runs inside a pgx.Tx that is always rolled back, so tests
// neither pollute the dev database nor depend on each other. One pgxpool for the package.

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	seq      atomic.Int64
)

func nextSeq() int64 {
	return seq.Add(1)
}

// testPool lazily opens the package-wide pool on first use, or skips the calling test if
// DATABASE_URL is not set.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed test")
	}
	poolOnce.Do(func() {
		p, err := NewPool(context.Background(), dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		pool = p
	})
	return pool
}

// beginTx opens a transaction that is always rolled back at test cleanup.
func beginTx(t *testing.T) pgx.Tx {
	t.Helper()
	p := testPool(t)
	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return tx
}

// beginSavepoint opens a nested transaction (savepoint) that the caller must explicitly roll
// back or commit into the parent tx. Used where a test needs to observe state after a rollback
// that is narrower than the whole test's transaction (e.g. §0.6's mutate-then-check guard).
func beginSavepoint(t *testing.T, tx pgx.Tx) pgx.Tx {
	t.Helper()
	nested, err := tx.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin savepoint: %v", err)
	}
	return nested
}

func randomUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if _, err := rand.Read(u.Bytes[:]); err != nil {
		t.Fatalf("random uuid: %v", err)
	}
	u.Valid = true
	return u
}

// --- fixtures -------------------------------------------------------------------------------

func mustUser(t *testing.T, tx pgx.Tx) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	email := fmt.Sprintf("test-%d@example.com", nextSeq())
	err := tx.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Test User') RETURNING id`,
		email,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func mustDeck(t *testing.T, tx pgx.Tx, ownerID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	name := fmt.Sprintf("Deck %d", nextSeq())
	err := tx.QueryRow(context.Background(),
		`INSERT INTO decks (owner_id, name) VALUES ($1, $2) RETURNING id`,
		ownerID, name,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert deck: %v", err)
	}
	return id
}

func mustNoteType(t *testing.T, tx pgx.Tx, ownerID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	name := fmt.Sprintf("NoteType %d", nextSeq())
	err := tx.QueryRow(context.Background(),
		`INSERT INTO note_types (owner_id, name) VALUES ($1, $2) RETURNING id`,
		ownerID, name,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert note_type: %v", err)
	}
	return id
}

func mustTemplate(t *testing.T, tx pgx.Tx, noteTypeID pgtype.UUID, ordinal int) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := tx.QueryRow(context.Background(),
		`INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt) VALUES ($1, $2, 'Card', '{{Front}}', '{{Back}}') RETURNING id`,
		noteTypeID, ordinal,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert template: %v", err)
	}
	return id
}

func mustField(t *testing.T, tx pgx.Tx, noteTypeID pgtype.UUID, ordinal int, name string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := tx.QueryRow(context.Background(),
		`INSERT INTO fields (note_type_id, ordinal, name) VALUES ($1, $2, $3) RETURNING id`,
		noteTypeID, ordinal, name,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert field: %v", err)
	}
	return id
}

// mustNoteWithFields is mustNote with an explicit fields array, for tests that need specific
// field content to assert a remap's positional rewrite (#89).
func mustNoteWithFields(t *testing.T, tx pgx.Tx, ownerID, noteTypeID, deckID pgtype.UUID, fields []string) pgtype.UUID {
	t.Helper()
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	var id pgtype.UUID
	guid := fmt.Sprintf("guid-%d", nextSeq())
	err = tx.QueryRow(context.Background(),
		`INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, checksum) VALUES ($1, $2, $3, $4, $5, 0) RETURNING id`,
		guid, ownerID, noteTypeID, deckID, fieldsJSON,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert note: %v", err)
	}
	return id
}

// noteFields reads back a note's fields jsonb array for assertion.
func noteFields(t *testing.T, tx pgx.Tx, noteID pgtype.UUID) []string {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(context.Background(), `SELECT fields FROM notes WHERE id = $1`, noteID).Scan(&raw); err != nil {
		t.Fatalf("select fields: %v", err)
	}
	var vals []string
	if err := json.Unmarshal(raw, &vals); err != nil {
		t.Fatalf("unmarshal fields: %v", err)
	}
	return vals
}

func mustNote(t *testing.T, tx pgx.Tx, ownerID, noteTypeID, deckID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	guid := fmt.Sprintf("guid-%d", nextSeq())
	err := tx.QueryRow(context.Background(),
		`INSERT INTO notes (guid, owner_id, note_type_id, deck_id, checksum) VALUES ($1, $2, $3, $4, 0) RETURNING id`,
		guid, ownerID, noteTypeID, deckID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert note: %v", err)
	}
	return id
}

func mustCard(t *testing.T, tx pgx.Tx, noteID, templateID, deckID pgtype.UUID, ordinal int) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := tx.QueryRow(context.Background(),
		`INSERT INTO cards (note_id, template_id, ordinal, deck_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		noteID, templateID, ordinal, deckID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert card: %v", err)
	}
	return id
}

func mustUserCardState(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID) {
	t.Helper()
	_, err := tx.Exec(context.Background(),
		`INSERT INTO user_card_state (user_id, card_id, due) VALUES ($1, $2, now())`,
		userID, cardID,
	)
	if err != nil {
		t.Fatalf("insert user_card_state: %v", err)
	}
}

func mustReviewLog(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := tx.QueryRow(context.Background(),
		`INSERT INTO review_log
			(user_id, card_id, rating, reviewed_at, state_before, learning_steps_before,
			 elapsed_days_before, scheduled_days_after, review_kind)
		 VALUES ($1, $2, 3, now(), 0, 0, 0, 0, 0)
		 RETURNING id`,
		userID, cardID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert review_log: %v", err)
	}
	return id
}

type accessFlags struct {
	canView, canStudy, canEditContent, canEditSettings, canManageAccess, canDelete bool
}

func fullAccess() accessFlags {
	return accessFlags{true, true, true, true, true, true}
}

func mustDeckAccess(t *testing.T, tx pgx.Tx, deckID, userID pgtype.UUID, f accessFlags) {
	t.Helper()
	_, err := tx.Exec(context.Background(),
		`INSERT INTO deck_access
			(deck_id, user_id, can_view, can_study, can_edit_content, can_edit_settings,
			 can_manage_access, can_delete)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		deckID, userID, f.canView, f.canStudy, f.canEditContent, f.canEditSettings,
		f.canManageAccess, f.canDelete,
	)
	if err != nil {
		t.Fatalf("insert deck_access: %v", err)
	}
}

func mustUserFsrsParams(t *testing.T, tx pgx.Tx, userID pgtype.UUID, deckID *pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	var arg any
	if deckID != nil {
		arg = *deckID
	}
	err := tx.QueryRow(context.Background(),
		`INSERT INTO user_fsrs_params (user_id, deck_id, fsrs_version, desired_retention)
		 VALUES ($1, $2, 6, 0.9) RETURNING id`,
		userID, arg,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert user_fsrs_params: %v", err)
	}
	return id
}

func mustMediaBlob(t *testing.T, tx pgx.Tx, sha string) {
	t.Helper()
	_, err := tx.Exec(context.Background(),
		`INSERT INTO media_blobs (sha256, size_bytes, mime) VALUES ($1, 100, 'image/png')`,
		sha,
	)
	if err != nil {
		t.Fatalf("insert media_blobs: %v", err)
	}
}

func mustMediaRef(t *testing.T, tx pgx.Tx, deckID pgtype.UUID, filename, sha string) {
	t.Helper()
	_, err := tx.Exec(context.Background(),
		`INSERT INTO media_refs (deck_id, filename, sha256) VALUES ($1, $2, $3)`,
		deckID, filename, sha,
	)
	if err != nil {
		t.Fatalf("insert media_refs: %v", err)
	}
}

func mustSession(t *testing.T, tx pgx.Tx, userID pgtype.UUID) string {
	t.Helper()
	id := fmt.Sprintf("session-%d", nextSeq())
	_, err := tx.Exec(context.Background(),
		`INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, now() + interval '1 day')`,
		id, userID,
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

// --- assertion helpers ------------------------------------------------------------------------

func countRows(t *testing.T, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

func rowExists(t *testing.T, tx pgx.Tx, table string, id pgtype.UUID) bool {
	t.Helper()
	return countRows(t, tx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id = $1`, table), id) > 0
}

// isForeignKeyViolation matches both Postgres FK-violation SQLSTATEs: 23503 (foreign_key_violation,
// e.g. inserting/updating a row whose FK value has no parent) and 23001 (restrict_violation, the
// code Postgres actually raises for an ON DELETE RESTRICT block on the parent side).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23503" || pgErr.Code == "23001"
}

// --- tests --------------------------------------------------------------------------------

// Case 1: Deck delete, plain.
func TestDeleteDeck_Plain(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)
	card := mustCard(t, tx, note, template, deck, 0)
	mustUserCardState(t, tx, user, card)

	if err := DeleteDeck(ctx, tx, deck, user); err != nil {
		t.Fatalf("DeleteDeck: %v", err)
	}

	if rowExists(t, tx, "decks", deck) {
		t.Error("deck still exists")
	}
	if rowExists(t, tx, "cards", card) {
		t.Error("card still exists")
	}
	if rowExists(t, tx, "notes", note) {
		t.Error("note still exists")
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, card) != 0 {
		t.Error("user_card_state row still exists")
	}
}

// Case 2: Deck delete, note with a card in another deck.
func TestDeleteDeck_NoteWithCardElsewhere(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	d1 := mustDeck(t, tx, user)
	d2 := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, d1, user, fullAccess())
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, d1)
	c1 := mustCard(t, tx, note, template, d1, 0)
	c2 := mustCard(t, tx, note, template, d2, 1)

	if err := DeleteDeck(ctx, tx, d1, user); err != nil {
		t.Fatalf("DeleteDeck: %v", err)
	}

	if !rowExists(t, tx, "notes", note) {
		t.Fatal("note should survive")
	}
	var deckID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT deck_id FROM notes WHERE id = $1`, note).Scan(&deckID); err != nil {
		t.Fatalf("select deck_id: %v", err)
	}
	if deckID != d2 {
		t.Errorf("note.deck_id = %v, want %v", deckID, d2)
	}
	if !rowExists(t, tx, "cards", c2) {
		t.Error("card in surviving deck should survive")
	}
	if rowExists(t, tx, "cards", c1) {
		t.Error("card in deleted deck should be gone")
	}
}

// Re-homing a note off a deleted deck must sync notes.owner_id to the new home deck's owner,
// not just deck_id -- schema.md documents owner_id as denormalised from decks.owner_id and
// requires it not to drift on any deck move, and a re-home is exactly that kind of move.
func TestDeleteDeck_RehomeSyncsOwnerID(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	owner1 := mustUser(t, tx)
	owner2 := mustUser(t, tx)
	d1 := mustDeck(t, tx, owner1) // deleted; note is homed here
	d2 := mustDeck(t, tx, owner2) // surviving deck, different owner
	mustDeckAccess(t, tx, d1, owner1, fullAccess())
	noteType := mustNoteType(t, tx, owner1)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, owner1, noteType, d1)
	mustCard(t, tx, note, template, d1, 0)
	mustCard(t, tx, note, template, d2, 1)

	if err := DeleteDeck(ctx, tx, d1, owner1); err != nil {
		t.Fatalf("DeleteDeck: %v", err)
	}

	var deckID, ownerID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT deck_id, owner_id FROM notes WHERE id = $1`, note).Scan(&deckID, &ownerID); err != nil {
		t.Fatalf("select note: %v", err)
	}
	if deckID != d2 {
		t.Errorf("note.deck_id = %v, want %v", deckID, d2)
	}
	if ownerID != owner2 {
		t.Errorf("note.owner_id = %v, want %v (d2's owner)", ownerID, owner2)
	}
}

// Case 3: Deck delete, note homed elsewhere with all cards in the deleted deck.
func TestDeleteDeck_NoteHomedElsewhereAllCardsInDeletedDeck(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	d1 := mustDeck(t, tx, user) // deleted
	d2 := mustDeck(t, tx, user) // note's home deck
	mustDeckAccess(t, tx, d1, user, fullAccess())
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, d2)
	mustCard(t, tx, note, template, d1, 0)

	if err := DeleteDeck(ctx, tx, d1, user); err != nil {
		t.Fatalf("DeleteDeck: %v", err)
	}

	if rowExists(t, tx, "notes", note) {
		t.Error("note homed elsewhere with all cards in the deleted deck should be deleted")
	}
}

// Case 4: Deck delete, note homed in the deck with no cards at all.
func TestDeleteDeck_NoteHomedInDeckNoCards(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())
	noteType := mustNoteType(t, tx, user)
	note := mustNote(t, tx, user, noteType, deck)

	if err := DeleteDeck(ctx, tx, deck, user); err != nil {
		t.Fatalf("DeleteDeck: %v", err)
	}

	if rowExists(t, tx, "notes", note) {
		t.Error("card-less note homed in the deleted deck should be deleted")
	}
}

// Case 5: Deck delete with review history -- the crux of §0.4.
func TestDeleteDeck_ReviewHistorySurvives(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)
	card := mustCard(t, tx, note, template, deck, 0)
	reviewLog := mustReviewLog(t, tx, user, card)

	before := countRows(t, tx, `SELECT count(*) FROM review_log`)

	if err := DeleteDeck(ctx, tx, deck, user); err != nil {
		t.Fatalf("DeleteDeck: %v", err)
	}

	after := countRows(t, tx, `SELECT count(*) FROM review_log`)
	if after != before {
		t.Errorf("review_log row count changed: before=%d after=%d", before, after)
	}
	var gotCardID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT card_id FROM review_log WHERE id = $1`, reviewLog).Scan(&gotCardID); err != nil {
		t.Fatalf("select review_log.card_id: %v", err)
	}
	if gotCardID != card {
		t.Errorf("review_log.card_id changed: got %v, want %v", gotCardID, card)
	}
	if rowExists(t, tx, "decks", deck) {
		t.Error("deck should be gone")
	}
	if rowExists(t, tx, "cards", card) {
		t.Error("card should be gone")
	}
}

// Case 6: Deck delete, fan-out.
func TestDeleteDeck_FanOut(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	other := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())
	mustDeckAccess(t, tx, deck, other, accessFlags{canView: true})

	deckScoped := mustUserFsrsParams(t, tx, user, &deck)
	global := mustUserFsrsParams(t, tx, user, nil)

	sha := fmt.Sprintf("%064d", nextSeq())
	mustMediaBlob(t, tx, sha)
	mustMediaRef(t, tx, deck, "image.png", sha)

	if err := DeleteDeck(ctx, tx, deck, user); err != nil {
		t.Fatalf("DeleteDeck: %v", err)
	}

	if countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1`, deck) != 0 {
		t.Error("deck_access rows should be gone")
	}
	if rowExists(t, tx, "user_fsrs_params", deckScoped) {
		t.Error("per-deck user_fsrs_params row should be gone")
	}
	if !rowExists(t, tx, "user_fsrs_params", global) {
		t.Error("global user_fsrs_params row should survive")
	}
	if countRows(t, tx, `SELECT count(*) FROM media_refs WHERE deck_id = $1`, deck) != 0 {
		t.Error("media_refs rows should be gone")
	}
	if countRows(t, tx, `SELECT count(*) FROM media_blobs WHERE sha256 = $1`, sha) != 1 {
		t.Error("media_blobs row should survive (§0.8)")
	}
}

// Case 7: Deck delete denied -- no can_delete, no deck_access row, nonexistent deck id.
func TestDeleteDeck_Denied(t *testing.T) {
	t.Run("no can_delete", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		deck := mustDeck(t, tx, user)
		mustDeckAccess(t, tx, deck, user, accessFlags{canView: true, canStudy: true})

		err := DeleteDeck(ctx, tx, deck, user)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("DeleteDeck error = %v, want pgx.ErrNoRows", err)
		}
		if !rowExists(t, tx, "decks", deck) {
			t.Error("deck should not have been deleted")
		}
	})

	t.Run("no deck_access row", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		deck := mustDeck(t, tx, user)

		err := DeleteDeck(ctx, tx, deck, user)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("DeleteDeck error = %v, want pgx.ErrNoRows", err)
		}
		if !rowExists(t, tx, "decks", deck) {
			t.Error("deck should not have been deleted")
		}
	})

	t.Run("nonexistent deck", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)

		err := DeleteDeck(ctx, tx, randomUUID(t), user)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("DeleteDeck error = %v, want pgx.ErrNoRows", err)
		}
	})
}

// Case 8: Bare DELETE FROM decks with a note homed in it -- the §0.2 #10 tripwire.
func TestBareDeckDelete_FailsOnNotesFK(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	mustNote(t, tx, user, noteType, deck)

	_, err := tx.Exec(ctx, `DELETE FROM decks WHERE id = $1`, deck)
	if !isForeignKeyViolation(err) {
		t.Fatalf("bare deck delete error = %v, want a foreign_key_violation on notes_deck_id_fkey", err)
	}
}

// Case 9: Note delete -- cards and user_card_state cascade, review_log survives.
func TestNoteDelete_CascadesCardsNotReviewLog(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)
	card := mustCard(t, tx, note, template, deck, 0)
	mustUserCardState(t, tx, user, card)
	reviewLog := mustReviewLog(t, tx, user, card)

	if _, err := tx.Exec(ctx, `DELETE FROM notes WHERE id = $1`, note); err != nil {
		t.Fatalf("delete note: %v", err)
	}

	if rowExists(t, tx, "cards", card) {
		t.Error("card should cascade away")
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, card) != 0 {
		t.Error("user_card_state should cascade away")
	}
	if !rowExists(t, tx, "review_log", reviewLog) {
		t.Error("review_log row should survive a note delete")
	}
}

// Case 10: User delete blocked by every referencing table (§0.3).
func TestUserDelete_Blocked(t *testing.T) {
	t.Run("owns a deck", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		mustDeck(t, tx, user)

		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, user)
		if !isForeignKeyViolation(err) {
			t.Fatalf("error = %v, want foreign_key_violation", err)
		}
	})

	t.Run("owns a note", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		deck := mustDeck(t, tx, user)
		noteType := mustNoteType(t, tx, user)
		mustNote(t, tx, user, noteType, deck)

		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, user)
		if !isForeignKeyViolation(err) {
			t.Fatalf("error = %v, want foreign_key_violation", err)
		}
	})

	t.Run("owns a note type", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		mustNoteType(t, tx, user)

		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, user)
		if !isForeignKeyViolation(err) {
			t.Fatalf("error = %v, want foreign_key_violation", err)
		}
	})

	t.Run("has an access row", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		owner := mustUser(t, tx)
		user := mustUser(t, tx)
		deck := mustDeck(t, tx, owner)
		mustDeckAccess(t, tx, deck, user, accessFlags{canView: true})

		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, user)
		if !isForeignKeyViolation(err) {
			t.Fatalf("error = %v, want foreign_key_violation", err)
		}
	})

	t.Run("has review history", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		deck := mustDeck(t, tx, user)
		noteType := mustNoteType(t, tx, user)
		template := mustTemplate(t, tx, noteType, 0)
		note := mustNote(t, tx, user, noteType, deck)
		card := mustCard(t, tx, note, template, deck, 0)
		mustReviewLog(t, tx, user, card)

		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, user)
		if !isForeignKeyViolation(err) {
			t.Fatalf("error = %v, want foreign_key_violation", err)
		}
	})
}

// Case 11: User delete with only a session -- succeeds, and the session cascades.
func TestUserDelete_OnlySessionSucceeds(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	sessionID := mustSession(t, tx, user)

	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if countRows(t, tx, `SELECT count(*) FROM sessions WHERE id = $1`, sessionID) != 0 {
		t.Error("session should cascade away with its user")
	}
}

// Case 12: Note-type delete -- blocked while a note references it; fields/templates cascade
// when no notes do.
func TestNoteTypeDelete(t *testing.T) {
	t.Run("blocked while a note references it", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		deck := mustDeck(t, tx, user)
		noteType := mustNoteType(t, tx, user)
		mustNote(t, tx, user, noteType, deck)

		_, err := tx.Exec(ctx, `DELETE FROM note_types WHERE id = $1`, noteType)
		if !isForeignKeyViolation(err) {
			t.Fatalf("error = %v, want foreign_key_violation", err)
		}
	})

	t.Run("fields and templates cascade with no notes", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		user := mustUser(t, tx)
		noteType := mustNoteType(t, tx, user)
		template := mustTemplate(t, tx, noteType, 0)
		var field pgtype.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO fields (note_type_id, ordinal, name) VALUES ($1, 0, 'Front') RETURNING id`,
			noteType,
		).Scan(&field); err != nil {
			t.Fatalf("insert field: %v", err)
		}

		if _, err := tx.Exec(ctx, `DELETE FROM note_types WHERE id = $1`, noteType); err != nil {
			t.Fatalf("delete note_type: %v", err)
		}
		if rowExists(t, tx, "templates", template) {
			t.Error("template should cascade away")
		}
		if rowExists(t, tx, "fields", field) {
			t.Error("field should cascade away")
		}
	})
}

// Case 13: Revoke the only can_manage_access holder -> ErrLastAccessHolder; row survives rollback.
func TestRevokeDeckAccess_OnlyManageAccessHolder(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())

	nested := beginSavepoint(t, tx)
	err := RevokeDeckAccess(ctx, nested, deck, user, user)
	if !errors.Is(err, ErrLastAccessHolder) {
		t.Fatalf("RevokeDeckAccess error = %v, want ErrLastAccessHolder", err)
	}
	if err := nested.Rollback(ctx); err != nil {
		t.Fatalf("rollback savepoint: %v", err)
	}

	if countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deck, user) != 1 {
		t.Error("deck_access row should still exist after rollback")
	}
}

// Case 14: Revoke a holder while a second can_manage_access AND can_delete holder exists -> succeeds.
func TestRevokeDeckAccess_SecondHolderExists(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user1 := mustUser(t, tx)
	user2 := mustUser(t, tx)
	deck := mustDeck(t, tx, user1)
	mustDeckAccess(t, tx, deck, user1, fullAccess())
	mustDeckAccess(t, tx, deck, user2, fullAccess())

	if err := RevokeDeckAccess(ctx, tx, deck, user1, user2); err != nil {
		t.Fatalf("RevokeDeckAccess: %v", err)
	}
	if countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deck, user2) != 0 {
		t.Error("user2's deck_access row should be gone")
	}
}

// Case 15: Revoke the only can_delete holder while another user holds can_manage_access ->
// ErrLastAccessHolder -- both counts are checked, not just one.
func TestRevokeDeckAccess_OnlyDeleteHolder(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user1 := mustUser(t, tx)
	user2 := mustUser(t, tx)
	deck := mustDeck(t, tx, user1)
	mustDeckAccess(t, tx, deck, user1, accessFlags{canView: true, canManageAccess: true})
	mustDeckAccess(t, tx, deck, user2, accessFlags{canView: true, canManageAccess: true, canDelete: true})

	nested := beginSavepoint(t, tx)
	err := RevokeDeckAccess(ctx, nested, deck, user1, user2)
	if !errors.Is(err, ErrLastAccessHolder) {
		t.Fatalf("RevokeDeckAccess error = %v, want ErrLastAccessHolder", err)
	}
	if err := nested.Rollback(ctx); err != nil {
		t.Fatalf("rollback savepoint: %v", err)
	}

	if countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deck, user2) != 1 {
		t.Error("user2's deck_access row should still exist after rollback")
	}
}

// Case 16: SetDeckAccess clearing can_manage_access on the last holder -> ErrLastAccessHolder.
func TestSetDeckAccess_DowngradeLastManageAccessHolder(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())

	nested := beginSavepoint(t, tx)
	err := SetDeckAccess(ctx, nested, user, UpdateDeckAccessRowParams{
		DeckID:          deck,
		TargetUserID:    user,
		CanView:         true,
		CanStudy:        true,
		CanEditContent:  true,
		CanEditSettings: true,
		CanManageAccess: false,
		CanDelete:       true,
	})
	if !errors.Is(err, ErrLastAccessHolder) {
		t.Fatalf("SetDeckAccess error = %v, want ErrLastAccessHolder", err)
	}
	if err := nested.Rollback(ctx); err != nil {
		t.Fatalf("rollback savepoint: %v", err)
	}

	var canManageAccess bool
	if err := tx.QueryRow(ctx,
		`SELECT can_manage_access FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deck, user,
	).Scan(&canManageAccess); err != nil {
		t.Fatalf("select can_manage_access: %v", err)
	}
	if !canManageAccess {
		t.Error("can_manage_access should be unchanged after rollback")
	}
}

// Case 17: Access change by a caller without can_manage_access -> pgx.ErrNoRows; nothing changed.
func TestSetDeckAccess_CallerLacksManageAccess(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	caller := mustUser(t, tx)
	target := mustUser(t, tx)
	deck := mustDeck(t, tx, caller)
	mustDeckAccess(t, tx, deck, caller, accessFlags{canView: true})
	mustDeckAccess(t, tx, deck, target, fullAccess())

	err := RevokeDeckAccess(ctx, tx, deck, caller, target)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RevokeDeckAccess error = %v, want pgx.ErrNoRows", err)
	}
	if countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deck, target) != 1 {
		t.Error("target's deck_access row should be unchanged")
	}
}
