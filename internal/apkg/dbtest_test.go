package apkg

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
	"github.com/Jolls/enshu/internal/media"
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

var seq atomic.Int64

func nextSeq() int64 { return seq.Add(1) }

func testMediaStore(t *testing.T) *media.Store {
	t.Helper()
	return media.New(t.TempDir())
}

func seedUser(t *testing.T, tx pgx.Tx) pgtype.UUID {
	t.Helper()
	var userID pgtype.UUID
	if err := tx.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Test') RETURNING id`,
		fmt.Sprintf("apkg-import-test-%d@example.com", nextSeq()),
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return userID
}
