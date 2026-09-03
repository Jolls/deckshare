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
