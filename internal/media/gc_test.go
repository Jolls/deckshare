package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jolls/deckshare/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Media GC tests (#91, docs/plans/91-orphaned-media-blob-gc.md). DB-backed: skipped unless
// DATABASE_URL is set. Every test runs inside a pgx.Tx that is always rolled back, and addresses
// its blobs by freshly generated random digests, so its own assertions stay scoped to rows it
// created (CLAUDE.md §16) even against a populated database. The bytes live in the test's own
// t.TempDir store, so the class-2 half of Sweep never touches the real MEDIA_ROOT.
//
// Sweep's class-1 half is the one exception: ListUnreferencedMediaBlobs/DeleteMediaBlob are
// table-wide by design (that is the feature), so a Sweep call in these tests also attempts to
// reclaim any orphaned media_blobs row already sitting in a populated database, not just the ones
// this file creates. That is safe here because the whole attempt rolls back with the transaction,
// and nothing else is expected to hold a real lock on those rows during a test run -- it would only
// matter racing a live media.GC ticker against the same database while tests run, which this
// project's workflow does not do (CLAUDE.md: don't run the app to verify).

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	seq      atomic.Int64
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
	tx, err := testPool(t).Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return tx
}

// randomSHA is a digest no other row or test can be using -- what keeps a table-wide sweep's
// assertions scoped to this test's own rows.
func randomSHA(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random sha: %v", err)
	}
	return hex.EncodeToString(b[:])
}

func mustUser(t *testing.T, tx pgx.Tx) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	email := fmt.Sprintf("media-gc-%d@example.com", seq.Add(1))
	if err := tx.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Test User') RETURNING id`,
		email,
	).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func mustDeck(t *testing.T, tx pgx.Tx, ownerID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	name := fmt.Sprintf("Media GC Deck %d", seq.Add(1))
	if err := tx.QueryRow(context.Background(),
		`INSERT INTO decks (owner_id, name) VALUES ($1, $2) RETURNING id`, ownerID, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert deck: %v", err)
	}
	return id
}

func mustBlobRow(t *testing.T, tx pgx.Tx, sha string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO media_blobs (sha256, size_bytes, mime) VALUES ($1, 1, 'image/png')`, sha,
	); err != nil {
		t.Fatalf("insert media_blobs: %v", err)
	}
}

func mustRef(t *testing.T, tx pgx.Tx, deckID pgtype.UUID, filename, sha string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO media_refs (deck_id, filename, sha256) VALUES ($1, $2, $3)`, deckID, filename, sha,
	); err != nil {
		t.Fatalf("insert media_refs: %v", err)
	}
}

func countRows(t *testing.T, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

func blobRowCount(t *testing.T, tx pgx.Tx, sha string) int64 {
	t.Helper()
	return countRows(t, tx, `SELECT count(*) FROM media_blobs WHERE sha256 = $1`, sha)
}

// mustStoredFile writes a blob into the store and backdates it by age, so a test can put a file on
// the wrong side of the sweep's grace period without waiting for one.
func mustStoredFile(t *testing.T, s *Store, sha string, age time.Duration) {
	t.Helper()
	if err := s.Put(sha, []byte("bytes for "+sha)); err != nil {
		t.Fatalf("Put %s: %v", sha, err)
	}
	if age > 0 {
		when := time.Now().Add(-age)
		path := filepath.Join(s.Root, sha[:2], sha)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("backdate %s: %v", path, err)
		}
	}
}

func fileExists(t *testing.T, s *Store, sha string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(s.Root, sha[:2], sha))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", sha, err)
	}
	return err == nil
}

// sweepTx adapts a rolled-back test transaction to the way the sweep meets the database in
// production, where every statement runs on a pooled connection inside its own implicit
// transaction. Two things it papers over. First, Postgres aborts a whole transaction on any error,
// so without a savepoint per Exec the foreign-key violation TestGC_SweepSkipsReReferencedBlob
// provokes would poison every assertion that follows it. Second, beforeExec gives a test a
// deterministic place to interleave a concurrent writer: DeleteMediaBlob is the only Exec the
// sweep issues, so a hook there lands exactly between the sweep listing a blob as unreferenced and
// deleting it.
type sweepTx struct {
	tx         pgx.Tx
	beforeExec func(args []any)
}

func (s *sweepTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return s.tx.Query(ctx, sql, args...)
}

func (s *sweepTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.tx.QueryRow(ctx, sql, args...)
}

func (s *sweepTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if s.beforeExec != nil {
		s.beforeExec(args)
	}
	sp, err := s.tx.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, execErr := sp.Exec(ctx, sql, args...)
	if execErr != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			return tag, errors.Join(execErr, rbErr)
		}
		return tag, execErr
	}
	return tag, sp.Commit(ctx)
}

func newTestGC(tx pgx.Tx, s *Store, beforeExec func(args []any)) *GC {
	return NewGC(&sweepTx{tx: tx, beforeExec: beforeExec}, s, 24*time.Hour)
}

// The class-1 query: a blob is orphaned exactly when no media_refs row names it. A deck delete
// cascades those refs away and leaves the blob row behind (docs/schema.md, Media).
func TestListUnreferencedMediaBlobs(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	deck := mustDeck(t, tx, mustUser(t, tx))
	orphan, referenced := randomSHA(t), randomSHA(t)
	mustBlobRow(t, tx, orphan)
	mustBlobRow(t, tx, referenced)
	mustRef(t, tx, deck, "kept.png", referenced)

	shas, err := db.New(tx).ListUnreferencedMediaBlobs(ctx)
	if err != nil {
		t.Fatalf("ListUnreferencedMediaBlobs: %v", err)
	}

	found := map[string]bool{}
	for _, sha := range shas {
		found[sha] = true
	}
	if !found[orphan] {
		t.Error("unreferenced blob was not listed")
	}
	if found[referenced] {
		t.Error("referenced blob was listed as unreferenced")
	}
}

// #176: users.avatar_sha256 is a second RESTRICT reference to a blob. The sweep unlinks bytes
// before deleting the row, so an avatar's blob missing from the class-1 query would lose its file
// before the FK could object.
func TestListUnreferencedMediaBlobs_ExcludesAvatars(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	orphan, avatar := randomSHA(t), randomSHA(t)
	mustBlobRow(t, tx, orphan)
	mustBlobRow(t, tx, avatar)

	user := mustUser(t, tx)
	if _, err := tx.Exec(ctx, `UPDATE users SET avatar_sha256 = $2 WHERE id = $1`, user, avatar); err != nil {
		t.Fatalf("set avatar: %v", err)
	}

	shas, err := db.New(tx).ListUnreferencedMediaBlobs(ctx)
	if err != nil {
		t.Fatalf("ListUnreferencedMediaBlobs: %v", err)
	}

	found := map[string]bool{}
	for _, sha := range shas {
		found[sha] = true
	}
	if !found[orphan] {
		t.Error("unreferenced blob was not listed")
	}
	if found[avatar] {
		t.Error("avatar-referenced blob was listed as unreferenced")
	}
}

// Class 1 end to end: the row and its bytes both go, and a referenced blob beside it is untouched.
func TestGC_SweepReclaimsUnreferencedBlob(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	deck := mustDeck(t, tx, mustUser(t, tx))
	orphan, referenced := randomSHA(t), randomSHA(t)
	mustBlobRow(t, tx, orphan)
	mustBlobRow(t, tx, referenced)
	mustRef(t, tx, deck, "kept.png", referenced)

	store := New(t.TempDir())
	mustStoredFile(t, store, orphan, 0)
	mustStoredFile(t, store, referenced, 0)

	if err := newTestGC(tx, store, nil).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if blobRowCount(t, tx, orphan) != 0 {
		t.Error("unreferenced blob row should be gone")
	}
	if fileExists(t, store, orphan) {
		t.Error("unreferenced blob file should be gone")
	}
	if blobRowCount(t, tx, referenced) != 1 {
		t.Error("referenced blob row should survive")
	}
	if !fileExists(t, store, referenced) {
		t.Error("referenced blob file should survive")
	}
}

// The interleaving the sweep exists to survive: a media_refs row appears between the sweep listing
// a blob as unreferenced and deleting it. Postgres's own RESTRICT enforcement -- not a re-check in
// the query -- catches it, and the sweep must read that violation as "someone re-referenced it",
// not as a failure: no error out of Sweep, and the row and its new ref both intact.
//
// Simulated rather than raced: the writer runs on the sweep's own transaction at the one point
// where a real concurrent writer would have to land to matter (see sweepTx). A two-goroutine
// version would have to commit its ref to be visible, which a rolled-back test transaction cannot
// do without leaving rows behind, and would still depend on lock-wait timing to be deterministic.
func TestGC_SweepSkipsReReferencedBlob(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	deck := mustDeck(t, tx, mustUser(t, tx))
	sha := randomSHA(t)
	mustBlobRow(t, tx, sha)

	store := New(t.TempDir())
	mustStoredFile(t, store, sha, 0)

	// Only fire for this test's own blob: the sweep is table-wide, so it may well delete other
	// unreferenced rows (rolled back with everything else) before it reaches this one.
	gc := newTestGC(tx, store, func(args []any) {
		if len(args) == 1 && args[0] == sha {
			mustRef(t, tx, deck, "reimported.png", sha)
		}
	})

	if err := gc.Sweep(ctx); err != nil {
		t.Fatalf("Sweep returned %v; a blob re-referenced mid-sweep is a skip, not an error", err)
	}

	if blobRowCount(t, tx, sha) != 1 {
		t.Error("re-referenced blob row should survive the sweep")
	}
	if countRows(t, tx, `SELECT count(*) FROM media_refs WHERE sha256 = $1`, sha) != 1 {
		t.Error("the ref created mid-sweep should survive")
	}
	// The bytes are gone: the unlink happens before the delete by design, and losing this race is
	// the residual window docs/plans/91-orphaned-media-blob-gc.md §2 accepts and documents (the
	// alternative ordering strands live rows far more easily). Asserted so that a change to the
	// ordering has to come back through the plan doc.
	if fileExists(t, store, sha) {
		t.Error("file should have been unlinked before the row delete was attempted")
	}
}

// Class 2: bytes with no media_blobs row at all, which is what a rolled-back import leaves behind.
// Only files past the grace period are fair game -- a younger one may belong to an import still
// running, since Put writes before that transaction commits.
func TestGC_SweepOrphanFiles(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	deck := mustDeck(t, tx, mustUser(t, tx))
	agedOrphan, freshOrphan, referenced := randomSHA(t), randomSHA(t), randomSHA(t)
	mustBlobRow(t, tx, referenced)
	mustRef(t, tx, deck, "kept.png", referenced)

	store := New(t.TempDir())
	mustStoredFile(t, store, agedOrphan, 48*time.Hour)
	mustStoredFile(t, store, freshOrphan, 0)
	mustStoredFile(t, store, referenced, 48*time.Hour)

	// A write in flight, backdated so only the walk's name check can save it.
	tmpPath := filepath.Join(store.Root, agedOrphan[:2], ".tmp-inflight")
	if err := os.WriteFile(tmpPath, []byte("partial"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	when := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(tmpPath, when, when); err != nil {
		t.Fatalf("backdate temp file: %v", err)
	}

	if err := newTestGC(tx, store, nil).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if fileExists(t, store, agedOrphan) {
		t.Error("aged file with no blob row should be gone")
	}
	if !fileExists(t, store, freshOrphan) {
		t.Error("file inside the grace period should survive")
	}
	if !fileExists(t, store, referenced) {
		t.Error("file with a live blob row should survive")
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Errorf("in-flight .tmp file should survive: %v", err)
	}
}
