package review

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/enshu/internal/db"
)

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed test")
	}
	poolOnce.Do(func() {
		p, err := db.NewPool(context.Background(), dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		pool = p
	})
	return pool
}

func beginTx(t *testing.T) pgx.Tx {
	t.Helper()
	p := testPool(t)
	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// fixture is one card's full graph: a user with can_view+can_study access to a deck holding one
// note (one field) of a one-template, non-cloze note type.
type fixture struct {
	UserID     pgtype.UUID
	DeckID     pgtype.UUID
	CardID     pgtype.UUID
	NoteTypeID pgtype.UUID
	TemplateID pgtype.UUID
}

func seedFixture(t *testing.T, tx pgx.Tx) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture

	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Test') RETURNING id`,
		fmt.Sprintf("review-test-%d@example.com", nextSeq()),
	).Scan(&f.UserID); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	if err := tx.QueryRow(ctx,
		`INSERT INTO note_types (owner_id, name) VALUES ($1, 'Basic') RETURNING id`,
		f.UserID,
	).Scan(&f.NoteTypeID); err != nil {
		t.Fatalf("insert note_type: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO fields (note_type_id, ordinal, name) VALUES ($1, 0, 'Front'), ($1, 1, 'Back')`,
		f.NoteTypeID,
	); err != nil {
		t.Fatalf("insert fields: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt) VALUES ($1, 0, 'Card 1', '{{Front}}', '{{FrontSide}}<hr>{{Back}}') RETURNING id`,
		f.NoteTypeID,
	).Scan(&f.TemplateID); err != nil {
		t.Fatalf("insert template: %v", err)
	}

	if err := tx.QueryRow(ctx,
		`INSERT INTO decks (owner_id, name) VALUES ($1, 'D') RETURNING id`,
		f.UserID,
	).Scan(&f.DeckID); err != nil {
		t.Fatalf("insert deck: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content, can_edit_settings, can_manage_access, can_delete)
		 VALUES ($1, $2, true, true, true, true, true, true)`,
		f.DeckID, f.UserID,
	); err != nil {
		t.Fatalf("insert deck_access: %v", err)
	}

	var noteID pgtype.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, checksum) VALUES ($1, $2, $3, $4, '["Q","A"]'::jsonb, 1) RETURNING id`,
		fmt.Sprintf("guid-%d", nextSeq()), f.UserID, f.NoteTypeID, f.DeckID,
	).Scan(&noteID); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO cards (note_id, template_id, ordinal, deck_id) VALUES ($1, $2, 0, $3) RETURNING id`,
		noteID, f.TemplateID, f.DeckID,
	).Scan(&f.CardID); err != nil {
		t.Fatalf("insert card: %v", err)
	}

	return f
}

var seq atomic.Int64

func nextSeq() int64 { return seq.Add(1) }

// seedCards adds n further never-seen cards to f's deck, sharing its note type and template.
// Returns their ids in insertion order (#101 batch_test.go).
func seedCards(t *testing.T, tx pgx.Tx, f fixture, n int) []pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	ids := make([]pgtype.UUID, n)
	for i := range ids {
		var noteID pgtype.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, checksum) VALUES ($1, $2, $3, $4, '["Q","A"]'::jsonb, 1) RETURNING id`,
			fmt.Sprintf("guid-%d", nextSeq()), f.UserID, f.NoteTypeID, f.DeckID,
		).Scan(&noteID); err != nil {
			t.Fatalf("insert note: %v", err)
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO cards (note_id, template_id, ordinal, deck_id) VALUES ($1, $2, 0, $3) RETURNING id`,
			noteID, f.TemplateID, f.DeckID,
		).Scan(&ids[i]); err != nil {
			t.Fatalf("insert card: %v", err)
		}
	}
	return ids
}

// setImportDuePosition stands in for what apkg import writes (internal/apkg/dbwrite.go) -- a
// direct update since batch_test.go's fixtures are hand-seeded, not imported (#82).
func setImportDuePosition(t *testing.T, tx pgx.Tx, cardID pgtype.UUID, position int32) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`UPDATE cards SET import_due_position = $2 WHERE id = $1`, cardID, position,
	); err != nil {
		t.Fatalf("set import_due_position: %v", err)
	}
}

func getUserCardState(t *testing.T, tx pgx.Tx, userID, cardID pgtype.UUID) db.UserCardState {
	t.Helper()
	row, err := db.New(tx).GetUserCardState(context.Background(), db.GetUserCardStateParams{UserID: userID, CardID: cardID})
	if err != nil {
		t.Fatalf("GetUserCardState: %v", err)
	}
	return row
}
