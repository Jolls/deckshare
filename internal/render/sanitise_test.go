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
		"math", "style", "script", "template", "textarea", "title", "xmp", "noscript",
		"noembed", "iframe", "object", "embed", "plaintext", "form", "input", "button", "select",
		"option", "label", "fieldset", "base", "link", "meta", "frame", "frameset", "applet",
		"marquee", "audio", "video", "source", "track",
		// svg itself is now allowlisted (svgShapeElements) -- these are the SVG-specific
		// exclusions from that subset (TestSanitiseCardHTML_SVGShapes covers svg/math foreign
		// content and the paint/geometry attribute grammar).
		"foreignobject", "animate", "animatemotion", "animatetransform", "set", "use", "image",
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
		{"script inside allowed svg", `<svg><script>alert(1)</script></svg><p>ok</p>`, []string{"<script", "alert(1)"}},
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

// TestSanitiseCardHTML_SVGShapes covers svgShapeElements: the real-world placeholder icon from
// the "[BetterVectorMaps] US States" note type survives intact, and every excluded SVG construct
// (foreignObject, SMIL animation, use/image references, event handlers, id, url()-bearing paint)
// is neutralised rather than merely relocated.
func TestSanitiseCardHTML_SVGShapes(t *testing.T) {
	t.Run("realistic placeholder icon survives", func(t *testing.T) {
		in := `<svg class="placeholder" xmlns="http://www.w3.org/2000/svg" width="500" height="308" viewBox="0 0 500 308"><path d="m154 2s-151 44-152 152c-1 108 152 152 152 152h193s151-44 151-152c1-107-151-152-151-152z" fill="none" stroke="currentColor"/></svg>`
		out := sanitiseCardHTML(in)
		// bluemonday's tokenizer lowercases attribute names on output (it has no SVG-namespace
		// awareness), so "viewBox" comes out "viewbox" here. That is not a bug: a browser
		// re-parsing this HTML applies the HTML5 "adjust SVG attributes" table while inside <svg>
		// content and restores the correct camelCase DOM attribute name regardless of the source
		// casing -- viewBox is on that fixed table. xmlns is dropped entirely and needn't survive
		// either; the HTML parser assigns the SVG namespace to <svg> by tag alone.
		for _, want := range []string{"<svg", `viewbox="0 0 500 308"`, `width="500"`, "<path", `d="m154 2s`, `fill="none"`, `stroke="currentColor"`} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want substring %q", out, want)
			}
		}
	})

	tests := []struct {
		name        string
		in          string
		mustNotHave []string
	}{
		{"foreignObject embeds HTML", `<svg><foreignObject><div onclick="alert(1)">x</div></foreignObject></svg>`, []string{"foreignobject", "onclick", "alert(1)"}},
		{"animate rewrites href to javascript", `<svg><path d="M0 0"><animate attributeName="href" values="javascript:alert(1)" /></path></svg>`, []string{"<animate", "javascript:alert(1)"}},
		{"set rewrites attribute", `<svg><path d="M0 0"><set attributeName="fill" to="red" /></path></svg>`, []string{"<set"}},
		{"use references external doc", `<svg><use href="https://evil.example/x.svg#y" /></svg>`, []string{"<use", "evil.example"}},
		{"image loads remote resource", `<svg><image href="https://evil.example/track.png" /></svg>`, []string{"<image", "evil.example"}},
		{"onload on svg root", `<svg onload="alert(1)"><path d="M0 0"/></svg>`, []string{"onload", "alert(1)"}},
		{"id on svg content", `<svg><path id="answer" d="M0 0"/></svg>`, []string{`id="answer"`}},
		{"fill url javascript scheme", `<svg><path d="M0 0" fill="url(javascript:alert(1))"/></svg>`, []string{"javascript:", "url("}},
		{"fill arbitrary function call", `<svg><path d="M0 0" fill="expression(alert(1))"/></svg>`, []string{"expression"}},
		{"unbalanced tags across svg content", `<svg><path d="M0 0` + `"><script>` + `</svg>alert(1)</script>`, []string{"<script", "alert(1)"}},
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
