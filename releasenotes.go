package deckshare

import (
	_ "embed"
	"regexp"
	"strconv"
	"strings"
)

//go:embed docs/release-notes.md
var releaseNotesSrc string

// ReleaseNote is one version's user-facing section of docs/release-notes.md.
type ReleaseNote struct {
	Version string
	Date    string
	Groups  []ReleaseNoteGroup
}

// ReleaseNoteGroup is one "### Features" / "### Bug fixes" block of a version's bullets. Title is
// empty for bullets that appear before any "###" heading.
type ReleaseNoteGroup struct {
	Title string
	Items []string
}

// ItemCount is the number of bullets across all groups.
func (n ReleaseNote) ItemCount() int {
	count := 0
	for _, g := range n.Groups {
		count += len(g.Items)
	}
	return count
}

var releaseNoteHeadingRe = regexp.MustCompile(`^## \[([^\]]+)\](?: - (\S+))?`)
var releaseNoteGroupRe = regexp.MustCompile(`^### (.+)`)

var releaseNotes = parseReleaseNotes(releaseNotesSrc)

// ReleaseNotes returns the embedded notes, newest first (file order).
func ReleaseNotes() []ReleaseNote { return releaseNotes }

// LatestNotedVersion is the newest version that has a release-notes section, or "" if none.
// Internal-only releases have no section, so this can trail Version().
func LatestNotedVersion() string {
	if len(releaseNotes) == 0 {
		return ""
	}
	return releaseNotes[0].Version
}

// VersionLess reports whether dotted-numeric version a sorts before b. "" sorts before every
// version; a non-numeric component counts as 0.
func VersionLess(a, b string) bool {
	if a == "" || b == "" {
		return a == "" && b != ""
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func parseReleaseNotes(src string) []ReleaseNote {
	var notes []ReleaseNote
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, " \r")
		if m := releaseNoteHeadingRe.FindStringSubmatch(line); m != nil {
			notes = append(notes, ReleaseNote{Version: m[1], Date: m[2]})
			continue
		}
		if len(notes) == 0 {
			continue
		}
		cur := &notes[len(notes)-1]
		if m := releaseNoteGroupRe.FindStringSubmatch(line); m != nil {
			cur.Groups = append(cur.Groups, ReleaseNoteGroup{Title: strings.TrimSpace(m[1])})
			continue
		}
		isBullet := strings.HasPrefix(line, "- ")
		isContinuation := strings.HasPrefix(line, " ") && strings.TrimSpace(line) != ""
		if !isBullet && !isContinuation {
			continue
		}
		if len(cur.Groups) == 0 {
			cur.Groups = append(cur.Groups, ReleaseNoteGroup{})
		}
		g := &cur.Groups[len(cur.Groups)-1]
		switch {
		case isBullet:
			g.Items = append(g.Items, strings.TrimPrefix(line, "- "))
		case len(g.Items) > 0:
			g.Items[len(g.Items)-1] += " " + strings.TrimSpace(line)
		}
	}
	return notes
}
