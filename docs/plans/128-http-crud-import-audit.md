# Issue #128: Audit — HTTP CRUD surface + import pipelines

Simplification/best-practices audit. **No behavior change intended.** Scope: `internal/http/{decks,notes,notetypes,settings,media,import,aiimport,errors,pathparam,static,templates,http}.go`, `internal/textimport/`, `internal/media/`. `review.go` and `auth.go` are explicitly out of scope (covered by sibling audit issues).

## Open questions (resolve before/at implementation start)

1. **Blanket 1:1 literal substitution (Change 1 below) — do it, or skip it?**
   Change 1 replaces two literal `http.Error(...)` strings with one-line helper calls at ~103
   call sites across 7 files, with zero control-flow change (pure rename, same status/message
   at every site). It mirrors the precedent already set by `pathparam.go`'s `notFound(w)`. The
   trade-off is diff size/reviewability vs. consistency + one place to change the message later.
   - **Option A (recommended):** do it everywhere in scope, as specified below — biggest diff,
     fully consistent, matches the issue's explicit invitation to look for this exact kind of
     helper.
   - **Option B:** skip Change 1 entirely; keep only the structural collapses (Changes 2-4),
     which have handleQueryErr/parseForm/startTx+commitTx calling `serverError`/`badRequest`
     only where those helpers are unavoidable as building blocks. Leaves the ~85 untouched
     literal sites inconsistent with the ~18 that Changes 2-4 necessarily rewrite.
   - **Option C:** do Change 1 only in the files Changes 2-4 already touch (all 7, as it
     happens — see Change 1's file list), which is actually identical to Option A here since
     every in-scope file with a literal hit is also touched by at least one of Changes 2-4.
   This plan is written assuming **Option A**. If Option B is chosen instead, drop Change 1 and
   in Changes 2-4 replace `serverError(w)`/`badRequest(w)` call-sites with the literal
   `http.Error(...)` they'd otherwise wrap (i.e. don't add `respond.go`'s `serverError`/
   `badRequest`, only add `handleQueryErr`/`parseForm`/`startTx`/`commitTx`, and have those four
   call the literal `http.Error(...)` directly instead of `serverError`/`badRequest`).

**Resolved decision: Option A.** Do the blanket substitution everywhere in scope, exactly as
Change 1 below specifies.

2. **Should `auth.go` and `review.go` (out of this issue's scope) be retrofitted to use the new
   `respond.go` helpers, since they contain the identical patterns** (`auth.go`: 3×
   `http.Error(w, "internal server error", ...)`, 2× `http.Error(w, "bad request", ...)`;
   `review.go`: 8× internal-server-error, 3× bad-request, 1× begin/commit-tx pair)?
   - **Option A (recommended):** no — leave them for the sibling auth/review-loop audit issues
     to pick up, since this issue's body explicitly carves those files out of scope. Note it as
     a known follow-on in this PR's description.
   - **Option B:** touch them anyway in this PR, since the helper already exists after Change 1
     and the swap is mechanical — but this means this PR reaches into files another audit issue
     owns, which risks a merge conflict with that issue's own PR.

**Resolved decision: Option B.** Retrofit `auth.go` and `review.go` now, in this PR — see
Change 5 below. User accepted the merge-conflict risk with the sibling audit issues' PRs.

## No change needed (reviewed, found correct)

- **Import path convergence (issue's review-focus #2).** `internal/apkg/dbwrite.go`'s `Import`
  intentionally does **not** call `db.CreateNoteWithCards` — it uses its own IR-specific
  functions (`CreateImportedNote`, `UpdateImportedNote`, `UpsertImportedCard`, etc.) because a
  binary `.apkg` import carries Anki-only baggage that a freshly-authored note never has:
  idempotent re-import keyed on `anki_id`/guid, due-state seeding (`seedCardStates`), and
  review-log replay (`importReviews` + `review.ReplayCard`). `internal/http/notes.go`'s
  `POST /decks/{deckId}/notes` and `internal/http/aiimport.go`'s `POST /import/ai` correctly
  **do** converge on the same pipeline: both call `db.CreateNoteWithCards` with the same
  `db.CreateNoteParams` shape, and `aiimport.go` calls `notes.go`'s own unexported
  `desiredCards`, `validateNoteFields`, and `randomGuid` directly (same package) rather than
  duplicating them — see `aiimport.go`'s own doc comment (lines 24-27) stating this design
  rationale. This is correct and by design; no consolidation needed.
- **`parseTags` (notes.go) vs. `dedupeTags` (aiimport.go).** Both dedup via a `map[string]struct{}`
  and an append loop, and look near-identical — but they take different input shapes
  (`parseTags` splits a raw space-separated string via `strings.Fields`; `dedupeTags` dedups an
  already-split `[]string` from parsed NDJSON and additionally skips empty strings). Only 2
  occurrences of the dedup-loop shape exist, below this audit's own 3-occurrence bar for
  extraction ("don't force an abstraction for 2 occurrences" — CLAUDE.md Simplicity First).
  Left as-is.
- **Media blob GC is genuinely absent, and correctly deferred, not a bug.** `internal/media/store.go`
  has exactly two methods, `Put` and `Open` — no delete/GC path exists anywhere in the package
  or in `internal/db`. The gap is explicitly documented: `docs/schema.md` §Media ("Orphaned
  `media_blobs` cleanup is deferred... nothing collects it"), and `docs/plans/51-deletion-policy.md`
  §0.8 ("Follow-up issue to file: 'Orphaned media blob GC', blocked on #60"). The schema itself
  enforces this at the constraint level: `media_refs.sha256 → media_blobs` is `ON DELETE RESTRICT`,
  so a referenced blob can never be deleted even accidentally. Confirmed as a known, intentional
  gap per the issue's own instruction — nothing to implement here.
- **Should this zone split into two issues/plans (core CRUD vs. import pipelines)?** No —
  recommend keeping it as one plan/PR. The primary simplification opportunity (the shared
  `respond.go` helpers) is genuinely cross-cutting: `aiimport.go` and `media.go` (the "import
  pipelines" half) use exactly the same `serverError`/`badRequest`/`handleQueryErr`/`parseForm`/
  `startTx`/`commitTx` helpers as `decks.go`/`notes.go`/`notetypes.go`/`settings.go` (the "core
  CRUD" half). Splitting would force either two PRs racing to create the same new file, or an
  artificial ordering dependency between them, for no isolation benefit — the two "halves" were
  never actually independent at the boilerplate layer.

## Change 1: new shared helpers file + blanket literal substitution

**New file: `internal/http/respond.go`**

```go
package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/enshu/internal/db"
)

// serverError writes the generic 500 response for an unexpected error, without leaking it to
// the client.
func serverError(w http.ResponseWriter) {
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// badRequest writes the generic 400 response for a malformed request.
func badRequest(w http.ResponseWriter) {
	http.Error(w, "bad request", http.StatusBadRequest)
}

// handleQueryErr writes the response for a query error and reports whether it wrote one: 404 if
// the row is absent or not visible to this caller (pgx.ErrNoRows, which a GetXForUser/
// GetXForOwner query returns for both cases via its deck_access join -- CLAUDE.md §9), otherwise
// a bare 500. err == nil always reports false. The caller must return immediately when this
// reports true.
func handleQueryErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
	} else {
		serverError(w)
	}
	return true
}

// parseForm calls r.ParseForm, writing a 400 and reporting false if the request body is
// malformed. The caller must return immediately when this reports false.
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		badRequest(w)
		return false
	}
	return true
}

// startTx begins a transaction, writing a 500 and reporting ok=false on failure. On success the
// caller must defer tx.Rollback(ctx) (a no-op after a successful commitTx) before doing anything
// else with tx.
func startTx(ctx context.Context, w http.ResponseWriter, store db.Beginner) (pgx.Tx, bool) {
	tx, err := store.Begin(ctx)
	if err != nil {
		serverError(w)
		return nil, false
	}
	return tx, true
}

// commitTx commits tx, writing a 500 and reporting false on failure.
func commitTx(ctx context.Context, w http.ResponseWriter, tx pgx.Tx) bool {
	if err := tx.Commit(ctx); err != nil {
		serverError(w)
		return false
	}
	return true
}
```

Naming check performed: no existing `serverError`, `badRequest`, `handleQueryErr`, `parseForm`,
or `commitTx` symbol exists anywhere in package `http` (including `_test.go` files, same
package). **`beginTx` is already taken** — `internal/http/auth_test.go:52` defines
`func beginTx(t *testing.T) pgx.Tx`, used 71 times across 10 test files. Do **not** name the new
helper `beginTx`; this plan uses `startTx` specifically to avoid that collision. Do not rename
the existing test helper.

**Blanket substitution** (mechanical, safe everywhere including inside `switch` statements —
same status code, same message, same control flow, just named):

In each of the following files, replace **every** occurrence of the exact literal
`http.Error(w, "internal server error", http.StatusInternalServerError)` with `serverError(w)`:
- `internal/http/decks.go` (16 occurrences)
- `internal/http/notes.go` (28 occurrences)
- `internal/http/notetypes.go` (11 occurrences)
- `internal/http/settings.go` (4 occurrences)
- `internal/http/media.go` (2 occurrences)
- `internal/http/import.go` (3 occurrences)
- `internal/http/aiimport.go` (10 occurrences)

Total 74. Verify after: `grep -rn 'http.Error(w, "internal server error"' internal/http/decks.go internal/http/notes.go internal/http/notetypes.go internal/http/settings.go internal/http/media.go internal/http/import.go internal/http/aiimport.go` returns nothing.

In each of the following files, replace **every** occurrence of the exact literal
`http.Error(w, "bad request", http.StatusBadRequest)` with `badRequest(w)`. Do **not** touch any
`http.Error` call whose message string differs (e.g. `"a note type needs a name and at least one
field"`, `"delete or re-type its notes first"`) — those are deliberately distinct messages, not
the generic case:
- `internal/http/decks.go` (10 occurrences)
- `internal/http/notetypes.go` (8 occurrences — includes the one inside the `switch` in
  `POST /note-types/{id}/edit`'s `case errors.Is(err, db.ErrFieldNotFound), errors.Is(err,
  db.ErrTemplateNotFound):` — replace just that case body's `http.Error(...)` line with
  `badRequest(w)`, leave the `case` line and the rest of the `switch` untouched)
- `internal/http/aiimport.go` (3 occurrences)
- `internal/http/settings.go` (3 occurrences)
- `internal/http/notes.go` (5 occurrences)

Total 29. Verify after: `grep -rn 'http.Error(w, "bad request"' internal/http/decks.go internal/http/notetypes.go internal/http/aiimport.go internal/http/settings.go internal/http/notes.go` returns nothing.

## Change 2: collapse the `pgx.ErrNoRows`-or-500 pattern with `handleQueryErr`

Apply **only** at the following 15 sites — each is the standalone shape
`if err != nil { if errors.Is(err, pgx.ErrNoRows) { notFound(w); return }; serverError(w)
[now, after Change 1]; return }` (or the `!errors.Is` skip-on-absence variant is explicitly
excluded, see below). Do **not** touch the `switch`-based error handling in `notes.go`'s two
note-create/update handlers or `notetypes.go`'s note-type-update handler (those switches have a
third case beyond ErrNoRows/default and must keep their `switch` structure — their `default:`
branch's `serverError(w)` from Change 1 stays as-is).

Replace, at each site, the block reading:
```go
if err != nil {
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	serverError(w)
	return
}
```
with:
```go
if handleQueryErr(w, err) {
	return
}
```

**decks.go** (3 sites):
- `GET /decks/{id}` handler: the block after `deck, err := q.GetDeckForUser(...)`.
- `GET /decks/{id}/edit` handler: the block after `deck, err := db.New(store).GetDeckForSettingsEdit(...)`.
- `POST /decks/{id}/delete` handler: the block after `if err := deleteDeck(r.Context(), store, deckID, user.ID); err != nil {` — note this site's `if` already carries the call inline (no separate `err !=nil` line above it); adapt to `if handleQueryErr(w, deleteDeck(r.Context(), store, deckID, user.ID)) { return }` — i.e. collapse the call and the check into the `handleQueryErr` argument, keeping `deleteDeck`'s own error return type (`error`) unchanged.

**notes.go** (7 sites — all follow the exact block shape above, no inlining needed):
- `GET /decks/{deckId}/notes/new` handler: after `deck, err := q.GetDeckForContentEdit(...)`.
- `GET /decks/{deckId}/notes/new` handler: after `nt, err := q.GetNoteTypeForOwner(...)` (second lookup in the same handler, when `note_type_id` is present).
- `POST /decks/{deckId}/notes` handler: after `nt, err := q.GetNoteTypeForOwner(...)`.
- `GET /notes/{id}/edit` handler: after `note, err := q.GetNoteForContentEdit(...)`.
- `POST /notes/{id}/edit` handler: after `note, err := q.GetNoteForContentEdit(...)`.
- `POST /notes/{id}/delete` handler: after `note, err := q.GetNoteForContentEdit(...)`.
- `POST /notes/{id}/move` handler: after `if err := db.MoveNote(r.Context(), tx, user.ID, noteID, targetDeckID); err != nil {` — same inlining as decks.go's delete site: `if handleQueryErr(w, db.MoveNote(r.Context(), tx, user.ID, noteID, targetDeckID)) { return }`.

Leave untouched (switch-based, keep structure, only the `default:` branch's `serverError(w)`
from Change 1 applies): the `switch` in `POST /decks/{deckId}/notes` (after `db.CreateNoteWithCards`)
and the `switch` in `POST /notes/{id}/edit` (after `db.UpdateNoteWithCards`).

**notetypes.go** (1 site):
- `GET /note-types/{id}/edit` handler: after `nt, err := q.GetNoteTypeForOwner(...)`.

Leave untouched (switch-based): the `switch` in `POST /note-types/{id}/edit` (after
`db.UpdateNoteType`) — its `case errors.Is(err, pgx.ErrNoRows): notFound(w)` case stays exactly
as-is; only its `default:` branch got `serverError(w)` from Change 1, and its
`ErrFieldNotFound`/`ErrTemplateNotFound` case got `badRequest(w)` from Change 1.

**aiimport.go** (3 sites):
- `GET /import/ai` handler: after `deck, err := q.GetDeckForContentEdit(...)`.
- `GET /import/ai` handler: after `nt, fields, err := loadNoteType(...)`.
- `POST /import/ai` handler: after `deck, nt, fields, err := loadDeckAndNoteType(...)`.

**media.go** (1 site):
- `GET /media/{sha256}` handler: after `blob, err := db.New(store).GetMediaBlobForUser(...)`.

Leave untouched (inverted logic — falls through to a default value rather than 404ing, a
different shape than "surface as not-found"): `settings.go`'s `GET /settings` handler's
```go
retention, err := db.New(store).GetGlobalFsrsRetention(r.Context(), user.ID)
if err != nil {
	if !errors.Is(err, pgx.ErrNoRows) {
		serverError(w)  // already updated by Change 1
		return
	}
	retention = review.DefaultDesiredRetention
}
```
This stays structurally as-is; only its `serverError(w)` came from Change 1.

**Import cleanup required after Change 2** (`go vet`/`golangci-lint` will fail on unused imports
otherwise — verify with `go build ./internal/http/...` after each file):
- `decks.go`: after converting all 3 sites, `errors.Is(err, pgx.ErrNoRows)` no longer appears
  anywhere in the file. Remove the now-unused `"errors"` and `"github.com/jackc/pgx/v5"` imports.
  Keep `"github.com/jackc/pgx/v5/pgtype"` (still used throughout, e.g. `pgtype.UUID`,
  `pgtype.Int4`, `pgtype.Timestamptz`).
- `aiimport.go`: after converting all 3 sites, same situation — remove `"errors"` and
  `"github.com/jackc/pgx/v5"`. Keep `"github.com/jackc/pgx/v5/pgtype"`.
- `media.go`: after converting the 1 site, remove `"errors"` and `"github.com/jackc/pgx/v5"`.
  (`media.go` does not import `pgtype`, so nothing else to keep there.)
- `notes.go`, `notetypes.go`, `settings.go`: **no import changes** — each still uses
  `errors.Is`/`pgx.ErrNoRows` elsewhere (the retained `switch` statements in `notes.go` and
  `notetypes.go`; the retained inverted-logic block in `settings.go`; `notes.go` additionally
  uses `errors.New` at file scope for `errNoClozeMarkers`).
- `import.go`: never imported `"errors"` or bare `"github.com/jackc/pgx/v5"` to begin with — no
  change.

## Change 3: collapse `r.ParseForm()` handling with `parseForm`

Apply at the following 12 sites — the shape
`if err := r.ParseForm(); err != nil { badRequest(w) [now, after Change 1]; return }` (in
`aiimport.go`'s case, preceded by an unrelated `r.Body = http.MaxBytesReader(...)` line, which
stays). Replace with `if !parseForm(w, r) { return }`.

- **decks.go** (3): `POST /decks`, `POST /decks/{id}/edit`, `POST /decks/{id}/settings/fsrs`.
- **notes.go** (3): `POST /decks/{deckId}/notes`, `POST /notes/{id}/edit`, `POST /notes/{id}/move`.
- **notetypes.go** (2): `POST /note-types`, `POST /note-types/{id}/edit`.
- **settings.go** (3): `POST /settings`, `POST /settings/password`, `POST /settings/fsrs`.
- **aiimport.go** (1): `POST /import/ai` (the line immediately after
  `r.Body = http.MaxBytesReader(w, r.Body, maxAIImportBytes)`).

Do **not** touch `import.go`'s `POST /import` handler — it calls `r.ParseMultipartForm`, a
different method with different semantics (multipart upload, not urlencoded form), not covered
by this helper.

## Change 4: collapse the begin-tx / commit-tx pairs with `startTx`/`commitTx`

Apply at the following 8 paired sites. Replace the begin block:
```go
tx, err := store.Begin(r.Context())
if err != nil {
	serverError(w)  // already updated by Change 1
	return
}
defer func() { _ = tx.Rollback(r.Context()) }()
```
with:
```go
tx, ok := startTx(r.Context(), w, store)
if !ok {
	return
}
defer func() { _ = tx.Rollback(r.Context()) }()
```
(the `defer` line is unchanged — it must stay in the caller's scope) — and replace the commit
block:
```go
if err := tx.Commit(r.Context()); err != nil {
	serverError(w)  // already updated by Change 1
	return
}
```
with:
```go
if !commitTx(r.Context(), w, tx) {
	return
}
```

- **decks.go** (1 pair): `POST /decks`.
- **notes.go** (3 pairs): `POST /decks/{deckId}/notes`, `POST /notes/{id}/edit`, `POST /notes/{id}/move`.
- **notetypes.go** (2 pairs): `POST /note-types`, `POST /note-types/{id}/edit`.
- **aiimport.go** (1 pair): `POST /import/ai`.
- **import.go** (1 pair): `POST /import`.

Do **not** touch `decks.go`'s `deleteDeck` helper function (lines ~308-319) — it takes a plain
`context.Context` (not `*http.Request`) and returns `error` to its caller rather than writing an
HTTP response itself, so `startTx`/`commitTx` (which take `http.ResponseWriter`) don't fit; it
already has its own minimal begin/commit shape appropriate to a non-handler helper. Leave as-is.

**Variable-name check:** at every one of the 8 sites, the existing code already names the
transaction variable `tx`. Four of the eight handlers (`notes.go`'s `POST /decks/{deckId}/notes`,
`POST /notes/{id}/edit`, `POST /notes/{id}/move`; `notetypes.go`'s `POST /note-types/{id}/edit`)
already have a local `ok bool` in scope from an earlier `id, ok := pathUUID(r, "id")` (or
`deckId`) call. `tx, ok := startTx(...)` in the same function scope is still valid Go — the `:=`
short-variable-declaration rule only requires at least one new variable on the left (`tx` is
new), so this legally reassigns the existing `ok` rather than redeclaring it, and it compiles
cleanly. This is safe because every such `ok` was already read-and-branched-on immediately after
its `pathUUID` call (`if !ok { notFound(w); return }`) and is never read again afterward in the
function — reassigning it later has no observable effect. The other 4 sites (`decks.go`'s
`POST /decks`; `notetypes.go`'s `POST /note-types`; `aiimport.go`'s `POST /import/ai`;
`import.go`'s `POST /import`) have no path-id parameter and so no pre-existing `ok` at all.

## Change 5: retrofit `auth.go` and `review.go` to use the `respond.go` helpers

Resolved per OQ2 — Option B. Apply the same mechanical, behaviour-identical substitutions as
Changes 1 and 4, scoped to these two files only. `auth.go` and `review.go` are both already
`package http`, so `respond.go`'s unexported helpers are directly visible — no new import needed
in either file for `serverError`/`badRequest`/`startTx`/`commitTx`.

**File: `internal/http/auth.go`**

Replace the literal `http.Error(w, "internal server error", http.StatusInternalServerError)`
with `serverError(w)` at these 3 line numbers (current content, verified by grep): lines 39, 78,
98.

Replace the literal `http.Error(w, "bad request", http.StatusBadRequest)` with `badRequest(w)`
at these 2 line numbers: lines 23, 63.

Verify after: `grep -n 'http.Error(w, "internal server error"\|http.Error(w, "bad request"'
internal/http/auth.go` returns nothing.

**File: `internal/http/review.go`**

Replace the literal `http.Error(w, "internal server error", http.StatusInternalServerError)`
with `serverError(w)` at these 8 line numbers: lines 46, 53, 58, 94, 100, 119, 126, 130.

Replace the literal `http.Error(w, "bad request", http.StatusBadRequest)` with `badRequest(w)`
at these 3 line numbers: lines 78, 83, 217.

Then collapse the one begin/commit-tx pair in the `POST /api/reviews/batch` handler (currently
lines 117-132):

```go
tx, err := store.Begin(r.Context())
if err != nil {
	http.Error(w, "internal server error", http.StatusInternalServerError)
	return
}
defer func() { _ = tx.Rollback(r.Context()) }()

results, err := review.GradeBatch(r.Context(), tx, user.ID, now(), events)
if err != nil {
	http.Error(w, "internal server error", http.StatusInternalServerError)
	return
}
if err := tx.Commit(r.Context()); err != nil {
	http.Error(w, "internal server error", http.StatusInternalServerError)
	return
}
```

becomes:

```go
tx, ok := startTx(r.Context(), w, store)
if !ok {
	return
}
defer func() { _ = tx.Rollback(r.Context()) }()

results, err := review.GradeBatch(r.Context(), tx, user.ID, now(), events)
if err != nil {
	serverError(w)
	return
}
if !commitTx(r.Context(), w, tx) {
	return
}
```

Note `ok` is already declared earlier in this handler (`events, ok := parseBatchRequest(w, r)`,
line 112) — `tx, ok := startTx(...)` legally reassigns it (same rule as the Change 4 call sites:
`:=` only requires one new variable on the left, and the prior `ok` was already
read-and-branched-on and never read again afterward). `store`'s parameter type in
`registerReviewRoutes` (`internal/http/review.go:31`) is already `db.Beginner`, matching
`startTx`'s signature exactly — no type change needed.

The middle `results, err := review.GradeBatch(...)` block's `http.Error(...)` becomes
`serverError(w)` via the same literal substitution as the other 8 sites above (it's already
counted in that list — line 126).

Verify after: `grep -n 'http.Error(w, "internal server error"\|http.Error(w, "bad request"'
internal/http/review.go` returns nothing, and `grep -n '\.Begin(\|\.Commit(' internal/http/review.go`
shows no remaining raw `store.Begin`/`tx.Commit` calls outside `startTx`/`commitTx`'s own
definitions in `respond.go`.

**Import cleanup check:** neither file currently imports anything that becomes unused as a result
of this change. `review.go` also has `errors.Is(err, pgx.ErrNoRows)`-guarded blocks elsewhere
(e.g. lines 90-95, 117-... via the `GetDeckForStudy` lookup) whose `notFound(w)`/`serverError(w)`
shape mirrors Change 2's `handleQueryErr` pattern from the 7 CRUD/import files — but Change 2 is
scoped to those 7 files only. Do not extend `handleQueryErr` into `auth.go`/`review.go` here;
this change (5) is limited to the literal-substitution and tx-pair collapses specified above.
`review.go`'s existing `"errors"` and `pgx` imports stay in use regardless (the untouched
`errors.Is(err, pgx.ErrNoRows)` checks). Verify with `go build ./internal/http/...` regardless.

## Verification (all changes)

1. `go build ./internal/http/...` — catches any orphaned import immediately.
2. `go vet ./internal/http/...`
3. `golangci-lint run ./internal/http/...`
4. `go test ./internal/http/...` — no test files change; this only proves behavior is unchanged.
   (DB-backed tests skip without `DATABASE_URL` set, per `auth_test.go`'s `testPool`; run with it
   set for full coverage of the tx-boundary changes in Change 4.)
5. Manual: no manual testing needed beyond the above — every change is a mechanical,
   behavior-preserving rewrite (same status codes, same messages, same control flow at every
   site), so there is nothing new to click through. Confirm via `git diff --stat` that only the
   8 listed `.go` files plus the new `respond.go` changed, and that no `.html`/template/migration
   file is touched.
