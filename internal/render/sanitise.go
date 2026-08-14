package render

import (
	"regexp"
	"sync"

	"github.com/microcosm-cc/bluemonday"
)

// sanitisableElements is the full allowlist -- also the selector allowlist in css.go (§4.1 /
// §5.3 of docs/plans/55-note-type-rendering-sanitisation.md are the same element set).
var sanitisableElements = []string{
	"b", "strong", "i", "em", "u", "s", "strike", "sub", "sup", "small", "mark",
	"br", "hr", "p", "div", "span", "pre", "code", "kbd", "blockquote",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"ul", "ol", "li", "dl", "dt", "dd",
	"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "colgroup", "col",
	"ruby", "rt", "rb", "rp",
	"details", "summary",
	"a", "img", "figure", "figcaption",
}

var classAttrRe = regexp.MustCompile(`^[A-Za-z0-9 _-]{0,200}$`)
var langAttrRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,35}$`)
var numericAttrRe = regexp.MustCompile(`^[0-9]{1,3}$`)
var dirAttrRe = regexp.MustCompile(`^(ltr|rtl|auto)$`)
var openAttrRe = regexp.MustCompile(`^(|open)$`)

var cardPolicy = sync.OnceValue(func() *bluemonday.Policy {
	p := bluemonday.NewPolicy() // NOT UGCPolicy: start from nothing and add.
	p.AllowElements(sanitisableElements...)
	p.AllowNoAttrs().OnElements(sanitisableElements...)

	// Raw-text / escapable-raw-text / foreign-content elements are never added to the allowlist
	// above, so bluemonday strips the element -- but by default keeps its TEXT content, which is
	// exactly the mXSS vector for style/textarea/title. Drop the content too.
	p.SkipElementsContent(
		"script", "style", "template", "textarea", "title",
		"svg", "math", "iframe", "object", "embed", "noscript", "xmp", "noembed",
	)

	p.AllowAttrs("class").Matching(classAttrRe).Globally()
	p.AllowAttrs("dir").Matching(dirAttrRe).Globally()
	p.AllowAttrs("lang").Matching(langAttrRe).Globally()
	p.AllowAttrs("title", "alt").Globally()
	p.AllowAttrs("colspan", "rowspan", "span").Matching(numericAttrRe).OnElements("td", "th", "col", "colgroup")
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("src", "width", "height").OnElements("img")
	p.AllowAttrs("open").Matching(openAttrRe).OnElements("details")

	p.AllowURLSchemes("http", "https", "mailto") // allowlist: javascript:/data:/vbscript:/file: excluded by omission
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(true) // Anki media convention (img src="x.jpg"); see plan Open question 3
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)

	p.AllowAttrs("style").OnElements(sanitisableElements...)
	for _, prop := range allowedCSSProperties {
		prop := prop
		p.AllowStyles(prop).MatchingHandler(func(v string) bool {
			return cssValueOK(prop, v)
		}).Globally()
	}

	return p
})

// sanitiseCardHTML is the single sanitisation point for assembled card HTML. Unexported: there
// is no legitimate caller outside this package (§0.9) -- exporting it invites sanitising the
// {{type:Field}} answer widget with it, which is exactly the conflation architecture.md §8 warns
// against.
func sanitiseCardHTML(raw string) string {
	return cardPolicy().Sanitize(raw)
}
