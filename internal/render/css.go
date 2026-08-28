package render

import (
	"html/template"
	"regexp"
	"strings"

	"github.com/aymerick/douceur/css"
	"github.com/aymerick/douceur/parser"
)

// ScopeClass is the class the caller MUST put on the element wrapping a card's rendered HTML.
// Every selector SanitiseCSS emits is scoped to it, so one deck's note-type CSS cannot restyle
// the application around it.
const ScopeClass = "enshu-card"

// allowedCSSProperties is shared between the note-type CSS blob (SanitiseCSS) and inline
// style="" attributes on card HTML (sanitise.go) -- one allowlist, one place it can drift.
//
// Excluded on purpose, with the attack each closes: position/top/right/bottom/left/z-index (a
// shared deck's card overlaying the reviewer's rating buttons -- clickjacking against trusted
// UI); transform/animation/transition/will-change (the same overlay, animated); content (text
// injection via a pseudo-element); cursor/pointer-events/user-select (spoofing interactivity);
// filter/backdrop-filter/mix-blend-mode/clip-path (CSS side-channel pixel reads);
// background/background-image/list-style-image/border-image/mask/src (URL-bearing -- the value
// grammar refuses url() anyway, so keeping them off this list makes the grammar a second line,
// not the only one); custom properties (--*, since var() is a bare '(').
var allowedCSSProperties = []string{
	"color", "background-color", "opacity",
	"font", "font-family", "font-size", "font-style", "font-weight", "font-variant", "line-height",
	"letter-spacing", "word-spacing", "text-align", "text-decoration", "text-decoration-color",
	"text-indent", "text-transform", "white-space", "word-break", "overflow-wrap", "direction",
	"unicode-bidi", "vertical-align", "list-style-type", "list-style-position",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
	"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left",
	"border-color", "border-style", "border-width", "border-radius", "border-collapse", "border-spacing",
	"width", "height", "min-width", "min-height", "max-width", "max-height",
	"display", "overflow", "text-shadow", "box-shadow", "ruby-align", "ruby-position",
	// SVG presentation properties -- meaningful only on svgShapeElements (sanitise.go), inert
	// elsewhere. Same value grammar as everything above: no bare '(' but the four colour
	// functions, so fill/stroke can never carry a url() reference to an external or internal id.
	"fill", "stroke", "stroke-width", "fill-opacity", "stroke-opacity",
	"stroke-linecap", "stroke-linejoin", "stroke-dasharray", "fill-rule",
}

var allowedCSSPropertySet = func() map[string]bool {
	m := make(map[string]bool, len(allowedCSSProperties))
	for _, p := range allowedCSSProperties {
		m[p] = true
	}
	return m
}()

// hexColourPattern is the #RGB / #RRGGBB / #RRGGBBAA grammar, unanchored so sanitise.go's
// svgPaintRe can embed it in its alternation -- one definition of what a hex colour is.
const hexColourPattern = `#[0-9A-Fa-f]{3}([0-9A-Fa-f]{3}([0-9A-Fa-f]{2})?)?`

var (
	forbiddenValueCharsRe = regexp.MustCompile(`[<>\\;{}@"']`)
	colourFuncRe          = regexp.MustCompile(`(?i)^(rgb|rgba|hsl|hsla)$`)
	hexColourRe           = regexp.MustCompile(`^` + hexColourPattern + `$`)
	lengthTokenRe         = regexp.MustCompile(`(?i)^-?[0-9]+(\.[0-9]+)?(px|em|rem|%|ex|ch|vw|vh|pt|cm|mm|in)?$`)
	fontFamilyRe          = regexp.MustCompile(`^[A-Za-z0-9 ,_-]+$`)

	displayEnum = map[string]bool{
		"block": true, "inline": true, "inline-block": true, "flex": true, "grid": true,
		"none": true, "table": true, "table-row": true, "table-cell": true, "list-item": true,
	}
	namedColours = map[string]bool{
		"black": true, "white": true, "red": true, "green": true, "blue": true, "yellow": true,
		"orange": true, "purple": true, "pink": true, "gray": true, "grey": true, "brown": true,
		"cyan": true, "magenta": true, "lime": true, "navy": true, "teal": true, "maroon": true,
		"olive": true, "silver": true, "gold": true, "indigo": true, "violet": true,
		"transparent": true, "currentcolor": true, "inherit": true, "initial": true, "unset": true,
	}
	keywordEnumProperties = map[string]map[string]bool{
		"display":             displayEnum,
		"text-align":          {"left": true, "right": true, "center": true, "justify": true, "start": true, "end": true},
		"font-style":          {"normal": true, "italic": true, "oblique": true},
		"font-weight":         {"normal": true, "bold": true, "bolder": true, "lighter": true, "100": true, "200": true, "300": true, "400": true, "500": true, "600": true, "700": true, "800": true, "900": true},
		"white-space":         {"normal": true, "nowrap": true, "pre": true, "pre-wrap": true, "pre-line": true},
		"overflow":            {"visible": true, "hidden": true, "scroll": true, "auto": true, "clip": true},
		"border-style":        {"none": true, "solid": true, "dashed": true, "dotted": true, "double": true, "groove": true, "ridge": true, "inset": true, "outset": true, "hidden": true},
		"list-style-type":     {"none": true, "disc": true, "circle": true, "square": true, "decimal": true, "lower-alpha": true, "upper-alpha": true, "lower-roman": true, "upper-roman": true},
		"direction":           {"ltr": true, "rtl": true},
		"unicode-bidi":        {"normal": true, "embed": true, "isolate": true, "bidi-override": true, "isolate-override": true, "plaintext": true},
		"vertical-align":      {"baseline": true, "sub": true, "super": true, "top": true, "text-top": true, "middle": true, "bottom": true, "text-bottom": true},
		"text-transform":      {"none": true, "capitalize": true, "uppercase": true, "lowercase": true},
		"word-break":          {"normal": true, "break-all": true, "keep-all": true, "break-word": true},
		"text-decoration":     {"none": true, "underline": true, "overline": true, "line-through": true},
		"border-collapse":     {"collapse": true, "separate": true},
		"list-style-position": {"inside": true, "outside": true},
		"overflow-wrap":       {"normal": true, "break-word": true, "anywhere": true},
		"fill":                {"none": true},
		"stroke":              {"none": true},
		"stroke-linecap":      {"butt": true, "round": true, "square": true},
		"stroke-linejoin":     {"miter": true, "round": true, "bevel": true},
		"fill-rule":           {"nonzero": true, "evenodd": true},
	}
)

// cssValueOK reports whether value is acceptable for prop. The bare-'(' rule is architecture.md
// §8's third bullet: no function call except the four colour functions, so url(), expression(),
// image-set(), var(), attr(), calc() -- and anything added to CSS later -- cannot appear, even
// if a URL-accepting property is mistakenly added to allowedCSSProperties one day.
func cssValueOK(prop, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7E {
			return false
		}
	}
	if forbiddenValueCharsRe.MatchString(value) {
		return false
	}
	if strings.Contains(value, "/*") || strings.Contains(value, "*/") {
		return false
	}
	if !bareParenOK(value) {
		return false
	}
	return propertyShapeOK(prop, value)
}

// bareParenOK walks value and rejects any '(' whose preceding identifier (no intervening
// whitespace) is not exactly rgb/rgba/hsl/hsla, and any unbalanced or nested parenthesisation.
func bareParenOK(value string) bool {
	depth := 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '(':
			if depth > 0 {
				return false // nested parens: never valid here
			}
			depth++
			j := i - 1
			for j >= 0 && isIdentChar(value[j]) {
				j--
			}
			ident := value[j+1 : i]
			if !colourFuncRe.MatchString(ident) {
				return false
			}
		case ')':
			if depth == 0 {
				return false
			}
			depth--
		}
	}
	return depth == 0
}

func isIdentChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '-'
}

func propertyShapeOK(prop, value string) bool {
	lower := strings.ToLower(value)
	if lower == "inherit" || lower == "initial" || lower == "unset" {
		return true
	}
	if enum, ok := keywordEnumProperties[prop]; ok {
		if enum[lower] {
			return true
		}
		return tokenListOK(value)
	}
	if isColourToken(value) {
		return true
	}
	if lengthTokenRe.MatchString(value) {
		return true
	}
	if prop == "font-family" {
		return fontFamilyRe.MatchString(value)
	}
	// Shorthand/list-shaped properties: every space- or comma-separated token must itself be a
	// safe token (colour, length, or a known keyword). This is deliberately permissive relative
	// to CSS's real grammar for these properties -- steps 1-4 above are the actual security
	// boundary; this step is a correctness/defence-in-depth pass, not the only line held.
	return tokenListOK(value)
}

// isColourToken reports whether value is a recognised colour. A value containing "(" has
// already passed bareParenOK by the time this runs, which only accepts rgb/rgba/hsl/hsla as the
// function name -- so presence of "(" here is sufficient without re-parsing the identifier.
func isColourToken(value string) bool {
	if hexColourRe.MatchString(value) {
		return true
	}
	if namedColours[strings.ToLower(value)] {
		return true
	}
	return strings.Contains(value, "(")
}

func tokenListOK(value string) bool {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ' ' || r == ',' })
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		lf := strings.ToLower(f)
		if lengthTokenRe.MatchString(f) || isColourToken(f) || lf == "auto" || lf == "none" || lf == "normal" {
			continue
		}
		matchedEnum := false
		for _, enum := range keywordEnumProperties {
			if enum[lf] {
				matchedEnum = true
				break
			}
		}
		if !matchedEnum {
			return false
		}
	}
	return true
}

// SanitiseCSS rewrites a note type's CSS blob to the property/value/selector allowlist above,
// scoped to ScopeClass. It never fails: anything it cannot validate is dropped and described in
// dropped, which a note-type editor can show the author. The result is re-emitted from the
// parsed AST, never copied from the input, and is guaranteed to contain no '<' or '>'.
func SanitiseCSS(raw string) (template.CSS, []string) {
	sheet, err := parser.Parse(raw)
	if err != nil {
		return "", []string{"CSS could not be parsed"}
	}

	var out strings.Builder
	var dropped []string

	for _, rule := range sheet.Rules {
		if rule.Kind != css.QualifiedRule { // at-rules are dropped entirely
			name := rule.Name
			if name == "" {
				name = "at-rule"
			}
			dropped = append(dropped, name+" dropped: at-rules are not supported")
			continue
		}

		selectors := make([]string, 0, len(rule.Selectors))
		for _, sel := range rule.Selectors {
			rewritten, ok := sanitiseSelector(sel)
			if !ok {
				dropped = append(dropped, "selector dropped: "+sel)
				continue
			}
			selectors = append(selectors, rewritten)
		}
		if len(selectors) == 0 {
			continue
		}

		decls := make([]string, 0, len(rule.Declarations))
		for _, d := range rule.Declarations {
			prop := strings.ToLower(strings.TrimSpace(d.Property))
			if !allowedCSSPropertySet[prop] {
				dropped = append(dropped, "property dropped: "+d.Property)
				continue
			}
			if !cssValueOK(prop, d.Value) {
				dropped = append(dropped, "value dropped: "+d.Property+": "+d.Value)
				continue
			}
			decls = append(decls, prop+": "+strings.TrimSpace(d.Value)+";")
		}
		if len(decls) == 0 {
			continue
		}

		out.WriteString(strings.Join(selectors, ", "))
		out.WriteString(" {\n")
		for _, decl := range decls {
			out.WriteString("  ")
			out.WriteString(decl)
			out.WriteString("\n")
		}
		out.WriteString("}\n")
	}

	result := out.String()
	if strings.ContainsAny(result, "<>") {
		return "", append(dropped, "output contained markup")
	}
	return template.CSS(result), dropped
}

var (
	selectorTokenRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	classTokenRe    = regexp.MustCompile(`^\.[A-Za-z0-9_-]+$`)
	pseudoClasses   = map[string]bool{
		":hover": true, ":focus": true, ":first-child": true, ":last-child": true,
		":only-child": true, ":empty": true,
	}
)

// sanitiseSelector validates and rewrites a single selector, scoping it to ScopeClass. Refused:
// id selectors, attribute selectors, the universal selector, :root/html/body, any pseudo-element
// or functional pseudo-class, and anything containing '@', a backslash, braces, quotes, '<' or a
// comment delimiter.
func sanitiseSelector(sel string) (string, bool) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return "", false
	}
	if strings.ContainsAny(sel, "@\\{}\"'<>") || strings.Contains(sel, "/*") {
		return "", false
	}
	if strings.Contains(sel, "::") {
		return "", false // pseudo-element
	}
	if strings.Contains(sel, "[") || strings.Contains(sel, "(") {
		return "", false // attribute selector or functional pseudo-class
	}

	compounds := strings.Fields(strings.NewReplacer(">", " > ", "+", " + ", "~", " ~ ").Replace(sel))
	for _, c := range compounds {
		if c == ">" || c == "+" || c == "~" {
			continue
		}
		if !compoundSelectorOK(c) {
			return "", false
		}
	}

	if strings.HasPrefix(sel, ".card") && (len(sel) == 5 || !isIdentChar(sel[5]) && sel[5] != '_' && sel[5] != '0') {
		return "." + ScopeClass + sel[5:], true
	}
	return "." + ScopeClass + " " + sel, true
}

func compoundSelectorOK(compound string) bool {
	if compound == "*" {
		return false
	}
	// Split into element/class parts on '.', keeping the leading element name (if any).
	parts := strings.Split(compound, ".")
	if parts[0] != "" {
		lower := strings.ToLower(parts[0])
		if lower == "html" || lower == "body" || lower == ":root" {
			return false
		}
		if strings.HasPrefix(parts[0], ":") {
			if !pseudoClassOK(parts[0]) {
				return false
			}
		} else if !selectorTokenRe.MatchString(parts[0]) || !elementAllowed(strings.ToLower(parts[0])) {
			return false
		}
	}
	for _, p := range parts[1:] {
		// p may carry a trailing pseudo-class, e.g. "night_mode:hover"
		cls, pseudo, _ := strings.Cut(p, ":")
		if !classTokenRe.MatchString("." + cls) {
			return false
		}
		if pseudo != "" && !pseudoClassOK(":"+pseudo) {
			return false
		}
	}
	return true
}

func pseudoClassOK(p string) bool {
	return pseudoClasses[p]
}

func elementAllowed(name string) bool {
	for _, e := range allAllowedElements {
		if e == name {
			return true
		}
	}
	return false
}
