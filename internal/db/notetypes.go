package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrClozeNoteTypeSingleTemplate is returned when an edit would leave a cloze note type with a
// template count other than one. desiredCards's cloze branch (internal/http/notes.go) only ever
// addresses the note type's first template, so a second template on a cloze note type creates
// cards that every subsequent SyncNoteCards on that note treats as undesired and deletes --
// discarding user_card_state/review_log. Only the create path checked this; the edit path must
// too.
var ErrClozeNoteTypeSingleTemplate = errors.New("db: a cloze note type must have exactly one template")

// ErrFieldNotFound and ErrTemplateNotFound are returned when an edit submission's field_id[]/
// template_id[] does not name an existing field/template of this note type (forged, stale, or
// copied from a different note type). UpdateFieldNameAndOrdinal/UpdateTemplateContentAndOrdinal
// scope their WHERE clause to note_type_id, so a mismatched id matches zero rows and must not be
// silently treated as success.
var ErrFieldNotFound = errors.New("db: field id does not belong to this note type")
var ErrTemplateNotFound = errors.New("db: template id does not belong to this note type")

// FieldEdit is one field row in a note-type edit submission. ID.Valid == true renames/repositions
// an existing field; ID.Valid == false creates a new one at the submitted position.
type FieldEdit struct {
	ID   pgtype.UUID
	Name string
}

// TemplateEdit is one template row in a note-type edit submission, same ID convention as
// FieldEdit.
type TemplateEdit struct {
	ID         pgtype.UUID
	Name       string
	Qfmt, Afmt string
}

// CreateNoteTypeWithFieldsAndTemplates creates a note type and its fields/templates in one
// transaction. Must be called inside a transaction it does not own; the caller commits.
func CreateNoteTypeWithFieldsAndTemplates(ctx context.Context, tx pgx.Tx, ownerID pgtype.UUID, name, css string, isCloze bool, sortFieldIdx int32, fieldNames []string, templates []TemplateEdit) (NoteType, error) {
	if isCloze && len(templates) != 1 {
		return NoteType{}, ErrClozeNoteTypeSingleTemplate
	}
	q := New(tx)
	nt, err := q.CreateNoteType(ctx, CreateNoteTypeParams{
		OwnerID:      ownerID,
		Name:         name,
		Css:          css,
		IsCloze:      isCloze,
		SortFieldIdx: sortFieldIdx,
	})
	if err != nil {
		return NoteType{}, err
	}
	for i, fieldName := range fieldNames {
		if _, err := q.CreateField(ctx, CreateFieldParams{
			NoteTypeID: nt.ID,
			Ordinal:    int32(i),
			Name:       fieldName,
		}); err != nil {
			return NoteType{}, err
		}
	}
	for i, t := range templates {
		if _, err := q.CreateTemplate(ctx, CreateTemplateParams{
			NoteTypeID: nt.ID,
			Ordinal:    int32(i),
			Name:       t.Name,
			Qfmt:       t.Qfmt,
			Afmt:       t.Afmt,
		}); err != nil {
			return NoteType{}, err
		}
	}
	return nt, nil
}

// UpdateNoteType applies a note-type edit: name/css/sort_field_idx, plus a field/template remap
// (#89) covering rename, reorder, removal, and addition, all in bulk regardless of how many notes
// the note type has. A field remap rewrites every affected note's fields jsonb array positionally
// (RemapNoteFields); a template remap reconciles cards via RemapNoteTypeCards, keeping a
// surviving card's identity (id, template_id) fixed while moving its ordinal to track the
// template's new position, and hard-deleting cards backed by a removed template.
//
// Must be called inside a transaction it does not own; the caller commits. Returns
// pgx.ErrNoRows if the note type is absent or not owned by ownerID.
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
		Name:         name,
		Css:          css,
		SortFieldIdx: sortFieldIdx,
		ID:           noteTypeID,
		OwnerID:      ownerID,
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

// FieldOrderChanged reports whether the submitted existing-field ids (in submitted order) differ
// from the stored order -- i.e. a removal or reorder was submitted. A new (ID-less) entry
// appearing before an existing one is also structural: it lands at the submitted position (#89),
// not necessarily the end, so it can shift every existing field after it.
func FieldOrderChanged(existing []Field, submitted []FieldEdit) bool {
	var submittedExisting []pgtype.UUID
	sawNew := false
	for _, f := range submitted {
		if !f.ID.Valid {
			sawNew = true
			continue
		}
		if sawNew {
			return true
		}
		submittedExisting = append(submittedExisting, f.ID)
	}
	if len(submittedExisting) != len(existing) {
		return true
	}
	for i, f := range existing {
		if submittedExisting[i] != f.ID {
			return true
		}
	}
	return false
}

// TemplateOrderChanged is FieldOrderChanged's template-side counterpart.
func TemplateOrderChanged(existing []Template, submitted []TemplateEdit) bool {
	var submittedExisting []pgtype.UUID
	sawNew := false
	for _, t := range submitted {
		if !t.ID.Valid {
			sawNew = true
			continue
		}
		if sawNew {
			return true
		}
		submittedExisting = append(submittedExisting, t.ID)
	}
	if len(submittedExisting) != len(existing) {
		return true
	}
	for i, t := range existing {
		if submittedExisting[i] != t.ID {
			return true
		}
	}
	return false
}

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
			return fieldPlan{}, ErrFieldNotFound
		}
		// A repeated id (forged/duplicated submission) would otherwise let two submitted rows
		// resolve to the same underlying field, assigning it two different final ordinals and
		// silently dropping one from the fields table while RemapNoteFields still reads its old
		// value twice.
		if seen[f.ID] {
			return fieldPlan{}, ErrFieldNotFound
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

// fieldPlanIsIdentity reports whether a plan changes nothing about field ordinals or membership --
// every submitted field is kept at its existing ordinal, nothing removed, nothing added. True for
// a pure rename (or a no-op resubmission).
func fieldPlanIsIdentity(plan fieldPlan) bool {
	if len(plan.removedIDs) != 0 {
		return false
	}
	for i, old := range plan.keptOldOrdinal {
		if old != int32(i) {
			return false
		}
	}
	return true
}

// templatePlan is the same shape as fieldPlan; a separate type only for readability at call sites.
type templatePlan struct {
	keptOldOrdinal []int32
	removedIDs     []pgtype.UUID
}

func planTemplates(existing []Template, submitted []TemplateEdit) (templatePlan, error) {
	existingByID := make(map[pgtype.UUID]Template, len(existing))
	for _, t := range existing {
		existingByID[t.ID] = t
	}
	seen := make(map[pgtype.UUID]bool, len(submitted))
	plan := templatePlan{keptOldOrdinal: make([]int32, len(submitted))}
	for i, t := range submitted {
		if !t.ID.Valid {
			plan.keptOldOrdinal[i] = -1
			continue
		}
		ex, ok := existingByID[t.ID]
		if !ok {
			return templatePlan{}, ErrTemplateNotFound
		}
		// See planFields's identical check: a repeated id must not resolve to two different
		// final ordinals for the same underlying template.
		if seen[t.ID] {
			return templatePlan{}, ErrTemplateNotFound
		}
		plan.keptOldOrdinal[i] = ex.Ordinal
		seen[t.ID] = true
	}
	for _, ex := range existing {
		if !seen[ex.ID] {
			plan.removedIDs = append(plan.removedIDs, ex.ID)
		}
	}
	return plan, nil
}

// applyFieldPlan rewrites the fields table rows to match the submission, then remaps every
// affected note's fields jsonb array in bulk (RemapNoteFields), and -- only when field 0's
// identity actually changed -- recomputes notes.checksum in bulk (one SELECT, one Go loop, one
// bulk UPDATE, never a per-note round trip).
func applyFieldPlan(ctx context.Context, q *Queries, noteTypeID pgtype.UUID, fields []FieldEdit, plan fieldPlan) error {
	// A per-id loop, not a bulk delete: removed-field count is bounded by the note type's own
	// field count (small), so a bulk DeleteFieldByIDs would be a fifth new query for no real gain
	// -- the hot/many-rows path is RemapNoteFields below, which is already a single statement.
	for _, id := range plan.removedIDs {
		if _, err := q.DeleteField(ctx, DeleteFieldParams{ID: id, NoteTypeID: noteTypeID}); err != nil {
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

	// A pure rename (or a no-op resubmission) leaves every field at its existing ordinal, with
	// nothing removed or added -- skip the bulk remap entirely rather than rewriting every note's
	// fields column (and bumping modified_at) to its own unchanged value.
	if !fieldPlanIsIdentity(plan) {
		if _, err := q.RemapNoteFields(ctx, RemapNoteFieldsParams{NoteTypeID: noteTypeID, OldOrdinals: plan.keptOldOrdinal}); err != nil {
			return err
		}
	}

	if len(fields) > 0 && plan.keptOldOrdinal[0] != 0 {
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

// applyTemplatePlan rewrites the templates table rows to match the submission, then reconciles
// cards via RemapNoteTypeCards for whichever kept templates actually moved ordinal, whichever
// were removed, and whichever were newly added.
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
