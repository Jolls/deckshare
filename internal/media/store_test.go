package media

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
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
