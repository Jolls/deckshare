# Plan: edit a note's note type (#138)

Issue: "Need to add ability to edit card note type." Motivating example: a note using
"Basic (and reversed card)" (2 templates: forward, reverse) should become "Basic" (1 template),
dropping the reverse card while keeping the forward card's scheduling state intact.

## Scope for v1

**In scope:** changing a note's `note_type_id` when the old and new note types are
*field-compatible* — same field **count, order, and names** — and have the same `is_cloze` flag.
This covers the issue's own example: Anki's "Basic (and reversed card)" and "Basic" both have
fields `["Front", "Back"]` in that order, and both are non-cloze.

**Out of scope, explicitly:** changing note type across a different field set (different count,
order, or names) — the N:M field-remapping UI Anki's own dialog has. No code in this plan
attempts partial remapping; the compatibility check rejects it outright with a 400. Filing a
follow-up issue for cross-field-set remapping is left to the human, not done here.

**No migration needed.** No schema/column change — this is new queries plus application logic
over the existing `notes`/`cards`/`note_types`/`fields`/`templates` tables.

## What the investigation found (read before implementing)

- **`internal/db/cards.go`'s `SyncNoteCards`** (called from both `CreateNoteWithCards` and
  `UpdateNoteWithCards` in `internal/db/notes.go`) is already the "diff old/new cards by ordinal,
  touch only what changed" mechanism docs/schema.md calls out as the fix for the
  drop-and-recreate data-loss trap (#51 §0.4). It reads existing cards with
  `ListCardsForNoteForUpdate` (`FOR UPDATE`, so it's race-safe against concurrent edits of the
  same note), diffs against a `[]DesiredCard{Ordinal, TemplateID}` slice, creates missing
  ordinals before deleting extra ones (note is never transiently card-less), and deletes extra
  ordinals via `DeleteCardsByOrdinals`. **This is the mechanism to extend, not replace.**
- **Card deletion vs. `review_log` is not a genuine open question — it's already decided and
  already exercised.** `cards.template_id → templates` is `RESTRICT` but `review_log.card_id` has
  **no FK at all** (docs/schema.md, "Deletion policy" — settled in #51). `user_card_state.card_id
  → cards` is `CASCADE`. So hard-deleting a `cards` row already: (a) cascades away that card's
  `user_card_state` (its scheduling progress), and (b) leaves any `review_log` rows referencing
  it in place, orphaned but intact (`card_id` stays a valid grouping key forever since UUIDv7 ids
  are never reused). This is exactly what `SyncNoteCards` already does *today* whenever a cloze
  note's field edit removes a `{{cN::}}` marker — `TestSyncNoteCards_KeepsSurvivingOrdinalsUntouched`
  in `internal/db/cards_test.go` exercises the create/keep/remove diff, just not the
  review_log-survives-a-removal case specifically. **Decision: follow this existing, already-live
  convention** — hard-delete the card, let the cascade and the missing FK do their jobs, no new
  soft-delete/archive concept. Options (a)/(b)/(c) from the issue's own framing collapse to "(a),
  already implemented, add a test for the untested half."
- **A new card needs no explicit fan-out to other users' `user_card_state`.** The study-queue
  queries (`internal/db/reviews.sql.go`, e.g. `listDueCardsForStudy` line 298) all
  `LEFT JOIN user_card_state ucs ON ucs.user_id = $caller AND ucs.card_id = c.id` — a missing row
  *is* the "New" state. So a newly-created card is automatically visible as New to every user
  with `deck_access` on the deck the moment the row exists; no insert loop over deck members is
  needed anywhere in this codebase (`CreateCardsForNewTemplate`, used by
  `internal/db/notetypes.go`'s `UpdateNoteType` when a template is appended, relies on the same
  fact and does no such fan-out either).
- **A surviving card's `template_id` must be repointed.** `SyncNoteCards` today never touches
  `template_id` on a card whose ordinal survives — correct for a same-note-type field edit (the
  template at that ordinal never changes), wrong for a note-type change (ordinal 0 of the *old*
  note type and ordinal 0 of the *new* note type are different `templates` rows). The study
  batch query joins `templates t ON t.id = c.template_id` for `qfmt`/`afmt` but
  `note_types nt ON nt.id = n.note_type_id` (via the note) for `is_cloze`/note-type name — so if
  `cards.template_id` is left stale after a note-type change, a card renders the *old* template's
  question/answer format while everything else reports the *new* note type. This must be fixed
  as part of the sync, not left as a latent bug.
- **Who's allowed to do this today, by precedent:** `internal/db/queries/notes.sql`'s `CreateNote`
  joins `note_types nt ON nt.id = sqlc.arg(note_type_id) AND nt.owner_id = sqlc.arg(user_id)` —
  i.e. note creation already requires the *note type* to be owned by the **acting user**, not by
  the deck or the note's owner. `GetNoteForContentEdit`/`LockNoteForContentEdit` authorize the
  edit itself on `can_view AND can_edit_content` (deck-level, so any collaborator with that flag
  on a shared deck qualifies, not just the deck owner). See "Open question" below — this plan
  follows the `CreateNote` precedent for the *target* note type ownership check, but flags that
  the combination has more destructive blast radius here than at note creation.

## Field-compatibility rule (decided, not left open)

Two note types are compatible for a v1 note-type change iff:
1. `is_cloze` is equal on both, **and**
2. their `fields`, ordered by `ordinal`, have the same **names** in the same order (which implies
   the same count).

Rationale: `notes.fields` is a positional `jsonb` array (docs/schema.md). Count-only equality
would silently transpose field meaning if two note types share a count but not an order/naming
(e.g. a note type with `["Back", "Front"]`) — the same field VALUES would render under different
labels. Exact name-for-name-by-position equality is the simplest rule that can't do that, and the
issue's own example already satisfies it (`Basic (and reversed card)` and `Basic` both have
`["Front", "Back"]`). Cross-field-set remapping (different names/count/order) is out of scope
(see Scope, above) — reject it with a 400, don't attempt a partial mapping.

## Concrete changes

### 1. `internal/db/queries/note_types.sql` — new query

Add, after the existing queries:

```sql
-- Note types owned by owner_id that a note currently on current_note_type_id could switch to
-- without cross-field-set remapping (#138 v1): same is_cloze flag, and the same field names in
-- the same ordinal order. Deliberately includes current_note_type_id itself (harmless no-op
-- selection) so the caller/template doesn't need a special case to pre-select the current value.
-- name: ListFieldCompatibleNoteTypesForOwner :many
SELECT nt.* FROM note_types nt
WHERE nt.owner_id = sqlc.arg(owner_id)
  AND nt.is_cloze = sqlc.arg(is_cloze)
  AND (SELECT array_agg(f.name ORDER BY f.ordinal) FROM fields f WHERE f.note_type_id = nt.id)
      = (SELECT array_agg(f2.name ORDER BY f2.ordinal) FROM fields f2
         WHERE f2.note_type_id = sqlc.arg(current_note_type_id))
ORDER BY nt.name;
```

Used only to populate the GET edit page's note-type dropdown. The POST handler does its own
Go-side compatibility check (below) as the authoritative one — never trust the submitted
`note_type_id` just because it *could* have come from this list.

### 2. `internal/db/queries/cards.sql` — two new queries

```sql
-- name: UpdateCardTemplate :execrows
UPDATE cards SET template_id = sqlc.arg(template_id) WHERE id = sqlc.arg(id);

-- Non-locking read for a preview (no FOR UPDATE): used only to compute which ordinals a note-type
-- change would add/remove, before the user has confirmed anything. The actual mutation re-reads
-- via ListCardsForNoteForUpdate inside SyncNoteCards's transaction, so a stale preview here is
-- never a correctness problem -- worst case the confirmation copy is one save behind.
-- name: ListCardsForNote :many
SELECT ordinal FROM cards WHERE note_id = sqlc.arg(note_id) ORDER BY ordinal;
```

### 3. `internal/db/queries/notes.sql` — extend `UpdateNoteContent`

Change:

```sql
-- name: UpdateNoteContent :execrows
UPDATE notes SET fields = sqlc.arg(fields), tags = sqlc.arg(tags),
                 checksum = sqlc.arg(checksum), modified_at = now()
WHERE id = sqlc.arg(note_id);
```

to:

```sql
-- name: UpdateNoteContent :execrows
UPDATE notes SET fields = sqlc.arg(fields), tags = sqlc.arg(tags),
                 checksum = sqlc.arg(checksum), note_type_id = sqlc.arg(note_type_id),
                 modified_at = now()
WHERE id = sqlc.arg(note_id);
```

Run `go generate ./...` after editing these three `.sql` files (regenerates
`internal/db/note_types.sql.go`, `internal/db/cards.sql.go`, `internal/db/notes.sql.go`); commit
the generated output, don't hand-edit it (CLAUDE.md §16).

### 4. `internal/db/cards.go` — extend `SyncNoteCards`

Currently `existingByOrdinal` only records ordinal presence (`map[int32]struct{}`). Change it to
keep the full row (`map[int32]ListCardsForNoteForUpdateRow`, which already carries `ID` and
`TemplateID`) and, after the existing create-then-destroy steps, add a third step: for every
ordinal present in both `existing` and `desired` whose `TemplateID` differs, call
`q.UpdateCardTemplate(ctx, UpdateCardTemplateParams{ID: existing.ID, TemplateID: desired.TemplateID})`.

This is safe for every existing caller: within a same-note-type field edit, `desired`'s
`TemplateID` for a surviving ordinal is always the same template it already had (both
`CreateNoteWithCards`/`UpdateNoteWithCards`'s existing call sites derive `desired` from the note's
*current* note type's templates via `desiredCards` in `internal/http/notes.go`), so the repoint
`UPDATE` is a guarded no-op there and never fires. It only fires for the new note-type-change call
site, where the ordinal survives but the parent note type — and therefore the template row at
that ordinal — has changed.

Update `SyncNoteCards`'s doc comment to mention the repoint step and why it's needed (a card's
`template_id`, not `notes.note_type_id`, is what the study-batch query's `qfmt`/`afmt` come from).

### 5. `internal/db/notes.go` — `UpdateNoteWithCards` takes the target note type id

Change the signature to:

```go
func UpdateNoteWithCards(ctx context.Context, tx pgx.Tx, userID, noteID, noteTypeID pgtype.UUID, fields []byte, tags []string, checksum int64, desired []DesiredCard) error
```

and pass `NoteTypeID: noteTypeID` into `UpdateNoteContentParams`. The one existing call site
(`internal/http/notes.go`'s `POST /notes/{id}/edit` handler) passes `note.NoteTypeID` unchanged
for an ordinary field/tag edit, and the new target note type id when a note-type change is being
confirmed — so this function stays the single mechanical entry point for "update a note's content
and reconcile its cards," same as today, just with one more always-supplied field. No new
sibling function.

### 6. `internal/http/notes.go` — handler changes

**`GET /notes/{id}/edit`** (around line 153): after fetching `fields`, also fetch

```go
compatible, err := q.ListFieldCompatibleNoteTypesForOwner(r.Context(), db.ListFieldCompatibleNoteTypesForOwnerParams{
    OwnerID: user.ID, IsCloze: nt.IsCloze, CurrentNoteTypeID: note.NoteTypeID,
})
```

and pass it to the template as `"NoteTypeOptions": compatible`.

**`POST /notes/{id}/edit`** (around line 191): after the existing `fields`/`templates` fetch for
the note's *current* note type, add:

```go
targetNoteTypeIDStr := r.PostForm.Get("note_type_id")
var targetNoteTypeID pgtype.UUID
if err := targetNoteTypeID.Scan(targetNoteTypeIDStr); err != nil {
    badRequest(w)
    return
}
```

Branch on `targetNoteTypeID == note.NoteTypeID`:

- **Equal (today's path, unchanged behavior):** proceed exactly as now, just pass
  `note.NoteTypeID` as the new `noteTypeID` argument to `UpdateNoteWithCards`.
- **Different (note-type change requested):**
  1. `targetNT, err := q.GetNoteTypeForOwner(ctx, GetNoteTypeForOwnerParams{ID: targetNoteTypeID, OwnerID: user.ID})` →
     `handleQueryErr` (404 if not found/not owned by the acting user — see Open question).
  2. `targetFields, err := q.ListFieldsForNoteType(ctx, targetNoteTypeID)`.
  3. Compatibility check (authoritative, Go-side — don't trust that the id came from the GET
     page's dropdown): `targetNT.IsCloze == nt.IsCloze` and `len(targetFields) == len(fields)` and
     `targetFields[i].Name == fields[i].Name` for every `i`. On failure: `http.Error(w, "note types must have the same fields in the same order to switch between them", http.StatusBadRequest)`.
  4. `targetTemplates, err := q.ListTemplatesForNoteType(ctx, targetNoteTypeID)`.
  5. `fieldsJSON, checksum, err := validateNoteFields(fieldValues, len(targetFields))` (same
     helper, same rules — field count is already guaranteed equal to the current note's by step 3).
  6. `desired, err := desiredCards(targetNT, targetTemplates, fieldValues)` (same helper used
     elsewhere; handles both the non-cloze per-template case and — if the target also happens to
     be cloze — the cloze-ordinal case, erroring `errNoClozeMarkers` the same way note creation
     does if the fields have no `{{cN::}}` marker).
  7. If `r.PostForm.Get("confirm_note_type_change") != "1"`: **don't mutate.** Compute a preview
     (existing ordinals via `q.ListCardsForNote(ctx, noteID)`, desired ordinals from `desired`;
     removed = existing − desired, added = desired − existing) and re-render `pages["note_form"]`
     with `200 OK` in a new confirm mode (template changes below) carrying the submitted field
     values/tags/target id as hidden fields plus `RemovedCount`/`AddedCount`.
  8. If confirmed: proceed to the existing transaction/`UpdateNoteWithCards` call, passing
     `targetNoteTypeID` as the new note type id and `desired` as computed above.

The existing error-handling `switch` after `UpdateNoteWithCards` (⁣`pgx.ErrNoRows` → 404,
`db.ErrNoCards` → 400, else 500) is unchanged and covers the note-type-change path too (e.g. a
cloze target with no markers still becomes `ErrNoCards` via the same `desiredCards` call feeding
an empty desired set — actually surfaces earlier as `errNoClozeMarkers` at step 6 in this path,
same 400 behavior as note creation today).

### 7. `web/templates/note_form.html` — two additions

a) In the existing edit branch (the final `{{else}}` block), add a note-type `<select>` sourced
from `.NoteTypeOptions`, defaulting to the note's current note type, right before the Save
button:

```html
{{if .NoteTypeOptions}}
<label for="note_type_id">Note type</label>
<select id="note_type_id" name="note_type_id">
    {{range .NoteTypeOptions}}<option value="{{.ID}}" {{if eq .ID $.NoteType.ID}}selected{{end}}>{{.Name}}</option>{{end}}
</select>
{{else}}
<input type="hidden" name="note_type_id" value="{{.NoteType.ID}}">
{{end}}
```

(The `{{else}}` branch is defensive — `ListFieldCompatibleNoteTypesForOwner` always includes the
current note type itself, so `.NoteTypeOptions` should never be empty in practice.)

b) A new top-level branch, checked before `.PickNoteType`/`.IsNew`, for the confirmation step:

```html
{{if .ConfirmNoteTypeChange}}
<h1>Confirm note type change</h1>
<p>Changing this note from "{{.NoteType.Name}}" to "{{.TargetNoteType.Name}}" will permanently
   remove {{.RemovedCount}} card(s) whose template doesn't exist on the new note type (their
   review history is kept, but the card and its current scheduling progress are deleted) and add
   {{.AddedCount}} new card(s) starting fresh.</p>
<form method="post" action="/notes/{{.Note.ID}}/edit">
    <input type="hidden" name="note_type_id" value="{{.TargetNoteType.ID}}">
    <input type="hidden" name="confirm_note_type_change" value="1">
    {{range .FieldValues}}<input type="hidden" name="field[]" value="{{.}}">{{end}}
    <input type="hidden" name="tags" value="{{.Tags}}">
    <button type="submit">Confirm change</button>
</form>
<p><a href="/notes/{{.Note.ID}}/edit">Cancel</a></p>
{{else if .PickNoteType}}
...
```

No JavaScript anywhere in this codebase's templates today (confirmed by inspection) — this is a
plain two-request POST→render→POST confirm, consistent with that.

### 8. `docs/schema.md` — documentation update

Amend the existing "Editing a note's fields must not regenerate its cards..." paragraph (the one
naming `SyncNoteCards` and #54) to mention the note-type-change extension: that changing a note's
`note_type_id` reuses the same diff, plus repoints `template_id` on any surviving ordinal, and
that a removed ordinal's card is hard-deleted the same way a shrunk cloze note's card already is
today (cascading `user_card_state`, orphaning but not deleting `review_log` per the no-FK
decision above it in the same file).

## Tests

Per CLAUDE.md §10 priority list, item 5 (access control) and the "silently corrupts
`user_card_state`/`review_log`" `sev: critical` bucket (§15) — this touches both.

**`internal/db/cards_test.go`** (mirrors `TestSyncNoteCards_KeepsSurvivingOrdinalsUntouched`'s
style):
- `TestSyncNoteCards_RepointsTemplateIDOnSurvivingOrdinal`: two templates on two different note
  types; a card at ordinal 0 pointing at template A; sync with `desired = [{0, templateB}]`;
  assert the card's `id` is unchanged (still the same row) but `template_id` is now `templateB`.
- `TestSyncNoteCards_RemovedOrdinal_ReviewLogSurvivesOrphaned`: card at ordinal 1 with a
  `mustUserCardState` row and a `mustReviewLog` row; sync with a `desired` that drops ordinal 1;
  assert the card row is gone, `user_card_state` for it is gone (cascade), and the `review_log`
  row (by its returned id) still exists with `card_id` unchanged — the concrete regression test
  for the "card deletion vs. review_log" question this plan resolves as already-decided.

**`internal/db/notes_test.go`**: `TestUpdateNoteWithCards_ChangesNoteType` — asserts
`notes.note_type_id` is updated when a different id is passed.

**`internal/http/notes_test.go`**:
- `TestNoteRoutes_ChangeNoteType_ConfirmFlow`: create a note on a 2-template note type
  ("Basic (and reversed card)"-shaped fixture), `POST .../edit` with a different (compatible)
  `note_type_id` and no confirm flag → 200, no DB change (`notes.note_type_id` unchanged, card
  count unchanged); then resubmit with `confirm_note_type_change=1` → 303, `notes.note_type_id`
  updated, one card removed (assert by ordinal), the surviving card's `id` unchanged from before
  the change (looked up before/after) and its `template_id` now points at the new note type's
  template.
- `TestNoteRoutes_ChangeNoteType_FieldMismatch_400`: target note type with a different field
  count (or same count, different names) → 400, nothing changed.
- `TestNoteRoutes_ChangeNoteType_ClozeMismatch_400`: target note type with a different `is_cloze`
  → 400.
- **Access-control regression** (§10 item 5), added as new cases to the existing
  `TestNoteRoutes_AccessControl` table or a new test alongside it: a second user with
  `can_view + can_edit_content` on the owner's shared deck (not the deck/note owner, and not the
  owner of the target note type) attempts `POST .../edit` naming a note type owned by the *first*
  user → 404 (mirrors the `CreateNote` ownership precedent this plan follows).
- **Multiuser scheduling-state regression**: owner shares a deck with a second user (`can_study`
  granted); both users study the note's two cards (each gets `user_card_state` +
  `review_log` for both cards via the normal grading path, or seeded directly for speed); owner
  changes note type dropping card 2; assert for **both** users: card 1's `user_card_state` is
  untouched (same values), card 2's `user_card_state` is gone for both users, and card 2's
  `review_log` rows for both users still exist (orphaned). This is the concrete test for the
  task's point 6 — a note-type change on a shared deck must not corrupt or leak another user's
  per-user state.

## Resolved decision: authorization for the note-type-change action

**Decision: option (b).** Ordinary content edits (field/tag changes, and the `targetNoteTypeID ==
note.NoteTypeID` no-op path) keep today's `can_view AND can_edit_content` threshold, unchanged.
The note-type-change branch specifically (`targetNoteTypeID != note.NoteTypeID`) additionally
requires `can_manage_access` — reserving this destructive, cross-user-impacting action (it can
delete cards and other users' `user_card_state` on a shared deck) for someone closer to deck
ownership than an ordinary content-editing collaborator. The target note type's *ownership* rule
is unchanged from the plan's original default — still `nt.owner_id = user.ID` (`CreateNote`'s
existing precedent), i.e. a caller can only switch to a note type they themselves own; this
decision only raises the bar for who may trigger the action at all.

**Exact change spec:**

1. Add to `internal/db/queries/notes.sql`, after `GetNoteForContentEdit`:

   ```sql
   -- Authorises the stronger can_manage_access permission required specifically for a note-type
   -- change (#138) -- ordinary content edits use can_edit_content alone via
   -- GetNoteForContentEdit/LockNoteForContentEdit. Same no-row-means-404 contract as those.
   -- name: GetNoteForNoteTypeChange :one
   SELECT n.*
   FROM notes n
   JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
                      AND da.can_view AND da.can_edit_content AND da.can_manage_access
   WHERE n.id = sqlc.arg(note_id);
   ```

   Regenerate via `go generate ./...` along with the other query changes in this plan.

2. In `internal/http/notes.go`'s `POST /notes/{id}/edit` handler, in the "different" branch
   (§6 point "Different (note-type change requested)" above), insert this check as the very first
   step, before `GetNoteTypeForOwner`:

   ```go
   if _, err := q.GetNoteForNoteTypeChange(r.Context(), db.GetNoteForNoteTypeChangeParams{
       ID: noteID, UserID: user.ID,
   }); err != nil {
       handleQueryErr(w, err) // pgx.ErrNoRows -> 404, same convention as every other authorization gate here
       return
   }
   ```

   This runs on both the unconfirmed (preview) and confirmed submission, since both enter the
   "different" branch — a caller without `can_manage_access` never sees the confirmation preview
   either, not just the final mutation.

3. `UpdateNoteWithCards`/`LockNoteForContentEdit` are unchanged — they still authorize on
   `can_edit_content` alone, since by the time that transaction runs the stronger check has
   already gated entry to this code path.

**Additional test** (append to the access-control list in Tests, above):
`TestNoteRoutes_ChangeNoteType_NoManageAccess_404` — a second user with `can_view +
can_edit_content` but **not** `can_manage_access` on the owner's shared deck, attempting a
note-type change to a note type *they themselves own* (so the ownership check alone would pass) →
404. Distinguish this from the existing
`TestNoteRoutes_ChangeNoteType_ConfirmFlow`/ownership-precedent test, which covers the target note
type being owned by someone else; this new case isolates the `can_manage_access` gate specifically.
