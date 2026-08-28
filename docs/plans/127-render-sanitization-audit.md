# Plan — #127 Audit: Render & sanitization

Zone: `internal/render/`. Constraint: **simplification and standard best practices, no behaviour
change**. This plan is written against the package as it stands on `main` at 8422f47 — i.e.
*after* 3871a9f (SVG allowlist), 58116be (image-only truthiness) and 29e3ad1 (`RewriteMediaSrcs`),
all three of which the issue body predates.

Baseline before touching anything: `go test ./internal/render/` is green.

Actual shape on disk (the issue's "18 files / ~2064 lines" counts source **and** tests together):

| source | LOC | | test | LOC |
|---|---|---|---|---|
| `css.go` | 384 | | `render_test.go` | 427 |
| `render.go` | 243 | | `sanitise_test.go` | 176 |
| `cloze.go` | 154 | | `token_test.go` | 86 |
| `filters.go` | 148 | | `typeanswer_test.go` | 82 |
| `sanitise.go` | 143 | | `css_test.go` | 59 |
| `token.go` | 81 | | `media_test.go` | 56 |
| `media.go` | 57 | | `cloze_scan_test.go` | 50 |
| `typeanswer.go` | 32 | | `cloze_test.go` | 36 |
| `doc.go` | 19 | | | |
| `errors.go` | 12 | | | |

9 source files, 1273 LOC → ~141 lines/source-file. The ratio the issue flagged is an artefact of
counting test files in the denominator.

---

## Open questions

Do not pick one of these while implementing. Ask, or leave the code untouched.

### OQ1 — Do the three SVG keyword-enum regexes collapse into `keywordEnumProperties`?

`sanitise.go` declares three regexes whose value sets are also spelled out, character for
character, in `css.go`'s `keywordEnumProperties`:

| `sanitise.go` | `css.go` `keywordEnumProperties[...]` | values |
|---|---|---|
| `svgLinecapRe` | `"stroke-linecap"` | `butt` `round` `square` |
| `svgLinejoinRe` | `"stroke-linejoin"` | `miter` `round` `bevel` |
| `svgFillRuleRe` | `"fill-rule"` | `nonzero` `evenodd` |

Two mechanisms need them in two shapes: bluemonday's attribute path takes only
`Matching(*regexp.Regexp)` (verified — `attrPolicyBuilder` in bluemonday v1.0.27 has
`Matching`/`OnElements`/`Globally`/`AllowNoAttrs` and **no** `MatchingHandler`; only
`stylePolicyBuilder` has that), while `propertyShapeOK` needs a map lookup.

Options:

- **A. Leave as-is (no code change).** Three tiny, frozen value sets; drift between them would
  produce at worst a cosmetic inconsistency (`<path stroke-linecap="x">` stripped while
  `style="stroke-linecap: x"` passes), never a security gap.
- **B. Generate the three regexes from the map.** Add to `css.go` a helper
  `func enumRe(prop string) *regexp.Regexp` that reads `keywordEnumProperties[prop]`, sorts the
  keys (for a deterministic pattern string; matching is order-independent either way) and returns
  `regexp.MustCompile("^(" + strings.Join(keys, "|") + ")$")`. Replace the three
  `regexp.MustCompile` literals in `sanitise.go` with `enumRe("stroke-linecap")` etc., each
  wrapped in `sync.OnceValue` the way `svgPaintRe` already is (the map must be initialised
  first). Behaviour-identical: all three sets are distinct case-sensitive literals with no
  prefix overlap. Cost: ~10 lines of helper to remove 3 lines of literal, and three regexes that
  are no longer readable at their declaration site.
- **C. (rejected — do not choose)** Make `keywordEnumProperties` drop those three entries and
  have `propertyShapeOK` consult the regexes instead. This *is* a behaviour change:
  `propertyShapeOK` lowercases before the map lookup, so `style="stroke-linecap: ROUND"` is
  accepted today and a case-sensitive regex would reject it.

**Resolved decision: Option A — leave as-is.** No code change for OQ1. The three regexes in
`sanitise.go` stay exactly as they are; do not add an `enumRe` helper to `css.go`.

---

## Changes

Two changes, both in the SVG-allowlist area the issue comment asked about, both provably
behaviour-identical. Nothing else in the package changes.

### C1 — Collapse the four duplicated `sanitisableElements` + `svgShapeElements` registrations

3871a9f introduced a second element list and then repeated every "applies to both lists" call
site. Four places now have to be edited in lockstep whenever either list gains a member. Fold the
concatenation into one package var; leave the two source lists themselves separate (their
attribute grammars genuinely have nothing in common, which is what `svgShapeElements`' own
comment says).

**File: `internal/render/sanitise.go`**

1. Immediately after the `svgShapeElements` declaration (currently line 38), add:

   ```go
   // allAllowedElements is sanitisableElements + svgShapeElements: everywhere the two lists get
   // identical treatment -- bluemonday element registration, the no-attribute default, the
   // style="" attribute, and css.go's elementAllowed selector check. The two lists stay separate
   // above because their attribute grammars have nothing in common; this is the one place that
   // has to see both.
   var allAllowedElements = append(append([]string{}, sanitisableElements...), svgShapeElements...)
   ```

2. In `cardPolicy`, replace these four lines (currently 74–77):

   ```go
   p.AllowElements(sanitisableElements...)
   p.AllowNoAttrs().OnElements(sanitisableElements...)
   p.AllowElements(svgShapeElements...)
   p.AllowNoAttrs().OnElements(svgShapeElements...)
   ```

   with:

   ```go
   p.AllowElements(allAllowedElements...)
   p.AllowNoAttrs().OnElements(allAllowedElements...)
   ```

3. Replace these two lines (currently 125–126):

   ```go
   p.AllowAttrs("style").OnElements(sanitisableElements...)
   p.AllowAttrs("style").OnElements(svgShapeElements...)
   ```

   with:

   ```go
   p.AllowAttrs("style").OnElements(allAllowedElements...)
   ```

4. In the `sanitisableElements` doc comment, change "also (together with svgShapeElements below)
   the selector allowlist in css.go's elementAllowed" to point at `allAllowedElements` as the
   thing `elementAllowed` reads, so the comment still names the real path.

5. **Do not touch lines 104–117.** Every SVG geometry/paint attribute rule
   (`viewBox`, `d`, `points`, `cx`/`cy`/`r`, `fill`/`stroke`, `stroke-width`, `stroke-linecap`,
   `stroke-linejoin`, `fill-rule`, `stroke-dasharray`, and the `width`/`height`/`x`/`y`/`rx`/`ry`
   rules) must keep its existing narrower `OnElements(svgShapeElements...)` or explicit
   element-name scoping. Widening any of them to `allAllowedElements` is a behaviour change and
   is out of scope.

6. **Do not touch line 98** (`p.AllowAttrs("src", "width", "height").OnElements("img")`) — its
   `width`/`height` are the unvalidated HTML ones, distinct from the `lengthTokenRe`-matched SVG
   ones on line 105.

**File: `internal/render/css.go`**

7. Replace `elementAllowed` (currently lines 372–384) with:

   ```go
   func elementAllowed(name string) bool {
   	for _, e := range allAllowedElements {
   		if e == name {
   			return true
   		}
   	}
   	return false
   }
   ```

   Same accepted set, one loop instead of two.

Why this is behaviour-identical: bluemonday's `AllowElements`, `AllowNoAttrs().OnElements` and
`AllowAttrs(...).OnElements` all accumulate into per-element maps, so registering `a` then `b` is
indistinguishable from registering `a+b` in one call; `elementAllowed`'s two sequential linear
scans and one scan over the concatenation accept exactly the same names. Package-var init order
is resolved by dependency, so `allAllowedElements` is built before `cardPolicy`'s `sync.OnceValue`
body ever runs.

### C2 — One definition of the hex-colour grammar

`css.go`'s `hexColourRe` and `sanitise.go`'s `svgPaintRe` each spell out the `#RGB` / `#RRGGBB` /
`#RRGGBBAA` grammar, in two different notations, for the same accepted set. `svgPaintRe` already
reuses `namedColours` from `css.go` for the colour-name half — this finishes that job for the hex
half.

**File: `internal/render/css.go`**

1. Immediately before the `var (...)` block that currently starts at line 55, add:

   ```go
   // hexColourPattern is the #RGB / #RRGGBB / #RRGGBBAA grammar, unanchored so sanitise.go's
   // svgPaintRe can embed it in its alternation -- one definition of what a hex colour is.
   const hexColourPattern = `#[0-9A-Fa-f]{3}([0-9A-Fa-f]{3}([0-9A-Fa-f]{2})?)?`
   ```

2. Inside that block, change

   ```go
   hexColourRe = regexp.MustCompile(`^#[0-9A-Fa-f]{3}([0-9A-Fa-f]{3}([0-9A-Fa-f]{2})?)?$`)
   ```

   to

   ```go
   hexColourRe = regexp.MustCompile(`^` + hexColourPattern + `$`)
   ```

**File: `internal/render/sanitise.go`**

3. In the `svgPaintRe` builder (currently line 69), change the returned expression from

   ```go
   return regexp.MustCompile(`(?i)^(none|` + strings.Join(names, "|") + `|#[0-9A-Fa-f]{3}|#[0-9A-Fa-f]{6}|#[0-9A-Fa-f]{8})$`)
   ```

   to

   ```go
   return regexp.MustCompile(`(?i)^(none|` + strings.Join(names, "|") + `|` + hexColourPattern + `)$`)
   ```

4. Extend `svgPaintRe`'s existing comment by one clause noting that the hex half now comes from
   `hexColourPattern` for the same reason the name half comes from `namedColours`.

Why this is behaviour-identical: the old alternation accepts `#` followed by exactly 3, 6 or 8 hex
digits; `hexColourPattern` accepts `#` + 3 hex, optionally + 3 more, optionally + 2 more — the
same {3, 6, 8} set. The `(?i)` flag is a no-op over `[0-9A-Fa-f]`, which already covers both cases.
`TestSanitiseCardHTML_SVGShapes` pins the `stroke="currentColor"` (named-colour) path and
`sanitise_test.go`'s fixtures pin the `url(...)`/`expression(...)` rejections; add nothing.

---

## Verification

Run in order; every one must pass before the diff is presented for review.

1. `go build ./...`
2. `go vet ./...`
3. `golangci-lint run`
4. `go test ./internal/render/ -count=1`
5. `go test ./...` — `internal/review` and `internal/apkg` both exercise the render path
   (`internal/apkg/media_render_test.go`, `internal/review/batch.go`).

Success criterion for both changes: the full suite is green with **no test file edited**. If any
test needs changing, the change was not behaviour-neutral — stop and report rather than adjusting
the test.

---

## No change needed — reviewed and confirmed clean

### File-count / LOC consolidation (issue review-focus bullet 1) — no merges recommended

Every source file under 60 lines is small for a structural reason, and merging any of them would
cost more than it saves:

- **`typeanswer.go` (32 lines) — must not be merged.** Its file boundary is a *checked*
  invariant: `typeanswer_test.go`'s `TestTypeAnswerGo_DoesNotImportSanitiser` parses the literal
  path `"typeanswer.go"` with `go/parser` and fails if that file's import set ever mentions
  bluemonday. Folding it into `render.go` or `filters.go` deletes that guard outright and pulls
  the widget builder into a file that does import the sanitiser path.
- **`doc.go` (19 lines)** — the package doc comment. Standard Go layout; nothing to merge.
- **`errors.go` (12 lines)** — three sentinel errors consumed from *two* different files
  (`token.go` uses `ErrUnterminatedTag`; `render.go` uses `ErrUnclosedSection` and
  `ErrSectionMismatch`). A shared dependency of two files does not belong inside either one, and
  a dedicated `errors.go` is the idiomatic Go home for package sentinels.
- **`media.go` (57 lines)** — a self-contained exported API with its own dependency-injection
  seam and its own test file. Distinct concern from rendering and from sanitisation.
- **`token.go` (81 lines)** — the lexer; `render.go` is the evaluator. Two phases, two files, and
  `token_test.go` tests the lexer independently.

The three files that carry real bulk (`css.go` 384, `render.go` 243, `cloze.go`/`filters.go`
~150) are each one coherent concern. No split is warranted either.

### Type-answer splice order (issue review-focus bullet 2) — confirmed correct

Traced end to end; the widget is spliced strictly **after** sanitisation and never routed through
it.

- `RenderCard` (`render.go:74`) does, per side, in this order: `tokenise` → `evaluate` →
  `sanitiseCardHTML` → `finaliseRendered`. The answer side receives the question side's
  *already-sanitised* HTML as `frontSide`, so `{{FrontSide}}` cannot reintroduce unsanitised
  markup.
- `typeFilter` (`filters.go:136`) emits **no HTML at all** — only an
  `ENSHUTYPEANSWER<32 hex>ENSHUEND` token from `crypto/rand`, with a `rand.Read` failure escalated
  to a hard error rather than a guessable fallback.
- `finaliseRendered` (`render.go:120`) runs *after* `sanitiseCardHTML` and errors unless the
  placeholder appears exactly once. This is fail-closed in the right direction: if the placeholder
  were ever dropped by the sanitiser (e.g. a `{{type:X}}` landing inside a `SkipElementsContent`
  element) the render fails loudly instead of silently emitting a widget-less card.
- `TypeAnswerInput` / `TypeAnswerExpected` (`typeanswer.go`) do a single
  `strings.Replace(..., 1)` on the finished `Rendered.HTML` and escape `Expected` with
  `html.EscapeString` only. `typeanswer.go` imports `html`, `html/template`, `strings` — nothing
  from the sanitiser, asserted by `TestTypeAnswerGo_DoesNotImportSanitiser`.
- The one real call site, `internal/review/batch.go:396–425`, preserves the order:
  `render.RenderCard` (sanitises) → `render.RewriteMediaSrcs` → `render.TypeAnswerInput` /
  `render.TypeAnswerExpected`. The widget is not passed through the media rewriter's HTML
  tokenizer either.
- `sanitiseCardHTML` is unexported, so no caller outside the package can route the widget through
  it even by mistake.

No ordering bug or ordering risk found. Nothing to fix, nothing to flag.

### Image-only-field truthiness (issue review-focus bullet 4) — already a single shared helper

58116be's rule is expressed **once**, in `fieldHasContent` (`filters.go:30`), and all three call
sites reach it:

- `{{#Field}}` and `{{^Field}}` — both go through `truthy` (`render.go:233`), which calls
  `fieldHasContent`. `evaluate` handles `kindOpen` and `kindOpenNeg` in one `case` and negates the
  single `truthy` result, so the negative form cannot drift from the positive one.
- `{{hint:Field}}` — `hintFilter` (`filters.go:124`) calls the same `fieldHasContent`.

`imgSrcRe` and `stripHTMLTags` each have exactly one definition and are used only from
`fieldHasContent` (and `stripHTMLTags` additionally from `textFilter`/`typeFilter`, where the
image special-case correctly does *not* apply). There is no duplicated `<img src>` check to
extract. `TestRenderCard_SectionImageOnlyFieldIsTruthy` and
`TestRenderCard_FilterHintImageOnlyField` cover both branches. No change.

### `RewriteMediaSrcs` / `MediaResolver` purity (issue review-focus bullet 5) — confirmed clean

`go list` over the package's full non-test import set returns:

```
crypto/rand encoding/hex errors fmt github.com/aymerick/douceur/css
github.com/aymerick/douceur/parser github.com/microcosm-cc/bluemonday golang.org/x/net/html
golang.org/x/text/unicode/norm html html/template math net/url regexp sort strconv strings sync
```

No `internal/db`, no `net/http`, no `database/sql`, no I/O. `media.go` itself imports only
`net/url`, `strings`, `golang.org/x/net/html` and `golang.org/x/text/unicode/norm`. The
`MediaResolver` seam is intact: `RewriteMediaSrcs` takes the resolver as a parameter and the only
production caller (`internal/review/batch.go:400–401`) supplies a closure. Nothing reaches into
the database directly. No change.

### SVG allowlist scope

Not touched beyond C1/C2. No element or attribute is added to or removed from
`svgShapeElements` or its attribute rules — broader SVG support is #121's job, and any widening
would be a behaviour change this audit is not allowed to make.
