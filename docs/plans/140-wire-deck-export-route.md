# #140 — Wire `GET /decks/{id}/export`

Issue: [#140](https://github.com/Jolls/enshu/issues/140) — *Wire `GET /decks/{id}/export` using
`internal/apkg`'s Export/Write*. Area: `area: apkg`. No schema change, **no migration**.

`docs/routes.md`'s Import/export table already specifies the target:

> `GET /decks/{id}/export` | `can_view` | Stream a `.apkg` (`db → IR → write`,
> `Content-Disposition: attachment`) — a read of the deck's content, not an edit

Today `apkg.Export` exports a *whole owner's* collection scoped only by `owner_id`
(`internal/apkg/dbexport.go:25`, `internal/db/queries/export.sql`), and has no HTTP caller —
`write_test.go` is the only one.

---

## 0. Settled design decisions (do not re-litigate)

1. **Export is scoped to one deck and to the *requesting caller*.** New signature:
   `Export(ctx, tx, deckID, callerID pgtype.UUID, now time.Time)`. Content (decks, note types,
   notes, cards) comes from the deck; progress (`user_card_state`, `review_log`) comes from
   `callerID` — so a collaborator exporting a shared deck gets **their own** progress, not the
   owner's. This is what `docs/apkg-format.md`'s Export section already says export is
   ("`db -> IR -> apkg` for one user, flattening **their** `user_card_state`").
2. **Permission is `can_view`**, enforced *at the query layer* (CLAUDE.md §9): every statement in
   `export.sql` joins `deck_access` on `(deck_id, caller_id) AND can_view`. Handler-level guards
   alone are not sufficient per §9. No access ⇒ `pgx.ErrNoRows` from the deck query ⇒ 404
   (the codebase's established "collapse no-access to 404" convention).
3. **Card membership defines the export set.** A card is in the export iff `cards.deck_id = deckID`;
   a note is in the export iff it has at least one such card (this is Anki's own deck-export
   behaviour, and `notes.deck_id` is only a *home deck* hint — a note's cards can live in several
   decks, `internal/apkg/ir.go`'s `IrNote.HomeDeckAnkiID` comment). Note types come along for the
   notes that are included, because `dbwrite.go`'s `importNotes` skips any note whose note type or
   home deck fails to resolve.
4. **Every exported note's `HomeDeckAnkiID` is the exported deck.** The package contains exactly one
   deck, so there is nothing else it could resolve to, and `importNotes`
   (`internal/apkg/dbwrite.go:309`) *skips the note entirely* if it doesn't resolve. Do **not** use
   `deckAnkiID[n.DeckID]` — a note whose home deck is a different deck would silently export a
   `HomeDeckAnkiID` of 0 and be dropped on re-import.
5. **Subdecks are not followed.** `/decks/{id}/export` exports exactly the one deck row. Enshu's
   deck hierarchy is name-only (`"::"` in `decks.name`, no parent column), and each subdeck is its
   own `decks` row with its own `deck_access`. Exporting a name-prefix tree would mean exporting
   decks the caller may not have `can_view` on. See Open questions.
6. **Buffer, then write.** `apkg.Write` takes an `io.Writer`, but streaming straight into the
   `http.ResponseWriter` makes a mid-write failure unrecoverable (headers already sent). Build into
   a `bytes.Buffer`, then set headers + `Content-Length` and copy. `/import` already holds a whole
   package in memory, so this is not a new memory profile.
7. **Media stays out** — `col.Media` is always empty on export (`dbexport.go`'s doc comment, #60).
   Unchanged by this work.

---

## 1. `internal/db/queries/export.sql` — rewrite (whole file)

Replace the file contents with the following. Query names change (`*ByOwner`/`ForOwner*` →
`*ForDeckExport`); the old names have no callers outside `dbexport.go`, so nothing else breaks.

```sql
-- Export (#59, wired to GET /decks/{id}/export by #140). Every statement here is called only from
-- internal/apkg/dbexport.go, and every one is scoped two ways at once:
--
--   * by DECK -- one deck's content, not an owner's whole collection. The route is deck-scoped
--     (docs/routes.md) and a caller exporting a deck shared with them has no business reading the
--     owner's other decks. Card membership is the definition: a card is in the export iff its own
--     cards.deck_id is this deck (architecture.md §20 -- cards.deck_id is authoritative, notes.deck_id
--     is only the note's home deck), and a note is in iff it has such a card. Note types follow the
--     notes, because Anki's package format cannot carry a note without its note type.
--
--   * by CALLER -- deck_access, never owner_id (CLAUDE.md §9's "no cross-user reads without a
--     deck_access row", "authorisation is explicit at the query layer"). The deck_access join is
--     repeated on every statement rather than trusted to the handler or to GetDeckForExport alone;
--     deck_access is primary-keyed (deck_id, user_id), so the join is a guard, never a fan-out.
--
-- user_card_state/review_log are the caller's OWN rows on this deck's cards -- not the owner's. A
-- shared deck's other reviewers' progress is exactly what apkg-format.md's Export section says
-- cannot be represented in a single Anki collection.

-- name: GetDeckForExport :one
SELECT d.* FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE d.id = sqlc.arg(deck_id);

-- name: ListNoteTypesForDeckExport :many
SELECT nt.* FROM note_types nt
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (
    SELECT 1 FROM notes n
    JOIN cards c ON c.note_id = n.id
    WHERE n.note_type_id = nt.id AND c.deck_id = sqlc.arg(deck_id)
)
ORDER BY nt.id;

-- name: ListFieldsForDeckExport :many
SELECT f.* FROM fields f
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (
    SELECT 1 FROM notes n
    JOIN cards c ON c.note_id = n.id
    WHERE n.note_type_id = f.note_type_id AND c.deck_id = sqlc.arg(deck_id)
)
ORDER BY f.note_type_id, f.ordinal;

-- name: ListTemplatesForDeckExport :many
SELECT t.* FROM templates t
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (
    SELECT 1 FROM notes n
    JOIN cards c ON c.note_id = n.id
    WHERE n.note_type_id = t.note_type_id AND c.deck_id = sqlc.arg(deck_id)
)
ORDER BY t.note_type_id, t.ordinal;

-- name: ListNotesForDeckExport :many
SELECT n.* FROM notes n
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (SELECT 1 FROM cards c WHERE c.note_id = n.id AND c.deck_id = sqlc.arg(deck_id))
ORDER BY n.id;

-- name: ListCardsForDeckExport :many
SELECT c.* FROM cards c
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE c.deck_id = sqlc.arg(deck_id)
ORDER BY c.id;

-- name: ListUserCardStateForDeckExport :many
SELECT ucs.* FROM user_card_state ucs
JOIN cards c ON c.id = ucs.card_id
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE c.deck_id = sqlc.arg(deck_id) AND ucs.user_id = sqlc.arg(caller_id);

-- name: ListReviewLogForDeckExport :many
SELECT rl.* FROM review_log rl
JOIN cards c ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE c.deck_id = sqlc.arg(deck_id) AND rl.user_id = sqlc.arg(caller_id)
ORDER BY rl.card_id, rl.reviewed_at;
```

Notes for the implementer:

- `sqlc` generates a `*Params` struct (`DeckID`, `CallerID`) for every one of these, since each now
  takes two named args. `GetDeckForExport` returns `db.Deck` (`SELECT d.*`).
- `GetDeckForExport` is a new query and is *not* `GetDeckForUser` — `GetDeckForUser` returns extra
  permission columns the exporter doesn't want, and adding a second consumer to it would couple the
  deck page's row shape to the exporter's.

### 1a. Regenerate (mandatory, explicit step)

```
go generate ./...      # runs `sqlc generate -f ../../sqlc.yaml` via internal/db/pool.go
go build ./...
```

`internal/db/export.sql.go` is generated — **do not hand-edit it**. Commit the regenerated file.
Verify the new `db.GetDeckForExportParams` / `db.ListNotesForDeckExportParams` etc. exist and that
`go build ./...` is clean before moving on.

---

## 2. `internal/apkg/dbexport.go`

### 2a. New signature + doc comment

Replace lines 16–25 (the doc comment and the `func Export(...)` line) with:

```go
// Export reads ONE deck's decks/note types/notes/cards row set, plus callerID's own scheduling
// state and review history on that deck's cards, into an IrCollection (db -> IR,
// architecture.md §4). Must be called inside a transaction it does not own; it only reads.
//
// Deck-scoped and caller-scoped, matching GET /decks/{id}/export (docs/routes.md, #140): a
// collaborator with can_view on someone else's shared deck exports that deck's content with
// THEIR OWN progress on it. Authorisation lives in export.sql's deck_access joins, not here --
// a caller without can_view gets pgx.ErrNoRows from GetDeckForExport and nothing else runs.
//
// Lossy in one direction by definition (apkg-format.md's Export section): a shared deck's other
// users' progress cannot fit in a single Anki collection. Media is never exported: #60 built the
// blob store for import only, so col.Media is always empty.
func Export(ctx context.Context, tx pgx.Tx, deckID, callerID pgtype.UUID, now time.Time) (*IrCollection, error) {
	q := db.New(tx)

	deck, err := q.GetDeckForExport(ctx, db.GetDeckForExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: reading deck for export: %w", err)
	}
	noteTypeRows, err := q.ListNoteTypesForDeckExport(ctx, db.ListNoteTypesForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing note types for export: %w", err)
	}
	fieldRows, err := q.ListFieldsForDeckExport(ctx, db.ListFieldsForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing fields for export: %w", err)
	}
	templateRows, err := q.ListTemplatesForDeckExport(ctx, db.ListTemplatesForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing templates for export: %w", err)
	}
	noteRows, err := q.ListNotesForDeckExport(ctx, db.ListNotesForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing notes for export: %w", err)
	}
	cardRows, err := q.ListCardsForDeckExport(ctx, db.ListCardsForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing cards for export: %w", err)
	}
	stateRows, err := q.ListUserCardStateForDeckExport(ctx, db.ListUserCardStateForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing scheduling state for export: %w", err)
	}
	reviewRows, err := q.ListReviewLogForDeckExport(ctx, db.ListReviewLogForDeckExportParams{DeckID: deckID, CallerID: callerID})
	if err != nil {
		return nil, fmt.Errorf("apkg: listing review history for export: %w", err)
	}
```

`fmt.Errorf("apkg: reading deck for export: %w", err)` wraps, so the caller's
`errors.Is(err, pgx.ErrNoRows)` still works — that is what the handler's 404 depends on. Do not
swallow it, and do not translate it to a new sentinel.

### 2b. Deck block (replaces current lines 61–67)

There is exactly one deck now, so the `deckAnkiID` map goes away:

```go
	deckAnkiID := exportAnkiID(deck.AnkiID, deck.CreatedAt.Time.UnixMilli())
	decks := []IrDeck{{AnkiID: deckAnkiID, Name: deck.Name, Description: deck.Description}}
```

### 2c. Note block — `HomeDeckAnkiID`

In the `for _, n := range noteRows` loop, change

```go
			HomeDeckAnkiID: deckAnkiID[n.DeckID],
```

to

```go
			// The package contains exactly one deck, so that is the only home deck a note can
			// have here. Notably NOT n.DeckID: a note whose cards span decks keeps its home deck
			// elsewhere, and dbwrite.go's importNotes SKIPS any note whose home deck does not
			// resolve in the package.
			HomeDeckAnkiID: deckAnkiID,
```

### 2d. Card block

In the `for _, c := range cardRows` loop, change `DeckAnkiID: deckAnkiID[c.DeckID],` to
`DeckAnkiID: deckAnkiID,` (every row already satisfies `c.deck_id = deckID`).

Everything else in `Export` (`deriveCardScheduling`, `deriveCrt`, `exportAnkiID`,
`uuidFallbackID`, the reviews loop) is unchanged. Delete nothing else; the `deckAnkiID` map
declaration is the only orphan your change creates.

---

## 3. `internal/http/export.go` — new file

```go
package http

import (
	"bytes"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/Jolls/enshu/internal/apkg"
	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
)

// registerExportRoutes wires GET /decks/{id}/export (docs/routes.md): one deck's content plus the
// CALLER's own progress on it, serialised db -> IR -> .apkg and sent as a download. can_view, not
// can_edit_* -- exporting reads the deck, it does not change it. Authorisation is enforced inside
// apkg.Export's queries (export.sql's deck_access joins, CLAUDE.md §9); no access surfaces here as
// a wrapped pgx.ErrNoRows and collapses to 404, the same as every other deck route.
func registerExportRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template, now func() time.Time) {
	mux.Handle("GET /decks/{id}/export", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}

		// Read-only: the transaction exists because apkg.Export requires one (it reads eight
		// statements that must see one consistent snapshot), and it is only ever rolled back --
		// there is deliberately no commitTx here.
		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		col, err := apkg.Export(r.Context(), tx, deckID, user.ID, now())
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		// Buffered rather than streamed straight into w: a Write failure partway through would
		// otherwise land after the 200 and the Content-Disposition, leaving the browser a
		// truncated .apkg it believes is complete. Packages are already held whole in memory on
		// the /import side.
		var buf bytes.Buffer
		if err := apkg.Write(col, &buf); err != nil {
			serverError(w)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+exportFilename(col.Decks[0].Name)+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = buf.WriteTo(w)
	})))
}

// exportFilename turns a deck name into a Content-Disposition-safe ASCII filename. Deliberately
// minimal: anything outside [A-Za-z0-9._-] (quotes, backslashes, path separators, CR/LF, and every
// non-ASCII rune) becomes "_", runs collapse, and the result is capped -- there is no filename*
// RFC 5987 form and no transliteration, because the name is a convenience for the downloading user,
// not an identifier anything reads back.
func exportFilename(deckName string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range deckName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	name := strings.Trim(b.String(), "_.")
	if len(name) > 80 {
		name = strings.Trim(name[:80], "_.")
	}
	if name == "" {
		name = "deck"
	}
	return name + ".apkg"
}
```

Add `"strconv"` to the import block (it is used for `Content-Length`).

`col.Decks[0]` is always present: `GetDeckForExport` is `:one`, so a missing/inaccessible deck has
already returned `pgx.ErrNoRows` above.

---

## 4. Route registration

`internal/http/http.go` — insert after the `registerImportRoutes` line in `NewHandler`:

```go
	registerExportRoutes(mux, pool, pages, time.Now)
```

`internal/http/auth_test.go` — insert the matching line in `newTestHandler`, after
`registerImportRoutes(mux, tx, pages, blobs, clock)`:

```go
	registerExportRoutes(mux, tx, pages, clock)
```

(A separate file/function rather than folding into `registerDeckRoutes`: `/import` and `/export`
are one aggregate in `docs/routes.md`, and `decks.go` is already the largest handler file.)

---

## 5. UI link — `web/templates/deck.html`

Add an Export button to the deck page's action row (line 16 area, before "Back to decks"), with no
permission guard — the page only renders for `can_view` callers, which is exactly the export
permission:

```html
    <a href="/decks/{{.Deck.ID}}/export" role="button" class="outline btn-sm">Export</a>
```

---

## 6. Tests

### 6a. `internal/apkg/write_test.go` — update `TestExport_RoundTripsThroughReimport`

The existing test exports owner A's whole collection in one call. `defaultSynthSpec` has **two**
decks and a note (`guid-note-1`) whose two cards live in *different* decks, so the deck-scoped
export must now be driven per deck and the results re-imported in sequence. Rewrite the middle of
the test (current lines 122–143); the whole assertion block below it (decks, notes, cards,
`import_due_position`, `user_card_state`, `review_log`) stays as-is and still passes, because
importing both decks converges on the same content.

Replace:

```go
	col2, err := Export(ctx, tx, ownerA, now)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(col2.Decks) != len(spec.Decks) { ... }
	if len(col2.Cards) != len(spec.Cards) { ... }

	var buf bytes.Buffer
	if err := Write(col2, &buf); err != nil { ... }
	col3 := readBytes(t, buf.Bytes())

	ownerB := seedUser(t, tx)
	if _, err := Import(ctx, tx, ownerB, col3, now, testMediaStore(t)); err != nil { ... }
```

with:

```go
	q0 := db.New(tx)
	ownerB := seedUser(t, tx)

	// #140: Export is deck-scoped, so the round trip is per deck. defaultSynthSpec's
	// "guid-note-1" has one card in each deck, which is the case that matters: each package
	// carries only its own deck's cards, and re-importing both must reassemble the note.
	totalCards := 0
	for _, d := range spec.Decks {
		deck, err := q0.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerA, Name: d.Name})
		if err != nil {
			t.Fatalf("owner A deck %q: %v", d.Name, err)
		}

		col2, err := Export(ctx, tx, deck.ID, ownerA, now)
		if err != nil {
			t.Fatalf("Export(%q): %v", d.Name, err)
		}
		if len(col2.Decks) != 1 || col2.Decks[0].Name != d.Name {
			t.Fatalf("Export(%q): decks = %+v, want exactly that deck", d.Name, col2.Decks)
		}
		for _, n := range col2.Notes {
			if n.HomeDeckAnkiID != col2.Decks[0].AnkiID {
				t.Errorf("Export(%q): note %q home deck = %d, want the exported deck %d",
					d.Name, n.Guid, n.HomeDeckAnkiID, col2.Decks[0].AnkiID)
			}
		}
		totalCards += len(col2.Cards)

		var buf bytes.Buffer
		if err := Write(col2, &buf); err != nil {
			t.Fatalf("Write(%q): %v", d.Name, err)
		}
		col3 := readBytes(t, buf.Bytes()) // Write's own output must re-parse cleanly

		if _, err := Import(ctx, tx, ownerB, col3, now, testMediaStore(t)); err != nil {
			t.Fatalf("second Import(%q): %v", d.Name, err)
		}
	}
	if totalCards != len(spec.Cards) {
		t.Fatalf("per-deck exports covered %d cards, want %d", totalCards, len(spec.Cards))
	}
```

`q := db.New(tx)` further down stays; `q0` above is separate only to avoid moving the existing
declaration. (Alternatively hoist the existing `q` above the loop and drop `q0` — either is fine,
pick one and be consistent.)

### 6b. `internal/apkg/write_test.go` — new `TestExport_ScopedToDeckAndCaller`

The invariant this issue introduces: a collaborator's export carries *their* progress, and only
the requested deck's content. Add after `TestExport_RoundTripsThroughReimport`:

```go
// TestExport_ScopedToDeckAndCaller is #140's scoping invariant: Export takes one deck and one
// CALLER, so a collaborator with can_view on a shared deck exports that deck's content with their
// own user_card_state -- never the owner's, and never the owner's other decks.
func TestExport_ScopedToDeckAndCaller(t *testing.T) { ... }
```

Test body, concretely:

1. `tx := beginTx(t)`; `ownerA := seedUser(t, tx)`; `collab := seedUser(t, tx)`.
2. `Import` `defaultSynthSpec`/`buildSchema11Package` as owner A (as in the existing test).
3. Look up both decks by name via `q.GetDeckByOwnerAndName`.
4. Grant the collaborator `can_view` on the **"Default"** deck only:
   `tx.Exec(ctx, "INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1,$2,true,true)", defaultDeck.ID, collab)`.
   (Direct SQL, matching how `internal/http/media_test.go` seeds fixture rows.)
5. Give the collaborator a distinguishable `user_card_state` row on one of that deck's cards —
   pick the card via the existing `findCardsForNote` helper on `guid-note-1` ordinal 0 — with a
   stability owner A's imported state does not have, e.g.
   `INSERT INTO user_card_state (user_id, card_id, state, due, stability, difficulty, reps, lapses) VALUES ($1,$2,2,now(),42.5,3.25,1,0)`.
   (Check `migrations/00010_user_card_state.sql` for NOT NULL columns without defaults and fill
   exactly those — do not guess the column list.)
6. `col, err := Export(ctx, tx, defaultDeck.ID, collab, time.Now())`; assert:
   - `len(col.Decks) == 1` and `col.Decks[0].Name == "Default"`.
   - Every `col.Cards` entry has `DeckAnkiID == col.Decks[0].AnkiID`, and the count equals the
     number of `defaultSynthSpec.Cards` with `Did: 1` (3).
   - No note in `col.Notes` is a note whose only cards are in the other deck (`guid-note-2` has a
     card in each deck, so all three notes are present — assert the guid set explicitly rather
     than a count, using `spec` to derive the expectation).
   - The card seeded in step 5 exports with `FSRS.Stability == 42.5` — i.e. the *caller's* state,
     not owner A's.
   - `Export(ctx, tx, subDeck.ID, collab, now)` (the deck the collaborator has **no**
     `deck_access` row on) returns an error satisfying `errors.Is(err, pgx.ErrNoRows)`.

### 6c. `internal/http/export_test.go` — new file

Follow `internal/http/import_test.go` / `access_test.go` conventions: `beginTx(t)`,
`newTestHandler(t, tx, auth.Config{})`, `loginCookie`, `doRequest`.

```go
// TestExportRoute_GoldenPath imports the real schema-18 fixture through /import, then exports the
// resulting deck back out through /decks/{id}/export and re-reads the response with apkg.Read --
// the HTTP half of CLAUDE.md §10.3's round-trip target (#140).
func TestExportRoute_GoldenPath(t *testing.T)
```

1. `tx := beginTx(t)`, handler + cookie, `os.ReadFile(mathFixture)` (the const already exists in
   `import_test.go`, same package — reuse it, don't redeclare).
2. `doUploadRequest(...)` to `POST /import`; take `loc := w.Header().Get("Location")` (that is
   `/decks/{id}`).
3. `w = doRequest(handler, "GET", loc+"/export", "", cookie, "")`.
4. Assert `w.Code == http.StatusOK`.
5. Assert `w.Header().Get("Content-Type") == "application/octet-stream"` and that
   `w.Header().Get("Content-Disposition")` starts with `attachment; filename="` and ends with
   `.apkg"`.
6. Assert the body re-parses: `body := w.Body.Bytes();
   col, err := apkg.Read(bytes.NewReader(body), int64(len(body)), apkg.DefaultArchiveLimits())`
   with `err == nil`, `len(col.Decks) == 1`, and `len(col.Cards) == 11` (the fixture's card count,
   already asserted-by-implication in `TestImportRoutes_RealFixture_GoldenPath`; if the fixture's
   cards span more than one deck, assert the per-deck count the import produced instead of 11 —
   derive it, don't hardcode blindly).

```go
// TestExportRoute_NoAccessIs404 is the authorisation half: a signed-in user with no deck_access
// row on someone else's deck gets the 404 page, not the package (CLAUDE.md §9).
func TestExportRoute_NoAccessIs404(t *testing.T)
```

1. Two accounts via two `loginCookie` calls with distinct `testEmail()`s.
2. User one imports the fixture (or creates a deck via `POST /decks` — cheaper; follow
   `decks_test.go`'s existing deck-creation helper if one exists).
3. `doRequest(handler, "GET", "/decks/"+id+"/export", "", cookieTwo, "")` ⇒ `http.StatusNotFound`,
   and the body must not begin with the ZIP magic `PK\x03\x04`.
4. Also assert a malformed id (`/decks/not-a-uuid/export`) ⇒ 404.

```go
// TestExportFilename is a pure unit test of the Content-Disposition sanitiser -- no DB, so it runs
// with DATABASE_URL unset.
func TestExportFilename(t *testing.T)
```

Table-driven: `{"Japanese Core", "Japanese_Core.apkg"}`, `{"Default::Sub", "Default_Sub.apkg"}`,
`{`a"b\c`, "a_b_c.apkg"}`, `{"日本語", "deck.apkg"}`, `{"", "deck.apkg"}`, `{"...", "deck.apkg"}`,
and one 200-char name asserting `len(got) <= 85`.

### 6d. Running the tests

Per CLAUDE.md §16, a green `go test ./...` with `DATABASE_URL` unset proves nothing here — every
new test but `TestExportFilename` is DB-backed. Export `DATABASE_URL` (see `.env.example`) and
confirm with `go test ./internal/apkg/... ./internal/http/... -run 'Export' -v` that nothing
reports `SKIP`.

---

## 7. Docs

- `docs/routes.md` — the `GET /decks/{id}/export` row already exists and stays. Amend its Purpose
  cell to record the two scopings: append " Scoped to the one deck (subdecks are separate decks and
  are not followed) and to the **caller's own** progress — a collaborator exports the deck's content
  with their own `user_card_state`, never the owner's." Also update the "Full-collection `.colpkg`
  export isn't scoped yet" paragraph below the table only if it now reads as stale (it does not —
  leave it).
- `docs/apkg-format.md` §Export — change the opening line from "`db -> IR -> apkg` for one user" to
  name the deck scoping too: "`db -> IR -> apkg` for one deck and one user, flattening that user's
  `user_card_state` back onto card rows (`GET /decks/{id}/export`, #140)." Leave the Known export
  losses subsection untouched; nothing here changes it.
- `CHANGELOG.md` — one new version entry at the top (bump `z`: `0.2.25` → `0.2.26`), then
  `git tag v0.2.26` after the version-bump commit:

```
## [0.2.26] - <YYYY-MM-DD>

### Added
- Decks can be exported back out as `.apkg` from the deck page: one deck's notes and cards plus
  the exporting user's own scheduling state and review history, so a collaborator on a shared
  deck exports their progress rather than the owner's
  ([#140](https://github.com/Jolls/enshu/issues/140)).
```

---

## 8. Order of work + verification

| # | Step | Verify |
|---|---|---|
| 1 | Rewrite `internal/db/queries/export.sql` | — |
| 2 | `go generate ./...` | `internal/db/export.sql.go` regenerated, new `*Params` types present |
| 3 | Update `internal/apkg/dbexport.go` (§2a–2d) | `go build ./...` clean |
| 4 | Add `internal/http/export.go` (§3) + registration (§4) | `go build ./...`, `go vet ./...` clean |
| 5 | Deck-page link (§5) | `go test ./internal/http/... -run Templates` (template parse) |
| 6 | Tests (§6) | `DATABASE_URL` set; `go test ./...` green, no unexpected SKIP |
| 7 | Docs + CHANGELOG (§7) | — |
| 8 | `golangci-lint run` | clean |

Pre-commit follows CLAUDE.md §14: build/vet/lint/test, then offer the review-pass multi-select
(recommend `/code-review medium` — this touches the `.apkg` writer and an authorisation surface,
but the diff is small and pattern-following), then pause for manual testing, then commit only on
explicit go-ahead. Branch: `feature/140-wire-deck-export-route`. PR body: `Closes #140`.

**Manual verification steps to hand the user** (do not start the server yourself, §14):
import a deck, click Export on the deck page, confirm the browser downloads
`<DeckName>.apkg`, and re-import that file into Anki (or back into a second Enshu account) and
confirm the cards and due dates look right.

---

## Open questions

None blocking — the two judgment calls below are decided above and recorded here only so the user
can overrule before implementation starts.

1. **Subdecks are not followed** (§0.5). `Default` exports without `Default::Sub`, so a user with a
   subdeck tree must export each deck separately. Alternatives, if this is unacceptable:
   (a) include decks whose `name` is `deck.name || '::%'` **and** which the caller has `can_view`
   on — a few extra lines in every `export.sql` statement (the deck predicate becomes a subquery),
   silently dropping subdecks the caller can't see; (b) leave as-is and open a follow-up issue for
   subdeck export. Recommendation: as-is, plus (b) if a user asks.
2. **`Content-Length` + buffering vs. true streaming** (§0.6). A very large deck is held whole in
   memory twice (IR + serialised bytes). Matches `/import`'s existing profile and the "no job queue
   for MVP" call in `docs/routes.md`; the alternative (stream into `w`, no `Content-Length`) trades
   a truncated-download failure mode for the memory saving. Recommendation: buffer, as specified.
