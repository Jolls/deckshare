package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/deckshare"
)

// migrationPool opens a pool whose search_path is a throwaway schema, so Migrate's tables and its
// goose_db_version bookkeeping all land there and the shared dev database is never touched or reset
// (CLAUDE.md §16). Skipped without DATABASE_URL.
func migrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := testPool(t)
	ctx := context.Background()

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	schema := "migtest_" + hex.EncodeToString(suffix[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	u, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	p, err := NewPool(ctx, u.String())
	if err != nil {
		t.Fatalf("open migration pool: %v", err)
	}
	t.Cleanup(func() {
		p.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	return p
}

// embeddedMigrationNames is the sorted .sql names of the embedded migrations.
func embeddedMigrationNames(t *testing.T) []string {
	t.Helper()
	names, err := fs.Glob(deckshare.Migrations(), "*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	slices.Sort(names)
	if len(names) == 0 {
		t.Fatal("no embedded migrations")
	}
	return names
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, query string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	return scalar[bool](t, pool, "SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL", name)
}

func TestMigrate_AppliesAllToFreshSchema(t *testing.T) {
	pool := migrationPool(t)
	names := embeddedMigrationNames(t)

	applied, err := Migrate(context.Background(), pool, deckshare.Migrations())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if applied != len(names) {
		t.Errorf("applied = %d, want %d", applied, len(names))
	}
	for _, table := range []string{"users", "sessions", "review_log", "user_card_state", "goose_db_version"} {
		if !tableExists(t, pool, table) {
			t.Errorf("table %s missing after Migrate", table)
		}
	}
	last, err := strconv.Atoi(strings.SplitN(names[len(names)-1], "_", 2)[0])
	if err != nil {
		t.Fatalf("parse last migration number: %v", err)
	}
	if got := scalar[int64](t, pool, "SELECT max(version_id) FROM goose_db_version"); got != int64(last) {
		t.Errorf("max(version_id) = %d, want %d", got, last)
	}
	// Migrate borrowed the pool through database/sql; it must not have closed it.
	if err := pool.Ping(context.Background()); err != nil {
		t.Errorf("pool unusable after Migrate: %v", err)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()

	if _, err := Migrate(ctx, pool, deckshare.Migrations()); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if !tableExists(t, pool, "users") {
		t.Fatal("users table missing after first Migrate")
	}
	rows := scalar[int64](t, pool, "SELECT count(*) FROM goose_db_version")

	applied, err := Migrate(ctx, pool, deckshare.Migrations())
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if applied != 0 {
		t.Errorf("second Migrate applied %d, want 0", applied)
	}
	if got := scalar[int64](t, pool, "SELECT count(*) FROM goose_db_version"); got != rows {
		t.Errorf("goose_db_version rows = %d after second Migrate, want %d", got, rows)
	}
}

// A dev database already migrated by the goose CLI up to some version must be picked up from there.
func TestMigrate_ResumesFromPartiallyMigrated(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	names := embeddedMigrationNames(t)

	first := fstest.MapFS{}
	for _, name := range names[:3] {
		b, err := fs.ReadFile(deckshare.Migrations(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		first[name] = &fstest.MapFile{Data: b}
	}
	applied, err := Migrate(ctx, pool, first)
	if err != nil {
		t.Fatalf("partial Migrate: %v", err)
	}
	if applied != 3 {
		t.Fatalf("partial Migrate applied %d, want 3", applied)
	}

	applied, err = Migrate(ctx, pool, deckshare.Migrations())
	if err != nil {
		t.Fatalf("resumed Migrate: %v", err)
	}
	if want := len(names) - 3; applied != want {
		t.Errorf("resumed Migrate applied %d, want %d", applied, want)
	}
	if !tableExists(t, pool, "review_log") {
		t.Error("review_log missing after resumed Migrate")
	}
}

func TestMigrate_FailsFastOnBadSQL(t *testing.T) {
	pool := migrationPool(t)
	fsys := fstest.MapFS{
		"00001_ok.sql":  {Data: []byte("-- +goose Up\nCREATE TABLE t1 (id int);\n")},
		"00002_bad.sql": {Data: []byte("-- +goose Up\nTHIS IS NOT SQL;\n")},
	}

	_, err := Migrate(context.Background(), pool, fsys)
	if err == nil {
		t.Fatal("Migrate succeeded on invalid SQL, want an error")
	}
	if !strings.Contains(err.Error(), "00002_bad.sql") {
		t.Errorf("error %q does not name the failing file 00002_bad.sql", err)
	}
	if !tableExists(t, pool, "t1") {
		t.Error("t1 missing: the migration before the failure should stay applied")
	}
	if n := scalar[int64](t, pool, "SELECT count(*) FROM goose_db_version WHERE version_id = 2"); n != 0 {
		t.Errorf("goose_db_version has %d row(s) for the failed version 2, want 0", n)
	}
}
