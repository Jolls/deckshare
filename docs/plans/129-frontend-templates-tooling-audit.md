# Issue #129 — Audit: frontend templates & tooling

Audit zone: `web/templates/` (16 files), `web/embed.go`, `web/static.go`, `cmd/enshu/main.go`,
`cmd/seed/main.go`, `.github/workflows/ci.yml`, `compose.yaml`, `Dockerfile`, `sqlc.yaml`,
`.golangci.yml`, `scripts/`. Simplification/cleanup only — **no behavior change intended**.

This plan is meant to be applied mechanically. Every edit below is a literal find/replace;
none require a design decision. Anything that did require a judgment call is in
**Open questions** instead, not decided here.

## How templates are parsed (read before editing)

`internal/http/templates.go`'s `parseTemplates()` builds one `*template.Template` per page name
by calling `template.ParseFS(web.Templates, files...)` where `files` = `templates/layout.html`,
`templates/<name>.html`, plus whatever `pagePartials[name]` lists. `web/embed.go` embeds with
`//go:embed templates/*.html` — a single-level glob, so **new partial files must live flat in
`web/templates/`, not in a subdirectory** (a `templates/partials/` subfolder would need the
embed pattern changed too, which is unnecessary here — the existing `review_cards.html` partial
already lives flat and is the precedent to follow).

Adding a shared partial is: (1) add a new flat file under `web/templates/` with one or more
`{{define "name"}}...{{end}}` blocks, (2) add that file's path to `pagePartials[pageName]` for
every page that calls it, (3) replace the call site with `{{template "name" .Pipeline}}`.
`{{template "name"}}` with no pipeline is valid Go template syntax (passes `nil` as dot) — used
where the partial is fully static.

`scripts/`: `git ls-files scripts/` returns nothing — the directory isn't tracked (not even a
`.gitkeep`). The "scripts worth keeping" review-focus bullet is moot; no action.

CI (`.github/workflows/ci.yml`): reviewed line by line, no redundant steps found. The
standalone `go vet ./...` step (line 33) is arguably covered by golangci-lint's `standard`
preset (`.golangci.yml`'s `default: standard`), but CLAUDE.md §14's pre-commit sequence lists
`go build`, `go vet`, `golangci-lint run`, `go test` as separate required steps — that's an
explicit repo convention, not an oversight, so removing it is out of scope here (see Open
questions if it's still worth raising).

`compose.yaml`, `Dockerfile`, `sqlc.yaml`, `.golangci.yml`, `web/embed.go`, `web/static.go`,
`cmd/enshu/main.go`: read in full, no duplication or dead code found. No edits proposed.

---

## Findings — template duplication

Four exact-duplicate (or field-substitution-only) snippets recur across the 16 templates:

1. **Error/success message paragraphs** — `{{if .X}}<p style="color: red;">{{.X}}</p>{{end}}`
   (and the green/`Success` variant), same shape, only the field name differs:
   - `web/templates/login.html:3` (`.Error`)
   - `web/templates/signup.html:3` (`.Error`)
   - `web/templates/deck_new.html:3` (`.Error`)
   - `web/templates/import.html:3` (`.Error`)
   - `web/templates/settings.html:6,7` (`.ProfileError` / `.ProfileSuccess`)
   - `web/templates/settings.html:24,25` (`.PasswordError` / `.PasswordSuccess`)
   - `web/templates/settings.html:42,43` (`.FsrsError` / `.FsrsSuccess`)
   - Confirmed via `internal/http/settings.go` that `ProfileError`/`PasswordError`/`FsrsError`/
     `*Success` are always plain strings (or absent, i.e. `nil` map lookup) — never a richer
     `error` value — so `{{if .}}` on the passed field is behaviorally identical to
     `{{if .Field}}` on the original map.
   - Note: `import_ai.html` uses `.Errors` (a `[]string` rendered as a `<ul>`), a different
     shape — not part of this dedup.

2. **"No note types yet" message** — identical static line, 3 call sites:
   - `web/templates/note_form.html:13`
   - `web/templates/import_ai.html:16`
   - `web/templates/import_ai.html:28`

3. **"Back to deck" link** — identical except both call sites already share the same `.Deck`
   context object:
   - `web/templates/review.html:5`
   - `web/templates/review.html:34`
   - `web/templates/deck_edit.html:40`

4. **"Back to decks" link** — identical static line, 3 call sites:
   - `web/templates/deck_new.html:13`
   - `web/templates/import.html:11`
   - `web/templates/import_ai.html:52`

Considered and **rejected** (not proposed, too marginal / adds more indirection than it saves):
- The inline delete `<form>` in `web/templates/deck.html:27-29` and
  `web/templates/notetypes.html:18-20` — identical except the action URL prefix
  (`/notes/{id}/delete` vs `/note-types/{id}/delete`). A partial is possible via
  `{{template "inlineDelete" (printf "/notes/%s/delete" .ID)}}`, but it trades 3 duplicated
  lines for a `printf`-built URL string whose output needs verifying against `pgtype.UUID`'s
  formatting — not worth it for 2 call sites. See Open questions if this should be reconsidered.
- Page-level nav paragraphs (`decks.html:3`, `notetypes.html:3`, `deck.html:6-11`) — each page's
  link set is meaningfully different (different links, different counts), not true duplication.

---

## Safe mechanical edits

### 1. New file `web/templates/messages.html`

```
{{define "errorMsg"}}{{if .}}<p style="color: red;">{{.}}</p>{{end}}{{end}}
{{define "successMsg"}}{{if .}}<p style="color: green;">{{.}}</p>{{end}}{{end}}
```

### 2. New file `web/templates/no_note_types.html`

```
{{define "noNoteTypesMsg"}}<p>You don't have any note types yet. <a href="/note-types/new">Create one</a> first.</p>{{end}}
```

### 3. New file `web/templates/back_to_deck.html`

```
{{define "backToDeck"}}<p><a href="/decks/{{.ID}}">Back to deck</a></p>{{end}}
```

### 4. New file `web/templates/back_to_decks.html`

```
{{define "backToDecks"}}<p><a href="/decks">Back to decks</a></p>{{end}}
```

### 5. `internal/http/templates.go` — register the new partials

Replace:

```go
var pagePartials = map[string][]string{
	"review": {"templates/review_cards.html"},
}
```

with:

```go
var pagePartials = map[string][]string{
	"review":    {"templates/review_cards.html", "templates/back_to_deck.html"},
	"deck_edit": {"templates/back_to_deck.html"},
	"login":     {"templates/messages.html"},
	"signup":    {"templates/messages.html"},
	"settings":  {"templates/messages.html"},
	"deck_new":  {"templates/messages.html", "templates/back_to_decks.html"},
	"import":    {"templates/messages.html", "templates/back_to_decks.html"},
	"import_ai": {"templates/back_to_decks.html", "templates/no_note_types.html"},
	"note_form": {"templates/no_note_types.html"},
}
```

(Keep the existing comment above the var, it still applies — just extend the map literal.)

### 6. `web/templates/login.html:3`

Old: `{{if .Error}}<p style="color: red;">{{.Error}}</p>{{end}}`
New: `{{template "errorMsg" .Error}}`

### 7. `web/templates/signup.html:3`

Old: `{{if .Error}}<p style="color: red;">{{.Error}}</p>{{end}}`
New: `{{template "errorMsg" .Error}}`

### 8. `web/templates/deck_new.html`

Line 3 — Old: `{{if .Error}}<p style="color: red;">{{.Error}}</p>{{end}}`
New: `{{template "errorMsg" .Error}}`

Line 13 — Old: `<p><a href="/decks">Back to decks</a></p>`
New: `{{template "backToDecks"}}`

### 9. `web/templates/import.html`

Line 3 — Old: `{{if .Error}}<p style="color: red;">{{.Error}}</p>{{end}}`
New: `{{template "errorMsg" .Error}}`

Line 11 — Old: `<p><a href="/decks">Back to decks</a></p>`
New: `{{template "backToDecks"}}`

### 10. `web/templates/import_ai.html`

Line 16 — Old: `<p>You don't have any note types yet. <a href="/note-types/new">Create one</a> first.</p>`
New: `{{template "noNoteTypesMsg"}}`

Line 28 — Old: `<p>You don't have any note types yet. <a href="/note-types/new">Create one</a> first.</p>`
New: `{{template "noNoteTypesMsg"}}`
(Note: identical text to line 16 — this file has two occurrences; both need this same edit
independently, they are not on consecutive lines.)

Line 52 — Old: `<p><a href="/decks">Back to decks</a></p>`
New: `{{template "backToDecks"}}`

### 11. `web/templates/note_form.html:13`

Old: `<p>You don't have any note types yet. <a href="/note-types/new">Create one</a> first.</p>`
New: `{{template "noNoteTypesMsg"}}`

### 12. `web/templates/review.html`

Line 5 (flush left) — Old: `<p><a href="/decks/{{.Deck.ID}}">Back to deck</a></p>`
New: `{{template "backToDeck" .Deck}}`

Line 34 (indented 2 spaces, inside `<section id="review-done" hidden>`) — Old:
`  <p><a href="/decks/{{.Deck.ID}}">Back to deck</a></p>`
New: `  {{template "backToDeck" .Deck}}`
(Preserve the existing 2-space indentation.)

### 13. `web/templates/deck_edit.html:40`

Old: `<p><a href="/decks/{{.Deck.ID}}">Back to deck</a></p>`
New: `{{template "backToDeck" .Deck}}`

### 14. `web/templates/settings.html`

Line 6 — Old: `{{if .ProfileError}}<p style="color: red;">{{.ProfileError}}</p>{{end}}`
New: `{{template "errorMsg" .ProfileError}}`

Line 7 — Old: `{{if .ProfileSuccess}}<p style="color: green;">{{.ProfileSuccess}}</p>{{end}}`
New: `{{template "successMsg" .ProfileSuccess}}`

Line 24 — Old: `{{if .PasswordError}}<p style="color: red;">{{.PasswordError}}</p>{{end}}`
New: `{{template "errorMsg" .PasswordError}}`

Line 25 — Old: `{{if .PasswordSuccess}}<p style="color: green;">{{.PasswordSuccess}}</p>{{end}}`
New: `{{template "successMsg" .PasswordSuccess}}`

Line 42 — Old: `{{if .FsrsError}}<p style="color: red;">{{.FsrsError}}</p>{{end}}`
New: `{{template "errorMsg" .FsrsError}}`

Line 43 — Old: `{{if .FsrsSuccess}}<p style="color: green;">{{.FsrsSuccess}}</p>{{end}}`
New: `{{template "successMsg" .FsrsSuccess}}`

---

## Findings — `cmd/seed/main.go` (currently 284 lines)

`seedBasicNotes` (lines 178-210) and `seedClozeNotes` (lines 217-249) are structurally
identical: same `ListTemplatesForNoteType` call, same "no templates" guard, same loop building
`fieldsAndChecksum` + `createNote` from a list of `(field1, field2)` string pairs. They differ
only in: the samples list's element type (`basicSamples` is `[][2]string`, `clozeSamples` is
`[]string` used as just the first field), and the literal note-type name in the error message
(`"Basic"` vs `"Cloze"`).

### Safe mechanical edit — merge into one function

In `cmd/seed/main.go`, replace `clozeSamples`'s declaration (lines 212-215):

```go
var clozeSamples = []string{
	"The mitochondria is the {{c1::powerhouse}} of the cell",
	"Water freezes at {{c1::0}} degrees Celsius",
}
```

with:

```go
var clozeSamples = [][2]string{
	{"The mitochondria is the {{c1::powerhouse}} of the cell", ""},
	{"Water freezes at {{c1::0}} degrees Celsius", ""},
}
```

Replace both `seedBasicNotes` (lines 178-210) and `seedClozeNotes` (lines 217-249) with a single
function:

```go
func seedSampleNotes(ctx context.Context, pool *pgxpool.Pool, userID, deckID, noteTypeID pgtype.UUID, typeName string, samples [][2]string) error {
	q := db.New(pool)
	templates, err := q.ListTemplatesForNoteType(ctx, noteTypeID)
	if err != nil {
		return err
	}
	if len(templates) == 0 {
		return fmt.Errorf("note type %q has no templates", typeName)
	}

	for _, pair := range samples {
		fieldsJSON, checksum, err := fieldsAndChecksum(pair[0], pair[1])
		if err != nil {
			return err
		}
		guid, err := randomGuid()
		if err != nil {
			return err
		}
		if err := createNote(ctx, pool, db.CreateNoteParams{
			Guid:       guid,
			Fields:     fieldsJSON,
			Tags:       []string{},
			Checksum:   checksum,
			UserID:     userID,
			NoteTypeID: noteTypeID,
			DeckID:     deckID,
		}, []db.DesiredCard{{Ordinal: 0, TemplateID: templates[0].ID}}); err != nil {
			return err
		}
	}
	return nil
}
```

`fmt.Errorf("note type %q has no templates", typeName)` with `typeName = "Basic"` produces the
byte-identical string to the original `errors.New(`+"`"+`note type "Basic" has no templates`+"`"+`)`
(same for `"Cloze"`) — verified by hand: `%q` on `"Basic"` renders `"Basic"` (with quotes),
matching the literal backtick string. No behavior change. `fmt` is already imported in this
file (used elsewhere, e.g. `fmt.Errorf("connect to database: %w", err)`); no new import needed.
`errors` stays imported — still used for `DATABASE_URL is required` and the two
`note type ... not found` checks elsewhere in the file.

Update the two call sites in `run()` (currently lines 112-128):

```go
	if basicDeck.CardCount == 0 {
		if err := seedBasicNotes(ctx, pool, user.ID, basicDeck.ID, basicType.ID); err != nil {
			return fmt.Errorf("seed basic notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", basicDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", basicDeckName)
	}

	if clozeDeck.CardCount == 0 {
		if err := seedClozeNotes(ctx, pool, user.ID, clozeDeck.ID, clozeType.ID); err != nil {
			return fmt.Errorf("seed cloze notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", clozeDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", clozeDeckName)
	}
```

to:

```go
	if basicDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, user.ID, basicDeck.ID, basicType.ID, "Basic", basicSamples); err != nil {
			return fmt.Errorf("seed basic notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", basicDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", basicDeckName)
	}

	if clozeDeck.CardCount == 0 {
		if err := seedSampleNotes(ctx, pool, user.ID, clozeDeck.ID, clozeType.ID, "Cloze", clozeSamples); err != nil {
			return fmt.Errorf("seed cloze notes: %w", err)
		}
		log.Printf("seeded sample notes in %s", clozeDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", clozeDeckName)
	}
```

This removes ~35 lines of exact duplication with no change to logged output, error text, or
control flow.

---

## Verification steps (for the implementing session)

1. `go build ./...` — new template partials are embedded via the existing `templates/*.html`
   glob (flat files, no embed.go change needed); `cmd/seed` must still compile.
2. `go vet ./...` and `golangci-lint run`.
3. `go test ./...` — searched `internal/http/*_test.go` for the literal strings being touched
   (`Back to deck`, `color: red`, `You don't have any note types`); the only hit is
   `security_test.go:192`'s `color: red` check, which asserts a *note's own* sanitised CSS
   block (`.card { color: red; }` from `review.html`'s `{{range .CSS}}<style>...`), unrelated
   to the error-message partial — it isn't affected by this plan. No test asserts exact HTML for
   any of the snippets being extracted, but re-run the suite to confirm.
4. No migration/schema change, no `go generate` / sqlc regen involved — nothing to commit there.

---

## Open questions

1. **`cmd/seed/main.go` `run()` still has two near-identical `if CardCount == 0 {...} else
   {...}` blocks** (one for the basic deck, one for the cloze deck) after the function merge
   above. They could be further collapsed into a loop over a small slice of
   `{deck, noteType, typeName, samples, deckName}` tuples, but that's a structural/style choice
   (worth a spec struct for 2 items?) rather than a mechanical dedup, so it's not included in
   the plan above. Worth doing as a follow-up, or leave as is?

2. **Inline delete `<form>` duplication** (`web/templates/deck.html:27-29` vs
   `web/templates/notetypes.html:18-20`) — identical markup except the action URL prefix. A
   partial is possible (`{{template "inlineDelete" (printf "/notes/%s/delete" .ID)}}`) but was
   rejected above as more indirection than the ~3 duplicated lines justify, and its correctness
   depends on `pgtype.UUID`'s `%s`-verb formatting matching its current direct-interpolation
   output (not independently verified). Worth doing, or leave the two forms as they are?

3. **Standalone `go vet ./...` step in `.github/workflows/ci.yml:33`** — potentially redundant
   with golangci-lint's `standard` linter preset (which includes `govet`), but CLAUDE.md §14's
   pre-commit sequence explicitly lists `go vet` as a separate required step, so this plan
   treats it as intentional and proposes no change. Flagging in case that reading is wrong and
   the CI step should be reconsidered — but note removing it is an observable CI-behavior
   change, which the issue's "no behavior change intended" framing argues against touching in
   an audit PR regardless.

## Resolved decisions

1. **Seed loop restructuring:** leave as-is. Two `if/else` blocks in `run()` after the function
   merge (item 1) is not further collapsed — 2 items doesn't justify a spec-table abstraction.
2. **Inline delete `<form>` dedup:** leave as-is. `deck.html:27-29` and `notetypes.html:18-20`
   stay two separate, non-partialed forms.
3. **CI `go vet` step:** **remove** the standalone `go vet ./...` step
   (`.github/workflows/ci.yml:33`). Confirmed `.golangci.yml` sets `default: standard`, and
   golangci-lint v2's `standard` preset includes the `govet` linter, so the two steps run
   overlapping checks. Remove line 33 (`- run: go vet ./...`) entirely; `go build ./...`
   (line 32) and the `golangci-lint-action` step (line 44-46, which now covers vet) remain.
   This does not affect CLAUDE.md §14's local pre-commit sequence, which is unchanged and still
   lists `go vet` as a required local step before every commit.
