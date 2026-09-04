# 192 — Note-type authority derives from decks, not `owner_id`

Decision record for [#192](https://github.com/Jolls/deckshare/issues/192). Implementation-ready;
resolves the note-type authorisation open question in [routes.md](../routes.md) at the same time.

## The rule

`note_types.owner_id` is a **namespace key only** — it exists so `UNIQUE (owner_id, name)` makes
re-import idempotent (§2.2). It is never consulted for authorisation.

Authority derives from the decks whose notes use the note type:

| Operation | Rule |
|---|---|
| **Read** (list, view, render, pick) | `can_view` on **any** deck holding a note that uses it, **or** you own it |
| **Write** (edit fields/templates/CSS/name) | `can_view + can_edit_content` on **every** deck holding a note that uses it; if no note uses it, the owner |
| **Create** | Anyone. `owner_id` = caller. Unchanged. |
| **Delete** | Owner. Unchanged — see below. |

**Why "any" for read, "every" for write:** removing a field permanently discards that field's
content from every note using the note type, and removing a template hard-deletes its cards. You
may only do that if you already hold the right to destroy all of it deck-by-deck.

**Why delete is unchanged:** `notes.note_type_id ON DELETE RESTRICT` already blocks deletion while
any note exists. A note type with no notes has no decks, so the write rule falls through to the
owner — identical outcome. Leaving `DeleteNoteType` owner-scoped also keeps the existing 23503 →
409 error shape, which is a better message than a 404.

## The two predicates

Bind these once and reuse; both are driven by `notes_note_type_id_idx` and `deck_access`'s
`(deck_id, user_id)` primary key.

```sql
-- READABLE(nt, user)
nt.owner_id = @user_id
OR EXISTS (
  SELECT 1 FROM notes n
  JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = @user_id AND da.can_view
  WHERE n.note_type_id = nt.id)

-- WRITABLE(nt, user) -- no deck using it blocks the caller
NOT EXISTS (
  SELECT 1 FROM (SELECT DISTINCT n.deck_id FROM notes n WHERE n.note_type_id = nt.id) d
  WHERE NOT EXISTS (
    SELECT 1 FROM deck_access da
    WHERE da.deck_id = d.deck_id AND da.user_id = @user_id
      AND da.can_view AND da.can_edit_content))
AND (EXISTS (SELECT 1 FROM notes n WHERE n.note_type_id = nt.id) OR nt.owner_id = @user_id)
```

The trailing `OR nt.owner_id` clause is load-bearing: `NOT EXISTS` over an empty deck set is
vacuously true, so without it an unused note type would be writable by everyone.

`can_view AND can_edit_content` together matches the existing convention in `CreateNote` and
`LockNoteForContentEdit`.

## Query changes — `internal/db/queries/note_types.sql`

| Current | Becomes |
|---|---|
| `ListNoteTypesForOwner` | `ListNoteTypesForUser(user_id)` — READABLE set, plus `note_count`, `deck_count`, and a computed `can_edit bool` (WRITABLE). One query; the handler splits the rows into sections. |
| `GetNoteTypeForOwner` | `GetNoteTypeForRead(id, user_id)` — READABLE. |
| `LockNoteTypeForOwner` | `LockNoteTypeForEdit(id, user_id)` — WRITABLE + `FOR UPDATE`. (`FOR UPDATE` is fine here: the `DISTINCT` sits inside a subquery, not the outer select.) |
| `UpdateNoteTypeRow` | Swap `AND owner_id = @owner_id` for WRITABLE. |
| `ListFieldCompatibleNoteTypesForOwner` | `...ForUser` — swap the `owner_id` filter for READABLE; keep the `is_cloze` + field-name-array match unchanged. |
| `DeleteNoteType` | Unchanged. |
| `CreateNoteType`, `GetNoteType`, `CountNotesOfNoteType` | Unchanged. |

**New queries:**

- `ListDecksUsingNoteType(note_type_id, user_id)` → `(deck_id, name, visible bool)` for the
  confirmation page and denial messages. `visible` = caller holds `can_view`; the handler must
  **not** render `name` when `visible` is false (see UI, denial messages).
- `ListNoteTypesForNoteForm(user_id, deck_id)` → READABLE set with an `in_this_deck bool`
  (a note in `deck_id` already uses it), ordered `in_this_deck DESC, name`.

`NoteTypeImpactSummary` stays as-is for `other_user_count`; `deck_count` moves to the new
`ListDecksUsingNoteType`.

Run `go generate` and commit the `sqlc` output.

## Handler changes

**`internal/http/notetypes.go`**
- `GET /note-types` — `ListNoteTypesForUser`; pass `can_edit`/`deck_count` through to the template.
- `GET /note-types/{id}/edit` (line ~100) — `GetNoteTypeForRead`. If `!can_edit`, render the
  read-only view instead of the form.
- `POST /note-types/{id}/edit` (line ~156) — `LockNoteTypeForEdit`; no row → 404.
- Structural-change confirmation page — add `ListDecksUsingNoteType`.

**`internal/http/notes.go`**
- Lines 39, 54, 85 — `ListNoteTypesForOwner` / `GetNoteTypeForOwner` →
  `ListNoteTypesForNoteForm` / `GetNoteTypeForRead`.
- Line 252 — `ListFieldCompatibleNoteTypesForUser`.
- Lines 162, 211 — already unscoped `GetNoteType`; leave alone.

**`internal/http/note_preview.go`** (lines 139-151) — the current/other split collapses. Both
branches become `GetNoteTypeForRead`. Delete the `currentNoteTypeID` special case and the comment
above it.

**`internal/http/aiimport.go`** (lines 35, 217) — same swap as `notes.go`; the AI import form
targets a deck, so it uses `ListNoteTypesForNoteForm`.

## UI changes

**`web/templates/notetypes.html`** — add a **"Used in"** column (deck count). Split the table into
two sections driven by `can_edit`, with no ownership language anywhere:

```
Note types you can edit
  Name  Cloze  Notes  Used in    Edit  Delete
Note types in decks you share
  Name  Cloze  Notes  Used in    View
```

Each read-only row carries a one-line reason. **Denial messages must not leak deck names the
caller cannot see:**
- blocking deck visible → *"Also used in **Spanish 201**, which you can't edit content in."*
- not visible → *"Also used in 1 deck you don't have access to."*
- mixed → name the visible ones, count the rest.

**`web/templates/notetype_form.html`** — needs a read-only mode for `can_edit = false`
(fields/templates/CSS shown, no submit).

**Confirmation page** — now lists affected decks by name rather than counting them. Safe by
construction: reaching this page requires WRITABLE, which implies `can_view` on every deck using
the note type. Keep `other_user_count` — other people may study those decks.

> Removing the field "Example" will permanently delete its content from 412 notes across
> **Bio 101**, **Bio 102**. 3 other people study these decks.

**`web/templates/note_form.html`** — the picker gains two optgroups from `in_this_deck`:

```
── Used in this deck ──
── Yours ──
```

This fixes a live bug: a collaborator with `can_edit_content` on someone else's deck currently
cannot select the note types that deck's existing notes already use.

**`web/templates/no_note_types.html`** — unchanged.

## Out of scope — separate issue

Split into [#203](https://github.com/Jolls/deckshare/issues/203).

**The frozen case.** A note type can span two decks with no common editor (reachable via
`MoveNote`, which requires `can_edit_content` on source and target at move time). WRITABLE then
denies everyone, including the owner. This is fail-closed and acceptable for now.

The escape is a **"Make a copy for this deck"** action: clone the note type into the caller's scope
(name suffixed on collision) and repoint that deck's notes at the clone. Field/template-identical
at clone time, so cards map 1:1 by ordinal and nothing regenerates; `guid` is untouched, so §2.2
re-import idempotence holds.

Offer it **only** on a WRITABLE denial caused by the multi-deck split — never as a general option.
A freely available fork breaks distribution: a teacher's later template fix stops reaching
students who forked, and copies proliferate across export → import round trips.

## Docs

- **[routes.md](../routes.md)** — replace the `owns row` entries in the note-types table with the
  new rule; delete the open-question block below it (the note-type read-authorisation question is
  answered here).
- **[architecture.md §20](../architecture.md#20-deviations-from-anki)** — add a deviation row.
  Traces to the §2.1 content/progress seam: Anki's note types are collection-global because there
  is one user; DeckShare makes their authority follow the content they render.
- **`CHANGELOG.md`** — one entry under `### Changed`, linking #192.

## Tests

Table-driven, added to the §10.5 access-control set:

1. **The classroom case (the point of the rule).** Note type backs notes in decks A and B. Caller
   holds `can_edit_content` on A only → edit denied, and the denial does not name B.
2. Caller holds `can_edit_content` on both A and B → edit allowed.
3. Note type with zero notes → owner may edit; a non-owner may not.
4. Read: `can_view` on A alone is enough to read a note type also used in B.
5. Read denied with no `deck_access` row on any deck using it and no ownership.
6. Owner with **no** `deck_access` on a deck using their note type → edit denied (the #192
   revoked-student case).
7. `ListNoteTypesForNoteForm` returns the deck's in-use note types to a collaborator who owns none
   of them.

All are DB-backed: scope every assertion to rows the test created (§16) — no table-wide `count(*)`,
no unscoped `LIMIT 1`.

## Not changing

Schema, migrations, importer, exporter, the render/review path (`reviews.sql`, `fields.sql`,
`templates.sql`, `export.sql` are already deck-authorised and owner-blind), `decks.owner_id`
policy. No ownership-transfer mechanism.
