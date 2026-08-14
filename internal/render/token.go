package render

import (
	"fmt"
	"strings"
)

type kind int

const (
	kindText    kind = iota // literal template text
	kindField               // {{Name}} or {{filter:...:Name}}
	kindOpen                // {{#Name}}
	kindOpenNeg             // {{^Name}}
	kindClose               // {{/Name}}
)

type token struct {
	kind    kind
	text    string   // kindText: the literal run
	name    string   // field or section name, trimmed
	filters []string // outermost first; {{text:cloze:X}} -> ["text","cloze"]
	offset  int      // byte offset of "{{", for error messages
}

// tokenise scans format for a flat sequence of {{...}} tags and the literal text between them.
// Template tags don't nest braces (architecture.md §8), so a single non-nested pass suffices.
func tokenise(format string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(format) {
		open := strings.Index(format[i:], "{{")
		if open < 0 {
			toks = append(toks, token{kind: kindText, text: format[i:]})
			break
		}
		open += i
		if open > i {
			toks = append(toks, token{kind: kindText, text: format[i:open]})
		}
		closeIdx := strings.Index(format[open+2:], "}}")
		if closeIdx < 0 {
			return nil, fmt.Errorf("%w: at byte offset %d", ErrUnterminatedTag, open)
		}
		closeIdx += open + 2
		inner := format[open+2 : closeIdx]
		i = closeIdx + 2

		if strings.HasPrefix(inner, "!") {
			// {{!comment}} -- dropped, emits nothing.
			continue
		}

		if inner == "" || strings.TrimSpace(inner) == "" {
			toks = append(toks, token{kind: kindText, text: format[open:i]})
			continue
		}

		trimmed := strings.TrimSpace(inner)
		if len(trimmed) > 0 && (trimmed[0] == '#' || trimmed[0] == '^' || trimmed[0] == '/') {
			k := kindOpen
			switch trimmed[0] {
			case '^':
				k = kindOpenNeg
			case '/':
				k = kindClose
			}
			toks = append(toks, token{kind: k, name: strings.TrimSpace(trimmed[1:]), offset: open})
			continue
		}

		parts := strings.Split(inner, ":")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		name := parts[len(parts)-1]
		filters := parts[:len(parts)-1]
		toks = append(toks, token{kind: kindField, name: name, filters: filters, offset: open})
	}
	return toks, nil
}
