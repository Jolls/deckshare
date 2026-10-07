package deckshare

import (
	"strings"
	"testing"
)

// Release notes are a second, user-facing view of CHANGELOG.md (#266): a version in the notes
// that isn't in the changelog is a typo or a note for a release that never happened.
func TestReleaseNotesVersionsExistInChangelog(t *testing.T) {
	notes := ReleaseNotes()
	if len(notes) == 0 {
		t.Fatal("no release notes parsed from docs/release-notes.md")
	}
	for _, n := range notes {
		if !strings.Contains(changelog, "## ["+n.Version+"]") {
			t.Errorf("release notes version %s has no CHANGELOG.md entry", n.Version)
		}
		if n.ItemCount() == 0 {
			t.Errorf("release notes version %s has no bullets", n.Version)
		}
	}
}

func TestReleaseNotesNewestFirstAndNotAheadOfVersion(t *testing.T) {
	notes := ReleaseNotes()
	for i := 1; i < len(notes); i++ {
		if !VersionLess(notes[i].Version, notes[i-1].Version) {
			t.Errorf("%s should sort before %s (newest first)", notes[i].Version, notes[i-1].Version)
		}
	}
	if VersionLess(Version(), LatestNotedVersion()) {
		t.Errorf("latest noted version %s is ahead of Version() %s", LatestNotedVersion(), Version())
	}
}

func TestVersionLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"", "0.1.0", true},
		{"0.1.0", "", false},
		{"", "", false},
		{"0.3.9", "0.3.10", true},
		{"0.3.10", "0.3.9", false},
		{"0.3.14", "0.3.14", false},
		{"0.3", "0.3.1", true},
		{"0.4.0", "0.3.99", false},
	}
	for _, tt := range tests {
		if got := VersionLess(tt.a, tt.b); got != tt.want {
			t.Errorf("VersionLess(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseReleaseNotes(t *testing.T) {
	got := parseReleaseNotes("# T\n\nintro\n\n## [1.2.0] - 2026-01-02\n### New features\n- a\n  more\n- b\n### Bug fixes\n- c\n\n## [1.1.0]\n- d\n")
	if len(got) != 2 {
		t.Fatalf("got %d sections, want 2", len(got))
	}
	if got[0].Version != "1.2.0" || got[0].Date != "2026-01-02" || len(got[0].Groups) != 2 || got[0].ItemCount() != 3 {
		t.Fatalf("section 0 = %+v", got[0])
	}
	if g := got[0].Groups[0]; g.Title != "New features" || len(g.Items) != 2 || g.Items[0] != "a more" {
		t.Errorf("group 0 = %+v", g)
	}
	if g := got[0].Groups[1]; g.Title != "Bug fixes" || len(g.Items) != 1 || g.Items[0] != "c" {
		t.Errorf("group 1 = %+v", g)
	}
	// Bullets before any "###" heading land in one untitled group.
	if got[1].Version != "1.1.0" || got[1].Date != "" || len(got[1].Groups) != 1 || got[1].Groups[0].Title != "" || got[1].Groups[0].Items[0] != "d" {
		t.Errorf("section 1 = %+v", got[1])
	}
}
