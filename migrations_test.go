package deckshare

import (
	"bytes"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var migrationFileRe = regexp.MustCompile(`^(\d{5})_[a-z0-9_]+\.sql$`)

// diskMigrations returns the .sql files in migrations/ (the directory also holds a README.md).
func diskMigrations(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations/: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	return names
}

// The embed pattern and the goose provider both lean on this layout (#274): zero-padded, strictly
// sequential, goose-annotated files. A malformed name would be skipped or misordered silently.
func TestMigrationFilesOnDiskAreSequentialGoose(t *testing.T) {
	names := diskMigrations(t)
	if len(names) == 0 {
		t.Fatal("no migrations found")
	}
	for i, name := range names {
		m := migrationFileRe.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("%s does not match NNNNN_snake_case.sql", name)
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n != i+1 {
			t.Errorf("%s is migration %d in order, want number %05d (gap or duplicate)", name, i+1, i+1)
		}
		b, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Contains(b, []byte("-- +goose Up")) {
			t.Errorf("%s has no '-- +goose Up' annotation", name)
		}
	}
}

// The binary applies what it embeds at startup, so the embed must be exactly migrations/*.sql --
// not empty, rooted at ".", and not carrying the README. (The bytes can't drift: go:embed reads
// them at build time.)
func TestMigrationsFSMatchesDisk(t *testing.T) {
	onDisk := diskMigrations(t)
	entries, err := fs.ReadDir(Migrations(), ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	var embedded []string
	for _, e := range entries {
		embedded = append(embedded, e.Name())
	}
	slices.Sort(embedded)
	if !slices.Equal(embedded, onDisk) {
		t.Fatalf("embedded migrations = %v, want the %d files on disk %v", embedded, len(onDisk), onDisk)
	}
}
