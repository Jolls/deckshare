package render

import (
	"html"
	"html/template"
	"strings"
)

// TypeAnswerInput builds the answer-input widget for r.Type and splices it into r.HTML at the
// placeholder. It lives OUTSIDE sanitisation on purpose (architecture.md §8): the <input> it
// emits would either need a hole in the card allowlist or be stripped by it. Nothing here flows
// through sanitiseCardHTML; Expected is escaped with html.EscapeString and nothing else.
// Returns r.HTML unchanged when r.Type is nil.
func TypeAnswerInput(r Rendered) template.HTML {
	if r.Type == nil {
		return r.HTML
	}
	widget := `<input type="text" class="type-answer" data-expected="` + html.EscapeString(r.Type.Expected) +
		`" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" aria-label="Type the answer">`
	return template.HTML(strings.Replace(string(r.HTML), r.Type.Placeholder, widget, 1))
}

// TypeAnswerExpected splices the expected answer, escaped, into r.HTML at the placeholder -- the
// answer side's counterpart. A later reviewer session can replace this with a typed-vs-expected
// diff node; the placeholder contract is what makes that a one-function change.
func TypeAnswerExpected(r Rendered) template.HTML {
	if r.Type == nil {
		return r.HTML
	}
	widget := `<span class="type-answer-expected">` + html.EscapeString(r.Type.Expected) + `</span>`
	return template.HTML(strings.Replace(string(r.HTML), r.Type.Placeholder, widget, 1))
}
