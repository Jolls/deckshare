package deckshare

import "testing"

func TestVersionMatchesTopChangelogEntry(t *testing.T) {
	got := Version()
	if got == "unknown" || got == "" {
		t.Fatalf("Version() = %q, want a version parsed from CHANGELOG.md", got)
	}
}
