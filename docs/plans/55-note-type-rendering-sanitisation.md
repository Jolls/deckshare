# Plan: #55 — Note-type template rendering (§8) + card-content HTML/CSS sanitisation

Phase 1, build-order step 6 (architecture.md §11). Owns **100%** of `internal/render/`'s rendering
and sanitisation logic: the `{{...}}` template mini-language, cloze fan-out by ordinal, the
card-HTML allowlist sanitiser, the note-type CSS-blob sanitiser, and the `{{type:Field}}`
answer-widget boundary.

**Sequencing.** This lands *after* #54 (deck/note-type/note/card CRUD), on the same branch, in the
same tree. #54 has already created `internal/render/cloze.go` with

```go
func ClozeOrdinals(fields []string) []int32   // distinct cloze NUMBERS, ascending
```

and `internal/render/cloze_test.go`. This issue **extends** that file; it does not add a second
cloze parser. `ClozeOrdinals`'s exported signature and semantics are frozen (`internal/http/notes.go`
calls it for card generation, and #54's table test must keep passing unchanged) — only its body is
re-pointed at the shared scanner added here (§3.5).

**Out of scope.** No handlers, no routes, no DB queries, no page templates. `internal/render/` stays
a pure package: no `internal/db` import, no `net/http` import, no I/O. Its callers build the input
structs. Media resolution (`<img src="x.jpg">` → `/media/{sha256}`) and `[sound:…]` belong to
#60/#34 (Open question 3). The consumer is #56, the reviewer (§7).

---

## 0. Resolved decisions

Every item below is decided. Genuinely-unresolved items are in **Open questions** at the end and
nowhere else.

### 0.1 Sanitiser dependency: `github.com/microcosm-cc/bluemonday`

Chosen, matching architecture.md §8's own naming ("`bluemonday` or equivalent is the Go analogue of
`sanitize-html`").

Why it, concretely — the alternatives were checked against this exact checklist, not in general:

| Candidate | Verdict |
|---|---|
| `github.com/microcosm-cc/bluemonday` | **Chosen.** The only well-maintained Go allowlist sanitiser with all four primitives the checklist needs as first-class policy: element allowlist, per-attribute URL **scheme allowlist** (`AllowURLSchemes`), per-property **style** policies with a custom value predicate (`AllowStyles(...).MatchingHandler`), and *content* skipping for raw-text elements (`SkipElementsContent`). Built on `golang.org/x/net/html` — a spec-compliant parser, re-serialised from the parsed tree, which is the property that closes the mXSS gap the checklist's first bullet is about. |
| `html/template` alone | Not a substitute. It escapes *interpolated* values; card content is HTML that must survive as markup, so there is nothing for it to escape. It is a second layer (§4.4), not the first. |
| `golang.org/x/net/html` hand-rolled | Rejected: it is what bluemonday already is, minus the scheme/style/skip-content policy layer this checklist needs — i.e. writing the same code with fewer eyes on it. |
| `github.com/aymerick/douceur` | Not an HTML sanitiser. It **is** used here, but for the CSS blob only (§0.2) — it is already in bluemonday's dependency tree, so it costs no new module. |
| `mrsaints/go-sanitize` and similar | Rejected: unmaintained and/or denylist-shaped, which is exactly what the checklist's second bullet forbids. |

Concretely:

```
go get github.com/microcosm-cc/bluemonday@v1.0.27   # latest v1.0.x at implementation time
go get github.com/aymerick/douceur@v0.2.0           # promote from indirect to direct
go mod tidy && go build ./...
```

New module tree: `microcosm-cc/bluemonday`, `aymerick/douceur`, `gorilla/css`, and
`golang.org/x/net`. CI has network for `go build`, so nothing else changes. Commit `go.mod`/`go.sum`.

### 0.2 CSS blob sanitiser: `douceur/parser` + our own value grammar, **not** bluemonday

bluemonday sanitises *HTML*; it has no entry point that takes a standalone stylesheet. The note-type
`css` column (`note_types.css`, docs/schema.md) is a full stylesheet with selectors and rules, so it
needs its own pass. `github.com/aymerick/douceur/parser` parses a stylesheet into a `css.Stylesheet`
AST (`Rules[]` → `Selectors[]` + `Declarations[]{Property, Value}`), which is exactly the shape
needed to **re-emit from tokens rather than pass source text through** (§5).

The *value* grammar (§5.2) is one function shared by both the CSS blob and inline `style=""`
attributes, so "no bare `(` outside `rgb`/`rgba`/`hsl`/`hsla`" is implemented once and cannot drift
between the two surfaces.

### 0.3 Sanitise on render, never on write

`note_types.css`, `templates.qfmt`/`afmt`, and `notes.fields` are all stored **raw**, exactly as the
author typed them. Sanitisation happens in `internal/render/` at render time, per architecture.md
§8's heading. Consequences, all intended: a later tightening of the allowlist applies retroactively
to existing content; a user's own CSS is never silently destroyed in the editor; and there is exactly
one place where the allowlist is enforced, so no write path can bypass it. #54's note-type form is
**not** modified by this issue.

### 0.4 The active cloze number is `ordinal + 1`

#54 §0.2 files cloze cards as `cards.ordinal = clozeNumber - 1` (Anki's `ord` convention). So a
card's active cloze number is `ordinal + 1`. `RenderCard` takes the card's `ordinal` (`int32`,
matching `db.Card.Ordinal`) and derives the cloze number internally. No caller ever passes a cloze
number.

### 0.5 Cloze processing is the `cloze:` filter, not a global pass

Ground truth (docs/anki-schema.md: note types carry `type: 0 = standard, 1 = cloze`; the cloze
template's `qfmt` is `{{cloze:Text}}`): cloze markers are only interpreted where the `cloze:` filter
is applied. A plain `{{Text}}` in a cloze note type renders the field with its `{{c1::…}}` markers
still visible — that is Anki's behaviour and it is what we do.

`isCloze` (from `note_types.is_cloze`) is still a parameter, and it does exactly one thing: when
`false`, the `cloze:` filter reveals **every** number as plain text on both sides and blanks nothing.
Rationale: on a non-cloze note type `cards.ordinal` is a *template* ordinal, so `ordinal + 1` is not
a cloze number and treating it as one would blank an arbitrary marker.

### 0.6 Error policy: structural errors fail; unknown names degrade

- **Returned as an `error`** (the whole side fails to render): unclosed section, `{{/X}}` closing a
  section opened as `{{#Y}}`, `{{/X}}` with no open section, unterminated `{{` at end of input.
  These are template bugs the note-type author must fix, and rendering half a card silently is worse.
- **Rendered as inert escaped text** (no error): unknown field name → `[unknown field: Name]`;
  unknown filter → `[unknown filter: name]`; a second `{{type:…}}` on one side →
  `[duplicate type: Field]`; an unsupported construct (§0.8) → `[unsupported: …]`. This matches
  Anki's own non-fatal behaviour for these and keeps a mistyped field name from bricking a deck.

Exported sentinels in `errors.go`: `ErrUnclosedSection`, `ErrSectionMismatch`, `ErrUnterminatedTag`,
each wrapped with the offending name/offset via `fmt.Errorf("%w: …", …)`.

### 0.7 Constructs implemented

| Construct | Behaviour |
|---|---|
| `{{Field}}` | Field value, inserted as HTML (it *is* HTML), then globally sanitised (§4.5). |
| `{{#Field}}…{{/Field}}` | Body emitted iff the field is non-empty. Nestable; stack-matched by name. |
| `{{^Field}}…{{/Field}}` | Body emitted iff the field is empty. Same stack, same close tag. |
| `{{#cN}}` / `{{^cN}}` | Truthy iff `N == ordinal+1`. Anki's per-cloze-card conditional. |
| `{{FrontSide}}` | Answer side only. §3.4. |
| `{{text:Field}}` | HTML stripped, entities decoded, result HTML-escaped → plain text. |
| `{{furigana:Field}}` | `kanji[reading]` → `<ruby>kanji<rt>reading</rt></ruby>`. |
| `{{kanji:Field}}` | Same parse, base text only (readings dropped). |
| `{{kana:Field}}` | Same parse, readings only. |
| `{{hint:Field}}` | `<details><summary>Field</summary>value</details>` — no JS, so no `onclick`, so no allowlist hole. Empty field → nothing. |
| `{{cloze:Field}}` | §3.5. |
| `{{type:Field}}` | §6 — placeholder + `TypeAnswer`, never HTML. |
| Special names | `{{Tags}}`, `{{Type}}`, `{{Deck}}`, `{{Subdeck}}`, `{{Card}}` — resolved from `Note`/`Template` metadata, not `notes.fields`. Included because imported Anki templates use them and omitting them would render `[unknown field: Deck]` on real decks. |
| Filter chaining | `{{text:cloze:Text}}` — filters apply **right to left** (innermost nearest the field name), Anki's order. |

`kana`/`kanji` are included because they are the *same parser* as `furigana` (three exports, one
function); this is not scope creep, it is one construct with three projections.

### 0.8 Constructs deliberately NOT implemented

Each renders as inert escaped text `[unsupported: <tag>]` — visible to the deck author, harmless,
and greppable — rather than being silently dropped:

`{{tts:…}}`, `{{cloze-only:…}}`, `{{type:cloze:Field}}`, `{{type:nc:Field}}`, LaTeX
(`[latex]…[/latex]`, `[$]…[/$]`, `[$$]…[/$$]` inside field content), and `[sound:…]` (left as
literal text until #60 exists). See Open question 4.

The "front of this card is blank" check Anki performs is **not** implemented — it is a
reviewer-level UX affordance, not a rendering construct, and #56 can add it against
`Rendered.HTML == ""`.

### 0.9 Package boundary — exact public API

`internal/render/render.go`:

```go
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

// RenderCard renders card `ordinal` of `note` through `tmpl`. ordinal is cards.ordinal; for a
// cloze note type the active cloze number is ordinal+1 (#54 §0.2). isCloze is
// note_types.is_cloze.
func RenderCard(tmpl Template, note Note, ordinal int32, isCloze bool) (Card, error)
```

`internal/render/typeanswer.go`:

```go
// TypeAnswerInput builds the answer-input widget for r.Type and splices it into r.HTML at the
// placeholder. It lives OUTSIDE sanitisation on purpose (architecture.md §8): the <input> it
// emits would either need a hole in the card allowlist or be stripped by it. Nothing here
// flows through sanitiseCardHTML; Expected is escaped with html.EscapeString and nothing else.
// Returns r.HTML unchanged when r.Type is nil.
func TypeAnswerInput(r Rendered) template.HTML

// TypeAnswerExpected splices the expected answer, escaped, into r.HTML at the placeholder --
// the answer side's counterpart. #56 replaces this with a typed-vs-expected diff node; the
// placeholder contract is what makes that a one-function change.
func TypeAnswerExpected(r Rendered) template.HTML
```

`internal/render/css.go`:

```go
// ScopeClass is the class the caller MUST put on the element wrapping a card's rendered HTML.
// Every selector SanitiseCSS emits is scoped to it, so one deck's note-type CSS cannot restyle
// the application around it.
const ScopeClass = "enshu-card"

// SanitiseCSS rewrites a note type's CSS blob to the property/value/selector allowlist in
// docs/plans/55-note-type-rendering-sanitisation.md §5, scoped to ScopeClass. It never fails:
// anything it cannot validate is dropped and described in `dropped`, which a note-type editor
// can show the author. The result is re-emitted from the parsed AST, never copied from the
// input, and is guaranteed to contain no '<'.
func SanitiseCSS(raw string) (css template.CSS, dropped []string)
```

That is the entire exported surface: `RenderCard`, `TypeAnswerInput`, `TypeAnswerExpected`,
`SanitiseCSS`, `ScopeClass`, `ClozeOrdinals` (#54), the types above, and the three error sentinels.
`sanitiseCardHTML` stays **unexported** — there is no legitimate caller outside this package, and
exporting it invites someone to sanitise the answer widget with it.

---

## 1. Files

**New**
- `internal/render/token.go` + `token_test.go` — the `{{…}}` tokeniser.
- `internal/render/render.go` + `render_test.go` — sections, substitution, `{{FrontSide}}`, `RenderCard`.
- `internal/render/filters.go` + `filters_test.go` — `text`/`furigana`/`kanji`/`kana`/`hint`/`cloze`/`type` dispatch, HTML stripping.
- `internal/render/sanitise.go` + `sanitise_test.go` — the bluemonday policy.
- `internal/render/css.go` + `css_test.go` — the CSS-blob sanitiser and the shared value grammar.
- `internal/render/typeanswer.go` + `typeanswer_test.go` — the post-sanitisation widget insertion.
- `internal/render/errors.go`
- `internal/render/golden_test.go` — the golden-file harness (§8).
- `internal/render/testdata/…` — one directory per construct (§8).

**Edited**
- `internal/render/cloze.go` — add the span scanner; `ClozeOrdinals` re-pointed at it, signature and
  semantics unchanged (#54's `cloze_test.go` must pass **untouched**; if it needs editing, the
  refactor is wrong).
- `internal/render/doc.go` — expand to state the package contract: pure, sanitises on render, and the
  `Rendered.HTML` / `TypeAnswer` split, with §7's call snippet.
- `go.mod`, `go.sum`.
- `docs/architecture.md` (§1 "step 6 has landed" paragraph; §8 gains a pointer to this plan),
  `docs/routes.md` (record what was decided about Open question 2 — see Open question 1 below),
  `CHANGELOG.md`.

---

## 2. `token.go` — the tokeniser

One non-nested pass, per architecture.md §8 ("Template tags don't nest braces"). A hand-written
scanner rather than a regex over the whole template, because the tokens are trivial and the error
offsets matter.

```go
type kind int
const (
    kindText    kind = iota // literal template text
    kindField               // {{Name}} or {{filter:...:Name}}
    kindOpen                // {{#Name}}
    kindOpenNeg             // {{^Name}}
    kindClose               // {{/Name}}
)

type token struct {
    kind    kind
    text    string   // kindText: the literal run
    name    string   // field or section name, trimmed
    filters []string // outermost first; {{text:cloze:X}} -> ["text","cloze"]
    offset  int      // byte offset of "{{", for error messages
}

func tokenise(format string) ([]token, error)
```

Rules, pinned:

- Scan for `{{`; everything before it is one `kindText`. Find the next `}}`; no nesting, no escaping,
  no triple braces (Anki has none). Missing `}}` → `ErrUnterminatedTag`.
- Inner text is split on `:`. The **last** segment is the name; every earlier segment is a filter,
  outermost first. `{{text:cloze:Text}}` → name `Text`, filters `["text","cloze"]`.
- A leading `#`, `^`, or `/` on the *first* character of the inner text makes it a section token; the
  rest is the name. Sections take no filters — `{{#text:X}}` is a section named `text:X`, which
  simply never matches a field and is therefore never truthy. (Anki has no filtered sections either.)
- Name and filter segments are `strings.TrimSpace`d. An empty name (`{{}}`, `{{ }}`) becomes a
  `kindText` token of the literal source text — no tag, no error.
- `{{!` … `}}` (Anki comment) → dropped, emits nothing.
- The tokeniser is **content-blind**: it never looks inside a field value, so a `{{c1::…}}` marker in
  *content* is invisible to it. That is the §8 invariant that makes one flat pass sufficient.

## 3. `render.go` — evaluation

### 3.1 Section stack

```go
func evaluate(toks []token, ctx *context, out *strings.Builder) error
```

A `[]frame{name string, emitting bool}` stack. On `kindOpen`/`kindOpenNeg`, push with
`emitting = parentEmitting && truthy(name)` (negated for `kindOpenNeg`). On `kindClose`, the top
frame's `name` must equal the close name (`ErrSectionMismatch`) — matched **by field name**, per
§8 — then pop; an empty stack at a close is `ErrSectionMismatch` too. A non-empty stack at end of
input is `ErrUnclosedSection`. Field/text tokens append only while every frame on the stack is
emitting — but tokens inside a false section are still *parsed*, so a malformed section in a
non-taken branch is still an error. A template that only breaks for some notes is worse than one
that always breaks.

`truthy(name)`:
- `^c(\d+)$` → that number equals `ordinal+1`.
- special names (`Tags`, `Type`, `Deck`, `Subdeck`, `Card`) → their value is non-empty.
- otherwise → the field exists **and** `fieldHasContent(value)` (filters.go): strip HTML and trim,
  same as originally planned to stop a `<br>`-only field from counting as present — ⚠️ but first
  substitute any `<img src="...">` with its `src`, matching Anki's own rule that a field
  consisting of only a media reference is not empty. Missed in the original design here (an
  image-only field silently stripped to nothing, so a question side gated entirely on one — a
  real, common template pattern — rendered blank); fixed once found via manual testing, not a
  deliberate divergence.
- unknown name → false. (§0.6's `[unknown field]` text applies to substitution tokens, not sections:
  a section on a mistyped field just never fires, as in Anki.)

### 3.2 Field substitution

`kindField` with no filters: look up the name (special names first, then `note.Fields` by exact,
case-sensitive match — Anki field names are case-sensitive), append the raw value. Unknown →
`html.EscapeString("[unknown field: " + name + "]")`.

### 3.3 Filter application

Filters apply right-to-left over the field's raw value, each `func(string, *context) string`.
`type:` is special-cased in the dispatcher and is only legal as the *outermost* filter; anywhere
else it renders `[unsupported: type: not outermost]`.

### 3.4 `{{FrontSide}}`

`RenderCard` renders the question side first, then the answer side with `ctx.frontSide` set to the
question's **already-sanitised** HTML. On the answer side, `{{FrontSide}}` appends that string
verbatim into the pre-sanitisation buffer; the answer's own sanitise pass runs over it again, which
is a no-op because the policy is idempotent (asserted by a fixture). Re-sanitising rather than
splicing post-sanitisation is deliberate: it keeps exactly one code path that can produce
`Rendered.HTML`.

**Placeholder handling across `FrontSide`.** If the question carried a `type:` placeholder, the copy
inserted by `{{FrontSide}}` carries it too — which is what makes Anki show the comparison where the
input box was. Rule: the answer side adopts that placeholder as its own `TypeAnswer` (same `Field`,
same `Expected`, same `Placeholder`), and an *additional* explicit `{{type:…}}` on the answer side
then renders `[duplicate type: Field]` per §0.6. Invariant, asserted after every render: the
placeholder occurs **exactly once** in `Rendered.HTML` when `Type != nil`, and zero times when
`Type == nil`. A violation returns an `error` — it can only be a bug in this package.

On a question side, `{{FrontSide}}` renders as `[unsupported: FrontSide on the question side]`.

### 3.5 Cloze — `cloze.go`

Extend the existing file with a real scanner, which then also backs `ClozeOrdinals`:

```go
// clozeSpan is one {{cN::text::hint}} marker in field content.
type clozeSpan struct {
    num        int
    start, end int    // offsets of "{{" and one past "}}"
    text       string // the hidden content, verbatim (may itself contain nested spans)
    hint       string // "" when absent
}

// scanCloze returns every top-level cloze marker in s, in source order.
func scanCloze(s string) []clozeSpan
```

Scanner rules:
- Match `{{c` + digits + `::` at a `{{`. Then walk forward tracking `{{`/`}}` depth so a **nested**
  `{{c2::…}}` inside a `{{c1::…}}` closes correctly. (Anki supports nesting; a non-counting regex
  stops at the inner `}}` and truncates the outer span.)
- The hidden text ends at the first `::` **at depth 0** inside the marker; whatever follows up to the
  closing `}}` is the hint. A further `::` at depth 0 is part of the hint (Anki splits on the first).
- An unterminated marker is not a marker: it stays literal text.
- Number parse: `< 1` dropped, `> math.MaxInt32` dropped — identical to #54's rules, now in one place.

`ClozeOrdinals` becomes: `scanCloze` each field, recursing into `span.text` for nested markers,
collect `num`, dedupe, sort ascending, return `[]int32`. **Same output as #54's regex for every case
its test covers**, plus correct handling of nesting.

Rendering (`clozeFilter(value string, ctx *context) string`) — the rule §8 calls out as easy to get
half-right:

| | active number (`ordinal+1`) | every other number |
|---|---|---|
| Question side | `<span class="cloze">[...]</span>`, or `<span class="cloze">[hint]</span>` when a hint is present | **revealed as plain text** — the hidden text, unwrapped, hint discarded |
| Answer side | `<span class="cloze">text</span>` (highlighted) | **revealed as plain text** — identical to the question side |
| `isCloze == false` | revealed as plain text | revealed as plain text |

"Revealed as plain text" means the marker's *text* is emitted as-is (it is HTML) with no wrapper — so
a multi-cloze note's other clozes read as context on both sides, which is the failure mode §8 names.
Processing is recursive: a nested marker inside a revealed span is processed by the same rules, so
`{{c1::a {{c2::b}}}}` on card 2 shows `a [...]`.

`[...]` and `[hint]` are emitted with literal square brackets; the hint text is HTML-escaped (it is
author text, not markup).

## 4. `sanitise.go` — the card-HTML policy

One `*bluemonday.Policy`, built once in a `sync.OnceValue` and reused (a policy is read-only after
construction and safe for concurrent use).

```go
func sanitiseCardHTML(raw string) string   // unexported on purpose (§0.9)
```

### 4.1 Elements — the allowlist, and the exclusions that are the point

```go
p := bluemonday.NewPolicy()   // NOT UGCPolicy: start from nothing and add
p.AllowElements(
    "b","strong","i","em","u","s","strike","sub","sup","small","mark",
    "br","hr","p","div","span","pre","code","kbd","blockquote",
    "h1","h2","h3","h4","h5","h6",
    "ul","ol","li","dl","dt","dd",
    "table","thead","tbody","tfoot","tr","th","td","caption","colgroup","col",
    "ruby","rt","rb","rp",           // {{furigana:}}
    "details","summary",             // {{hint:}}
    "a","img","figure","figcaption",
)
p.AllowNoAttrs().OnElements(/* every element above that carries no allowed attribute */)
```

**Never added — each name is a checklist item, not an oversight**, and a table test asserts every one
of them is stripped:

- Foreign content: `math`. ⚠️ `svg` **used to be here too, and no longer is** — a later change
  (internal/render/sanitise.go's `svgShapeElements`) allowlists a static-shape SVG subset (`svg`,
  `g`, `path`, `rect`, `circle`, `ellipse`, `line`, `polyline`, `polygon`; geometry and
  fill/stroke/opacity attributes only, no `<use>`/`<image>`/gradients/references of any kind).
  `TestSanitiseCardHTML_ForbiddenElements` no longer includes `svg` in its forbidden set for this
  reason; `TestSanitiseCardHTML_SVGShapes` covers the new subset's own attack surface instead.
  Broader SVG support is tracked separately (issue #121).
- Raw-text / escapable-raw-text: `style`, `script`, `template`, `textarea`, `title`, `xmp`,
  `noscript`, `noembed`, `iframe`, `object`, `embed`, `plaintext`.
- Interactive/form: `form`, `input`, `button`, `select`, `option`, `label`, `fieldset` — the
  `{{type:Field}}` widget is the *only* interactive element on a card and it does not come from here
  (§6).
- `base`, `link`, `meta`, `frame`, `frameset`, `applet`, `marquee`, `audio`, `video`, `source`,
  `track`.
- SVG-specific, excluded from the static-shape subset above for the same "scripts or references
  something" reason as the rest of this list: `foreignObject`, `animate`/`animateMotion`/
  `animateTransform`/`set` (SMIL's own `javascript:`-href vector), `use`, `image`.

Plus `p.SkipElementsContent("script","style","template","textarea","title","math","foreignobject",
"iframe","object","embed","noscript","xmp","noembed")` — for these the *text inside* is dropped
too, not lifted into the output where a second parse could re-read it as markup. (`svg` is no
longer in this list — see above; `foreignobject` was added to it even though it was never on the
allowlist, as a zero-cost defensive measure against SVG's canonical embed-arbitrary-HTML vector.)
And `AllowComments` and `AllowDocType` left off (asserted, since conditional comments are an mXSS
vector).

### 4.2 Attributes and the URL scheme allowlist

```go
p.AllowAttrs("class").Matching(regexp.MustCompile(`^[A-Za-z0-9 _-]{0,200}$`)).Globally()
p.AllowAttrs("dir").MatchingEnum("ltr","rtl","auto").Globally()
p.AllowAttrs("lang").Matching(regexp.MustCompile(`^[A-Za-z0-9-]{1,35}$`)).Globally()
p.AllowAttrs("title","alt").Globally()                      // text-only; bluemonday escapes them
p.AllowAttrs("colspan","rowspan","span").Matching(numeric).OnElements("td","th","col","colgroup")
p.AllowAttrs("href").OnElements("a")
p.AllowAttrs("src","width","height").OnElements("img")
p.AllowAttrs("open").MatchingEnum("","open").OnElements("details")

p.AllowURLSchemes("http","https","mailto")   // ALLOWLIST: javascript:, data:, vbscript:, file:
                                             // are excluded BY OMISSION, never named
p.RequireParseableURLs(true)                 // an unparseable URL drops the attribute
p.AllowRelativeURLs(true)                    // media filenames: img src="x.jpg" (Open question 3)
p.RequireNoFollowOnLinks(true)
p.AddTargetBlankToFullyQualifiedLinks(true)  // bluemonday adds rel="noopener" with it
```

`p.AllowDataURIImages()` is **not** called — it is the one bluemonday helper that would reopen
`data:`, and its absence has its own fixture.

`id`, `name`, and every `on*` handler are absent from the allowlist and therefore dropped: `id`
because a card could otherwise collide with an application element id, `on*` because they are script.

### 4.3 Inline `style` — the same value grammar as the CSS blob

```go
p.AllowAttrs("style").OnElements(/* the same element list as 4.1 */)
for _, prop := range allowedCSSProperties {          // 5.1, one shared table
    prop := prop
    p.AllowStyles(prop).MatchingHandler(func(v string) bool {
        return cssValueOK(prop, v)                   // 5.2, one shared function
    }).Globally()
}
```

**Verify at implementation time** that bluemonday requires *both* the `style` attribute allowance and
the per-property style policies, and that an unlisted property is dropped while listed siblings in
the same attribute survive. This interaction is the easiest thing here to get subtly wrong, so it has
its own fixture (`sanitise-style-mixed`): `style="color:red;position:fixed"` must come out as
`style="color:red"`.

### 4.4 `html/template` is the second layer, not the first

`Rendered.HTML` is `template.HTML`, so `html/template` will not re-escape it — that is the point of
the type. What still gets escaped: everything else on the page (deck names, field labels) through
normal interpolation, `TypeAnswer.Expected` through `html.EscapeString` at insertion, and
`SanitiseCSS`'s `template.CSS` result landing in a `style` element context where `html/template` runs
its own CSS-context check. Two independent layers, neither relied on alone.

### 4.5 Order of operations, per side

1. `tokenise(format)`
2. `evaluate(...)` — a `strings.Builder` of assembled, **unsanitised** HTML (field values raw, filter
   output escaped where the filter produces text, cloze wrappers inserted, `type:` placeholder
   inserted)
3. `sanitiseCardHTML(buf)` — the single sanitisation point
4. placeholder-count assertion (3.4)
5. wrap in `Rendered`

Sanitising the **assembled** document, not each field in isolation, is required for correctness: a
field ending mid-tag and the next beginning with its close is only resolvable once they are adjacent,
and a per-field pass would balance them separately and produce markup different from what the browser
would build.

## 5. `css.go` — the note-type CSS blob

```go
func SanitiseCSS(raw string) (template.CSS, []string)
```

Pipeline: `parser.Parse(raw)` (douceur) — walk `Stylesheet.Rules` — for each **qualified** rule,
filter its selectors (5.3) and its declarations (5.1/5.2) — re-emit with a `strings.Builder` from the
validated pieces only. A parse error returns `("", []string{"CSS could not be parsed"})`: a blob that
does not parse is not partially salvaged.

**At-rules are dropped, all of them**, each with a `dropped` entry naming it: `@import` (fetches a
remote stylesheet — a beacon and a bypass), `@font-face` (needs `url()`, which the value grammar bans
outright), `@media`/`@supports`/`@keyframes` (no Phase 1 need; `@keyframes` plus `animation` is a
route to covering application UI). See Open question 2.

### 5.1 Property allowlist (shared with inline `style`)

```go
var allowedCSSProperties = []string{
    "color","background-color","opacity",
    "font","font-family","font-size","font-style","font-weight","font-variant","line-height",
    "letter-spacing","word-spacing","text-align","text-decoration","text-decoration-color",
    "text-indent","text-transform","white-space","word-break","overflow-wrap","direction",
    "unicode-bidi","vertical-align","list-style-type","list-style-position",
    "margin","margin-top","margin-right","margin-bottom","margin-left",
    "padding","padding-top","padding-right","padding-bottom","padding-left",
    "border","border-top","border-right","border-bottom","border-left",
    "border-color","border-style","border-width","border-radius","border-collapse","border-spacing",
    "width","height","min-width","min-height","max-width","max-height",
    "display","overflow","text-shadow","box-shadow","ruby-align","ruby-position",
}
```

**Excluded on purpose, with the attack each closes:** `position`/`top`/`right`/`bottom`/`left`/
`z-index` (a shared deck's card overlaying the reviewer's rating buttons — clickjacking against UI
the user trusts), `transform`/`animation`/`transition`/`will-change` (the same overlay, animated),
`content` (text injection via a pseudo-element, though pseudo-elements are already refused in 5.3),
`cursor`/`pointer-events`/`user-select` (spoofing interactivity),
`filter`/`backdrop-filter`/`mix-blend-mode`/`clip-path` (historical CSS side-channel pixel reads),
`background`/`background-image`/`list-style-image`/`border-image`/`mask`/`src` (URL-bearing — the
value grammar would refuse them anyway, so keeping them off the *list* makes the grammar a second
line rather than the only one, which is exactly what section 8's third bullet asks for), `all`, and
every custom property (`--*`, since `var()` is a bare `(`).

`display` additionally takes a value enum (`block inline inline-block flex grid none table
table-row table-cell list-item`), so `display:contents` — which strips an element from the
accessibility tree — is out.

### 5.2 The value grammar — where the bare `(` rule lives

```go
// cssValueOK reports whether value is acceptable for prop. The bare-'(' rule is architecture.md
// section 8's third bullet: no function call except the four colour functions, so url(),
// expression(), image-set(), var(), attr(), calc() -- and anything added to CSS later -- cannot
// appear, even if a URL-accepting property is mistakenly added to allowedCSSProperties one day.
func cssValueOK(prop, value string) bool
```

Applied in order; any failure rejects the whole declaration:

1. **Length**: `len(value) <= 512`.
2. **Character class**: every byte printable ASCII (0x20-0x7E). Rejects control characters, NUL and
   non-ASCII — the latter because CSS identifier escapes and homoglyph tricks need it and no allowed
   value does.
3. **Forbidden characters**, any occurrence: `<`, `>`, backslash, `;`, `{`, `}`, `@`, double quote,
   single quote, and the comment delimiters. The backslash ban alone kills escape smuggling such as
   `\75 rl(...)`; the `<` ban alone guarantees 5.5's no-markup output; `;` and braces stop
   declaration- and rule-injection.
4. **Bare-paren scan**: walk the value; at every `(`, read backwards over `[A-Za-z-]` for the
   identifier immediately preceding it — no whitespace permitted between identifier and paren, so a
   space before `(` is itself a rejection. Lowercased, it must be exactly `rgb`, `rgba`, `hsl` or
   `hsla`. The parenthesised body must contain no further `(`. Every `(` must have a matching `)`,
   balanced and non-nested; a `)` with no open `(` rejects. Any other outcome rejects.
5. **Per-property shape**: a `map[string]*regexp.Regexp` (or value enum) per property — colours
   (3/6/8-digit hex, the CSS named colours, or a colour function already validated by step 4),
   lengths (`-?[0-9]+(\.[0-9]+)?(px|em|rem|%|ex|ch|vw|vh|pt|cm|mm|in)?` plus bare `0`), keyword enums
   for `display`/`text-align`/`font-style`/`font-weight`/`white-space`/`overflow`/`border-style`/
   `list-style-type`/`direction`/`unicode-bidi`/`vertical-align`/`text-transform`/`word-break`, and a
   "space- or comma-separated list of the above" for the shorthands (`font`, `margin`, `padding`,
   `border`, `text-shadow`, `box-shadow`). `font-family` allows unquoted family names matching
   `^[A-Za-z0-9 ,_-]+$` (step 3 already banned quote characters, so a family whose name contains a
   space is written unquoted; dropping the declaration is the safe failure).
6. `!important` is stripped before validation and **not** re-emitted — a note type must not be able
   to outrank the application's own stylesheet.

Steps 1-4 are `prop`-independent and run on the inline-`style` path too, which is what makes "no bare
`(`" a property of the whole system rather than of one file.

### 5.3 Selectors

Each selector is validated then rewritten; a rule with **zero** surviving selectors is dropped whole
(never emitted with an empty prelude):

- Allowed tokens: element names **from the 4.1 element allowlist**, a class (`\.[A-Za-z0-9_-]+`), the
  combinators space, `>`, `+`, `~`, the comma separator, and the non-functional pseudo-classes
  `:hover`, `:focus`, `:first-child`, `:last-child`, `:only-child`, `:empty`.
- Refused: id selectors (they target application elements), attribute selectors (they can exfiltrate
  attribute values through a URL-bearing property — belt and braces with the URL ban), the universal
  selector, `:root`, `html`, `body`, any pseudo-element, any functional pseudo-class
  (`:nth-child(...)`, `:not(...)`, `:has(...)` — they contain `(`, and the same rule applies as in
  values), and anything containing `@`, a backslash, braces, quotes, `<` or a comment delimiter.
- **Scoping rewrite**, exactly:
  - a selector whose first compound is `.card` — Anki's root class, the overwhelmingly common case in
    real note types — has that `.card` **replaced** by `.enshu-card`: `.card` becomes `.enshu-card`,
    `.card .x` becomes `.enshu-card .x`, `.card.night_mode` becomes `.enshu-card.night_mode`;
  - every other selector is **prefixed**: `S` becomes `.enshu-card S`.
  - The caller therefore wraps card HTML in an element with `class="enshu-card card"`, so both
    rewritten root rules and un-prefixed `.card`-descendant rules land where the author intended.

### 5.4 Where the sanitised CSS goes

Into a `style` element in the page head, as `template.CSS`, on whichever page shows a card (#56's
reviewer page). That is not a contradiction of 4.1's "no `style` element": the ban is on `style`
*inside sanitised card content*, where a browser re-parses its contents as raw text. This one is
server-generated from a validated AST, not attacker-shaped markup.

### 5.5 The no-markup guarantee

`SanitiseCSS` output is assembled solely from validated selectors, property names taken from the
fixed `allowedCSSProperties` slice, validated values, and the literal characters `{`, `}`, `:`, `;`,
`,` and newline. No input substring reaches the output un-validated. A final defensive check —
`strings.ContainsAny(out, "<>")` returns `("", dropped + ["output contained markup"])` — makes a bug
anywhere above fail closed rather than fail into a `</style>` breakout, and has its own fixture.

## 6. `{{type:Field}}` — the boundary, precisely

**During render** (`filters.go`), `{{type:Field}}` does exactly three things:

1. Compute `Expected = strings.TrimSpace(html.UnescapeString(stripHTML(fieldValue)))` — plain text,
   Anki's own comparison basis.
2. Generate `Placeholder = "ENSHUTYPEANSWER" + hex(16 bytes from crypto/rand) + "ENSHUEND"`. Per
   render, unguessable, ASCII-only and free of any character HTML-escaping would alter, so it passes
   through `sanitiseCardHTML` byte-identical (asserted by a fixture). Random rather than constant
   because a constant token could be typed into a note field by an author and hijack the insertion
   point.
3. Append the placeholder to the buffer as text, and set `ctx.typeAnswer`.

`{{type:Field}}` **emits no HTML at all.** No `input` element, no wrapper, nothing that the sanitiser
could strip and nothing that needs a hole in the allowlist. This is the mechanical form of section
8's fourth bullet.

**After render**, the caller — #56's reviewer, or a template test — calls
`render.TypeAnswerInput(rendered)` (question side) or `render.TypeAnswerExpected(rendered)` (answer
side), each of which:

- returns `r.HTML` unchanged when `r.Type == nil`, so a caller that always calls it is correct;
- otherwise does `strings.Replace(string(r.HTML), r.Type.Placeholder, widget, 1)` and returns
  `template.HTML`;
- builds `widget` as an `input` element with
  `type="text" class="type-answer" data-expected="..." autocomplete="off" autocapitalize="off"
  autocorrect="off" spellcheck="false" aria-label="Type the answer"` (question side), or a
  `span class="type-answer-expected"` (answer side), with the expected answer through
  `html.EscapeString` and **nothing else**.

Why a caller cannot conflate the two:

- `sanitiseCardHTML` is unexported, so no caller can run the widget through it even deliberately.
- `TypeAnswer.Expected` is a `string`, not `template.HTML`, so a caller pasting it into a page
  template gets it escaped by `html/template` — the safe failure.
- The widget builders live in `typeanswer.go`, which imports neither `bluemonday` nor the sanitiser —
  asserted by a test that parses the file's import block with `go/parser`, making the boundary a
  checked fact rather than a comment.
- `Rendered.HTML` alone is always renderable and always safe; a caller who forgets the insertion gets
  a *visible* absence (no input box, a stray placeholder token on screen), not a silent security
  hole — the failure direction section 8 asks for.

## 7. Integration point (no new route in this issue)

Nothing in #54's handlers calls `internal/render/` beyond `ClozeOrdinals`, and routes.md's Open
question 2 leaves a card-preview route explicitly undecided, so **#55 adds no route** — see Open
question 1. What it does add is a pinned contract for #56:

```go
// #56, GET /decks/{id}/review, per card in the batch:
card, err := render.RenderCard(tmpl, note, dbCard.Ordinal, noteType.IsCloze)
q := render.TypeAnswerInput(card.Question)         // question side
a := render.TypeAnswerExpected(card.Answer)        // answer side
css, dropped := render.SanitiseCSS(noteType.Css)   // once per note type per page, never per card
// page: <style>{{.CSS}}</style>
//       <article hidden data-card-id class="enshu-card card">{{.Q}}</article>
```

`doc.go` records exactly that snippet, so the first reviewer session does not have to re-derive the
call order or the `enshu-card card` wrapper.

## 8. Golden-file tests

Harness (`golden_test.go`): one directory per case under `internal/render/testdata/render/<case>/`:

```
case.json     {"qfmt":..., "afmt":..., "fields":[{"name":..,"value":..}], "tags":[..],
               "noteType":.., "deck":.., "subdeck":.., "cardName":.., "ordinal":0,
               "isCloze":false, "wantErr":""}
want_q.html   expected Rendered.HTML for the question side
want_a.html   expected Rendered.HTML for the answer side
```

Driven by `filepath.WalkDir` over `testdata/render`, with an `-update` flag that rewrites the
`want_*` files. Normalisation before comparison: the returned `TypeAnswer.Placeholder` is replaced
with the literal `TYPEANSWER`, so a `crypto/rand` nonce does not make goldens unstable; a `type:`
case's `want_q.html` therefore contains `TYPEANSWER`. Cases with `wantErr` set assert `errors.Is`
against the named sentinel and have no `want_*` files.

CSS cases live in `testdata/css/<case>.css` + `<case>.want.css` + optional `<case>.dropped.txt`.

**One fixture per construct** — the directory names are the checklist:

*Template language*
`field-plain`, `field-html-passthrough`, `field-unknown-name`, `field-missing-from-note`,
`special-tags`, `special-type-deck-subdeck-card`,
`section-truthy`, `section-empty-field`, `section-whitespace-and-html-only-field-is-empty`,
`section-inverted`, `section-nested`, `section-unclosed` (err), `section-mismatched-close` (err),
`section-close-without-open` (err), `section-cN-active`, `section-cN-inactive`,
`tag-unterminated` (err), `comment-tag`, `empty-tag-is-literal`.

*Filters*
`filter-text`, `filter-text-strips-tags-and-decodes-entities`, `filter-furigana`,
`filter-furigana-multiple-and-bare-text`, `filter-kanji`, `filter-kana`, `filter-hint`,
`filter-hint-empty-field`, `filter-chained-text-cloze`, `filter-unknown-name`,
`filter-unsupported-tts`, `filter-unsupported-type-cloze`.

*FrontSide*
`frontside-basic`, `frontside-carries-type-placeholder`, `frontside-on-question-side-unsupported`.

*Cloze*
`cloze-single-front`, `cloze-single-back`, `cloze-with-hint-front`,
`cloze-multi-active-blanked-others-revealed-front` and `-back` — **the section-8 rule's own fixture:
the non-active numbers must appear as plain text on BOTH sides**, `cloze-nested`,
`cloze-unterminated-marker-is-literal`, `cloze-c0-and-non-numeric-ignored`,
`cloze-filter-on-non-cloze-notetype-reveals-all`, `cloze-hint-is-escaped`.

*type:*
`type-question-side-placeholder-only` (asserts the HTML contains no `input` element and no angle
bracket anywhere near the placeholder), `type-expected-strips-html`, `type-duplicate-on-one-side`.

*Sanitisation — one XSS-attempt fixture per checklist bullet, each asserted neutralised*
- `xss-script-tag`, `xss-script-content-skipped` (the text inside must not survive)
- `xss-svg-foreign-content`, `xss-math-foreign-content` — the mXSS pair
- `xss-style-element`, `xss-template-element`, `xss-textarea-element`, `xss-title-element`,
  `xss-noscript-element`, `xss-xmp-element` — the raw-text set
- `xss-mutation-noscript-nested` — the classic `noscript` + `title`-attribute breakout
- `xss-javascript-href`, `xss-javascript-href-obfuscated` (entity-encoded, mixed case, leading
  whitespace/NUL), `xss-data-uri-href`, `xss-data-uri-img-src`, `xss-vbscript-href`,
  `xss-file-scheme-href`
- `xss-onerror-attribute`, `xss-onload-attribute`, `xss-formaction`, `xss-id-attribute-dropped`
- `xss-input-element-dropped` — the answer widget must never arrive as content
- `xss-iframe`, `xss-object-embed`, `xss-base-tag`, `xss-meta-refresh`
- `xss-html-comment-conditional`
- `xss-unbalanced-tags-across-fields` — two fields, one opening and one closing a tag, pinning the
  assembled-then-sanitised order of 4.5
- `sanitise-idempotent` — sanitising `Rendered.HTML` again is a no-op (the `{{FrontSide}}` path
  depends on it)
- `sanitise-style-mixed` — `style="color:red;position:fixed"` becomes `style="color:red"`
- `sanitise-style-url` — a `url(javascript:...)` declaration is dropped while an `rgb(...)` sibling
  survives
- `sanitise-keeps-cloze-span`, `sanitise-keeps-ruby` — the two element sets *we* generate must
  survive the policy

*CSS blob* (`testdata/css/`)
`css-basic-card-rule`, `css-descendant-scoped`, `css-multiple-selectors-partial-drop`,
`css-url-value-dropped`, `css-expression-value-dropped`, `css-image-set-dropped`,
`css-var-function-dropped`, `css-calc-dropped`, `css-colour-functions-allowed` (all four survive),
`css-space-before-paren-dropped`, `css-nested-paren-dropped`, `css-backslash-escape-dropped`,
`css-comment-injection-dropped`, `css-style-close-breakout`, `css-import-at-rule-dropped`,
`css-font-face-dropped`, `css-media-dropped`, `css-keyframes-dropped`, `css-position-fixed-dropped`,
`css-id-selector-dropped`, `css-attribute-selector-dropped`, `css-pseudo-element-dropped`,
`css-star-selector-dropped`, `css-root-html-body-dropped`, `css-important-stripped`,
`css-unparseable-returns-empty`, `css-output-has-no-angle-brackets`.

Non-golden unit tests: `token_test.go` (offsets, filter splitting, comment tags); `cloze_test.go` —
**#54's file, unmodified** — plus a new `cloze_scan_test.go` for nesting and hint splitting;
`typeanswer_test.go` (placeholder replaced exactly once, `Expected` escaped, nil-`Type` passthrough,
the import-boundary assertion).

A table test in `sanitise_test.go` iterates the *entire* forbidden-element list from 4.1 and asserts
each is absent from the output — so adding one of those elements to the allowlist fails the build
rather than passing review.

## 9. Implementation order

1. `go get` the two dependencies; `go build ./...` (confirms module fetch works in this environment).
2. `token.go` + `token_test.go` — pure, fastest feedback, no dependencies.
3. `cloze.go` scanner + `cloze_scan_test.go`; re-point `ClozeOrdinals`; run #54's `cloze_test.go`
   **unedited** as the regression gate.
4. `sanitise.go` + the XSS fixtures. Sanitiser before renderer, so no golden is ever recorded from an
   unsanitised pipeline.
5. `css.go` (value grammar first, since 4.3 depends on `cssValueOK`) + CSS fixtures.
6. `filters.go` (`text`, the furigana family, `hint`, `cloze`) + fixtures.
7. `render.go`: sections, substitution, `{{FrontSide}}`, `RenderCard` + fixtures.
8. `typeanswer.go` + `type:` fixtures + the import-boundary test.
9. `doc.go`, docs, `CHANGELOG.md`.

## 10. Anticipated traps

- **`ClozeOrdinals` behaviour drift.** The refactor in step 3 is the only place this issue can break
  #54's card generation, and a drift there changes which *cards exist* — the `sev: critical`
  "silently corrupts `user_card_state`" bucket, since a vanished ordinal cascades a card away
  (#54 section 0.3). #54's test file is the gate and must not be edited.
- **bluemonday's `style` handling.** `AllowStyles(...)` without `AllowAttrs("style")` (or the
  reverse) silently produces either no styles at all or unfiltered ones. Fixture
  `sanitise-style-mixed` catches both directions; get it passing before writing any other style
  fixture.
- **`SkipElementsContent` versus plain disallow.** A disallowed element's *text* is kept by default.
  For `script` that is inert but ugly; for `style` and `textarea` it is the mXSS vector itself. Do not
  rely on the default.
- **Cloze reveal is HTML, cloze hint is text.** Escaping the reveal breaks formatted cards; not
  escaping the hint is an injection. Two different treatments in adjacent lines of one function.
- **The `{{FrontSide}}` placeholder.** Rendering the question twice — once for `Card.Question`, once
  inside `Card.Answer` — with two *different* nonces would leave a stale placeholder in the answer.
  Render the question exactly once and reuse the string.
- **`crypto/rand` in a hot path.** 16 bytes per side per card, 20 cards per batch, is negligible —
  but generate the nonce only when a `type:` tag is actually present, not on every render.
- **`html.EscapeString` escapes only the five HTML characters.** Correct inside an attribute value
  and inside text; not sufficient inside a script or a URL — and there is neither here. Do not
  generalise it.
- **Field-name matching is case-sensitive and exact.** Trim the tag's surrounding whitespace, never
  the field's stored name.
- **`douceur` version.** v0.2.0 is old but stable and is what bluemonday itself depends on; do not
  pull a fork.
- **Golden files and line endings.** `.gitattributes` already forces LF (CLAUDE.md section 16); write
  fixtures with `\n` and compare exact bytes, or every fixture diff on Windows becomes noise.
- **Do not let a fixture record current-but-wrong behaviour.** `-update` regenerates goldens from the
  code; for the `xss-*` cases the assertion is not "matches the golden" alone but also "contains no
  `<script`, no ` on`-prefixed attribute, and no disallowed scheme" — assert both, so a regression
  cannot be blessed by re-running `-update`.

## 11. Verification

`go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` — `internal/render/` needs no
database, so its whole suite runs without Postgres. Manual verification for the user is deferred to
#56 (no page shows a card yet); the reviewable artefact for this issue is the fixture set, and the PR
description should point at `testdata/render/xss-*` and `testdata/css/` as the security-relevant part
of the diff.

Suggested review pass (CLAUDE.md section 14.3): `/code-review high` — this is `area: security`
(sanitisation) and cross-cutting per CLAUDE.md rule 7.

---

## Open questions

1. **Card-preview route.** routes.md Open question 2 leaves `GET /notes/{id}/preview` (or a note-type
   preview) explicitly undecided — "add only if template authoring needs a live preview". This plan
   adds **no route**, so #55 ships with golden tests as the only exercise of the renderer and nothing
   user-visible until #56. The alternative — a small htmx fragment route on the note-type edit page —
   would make the note-type editor genuinely usable (today an author writes `qfmt` blind) and would
   give `SanitiseCSS`'s `dropped` list somewhere to surface. Decide before implementation; it changes
   the file list (`internal/http/preview.go`, a routes.md row) but nothing in `internal/render/`.
2. **`@media` and `@font-face` in note-type CSS.** The plan drops every at-rule. `@media` appears in
   real Anki note types (night-mode and mobile tweaks) and dropping it is a visible fidelity loss on
   import; supporting it is mechanical (validate the media query text against a small allowlist, then
   recurse the same rule filter over its body). `@font-face` cannot be supported at all without
   `url()`, which section 8's third bullet forbids, so a deck shipping a custom font loses it either
   way. Confirm the blanket drop, or scope `@media` in.
3. **Relative URLs and media, before the blob store exists.** `AllowRelativeURLs(true)` is on so that
   Anki's media convention (`<img src="x.jpg">`, `media_refs (deck_id, filename)` in docs/schema.md)
   survives sanitisation for #60/#34 to rewrite later. Until then those `src`s resolve against the
   application's own origin and 404, and a crafted relative URL (`../../decks/...`) reaches an
   application route as an image load — a weak CSRF-shaped surface, given GET routes are read-only.
   The alternative is `AllowRelativeURLs(false)` now and a flip when the media route lands. Confirm
   which.
4. **`[sound:...]`, LaTeX and `tts:` as visible `[unsupported: ...]` markers.** The plan makes every
   unsupported construct render as inert bracketed text rather than disappearing, on the grounds that
   a silent drop is precisely the failure mode section 8 warns about for `type:`. That does mean an
   imported deck using LaTeX shows `[unsupported: ...]` on every card until #58/#60 land. Confirm
   visible-marker over silent-drop.
5. **Note-type read access under sharing** (routes.md Open question 1, unchanged by this issue): a
   user with only `can_view`/`can_study` on a shared deck has no authorised read path to a note type
   they do not own — and rendering *requires* that note type's fields, templates and CSS.
   `internal/render/` takes its input as plain structs so it is unaffected either way, but #56 cannot
   render a shared deck until this is decided. Flagged here because rendering is the feature that
   makes the gap concrete.

## Resolved decisions

1. **Card-preview route → no route, pure package only.** `internal/render/` ships as a package
   exercised only by golden-file tests; no `internal/http/preview.go`, no routes.md row. Keeps
   #55 scoped to rendering + sanitisation, not handler/route work. A preview route can be filed
   as a small follow-up issue if wanted before #56.
2. **`@media`/`@font-face` in note-type CSS → drop all at-rules, as planned.** No `@media`
   allowlisting is built. Simplest, safest default for Phase 1 (CLAUDE.md rule 2 — no
   speculative complexity); `@media` support can be added later if a real imported note type
   needs it.
3. **Relative URLs before the media blob store exists → `AllowRelativeURLs(true)`, as planned.**
   Anki's `<img src="x.jpg">` convention survives sanitisation now; unresolved `src`s 404
   harmlessly against the application's own origin until #60's media route lands and rewrites
   them. No flip planned for this issue.
4. **Unsupported constructs (`[sound:...]`, LaTeX, `tts:`, `type:cloze:`) → visible
   `[unsupported: ...]` markers, as planned.** Not silently dropped. Makes gaps obvious during
   Phase 1 development and to early importers, consistent with section 8's warning against
   silent drops for `type:`.
5. **Note-type read access under sharing → deferred, unaddressed by #55.** Consistent with #54's
   resolution to hard-code note-type access to single-owner for Phase 1 (docs/plans/
   54-deck-note-type-note-card-crud.md, Resolved decisions #3). `internal/render/`'s plain-struct
   input means this package needs no change when the sharing question is eventually settled;
   only #56 (the reviewer) is blocked by it, and only in Phase 2.
