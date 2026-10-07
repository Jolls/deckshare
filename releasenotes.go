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
	Items   []string
}

var releaseNoteHeadingRe = regexp.MustCompile(`^## \[([^\]]+)\](?: - (\S+))?`)

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
		switch {
		case strings.HasPrefix(line, "- "):
			cur.Items = append(cur.Items, strings.TrimPrefix(line, "- "))
		case strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" && len(cur.Items) > 0:
			cur.Items[len(cur.Items)-1] += " " + strings.TrimSpace(line)
		}
	}
	return notes
}
