// Package deckshare exposes build-wide values that need to come from a single source of
// truth, such as the running application version.
package deckshare

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed CHANGELOG.md
var changelog string

var changelogVersionRe = regexp.MustCompile(`^## \[([^\]]+)\]`)

// Version returns the version from the top CHANGELOG.md entry, e.g. "0.3.8". It is derived
// from the embedded changelog rather than a separately maintained constant, so it can never
// drift from the version already bumped alongside every PR (CLAUDE.md §14).
func Version() string {
	for _, line := range strings.Split(changelog, "\n") {
		if m := changelogVersionRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return "unknown"
}
