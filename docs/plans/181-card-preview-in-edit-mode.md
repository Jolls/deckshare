# Plan: Card preview in Card Edit mode (#181)

## Summary

Add a "Preview" action to both note-editing forms (`GET /notes/{id}/edit` and
`GET /decks/{deckId}/notes/new`) that renders the note's card(s) — question and answer sides,
CSS, media — from the **currently-typed, unsaved** form values, without persisting anything.
Two new POST routes render an HTML fragment through `htmx`, swapped into a panel on the same
page. No schema change. No new SQL queries beyond ones already used elsewhere in this codebase.

This is a read-only, non-scheduling operation: it never touches `cards`, `user_card_state`, or
`review_log`, and never calls into `internal/fsrs` or `internal/review`.

---

## 1. New routes

| Method | Path | Auth | Mirrors |
|---|---|---|---|
| POST | `/notes/{id}/preview` | `can_edit_content` on the note's deck (via `GetNoteForContentEdit`) | `POST /notes/{id}/edit` |
| POST | `/decks/{deckId}/notes/preview` | `can_edit_content` on the deck (via `GetDeckForContentEdit`) | `POST /decks/{deckId}/notes` |

Both:
- Request: `application/x-www-form-urlencoded`, same field shape as their sibling save route —
  `note_type_id`, repeated `field[]`, `tags`. Parsed with `parseForm(w, r)` (existing helper,
  `internal/http/respond.go:43`).
- Response: `200 text/html`, a standalone fragment (no `layout`), rendered via `renderFragment`
  (`internal/http/templates.go:75`) — same mechanism `GET /api/reviews/next` already uses for
  `review_cards`.
- Every failure mode that means "can't render this yet" (see step 6 below) is *also* a `200`
  with an inline notice in the fragment, not an HTTP error — htmx swaps whatever comes back, and
  a live-typing preview hitting "cloze note, no `{{c1::...}}` yet" is a normal, expected state,
  not a client error.
- The one genuine error case is a **field-count mismatch** between `field[]` and the note type's
  field count — that cannot happen from the real form (see step 5) and only happens from a
  malformed/direct POST, so it gets `badRequest(w)` (400), matching how every other POST handler
  in `notes.go` treats a field-count mismatch.

Add a new file `internal/http/note_preview.go` for `registerNotePreviewRoutes` and both handlers
— don't add to `notes.go` (working rule 3: surgical, and this is a distinct read-only concern).

### `internal/http/http.go`

Add one line in `NewHandler`, after `registerNoteRoutes(mux, pool, pages)`:
```go
registerNotePreviewRoutes(mux, pool, fragments)
```
`fragments` is already built earlier in `NewHandler` (`parseFragments()`); pass it through the
same way `registerReviewRoutes` already does.

### `internal/http/templates.go`

In `parseFragments`, add `"note_preview"` to the fragment name list (currently just
`"review_cards"`, line 53):
```go
for _, name := range []string{"review_cards", "note_preview"} {
```

---

## 2. Handler step-by-step

Both handlers share a helper. Put it in `note_preview.go`:

```go
func buildNotePreview(ctx context.Context, q *db.Queries, ownerID pgtype.UUID,
    noteTypeID pgtype.UUID, fieldValues []string, tagsRaw string,
    deckID pgtype.UUID, deckName string) (notePreviewView, error)
```

Steps, identical for both routes from here on:

1. `nt, err := q.GetNoteTypeForOwner(ctx, db.GetNoteTypeForOwnerParams{ID: noteTypeID, OwnerID: ownerID})`
   — `handleQueryErr` (404 if not owned). This is the same owner-scoped lookup
   `POST /notes/{id}/edit` already uses for a note-type switch (`notes.go:252`) — note types are
   owner-scoped, not deck-scoped (invariant §2.2), so this does not need a `deck_access` join.
2. `fields, err := q.ListFieldsForNoteType(ctx, noteTypeID)` — 500 on error.
3. `templates, err := q.ListTemplatesForNoteType(ctx, noteTypeID)` — 500 on error.
4. **Field count check**: `if len(fieldValues) != len(fields) { return badRequest }`. This is the
   one hard-error case (see §1). Do **not** additionally require the first field to be non-empty
   (that is `validateNoteFields`'s save-time rule, `notes.go:454` — a save needs a non-empty sort
   field, but a render doesn't; a preview with a still-blank first field should render the blank
   field, not fail) — so this handler does not call `validateNoteFields`.
5. `desired, err := desiredCards(nt, templates, fieldValues)` — the exact function
   `notes.go:424` already uses to decide what cards a save would create (one per template, or one
   per distinct cloze ordinal). Reused unmodified. If it returns `errNoClozeMarkers` (the only
   realistic error once step 4 has passed — the "no template" case can't occur for an owned,
   already-created note type), render the fragment with a single notice paragraph — e.g. "Add a
   `{{c1::...}}` marker to preview this note's cards." — and return 200. Do not treat this as an
   error.
6. Build `render.Note`:
   - `Fields`: `[]render.Field{{Name: fields[i].Name, Value: fieldValues[i]}, ...}` for `i` in
     `range fields` (same positional convention `notes.go` and `internal/review/batch.go` both
     use — fields query is ordinal-ordered, `field[]` is submitted in that same order by the
     form).
   - `Tags`: `parseTags(tagsRaw)` (existing helper, `notes.go:474`) — sourced from the posted
     `tags` form value, not from the saved note, so an edited-but-unsaved tag list is reflected
     (open question 1 below covers unsaved-vs-saved generally; tags follow the same choice).
   - `NoteType`: `nt.Name`.
   - `Deck` / `Subdeck`: `deckName` / a `lastSubdeck(deckName)` equivalent. `lastSubdeck` itself
     is unexported in `internal/review` (`internal/review/batch.go:124`), so add a private
     one-line copy of it in `note_preview.go` rather than exporting the original — `internal/http`
     gains no new dependency on `internal/review` for a one-line "last `::` component" helper.
7. `templatesByID := map[pgtype.UUID]db.Template` built from `templates`.
8. For each `d := range desired`: look up `tmpl := templatesByID[d.TemplateID]`, call
   `render.RenderCard(tmpl, note, d.Ordinal, nt.IsCloze)`. **If any card errors** (malformed
   template — unclosed section, mismatched section), stop and render the fragment with a single
   top-level notice — "This note type's template has an error and can't be previewed: `<err>`."
   — instead of partial output. (A template syntax error is a note-type authoring problem, not a
   per-card one; showing some cards and not others would be confusing.)
9. Media: `mediaRefs, err := q.ListMediaRefsForDeck(ctx, deckID)` (existing query,
   `internal/db/media_refs.sql.go:55` — already used by `internal/review/batch.go:398` for
   exactly this purpose). Build the same `resolveMedia func(filename string) (string, bool)`
   closure `batch.go:406` builds, and call
   `render.RewriteMediaSrcs(string(rendered.Question.HTML), resolveMedia)` /
   `...Answer.HTML...` on each card, same as `batch.go:439-440`. See §5 below for what this does
   and doesn't resolve.
   - **`internal/db/media_refs.sql.go`'s doc comment update**: `ListMediaRefsForDeck`'s comment
     currently says "No deck_access join... the only caller is internal/review's
     renderQueueRows, for a deck the review handler has already authorised." This is no longer
     true once this handler calls it too — update the comment to say the caller list now also
     includes the note-preview handlers, each of which has already authorised the same deck via
     `GetNoteForContentEdit` / `GetDeckForContentEdit` before calling this. **No SQL or query
     signature changes** — comment only. Since this file is `sqlc`-generated
     (`internal/db/media_refs.sql.go`), the comment actually lives in the source
     `internal/db/queries/media_refs.sql:9-11` and reaches the generated file via `go generate`
     — edit the `.sql` file, then run `go generate` and commit the regenerated `.go` file (never
     hand-edit `media_refs.sql.go` directly, per CLAUDE.md §16).
10. Type-answer widgets: `render.TypeAnswerInput(card.Question)` for the front,
    `render.TypeAnswerExpected(card.Answer)` for the back — the exact pair
    `internal/review/batch.go:463-464` uses. See open question 4 below; this is the recommended
    default, listed there for confirmation rather than silently decided, since the issue's own
    "fake study session" framing suggests the author may want typing to actually work.
11. CSS: `sanitisedCSS, _ := render.SanitiseCSS(nt.Css)` (`internal/render/css.go:230`) — sanitise
    once per response, not per card, same as `noteTypeCSS` does in `review.go:270`.
12. Assemble the view model (below) and `renderFragment(w, fragments["note_preview"], 200,
    "note_preview", view)`.

### View model (add to `note_preview.go`)

```go
type previewCardView struct {
    Question template.HTML
    Answer   template.HTML
}

type notePreviewView struct {
    CSS      template.CSS
    Cards    []previewCardView
    Notice   string // non-empty means: show this message instead of Cards
}
```

### `POST /notes/{id}/preview` handler

1. `auth.RequireUser`; get `user`.
2. `noteID, ok := pathUUID(r, "id")` — `notFound` if bad.
3. `parseForm(w, r)`.
4. `note, err := q.GetNoteForContentEdit(ctx, db.GetNoteForContentEditParams{UserID: user.ID, NoteID: noteID})` — `handleQueryErr`. This authorises `can_view + can_edit_content` on the note's deck (`internal/db/queries/notes.sql:34-39`), same as the real edit handler.
5. `deck, err := q.GetDeckForContentEdit(ctx, db.GetDeckForContentEditParams{UserID: user.ID, DeckID: note.DeckID})` — `handleQueryErr`. Needed for `deck.Name` (render `{{Deck}}`/`{{Subdeck}}`) and to pass `note.DeckID` into `buildNotePreview` for media resolution. (This re-checks access already implied by step 4; that's an accepted small redundancy — no new query, reuses an existing one, and keeps the "always fetch what I need through an authorising query" pattern uniform across this file.)
6. `noteTypeIDStr := r.PostForm.Get("note_type_id")`; scan to `pgtype.UUID` — `badRequest` on failure. (The edit form can offer a note-type switch — `NoteTypeOptions`, `notes.go:182-188` — so preview must honor whatever `note_type_id` is currently selected in the form, not `note.NoteTypeID`.)
7. `fieldValues := r.PostForm["field[]"]`.
8. `view, err := buildNotePreview(ctx, q, user.ID, noteTypeID, fieldValues, r.PostForm.Get("tags"), note.DeckID, deck.Name)`. On the one hard-error case (field count mismatch), `buildNotePreview` returns a sentinel error the handler maps to `badRequest`; everything else comes back as a `notePreviewView` with `Notice` set.
9. `renderFragment(w, fragments["note_preview"], http.StatusOK, "note_preview", view)`.

### `POST /decks/{deckId}/notes/preview` handler

1. `auth.RequireUser`; get `user`.
2. `deckID, ok := pathUUID(r, "deckId")` — `notFound` if bad.
3. `parseForm(w, r)`.
4. `deck, err := q.GetDeckForContentEdit(ctx, db.GetDeckForContentEditParams{UserID: user.ID, DeckID: deckID})` — `handleQueryErr`.
5. `noteTypeIDStr := r.PostForm.Get("note_type_id")`; scan — `badRequest` on failure.
6. `fieldValues := r.PostForm["field[]"]`.
7. `view, err := buildNotePreview(ctx, q, user.ID, noteTypeID, fieldValues, r.PostForm.Get("tags"), deckID, deck.Name)`.
8. `renderFragment(...)` as above.

---

## 3. Card ordinals (including cloze)

Delegated entirely to `desiredCards(nt, templates, fieldValues)` (`internal/http/notes.go:424`,
unchanged, not exported further — it's already package-visible within `internal/http`). This is
the single existing place that decides card shape from a note type + field values:

- Non-cloze: one `db.DesiredCard{Ordinal: t.Ordinal, TemplateID: t.ID}` per template, in
  template order.
- Cloze: `noterender.ClozeOrdinals(fieldValues)` (`internal/render/cloze.go:97`) scans every
  field for `{{cN::...}}` markers (including nested ones) and returns the distinct `N` values,
  ascending; one `db.DesiredCard{Ordinal: n-1, TemplateID: templates[0].ID}` per distinct `N`.

Because this is the exact function the real save path calls, the preview's card count and
ordinals are always what a save would actually produce — no separate "preview ordinal" logic to
drift from it.

---

## 4. Field values and question/answer sides

- Field values come from the POST body's `field[]`, not from the database — see open question 1
  for the saved-vs-unsaved framing; this plan's answer is "unsaved," which is why the field
  values are read from `r.PostForm` in both handlers rather than from `note.Fields`.
- `render.RenderCard(tmpl, note, ordinal, isCloze)` (`internal/render/render.go:74`) renders both
  sides in one call and returns a `render.Card{Question, Answer render.Rendered}` — already
  sanitised (`sanitiseCardHTML`, called internally). Both sides are shown together in the preview
  panel (no reveal-answer toggle, no grading buttons) — this is an editing aid, not a quiz.

---

## 5. CSS and media

- **CSS**: `render.SanitiseCSS(nt.Css)` once per response (step 11 above), emitted as one
  `<style>{{.CSS}}</style>` at the top of the fragment. Each card's rendered HTML is wrapped in
  `<div class="{{render.ScopeClass}}">` (`"enshu-card"`, `internal/render/css.go:15`) in the
  template — same scoping contract the reviewer already relies on, so sanitised note-type CSS
  cannot escape the card and restyle the preview panel or the rest of the edit page.
- **Media**: `render.RewriteMediaSrcs` against `ListMediaRefsForDeck(deckID)`, exactly as the
  reviewer does (§2 step 9). This resolves any filename that already has a `media_refs` row for
  this deck — i.e., any media previously imported or already referenced by an existing note in
  this deck. A filename typed into a field that has **never** been associated with this deck
  (e.g., an author hand-typing a brand-new `<img src="new.jpg">` with nothing behind it yet) has
  no `media_refs` row, so `RewriteMediaSrcs` leaves that `<img src>` unresolved and it 404s in
  the browser — the same "unresolved filename left alone" behavior the reviewer already has
  (`internal/render/media.go:21`, `#92`'s resolved behavior). No new code needed for this case;
  it falls out of reusing the existing resolver as-is. See open question 5.

---

## 6. New template: `web/templates/note_preview.html`

Fragment, parsed standalone (no `layout`), following `review_cards.html`'s shape:

```html
{{define "note_preview"}}
{{if .Notice}}
<p class="preview-notice">{{.Notice}}</p>
{{else}}
<style>{{.CSS}}</style>
{{range $i, $c := .Cards}}
<article class="enshu-card">
  {{if gt (len $.Cards) 1}}<p><small>Card {{inc $i}} of {{len $.Cards}}</small></p>{{end}}
  <div class="card-question">{{$c.Question}}</div>
  <div class="card-answer">{{$c.Answer}}</div>
</article>
{{end}}
{{end}}
{{end}}
```

(`inc` — 1-based card numbering off a 0-based `range` index — needs a template func or a
precomputed `Number int` field on `previewCardView` instead; **prefer adding `Number int` to
`previewCardView`** set to `i+1` in Go, rather than adding a new template function, matching
"minimum code" — working rule 2.)

---

## 7. `web/templates/note_form.html` changes

Add a Preview button + result container to both the new-note form (`.IsNew` branch) and the
edit form (the final `else` branch) — **not** to `.PickNoteType` or the two confirmation
branches (`.ConfirmNoteTypeChange`, and notetype's own `.ConfirmStructuralChange`, which lives in
`notetype_form.html`, not this file), since none of those have field inputs to preview.

New-note form (inside the existing `<form method="post" action="/decks/{{.Deck.ID}}/notes">`,
after the fields, before the submit button):
```html
<button type="button" hx-post="/decks/{{.Deck.ID}}/notes/preview"
        hx-include="closest form" hx-target="#note-preview" hx-swap="innerHTML">Preview</button>
```
Edit form (inside `<form method="post" action="/notes/{{.Note.ID}}/edit">`, same placement):
```html
<button type="button" hx-post="/notes/{{.Note.ID}}/preview"
        hx-include="closest form" hx-target="#note-preview" hx-swap="innerHTML">Preview</button>
```
And, once per page (outside both forms, after them), a shared container:
```html
<div id="note-preview"></div>
```
`hx-include="closest form"` sends every field in the enclosing `<form>` — `note_type_id`,
every `field[]`, and (edit form only) `tags` — matching each route's expected POST shape exactly.
`htmx.min.js` is already loaded globally in `layout.html` (deferred, every page) — no new script
tag needed.

---

## 8. `docs/routes.md` update

In the Notes table (line 95-102), add two rows:
```
| POST | `/notes/{id}/preview` | `can_edit_content` | Render the note's card(s) from the posted (possibly unsaved) field values; writes nothing |
| POST | `/decks/{deckId}/notes/preview` | `can_edit_content` | Same, for the new-note form before the note exists |
```
Remove open question 2 (line 219-220: "Card preview route — not included...") — it's resolved by
this plan. Renumber the remaining "Explicitly not routed" note-type-read-access item if the doc's
convention numbers these sequentially (check current numbering at edit time).

---

## 9. Tests

New file `internal/http/note_preview_test.go`, following `notes_test.go`'s conventions
(`beginTx`, `newTestHandler`, `loginCookie`, `doRequest`, `countRows`).

1. **Non-cloze preview renders one card per template.** Create a deck + 2-template note type
   (`newReversedNoteTypeBody`-style fixture, already in `notes_test.go`). POST
   `/decks/{deckId}/notes/preview` with field values, no prior note creation. Assert `200`, body
   contains both templates' rendered question/answer content (e.g. field text appears twice, once
   per card), and — the load-bearing assertion — `countRows` on `notes`, `cards`,
   `user_card_state`, `review_log` are unchanged (all zero) before and after the call.
2. **Cloze preview renders one card per distinct cloze ordinal.** Cloze note type, field value
   with `{{c1::a}}...{{c2::b}}`. Assert 2 cards in the response, and again assert zero rows in
   `notes`/`cards`/`user_card_state`/`review_log`.
3. **Cloze preview with no cloze marker yet returns 200 with a notice, not 400.** Field value with
   no `{{cN::...}}`. Assert `200` and the notice text, not an error status.
4. **Edit-form preview uses unsaved field values, not the saved note.** Create and save a note
   with field `"A"`. POST `/notes/{id}/preview` with `field[]=B` (different from saved). Assert
   the response contains `"B"` and does not contain `"A"`, and that `SELECT fields FROM notes
   WHERE id = $1` in the DB is still `["A"]` (unchanged).
5. **Field-count mismatch is 400.** POST with wrong number of `field[]` values. Assert 400, and
   (again) zero rows in the scheduling/content tables — nothing was written or partially applied.
6. **Auth: a user without `can_edit_content` on the deck gets 404** on both routes — same
   collapse-to-404 pattern `notes_test.go`'s existing access tests use for the sibling save
   routes. Add one row each to whatever table-driven access test already covers
   `/notes/{id}/edit` and `/decks/{deckId}/notes` (CLAUDE.md §10.5: "Add a row on every new
   endpoint").
7. **Media**: a field referencing a filename with an existing `media_refs` row for the deck
   resolves to `/media/{sha256}` in the preview output; a field referencing an unknown filename is
   left as-is (matches `internal/render/media_test.go`'s existing coverage of
   `RewriteMediaSrcs` — this test is about the *handler* wiring the resolver correctly, not about
   `RewriteMediaSrcs` itself, which stays untested here beyond that one wiring check).
8. **CSS is sanitised and scoped**: note type CSS containing a disallowed property (e.g.
   `position: fixed`) is stripped from the preview's `<style>` block — reuses the existing
   `SanitiseCSS` behavior; this test is again about the handler calling it, not re-testing
   `SanitiseCSS` itself (already covered in `internal/render`).

No new tests needed in `internal/render` — nothing in that package changes (see §17: it stays
pure, no DB/HTTP/I/O added, and this plan adds none).

---

## 10. Success criteria

1. `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` all pass.
2. From the edit form (`/notes/{id}/edit`) and the new-note form
   (`/decks/{deckId}/notes/new?note_type_id=...`), clicking "Preview" renders the note's card(s)
   — question and answer sides, both visible — without a page navigation.
3. A cloze note with markers for `{{c1}}` and `{{c2}}` previews as 2 separate cards; a non-cloze
   note type with 2 templates previews as 2 separate cards.
4. Editing a field and clicking Preview *before* saving shows the new value; the database's
   stored `notes.fields` for that note is provably unchanged (test #4 above).
5. **No scheduling state is written by either preview route**: `notes`, `cards`,
   `user_card_state`, and `review_log` row counts are identical before and after any preview
   request, across every test scenario above, including the error-path ones (field-count
   mismatch, no-cloze-marker). Verified by `countRows` assertions in tests #1, #2, #5.
6. A user without `can_edit_content` on the relevant deck gets 404 from both preview routes
   (test #6).
7. Card content in the preview is sanitised the same way the reviewer's is — no raw
   author-supplied HTML reaches the browser outside `internal/render`'s sanitiser (test #8, and
   by construction: the handler never emits `fieldValues` or `nt.Css` directly, only
   `render.RenderCard`'s and `render.SanitiseCSS`'s outputs).
8. `internal/render` has zero changes — still pure, still no DB/HTTP/I/O.
9. `docs/routes.md`'s open question 2 is resolved (the two new rows are documented, the open
   question entry is removed).

---

## Open questions

### 1. Preview the note as currently saved, vs. the unsaved values in the form

- **A. Unsaved form values (this plan's assumption).** POST endpoint takes `field[]`/`tags`/
  `note_type_id` from the form exactly as typed, renders those. Matches "preview what I'm about
  to save" — the more useful reading of the issue for an edit-in-progress. Cannot rely on a
  newly-referenced (never-before-uploaded) filename resolving to real media (§5) since there's no
  `media_refs` row for it yet.
- **B. Saved note only.** A `GET` endpoint (no body) that re-renders `note.Fields` as stored.
  Simpler (no field-count/validation edge cases), but useless mid-edit — the user has to save
  first to see a change, which is the opposite of what "preview while editing" usually means.
- **C. Both**: a `GET` for "preview as saved" (e.g. a link on the note's detail view, if one
  exists) plus the POST described in this plan for "preview what I'm typing." More surface area
  for one issue.

**Recommendation: A.** It's what "preview when in Card Edit mode" most plausibly means, it's not
meaningfully more code than B (the POST shape mirrors the existing save routes almost exactly),
and C is scope beyond what #181 asks for.

### 2. Presentation: inline panel vs. modal vs. dedicated preview page vs. fake study session

- **A. Inline panel on the same page (this plan).** A `<div id="note-preview">` on the edit/
  new-note page itself, filled via `hx-post` + `hx-swap`. No page navigation, no new page
  template beyond the fragment, no JS beyond what htmx already does elsewhere in this app.
- **B. Modal/dialog.** Same fragment, but swapped into a `<dialog>` opened on click. Slightly more
  UI polish (doesn't push page content down), needs a couple lines of Alpine (already loaded) to
  open/close it.
- **C. Dedicated preview page** (`GET /notes/{id}/preview` as a full page, or similar). Heavier:
  a new page template, a page navigation away from the edit form (loses in-progress unsaved
  field values unless they ride along as query params or a session-stashed draft), and doesn't
  fit the "unsaved values" answer to question 1 without extra plumbing.
- **D. "Fake study session"** (the issue's own suggestion) — reuse `review.html`/`review.js`,
  building a one-off in-memory `review.Batch` from the previewed card(s) instead of a DB query.
  This means computing `fsrs.PreviewAll` against a synthetic/zero prior `user_card_state` (since
  an unsaved or even a saved note's cards may have real prior scheduling state that a "preview"
  has no business reading or displaying), reusing the rating-button UI that implies grading is
  about to happen, and carefully ensuring the grading POST (`/api/reviews/batch`) is never wired
  up to these synthetic cards — i.e., building most of a second, parallel card-shape path through
  UI that was built to *look like* it grades. This is the most issue-literal option but by far
  the most code and the most risk of the four (CLAUDE.md working rule 2: "no unrequested
  features/abstractions").

**Recommendation: A.** Simplest, reuses existing infrastructure (`renderFragment`, htmx already
loaded), and keeps the preview visibly distinct from a real review session — no rating buttons,
no "Show Answer" toggle, nothing that looks gradeable, which sidesteps needing to defend against
someone mistaking a preview for a real review.

### 3. Preview on the new-note form as well as the edit form

- **A. Both** (this plan). Same fragment/handler shape for `/decks/{deckId}/notes/new` and
  `/notes/{id}/edit`, via two routes that both call the same `buildNotePreview` helper.
- **B. Edit form only.** Smaller surface (#181's title says "Card Edit mode" specifically), but
  the new-note form has the exact same "does this look right before I commit it" need, arguably
  more so — there's no saved fallback to compare against.

**Recommendation: A.** The marginal cost is one extra route + one extra thin handler calling the
same shared helper; the new-note form is the case a preview matters most for (nothing saved yet
to fall back on if the cards come out wrong).

### 4. `{{type:Field}}` in a preview

- **A. Render the real widgets** (this plan's default): `render.TypeAnswerInput` on the question
  side (a live `<input>`, same as the reviewer — a preview author could actually type into it,
  though nothing checks or scores it since there's no grading path here) and
  `render.TypeAnswerExpected` on the answer side (shows the expected answer as text). Zero new
  code — both functions already exist and already do exactly this for the reviewer
  (`internal/review/batch.go:463-464`).
- **B. Static placeholder only.** Replace the widget with inert text like `[type answer field]`
  on both sides, never showing the expected answer even on the "answer" side. Needs a small new
  branch in the handler to skip `TypeAnswerInput`/`TypeAnswerExpected`.
- **C. Show the expected answer on both sides.** Use `TypeAnswerExpected` for both question and
  answer, so a preview author immediately sees what's expected without an inert input at all.
  Also a small new branch.

**Recommendation: A.** No new code, and consistent with "the preview shows what a real review of
this card would look like" — the input simply does nothing when submitted, same as any other
inert control on a page with no submit handler for it.

### 5. Media that's referenced only in unsaved text

Whether a brand-new filename (never associated with this deck via `media_refs`) can resolve at
all in a preview:

- **A. Cannot resolve; `<img>` left as an unresolved relative src and 404s in the browser** (this
  plan). Zero new code — this is what `render.RewriteMediaSrcs` already does for any unresolved
  filename, reused unmodified.
- **B. Add a client-side "no image available" placeholder.** Detect unresolved `<img>` in the
  fragment (e.g. a JS `onerror` handler swapping in a placeholder icon) so the broken-image icon
  doesn't look like an app bug. Small, purely cosmetic addition, no server-side change.
- **C. Support uploading media ahead of/during note editing** so a brand-new filename can resolve
  before the note is saved. This is a real feature (an upload endpoint, a way to associate the
  upload with the eventual note/deck) — well beyond #181's scope.

**Recommendation: A**, optionally **B** as a small polish pass if the broken-image icon reads as
confusing in practice. **C is out of scope** for this issue.

---

## Resolved decisions

All five open questions above were put to the user and resolved on 2026-09-02. These are
binding: implement exactly these, and do not re-litigate them.

### Resolved decision 1 — preview source: **Option A, unsaved form values**
Preview renders the values currently in the form, not the saved note. Implement the POST
endpoint that takes `field[]`, `tags`, and `note_type_id` from the form exactly as typed and
renders those. Do NOT add a GET "preview as saved" variant (that was option C, rejected as
scope beyond #181).

### Resolved decision 2 — presentation: **Option A, inline panel on the same page**
A `<div id="note-preview">` on the edit / new-note page itself, filled via a fragment swap.
No page navigation, no new full-page template beyond the fragment.

Explicitly rejected: **Option D, the "fake study session"** the issue text suggested. Do not
reuse `review.html` / `review.js`, do not construct a synthetic `review.Batch`, and do not call
`fsrs.PreviewAll` from the preview path. The preview must be visibly distinct from a real
review: **no rating buttons, no "Show Answer" grading affordance, nothing that looks
gradeable.** This is the line invariants §2.5/§2.7 require — the preview path never touches
`user_card_state`, `review_log`, or FSRS at all.

### Resolved decision 3 — which forms: **Option A, both**
Preview is available on both `GET /decks/{deckId}/notes/new` and `GET /notes/{id}/edit`. Two
routes, two thin handlers, both calling the same shared `buildNotePreview` helper.

### Resolved decision 4 — `{{type:Field}}`: **Option A, render the real widgets**
Use the existing `render.TypeAnswerInput` on the question side and `render.TypeAnswerExpected`
on the answer side, exactly as the reviewer does. No new branching in the handler. The input is
inert because nothing is wired to submit it — that is acceptable and intended.

### Resolved decision 5 — unresolved media: **Option A, leave unresolved**
A filename with no `media_refs` row keeps its unresolved relative `src` and 404s in the browser,
showing the browser's usual broken-image icon. Reuse `render.RewriteMediaSrcs` unmodified. Add
no `onerror` placeholder (option B) and no pre-save upload endpoint (option C, out of scope —
would need its own issue).
