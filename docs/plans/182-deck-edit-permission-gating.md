# Plan: #182 — deck edit permission gating and a real "not found" page

## Problem

`GET /decks/{id}/edit` (and the sibling controls below) is gated at the query layer by
`deck_access` flags (CLAUDE.md §9), which is correct and must not change. But nothing in the
UI knows a caller's flags, so every permission-gated link/button renders unconditionally —
including to a collaborator who lacks the flag. Clicking it hits the query-layer deny and the
caller gets `http.Error(w, "not found", http.StatusNotFound)`: a bare, unstyled text response
with no header, no nav, no explanation.

Two fixes, both required per the issue:
1. Hide/show the controls themselves based on the caller's actual flags (display only — the
   query-layer deny stays exactly as strong).
2. Replace the bare-text 404 with a real page, for whoever still reaches it directly (typed
   URL, stale link, bookmark).

## Settled: this stays a 404, not a 403

`docs/architecture.md` (in §6, the paragraph beginning "A deck that exists but the caller
can't see should respond identically to a deck that doesn't exist") and the header comment on
`TestDeckRoutes_AccessControl` in `internal/http/decks_test.go` ("A deck that exists but is
invisible to the caller must 404, never 403 ... CLAUDE.md §10.5") already settle this: 404 is
deliberate, so an unauthorized caller cannot use the status code as an existence oracle. This
plan does not touch that. The only change is that the 404 response becomes a styled page
instead of bare `http.Error` text — same status code, same collapsed "absent or invisible"
semantics, better copy.

## Scope

In scope (all reached via query-layer checks that already collapse absent/invisible/forbidden
into one `pgx.ErrNoRows` → 404, per CLAUDE.md §9):

- Deck edit (`can_edit_settings`) — the issue's literal ask.
- Deck delete (`can_delete`) — same page (`deck_edit.html`), same bug shape.
- Manage access (`can_manage_access`).
- Add note / Import via AI (`can_edit_content`).
- Notes table Edit/Delete links on the deck page (`can_edit_content`).
- Decks-list Edit link (`can_edit_settings`).
- Note-type-change dropdown on the note edit page (`can_manage_access`, on top of the
  `can_edit_content` that already gates reaching the page at all).

Out of scope (see Open Questions):
- `can_study`-gated controls (the "Study" link and the deck's retention-target form) — same
  bug shape, but a different permission and not named in the issue; listed as a follow-up
  option below rather than folded in silently.
- `notetypes.go`, `aiimport.go`'s non-page paths, `review.go`, `media.go` — these either don't
  render the shared page layout or are JSON/asset endpoints, not HTML pages a user browses
  into by clicking a link. Their `notFound`/`handleQueryErr` calls are untouched.

## Part 1 — permission flags reach the templates

Each flag a template needs to gate on gets added to the SELECT of the query that already
authorises and fetches that row — no new query, no new round trip, no schema change. Field
names sqlc will generate follow the existing convention (`can_edit_content` →
`CanEditContent`), matching `accessFlags` in `internal/http/access.go`.

### `internal/db/queries/decks.sql`

**`GetDeckForUser`** (backs `GET /decks/{id}` → `deck.html`). Currently:
```sql
-- name: GetDeckForUser :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
WHERE d.id = sqlc.arg(deck_id);
```
Change the SELECT to `SELECT d.*, da.can_edit_content, da.can_edit_settings,
da.can_manage_access`. Leave the JOIN/WHERE untouched (still `can_view` only — this query's
authorization boundary does not change, it just also reports three more flags for the caller
who already passed it).

**`GetDeckForSettingsEdit`** (backs `GET/POST /decks/{id}/edit` → `deck_edit.html`). Add
`da.can_delete` to the SELECT (the page's own access already requires `can_edit_settings`; the
Delete button needs the separate `can_delete` flag, matching what `DeleteDeck`'s
`LockDeckForDelete` actually checks in `internal/db/queries/deletion.sql`).

**`ListDecksForUser`** (backs `GET /decks` → `decks.html`, and reused by
`internal/http/notes.go`'s move-to-deck dropdown and `internal/http/aiimport.go`'s deck
picker — both only read `.ID`/`.Name`, so the extra column is inert there). Currently:
```sql
-- name: ListDecksForUser :many
SELECT d.*, (SELECT count(*) FROM cards c WHERE c.deck_id = d.id) AS card_count
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
ORDER BY d.name;
```
Add `da.can_edit_settings` to the SELECT list (after `card_count`).

### `internal/db/queries/notes.sql`

**`GetNoteForContentEdit`** (backs `GET/POST /notes/{id}/edit` → `note_form.html`'s edit
branch). Currently:
```sql
-- name: GetNoteForContentEdit :one
SELECT n.*
FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE n.id = sqlc.arg(note_id);
```
Add `da.can_manage_access` to the SELECT.

### Regenerate

Run `go generate ./...` (sqlc) after editing the `.sql` files above and commit the regenerated
`internal/db/decks.sql.go` / `internal/db/notes.sql.go`. Do not hand-edit those files. sqlc will
rename the return types (e.g. `GetDeckForUser` will now return `GetDeckForUserRow` instead of
`Deck`, matching the existing pattern already used by `ListDecksForUserRow`) — this is a
mechanical rename with no behavior change; every existing field access (`deck.Preset`,
`deck.ID`, `note.NoteTypeID`, etc.) keeps working because those fields are still promoted onto
the new row struct.

### Handler changes

None needed beyond what already exists — every one of these rows is already passed whole into
the template as `"Deck": deck` / `"Note": note` in `internal/http/decks.go` and
`internal/http/notes.go`. The new flag fields ride along automatically; templates read them as
`.Deck.CanEditContent` etc. (see Part 2). Do not add new top-level map keys for these flags —
that would duplicate what's already reachable off `.Deck`/`.Note`.

## Part 2 — template gating

All changes are `{{if …}}` wraps around existing markup; no new markup, no restyling.

### `web/templates/deck.html`

```html
<nav class="button-nav">
    {{if .Deck.CanEditContent}}<a href="/decks/{{.Deck.ID}}/notes/new" role="button" class="outline btn-sm">Add note</a>{{end}}
    {{if .Deck.CanEditContent}}<a href="/import/ai?deck_id={{.Deck.ID}}" role="button" class="outline btn-sm">Import via AI</a>{{end}}
    {{if .Deck.CanEditSettings}}<a href="/decks/{{.Deck.ID}}/edit" role="button" class="outline btn-sm">Edit deck</a>{{end}}
    {{if .Deck.CanManageAccess}}<a href="/decks/{{.Deck.ID}}/access" role="button" class="outline btn-sm">Manage access</a>{{end}}
    <a href="/decks" role="button" class="outline btn-sm">Back to decks</a>
</nav>
```

Notes table row — wrap the `<td>` contents, keep the cell so the table doesn't lose a column:
```html
<td>
    {{if $.Deck.CanEditContent}}
    <a href="/notes/{{.ID}}/edit">Edit</a>
    <form method="post" action="/notes/{{.ID}}/delete" style="display:inline">
        <button type="submit" x-data @click="confirm('Delete this note?') || $event.preventDefault()">Delete</button>
    </form>
    {{end}}
</td>
```
(Inside `{{range .Notes}}`, `.` is the note row and `.Deck` isn't in scope — use `$.Deck.CanEditContent`, matching the existing `$.Counts`/`$counts` pattern already used in `decks.html`.)

Leave the "Retention target" `<details>` block unchanged (out of scope — see Open Questions).

### `web/templates/decks.html`

```html
<td>{{if .CanEditSettings}}<a href="/decks/{{.ID}}/edit">Edit</a>{{end}}</td>
```
(`.` inside `{{range .Decks}}` is the `ListDecksForUserRow`, which now carries `CanEditSettings` directly.)

### `web/templates/deck_edit.html`

Wrap the delete form:
```html
{{if .Deck.CanDelete}}
<form method="post" action="/decks/{{.Deck.ID}}/delete">
    <button type="submit" x-data @click="confirm('Delete this deck and all its notes and cards?') || $event.preventDefault()">Delete deck</button>
</form>
{{end}}
```
The rest of the page (the edit form itself) needs no gating — reaching this page at all already required `can_edit_settings`.

### `web/templates/note_form.html`

In the edit-note branch (the final `{{else}}` block), change:
```html
{{if .NoteTypeOptions}}
```
to:
```html
{{if and .NoteTypeOptions .Note.CanManageAccess}}
```
and leave the `{{else}}` (hidden `note_type_id` input) branch as-is — a caller without
`can_manage_access` now always falls into it, same as a caller with no compatible note types
today.

## Part 3 — a real "not found" page

### New template: `web/templates/not_found.html`

```html
{{define "content"}}
<h1>Not found</h1>
<p>This page doesn't exist, or you don't have access to it.</p>
{{template "backToDecks" .}}
{{end}}
```

### `internal/http/templates.go`

Add `"not_found"` to the page-name list in `parseTemplates` (alongside `"decks"`, `"deck"`,
etc.), and add an entry to `pagePartials`:
```go
"not_found": {"templates/back_to_decks.html"},
```

### New helpers

In `internal/http/templates.go`, next to `render`:
```go
// notFoundPage renders the styled 404 page for a page route (as opposed to notFound in
// pathparam.go, which stays a bare-text 404 for JSON/asset routes — see docs/plans/
// 182-deck-edit-permission-gating.md). Same collapsed absent-or-invisible semantics as every
// other deck route (CLAUDE.md §9, docs/architecture.md §6): the caller cannot distinguish "does
// not exist" from "exists but you lack the flag".
func notFoundPage(w http.ResponseWriter, pages map[string]*template.Template, user db.User) {
	render(w, pages["not_found"], http.StatusNotFound, map[string]any{"User": user})
}
```
(Needs `"github.com/Jolls/enshu/internal/db"` added to `templates.go`'s imports.)

In `internal/http/respond.go`, next to `handleQueryErr`:
```go
// handleQueryErrPage is handleQueryErr for a page route: same pgx.ErrNoRows → 404 collapse,
// but rendered through notFoundPage instead of the bare-text notFound.
func handleQueryErrPage(w http.ResponseWriter, pages map[string]*template.Template, user db.User, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		notFoundPage(w, pages, user)
	} else {
		serverError(w)
	}
	return true
}
```
(Needs `"html/template"` added to `respond.go`'s imports; `db` is already imported there.)

### Call-site substitution — `internal/http/decks.go`, `internal/http/access.go`, `internal/http/notes.go`

In these three files only, every `notFound(w)` becomes `notFoundPage(w, pages, user)`, and
every `handleQueryErr(w, <expr>)` becomes `handleQueryErrPage(w, pages, user, <expr>)`. In
every one of these handlers, `user, _ := auth.UserFromContext(r.Context())` already runs
before the first `notFound`/`handleQueryErr` call in that closure, and `pages` is already a
parameter of `registerDeckRoutes` / `registerAccessRoutes` / `registerNoteRoutes` — no new
parameter threading needed. Specifically:

**`internal/http/decks.go`** (`registerDeckRoutes`): convert all `notFound(w)` calls (currently
at the malformed-`id` guards in `GET /decks/{id}`, `GET /decks/{id}/edit`,
`POST /decks/{id}/edit`, `POST /decks/{id}/delete`, `POST /decks/{id}/settings/fsrs`, plus the
`n == 0` checks in the `POST /decks/{id}/edit` and `POST /decks/{id}/settings/fsrs` handlers)
and all `handleQueryErr(w, …)` calls (`GetDeckForUser`, `GetDeckForSettingsEdit`, and
`deleteDeck(...)` in `POST /decks/{id}/delete`).

**`internal/http/access.go`** (`registerAccessRoutes`): convert all `notFound(w)` calls
(malformed-id guards across all four routes, the `rows == 0` race check in
`POST /decks/{id}/access`) and both `handleQueryErr(w, …)` calls (`GetDeckForAccessManage` in
the GET and POST handlers). Change `handleAccessChangeErr`'s signature to
`handleAccessChangeErr(w http.ResponseWriter, pages map[string]*template.Template, user db.User, err error) bool`
and update its internal `notFound(w)` call (the `pgx.ErrNoRows` branch) to
`notFoundPage(w, pages, user)`; update its two call sites (in
`POST /decks/{id}/access/{userId}/edit` and `.../delete`) to pass `pages, user`.

**`internal/http/notes.go`** (`registerNoteRoutes`): convert all `notFound(w)` calls
(malformed-id guards, the `noteTypeID.Scan` failure in `GET /decks/{deckId}/notes/new`, the
`pgx.ErrNoRows` case in the `POST /decks/{deckId}/notes` error switch, the `n == 0` check in
`POST /notes/{id}/delete`) and all `handleQueryErr(w, …)` calls (`GetDeckForContentEdit`,
`GetNoteTypeForOwner` ×2, `GetNoteForContentEdit` ×2, `GetNoteForNoteTypeChange`,
`db.MoveNote(...)`).

Do not touch `notFound`/`handleQueryErr` in `internal/http/notetypes.go`,
`internal/http/aiimport.go`, `internal/http/review.go`, or `internal/http/media.go` — out of
scope (see Open Questions).

After this change, `internal/http/pathparam.go`'s `notFound` and
`internal/http/respond.go`'s `handleQueryErr` are still used (by the four files listed above as
out of scope), so neither becomes dead code — do not remove them.

## Tests

Follow the existing table-test pattern (`TestDeckRoutes_AccessControl` in `decks_test.go`,
`TestAccessRoutes_AccessControl` in `access_test.go`, `TestNoteRoutes_AccessControl` in
`notes_test.go`) — table of `(name, method, path, body, cookie, wantStatus)`, one row per
`(permission, resource, operation)` per CLAUDE.md §10.5. Status-code assertions in the existing
tables (`http.StatusNotFound`) do not change and need no edits — the status code is unchanged,
only the response body/content-type.

New assertions to add:

1. **`internal/http/decks_test.go`** — a body/content-type check that a 404 from a deck route
   now renders the styled page, not bare text: `GET` a nonexistent deck ID (or a deck the
   caller can't view) and assert `w.Header().Get("Content-Type")` starts with `text/html` and
   `w.Body.String()` contains a recognizable string from `not_found.html` (e.g. `"don't have
   access"`), not the literal old string `"not found"`. One such check is enough to cover
   `notFoundPage`/`handleQueryErrPage`'s wiring; it does not need to be repeated per route.

2. **`internal/http/decks_test.go`** — extend `TestDeckRoutes_GoldenPath` (or a new test) to
   assert the rendered `deck.html`/`decks.html` bodies:
   - Owner (all flags): body contains `"Edit deck"`, `"Manage access"`, `"Add note"` links.
   - A collaborator granted only `can_view` (use the `grantViewer` helper already in
     `access_test.go`, or the inline `INSERT INTO deck_access` pattern already used in
     `TestDeckFsrsRoute_DeniesWithoutCanStudy`): `GET /decks/{id}` body must NOT contain
     `/decks/{id}/edit`, `/decks/{id}/access`, or `/decks/{id}/notes/new`; `GET /decks` body
     must not contain an edit link for that deck's row.
   - A collaborator granted `can_view` + `can_edit_settings` only: body DOES contain the edit
     link, still does not contain the access-manage or add-note links.

3. **`internal/http/decks_test.go`** — `deck_edit.html`'s Delete button: a collaborator with
   `can_edit_settings` but not `can_delete` gets `GET /decks/{id}/edit` → 200 with a body that
   does not contain `action="/decks/{id}/delete"`; a collaborator with both flags gets a body
   that does.

4. **`internal/http/notes_test.go`** — the note-type-change dropdown: a collaborator with
   `can_edit_content` but not `can_manage_access` gets `GET /notes/{id}/edit` → 200 with a body
   that does not contain `name="note_type_id"` as a `<select>` (falls into the hidden-input
   branch); a collaborator with both flags gets the `<select>`.

None of these need new DB fixtures beyond what the existing access-control tests already set
up (`grantViewer`, direct `INSERT INTO deck_access` with explicit flag columns) — reuse that
pattern, don't invent a new one.

## Changelog

Add one entry under the current unreleased/next version in `CHANGELOG.md` (top of file is
`[0.2.18]`, dated today; per CLAUDE.md §14 this PR bumps to `[0.2.19]`) under `### Added` and
`### Fixed`:
```
### Fixed
- Deck- and note-editing controls (edit deck, delete deck, manage access, add note, import via
  AI, edit/delete note, change note type) are now hidden from collaborators who lack the
  underlying permission, instead of leading to a bare-text 404
  ([#182](https://github.com/Jolls/enshu/issues/182)).

### Added
- A styled "not found" page for deck/note routes a caller can't reach, replacing the bare-text
  404 response ([#182](https://github.com/Jolls/enshu/issues/182)).
```
Tag `v0.2.19` after committing, per CLAUDE.md §14.

## Success criteria

- `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` all pass.
- `go generate ./...` was run after editing the `.sql` files and the diff includes the
  regenerated `internal/db/decks.sql.go` and `internal/db/notes.sql.go` — no hand edits to
  those files.
- Every existing access-control table test (`TestDeckRoutes_AccessControl`,
  `TestAccessRoutes_AccessControl`, `TestNoteRoutes_AccessControl`,
  `TestDeckFsrsRoute_DeniesWithoutCanStudy`) still asserts `http.StatusNotFound` and still
  passes unmodified in its status-code expectations.
- A collaborator with only `can_view` on a deck, hitting `GET /decks/{id}`, sees no Edit deck /
  Manage access / Add note / Import via AI links, and no per-note Edit/Delete controls.
- A collaborator with only `can_view`, hitting `GET /decks`, sees no Edit link on that deck's
  row.
- A collaborator with `can_edit_settings` but not `can_delete`, on `GET /decks/{id}/edit`, sees
  no Delete deck button.
- A collaborator with `can_edit_content` but not `can_manage_access`, on `GET /notes/{id}/edit`,
  sees no note-type-change dropdown (falls back to the hidden fixed note-type input).
- Directly requesting any of `GET /decks/{id}/edit`, `POST /decks/{id}/edit`,
  `POST /decks/{id}/delete`, `GET|POST /decks/{id}/access`,
  `POST /decks/{id}/access/{userId}/edit|delete`, `GET /decks/{deckId}/notes/new`,
  `POST /decks/{deckId}/notes`, `GET|POST /notes/{id}/edit`, `POST /notes/{id}/delete`,
  `POST /notes/{id}/move` without the required flag still returns HTTP 404, but the response
  body is the styled `not_found.html` page (has the header/nav, not bare `http.Error` text).
- `GET /media/{sha256}` and `GET /api/reviews/next` (and the other explicitly-out-of-scope
  routes) are unchanged — still bare-text 404 via the original `notFound`/`handleQueryErr`.
- No change to which flag gates which route at the query layer — every `deck_access` JOIN
  condition in `internal/db/queries/*.sql` is identical before and after except for the added
  SELECT columns called out in Part 1.

## Open questions

### 1. How far should the "hide the button" treatment extend beyond deck edit?

The issue asks about "the edit button" specifically but frames the underlying question
generally ("can \[buttons\] be visible/not visible based on authorization?").

- **A. Deck edit only** (`can_edit_settings` on `deck.html`'s "Edit deck" link and
  `decks.html`'s per-row Edit link). Smallest possible diff, literal reading of the issue title.
  Leaves the identical bug live on Manage access, Add note, Import via AI, note edit/delete,
  and the note-type-change dropdown.
- **B. This plan's scope** — every `can_edit_*`/`can_manage_access`/`can_delete`-gated control
  reachable from `deck.html`, `decks.html`, `deck_edit.html`, and `note_form.html` (deck edit +
  delete + manage access + add note + import AI + note edit/delete + note-type change). Fixes
  every control with the exact same bug shape as the reported one, still leaves `can_study`
  gating untouched.
- **C. B, plus `can_study`-gated controls** — also hide/adjust the "Study" link (`deck.html`,
  `decks.html`) and the retention-target form (`deck.html`) for a `can_view`-only collaborator.
  Same bug shape, different permission and different page area; not mentioned in the issue.
- **D. C, plus the currently-excluded files** — also convert `notetypes.go`'s page routes,
  `aiimport.go`'s AI-import flow pages, and `review.go`'s `GET /decks/{id}/review` page to the
  styled 404 and add equivalent template gating (e.g. hiding the "Study" button from a
  `can_view`-only deck in the same nav where relevant). Broadest, but note-type routes aren't
  deck_access-gated at all (they're owner-only, no sharing), so "gating" there is a smaller,
  different-shaped change.

**Recommendation: B** (this plan's scope) — it closes every control with the issue's exact
failure mode without reaching into `can_study`, which is a different permission with its own
UX questions (e.g., should a view-only collaborator see queue counts at all?) better left to a
separate issue if wanted.

### 2. Copy and detail level of the "not found" page

- **A. Minimal** (what this plan writes): "Not found" / "This page doesn't exist, or you don't
  have access to it." — deliberately vague per the existence-oracle rule (Settled section
  above): it must read identically whether the resource never existed or the caller just lacks
  a flag.
- **B. Minimal, plus a link to request access** — same copy, plus "If you think you should have
  access, ask the deck's owner to grant it," with no deck name or owner identity (would leak
  existence). Slightly more helpful, more copy to maintain.
- **C. Distinguish "not logged in as the right account"** — add a note suggesting the user check
  they're signed into the correct account. Only makes sense if account-switching is a real
  workflow in this app; adds copy for a scenario that may not apply.

**Recommendation: A** — matches the terse tone of the rest of the app's copy (e.g.
`messages.html`), and avoids scope creep into a "request access" feature that doesn't exist
yet (no pending-invite flow, per the comment in `internal/http/access.go`).

---

## Resolved decisions

Both open questions above were put to the user and resolved on 2026-09-02. These are binding:
implement exactly these, and do not re-litigate them.

### Resolved decision 1 — gating scope: **Option B, this plan's scope**
Gate all `can_edit_content` / `can_edit_settings` / `can_manage_access` / `can_delete`-gated
controls across `deck.html`, `decks.html`, `deck_edit.html`, and `note_form.html`: edit deck,
delete deck, manage access, add note, import via AI, note edit, note delete, and note-type
change.

Out of scope, do not touch in this change:
- `can_study`-gated controls (the Study link, the retention-target form) — that was option C.
- Converting `notetypes.go`, `aiimport.go`, or `review.go` to the same treatment — that was
  option D.

### Resolved decision 2 — not-found page copy: **Option A, minimal and vague**
The page says only that the thing was not found / is not accessible. No "ask the owner for
access" hint, no "check which account you're signed into" hint. Both were considered and
rejected: either one nudges the reader toward concluding the resource exists, which erodes the
existence-oracle guarantee that is the entire reason this codebase mandates 404-never-403.

### Standing constraint — 404, never 403 (already settled; not an open question)
`docs/architecture.md` §6 and the header comment on `TestDeckRoutes_AccessControl` in
`internal/http/decks_test.go` already mandate that "does not exist" and "exists but you lack the
flag" collapse to the same 404, precisely so the response is not an existence oracle. This
change does **not** alter that: it makes the existing 404 render a styled page instead of bare
text. Do not introduce a 403 anywhere in this work, and do not make the two cases
distinguishable by status code, copy, timing, or headers.

### Standing constraint — gating is display-only
Hiding a control is a UX improvement layered on top of enforcement that already exists. Every
query-layer `deck_access` check (CLAUDE.md §9) stays exactly as strong as it is today. Do not
remove, relax, or skip a server-side check on the grounds that the button is now hidden — a
hidden button is not an access control.
