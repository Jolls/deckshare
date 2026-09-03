package render

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"strings"
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTMLTags(s string) string {
	return htmlTagRe.ReplaceAllString(s, "")
}

// imgSrcRe extracts an <img>'s src so fieldHasContent can see it as text. [sound:file.mp3]
// already survives stripHTMLTags untouched (it's bracket syntax, not a tag), but an <img> tag
// strips to nothing, and a field that is only an image is extremely common in real templates
// ({{#Flag}}/{{#Map}} gating a card's whole question side on an image field, e.g. the Ultimate
// Geography and BetterVectorMaps shared decks).
var imgSrcRe = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc\s*=\s*["']([^"']*)["'][^>]*>`)

// fieldHasContent reports whether value counts as non-empty for {{#Field}}/{{^Field}} sections
// and {{hint:Field}} -- both documented as "emitted iff the field is non-empty" (§3.1). Anki
// treats a field consisting of only a media reference as non-empty even though it strips to no
// text; a <br>-only or whitespace-only field must still count as empty, so images get their src
// substituted in as a stand-in for "there is content here" before the usual strip-and-trim check.
func fieldHasContent(value string) bool {
	return strings.TrimSpace(stripHTMLTags(imgSrcRe.ReplaceAllString(value, "$1"))) != ""
}

// applyFilters resolves name to a value and applies filters right-to-left over it (§3.3):
// filters[len-1] (nearest the field name) applies first, filters[0] (outermost) applies last.
// type: is only legal as the outermost filter (index 0); anywhere else, or combined with
// type:cloze:/type:nc: (§0.8), it degrades to inert bracketed text rather than failing the
// whole render (§0.6).
func applyFilters(name string, filters []string, ctx *context) (string, error) {
	raw, ok := lookupName(name, ctx)
	if !ok {
		return html.EscapeString("[unknown field: " + name + "]"), nil
	}
	if len(filters) == 0 {
		return raw, nil
	}

	if filters[0] == "type" {
		if len(filters) == 1 {
			return typeFilter(name, raw, ctx)
		}
		return html.EscapeString("[unsupported: type:" + filters[1] + ":" + name + "]"), nil
	}
	for _, f := range filters {
		if f == "type" {
			return html.EscapeString("[unsupported: type: not outermost]"), nil
		}
	}

	value := raw
	for i := len(filters) - 1; i >= 0; i-- {
		value = applyFilter(name, filters[i], value, ctx)
	}
	return value, nil
}

func applyFilter(fieldName, filterName, value string, ctx *context) string {
	switch filterName {
	case "text":
		return textFilter(value)
	case "furigana":
		return furiganaFilter(value, furiganaFull)
	case "kanji":
		return furiganaFilter(value, furiganaKanjiOnly)
	case "kana":
		return furiganaFilter(value, furiganaKanaOnly)
	case "hint":
		return hintFilter(fieldName, value)
	case "cloze":
		return clozeFilter(value, ctx)
	case "tts":
		return "[unsupported: tts]"
	case "cloze-only":
		return "[unsupported: cloze-only]"
	default:
		return html.EscapeString("[unknown filter: " + filterName + "]")
	}
}

// textFilter: HTML stripped, entities decoded, result HTML-escaped -> plain text.
func textFilter(value string) string {
	stripped := stripHTMLTags(value)
	decoded := html.UnescapeString(stripped)
	return html.EscapeString(decoded)
}

type furiganaMode int

const (
	furiganaFull furiganaMode = iota
	furiganaKanjiOnly
	furiganaKanaOnly
)

// furiganaRe matches Anki's "base[reading]" furigana notation.
var furiganaRe = regexp.MustCompile(`([^\s\[\]]+)\[([^\]]+)\]`)

func furiganaFilter(value string, mode furiganaMode) string {
	return furiganaRe.ReplaceAllStringFunc(value, func(m string) string {
		sub := furiganaRe.FindStringSubmatch(m)
		base, reading := sub[1], sub[2]
		switch mode {
		case furiganaKanjiOnly:
			return base
		case furiganaKanaOnly:
			return reading
		default:
			return "<ruby>" + base + "<rt>" + reading + "</rt></ruby>"
		}
	})
}

// hintFilter: <details><summary>Field</summary>value</details>; empty field -> nothing.
func hintFilter(fieldName, value string) string {
	if !fieldHasContent(value) {
		return ""
	}
	return "<details><summary>" + html.EscapeString(fieldName) + "</summary>" + value + "</details>"
}

// typeFilter builds the {{type:Field}} placeholder (architecture.md §8's fourth bullet): no
// HTML is emitted here at all, just an unguessable per-render token the caller splices a real
// <input>/expected-answer widget into post-sanitisation (typeanswer.go). The token must actually
// be unguessable -- a caller could otherwise spoof the splice point with field content -- so a
// rand.Read failure is a hard error, not a silently-degraded placeholder.
func typeFilter(fieldName, rawValue string, ctx *context) (string, error) {
	if ctx.typeAnswer != nil {
		return html.EscapeString("[duplicate type: " + fieldName + "]"), nil
	}
	expected := strings.TrimSpace(html.UnescapeString(stripHTMLTags(rawValue)))
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("render: generating type-answer token: %w", err)
	}
	placeholder := "DECKSHARETYPEANSWER" + hex.EncodeToString(buf[:]) + "DECKSHAREEND"
	ctx.typeAnswer = &TypeAnswer{Field: fieldName, Expected: expected, Placeholder: placeholder}
	return placeholder, nil
}
