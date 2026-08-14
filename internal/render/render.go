package render

import (
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
)

// Field is one of a note's fields, in fields.ordinal order.
type Field struct {
	Name  string
	Value string // raw, author-supplied HTML -- never pre-escaped, never pre-sanitised
}

// Note is everything a render needs about a note. The caller builds it from db rows; this
// package never touches the database.
type Note struct {
	Fields   []Field
	Tags     []string // {{Tags}}
	NoteType string   // {{Type}}    -- note_types.name
	Deck     string   // {{Deck}}    -- full deck name
	Subdeck  string   // {{Subdeck}} -- last "::" component
}

// Template is one templates row's two formats plus its name ({{Card}}).
type Template struct {
	Name string
	Qfmt string
	Afmt string
}

// TypeAnswer describes a {{type:Field}} the template asked for. It is NOT html: Expected is
// plain text and has never been through the sanitiser, because an answer-input widget is not
// sanitisable card content (architecture.md §8). Whoever renders the widget escapes Expected
// itself. Placeholder appears verbatim exactly once in the matching Rendered.HTML.
type TypeAnswer struct {
	Field       string
	Expected    string
	Placeholder string
}

// Rendered is one side of one card: sanitised static HTML, plus -- separately -- the answer
// widget the caller must insert itself. HTML is safe to emit unescaped; that is the whole
// contract of this type.
type Rendered struct {
	HTML template.HTML
	Type *TypeAnswer // nil unless the side used {{type:Field}}
}

// Card is both sides of one card.
type Card struct {
	Question Rendered
	Answer   Rendered
}

// context carries per-side render state through evaluate and the filter dispatcher.
type context struct {
	note           Note
	fieldIndex     map[string]string
	ordinal        int32
	isCloze        bool
	cardName       string
	isQuestionSide bool
	frontSide      string      // question side's sanitised HTML; set before evaluating the answer
	frontSideType  *TypeAnswer // the question side's TypeAnswer, adopted if {{FrontSide}} is used
	typeAnswer     *TypeAnswer
}

// RenderCard renders card `ordinal` of `note` through `tmpl`. ordinal is cards.ordinal; for a
// cloze note type the active cloze number is ordinal+1 (#54 §0.2). isCloze is
// note_types.is_cloze.
func RenderCard(tmpl Template, note Note, ordinal int32, isCloze bool) (Card, error) {
	fieldIndex := make(map[string]string, len(note.Fields))
	for _, f := range note.Fields {
		fieldIndex[f.Name] = f.Value
	}

	qToks, err := tokenise(tmpl.Qfmt)
	if err != nil {
		return Card{}, err
	}
	aToks, err := tokenise(tmpl.Afmt)
	if err != nil {
		return Card{}, err
	}

	qCtx := &context{note: note, fieldIndex: fieldIndex, ordinal: ordinal, isCloze: isCloze, cardName: tmpl.Name, isQuestionSide: true}
	var qBuf strings.Builder
	if err := evaluate(qToks, qCtx, &qBuf); err != nil {
		return Card{}, err
	}
	qSanitised := sanitiseCardHTML(qBuf.String())
	question, err := finaliseRendered(qSanitised, qCtx.typeAnswer)
	if err != nil {
		return Card{}, err
	}

	aCtx := &context{
		note: note, fieldIndex: fieldIndex, ordinal: ordinal, isCloze: isCloze, cardName: tmpl.Name,
		isQuestionSide: false, frontSide: qSanitised, frontSideType: qCtx.typeAnswer,
	}
	var aBuf strings.Builder
	if err := evaluate(aToks, aCtx, &aBuf); err != nil {
		return Card{}, err
	}
	aSanitised := sanitiseCardHTML(aBuf.String())
	answer, err := finaliseRendered(aSanitised, aCtx.typeAnswer)
	if err != nil {
		return Card{}, err
	}

	return Card{Question: question, Answer: answer}, nil
}

// finaliseRendered wraps html in a Rendered, asserting the §3.4 placeholder invariant: the
// TypeAnswer placeholder occurs exactly once when set, zero times otherwise. A violation can
// only be a bug in this package.
func finaliseRendered(html string, ta *TypeAnswer) (Rendered, error) {
	if ta != nil {
		if n := strings.Count(html, ta.Placeholder); n != 1 {
			return Rendered{}, fmt.Errorf("render: type answer placeholder appeared %d times, want 1", n)
		}
	}
	return Rendered{HTML: template.HTML(html), Type: ta}, nil
}

type frame struct {
	name     string
	emitting bool
}

// evaluate walks toks, appending literal text and resolved field/filter output to out while
// every frame on the section stack is emitting. Section open/close tokens are always processed
// (never gated by emitting), so a malformed section inside a false branch is still an error
// (§3.1) -- a template that only breaks for some notes is worse than one that always breaks.
func evaluate(toks []token, ctx *context, out *strings.Builder) error {
	var stack []frame
	emitting := func() bool {
		for _, f := range stack {
			if !f.emitting {
				return false
			}
		}
		return true
	}

	for _, tok := range toks {
		switch tok.kind {
		case kindText:
			if emitting() {
				out.WriteString(tok.text)
			}

		case kindOpen, kindOpenNeg:
			parentEmitting := emitting()
			t := truthy(tok.name, ctx)
			if tok.kind == kindOpenNeg {
				t = !t
			}
			stack = append(stack, frame{name: tok.name, emitting: parentEmitting && t})

		case kindClose:
			if len(stack) == 0 {
				return fmt.Errorf("%w: {{/%s}} with no open section", ErrSectionMismatch, tok.name)
			}
			top := stack[len(stack)-1]
			if top.name != tok.name {
				return fmt.Errorf("%w: {{/%s}} does not match open {{#%s}}", ErrSectionMismatch, tok.name, top.name)
			}
			stack = stack[:len(stack)-1]

		case kindField:
			if !emitting() {
				continue
			}
			if tok.name == "FrontSide" && len(tok.filters) == 0 {
				writeFrontSide(ctx, out)
				continue
			}
			v, err := applyFilters(tok.name, tok.filters, ctx)
			if err != nil {
				return err
			}
			out.WriteString(v)
		}
	}
	if len(stack) > 0 {
		return fmt.Errorf("%w: {{#%s}} never closed", ErrUnclosedSection, stack[len(stack)-1].name)
	}
	return nil
}

func writeFrontSide(ctx *context, out *strings.Builder) {
	if ctx.isQuestionSide {
		out.WriteString("[unsupported: FrontSide on the question side]")
		return
	}
	out.WriteString(ctx.frontSide)
	if ctx.frontSideType == nil {
		return
	}
	if ctx.typeAnswer != nil {
		out.WriteString("[duplicate type: " + ctx.frontSideType.Field + "]")
		return
	}
	ctx.typeAnswer = ctx.frontSideType
}

var clozeNameRe = regexp.MustCompile(`^c(\d+)$`)

// lookupName resolves a field/section name: special names first, then note.Fields by exact,
// case-sensitive match.
func lookupName(name string, ctx *context) (string, bool) {
	switch name {
	case "Tags":
		return strings.Join(ctx.note.Tags, " "), true
	case "Type":
		return ctx.note.NoteType, true
	case "Deck":
		return ctx.note.Deck, true
	case "Subdeck":
		return ctx.note.Subdeck, true
	case "Card":
		return ctx.cardName, true
	}
	v, ok := ctx.fieldIndex[name]
	return v, ok
}

// truthy implements §3.1's section-condition rules.
func truthy(name string, ctx *context) bool {
	if m := clozeNameRe.FindStringSubmatch(name); m != nil {
		n, err := strconv.Atoi(m[1])
		return err == nil && ctx.isCloze && n == int(ctx.ordinal)+1
	}
	v, ok := lookupName(name, ctx)
	if !ok {
		return false
	}
	return strings.TrimSpace(stripHTMLTags(v)) != ""
}
