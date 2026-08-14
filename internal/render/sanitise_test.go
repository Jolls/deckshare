package render

import (
	"strings"
	"testing"
)

// TestSanitiseCardHTML_ForbiddenElements iterates the entire forbidden-element list from §4.1
// and asserts each is absent from the output -- adding one of these to the allowlist should fail
// this test, not pass review unnoticed.
func TestSanitiseCardHTML_ForbiddenElements(t *testing.T) {
	forbidden := []string{
		"svg", "math", "style", "script", "template", "textarea", "title", "xmp", "noscript",
		"noembed", "iframe", "object", "embed", "plaintext", "form", "input", "button", "select",
		"option", "label", "fieldset", "base", "link", "meta", "frame", "frameset", "applet",
		"marquee", "audio", "video", "source", "track",
	}
	for _, el := range forbidden {
		t.Run(el, func(t *testing.T) {
			out := sanitiseCardHTML("<" + el + ">payload</" + el + "><p>safe</p>")
			if strings.Contains(strings.ToLower(out), "<"+el) {
				t.Errorf("output still contains <%s>: %q", el, out)
			}
			if !strings.Contains(out, "safe") {
				t.Errorf("output should keep the allowed sibling content: %q", out)
			}
		})
	}
}

func TestSanitiseCardHTML_XSSFixtures(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		mustNotHave []string
	}{
		{"script tag", `<script>alert(1)</script><p>ok</p>`, []string{"<script", "alert(1)"}},
		{"svg foreign content", `<svg><script>alert(1)</script></svg><p>ok</p>`, []string{"<svg", "alert(1)"}},
		{"math foreign content", `<math><mtext><script>alert(1)</script></mtext></math><p>ok</p>`, []string{"<math", "alert(1)"}},
		{"style element", `<style>body{}</style><p>ok</p>`, []string{"<style"}},
		{"noscript+title mutation", `<noscript><title><style></noscript><img src=x onerror=alert(1)>`, []string{"onerror", "alert(1)"}},
		{"javascript href", `<a href="javascript:alert(1)">x</a>`, []string{"javascript:"}},
		{"javascript href obfuscated", `<a href="&#106;avascript:alert(1)">x</a>`, []string{"javascript:"}},
		{"data uri href", `<a href="data:text/html,alert(1)">x</a>`, []string{"data:"}},
		{"data uri img src", `<img src="data:image/png;base64,AAAA">`, []string{"data:"}},
		{"vbscript href", `<a href="vbscript:alert(1)">x</a>`, []string{"vbscript:"}},
		{"file scheme href", `<a href="file:///etc/passwd">x</a>`, []string{"file:"}},
		{"onerror attribute", `<img src="x.jpg" onerror="alert(1)">`, []string{"onerror"}},
		{"onload attribute", `<svg onload="alert(1)"></svg>`, []string{"onload"}},
		{"formaction", `<button formaction="javascript:alert(1)">x</button>`, []string{"formaction", "<button"}},
		{"id attribute dropped", `<div id="app-root">x</div>`, []string{`id="app-root"`}},
		{"input element dropped", `<input type="text" value="hijack">`, []string{"<input"}},
		{"iframe", `<iframe src="//evil.example"></iframe>`, []string{"<iframe"}},
		{"object embed", `<object data="evil.swf"></object><embed src="evil.swf">`, []string{"<object", "<embed"}},
		{"base tag", `<base href="//evil.example/">`, []string{"<base"}},
		{"meta refresh", `<meta http-equiv="refresh" content="0;url=//evil.example">`, []string{"<meta"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := sanitiseCardHTML(tt.in)
			for _, bad := range tt.mustNotHave {
				if strings.Contains(strings.ToLower(out), strings.ToLower(bad)) {
					t.Errorf("output = %q still contains %q", out, bad)
				}
			}
		})
	}
}

func TestSanitiseCardHTML_KeepsSafeConstructs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"keeps cloze span", `<span class="cloze">[...]</span>`, `<span class="cloze">[...]</span>`},
		{"keeps ruby", `<ruby>漢<rt>かん</rt></ruby>`, "<ruby>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := sanitiseCardHTML(tt.in)
			if !strings.Contains(out, tt.want) {
				t.Errorf("output = %q, want to contain %q", out, tt.want)
			}
		})
	}
}

func TestSanitiseCardHTML_StyleMixed(t *testing.T) {
	out := sanitiseCardHTML(`<p style="color:red;position:fixed">x</p>`)
	if !strings.Contains(out, "color:red") && !strings.Contains(out, "color: red") {
		t.Errorf("allowed style property dropped: %q", out)
	}
	if strings.Contains(out, "position") {
		t.Errorf("disallowed style property survived: %q", out)
	}
}

func TestSanitiseCardHTML_StyleURLDropped(t *testing.T) {
	out := sanitiseCardHTML(`<p style="background-color:rgb(1,2,3);color:url(javascript:alert(1))">x</p>`)
	if strings.Contains(out, "url(") {
		t.Errorf("url() in style survived: %q", out)
	}
}

func TestSanitiseCardHTML_Idempotent(t *testing.T) {
	once := sanitiseCardHTML(`<p style="color:red">safe <b>bold</b></p>`)
	twice := sanitiseCardHTML(once)
	if once != twice {
		t.Errorf("sanitiser is not idempotent:\n1st: %q\n2nd: %q", once, twice)
	}
}

func TestSanitiseCardHTML_UnbalancedTagsAcrossFields(t *testing.T) {
	// Simulates two field values concatenated during evaluate(): one opens a tag the other
	// closes. Sanitising the assembled buffer (not each field separately) must not let the
	// browser re-balance a raw-text element across the join.
	assembled := `<div>` + `<script>` + `</div>` + `alert(1)</script>`
	out := sanitiseCardHTML(assembled)
	if strings.Contains(out, "<script") || strings.Contains(out, "alert(1)") {
		t.Errorf("unbalanced-tag assembly survived sanitisation: %q", out)
	}
}
