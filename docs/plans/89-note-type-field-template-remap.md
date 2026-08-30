# Plan: field/template removal and reorder with positional note remap (#89)

Issue: `POST /note-types/{id}/edit` currently 409s (`db.ErrNoteTypeStructureLocked`) on any field/template removal or reorder once the note type has notes, because `notes.fields` is a positional `jsonb` array and a template removal deletes cards. This plan replaces the refusal with an actual remap, for both fields (which touch `notes.fields`) and templates (which touch `cards`), gated by a confirmation preview rather than a hard block.

I verified the investigation summary in the prompt against the current code (`internal/db/notetypes.go`, `internal/db/cards.go`, `internal/db/notes.go`, `internal/http/notetypes.go`, `internal/http/notes.go`, `docs/schema.md`, `docs/plans/138-edit-note-type.md`) — it's accurate as of this session (2026-08-29). One thing it didn't surface, found during design, changes the mechanism materially: see "Correction to the investigation" below.

## Scope for v1

**In scope:**
- Removing and/or reordering existing **fields** on a note type that has notes: rewrite every affected note's `fields` jsonb array positionally, in bulk (no per-note loop).
- Removing and/or reordering existing **templates** on a note type that has notes: hard-delete cards backed by a removed template (cascading `user_card_state`, orphaning `review_log`, per the #51/#138 precedent), and re-point/renumber cards backed by a *kept* template whose ordinal moved — in bulk, no per-note loop.
- A confirmation-preview step before committing a structural change while the note type has notes (mirrors #138), showing note/deck/other-user counts and exactly what's discarded.
- A plain-HTML (no-JS) reorder affordance in the note-type edit form: a numeric "Position" input per field/template row.
- Recomputing `notes.checksum` when a field remap changes which field is now first (its input), since checksum is derived from field 0.

**Out of scope, explicitly:**
- Any change to `SyncNoteCards`, `desiredCards`, or the ordinary per-note edit path (#54/#138) — untouched. This plan adds a new note-type-scoped bulk operation alongside them, not a change to them.
- Any change to `sort_field_idx` handling beyond what exists today. The handler already hardcodes `sortFieldIdx = 0` on every note-type edit (`internal/http/notetypes.go:185`), unconditionally, regardless of field changes — that's pre-existing behavior, not something #89 introduces or worsens, and this plan does not touch it.
- Validating that a template's `qfmt`/`afmt` doesn't still reference a field name being removed (e.g. a stale `{{RemovedField}}`). This renders as empty content, not an error, under the existing render engine — a content-authoring footgun, not a data-corruption risk. Worth a future nice-to-have warning in the preview; not built here.
- A drag-and-drop or up/down-button reorder UI. A numeric position input is the simplest no-JS mechanism and is what this plan builds; nicer UX is a follow-up.
- Fixing the pre-existing note-creation-vs-note-type-edit race (see "Known, pre-existing race" below) — not introduced or worsened by this issue.
- No schema/migration change — this is new queries plus application logic over the existing tables, exactly like #138.

## Correction to the investigation: `cards.ordinal` must track `templates.ordinal`, and that's the hard part

The prompt's summary treats "reuse `SyncNoteCards`" as close to sufficient. It isn't, for the *reorder* case, and tracing this through is the main correctness finding of this plan.

`SyncNoteCards` diffs `existing` vs. `desired` **by ordinal**, and when a surviving ordinal's template differs, it repoints `cards.template_id` (added by #138). That's correct for #138, where the *note type itself* changes wholesale — ordinal 0 meaning "old NT's template" becomes ordinal 0 meaning "new NT's template" is a deliberate, coarse swap.

It's wrong for an in-place **template reorder** on the *same* note type. If template A (ordinal 0) and template B (ordinal 1) swap positions, keying by ordinal and repointing `template_id` would flip every existing card's rendered content: the card that has always rendered as "Card 1" would silently start rendering as "Card 2", mid-schedule, for every user studying it. That's a real content-swap bug, not cosmetic — `cards.template_id` is what the study batch query reads `qfmt`/`afmt` from (confirmed by #138's own investigation).

The correct behavior (and what real Anki does when you reorder its Card Types manager) is the opposite: a card's `template_id` — its content identity — must stay fixed to the same template row; what should move is `cards.ordinal`, to track the template's new position.

That, in turn, creates a **second**, easy-to-miss trap: `desiredCards` (`internal/http/notes.go`) computes `DesiredCard{Ordinal: t.Ordinal, TemplateID: t.ID}` for every *future*, ordinary note edit, straight from the note type's *current* `templates.ordinal`. If a reorder leaves `cards.ordinal` stale (not updated to match), the **next unrelated field/tag edit** on that note calls `SyncNoteCards`, which will see the stale-ordinal card as "not in desired" (desired now uses the new ordinal) and the new-ordinal slot as "missing" — and hard-delete-then-recreate the card, silently destroying `user_card_state` on a completely unrelated, innocuous edit, sometime later. This is exactly the CLAUDE.md §15 "silently corrupts `user_card_state`" bucket, and it would be a **latent time bomb** if `cards.ordinal` isn't kept in sync at remap time. So: on a template reorder, `cards.ordinal` **must** be updated to the template's new ordinal, for every card backed by that template, and `cards.template_id` must **not** change.

This has a further consequence: `cards.ordinal` carries a real `UNIQUE (note_id, ordinal)` constraint (`migrations/00009_cards.sql`), so reassigning ordinals for a general permutation (not just a 2-swap) needs a collision-safe two-phase update (stage to negative ordinals, then finalize) — worked out in full below. By contrast, `fields.ordinal`/`templates.ordinal` are deliberately **not** unique-constrained (`migrations/00004_fields.sql`'s comment: *"reordering a note type's fields swaps two ordinals... a non-deferrable UNIQUE would make the intermediate state of that swap illegal"*) — the schema was already built anticipating in-place ordinal reassignment on those two tables, so field/template row updates need no such staging.

## Design overview

Two independent bulk mechanisms, both driven off a **kept/removed/added** plan computed from comparing the submission against the note type's existing fields/templates (by id):

1. **Fields → `notes.fields`**: one `UPDATE ... WHERE note_type_id = $1` per note-type edit (not per note), rewriting every affected note's field array via a single old-ordinal→new-position permutation/subset array. Plus a conditional, also-bulk, checksum recompute.
2. **Templates → `cards`**: a new `RemapNoteTypeCards` function (`internal/db/cards.go`, sibling to `SyncNoteCards`) doing, in a fixed number of SQL statements bounded by *how many templates changed* (never by note count):
   - stage affected kept-template cards to negative ordinals (collision-safe swap),
   - delete cards for removed templates, then delete the now-cardless template rows,
   - create cards for added templates (reuses the existing `CreateCardsForNewTemplate`, one call per added template),
   - finalize kept-template cards to their real new ordinals.

Both run inside the same transaction as today's `UpdateNoteType`, under the same `LockNoteTypeForOwner ... FOR UPDATE` row lock — no chunking, no batching. Given every step above is O(1) round trips regardless of note count, chunking would be premature complexity (CLAUDE.md "Simplicity First"): a note type with many thousands of notes still costs a small constant number of statements, and "must not be possible for partial application" falls out for free from doing it all in one transaction the caller commits, exactly like `UpdateNoteType` does today.

A structural change (removal or reorder of an existing field/template) while the note type has notes requires a confirmation step first, mirroring #138. A non-structural change (pure rename, pure trailing append) — or any change while the note type has zero notes — applies immediately, exactly as today; no new friction for the common case.

## 1. `internal/db/queries/fields.sql` — replace `RenameField` with an ordinal-aware version, add delete-by-id

```sql
-- name: ListFieldsForNoteType :many
SELECT * FROM fields WHERE note_type_id = $1 ORDER BY ordinal;

-- name: ListFieldsForNoteTypes :many
SELECT note_type_id, ordinal, name FROM fields
WHERE note_type_id = ANY(sqlc.arg(note_type_ids)::uuid[])
ORDER BY note_type_id, ordinal;

-- name: CreateField :one
INSERT INTO fields (note_type_id, ordinal, name) VALUES ($1, $2, $3) RETURNING *;

-- Renames AND repositions a kept field in one statement (#89): fields.ordinal has no UNIQUE
-- constraint (migrations/00004_fields.sql -- deliberately, for exactly this reason), so an
-- in-place ordinal reassignment, including a full permutation, is always collision-free.
-- name: UpdateFieldNameAndOrdinal :execrows
UPDATE fields SET name = sqlc.arg(name), ordinal = sqlc.arg(ordinal)
WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);

-- name: DeleteField :execrows
DELETE FROM fields WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);
```

Remove `RenameField` and `DeleteFieldsForNoteType` — grep-confirmed (this session) both are called only from `UpdateNoteType`, which no longer needs a wholesale delete-all-then-recreate path (see §4).

## 2. `internal/db/queries/templates.sql` — same shape as fields, plus a bulk id-list delete

```sql
-- name: ListTemplatesForNoteType :many
SELECT * FROM templates WHERE note_type_id = $1 ORDER BY ordinal;

-- name: CreateTemplate :one
INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt) VALUES ($1,$2,$3,$4,$5) RETURNING *;

-- Renames/re-formats AND repositions a kept template in one statement (#89), same reasoning as
-- UpdateFieldNameAndOrdinal -- templates.ordinal is likewise not UNIQUE-constrained.
-- name: UpdateTemplateContentAndOrdinal :execrows
UPDATE templates SET name = sqlc.arg(name), qfmt = sqlc.arg(qfmt), afmt = sqlc.arg(afmt),
                     ordinal = sqlc.arg(ordinal)
WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);

-- Only safe to call once every card backed by these templates is already gone --
-- cards.template_id REFERENCES templates ON DELETE RESTRICT (docs/schema.md's Deletion policy).
-- RemapNoteTypeCards (internal/db/cards.go) always calls DeleteCardsForTemplates first.
-- name: DeleteTemplatesByIDs :execrows
DELETE FROM templates WHERE note_type_id = sqlc.arg(note_type_id) AND id = ANY(sqlc.arg(ids)::uuid[]);
```

Remove `UpdateTemplate` and `DeleteTemplatesForNoteType` — same grep-confirmed "only called from `UpdateNoteType`" reasoning.

## 3. `internal/db/queries/cards.sql` — three new bulk statements, `AppendNoteFieldSlot` removed

```sql
-- Offsets every card backed by these templates to a negative, per-note-unique ordinal so that a
-- subsequent finalize (FinalizeCardOrdinalsForTemplates) can permute ordinals -- including cyclic,
-- not just pairwise -- without transiently violating cards_note_id_ordinal_key. Negative values
-- never collide with a real (>=0) ordinal, touched or not, and the transform x -> -(x+1) is
-- injective, so cards that were already distinct per note stay distinct. Never committed in this
-- shape -- FinalizeCardOrdinalsForTemplates always runs before the transaction commits.
-- name: OffsetCardOrdinalsForTemplates :execrows
UPDATE cards SET ordinal = -(ordinal + 1) WHERE template_id = ANY(sqlc.arg(template_ids)::uuid[]);

-- Sets the real final ordinal for cards staged by OffsetCardOrdinalsForTemplates. Safe as one
-- statement because every affected card is currently negative (from the offset above) and every
-- target is non-negative and, by construction of the caller's plan, unique per note -- so no row's
-- new value can collide with another affected row's current (still-negative) value, or with an
-- untouched card's (already-final, non-negative) value.
-- name: FinalizeCardOrdinalsForTemplates :execrows
WITH m AS (
    SELECT t.template_id, o.new_ordinal
    FROM unnest(sqlc.arg(template_ids)::uuid[]) WITH ORDINALITY AS t(template_id, ord)
    JOIN unnest(sqlc.arg(new_ordinals)::int[]) WITH ORDINALITY AS o(new_ordinal, ord)
      ON t.ord = o.ord
)
UPDATE cards c SET ordinal = m.new_ordinal FROM m WHERE c.template_id = m.template_id;

-- A removed template's cards, across every note of the note type, in one statement (#89). Cascades
-- user_card_state; leaves any review_log rows in place, orphaned but intact -- card_id has no FK
-- (docs/schema.md's Deletion policy), so this is the same, already-decided convention #138 already
-- exercises for a single note's card removal, just applied note-type-wide.
-- name: DeleteCardsForTemplates :execrows
DELETE FROM cards WHERE template_id = ANY(sqlc.arg(template_ids)::uuid[]);
```

Remove `AppendNoteFieldSlot` — grep-confirmed its only caller is `UpdateNoteType`'s old non-structural-append branch, which the new unified field-remap (§5) subsumes: a pure trailing append is just the general case with `old_ordinals[i] == -1` for the new field, producing the identical `fields || '""'::jsonb` effect via `RemapNoteFields` (§4). `CreateCardsForNewTemplate` is unchanged and reused as-is by `RemapNoteTypeCards` for added templates.

## 4. `internal/db/queries/notes.sql` — bulk field-array remap and bulk checksum update

```sql
-- Rewrites every note of note_type_id's fields array positionally, in one statement (#89): new
-- position i takes its value from OLD ordinal old_ordinals[i], or an empty string when
-- old_ordinals[i] is -1 (a field newly added by this same edit, which no note has ever held a
-- value for -- same '""'::jsonb sentinel AppendNoteFieldSlot used). old_ordinals encodes the full
-- old->new permutation and/or subset (a removed field simply has no entry) in one array, so this
-- costs one UPDATE regardless of note count -- it scales in rows touched, not round trips.
-- name: RemapNoteFields :execrows
UPDATE notes n
SET fields = (
    SELECT jsonb_agg(CASE WHEN t.old_ordinal < 0 THEN '""'::jsonb ELSE n.fields -> t.old_ordinal END
                     ORDER BY t.pos)
    FROM unnest(sqlc.arg(old_ordinals)::int[]) WITH ORDINALITY AS t(old_ordinal, pos)
),
    modified_at = now()
WHERE n.note_type_id = sqlc.arg(note_type_id);

-- Non-locking read used only to recompute checksum after a field remap that changes which field is
-- now first (notes.checksum is sha1-of-stripped-html of field 0 -- see db.ComputeNoteChecksum).
-- name: ListNoteFieldsForNoteType :many
SELECT id, fields FROM notes WHERE note_type_id = sqlc.arg(note_type_id);

-- name: BulkUpdateNoteChecksums :execrows
WITH v AS (
    SELECT i.note_id, c.checksum
    FROM unnest(sqlc.arg(note_ids)::uuid[]) WITH ORDINALITY AS i(note_id, ord)
    JOIN unnest(sqlc.arg(checksums)::bigint[]) WITH ORDINALITY AS c(checksum, ord)
      ON i.ord = c.ord
)
UPDATE notes n SET checksum = v.checksum FROM v WHERE n.id = v.note_id;
```

The two-`unnest`-joined-on-`ORDINALITY` shape mirrors an existing pattern already proven against this project's live-catalog sqlc setup (`internal/db/queries/reviews.sql`'s `CountQueueForUser`) — not a new idiom. `internal/db/queries/notes.sql`'s existing `UpdateNoteContent`, `CreateNote`, `LockNoteForContentEdit`, `GetNoteForContentEdit`, `GetNoteForNoteTypeChange`, `MoveNoteToDeck`, `MoveNoteCardsFromDeck`, `DeleteNote`, `ListNoteIDsOfNoteType` are all unchanged.

Run `go generate ./...` after these four `.sql` edits; commit the generated `internal/db/*.sql.go`, don't hand-edit (CLAUDE.md §16). **Flag for the implementer:** verify `RemapNoteFields`'s correlated subquery (`n.fields -> t.old_ordinal` referencing the outer `notes` row inside a `SET` subquery) compiles under `sqlc generate` against the real schema — this project's other correlated subqueries (`ListNotesInDeck`'s card-count subselect, `ListFieldCompatibleNoteTypesForOwner`'s `array_agg` comparison) suggest it will, but this could not be run directly to confirm.

## 5. `internal/db/notes.go` — shared checksum helper

Move the checksum algorithm out of `internal/http/notes.go` into `internal/db` (both `UpdateNoteType`'s bulk recompute and the ordinary note-edit path need it; `internal/db` must not import `internal/http`, and `internal/http` already imports `internal/db`, so this is the only direction that keeps dependencies flowing the way they already do):

```go
// internal/db/notes.go, new addition

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// ComputeNoteChecksum is Anki's own csum algorithm: truncated SHA-1 of a note's first field with
// HTML tags stripped. Shared by internal/http/notes.go's validateNoteFields (ordinary note
// create/edit) and UpdateNoteType's bulk recompute (#89, when a field remap changes which field is
// now first).
func ComputeNoteChecksum(firstField string) int64 {
	stripped := htmlTagRe.ReplaceAllString(firstField, "")
	sum := sha1.Sum([]byte(stripped)) //nolint:gosec // Anki csum compatibility, not a security use of SHA-1
	return int64(binary.BigEndian.Uint32(sum[:4]))
}
```

`internal/http/notes.go`'s `validateNoteFields` changes its last three lines to `checksum = db.ComputeNoteChecksum(fieldValues[0])`; remove its now-unused `htmlTagRe` var and the `crypto/sha1`/`encoding/binary`/`regexp` imports (all three become orphaned by this move — CLAUDE.md's "remove imports YOUR change orphaned").

## 6. `internal/db/notetypes.go` — the core rewrite

Delete `ErrNoteTypeStructureLocked` entirely (dead once the gate moves to the handler as a confirm-flag check, not a refusal). Keep `ErrClozeNoteTypeSingleTemplate`, `ErrFieldNotFound`, `ErrTemplateNotFound`, `FieldEdit`, `TemplateEdit` unchanged. Keep `CreateNoteTypeWithFieldsAndTemplates` unchanged.

Keep `fieldOrderChanged`/`templateOrderChanged` (rename to exported `FieldOrderChanged`/`TemplateOrderChanged` — the handler needs to call them pre-transaction, on a non-locking read, to decide whether to show the confirmation preview). Their logic is unchanged; they're now purely a "does this need confirmation" signal, decoupled from how the mutation itself works.

New unexported plan helpers:

```go
// fieldPlan and templatePlan describe a validated note-type edit's effect on the note type's own
// fields/templates rows, computed once (inside the lock) and consumed by both the row mutation and
// (for templates) the cards remap. keptOldOrdinal is parallel to the submitted slice: -1 means "new
// entry, no prior row"; otherwise it's the existing row's ordinal before this edit.

type fieldPlan struct {
	keptOldOrdinal []int32
	removedIDs     []pgtype.UUID
}

func planFields(existing []Field, submitted []FieldEdit) (fieldPlan, error) {
	existingByID := make(map[pgtype.UUID]Field, len(existing))
	for _, f := range existing {
		existingByID[f.ID] = f
	}
	seen := make(map[pgtype.UUID]bool, len(submitted))
	plan := fieldPlan{keptOldOrdinal: make([]int32, len(submitted))}
	for i, f := range submitted {
		if !f.ID.Valid {
			plan.keptOldOrdinal[i] = -1
			continue
		}
		ex, ok := existingByID[f.ID]
		if !ok {
			return fieldPlan{}, ErrFieldNotFound // forged/stale/foreign id, same as today's RenameField n==0 case
		}
		plan.keptOldOrdinal[i] = ex.Ordinal
		seen[f.ID] = true
	}
	for _, ex := range existing {
		if !seen[ex.ID] {
			plan.removedIDs = append(plan.removedIDs, ex.ID)
		}
	}
	return plan, nil
}

// templatePlan is the same shape as fieldPlan; a separate type only for readability at call sites.
type templatePlan struct {
	keptOldOrdinal []int32
	removedIDs     []pgtype.UUID
}

func planTemplates(existing []Template, submitted []TemplateEdit) (templatePlan, error) {
	// identical construction to planFields, over Template/TemplateEdit, returning ErrTemplateNotFound
}
```

`UpdateNoteType`'s new body (signature unchanged — `error` return, same params as today):

```go
func UpdateNoteType(ctx context.Context, tx pgx.Tx, ownerID, noteTypeID pgtype.UUID, name, css string, sortFieldIdx int32, fields []FieldEdit, templates []TemplateEdit) error {
	q := New(tx)

	nt, err := q.LockNoteTypeForOwner(ctx, LockNoteTypeForOwnerParams{ID: noteTypeID, OwnerID: ownerID})
	if err != nil {
		return err
	}
	if nt.IsCloze && len(templates) != 1 {
		return ErrClozeNoteTypeSingleTemplate
	}

	existingFields, err := q.ListFieldsForNoteType(ctx, noteTypeID)
	if err != nil {
		return err
	}
	existingTemplates, err := q.ListTemplatesForNoteType(ctx, noteTypeID)
	if err != nil {
		return err
	}

	if _, err := q.UpdateNoteTypeRow(ctx, UpdateNoteTypeRowParams{
		Name: name, Css: css, SortFieldIdx: sortFieldIdx, ID: noteTypeID, OwnerID: ownerID,
	}); err != nil {
		return err
	}

	fieldsPlan, err := planFields(existingFields, fields)
	if err != nil {
		return err
	}
	if err := applyFieldPlan(ctx, q, noteTypeID, fields, fieldsPlan); err != nil {
		return err
	}

	templatesPlan, err := planTemplates(existingTemplates, templates)
	if err != nil {
		return err
	}
	if err := applyTemplatePlan(ctx, tx, noteTypeID, templates, templatesPlan); err != nil {
		return err
	}

	return nil
}
```

`applyFieldPlan` (fields-table rows + bulk `notes.fields` remap + conditional checksum recompute):

```go
func applyFieldPlan(ctx context.Context, q *Queries, noteTypeID pgtype.UUID, fields []FieldEdit, plan fieldPlan) error {
	if len(plan.removedIDs) > 0 {
		if _, err := q.DeleteFieldByIDs(ctx, ...); err != nil { // or loop DeleteField per id -- see note below
			return err
		}
	}
	for i, f := range fields {
		if plan.keptOldOrdinal[i] >= 0 {
			n, err := q.UpdateFieldNameAndOrdinal(ctx, UpdateFieldNameAndOrdinalParams{
				Name: f.Name, Ordinal: int32(i), ID: f.ID, NoteTypeID: noteTypeID,
			})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrFieldNotFound
			}
			continue
		}
		if _, err := q.CreateField(ctx, CreateFieldParams{NoteTypeID: noteTypeID, Ordinal: int32(i), Name: f.Name}); err != nil {
			return err
		}
	}

	if _, err := q.RemapNoteFields(ctx, RemapNoteFieldsParams{NoteTypeID: noteTypeID, OldOrdinals: plan.keptOldOrdinal}); err != nil {
		return err
	}

	if len(fields) > 0 && plan.keptOldOrdinal[0] != 0 {
		// Field 0 identity changed (or is brand new) -- notes.checksum, derived from field 0
		// (db.ComputeNoteChecksum), is now stale. Recompute in bulk: one SELECT, one Go-side loop,
		// one bulk UPDATE -- not one round trip per note.
		rows, err := q.ListNoteFieldsForNoteType(ctx, noteTypeID)
		if err != nil {
			return err
		}
		ids := make([]pgtype.UUID, 0, len(rows))
		checksums := make([]int64, 0, len(rows))
		for _, r := range rows {
			var vals []string
			if err := json.Unmarshal(r.Fields, &vals); err != nil || len(vals) == 0 {
				continue // defensive; every note of this note type now has >=1 field by construction
			}
			ids = append(ids, r.ID)
			checksums = append(checksums, ComputeNoteChecksum(vals[0]))
		}
		if len(ids) > 0 {
			if _, err := q.BulkUpdateNoteChecksums(ctx, BulkUpdateNoteChecksumsParams{NoteIds: ids, Checksums: checksums}); err != nil {
				return err
			}
		}
	}
	return nil
}
```

A bulk `DeleteFieldByIDs` query is sketched above for symmetry with templates, but since removed-field count is bounded by the note type's own field count (small), a plain loop calling the existing per-id `DeleteField` is equally fine and one fewer query to add — **default to the loop** (fewer new queries, "Simplicity First," and it's not the hot path — the hot/many-rows path is `RemapNoteFields`, which is already a single statement regardless).

`applyTemplatePlan` (templates-table rows + `RemapNoteTypeCards`):

```go
func applyTemplatePlan(ctx context.Context, tx pgx.Tx, noteTypeID pgtype.UUID, templates []TemplateEdit, plan templatePlan) error {
	q := New(tx)
	var changed []TemplateOrdinalChange
	var added []AddedTemplate

	for i, t := range templates {
		if plan.keptOldOrdinal[i] >= 0 {
			n, err := q.UpdateTemplateContentAndOrdinal(ctx, UpdateTemplateContentAndOrdinalParams{
				Name: t.Name, Qfmt: t.Qfmt, Afmt: t.Afmt, Ordinal: int32(i), ID: t.ID, NoteTypeID: noteTypeID,
			})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrTemplateNotFound
			}
			if plan.keptOldOrdinal[i] != int32(i) {
				changed = append(changed, TemplateOrdinalChange{TemplateID: t.ID, NewOrdinal: int32(i)})
			}
			continue
		}
		created, err := q.CreateTemplate(ctx, CreateTemplateParams{NoteTypeID: noteTypeID, Ordinal: int32(i), Name: t.Name, Qfmt: t.Qfmt, Afmt: t.Afmt})
		if err != nil {
			return err
		}
		added = append(added, AddedTemplate{TemplateID: created.ID, Ordinal: int32(i)})
	}

	if len(plan.removedIDs) == 0 && len(changed) == 0 && len(added) == 0 {
		return nil
	}
	return RemapNoteTypeCards(ctx, tx, noteTypeID, changed, plan.removedIDs, added)
}
```

Note `UpdateTemplateContentAndOrdinal` runs for *every kept* template regardless of whether its ordinal actually changed — harmless (a same-value `ordinal` write), and keeps the code path uniform rather than branching on "did the ordinal change" twice (once for the row update, once for `changed`).

## 7. `internal/db/cards.go` — `RemapNoteTypeCards`

```go
// TemplateOrdinalChange is a kept template whose ordinal moved (#89's reorder case).
type TemplateOrdinalChange struct {
	TemplateID pgtype.UUID
	NewOrdinal int32
}

// AddedTemplate is a template created by this same note-type edit.
type AddedTemplate struct {
	TemplateID pgtype.UUID
	Ordinal    int32
}

// RemapNoteTypeCards reconciles cards.ordinal/existence for every note of a note type against a
// structural change to that note type's templates (#89). Unlike SyncNoteCards (per-note, called on
// an ordinary note edit), this is note-type-scoped: it costs a small, fixed number of SQL
// statements -- bounded by how many templates changed, never by how many notes the note type has --
// not one round trip per note.
//
// A kept template whose ordinal moved keeps its id and its cards' template_id (card content
// identity must not change on a reorder -- see this plan's "cards.ordinal must track
// templates.ordinal" note); only cards.ordinal moves, in two phases (stage to negative, then
// finalize) because cards carries a genuine UNIQUE (note_id, ordinal) constraint a same-statement
// permutation could otherwise violate mid-write. A removed template's cards are hard-deleted --
// cascading user_card_state, orphaning but not deleting review_log, per the #51/#138 precedent --
// before the now-cardless template row itself is deleted (cards.template_id is ON DELETE
// RESTRICT). An added template's cards are created via the existing CreateCardsForNewTemplate, one
// call per added template.
//
// Must be called inside a transaction that already holds LockNoteTypeForOwner's row lock on the
// note type. The caller commits.
func RemapNoteTypeCards(ctx context.Context, tx pgx.Tx, noteTypeID pgtype.UUID, changed []TemplateOrdinalChange, removed []pgtype.UUID, added []AddedTemplate) error {
	q := New(tx)

	if len(changed) > 0 {
		ids := make([]pgtype.UUID, len(changed))
		for i, c := range changed {
			ids[i] = c.TemplateID
		}
		if _, err := q.OffsetCardOrdinalsForTemplates(ctx, ids); err != nil {
			return err
		}
	}

	if len(removed) > 0 {
		if _, err := q.DeleteCardsForTemplates(ctx, removed); err != nil {
			return err
		}
		if _, err := q.DeleteTemplatesByIDs(ctx, DeleteTemplatesByIDsParams{NoteTypeID: noteTypeID, Ids: removed}); err != nil {
			return err
		}
	}

	for _, a := range added {
		if _, err := q.CreateCardsForNewTemplate(ctx, CreateCardsForNewTemplateParams{
			TemplateID: a.TemplateID, Ordinal: a.Ordinal, NoteTypeID: noteTypeID,
		}); err != nil {
			return err
		}
	}

	if len(changed) > 0 {
		ids := make([]pgtype.UUID, len(changed))
		ords := make([]int32, len(changed))
		for i, c := range changed {
			ids[i] = c.TemplateID
			ords[i] = c.NewOrdinal
		}
		if _, err := q.FinalizeCardOrdinalsForTemplates(ctx, FinalizeCardOrdinalsForTemplatesParams{
			TemplateIds: ids, NewOrdinals: ords,
		}); err != nil {
			return err
		}
	}

	return nil
}
```

Ordering is load-bearing: **offset → delete-cards-then-templates → add → finalize**. Offset must precede finalize (that's the whole point of staging). Offset and delete must precede add, in case an added template's target ordinal coincides with a changed-kept template's *old* ordinal (still occupied until offset runs) or a *just-removed* template's ordinal (occupied until delete runs) — both are guaranteed vacated by the time add runs. Finalize must be last, once every other phase has settled which ordinals are actually free.

## 8. `internal/http/notetypes.go` — handler rewrite

**`GET /note-types/{id}/edit`**: unchanged in shape; fields/templates are already listed in ordinal order, which is what the position inputs default to.

**`POST /note-types/{id}/edit`**:

```go
user, _ := auth.UserFromContext(r.Context())
id, ok := pathUUID(r, "id")
if !ok { notFound(w); return }
if !parseForm(w, r) { return }

name := strings.TrimSpace(r.PostForm.Get("name"))
css := r.PostForm.Get("css")
if name == "" { badRequest(w); return }

fields, ok := parseFieldEdits(w, r) // field_id[]/field_name[]/field_position[], sorted by position,
if !ok { return }                  // blank-name rows skipped (today's "remove via blank" convention, unchanged)
if len(fields) == 0 {
	http.Error(w, "a note type needs at least one field", http.StatusBadRequest)
	return
}

templates, ok := parseTemplateEdits(w, r) // template_id[]/template_name[]/qfmt[]/afmt[]/template_position[]
if !ok { return }
if len(templates) == 0 {
	http.Error(w, "at least one template is required", http.StatusBadRequest)
	return
}

q := db.New(store)
nt, err := q.GetNoteTypeForOwner(r.Context(), db.GetNoteTypeForOwnerParams{ID: id, OwnerID: user.ID})
if handleQueryErr(w, err) { return }
if nt.IsCloze && len(templates) != 1 {
	http.Error(w, "a cloze note type must have exactly one template", http.StatusBadRequest)
	return
}

existingFields, err := q.ListFieldsForNoteType(r.Context(), id)
if err != nil { serverError(w); return }
existingTemplates, err := q.ListTemplatesForNoteType(r.Context(), id)
if err != nil { serverError(w); return }

structural := db.FieldOrderChanged(existingFields, fields) || db.TemplateOrderChanged(existingTemplates, templates)
noteCount, err := q.CountNotesOfNoteType(r.Context(), id)
if err != nil { serverError(w); return }

if structural && noteCount > 0 && r.PostForm.Get("confirm_structural_change") != "1" {
	preview, err := buildStructuralChangePreview(r.Context(), q, user.ID, id, existingFields, fields, existingTemplates, templates, noteCount)
	if err != nil { serverError(w); return }
	render(w, pages["notetype_form"], http.StatusOK, map[string]any{
		"User": user, "NoteType": nt, "Fields": fields, "Templates": templates,
		"ConfirmStructuralChange": true, "Preview": preview,
		"Name": name, "Css": css, // for the hidden re-submit fields
	})
	return
}

tx, ok := startTx(r.Context(), w, store)
if !ok { return }
defer func() { _ = tx.Rollback(r.Context()) }()

err = db.UpdateNoteType(r.Context(), tx, user.ID, id, name, css, 0, fields, templates)
if err != nil {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
	case errors.Is(err, db.ErrClozeNoteTypeSingleTemplate):
		http.Error(w, "a cloze note type must have exactly one template", http.StatusBadRequest)
	case errors.Is(err, db.ErrFieldNotFound), errors.Is(err, db.ErrTemplateNotFound):
		badRequest(w)
	case db.IsUniqueViolation(err, "note_types_owner_id_name_key"):
		http.Error(w, "you already have a note type with that name", http.StatusConflict)
	default:
		serverError(w)
	}
	return
}
if !commitTx(r.Context(), w, tx) { return }
http.Redirect(w, r, "/note-types", http.StatusSeeOther)
```

`db.ErrNoteTypeStructureLocked` case removed from the switch (the error no longer exists).

`parseFieldEdits`/`parseTemplateEdits` are new small helpers replacing the inline parsing blocks: same id/name(/qfmt/afmt) parsing as today, plus parsing `field_position[]`/`template_position[]` (`strconv.Atoi`, 400 on parse failure — same strictness as the existing malformed-UUID 400), then `sort.SliceStable` by position before building the `[]db.FieldEdit`/`[]db.TemplateEdit` slice (stable sort preserves submission order as the tie-break, so an untouched position value never reorders anything relative to its siblings).

`buildStructuralChangePreview` computes:

```go
type structuralChangePreview struct {
	NoteCount        int64
	DeckCount        int64
	OtherUserCount   int64
	RemovedFieldNames    []string
	RemovedTemplateNames []string
	RemovedCardCount     int64
	AddedTemplateCount   int
	AddedCardEstimate    int64 // AddedTemplateCount * NoteCount
}
```

using:
- `noteCount` (already fetched by the caller),
- a new `NoteTypeImpactSummary` query (deck/other-user counts),
- `existingFields`/`existingTemplates` minus the kept-id set (Go-side, no new query — names for display),
- a new `CountCardsForTemplates` query, called with the removed template ids, for `RemovedCardCount`.

```sql
-- internal/db/queries/note_types.sql, new addition
-- Non-locking preview reads for the #89 structural-change confirmation page. Same tolerance as
-- #138's ListCardsForNote: a stale preview here is never a correctness problem, since the actual
-- mutation re-reads everything fresh under LockNoteTypeForOwner's row lock.
-- name: NoteTypeImpactSummary :one
SELECT
  (SELECT count(DISTINCT deck_id) FROM notes WHERE note_type_id = sqlc.arg(note_type_id)) AS deck_count,
  (SELECT count(DISTINCT da.user_id) FROM notes n
     JOIN deck_access da ON da.deck_id = n.deck_id
     WHERE n.note_type_id = sqlc.arg(note_type_id) AND da.user_id != sqlc.arg(owner_id)) AS other_user_count;

-- internal/db/queries/cards.sql, new addition
-- name: CountCardsForTemplates :one
SELECT count(*) FROM cards WHERE template_id = ANY(sqlc.arg(template_ids)::uuid[]);
```

`AddedCardEstimate` is pure arithmetic (`len(addedTemplates) * noteCount`), no query — the actual insert uses `ON CONFLICT (note_id, ordinal) DO NOTHING` so this is an estimate, same "worst case the confirmation copy is one save behind" tolerance as #138.

## 9. `web/templates/notetype_form.html` — position inputs + confirmation branch

Add a "Position" number input to each field/template row (default value = current ordinal for existing rows, `len .Fields`/`len .Templates` for the trailing blank "add new" row):

```html
<fieldset>
    <legend>Fields</legend>
    {{range .Fields}}
    <div>
        <input type="hidden" name="field_id[]" value="{{.ID}}">
        <input type="text" name="field_name[]" value="{{.Name}}" required>
        <label>Position</label>
        <input type="number" name="field_position[]" value="{{.Ordinal}}" min="0">
    </div>
    {{end}}
    <div>
        <input type="hidden" name="field_id[]" value="">
        <input type="text" name="field_name[]" placeholder="New field">
        <input type="number" name="field_position[]" value="{{len .Fields}}" min="0">
    </div>
</fieldset>
```

Same pattern for `template_position[]` in the templates `<fieldset>`.

New top-level branch, checked first (before `.IsNew`):

```html
{{if .ConfirmStructuralChange}}
<h1>Confirm note type change</h1>
<p>This note type has {{.Preview.NoteCount}} note(s) across {{.Preview.DeckCount}} deck(s)
   {{if gt .Preview.OtherUserCount 0}}, {{.Preview.OtherUserCount}} of them shared with other users
   whose scheduling progress will also be affected{{end}}.</p>

{{if .Preview.RemovedFieldNames}}
<p><strong>Removing field(s) {{range .Preview.RemovedFieldNames}}"{{.}}" {{end}}will permanently
   discard that field's content from every note above. This cannot be undone.</strong></p>
{{end}}

{{if .Preview.RemovedTemplateNames}}
<p>Removing template(s) {{range .Preview.RemovedTemplateNames}}"{{.}}" {{end}}will permanently
   delete {{.Preview.RemovedCardCount}} card(s). Each card's review history is kept (orphaned, not
   deleted) but its scheduling progress is discarded.</p>
{{end}}

{{if gt .Preview.AddedTemplateCount 0}}
<p>Adding {{.Preview.AddedTemplateCount}} template(s) will generate up to
   {{.Preview.AddedCardEstimate}} new card(s), starting fresh.</p>
{{end}}

<form method="post" action="/note-types/{{.NoteType.ID}}/edit">
    <input type="hidden" name="name" value="{{.Name}}">
    <input type="hidden" name="css" value="{{.Css}}">
    {{range .Fields}}
    <input type="hidden" name="field_id[]" value="{{.ID}}">
    <input type="hidden" name="field_name[]" value="{{.Name}}">
    <input type="hidden" name="field_position[]" value="{{.Ordinal}}">
    {{end}}
    {{range .Templates}}
    <input type="hidden" name="template_id[]" value="{{.ID}}">
    <input type="hidden" name="template_name[]" value="{{.Name}}">
    <input type="hidden" name="qfmt[]" value="{{.Qfmt}}">
    <input type="hidden" name="afmt[]" value="{{.Afmt}}">
    <input type="hidden" name="template_position[]" value="{{.Ordinal}}">
    {{end}}
    <input type="hidden" name="confirm_structural_change" value="1">
    <button type="submit">Confirm change</button>
</form>
<p><a href="/note-types/{{.NoteType.ID}}/edit">Cancel</a></p>
{{else if .IsNew}}
...
```

Note `.Fields`/`.Templates` on the confirm page are the **already-sorted, submitted** `[]db.FieldEdit`/`[]db.TemplateEdit` (their `.Ordinal` here is really "submitted position," reusing the same field names as the struct for template simplicity — worth a one-line comment in the template or a small dedicated preview view-struct if that's confusing at implementation time). No JavaScript, consistent with the rest of this codebase's templates (confirmed by inspection, same as #138 noted).

## 10. Authorization: resolved decision — no new gate

#138 added a `can_manage_access` gate on top of `can_edit_content` specifically because its action (changing one note's note type) is triggered by whoever is editing *that note*, and on a shared deck that can be a collaborator with only `can_edit_content` — a strictly *weaker* actor than the deck/note-type owner, given a lever with cross-user blast radius (deleting another user's `user_card_state`). That's a real privilege-escalation gap #138 had to close.

**#89's entry point (`POST /note-types/{id}/edit`) has no such gap.** It is authorized purely by note-type *ownership* (`LockNoteTypeForOwner`: `nt.owner_id = user.ID`), not by any deck's `deck_access` — note types have no sharing/collaboration model of their own (no `note_type_access` table; a note type is owned by exactly one user, full stop). There is no weaker "collaborator" identity that can trigger this action against someone else's note type the way #138's collaborator could trigger a note-type swap against someone else's note type; the actor is always, unconditionally, the note type's sole owner. There is nothing stronger to gate against **within note-type ownership**, because note-type ownership has no internal permission gradations to escalate past.

The genuine risk here — an owner's edit reaching into `user_card_state`/`review_log` for *other* users on decks the owner doesn't own but has shared access to — is inherent to the existing shared-deck model already: any `can_edit_content` collaborator editing a note on a shared deck today can already delete cards (e.g. shrinking a cloze note) and thereby affect every other studier's `user_card_state` on that card, with no note-type-specific gate involved at all. #89 doesn't add a new *kind* of cross-user exposure, it adds a *bulk* instance of the exposure the schema already accepts everywhere content is shared. The right mitigation is making the actor aware of the blast radius before they commit — which is exactly what the confirmation preview's deck/other-user counts (§8) are for — not an authorization gate that has no weaker actor to check against.

**Decision: keep authorization exactly as it is today — note-type ownership only (`LockNoteTypeForOwner`/`GetNoteTypeForOwner`).** No new permission check.

## Edge cases worked through

1. **Pure field reorder, no removal.** `planFields` produces `keptOldOrdinal` as a permutation of `0..n-1`, no `removedIDs`. `RemapNoteFields` rewrites every note's array to match. If field 0's identity changes, checksum is recomputed; otherwise the whole thing is one `UPDATE` per table, zero cards touched.
2. **Field removal.** The value is **permanently discarded** from every affected note — unlike cards, there's no orphaned-but-recoverable concept for field content once it's overwritten. Called out explicitly, and strongly, in the preview copy — this is the single most destructive, least-recoverable part of this feature.
3. **Template removal.** Cards deleted (cascade `user_card_state`, orphan `review_log`) — established #51/#138 convention, unchanged.
4. **Template reorder without removal.** The subtle one (see "Correction to the investigation" above): `cards.template_id` must stay fixed (content identity), `cards.ordinal` must move to match (or the *next*, unrelated note edit silently destroys the card via `SyncNoteCards`'s stale-ordinal diff). Handled by `RemapNoteTypeCards`'s stage/finalize phases.
5. **Combined edit**: some fields removed, others reordered, others added, simultaneously with templates removed/reordered/added. Fields and templates are handled by fully independent mechanisms (`notes.fields` jsonb vs. `cards` rows) with no interaction between them — order between `applyFieldPlan`/`applyTemplatePlan` doesn't matter.
6. **Cloze note types.** Always exactly one template (`ErrClozeNoteTypeSingleTemplate` unchanged, checked first). A cloze template "replacement" (old id absent, new id present in one submission) is just remove+add, handled uniformly — no special case needed. Field remap applies identically to cloze and non-cloze note types.
7. **Note type spanning multiple decks, some shared.** Surfaced in the preview (`OtherUserCount`, `DeckCount`) rather than gated (see §10).
8. **Zero notes.** Every bulk statement's `WHERE note_type_id = $1` matches zero rows — correct no-op, same code path as the notes>0 case, no special-casing needed (this is what let `AppendNoteFieldSlot`'s split disappear along with the old structural/non-structural branch).
9. **Checksum staleness.** Only recomputed when field 0's identity actually changes (`plan.keptOldOrdinal[0] != 0`), not on every field edit — a plain rename or a reorder that leaves field 0 alone costs nothing extra. When it does trigger, it's one `SELECT` + one Go loop + one bulk `UPDATE`, not a per-note round trip. Confirmed low-stakes: `checksum` is never used for Enshu's own dedup (that's `notes.guid`, per `docs/schema.md`), only for `.apkg` export/import round-trip fidelity — a stale value there is a cosmetic gap in a re-export scenario, not a correctness bug, but it's cheap enough to fix that it's included here rather than left to drift.
10. **Known, pre-existing race (not fixed here, flagged for the record):** `LockNoteTypeForOwner` locks the `note_types` row, but `CreateNote` doesn't take any lock on it — a concurrent note creation that read the old fields/templates before this transaction commits, and inserts after, can end up shaped for a note type that no longer exists in that shape. This race already exists today (it's what makes today's `noteCount==0` check itself TOCTOU-able, per the existing comment on `LockNoteTypeForOwner`); #89 doesn't widen or narrow it, and worst case it now surfaces as a `cards.template_id` FK violation (loud 500) rather than silent corruption, for the same reason it does today. Out of scope for this issue; would need `note_types` locked for the duration of note creation too.

## 11. Docs updates

**`docs/schema.md`**, in the same paragraph #138 amended (*"Editing a note's fields must not regenerate its cards..."*): add that #89 extends the same "diff, don't drop-and-recreate" principle one level up — a field remap rewrites every affected note's `fields` array in bulk rather than per-note, and a template remap keeps a surviving card's `template_id` fixed while moving its `ordinal` to track the template's new position (the reason `cards.ordinal` isn't allowed to go stale relative to `templates.ordinal`), with a removed template's cards hard-deleted the same way an #138 note-type change already deletes a dropped-ordinal's card.

**`docs/routes.md`**, the `POST /note-types/{id}/edit` row: replace *"Removing or reordering an existing field or template is refused with 409 while the note type has any notes... Follow-up issue: field/template removal-and-reorder with positional note remap"* with a description of the new behavior — remap applies with confirmation once the note type has notes; free without confirmation otherwise, matching today's non-structural UX.

## Tests

Per CLAUDE.md §10 item 5 (access control — even though the answer here is "unchanged," it needs a test showing that) and the §15 "silently corrupts `user_card_state`/`review_log`" `sev: critical` bucket, which this whole issue is squarely inside.

**`internal/db/cards_test.go`** (new helper needed: `mustField(t, tx, noteTypeID, ordinal, name) pgtype.UUID`, mirroring `mustTemplate`; and a way to seed a note's `fields` — either extend `mustNote` with a `fields []string` param or `UPDATE notes SET fields=$1` inline in the new tests):

- `TestRemapNoteTypeCards_ReorderKeepsCardIdentityAndRepointsOrdinal`: two templates A(ord 0), B(ord 1); one note with `card_a`@ord0/template A and `card_b`@ord1/template B, each with `user_card_state` + `review_log`. Call `RemapNoteTypeCards` with `changed=[{A,1},{B,0}]`. Assert: `card_a`'s id unchanged, ordinal now 1, `template_id` still A; `card_b`'s id unchanged, ordinal now 0, `template_id` still B; both cards' `user_card_state`/`review_log` untouched (same values, keyed by unchanged card id). This is the concrete regression test for the "content identity must not flip on reorder" finding above.
- `TestRemapNoteTypeCards_ThreeWayCyclicReorder`: three templates at ordinals 0,1,2 permuted cyclically (0→1, 1→2, 2→0) — exercises the general-permutation path, not just a pairwise swap, through the stage/finalize mechanism.
- `TestRemapNoteTypeCards_RemovedTemplate_CardsDeletedReviewLogOrphanedTemplateRowGone`: a card backed by template C, with `user_card_state` + `review_log`. Call with `removed=[C]`. Assert the card is gone, `user_card_state` cascaded away, `review_log` survives with `card_id` unchanged (mirrors #138's `TestSyncNoteCards_RemovedOrdinal_ReviewLogSurvivesOrphaned`, at the note-type-bulk level), **and** the `templates` row for C is also gone (proves the RESTRICT-respecting delete-cards-before-delete-template ordering).
- `TestRemapNoteTypeCards_AddedTemplate_CreatesCardsForEveryExistingNote`: two existing notes, no cards yet for the new template. Call with `added=[{newTemplateID, ord}]`. Assert both notes now have a card at that ordinal/template, fresh ids, no `user_card_state` (New for everyone via the missing-row convention #138 already established).
- `TestRemapNoteTypeCards_MultipleNotesRemappedTogether`: 3+ notes on the same note type, each with its own cards, one call covering all of them — correctness-at-scale sanity check. (The "not one round trip per note" property is architectural/reviewed by design, not separately instrumented — this codebase has no query-counting test harness, and building one just for this would be over-engineering per CLAUDE.md's Simplicity First.)
- `TestApplyFieldPlan_ReorderAndRemoval_RemapsNotesFieldsPositionally`: note type with fields Front/Back/Extra; two notes with distinct `["F1","B1","E1"]`/`["F2","B2","E2"]`. Remap to keep Back and Front (swapped) and drop Extra (`fields = [{Back}, {Front}]`). Assert both notes' `fields` become `["B1","F1"]`/`["B2","F2"]`.
- `TestApplyFieldPlan_AddedField_NewNotesGetEmptyString`: append a field; assert existing notes' `fields` gain a trailing `""`.
- `TestApplyFieldPlan_RecomputesChecksumOnlyWhenField0Changes`: reorder that moves field 0 to a different value → checksum changes to match the new field-0 content (`db.ComputeNoteChecksum`); a reorder/rename that leaves field 0's identity alone → checksum unchanged (asserts the "only when needed" gate, not just correctness).

**`internal/http/notetypes_test.go`**:

- Rewrite `TestNoteTypeEdit_StructureLockedWithNotes` (name to something like `TestNoteTypeEdit_StructuralChangeWithNotes_RequiresConfirmation`): submitting a field removal while a note exists, without `confirm_structural_change` → 200 (the preview page), and asserts **no** DB change (field count/note field array unchanged). Resubmitting the same edit with `confirm_structural_change=1` → 303, and asserts the field is gone from both the `fields` table and the note's `fields` array.
- `TestNoteTypeEdit_TemplateReorderWithNotes_PreservesCardIdentityAcrossConfirm`: two templates, a note with two cards + `user_card_state` for one of them; submit a template swap with confirmation; assert both cards' ids are unchanged (looked up before/after), ordinals swapped, `template_id`s unchanged, `user_card_state` untouched.
- `TestNoteTypeEdit_TemplateRemovalWithNotes_DeletesCardsOrphansReviewLog`: analogous end-to-end version of the db-level test, through the HTTP confirm flow.
- `TestNoteTypeEdit_PureAppendOrRenameWithNotes_NoConfirmationNeeded`: a note-type edit that only renames a field / appends a trailing field while notes exist → 303 immediately, no preview page, matching today's existing UX (regression guard against accidentally making the confirmation gate too broad).
- `TestNoteTypeEdit_MalformedFieldPosition_400`: non-numeric `field_position[]` → 400 (mirrors the existing `TestNoteTypeEdit_MalformedFieldID_400` pattern).
- `TestNoteTypeEdit_EmptyFieldsOrTemplates_400`: a submission that blanks every field name (or every template name) → 400, "a note type needs at least one field"/"at least one template is required" — new validation this plan adds (previously ungated on the edit path, unlike the create path).
- **Access-control regression**: extend `TestNoteTypeRoutes_AccessControl` (or add alongside it) — a second user, not the note-type owner, `POST`ing a structural edit (with or without `confirm_structural_change=1`) → 404, same as today; confirms §10's "no new gate, but the existing gate still holds" decision didn't regress.
- **Multiuser scheduling-state regression** (mirrors #138's): owner's note type backs notes on a deck shared with a second user (`can_study` granted); both users study the note's two cards (seeded `user_card_state`/`review_log` for both); owner removes one template with confirmation. Assert for **both** users: the surviving card's `user_card_state` is untouched; the removed card's `user_card_state` is gone for both; the removed card's `review_log` rows for both users still exist, orphaned. This is the concrete #89-scale version of CLAUDE.md §15's "anything that silently corrupts `review_log` or `user_card_state`" bucket, extended from #138's single-note case to a note-type-wide edit.

## Open items flagged rather than silently decided

- **Reorder UI**: a numeric "Position" input (§9) is the simplest no-JS mechanism consistent with this codebase's plain-HTML-forms style. An up/down-button or drag-and-drop UX is nicer but needs either JS or extra round trips to preview an in-progress reorder before the final save; treated as a follow-up polish item, not a v1 blocker, since the issue is scoped to the DB-remap mechanism (`area: db`) rather than UI polish.
- **`DeleteField` bulk vs. loop** (§6): defaulted to a per-id loop (bounded by the note type's own field count, not note count) over adding a fifth new query for a bulk `DeleteFieldByIDs`. Either is fine; the loop is fewer new queries.
- **`sqlc generate` compiling `RemapNoteFields`'s correlated subquery** — flagged in §4 as something to verify at implementation time; good precedent it'll work but not confirmed by actually running the generator.

### Critical files for implementation

- internal/db/notetypes.go
- internal/db/cards.go
- internal/db/queries/notes.sql
- internal/http/notetypes.go
- internal/db/queries/fields.sql
- internal/db/queries/templates.sql
- internal/db/queries/cards.sql
- web/templates/notetype_form.html
