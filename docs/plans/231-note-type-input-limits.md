# #231 — Bound the note-type form and `parseTags`

Issue: [#231](https://github.com/Jolls/deckshare/issues/231) — `POST /note-types` accepts
unbounded fields, templates, names and CSS.

Scope: `POST /note-types`, `POST /note-types/{id}/edit`, and `parseTags` (`internal/http/notes.go`).
Application-level validation only — no migration. See §1 for why.

---

## 0. Investigation findings that fix the design

**0.1 Every comparable limit in this repo is application-level, not a DB CHECK.**

| Limit | Where enforced | How measured |
|---|---|---|
| deck name ≤ 200 | `internal/http/decks.go:123` and `:318`, `len(name) > 200` | **bytes** |
| note field ≤ 64 KiB | `internal/http/notes.go:700` `maxFieldBytes`, `len(v) > maxFieldBytes` in `validateNoteFields` | **bytes** |
| bulk selection ≤ 200 | `internal/http/notes.go:33` `maxBulkSelection`, checked in `parseBulkNoteIDs` | count |
| flag comment ≤ 2000 | `internal/http/flags.go:23` `maxFlagCommentLen`, `utf8.RuneCountInString` | **runes** (because it mirrors a `maxlength` textarea) |

`decks.name`, `decks.description`, `notes.fields` and `notes.tags` have **no** CHECK constraints
(`migrations/00006_decks.sql`, `00008_notes.sql`). The only length/range CHECK in the whole
migration set is `00021_user_card_state_flag_check.sql`, and that is a *domain* constraint
(`flag BETWEEN 0 AND 7`), not a size cap.

**0.2 Byte length, not rune count, for everything in this issue.** The unit question the task
flagged has a precedent to mirror in both directions: the *name* limits copy `decks.go`'s
`len(name) > 200` (bytes), and the CSS/`qfmt`/`afmt` limits copy `validateNoteFields`'s
`len(v) > maxFieldBytes` (bytes). `flags.go`'s rune count is the deliberate exception, and its
own comment says why — it exists to match a `maxlength` attribute on a textarea so the client
never gets a surprise rejection. The note-type name input already carries `maxlength="200"`
(`web/templates/notetype_form.html:47`, `:108`) and so does `deck_new.html` — i.e. the repo
already accepts the byte-vs-UTF-16 mismatch on `maxlength="200"` name inputs. Matching `decks.go`
keeps the two name limits identical; diverging would make two "≤ 200 character" rules behave
differently. **Decision: bytes everywhere in this change.**

**0.3 `desiredCards` confirms the storage multiplier.** `internal/http/notes.go:678` emits one
`db.DesiredCard` per template for a non-cloze note type, with no cap, so N templates ⇒ N cards
per note forever. The cloze branch is already bounded by the note's own field content and by
`maxFieldBytes`. So the template-count cap is the load-bearing one; the field-count cap is
symmetry plus a bound on `notes.fields` array width.

**0.4 The `.apkg` importer writes the same three tables** via `CreateImportedNoteType` /
`CreateImportedField` / `CreateImportedTemplate` (`internal/db/import.sql.go:114`, `:14`, `:152`),
and `internal/auth/notetypes.go` writes them at signup. This is the decisive fact for §1.

---

## 1. Decision: no migration, no CHECK constraints

The issue proposes schema CHECKs. **Do not add them.** Three reasons, in order of weight:

1. **A CHECK would break `.apkg` import.** The importer inserts note-type name, CSS, `qfmt` and
   `afmt` verbatim from a third-party file (§0.4). A CHECK turns an oversized shared deck — a real
   one, authored in Anki by someone else — into a `23514` constraint violation surfacing through
   `serverError` as a 500, on a path that has no branch for it. That is a worse failure than the
   unbounded write, and it directly threatens CLAUDE.md §10.3 (`.apkg` round-trip: `import(f)` must
   keep working for fixture files from several Anki versions). Bounding *our own form* while
   leaving the import path free is the correct asymmetry: the form is an attacker-controlled input
   surface, an import is a user's own data they already hold.
2. **Row-count limits are not expressible as a table CHECK anyway.** "at most 64 rows in `fields`
   for a given `note_type_id`" is a cross-row aggregate over a child table. A CHECK constraint is
   evaluated per row against that row's own columns; expressing this needs a trigger or an
   exclusion-style construct, both of which are new mechanism for this repo. So the two limits the
   issue cares about most (fields-per-note-type, templates-per-note-type) *must* be
   application-level regardless. Adding CHECKs for only the four length limits would split one rule
   across two enforcement layers for no gain.
3. **Precedent.** §0.1 — deck name, note field, bulk selection and flag comment are all
   application-level. Doing this one differently is the divergence that needs justifying.

Consequence: **no new migration file.** The next migration number, if a later issue needs one, is
`00022`. No `go generate` / sqlc run is needed — no `.sql` query file, schema file or generated
struct changes in this plan. (For the record: a CHECK constraint would not change sqlc output
either; sqlc reads column types, not constraints. This plan simply produces no schema change at
all.)

What *is* feasible as a CHECK, recorded so a future reader does not re-derive it: per-column
`length(name) <= 200`, `length(css) <= 65536`, `length(qfmt) <= 65536`, `length(afmt) <= 65536` on
`note_types` / `fields` / `templates` — all rejected above. Not feasible as a CHECK: any
per-parent row count.

No `docs/architecture.md` §20 deviation row is needed: this is a validation limit, not a
divergence from Anki's model.

---

## 2. Limits

Adopt the issue's table verbatim. `browser_qfmt` / `browser_afmt` are **not** in scope — the form
never submits them (`notetype_form.html` has no input for either) and they are import-only columns.

```go
// internal/http/notetypes.go, above registerNoteTypeRoutes.

// #231: the note-type form is the one input surface that establishes a permanent storage
// multiplier -- a note type with N templates generates N cards for every note ever written with
// it (desiredCards, notes.go) -- so it is bounded here the way every other write surface already
// is (deck name <= 200, note field <= 64 KiB, bulk selection <= 200). Lengths are BYTES, matching
// decks.go's name check and validateNoteFields's maxFieldBytes; the .apkg importer is deliberately
// not subject to these (it carries someone else's already-authored deck, and a hard limit there
// would fail a legitimate import).
const (
	maxFieldsPerNoteType    = 64
	maxTemplatesPerNoteType = 64
	maxNoteTypeNameBytes    = 200   // note-type, field and template names alike; matches the
	                                // maxlength="200" the name input already carries
	maxCSSBytes             = 64 * 1024
	maxFormatBytes          = 64 * 1024 // qfmt / afmt each
)
```

```go
// internal/http/notes.go, next to maxFieldBytes.

// #231: parseTags was the other unbounded write on this surface -- tags are a text[] column with
// no cap, and nothing stopped a direct POST from writing thousands.
const (
	maxTagsPerNote = 64
	maxTagBytes    = 100
)
```

---

## 3. File changes

### 3.1 `internal/http/notetypes.go` — `POST /note-types` (handler at line 103)

Insert the count checks immediately after the existing shape checks, and the per-item length checks
inside the existing template loop. Error convention: `http.Error(w, "<lowercase message>",
http.StatusBadRequest)`, matching the four messages already in this handler.

After the existing `if name == "" || len(fieldNames) == 0 { ... }` block (line 116-119), **change
that block's condition** to fold in the name cap, then add the field-count check:

```go
		if name == "" || len(name) > maxNoteTypeNameBytes || len(fieldNames) == 0 {
			http.Error(w, "a note type needs a name of at most 200 characters and at least one field", http.StatusBadRequest)
			return
		}
		if len(fieldNames) > maxFieldsPerNoteType {
			http.Error(w, fmt.Sprintf("a note type can have at most %d fields", maxFieldsPerNoteType), http.StatusBadRequest)
			return
		}
		for _, n := range fieldNames {
			if len(n) > maxNoteTypeNameBytes {
				http.Error(w, "a field name is too long", http.StatusBadRequest)
				return
			}
		}
		if len(css) > maxCSSBytes {
			http.Error(w, "the CSS is too large", http.StatusBadRequest)
			return
		}
```

`fmt` is already imported in this file. `fieldNames` is the output of `trimmedNonEmpty`, so the
count is of *surviving* fields — blank rows do not count against the cap, which matches how the
handler already treats them.

After the existing `len(templateNames) == 0 || ...` length-agreement check (line 124-127), add:

```go
		if len(templateNames) > maxTemplatesPerNoteType {
			http.Error(w, fmt.Sprintf("a note type can have at most %d templates", maxTemplatesPerNoteType), http.StatusBadRequest)
			return
		}
```

Inside the existing `for i, n := range templateNames` loop (line 130-137), after the existing
blank-name check:

```go
			if len(n) > maxNoteTypeNameBytes {
				http.Error(w, "a template name is too long", http.StatusBadRequest)
				return
			}
			if len(templateQfmts[i]) > maxFormatBytes || len(templateAfmts[i]) > maxFormatBytes {
				http.Error(w, "a template's question or answer format is too large", http.StatusBadRequest)
				return
			}
```

Ordering note: the template-count check must precede the loop so a 10,000-template submission is
rejected before 10,000 iterations of work.

### 3.2 `internal/http/notetypes.go` — `POST /note-types/{id}/edit` (handler at line 192)

Name and CSS, replacing the existing `if name == "" { badRequest(w); return }` at line 205-208:

```go
		if name == "" || len(name) > maxNoteTypeNameBytes || len(css) > maxCSSBytes {
			badRequest(w)
			return
		}
```

`badRequest(w)` (not `http.Error`) because that is what this handler already does for name here —
keep it.

Count checks, folded into the two existing emptiness checks so the shape of the handler does not
change:

```go
		fields, ok := parseFieldEdits(w, r)
		if !ok {
			return
		}
		if len(fields) == 0 {
			http.Error(w, "a note type needs at least one field", http.StatusBadRequest)
			return
		}
		if len(fields) > maxFieldsPerNoteType {
			http.Error(w, fmt.Sprintf("a note type can have at most %d fields", maxFieldsPerNoteType), http.StatusBadRequest)
			return
		}

		templates, ok := parseTemplateEdits(w, r)
		if !ok {
			return
		}
		if len(templates) == 0 {
			http.Error(w, "at least one template is required", http.StatusBadRequest)
			return
		}
		if len(templates) > maxTemplatesPerNoteType {
			http.Error(w, fmt.Sprintf("a note type can have at most %d templates", maxTemplatesPerNoteType), http.StatusBadRequest)
			return
		}
```

Both counts are post-parse, i.e. after blank-named rows have been dropped by
`parseFieldEdits`/`parseTemplateEdits` — the same "surviving rows" semantics as §3.1.

### 3.3 `internal/http/notetypes.go` — `parseFieldEdits` / `parseTemplateEdits`

Per-item length checks go here rather than in the handler: these two helpers already own per-entry
validation (blank name, malformed UUID, non-integer position) and already write the 400 themselves
via `badRequest(w)` and report `ok=false`. Add, inside each loop right after the existing
`name == ""` continue:

`parseFieldEdits` (after line 355's `continue` block):

```go
		if len(name) > maxNoteTypeNameBytes {
			badRequest(w)
			return nil, false
		}
```

`parseTemplateEdits` (after line 401's `continue` block):

```go
		if len(name) > maxNoteTypeNameBytes {
			badRequest(w)
			return nil, false
		}
		if len(qfmts[i]) > maxFormatBytes || len(afmts[i]) > maxFormatBytes {
			badRequest(w)
			return nil, false
		}
```

Update each helper's doc comment with one clause naming the new rejection, e.g. append to
`parseFieldEdits`'s comment: `An over-long name is a 400, not a dropped row (#231).`

### 3.4 `internal/http/notes.go` — tag limits

`parseTags` has four callers (`notes.go:171`, `:344`, `:601`, `note_preview.go:172`). Keep its
signature — a `([]string, error)` return would force `note_preview.go` to grow error handling for a
render-only path, and CLAUDE.md §9 forbids discarding the error. Instead add a sibling validator
next to `validateNoteFields`, mirroring its "return an error, let the handler write it" shape:

```go
// validateTags bounds the tag list a note can carry (#231): notes.tags is an unbounded text[] and
// parseTags applied no cap, so nothing stopped a direct POST from writing thousands. The count is
// checked AFTER parseTags's dedup, so a paste with repeats is not rejected for length it does not
// actually store. Byte length, matching validateNoteFields's maxFieldBytes.
func validateTags(tags []string) error {
	if len(tags) > maxTagsPerNote {
		return fmt.Errorf("at most %d tags", maxTagsPerNote)
	}
	for _, t := range tags {
		if len(t) > maxTagBytes {
			return fmt.Errorf("a tag is too long (max %d characters)", maxTagBytes)
		}
	}
	return nil
}
```

Call sites:

- `notes.go:171` (`POST /decks/{deckId}/notes`) and `notes.go:344` (`POST /notes/{id}/edit`) —
  replace `tags := parseTags(...)` with:

  ```go
  		tags := parseTags(r.PostForm.Get("tags"))
  		if err := validateTags(tags); err != nil {
  			http.Error(w, err.Error(), http.StatusBadRequest)
  			return
  		}
  ```

  This is exactly the convention the two `validateNoteFields` / `desiredCards` calls immediately
  above use in both handlers.

- `parseBulkTags` (`notes.go:600`) — add after the existing empty check, using that helper's
  own `(tags, ok)` convention:

  ```go
  	if err := validateTags(tags); err != nil {
  		http.Error(w, err.Error(), http.StatusBadRequest)
  		return nil, false
  	}
  ```

- `note_preview.go:172` — **unchanged, deliberately.** The preview handler renders and persists
  nothing (`note_preview.go:122` documents that even invalid mid-typing states answer 200), so a cap
  there would only produce a rejection for a state the save path will reject anyway, at the cost of
  breaking the live-preview contract. Note this in the commit message, not in code.

### 3.5 `web/templates/notetype_form.html` — client-side hints

Purely a UX nicety so the browser stops the user before the server does; the server checks are
authoritative. Add `maxlength="200"` to the field-name and template-name inputs (lines 59, 60, 65,
123, 130, 141, 153). Do **not** add `maxlength` to the `css` / `qfmt` / `afmt` textareas: those
limits are byte-based and 64 KiB, so a `maxlength` there would be both wrong in unit and useless in
practice. The `name` input already has `maxlength="200"` on both branches.

### 3.6 `web/templates/note_form.html` — tags input

Check whether the tags input exists and is unbounded; if present, no `maxlength` is added — the cap
is on tag *count* and per-*tag* length, neither of which a single `maxlength` on a space-separated
field expresses. Leave the template alone.

### 3.7 `CHANGELOG.md`

New `## [0.3.12] - <date>` entry:

```
### Security
- Bound the note-type form and note tags: at most 64 fields and 64 templates per note type, 200-byte
  names, 64 KiB CSS and card formats, 64 tags of at most 100 bytes each
  ([#231](https://github.com/Jolls/deckshare/issues/231)).
```

Tag `v0.3.12` after the version-bump commit (CLAUDE.md §14).

---

## 4. Tests

Add to `internal/http/notetypes_test.go`, following the file's existing shape: `beginTx(t)` →
`newTestHandler(t, tx, auth.Config{})` → `loginCookie(...)` → `doRequest(handler, "POST", path,
body, cookie, "http://example.com")`, bodies built with `url.Values`, DB assertions via
`countRows(t, tx, ...)`. Names must not collide with the seeded `Basic` note type (see
`newNoteTypeBody`'s comment) — use `Basic2`, `Basic3`, … per test.

Each test is a boundary pair: **exactly at the limit → 303 and the rows exist; one over → 400 and
nothing was written.** Asserting the *absence* of the row on the over-limit case is the part that
matters — a 400 that still wrote is the failure mode.

1. `TestNoteTypeCreate_FieldCountBoundary` — 64 `field_name[]` values → 303, `countRows` on
   `fields` == 64. Then 65 → 400, and `SELECT count(*) FROM note_types WHERE name = <that name>`
   == 0.
2. `TestNoteTypeCreate_TemplateCountBoundary` — 64 matched `template_name[]`/`qfmt[]`/`afmt[]`
   triples → 303, `countRows` on `templates` == 64; 65 → 400 and no `note_types` row.
   (Non-cloze; the cloze branch already requires exactly one template.)
3. `TestNoteTypeCreate_NameLengthBoundary` — `strings.Repeat("a", 200)` → 303;
   `strings.Repeat("a", 201)` → 400, no row.
4. `TestNoteTypeCreate_FieldAndTemplateNameLengthBoundary` — 200-byte field name and 200-byte
   template name → 303; 201 on each (two sub-cases) → 400, no row.
5. `TestNoteTypeCreate_CSSAndFormatSizeBoundary` — table-driven over
   `{css, qfmt, afmt} × {64*1024, 64*1024 + 1}` using `strings.Repeat("x", n)` → 303 / 400, with
   the over-limit cases asserting no `note_types` row.
6. `TestNoteTypeEdit_LimitsEnforced` — create a note type with `newNoteTypeBody()`, read back its
   id/field-id/template-id the way `TestNoteTypeEdit_StructuralChangeWithNotes_RequiresConfirmation`
   does, then POST `/note-types/{id}/edit` with (a) 65 field rows, (b) 65 template rows, (c) a
   201-byte name, (d) a 201-byte field name, (e) a 64 KiB + 1 `qfmt`, (f) a 64 KiB + 1 `css` —
   each expecting 400 — and assert the stored row is unchanged (`countRows` on `fields` still 2,
   on `templates` still 1, and `SELECT name FROM note_types WHERE id = $1` still `Basic2`). Then
   one at-limit case (64 field rows, each with a distinct name and `field_position[]`) expecting
   303.

Add to `internal/http/notes_test.go`:

7. `TestNoteCreate_TagLimits` — a note POST with 64 distinct space-separated tags → 303 and
   `SELECT cardinality(tags) FROM notes WHERE id = ...` == 64; 65 → 400 and no note row; one tag of
   100 bytes → 303; 101 bytes → 400.
8. `TestBulkTagAdd_TagLimits` — one over-limit case through
   `POST /decks/{deckId}/notes/bulk-tag-add` (65 tags) asserting 400 and that the selected note's
   `tags` is unchanged, so the `parseBulkTags` call site is covered too.

These are DB-backed: they need `DATABASE_URL` exported or they silently `t.Skip` (CLAUDE.md §16).
Scope every assertion to rows the test itself created — no table-wide `count(*)`, no unscoped
`LIMIT 1`.

---

## 5. Verification

1. `go build ./...`
2. `go vet ./...`
3. `golangci-lint run`
4. `$env:DATABASE_URL = ...; go test ./internal/http/... -run 'NoteType|Tag' -v` — confirm the new
   tests **ran** rather than skipped (`| Select-String -Pattern skip` should show none of them).
5. `go test ./...` with `DATABASE_URL` set.
6. No `go generate` needed (§1). No migration to apply.
7. Manual check to hand the user: create a note type at `/note-types/new` with a normal 2-field /
   1-template shape (still works), then try pasting a 300-character name (the browser's
   `maxlength` blocks it; a `curl`/devtools POST gets a 400).

## 6. Out of scope, recorded

- The `.apkg` import path (`CreateImportedNoteType` / `CreateImportedField` /
  `CreateImportedTemplate`) stays unbounded by design (§1.1). If a bound is ever wanted there it
  should be a *rejection with a readable import error*, not a CHECK — worth its own issue, not this
  one.
- `browser_qfmt` / `browser_afmt` — import-only, no form input (§2).
- `notes.fields` total size (as opposed to per-field) — `maxFieldBytes` × field count is now
  bounded transitively by the field-count cap, which is the practical fix; a separate aggregate cap
  is not proposed.

## Resolved decision (confirmed with the user)

App-level validation only, per §1 above — no migration, no CHECK constraints. Confirmed
2026-09-12. A follow-up issue tracks reconsidering DB-level constraints later (filed against
this decision, not blocking this plan).

Amendment (user decision after `/code-review low`, option B): `validateTags` is called only in the
bulk-tag-add path, not in the shared `parseBulkTags`, so bulk-tag-remove can still clear oversized
tags (e.g. imported ones). Note create and note edit keep `validateTags`. Covered by
`TestBulkTagRemove_OversizedTagStillRemovable`.

Amendment (after `/code-review high`): the code sketches above are superseded in four ways.
(1) Edits grandfather what import left over a limit — `validateTags(tags, existing)` and
`noteTypeLimitErr` only bound values the edit changes (and counts only when they grow), since the
importer is unbounded and otherwise an imported note or note type could not be edited at all.
(2) Names and tags are counted in characters (runes), matching `maxlength` and the messages;
`maxNameChars` is shared with the deck-name check. (3) `BulkAddNoteTags` caps each note's merged
list at `max_tags` in SQL, so repeated bulk adds cannot grow it past 64. (4) Every note-type limit
rejection names the value that failed.

Amendment (user decision after `/code-review low`, option B): `validateTags` is called only in the
bulk-tag-add path, not in the shared `parseBulkTags`, so bulk-tag-remove can still clear oversized
tags (e.g. imported ones). Note create and note edit keep `validateTags`. Covered by
`TestBulkTagRemove_OversizedTagStillRemovable`.
