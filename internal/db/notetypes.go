package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrNoteTypeStructureLocked is returned when a note-type edit tries to remove or reorder a
// field or template while the note type has at least one note. notes.fields is a positional
// jsonb array indexed by fields.ordinal, so a removal/reorder is a data migration over every
// note of the type; a template removal additionally deletes cards, discarding user_card_state.
// Neither is built (#54 §0.5) -- only append, rename, and non-structural edits are allowed once
// a note type has notes.
var ErrNoteTypeStructureLocked = errors.New("db: cannot remove or reorder fields/templates while the note type has notes")

// ErrClozeNoteTypeSingleTemplate is returned when an edit would leave a cloze note type with a
// template count other than one. desiredCards's cloze branch (internal/http/notes.go) only ever
// addresses the note type's first template, so a second template on a cloze note type creates
// cards that every subsequent SyncNoteCards on that note treats as undesired and deletes --
// discarding user_card_state/review_log. Only the create path checked this; the edit path must
// too.
var ErrClozeNoteTypeSingleTemplate = errors.New("db: a cloze note type must have exactly one template")

// ErrFieldNotFound and ErrTemplateNotFound are returned when an edit submission's field_id[]/
// template_id[] does not name an existing field/template of this note type (forged, stale, or
// copied from a different note type). RenameField/UpdateTemplate scope their WHERE clause to
// note_type_id, so a mismatched id matches zero rows and must not be silently treated as success.
var ErrFieldNotFound = errors.New("db: field id does not belong to this note type")
var ErrTemplateNotFound = errors.New("db: template id does not belong to this note type")

// FieldEdit is one field row in a note-type edit submission. ID.Valid == true renames an
// existing field; ID.Valid == false appends a new one.
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

// UpdateNoteType applies a note-type edit: name/css/sort_field_idx, renames of existing
// fields/templates, and appends of new ones. A submission that removes or reorders an existing
// field or template is rejected with ErrNoteTypeStructureLocked while the note type has notes
// (§0.5); with zero notes it is applied freely by replacing the full set.
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
	noteCount, err := q.CountNotesOfNoteType(ctx, noteTypeID)
	if err != nil {
		return err
	}

	existingFields, err := q.ListFieldsForNoteType(ctx, noteTypeID)
	if err != nil {
		return err
	}
	existingTemplates, err := q.ListTemplatesForNoteType(ctx, noteTypeID)
	if err != nil {
		return err
	}

	fieldsStructural := fieldOrderChanged(existingFields, fields)
	templatesStructural := templateOrderChanged(existingTemplates, templates)

	if (fieldsStructural || templatesStructural) && noteCount > 0 {
		return ErrNoteTypeStructureLocked
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

	if fieldsStructural {
		if _, err := q.DeleteFieldsForNoteType(ctx, noteTypeID); err != nil {
			return err
		}
		for i, f := range fields {
			if _, err := q.CreateField(ctx, CreateFieldParams{NoteTypeID: noteTypeID, Ordinal: int32(i), Name: f.Name}); err != nil {
				return err
			}
		}
	} else {
		nextFieldOrdinal := int32(len(existingFields))
		for _, f := range fields {
			if f.ID.Valid {
				n, err := q.RenameField(ctx, RenameFieldParams{Name: f.Name, ID: f.ID, NoteTypeID: noteTypeID})
				if err != nil {
					return err
				}
				if n == 0 {
					return ErrFieldNotFound
				}
				continue
			}
			if _, err := q.CreateField(ctx, CreateFieldParams{NoteTypeID: noteTypeID, Ordinal: nextFieldOrdinal, Name: f.Name}); err != nil {
				return err
			}
			if _, err := q.AppendNoteFieldSlot(ctx, noteTypeID); err != nil {
				return err
			}
			nextFieldOrdinal++
		}
	}

	if templatesStructural {
		if _, err := q.DeleteTemplatesForNoteType(ctx, noteTypeID); err != nil {
			return err
		}
		for i, t := range templates {
			if _, err := q.CreateTemplate(ctx, CreateTemplateParams{NoteTypeID: noteTypeID, Ordinal: int32(i), Name: t.Name, Qfmt: t.Qfmt, Afmt: t.Afmt}); err != nil {
				return err
			}
		}
	} else {
		nextOrdinal := int32(len(existingTemplates))
		for _, t := range templates {
			if t.ID.Valid {
				n, err := q.UpdateTemplate(ctx, UpdateTemplateParams{Name: t.Name, Qfmt: t.Qfmt, Afmt: t.Afmt, ID: t.ID, NoteTypeID: noteTypeID})
				if err != nil {
					return err
				}
				if n == 0 {
					return ErrTemplateNotFound
				}
				continue
			}
			created, err := q.CreateTemplate(ctx, CreateTemplateParams{NoteTypeID: noteTypeID, Ordinal: nextOrdinal, Name: t.Name, Qfmt: t.Qfmt, Afmt: t.Afmt})
			if err != nil {
				return err
			}
			if _, err := q.CreateCardsForNewTemplate(ctx, CreateCardsForNewTemplateParams{
				TemplateID: created.ID,
				Ordinal:    nextOrdinal,
				NoteTypeID: noteTypeID,
			}); err != nil {
				return err
			}
			nextOrdinal++
		}
	}

	return nil
}

// fieldOrderChanged reports whether the submitted existing-field ids (in submitted order)
// differ from the stored order -- i.e. a removal or reorder was attempted. A new (ID-less) entry
// appearing before an existing one is also structural: the non-structural append path always
// assigns new ordinals at the end, so an insert-in-the-middle submission would otherwise be
// silently misclassified as a trailing append and land in the wrong position.
func fieldOrderChanged(existing []Field, submitted []FieldEdit) bool {
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

func templateOrderChanged(existing []Template, submitted []TemplateEdit) bool {
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
