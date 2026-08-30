package media

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestStore_PutThenOpen(t *testing.T) {
	s := New(t.TempDir())
	data := []byte("hello media")

	if err := s.Put(testSHA, data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	wantPath := filepath.Join(s.Root, testSHA[:2], testSHA)
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("blob not at expected path %s: %v", wantPath, err)
	}

	f, err := s.Open(testSHA)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("read %q, want %q", got, data)
	}
}

// Put must be idempotent -- a re-import writing the same blob a second time must not fail or
// leave temp files behind (docs/schema.md's Media section: dedup is by content, not by import).
func TestStore_PutTwiceIsIdempotent(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Put(testSHA, []byte("hello media")); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := s.Put(testSHA, []byte("hello media")); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(s.Root, testSHA[:2]))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("shard dir has %d entries, want 1 (no leftover temp files)", len(entries))
	}
}

func TestStore_RejectsMalformedDigest(t *testing.T) {
	s := New(t.TempDir())
	cases := []string{
		"",
		"not-hex",
		"../../etc/passwd",
		testSHA[:63],  // too short
		testSHA + "a", // too long
		"E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855", // uppercase
	}
	for _, digest := range cases {
		if err := s.Put(digest, []byte("x")); err == nil {
			t.Errorf("Put(%q): want error, got nil", digest)
		}
		if f, err := s.Open(digest); err == nil {
			_ = f.Close()
			t.Errorf("Open(%q): want error, got nil", digest)
		}
	}

	// Nothing should have escaped Root.
	entries, _ := os.ReadDir(s.Root)
	if len(entries) != 0 {
		t.Errorf("Root has %d entries after rejected writes, want 0", len(entries))
	}
}

func TestStore_OpenMissingBlob(t *testing.T) {
	s := New(t.TempDir())
	_, err := s.Open(testSHA)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open(missing) err = %v, want os.ErrNotExist", err)
	}
}

// Delete has to be idempotent for the GC sweep (gc.go): the sweep unlinks before deleting the row,
// so a retried sweep meets a file it already removed.
func TestStore_Delete(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Put(testSHA, []byte("hello media")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.Delete(testSHA); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Open(testSHA); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open after Delete err = %v, want os.ErrNotExist", err)
	}
	if err := s.Delete(testSHA); err != nil {
		t.Errorf("second Delete: %v, want nil (idempotent)", err)
	}
	if err := s.Delete("not-a-digest"); err == nil {
		t.Error("Delete(malformed digest): want error, got nil")
	}
}

func TestStore_Walk(t *testing.T) {
	s := New(t.TempDir())
	other := "ffb0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	for _, sha := range []string{testSHA, other} {
		if err := s.Put(sha, []byte("x")); err != nil {
			t.Fatalf("Put(%s): %v", sha, err)
		}
	}

	// A ".tmp-*" file is a write in flight, not an orphan, and must never be yielded. Neither must
	// anything else that is not a digest -- the walk is a delete path, so unknown files are left be.
	shard := filepath.Join(s.Root, testSHA[:2])
	for _, name := range []string{".tmp-123456", "README", testSHA[:63]} {
		if err := os.WriteFile(filepath.Join(shard, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	seen := map[string]time.Time{}
	if err := s.Walk(func(sha string, modTime time.Time) error {
		seen[sha] = modTime
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("Walk yielded %d entries (%v), want 2", len(seen), seen)
	}
	for _, sha := range []string{testSHA, other} {
		modTime, ok := seen[sha]
		if !ok {
			t.Errorf("Walk did not yield %s", sha)
			continue
		}
		if modTime.IsZero() {
			t.Errorf("Walk yielded a zero mod time for %s", sha)
		}
	}
}

func TestStore_WalkPropagatesCallbackError(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Put(testSHA, []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	sentinel := errors.New("stop")
	if err := s.Walk(func(string, time.Time) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("Walk err = %v, want %v", err, sentinel)
	}
}

// A store whose root was never written to is an empty store, not a failure -- New creates the
// directory lazily, so the first sweep on a fresh deployment walks a path that does not exist.
func TestStore_WalkMissingRoot(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "never-written"))
	calls := 0
	if err := s.Walk(func(string, time.Time) error { calls++; return nil }); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if calls != 0 {
		t.Errorf("Walk called fn %d times on a missing root, want 0", calls)
	}
}
