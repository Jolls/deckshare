package render

import (
	"html"
	"math"
	"sort"
	"strconv"
	"strings"
)

// clozeSpan is one {{cN::text::hint}} marker in field content.
type clozeSpan struct {
	num        int    // 0 when the marker's number was out of range (c0, or overflow) -- never
	start, end int    // an "active" ordinal, so it always renders as revealed
	text       string // the hidden content, verbatim (may itself contain nested spans)
	hint       string // "" when absent
}

// scanCloze returns every top-level cloze marker in s, in source order. A nested marker (e.g.
// {{c2::...}} inside {{c1::...}}) is returned inside its parent's text, not as a top-level span
// -- recurse into span.text to find it.
func scanCloze(s string) []clozeSpan {
	var spans []clozeSpan
	i := 0
	for i < len(s) {
		rel := strings.Index(s[i:], "{{c")
		if rel < 0 {
			break
		}
		start := i + rel

		j := start + 3
		numStart := j
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == numStart || j+2 > len(s) || s[j:j+2] != "::" {
			i = start + 3
			continue
		}
		numStr := s[numStart:j]
		contentStart := j + 2

		depth := 0
		sepIdx := -1
		end := -1
		k := contentStart
		for k < len(s) {
			switch {
			case strings.HasPrefix(s[k:], "{{"):
				depth++
				k += 2
			case strings.HasPrefix(s[k:], "}}"):
				if depth == 0 {
					end = k + 2
				} else {
					depth--
					k += 2
				}
			case depth == 0 && sepIdx < 0 && strings.HasPrefix(s[k:], "::"):
				sepIdx = k
				k++
			default:
				k++
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			// Unterminated marker: not a marker at all -- stays literal text (§0.2).
			i = start + 3
			continue
		}

		num := 0
		if v, err := strconv.ParseInt(numStr, 10, 64); err == nil && v >= 1 && v <= math.MaxInt32 {
			num = int(v)
		}

		var text, hint string
		if sepIdx >= 0 {
			text = s[contentStart:sepIdx]
			hint = s[sepIdx+2 : end-2]
		} else {
			text = s[contentStart : end-2]
		}
		spans = append(spans, clozeSpan{num: num, start: start, end: end, text: text, hint: hint})
		i = end
	}
	return spans
}

// ClozeOrdinals returns the distinct cloze numbers appearing in fields, ascending, including
// numbers found in nested markers. A cloze note generates one card per number (architecture.md
// §8). {{c0::...}} and numbers that overflow int32 are ignored -- Anki numbers clozes from 1.
func ClozeOrdinals(fields []string) []int32 {
	seen := map[int32]struct{}{}
	var collect func(s string)
	collect = func(s string) {
		for _, sp := range scanCloze(s) {
			if sp.num >= 1 {
				seen[int32(sp.num)] = struct{}{}
			}
			collect(sp.text)
		}
	}
	for _, f := range fields {
		collect(f)
	}
	if len(seen) == 0 {
		return nil
	}
	ordinals := make([]int32, 0, len(seen))
	for n := range seen {
		ordinals = append(ordinals, n)
	}
	sort.Slice(ordinals, func(i, j int) bool { return ordinals[i] < ordinals[j] })
	return ordinals
}

// clozeFilter renders {{cloze:Field}} per architecture.md §8's rule: the active cloze number
// (ordinal+1) blanks on the question side and reveals highlighted on the answer side; every
// OTHER cloze number reveals as plain text on BOTH sides, recursively, so a multi-cloze note's
// non-active clozes read as context instead of vanishing.
func clozeFilter(value string, ctx *context) string {
	var out strings.Builder
	renderClozeRecursive(value, ctx, &out)
	return out.String()
}

func renderClozeRecursive(s string, ctx *context, out *strings.Builder) {
	pos := 0
	for _, sp := range scanCloze(s) {
		out.WriteString(s[pos:sp.start])
		active := ctx.isCloze && sp.num == int(ctx.ordinal)+1
		switch {
		case active && ctx.isQuestionSide:
			if sp.hint != "" {
				out.WriteString(`<span class="cloze">[` + html.EscapeString(sp.hint) + `]</span>`)
			} else {
				out.WriteString(`<span class="cloze">[...]</span>`)
			}
		case active && !ctx.isQuestionSide:
			out.WriteString(`<span class="cloze">`)
			renderClozeRecursive(sp.text, ctx, out)
			out.WriteString(`</span>`)
		default:
			renderClozeRecursive(sp.text, ctx, out)
		}
		pos = sp.end
	}
	out.WriteString(s[pos:])
}
