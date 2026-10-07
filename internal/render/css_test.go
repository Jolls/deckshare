package render

import (
	"strings"
	"testing"
)

func TestSanitiseCSS(t *testing.T) {
	tests := []struct {
		name        string
		css         string
		wantOutput  string // substring the output must contain; "" means output must be empty
		mustNotHave string
	}{
		{"basic card rule", `.card { color: red; }`, ".deckshare-card {", ""},
		{"descendant scoped", `.card .front { color: blue; }`, ".deckshare-card .front {", ""},
		{"non-card selector prefixed", `.night_mode { color: white; }`, ".deckshare-card .night_mode", ""},
		{"url value dropped", `.card { background: url(evil.com); color: red; }`, "color: red", "url("},
		{"colour functions allowed", `.card { color: rgb(1,2,3); }`, "rgb(1,2,3)", ""},
		{"expression dropped", `.card { width: expression(alert(1)); }`, "", "expression"},
		{"var function dropped", `.card { color: var(--x); }`, "", "var("},
		{"calc dropped", `.card { width: calc(1px + 2px); }`, "", "calc("},
		{"space before paren dropped", `.card { color: rgb (1,2,3); }`, "", "rgb"},
		{"position dropped", `.card { position: fixed; top: 0; }`, "", "position"},
		{"important stripped", `.card { color: red !important; }`, "color: red", "important"},
		{"id selector dropped", `#foo { color: red; }`, "", "#foo"},
		{"attribute selector dropped", `a[href] { color: red; }`, "", "["},
		{"pseudo element dropped", `.card::before { color: red; }`, "", "::before"},
		{"star selector dropped", `* { color: red; }`, "", "*"},
		{"root html body dropped", `html { color: red; } body { color: blue; }`, "", "html"},
		{"data-theme attribute selector dropped", `[data-theme=dark] .x { color: white; }`, "", "data-theme"},
		{"html data-theme selector dropped", `html[data-theme=dark] .card { color: white; }`, "", "data-theme"},
		{"root descendant selector dropped", `:root .card { color: white; }`, "", ":root"},
		{"at-rule import dropped", `@import url(evil.com); .card { color: red; }`, "color: red", "@import"},
		{"at-rule font-face dropped", `@font-face { font-family: x; } .card { color: red; }`, "color: red", "@font-face"},
		{"at-rule media dropped", `@media screen { .card { color: red; } } .card { color: blue; }`, "color: blue", "@media"},
		{"unparseable returns empty", `.card { color: `, "", "color"},
		{"svg element selector allowed", `path { fill: red; }`, ".deckshare-card path {", ""},
		{"svg fill none allowed", `.placeholder path { fill: none; stroke: currentColor; stroke-width: 1; }`, "fill: none", ""},
		{"svg fill url dropped", `path { fill: url(evil.com); color: red; }`, "color: red", "fill"},
		{"svg fill-rule enum enforced", `path { fill-rule: spiral; }`, "", "fill-rule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, dropped := SanitiseCSS(tt.css)
			s := string(out)
			if tt.wantOutput != "" && !strings.Contains(s, tt.wantOutput) {
				t.Errorf("output = %q, want substring %q (dropped=%v)", s, tt.wantOutput, dropped)
			}
			if tt.mustNotHave != "" && strings.Contains(s, tt.mustNotHave) {
				t.Errorf("output = %q must not contain %q", s, tt.mustNotHave)
			}
		})
	}
}

func TestSanitiseCSS_NoMarkupInOutput(t *testing.T) {
	out, _ := SanitiseCSS(`.card { color: red; }`)
	if strings.Contains(string(out), "<") || strings.Contains(string(out), ">") {
		t.Errorf("output contains markup: %q", out)
	}
}

// #276: colour declarations get a light-dark() variant after the original, which stays as the
// fallback; values that aren't one plain colour are left alone.
func TestSanitiseCSS_DarkModeColour(t *testing.T) {
	const flip = "calc(l + (0.95 - 1.75 * l) * clamp(0, calc(1 - c / 0.04), 1)) c h"
	tests := []struct {
		name string
		css  string
		want string // substring; "" means no light-dark() in the output
	}{
		{"hex colour", `.card { color: #000; }`, "color: #000;\n  color: light-dark(#000, oklch(from #000 " + flip + "));"},
		{"named colour", `.card { background-color: white; }`, "background-color: light-dark(white, oklch(from white " + flip + "));"},
		{"colour function", `.card { color: rgb(1, 2, 3); }`, "color: light-dark(rgb(1, 2, 3), oklch(from rgb(1, 2, 3) " + flip + "));"},
		{"uppercase name", `.card { color: WHITE; }`, "color: light-dark(WHITE, oklch(from WHITE " + flip + "));"},
		{"uppercase hex with alpha", `.card { color: #AABBCC80; }`, "color: light-dark(#AABBCC80, oklch(from #AABBCC80 " + flip + "));"},
		{"rgba function", `.card { color: rgba(0, 0, 0, 0.5); }`, "color: light-dark(rgba(0, 0, 0, 0.5), oklch(from rgba(0, 0, 0, 0.5) " + flip + "));"},
		{"hsla function", `.card { color: hsla(0, 0%, 20%, 0.5); }`, "color: light-dark(hsla(0, 0%, 20%, 0.5),"},
		{"border-color", `.card { border-color: #ccc; }`, "border-color: light-dark(#ccc,"},
		{"text-decoration-color", `.card { text-decoration-color: gray; }`, "text-decoration-color: light-dark(gray,"},
		{"transparent unchanged", `.card { background-color: transparent; }`, ""},
		{"currentcolor unchanged", `.card { border-color: currentcolor; }`, ""},
		{"inherit unchanged", `.card { color: inherit; }`, ""},
		{"multi-value border-color unchanged", `.card { border-color: red blue; }`, ""},
		{"shorthand unchanged", `.card { border: 1px solid #000; }`, ""},
		{"fill unchanged", `path { fill: #000; }`, ""},
		{"dropped value adds nothing", `.card { color: var(--x); }`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := SanitiseCSS(tt.css)
			s := string(out)
			if tt.want == "" {
				if strings.Contains(s, "light-dark") {
					t.Errorf("output = %q, want no light-dark()", s)
				}
				return
			}
			if !strings.Contains(s, tt.want) {
				t.Errorf("output = %q, want substring %q", s, tt.want)
			}
		})
	}
}
