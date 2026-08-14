# Plan: #54 — Deck / note-type / note / card CRUD

Phase 1, build-order step 5 (architecture.md §11). Handlers + queries for routes.md's **Decks**,
**Note types**, **Notes** tables; DB-level enforcement of `notes.owner_id = decks.owner_id`; card
generation on note create and diff-based regeneration on note edit.

Out of scope: `internal/render/`'s `{{Field}}`/cloze/conditional rendering engine (#55). This
issue adds exactly one pure function to `internal/render/` — a cloze *ordinal scanner* — because
card generation needs the ordinal set and #55 needs the same scan.

## 0. Resolved decisions (no judgment calls left downstream)

### 0.1 `notes.owner_id = decks.owner_id` is enforced by a composite foreign key

Mechanism, decided: **`UNIQUE (id, owner_id)` on `decks` + `FOREIGN KEY (deck_id, owner_id)
REFERENCES decks (id, owner_id)` on `notes`.**

Alternatives rejected on Postgres semantics, not taste:
- **`CHECK`** — cannot contain a subquery; a `CHECK` referencing `decks` is rejected outright by
  Postgres. Not available for this shape.
- **Generated column** — the generation expression must be immutable and may only reference
  columns of the same row. Cannot read `decks`. Not available.
- **Trigger** — works, but is procedural, needs *two* triggers (`BEFORE INSERT OR UPDATE` on
  `notes`, plus `AFTER UPDATE OF owner_id` on `decks`), and takes no lock on the parent row, so a
  concurrent `UPDATE decks SET owner_id` and `INSERT INTO notes` can interleave past it. A
  composite FK is the same rule declaratively, and RI takes a `KEY SHARE` lock on the parent row,
  which is what makes it race-free. schema.md/00007's "the guard lives in the query layer, not a
  trigger" reasoning was about the *last-holder* guard (which needs a row lock and a counting
  predicate); this is plain referential integrity and belongs in the schema.

The existing single-column `notes.deck_id → decks(id) ON DELETE RESTRICT` is **kept**: it is the
documented deck-delete tripwire (00008's comment, schema.md deletion policy), the composite FK
does not replace that comment's meaning, and dropping it is churn in a fix-forward migration. The
extra RI check fires only on deck delete, which is already an ordered transaction.

No new index is needed: the parent-delete check is
`SELECT 1 FROM notes WHERE deck_id = $1 AND owner_id = $2`, and `notes_deck_id_idx` leads with
`deck_id`.

Migration `migrations/00015_notes_owner_matches_deck.sql`:

```sql
-- +goose Up
-- #54: notes.owner_id is denormalised from decks.owner_id (docs/schema.md, Identifiers) because
-- UNIQUE (owner_id, guid) -- the import idempotency key -- cannot span a join. Until now nothing
-- enforced the equality at the database level; a composite FK does, declaratively. CHECK cannot
-- subquery another table and a generated column cannot reference another row, so this is the only
-- non-procedural mechanism Postgres offers for this shape.
ALTER TABLE decks ADD CONSTRAINT decks_id_owner_id_key UNIQUE (id, owner_id);

ALTER TABLE notes ADD CONSTRAINT notes_deck_id_owner_id_fkey
    FOREIGN KEY (deck_id, owner_id) REFERENCES decks (id, owner_id)
    ON UPDATE RESTRICT ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE notes DROP CONSTRAINT notes_deck_id_owner_id_fkey;
ALTER TABLE decks DROP CONSTRAINT decks_id_owner_id_key;
```

`ON DELETE RESTRICT` matches the single-column FK (deck delete must keep going through
`DeleteDeck`). `ON UPDATE RESTRICT` because `decks.owner_id` is never updated — ownership
transfer does not exist (schema.md, "User deletion is not a supported operation").

`migrations/00008_notes.sql`'s comment ("Nothing enforces that equality at the DB level -- by
design... No trigger.") is now stale, and migrations are immutable — **do not edit it**.
schema.md is corrected instead (§7 below), and 00015's comment carries the correction forward.

`RehomeNotesOffDeck` (deletion.sql) already re-syncs `owner_id` alongside `deck_id`; this
migration turns that from convention into a checked fact, and its existing test becomes a
regression test for the constraint.

### 0.2 Card ordinal conventions

- **Non-cloze note type:** one card per `templates` row. `cards.ordinal = templates.ordinal`,
  `cards.template_id` = that template.
- **Cloze note type:** exactly one template (ordinal 0). One card per **distinct cloze number**
  found across all field values. `cards.ordinal = clozeNumber - 1` (Anki's `cards.ord`
  convention: `{{c1::…}}` → ord 0), `cards.template_id` = the single template's id.
- `{{c0::…}}` and non-numeric markers are ignored (Anki numbers clozes from 1).
- A cloze note whose fields contain **no** valid cloze marker is rejected at create and edit with
  **400** ("A cloze note must contain at least one `{{c1::…}}` marker"). Rationale: a note with
  zero cards breaks the invariant `DeleteDeck` relies on ("no note is ever left with zero cards",
  schema.md), and there is no card for the user to study.
- New cards created during a note edit are filed in the **note's home deck** (`notes.deck_id`) —
  architecture.md §20: `notes.deck_id` is "the default for cards generated later". Existing
  cards' `deck_id` is never rewritten by a regeneration (a card deliberately filed in another
  deck stays there).

### 0.3 The card-regeneration trap: diff, never drop-and-reinsert

The trap is written down in **docs/schema.md** ("Editing a note's fields must not regenerate its
cards by dropping and recreating them") and re-tensed in **docs/plans/51-deletion-policy.md
§0.4**: since #51, `user_card_state.card_id` **cascades**, so a naive delete-all-then-reinsert
silently discards live scheduling state (CLAUDE.md §2.5), and the recreated card gets a new
UUID, stranding its `review_log` rows (which survive — `review_log.card_id` has no FK — but no
longer join to any card). This is the `sev: critical` "silently corrupts `user_card_state`"
bucket.

The algorithm, implemented once in `internal/db/cards.go` and called by **every** write path
(note create, note edit, note-type template append):

```
desired D = ordinal set from §0.2
existing E = SELECT id, ordinal, template_id FROM cards WHERE note_id = $1 ORDER BY ordinal FOR UPDATE
1. if len(D) == 0            -> return ErrNoCards        (never leave a note card-less)
2. keep    = D ∩ E           -> NOT TOUCHED. No UPDATE, no DELETE, no id change.
                                 user_card_state (PK user_id, card_id) and review_log.card_id
                                 both keep pointing at the same row -- this is the whole point.
3. create  = D \ E           -> INSERT (note_id, template_id, ordinal, deck_id = note's home deck)
4. destroy = E \ D           -> DELETE ... WHERE note_id = $1 AND ordinal = ANY($2)
```

Ordering: create before destroy, so the note is never transiently card-less. `FOR UPDATE` on
step 0's read is what serialises two concurrent edits of the same note; without it both can
compute the same `create` set and one hits `cards_note_id_ordinal_key`.

No `UPDATE cards SET template_id` case exists, because template **reorder/removal is refused
while notes exist** (§0.5), so a kept ordinal's template never changes identity.

Step 4 does cascade `user_card_state` for genuinely-removed ordinals (the user deleted
`{{c2::…}}`). That is the removal schema.md specifies ("only add/remove the cards that actually
changed") — see Open question 1.

### 0.4 Where the cloze scanner lives

`internal/render/cloze.go`, one exported pure function:

```go
// ClozeOrdinals returns the distinct cloze numbers appearing in fields, ascending.
// A cloze note generates one card per number (architecture.md §8).
func ClozeOrdinals(fields []string) []int32
```

Regex `\{\{c(\d+)::` — the hint form `{{c1::text::hint}}` shares the same prefix, so one
pattern covers both. Values parsed as int, `< 1` dropped, `> math.MaxInt32` dropped, deduped,
sorted ascending. Returned numbers are cloze numbers; the caller subtracts 1 for `cards.ordinal`
(§0.2).

Location rationale: architecture.md §8 assigns cloze to `internal/render/`, and #55 needs
exactly this ordinal set to pick which cloze is "active" per card. `internal/db` must **not**
import `internal/render` — the handler computes the desired ordinal set and passes it down as
data.

### 0.5 Note-type edits are append-only for structure while notes exist

Allowed at any time: `name`, `css`, `sort_field_idx`, field **rename**, template **rename**,
`qfmt`/`afmt`/`browser_qfmt`/`browser_afmt` edits, **appending** a field, **appending** a
template (non-cloze only).

Refused with **409** ("Remove or reorder fields only while the note type has no notes") when
the note type has ≥ 1 note: removing a field, reordering fields, removing a template, reordering
templates. Unrestricted when it has zero notes.

Rationale: `notes.fields` is a *positional* jsonb array indexed by `fields.ordinal` (schema.md),
so a removal/reorder is a data migration over every note of the type; a template removal
additionally deletes cards, discarding `user_card_state` (§2.5 bucket). Neither is required by
any Phase 1 step, and building the remap speculatively is against CLAUDE.md rule 2. **File a
follow-up issue: "Note-type field/template removal and reorder, with positional note remap."**

Consequences that must be implemented:
- Appending a field runs
  `UPDATE notes SET fields = fields || '""'::jsonb, modified_at = now() WHERE note_type_id = $1`
  once per appended field, keeping every note's array length equal to the field count.
- Appending a template (non-cloze) runs one `INSERT ... SELECT` creating one card per existing
  note, filed in each note's home deck. Cloze note types reject template append (a cloze note
  type has exactly one template).

### 0.6 Authorisation is in the query, and a hidden deck 404s

Every deck-scoped statement carries the join, per CLAUDE.md §9 and schema.md ("Access control at
the query layer"):

```sql
JOIN deck_access da ON da.deck_id = <deck> AND da.user_id = @user_id AND da.can_view AND da.<flag>
```

`can_view` is required **in addition to** the operation flag on every deck-scoped route, matching
`LockDeckForDelete`'s existing shape and routes.md's `/decks/{id}/delete` note. Zero rows →
`pgx.ErrNoRows` → handler answers **404**, never 403 — "absent OR invisible OR not permitted"
must be one outcome.

Note types are not deck-scoped: every note-type statement carries `AND owner_id = @user_id`
(routes.md "owns row").

Flag per route:

| Route | Query-layer predicate |
|---|---|
| `GET /decks` | `da.user_id = @user_id AND da.can_view` |
| `POST /decks` | none (session only); creator gets all six flags |
| `GET /decks/{id}` | `can_view` |
| `GET/POST /decks/{id}/edit` | `can_view AND can_edit_settings` |
| `POST /decks/{id}/delete` | `can_view AND can_delete` (existing `LockDeckForDelete`) |
| `GET/POST /note-types*` | `note_types.owner_id = @user_id` |
| `GET /decks/{deckId}/notes/new`, `POST /decks/{deckId}/notes` | `can_view AND can_edit_content` on `{deckId}`, **and** `note_types.owner_id = @user_id` |
| `GET/POST /notes/{id}/edit`, `POST /notes/{id}/delete` | `can_view AND can_edit_content` on the note's `deck_id` |
| `POST /notes/{id}/move` | `can_view AND can_edit_content` on **both** the source and the target deck |

### 0.7 Scope trims inside the routes.md tables, with reasons

- **`GET /decks/{id}` shows note count and card count; the *due* count is deferred to step 7.**
  Due counts need `StudyDayStart()`/`StudyDayEnd()` (schema.md, "The day boundary"), which do
  not exist and are specified as part of the reviewer's queue query. Until step 7 there are no
  `user_card_state` rows at all, so every due count would render as "all cards". routes.md's
  Deck-detail row is amended to say so.
- **`GET/POST /decks/{id}/edit` covers name + description only; `preset` is not editable.**
  `preset jsonb` is deck-options config whose shape no code reads yet (learning steps arrive
  with the reviewer). It keeps its `'{}'` default. routes.md amended.
- **Notes list on the deck page is `ORDER BY n.modified_at DESC LIMIT 200`, no pagination.**
  Pagination is a separate feature; note it in routes.md so it isn't mistaken for done.

### 0.8 `guid` and `checksum` for locally-authored notes

- `guid`: `base64.RawURLEncoding.EncodeToString(10 random bytes from crypto/rand)`. Anki treats
  `guid` as opaque text on import; no dependency, no collision risk at this scale. (Anki's own
  guids are base91 of a random 64-bit int — see Open question 4.)
- `checksum`: `int64` of the first 8 hex digits of `SHA-1(stripHTML(fields[0]))`, per
  apkg-format.md ("truncated SHA-1 of the first field"). `stripHTML` removes `<[^>]*>` and leaves
  entities as-is. The column is `NOT NULL` with no default deliberately, so a path that forgets
  it fails loudly. Nothing in Phase 1 *reads* it; export (#58) is the only consumer — see Open
  question 2.

### 0.9 No new Go dependency

Ids are `pgtype.UUID`; `uuid` PKs carry `DEFAULT uuidv7()` and every insert uses `RETURNING`, so
no `google/uuid` is needed. Path params parse with `var u pgtype.UUID; u.Scan(r.PathValue("id"))`.

Transactions: add to `internal/db`

```go
// Beginner is a DBTX that can also open a transaction: *pgxpool.Pool in production, pgx.Tx in
// tests (where Begin opens a savepoint), which is what lets handler tests run inside a
// rollback-only transaction the way internal/http's existing tests already do.
type Beginner interface {
    DBTX
    Begin(ctx context.Context) (pgx.Tx, error)
}
```

## 1. Files touched

**New**
- `migrations/00015_notes_owner_matches_deck.sql`
- `internal/db/cards.go` (hand-written, sibling of `deletion.go`)
- `internal/db/notes.go` (hand-written: `MoveNote`, `CreateNoteWithCards`, `UpdateNoteWithCards`)
- `internal/db/decks.go` (hand-written: `CreateDeckWithAccess`)
- `internal/db/notetypes.go` (hand-written: `CreateNoteType`, `UpdateNoteType`)
- `internal/db/errors.go` (`IsUniqueViolation`)
- `internal/db/tx.go` (`Beginner`)
- `internal/render/cloze.go`, `internal/render/cloze_test.go`
- `internal/http/decks.go`, `notetypes.go`, `notes.go`, `pathparam.go`
- `internal/http/decks_test.go`, `notetypes_test.go`, `notes_test.go`
- `internal/db/cards_test.go`, `internal/db/notes_test.go`
- `web/templates/`: `decks.html`, `deck_new.html`, `deck.html`, `deck_edit.html`, `notetypes.html`,
  `notetype_form.html`, `note_form.html`

**Edited**
- `internal/db/queries/{decks,deck_access,note_types,fields,templates,notes,cards}.sql` — append
- `internal/db/{querier,*.sql}.go` — regenerated, committed, never hand-edited
- `internal/http/http.go` — register the three route groups, pass the pool as `db.Beginner`
- `internal/http/templates.go` — add the seven new page names, drop `"home"`
- `internal/http/auth.go` — `GET /{$}` redirects an authed user to `/decks` (routes.md's Auth
  table already says step 5 does this)
- `web/templates/home.html` — **deleted** (orphaned by the redirect; CLAUDE.md rule 3)
- `docs/schema.md`, `docs/routes.md`, `docs/architecture.md`, `migrations/README.md`,
  `CHANGELOG.md`

## 2. SQL to add (`internal/db/queries/`, then `go generate ./internal/db`)

### 2.1 `decks.sql`

```sql
-- name: ListDecksForUser :many
SELECT d.*, (SELECT count(*) FROM cards c WHERE c.deck_id = d.id) AS card_count
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
ORDER BY d.name;

-- name: GetDeckForUser :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
WHERE d.id = sqlc.arg(deck_id);

-- name: GetDeckForSettingsEdit :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_settings
WHERE d.id = sqlc.arg(deck_id);

-- name: CountDeckContents :one
SELECT (SELECT count(*) FROM notes n WHERE n.deck_id = sqlc.arg(deck_id)) AS note_count,
       (SELECT count(*) FROM cards c WHERE c.deck_id = sqlc.arg(deck_id)) AS card_count;

-- name: CreateDeck :one
INSERT INTO decks (owner_id, name, description) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateDeck :execrows
UPDATE decks d
SET name = sqlc.arg(name), description = sqlc.arg(description), modified_at = now()
FROM deck_access da
WHERE d.id = sqlc.arg(deck_id) AND da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_settings;
```

### 2.2 `deck_access.sql`

```sql
-- A deck's creator gets all six flags (docs/schema.md). A personal deck is the trivial case of
-- this, not a separate code path.
-- name: GrantFullDeckAccess :exec
INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content,
                         can_edit_settings, can_manage_access, can_delete)
VALUES ($1, $2, true, true, true, true, true, true);
```

### 2.3 `note_types.sql`

```sql
-- name: ListNoteTypesForOwner :many
SELECT nt.*, (SELECT count(*) FROM notes n WHERE n.note_type_id = nt.id) AS note_count
FROM note_types nt WHERE nt.owner_id = sqlc.arg(owner_id) ORDER BY nt.name;

-- name: GetNoteTypeForOwner :one
SELECT * FROM note_types WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);

-- name: CreateNoteType :one
INSERT INTO note_types (owner_id, name, css, is_cloze, sort_field_idx)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: UpdateNoteTypeRow :execrows
UPDATE note_types SET name = sqlc.arg(name), css = sqlc.arg(css),
                      sort_field_idx = sqlc.arg(sort_field_idx)
WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);
-- is_cloze is immutable after creation: flipping it changes what every existing note's cards
-- mean. Not editable in the form, not settable here.

-- name: CountNotesOfNoteType :one
SELECT count(*) FROM notes WHERE note_type_id = $1;

-- name: DeleteNoteType :execrows
DELETE FROM note_types WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);
-- notes.note_type_id ON DELETE RESTRICT blocks this while any note exists (routes.md);
-- fields and templates cascade. The handler turns 23503 into 409.
```

### 2.4 `fields.sql` / `templates.sql`

```sql
-- name: ListFieldsForNoteType :many
SELECT * FROM fields WHERE note_type_id = $1 ORDER BY ordinal;

-- name: CreateField :one
INSERT INTO fields (note_type_id, ordinal, name) VALUES ($1, $2, $3) RETURNING *;

-- name: RenameField :execrows
UPDATE fields SET name = sqlc.arg(name) WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);

-- name: ListTemplatesForNoteType :many
SELECT * FROM templates WHERE note_type_id = $1 ORDER BY ordinal;

-- name: CreateTemplate :one
INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt) VALUES ($1,$2,$3,$4,$5) RETURNING *;

-- name: UpdateTemplate :execrows
UPDATE templates SET name = sqlc.arg(name), qfmt = sqlc.arg(qfmt), afmt = sqlc.arg(afmt)
WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);
```

### 2.5 `notes.sql`

```sql
-- name: ListNotesInDeck :many
SELECT n.id, n.fields ->> nt.sort_field_idx AS sort_text, n.tags, n.modified_at, nt.name AS note_type_name,
       (SELECT count(*) FROM cards c WHERE c.note_id = n.id) AS card_count
FROM notes n
JOIN note_types nt ON nt.id = n.note_type_id
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
WHERE n.deck_id = sqlc.arg(deck_id)
ORDER BY n.modified_at DESC
LIMIT 200;

-- Owner_id comes from the DECK, not the caller: notes.owner_id is denormalised from
-- decks.owner_id and, as of migration 00015, a composite FK rejects any other value.
-- name: CreateNote :one
INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, tags, checksum)
SELECT sqlc.arg(guid), d.owner_id, nt.id, d.id, sqlc.arg(fields), sqlc.arg(tags), sqlc.arg(checksum)
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
JOIN note_types nt ON nt.id = sqlc.arg(note_type_id) AND nt.owner_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
RETURNING *;

-- Locks the note for the duration of the transaction and authorises the caller in one step --
-- the same no-row-means-404 contract as LockDeckForDelete. The lock is what makes the card
-- ordinal diff in SyncNoteCards atomic against a concurrent edit of the same note.
-- name: LockNoteForContentEdit :one
SELECT n.*
FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE n.id = sqlc.arg(note_id)
FOR UPDATE OF n;

-- name: GetNoteForContentEdit :one
SELECT n.*
FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE n.id = sqlc.arg(note_id);

-- name: UpdateNoteContent :execrows
UPDATE notes SET fields = sqlc.arg(fields), tags = sqlc.arg(tags),
                 checksum = sqlc.arg(checksum), modified_at = now()
WHERE id = sqlc.arg(note_id);

-- Moving a note must move owner_id with it (docs/schema.md, "must not drift"); migration 00015
-- makes a drifted pair fail loudly instead of silently breaking the import key.
-- name: MoveNoteToDeck :execrows
UPDATE notes n
SET deck_id = d.id, owner_id = d.owner_id, modified_at = now()
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE n.id = sqlc.arg(note_id) AND d.id = sqlc.arg(target_deck_id);

-- Cards filed in the note's OLD home deck follow it; cards deliberately filed elsewhere stay
-- put (architecture.md §20: a card belongs to exactly one deck, and a note's cards need not
-- share one).
-- name: MoveNoteCardsFromDeck :execrows
UPDATE cards SET deck_id = sqlc.arg(target_deck_id)
WHERE note_id = sqlc.arg(note_id) AND deck_id = sqlc.arg(source_deck_id);

-- name: DeleteNote :execrows
DELETE FROM notes n
USING deck_access da
WHERE n.id = sqlc.arg(note_id) AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;
-- cards.note_id CASCADEs, and through cards, user_card_state. review_log rows persist:
-- review_log.card_id has no FK (#51).

-- name: ListNoteIDsOfNoteType :many
SELECT id FROM notes WHERE note_type_id = $1;
```

### 2.6 `cards.sql`

```sql
-- Cards are content addressing only -- no scheduling columns exist here to lose (CLAUDE.md §2.1).
-- These four statements are the whole of card regeneration; see internal/db/cards.go for the
-- diff that calls them and docs/schema.md's card-regeneration trap for why it is a diff.

-- name: ListCardsForNoteForUpdate :many
SELECT id, ordinal, template_id, deck_id FROM cards WHERE note_id = $1 ORDER BY ordinal FOR UPDATE;

-- name: CreateCardsForNote :execrows
INSERT INTO cards (note_id, template_id, ordinal, deck_id)
SELECT sqlc.arg(note_id), t.template_id, t.ordinal, sqlc.arg(deck_id)
FROM unnest(sqlc.arg(template_ids)::uuid[], sqlc.arg(ordinals)::int[]) AS t(template_id, ordinal);

-- name: DeleteCardsByOrdinals :execrows
DELETE FROM cards WHERE note_id = sqlc.arg(note_id) AND ordinal = ANY(sqlc.arg(ordinals)::int[]);

-- One card per existing note when a template is appended to a non-cloze note type (#54 §0.5).
-- Filed in each note's home deck: notes.deck_id is the default for cards generated later
-- (architecture.md §20).
-- name: CreateCardsForNewTemplate :execrows
INSERT INTO cards (note_id, template_id, ordinal, deck_id)
SELECT n.id, sqlc.arg(template_id), sqlc.arg(ordinal), n.deck_id
FROM notes n WHERE n.note_type_id = sqlc.arg(note_type_id)
ON CONFLICT (note_id, ordinal) DO NOTHING;

-- name: AppendNoteFieldSlot :execrows
UPDATE notes SET fields = fields || '""'::jsonb, modified_at = now() WHERE note_type_id = $1;
```

Regenerate: `go generate ./internal/db` (runs `sqlc generate`), commit the output unedited.

## 3. `internal/db/cards.go` — the regeneration helper

Mirrors `deletion.go`'s contract: **takes a `pgx.Tx`, never opens one**, so "must run in a
transaction" is a compile-time fact and the caller owns commit/rollback.

```go
// ErrNoCards is returned when a sync would leave a note with zero cards -- a cloze note whose
// last {{cN::}} marker was removed. Callers answer 400 and roll back: a card-less note breaks
// the post-condition DeleteDeck depends on (docs/schema.md).
var ErrNoCards = errors.New("db: note would be left with no cards")

// DesiredCard is one (ordinal, template) pair the note should have a card for.
type DesiredCard struct {
    Ordinal    int32
    TemplateID pgtype.UUID
}

// SyncNoteCards reconciles a note's cards to desired by DIFFING ordinals: cards whose ordinal
// survives are left completely untouched, so their id -- and therefore their user_card_state and
// review_log history -- survives the edit. Dropping and recreating a note's cards instead is the
// data-loss trap docs/schema.md names and #51 §0.4 made live.
func SyncNoteCards(ctx context.Context, tx pgx.Tx, noteID, homeDeckID pgtype.UUID, desired []DesiredCard) error
```

Body: `ListCardsForNoteForUpdate` → build `map[int32]struct{}` of existing and desired →
`CreateCardsForNote` for `desired \ existing` (skip when empty) → `DeleteCardsByOrdinals` for
`existing \ desired` (skip when empty) → return `ErrNoCards` first if `len(desired) == 0`.

`internal/db/notes.go` wraps it:

```go
func CreateNoteWithCards(ctx context.Context, tx pgx.Tx, arg CreateNoteParams, desired []DesiredCard) (Note, error)
func UpdateNoteWithCards(ctx context.Context, tx pgx.Tx, userID, noteID pgtype.UUID, fields []byte, tags []string, checksum int64, desired []DesiredCard) error   // LockNoteForContentEdit -> UpdateNoteContent -> SyncNoteCards
func MoveNote(ctx context.Context, tx pgx.Tx, userID, noteID, targetDeckID pgtype.UUID) error                                                                     // LockNoteForContentEdit (source auth) -> MoveNoteToDeck (target auth) -> MoveNoteCardsFromDeck
```

`MoveNote` returns `pgx.ErrNoRows` when either side's authorisation misses — both decks need
`can_view` + `can_edit_content`, and the caller cannot learn which one failed.

`internal/db/decks.go`: `CreateDeckWithAccess(ctx, tx, ownerID, name, description) (Deck, error)`
= `CreateDeck` + `GrantFullDeckAccess`, one transaction.

`internal/db/errors.go`:

```go
// IsUniqueViolation reports whether err is a Postgres 23505 on the named constraint, so a
// duplicate deck or note-type name is an ordinary 409 rather than a 500 (docs/schema.md).
func IsUniqueViolation(err error, constraint string) bool
func IsForeignKeyViolation(err error, constraint string) bool   // note-type delete blocked by notes.note_type_id
```

## 4. Handlers — `internal/http/`

Conventions taken from `settings.go`:
`register*Routes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template)`,
each route a closure wrapped in `auth.RequireUser`, user from `auth.UserFromContext`,
`render(w, pages["x"], status, map[string]any{...})`, `http.Redirect(..., http.StatusSeeOther)`
after a successful POST. CSRF and session population are already central in
`auth.Service.Middleware` — no per-handler CSRF code.

Shared helper, `internal/http/pathparam.go`:

```go
func pathUUID(r *http.Request, name string) (pgtype.UUID, bool)   // u.Scan(r.PathValue(name))
func notFound(w http.ResponseWriter)                              // http.Error(w, "not found", 404)
```

Every `pgx.ErrNoRows` (and every `execrows == 0`) from a deck-scoped query → `notFound(w)`.

### 4.1 `decks.go` — `registerDeckRoutes`

| Route | Body |
|---|---|
| `GET /decks` | `ListDecksForUser` → `decks.html` |
| `GET /decks/new` | `deck_new.html` |
| `POST /decks` | validate name (trimmed, 1–200 chars) → tx → `db.CreateDeckWithAccess` → commit → 303 `/decks/{id}`. `IsUniqueViolation(err, "decks_owner_id_name_key")` → 409 re-render with "You already have a deck with that name." |
| `GET /decks/{id}` | `GetDeckForUser` + `CountDeckContents` + `ListNotesInDeck` → `deck.html` |
| `GET /decks/{id}/edit` | `GetDeckForSettingsEdit` → `deck_edit.html` |
| `POST /decks/{id}/edit` | `UpdateDeck`; rows==0 → 404; 23505 → 409; else 303 `/decks/{id}` |
| `POST /decks/{id}/delete` | tx → `db.DeleteDeck(ctx, tx, deckID, userID)` → `pgx.ErrNoRows` → 404 → commit → 303 `/decks` |

### 4.2 `notetypes.go` — `registerNoteTypeRoutes`

| Route | Body |
|---|---|
| `GET /note-types` | `ListNoteTypesForOwner` → `notetypes.html` |
| `GET /note-types/new` | `notetype_form.html` with one blank field row and one blank template row |
| `POST /note-types` | validate (§4.4) → tx → `CreateNoteType` + N × `CreateField` (ordinal = index) + M × `CreateTemplate` → commit → 303 `/note-types`. 23505 on `note_types_owner_id_name_key` → 409 |
| `GET /note-types/{id}/edit` | `GetNoteTypeForOwner` + `ListFieldsForNoteType` + `ListTemplatesForNoteType` → `notetype_form.html` |
| `POST /note-types/{id}/edit` | ordered transaction, §4.3 |
| `POST /note-types/{id}/delete` | `DeleteNoteType`; rows==0 → 404; `IsForeignKeyViolation(err, "notes_note_type_id_fkey")` → 409 "Delete or re-type its notes first"; else 303 `/note-types` |

### 4.3 Note-type edit transaction, in this order

1. `GetNoteTypeForOwner` (404 if missing) and `CountNotesOfNoteType`.
2. Compare submitted `field_id[]` / `template_id[]` against the stored rows. Any **missing** id
   or any id out of stored order → if `note_count > 0`, roll back with **409** (§0.5); if
   `note_count == 0`, apply the removal/reorder freely (delete removed rows, re-`CreateField`/
   `CreateTemplate` in submitted order).
3. `UpdateNoteTypeRow` (name, css, sort_field_idx).
4. `RenameField` / `UpdateTemplate` for every existing row.
5. For each **appended** field: `CreateField(ordinal = len(existing)+i)` then
   `AppendNoteFieldSlot(noteTypeID)`.
6. For each **appended** template (rejected outright when `is_cloze`):
   `CreateTemplate(ordinal = len(existing)+i)` then
   `CreateCardsForNewTemplate(noteTypeID, templateID, ordinal)`.
7. Commit → 303 `/note-types`.

### 4.4 `notes.go` — `registerNoteRoutes`

| Route | Body |
|---|---|
| `GET /decks/{deckId}/notes/new` | `GetDeckForUser` (404 gate; the write query re-checks `can_edit_content`) + `ListNoteTypesForOwner`. With `?note_type_id=` present: `GetNoteTypeForOwner` + `ListFieldsForNoteType` → `note_form.html` with one input per field. Without it: the same page rendered as a plain `<form method="get">` note-type picker — no JS, no htmx required. |
| `POST /decks/{deckId}/notes` | parse `note_type_id`, `field[]` (ordinal order), `tags` (space-separated, Anki's convention) → compute desired cards (§4.5) → tx → `db.CreateNoteWithCards` → commit → 303 `/decks/{deckId}` |
| `GET /notes/{id}/edit` | `GetNoteForContentEdit` (same joins as the write path, no `FOR UPDATE`) + fields/templates → `note_form.html` |
| `POST /notes/{id}/edit` | as create, then `db.UpdateNoteWithCards`; `ErrNoCards` → 400; 303 `/decks/{deckId}` |
| `POST /notes/{id}/delete` | `DeleteNote`; rows==0 → 404; 303 back to the deck (deck id read before the delete) |
| `POST /notes/{id}/move` | `target_deck_id` form value → tx → `db.MoveNote` → commit → 303 `/decks/{target}` |

### 4.5 Desired-card computation (the generation logic, shared by create and edit)

```go
// in internal/http/notes.go -- the one place that decides what cards a note has.
func desiredCards(nt db.NoteType, tmpls []db.Template, fieldValues []string) ([]db.DesiredCard, error) {
    if !nt.IsCloze {
        // One card per template (architecture.md §8). Ordinal is the template's own ordinal.
        ...one DesiredCard per tmpls[i]{Ordinal: t.Ordinal, TemplateID: t.ID}
    }
    // Cloze: N cards by distinct cloze ordinal, all against the single template.
    ords := render.ClozeOrdinals(fieldValues)   // cloze NUMBERS, ascending
    if len(ords) == 0 { return nil, errNoClozeMarkers }   // -> 400 (§0.2)
    ...one DesiredCard per n: {Ordinal: n - 1, TemplateID: tmpls[0].ID}   // Anki's ord convention
}
```

Validation applied before any DB write: field count must equal the note type's field count;
every field value ≤ 64 KiB; first field non-empty after trimming (it is the sort field's usual
home and what `checksum` is computed over); tag tokens are whitespace-split, deduped, order
preserved.

### 4.6 Wiring

`http.go`:

```go
mux.HandleFunc("GET /healthz", healthHandler(pool))
registerAuthRoutes(mux, a, pages)
registerSettingsRoutes(mux, a, pages)
registerDeckRoutes(mux, pool, pages)
registerNoteTypeRoutes(mux, pool, pages)
registerNoteRoutes(mux, pool, pages)
```

`templates.go`: page list becomes `{"login", "signup", "settings", "decks", "deck_new", "deck",
"deck_edit", "notetypes", "notetype_form", "note_form"}` — `"home"` removed.

## 5. Templates — `web/templates/`

Each is `{{define "content"}}…{{end}}` over the existing `layout.html`, Pico-classless markup
with plain `<form method="post">`, matching `settings.html` exactly (inline
`style="color: red;"` error paragraph, `<label>`+`<input>` pairs, no CSS classes, no JS). No
htmx attributes are needed by any route in this issue — every interaction is a form POST + 303.

- `decks.html` — `<table>` of name / card count / links; "New deck" link.
- `deck_new.html`, `deck_edit.html` — name + description; `deck_edit.html` also carries the
  delete form (`POST /decks/{id}/delete`) and a link to `/decks/{id}/access` is **not** added
  (Phase 2).
- `deck.html` — deck name, note/card counts, "Add note" link, notes table (sort text, note type,
  card count, edit/delete/move forms).
- `notetypes.html` — name, cloze marker, note count, edit/delete.
- `notetype_form.html` — name, css `<textarea>`, `is_cloze` checkbox (disabled on edit — §2.3's
  immutability note), `sort_field_idx` `<select>`, repeated field rows (`field_id[]` hidden +
  `field_name[]`), repeated template rows (`template_id[]` hidden + `template_name[]`, `qfmt[]`,
  `afmt[]`), plus one blank append row of each.
- `note_form.html` — note-type `<select>` (create only) or fixed heading (edit), one
  `<input name="field[]">` per field labelled with the field name, `tags` input, and on edit a
  `target_deck_id` `<select>` with its own move form.

## 6. Tests

- `internal/render/cloze_test.go` (no DB) — table-driven: single marker, hint form, duplicate
  numbers across fields, out-of-order numbers, `{{c0::}}`, `{{c01::}}`, unterminated `{{c1:`,
  plain text containing `c1::`, empty input.
- `internal/db/cards_test.go` (DB-backed, tx-rollback, same `testPool`/`beginTx` helpers as
  `deletion_test.go`) — **the trap's regression test**: create a 3-cloze note, insert a
  `user_card_state` row for ordinal 1, edit to remove cloze 3 and add cloze 4, assert (a)
  ordinal-1 card's **id is unchanged**, (b) its `user_card_state` row survives with identical
  values, (c) ordinal 2's card is gone, (d) ordinal 3's card exists. Plus: `ErrNoCards` on an
  empty desired set; two concurrent syncs serialise rather than colliding on
  `cards_note_id_ordinal_key`.
- `internal/db/notes_test.go` (DB-backed) — composite-FK regression: inserting a note with
  `owner_id` ≠ its deck's owner raises `23503`; `MoveNote` across owners updates both columns;
  a bare `UPDATE notes SET deck_id` to another owner's deck fails.
- `internal/http/{decks,notetypes,notes}_test.go` (DB-backed, through the handler like
  `settings_test.go`) — the CLAUDE.md §10.5 access-control table: for each route ×
  (no session / session but no `deck_access` row / row missing the required flag / row with the
  flag), assert 303/404/404/2xx respectively. **A deck that exists but is invisible must return
  404, never 403** — one row per route asserting exactly that.

## 7. Doc updates (same PR)

- **docs/schema.md** — Identifiers: replace "Nothing enforces the equality at the database level
  today — it holds by convention in the query layer, which is weaker than it should be for an
  import key" with the composite-FK description and a pointer to migration 00015. Content
  section: re-tense the card-regeneration trap paragraph to "implemented in
  `internal/db/cards.go` (#54); the diff is what keeps `user_card_state` and `review_log`
  attached to surviving cards." Deletion-policy table: add the `notes.(deck_id, owner_id) →
  decks` row.
- **docs/routes.md** — mark Decks / Note types / Notes as built (#54) the way Auth and Settings
  are marked; amend the deck-detail row (due counts deferred to step 7), the deck-edit row
  (`preset` not editable yet), the note-type edit row (removal/reorder refused while notes
  exist, follow-up issue linked), and the Auth table's `/` row (now redirects to `/decks`).
- **docs/architecture.md** — §1: a "Build order step 5 has landed (#54)" paragraph naming the
  composite FK and the diff-based regeneration. §20, "Deliberate scope, not disagreement": add
  the empty-cards row — *Anki: cards whose cloze ordinal disappears stay as empty cards until
  Tools → Empty Cards. Enshu: removed on the edit that removed the ordinal (schema.md's diff
  rule).*
- **migrations/README.md** — one line: 00015 adds the `notes.owner_id = decks.owner_id`
  composite FK (#54).
- **CHANGELOG.md** — `### Added` deck/note-type/note CRUD with card generation; `### Changed`
  `notes.owner_id` now DB-enforced.
- **Follow-up issues to file:** (1) note-type field/template removal and reorder with positional
  note remap; (2) notes-list pagination on the deck page.

## 8. Implementation order

1. Migration 00015 + `go generate ./internal/db` (verify it applies to a fresh DB and that
   `sqlc` still parses `migrations/`).
2. Query files + regenerate; commit generated output unedited.
3. `internal/render/cloze.go` + test (pure, fastest feedback).
4. `internal/db/{tx,errors,cards,notes,decks,notetypes}.go` + DB-backed tests.
5. Templates + `templates.go` + `http.go` wiring + `home.html` removal.
6. `internal/http/{decks,notetypes,notes}.go` + access-control tests.
7. Docs, CHANGELOG, follow-up issues.

## 9. Anticipated traps

- **`sqlc` and `unnest(...)` with two arrays** — `CreateCardsForNote` needs explicit `::uuid[]` /
  `::int[]` casts on `sqlc.arg`, or sqlc infers `any`.
- **`FOR UPDATE` with a join** — `LockNoteForContentEdit` must say `FOR UPDATE OF n`, not bare
  `FOR UPDATE`; a bare one tries to lock the `deck_access` row too. Same shape as the existing
  `LockDeckForDelete`.
- **`UPDATE … FROM deck_access` returning 0 rows** is the *only* signal that authorisation
  failed — it is indistinguishable from "deck absent", which is the requirement, not a defect.
  Do not add a second query to tell them apart.
- **`notes.fields` is `[]byte` in Go** (`jsonb`); marshal `[]string` with `encoding/json` and
  never build the JSON by string concatenation.
- **`checksum` has no default on purpose** — a create path that forgets it fails loudly; do not
  "fix" that by adding a default.
- **`is_cloze` must not be editable after creation** — flipping it silently reinterprets every
  existing card's ordinal.
- **Handler tests need `Origin`** on every POST (the middleware rejects a missing one with 403);
  `doRequest`'s existing signature already carries it.

---

## Open questions

1. **Cloze-card deletion vs. Anki's empty-card retention.** The plan deletes cards whose cloze
   ordinal disappeared (schema.md: "only add/remove the cards that actually changed"), which
   cascades that card's `user_card_state` away permanently — a mistyped edit that drops
   `{{c2::` loses that card's scheduling state, and its `review_log` rows survive but are
   stranded from any card. Anki instead keeps such cards as "empty cards" until the user
   explicitly runs Tools → Empty Cards. schema.md sanctions the delete, so the plan implements
   it and records the divergence in architecture.md §20 — but confirm this is wanted before it
   becomes reachable, because it is the `sev: critical` "silently discards `user_card_state`"
   bucket doing it *by design*.
2. **`notes.checksum` parity with Anki's `csum`.** apkg-format.md says only "truncated SHA-1 of
   the first field" and does not specify the stripping. The plan uses SHA-1 over the first field
   with `<[^>]*>` removed, first 8 hex digits → int64. Nothing in Phase 1 reads it; export (#58)
   is the only consumer, and exact parity is unverifiable without a fixture. Confirm this stays
   a #58 concern.
3. **Note-type read/use under sharing** (routes.md open question 1, unchanged by this issue).
   `CreateNote` requires `note_types.owner_id = @user_id`, so a Phase 2 co-author holding
   `can_edit_content` on someone else's deck cannot add notes using the deck owner's note type.
   Moot in Phase 1 (one owner), but this plan hard-codes the restriction into a query — confirm
   that's the right place to leave it rather than pre-emptively widening it.
4. **`guid` format for locally-authored notes.** The plan uses `base64url(10 random bytes)`.
   Anki's own guids are base91 of a random 64-bit int; Anki treats `guid` as opaque text on
   import, so a round-trip should be fine, but that is unverified against a real Anki build and
   only becomes testable when the `.apkg` fixtures land (#58, CLAUDE.md §10.3).

## Resolved decisions

1. **Cloze card deletion vs. empty-card retention → delete immediately, as planned.** When a
   cloze ordinal disappears from a note's fields on edit, `SyncNoteCards` (§0.3/§3) deletes that
   card immediately, cascading its `user_card_state`. Matches docs/schema.md's stated rule
   ("only add/remove the cards that actually changed"). No Anki-style "empty cards" concept is
   built — that needs its own cleanup UI/route, out of scope for #54. The divergence is recorded
   in architecture.md §20 per §7 of this plan.
2. **`notes.checksum` stripping → simple `<[^>]*>` regex strip before SHA-1, as planned.**
   Nothing in Phase 1 reads `checksum`; exact Anki `csum` parity is verified against real
   fixtures when `.apkg` export (#58) lands, not here.
3. **`note_types.owner_id`-scoped note creation → keep hard-coded to single-owner, as planned.**
   `CreateNote` requires `note_types.owner_id = @user_id` with no seam added for Phase 2 sharing
   — routes.md's existing open question ("note-type read access under sharing") stays open and
   unaddressed by this issue, consistent with Phase 1 being single-owner-per-deck.
4. **`guid` format → `base64url(10 random bytes from crypto/rand)`, as planned.** No dependency
   on matching Anki's base91 encoding; `guid` is opaque text to Anki's importer. Compatibility
   is unverified until `.apkg` fixtures land in #58, tracked there rather than blocking #54.
