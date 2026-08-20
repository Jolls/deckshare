package render

import (
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
)

// sanitisableElements is the prose-content allowlist -- also (together with svgShapeElements
// below) the selector allowlist in css.go's elementAllowed (§4.1 / §5.3 of
// docs/plans/55-note-type-rendering-sanitisation.md are the same element set).
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

// svgShapeElements is the static-shape SVG subset: pure geometry, drawn with no scripting and no
// reference to anything else -- no <use>, <image>, gradients, patterns, masks or clipPath, so
// there is nothing an href/xlink:href could point at, internally or externally. That is what
// makes this subset safe to allowlist at all: every excluded SVG element (<script>,
// <foreignObject>, <animate>/<animateMotion>/<animateTransform>/<set> -- SMIL's own
// javascript:-href vector, historically a sanitiser bypass elsewhere) is excluded because it
// either scripts or references, not because of a per-element judgement call. Broader SVG support
// (gradients, <use>, <text>) is a separate, deliberately unscoped decision -- see the tracking
// issue linked from docs/plans/55-note-type-rendering-sanitisation.md.
//
// Kept separate from sanitisableElements (not folded in) because its attribute grammar --
// coordinate/path data -- has nothing in common with the prose-content elements there; the two
// lists mean the same thing to bluemonday and css.go's elementAllowed checks both.
var svgShapeElements = []string{"svg", "g", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon"}

var classAttrRe = regexp.MustCompile(`^[A-Za-z0-9 _-]{0,200}$`)
var langAttrRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,35}$`)
var numericAttrRe = regexp.MustCompile(`^[0-9]{1,3}$`)
var dirAttrRe = regexp.MustCompile(`^(ltr|rtl|auto)$`)
var openAttrRe = regexp.MustCompile(`^(|open)$`)

// SVG geometry attributes: character-class allowlists, not grammars -- every one of these excludes
// every HTML-meaningful byte (<, >, ", ', &, (, ), ;) outright, so even a malformed value can only
// ever fail to parse as a shape, never break out of the attribute it sits in. lengthTokenRe
// (css.go) covers the plain single-number attributes (x/y/width/height/... below); these three
// cover the multi-number ones bluemonday's plain regex Matching() has to validate in one shot.
var svgViewBoxRe = regexp.MustCompile(`^[0-9.,+\-\s]{1,100}$`)
var svgPointsRe = regexp.MustCompile(`^[0-9.,+\-\s]{1,1000}$`)
var svgPathDataRe = regexp.MustCompile(`^[MmLlHhVvCcSsQqTtAaZz0-9.,+\-\seE]{1,1000}$`)
var svgLinecapRe = regexp.MustCompile(`^(butt|round|square)$`)
var svgLinejoinRe = regexp.MustCompile(`^(miter|round|bevel)$`)
var svgFillRuleRe = regexp.MustCompile(`^(nonzero|evenodd)$`)
var svgDasharrayRe = regexp.MustCompile(`^[0-9.,+\-\s%]{1,500}$`)

// svgPaintRe is fill/stroke's value grammar: bluemonday's plain attribute Matching() takes a
// regexp, not the cssValueOK/isColourToken functions style="" reuses (those need
// MatchingHandler, which only the style-property builder has) -- so this is built once from the
// same namedColours table css.go already validates style="" colours against, one source of
// truth even though the two paths can't share the same function call.
var svgPaintRe = sync.OnceValue(func() *regexp.Regexp {
	names := make([]string, 0, len(namedColours))
	for n := range namedColours {
		names = append(names, regexp.QuoteMeta(n))
	}
	return regexp.MustCompile(`(?i)^(none|` + strings.Join(names, "|") + `|#[0-9A-Fa-f]{3}|#[0-9A-Fa-f]{6}|#[0-9A-Fa-f]{8})$`)
})

var cardPolicy = sync.OnceValue(func() *bluemonday.Policy {
	p := bluemonday.NewPolicy() // NOT UGCPolicy: start from nothing and add.
	p.AllowElements(sanitisableElements...)
	p.AllowNoAttrs().OnElements(sanitisableElements...)
	p.AllowElements(svgShapeElements...)
	p.AllowNoAttrs().OnElements(svgShapeElements...)

	// Raw-text / escapable-raw-text / foreign-content elements are never added to the allowlist
	// above, so bluemonday strips the element -- but by default keeps its TEXT content, which is
	// exactly the mXSS vector for style/textarea/title. Drop the content too. "math" stays here in
	// full (no MathML support at all); "svg" doesn't -- it's the scoped allowlist above now --
	// but "foreignobject" is added: it is never itself allowlisted (it exists to embed arbitrary
	// HTML inside SVG, the one thing this subset must not do), and dropping its content rather
	// than promoting it up a level means an author can't rely on it as a second place to stash
	// markup that survives sanitisation.
	p.SkipElementsContent(
		"script", "style", "template", "textarea", "title",
		"math", "foreignobject", "iframe", "object", "embed", "noscript", "xmp", "noembed",
	)

	p.AllowAttrs("class").Matching(classAttrRe).Globally()
	p.AllowAttrs("dir").Matching(dirAttrRe).Globally()
	p.AllowAttrs("lang").Matching(langAttrRe).Globally()
	p.AllowAttrs("title", "alt").Globally()
	p.AllowAttrs("colspan", "rowspan", "span").Matching(numericAttrRe).OnElements("td", "th", "col", "colgroup")
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("src", "width", "height").OnElements("img")
	p.AllowAttrs("open").Matching(openAttrRe).OnElements("details")

	// svgShapeElements' geometry: id, href/xlink:href, and every event/animation attribute stay
	// off every allowlist below on purpose -- see svgShapeElements' comment for why that's what
	// makes this subset safe rather than a per-attribute judgement call.
	p.AllowAttrs("viewBox").Matching(svgViewBoxRe).OnElements("svg")
	p.AllowAttrs("width", "height").Matching(lengthTokenRe).OnElements("svg", "rect")
	p.AllowAttrs("x", "y", "rx", "ry").Matching(lengthTokenRe).OnElements("rect")
	p.AllowAttrs("cx", "cy", "r").Matching(lengthTokenRe).OnElements("circle")
	p.AllowAttrs("cx", "cy", "rx", "ry").Matching(lengthTokenRe).OnElements("ellipse")
	p.AllowAttrs("x1", "y1", "x2", "y2").Matching(lengthTokenRe).OnElements("line")
	p.AllowAttrs("points").Matching(svgPointsRe).OnElements("polyline", "polygon")
	p.AllowAttrs("d").Matching(svgPathDataRe).OnElements("path")
	p.AllowAttrs("fill", "stroke").Matching(svgPaintRe()).OnElements(svgShapeElements...)
	p.AllowAttrs("stroke-width", "opacity", "fill-opacity", "stroke-opacity").Matching(lengthTokenRe).OnElements(svgShapeElements...)
	p.AllowAttrs("stroke-linecap").Matching(svgLinecapRe).OnElements(svgShapeElements...)
	p.AllowAttrs("stroke-linejoin").Matching(svgLinejoinRe).OnElements(svgShapeElements...)
	p.AllowAttrs("fill-rule").Matching(svgFillRuleRe).OnElements(svgShapeElements...)
	p.AllowAttrs("stroke-dasharray").Matching(svgDasharrayRe).OnElements(svgShapeElements...)

	p.AllowURLSchemes("http", "https", "mailto") // allowlist: javascript:/data:/vbscript:/file: excluded by omission
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(true) // Anki media convention (img src="x.jpg"); see plan Open question 3
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)

	p.AllowAttrs("style").OnElements(sanitisableElements...)
	p.AllowAttrs("style").OnElements(svgShapeElements...)
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
