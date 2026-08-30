// Package media is the filesystem-backed, content-addressed blob store (docs/schema.md, Media).
// Bytes live at ${MEDIA_ROOT}/<sha[0:2]>/<sha>; the database holds metadata rows only, never the
// bytes themselves (CLAUDE.md §2, architecture.md §12). S3-compatible remains a drop-in later --
// the metadata-row-plus-external-bytes shape is identical, only "external" changes.
package media

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// sha256Hex matches a lowercase hex sha256 digest -- the only shape ever used to address a blob.
// Enforced here because Store.Open's argument can originate from an HTTP path segment (the
// GET /media/{sha256} route): without this check a value like "../../etc/passwd" would escape
// Root through path.Join.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Store is a content-addressed blob store rooted at a directory on the local filesystem.
type Store struct {
	Root string
}

// New returns a Store rooted at root. The directory is created lazily, on first write.
func New(root string) *Store {
	return &Store{Root: root}
}

// Put writes data under its sha256 hex digest. Idempotent: a blob that already exists on disk is
// left untouched, since content-addressing means any file already at that path holds these exact
// bytes. Written via a temp file plus rename so a concurrent Open never observes a partial write.
func (s *Store) Put(sha256Hex string, data []byte) error {
	path, err := s.path(sha256Hex)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("media: creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("media: creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("media: writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("media: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("media: renaming %s to %s: %w", tmpName, path, err)
	}
	return nil
}

// Delete removes the blob addressed by sha256Hex. Idempotent: a blob already gone is success, not
// an error -- the GC sweep (gc.go) can legitimately meet a file another sweep, or a manual clean-up,
// already removed, and there is nothing left to do about it either way.
func (s *Store) Delete(sha256Hex string) error {
	path, err := s.path(sha256Hex)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("media: removing %s: %w", path, err)
	}
	return nil
}

// Walk calls fn for every blob in the store, with its digest and last-modified time. Anything whose
// name is not a digest is skipped rather than reported -- which is what excludes Put's in-flight
// ".tmp-*" scratch files, and equally anything else that finds its way under Root.
func (s *Store) Walk(fn func(sha256Hex string, modTime time.Time) error) error {
	return filepath.WalkDir(s.Root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// A Root that does not exist yet is an empty store, not a failure (New creates it
			// lazily, on first write), and an entry that vanished mid-walk is one nothing needs
			// to be told about either.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !sha256Hex.MatchString(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		return fn(d.Name(), info.ModTime())
	})
}

// Open returns a reader for the blob addressed by sha256Hex. The caller must Close it.
func (s *Store) Open(sha256Hex string) (io.ReadCloser, error) {
	path, err := s.path(sha256Hex)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *Store) path(digest string) (string, error) {
	if !sha256Hex.MatchString(digest) {
		return "", fmt.Errorf("media: %q is not a valid sha256 hex digest", digest)
	}
	return filepath.Join(s.Root, digest[:2], digest), nil
}
