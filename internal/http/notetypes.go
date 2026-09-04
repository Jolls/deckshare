package http

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// noteTypeListRow is a read-only-section row for the notetypes list: the ordinary listing fields
// plus the one-line reason it can't be edited (docs/plans/192-note-type-authority.md).
type noteTypeListRow struct {
	db.ListNoteTypesForUserRow
	Reason string
}

// splitNoteTypesByEdit partitions ListNoteTypesForUser's rows into the two sections
// notetypes.html renders, computing each read-only row's denial reason from the decks that use
// it (one extra query per read-only row -- bounded by how many note types a user has, not worth
// a bulk join).
func splitNoteTypesByEdit(ctx context.Context, q *db.Queries, userID pgtype.UUID, rows []db.ListNoteTypesForUserRow) (editable []db.ListNoteTypesForUserRow, readOnly []noteTypeListRow, err error) {
	for _, nt := range rows {
		if nt.CanEdit.Bool {
			editable = append(editable, nt)
			continue
		}
		decks, derr := q.ListDecksUsingNoteType(ctx, db.ListDecksUsingNoteTypeParams{UserID: userID, NoteTypeID: nt.ID})
		if derr != nil {
			return nil, nil, derr
		}
		readOnly = append(readOnly, noteTypeListRow{ListNoteTypesForUserRow: nt, Reason: noteTypeDenialReason(decks)})
	}
	return editable, readOnly, nil
}

// noteTypeDenialReason explains why a note type is read-only, using only the decks that actually
// block WRITABLE (editable == false) -- a deck the caller can already edit content in is not a
// reason -- and without leaking the name of a blocking deck the caller can't see: a visible
// blocking deck is named, an invisible one is only counted.
func noteTypeDenialReason(decks []db.ListDecksUsingNoteTypeRow) string {
	var visibleNames []string
	hidden := 0
	for _, d := range decks {
		if d.Editable {
			continue
		}
		if d.Visible {
			visibleNames = append(visibleNames, d.Name)
		} else {
			hidden++
		}
	}
	switch {
	case len(visibleNames) == 0:
		return fmt.Sprintf("Also used in %s you don't have access to.", pluralDecks(hidden))
	case hidden == 0:
		return fmt.Sprintf("Also used in %s, which you can't edit content in.", strings.Join(visibleNames, ", "))
	default:
		return fmt.Sprintf("Also used in %s, which you can't edit content in, and %s you don't have access to.",
			strings.Join(visibleNames, ", "), pluralDecks(hidden))
	}
}

func pluralDecks(n int) string {
	if n == 1 {
		return "1 deck"
	}
	return fmt.Sprintf("%d decks", n)
}

func registerNoteTypeRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /note-types", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		q := db.New(store)
		noteTypes, err := q.ListNoteTypesForUser(r.Context(), user.ID)
		if err != nil {
			serverError(w)
			return
		}
		editable, readOnly, err := splitNoteTypesByEdit(r.Context(), q, user.ID, noteTypes)
		if err != nil {
			serverError(w)
			return
		}
		render(w, pages["notetypes"], http.StatusOK, map[string]any{"User": user, "Editable": editable, "ReadOnly": readOnly})
	})))

	mux.Handle("GET /note-types/new", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		render(w, pages["notetype_form"], http.StatusOK, map[string]any{"User": user, "IsNew": true})
	})))

	mux.Handle("POST /note-types", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		css := r.PostForm.Get("css")
		isCloze := r.PostForm.Get("is_cloze") == "on"
		fieldNames := trimmedNonEmpty(r.PostForm["field_name[]"])
		templateNames := r.PostForm["template_name[]"]
		templateQfmts := r.PostForm["qfmt[]"]
		templateAfmts := r.PostForm["afmt[]"]

		if name == "" || len(fieldNames) == 0 {
			http.Error(w, "a note type needs a name and at least one field", http.StatusBadRequest)
			return
		}
		if isCloze && len(templateNames) != 1 {
			http.Error(w, "a cloze note type has exactly one template", http.StatusBadRequest)
			return
		}
		if len(templateNames) == 0 || len(templateNames) != len(templateQfmts) || len(templateNames) != len(templateAfmts) {
			http.Error(w, "at least one template is required", http.StatusBadRequest)
			return
		}

		templates := make([]db.TemplateEdit, 0, len(templateNames))
		for i, n := range templateNames {
			n = strings.TrimSpace(n)
			if n == "" {
				http.Error(w, "every template needs a name", http.StatusBadRequest)
				return
			}
			templates = append(templates, db.TemplateEdit{Name: n, Qfmt: templateQfmts[i], Afmt: templateAfmts[i]})
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		_, err := db.CreateNoteTypeWithFieldsAndTemplates(r.Context(), tx, user.ID, name, css, isCloze, 0, fieldNames, templates)
		if err != nil {
			if db.IsUniqueViolation(err, "note_types_owner_id_name_key") {
				http.Error(w, "you already have a note type with that name", http.StatusConflict)
				return
			}
			serverError(w)
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/note-types", http.StatusSeeOther)
	})))

	mux.Handle("GET /note-types/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		id, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		nt, err := q.GetNoteTypeForRead(r.Context(), db.GetNoteTypeForReadParams{ID: id, UserID: user.ID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}
		fields, err := q.ListFieldsForNoteType(r.Context(), id)
		if err != nil {
			serverError(w)
			return
		}
		templates, err := q.ListTemplatesForNoteType(r.Context(), id)
		if err != nil {
			serverError(w)
			return
		}
		canEdit, err := q.CanEditNoteType(r.Context(), db.CanEditNoteTypeParams{ID: id, UserID: user.ID})
		if err != nil {
			serverError(w)
			return
		}
		render(w, pages["notetype_form"], http.StatusOK, map[string]any{
			"User": user, "NoteType": nt, "Fields": fields, "Templates": templates, "CanEdit": canEdit.Bool,
		})
	})))

	mux.Handle("POST /note-types/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		id, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		if !parseForm(w, r) {
			return
		}

		name := strings.TrimSpace(r.PostForm.Get("name"))
		css := r.PostForm.Get("css")
		if name == "" {
			badRequest(w)
			return
		}

		fields, ok := parseFieldEdits(w, r)
		if !ok {
			return
		}
		if len(fields) == 0 {
			http.Error(w, "a note type needs at least one field", http.StatusBadRequest)
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

		q := db.New(store)
		nt, err := q.LockNoteTypeForEdit(r.Context(), db.LockNoteTypeForEditParams{ID: id, UserID: user.ID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}
		if nt.IsCloze && len(templates) != 1 {
			http.Error(w, "a cloze note type must have exactly one template", http.StatusBadRequest)
			return
		}

		existingFields, err := q.ListFieldsForNoteType(r.Context(), id)
		if err != nil {
			serverError(w)
			return
		}
		existingTemplates, err := q.ListTemplatesForNoteType(r.Context(), id)
		if err != nil {
			serverError(w)
			return
		}

		structural := db.FieldOrderChanged(existingFields, fields) || db.TemplateOrderChanged(existingTemplates, templates)
		noteCount, err := q.CountNotesOfNoteType(r.Context(), id)
		if err != nil {
			serverError(w)
			return
		}

		if structural && noteCount > 0 && r.PostForm.Get("confirm_structural_change") != "1" {
			preview, err := buildStructuralChangePreview(r.Context(), q, user.ID, id, existingFields, fields, existingTemplates, templates, noteCount)
			if err != nil {
				serverError(w)
				return
			}
			render(w, pages["notetype_form"], http.StatusOK, map[string]any{
				"User": user, "NoteType": nt, "Fields": previewFields(fields), "Templates": previewTemplates(templates),
				"ConfirmStructuralChange": true, "Preview": preview,
				"Name": name, "Css": css,
			})
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		err = db.UpdateNoteType(r.Context(), tx, user.ID, id, name, css, 0, fields, templates)
		if err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				notFoundPage(w, pages, user)
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
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/note-types", http.StatusSeeOther)
	})))

	mux.Handle("POST /note-types/{id}/delete", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		id, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		n, err := db.New(store).DeleteNoteType(r.Context(), db.DeleteNoteTypeParams{ID: id, OwnerID: user.ID})
		if err != nil {
			if db.IsForeignKeyViolation(err, "notes_note_type_id_fkey") {
				http.Error(w, "delete or re-type its notes first", http.StatusConflict)
				return
			}
			serverError(w)
			return
		}
		if n == 0 {
			notFoundPage(w, pages, user)
			return
		}
		http.Redirect(w, r, "/note-types", http.StatusSeeOther)
	})))
}

func trimmedNonEmpty(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseFieldEdits parses field_id[]/field_name[]/field_position[] into submission order sorted by
// position (#89's plain-HTML reorder affordance). A blank name (after trim) is today's "remove via
// blank" convention -- the row is dropped, not carried forward as an edit. A stable sort means an
// untouched position value never reorders a row relative to its submission-order siblings.
func parseFieldEdits(w http.ResponseWriter, r *http.Request) ([]db.FieldEdit, bool) {
	ids := r.PostForm["field_id[]"]
	names := r.PostForm["field_name[]"]
	positions := r.PostForm["field_position[]"]
	if len(ids) != len(names) || len(names) != len(positions) {
		badRequest(w)
		return nil, false
	}

	type entry struct {
		edit     db.FieldEdit
		position int
	}
	entries := make([]entry, 0, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var fieldID pgtype.UUID
		if ids[i] != "" {
			if err := fieldID.Scan(ids[i]); err != nil {
				badRequest(w)
				return nil, false
			}
		}
		pos, err := strconv.Atoi(positions[i])
		if err != nil {
			badRequest(w)
			return nil, false
		}
		entries = append(entries, entry{edit: db.FieldEdit{ID: fieldID, Name: name}, position: pos})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].position < entries[j].position })

	out := make([]db.FieldEdit, len(entries))
	for i, e := range entries {
		out[i] = e.edit
	}
	return out, true
}

// parseTemplateEdits is parseFieldEdits's template-side counterpart, over
// template_id[]/template_name[]/qfmt[]/afmt[]/template_position[].
func parseTemplateEdits(w http.ResponseWriter, r *http.Request) ([]db.TemplateEdit, bool) {
	ids := r.PostForm["template_id[]"]
	names := r.PostForm["template_name[]"]
	qfmts := r.PostForm["qfmt[]"]
	afmts := r.PostForm["afmt[]"]
	positions := r.PostForm["template_position[]"]
	if len(ids) != len(names) || len(names) != len(qfmts) || len(names) != len(afmts) || len(names) != len(positions) {
		badRequest(w)
		return nil, false
	}

	type entry struct {
		edit     db.TemplateEdit
		position int
	}
	entries := make([]entry, 0, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var templateID pgtype.UUID
		if ids[i] != "" {
			if err := templateID.Scan(ids[i]); err != nil {
				badRequest(w)
				return nil, false
			}
		}
		pos, err := strconv.Atoi(positions[i])
		if err != nil {
			badRequest(w)
			return nil, false
		}
		entries = append(entries, entry{
			edit:     db.TemplateEdit{ID: templateID, Name: name, Qfmt: qfmts[i], Afmt: afmts[i]},
			position: pos,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].position < entries[j].position })

	out := make([]db.TemplateEdit, len(entries))
	for i, e := range entries {
		out[i] = e.edit
	}
	return out, true
}

// previewFieldRow/previewTemplateRow give the confirmation page (#89) the same {{.ID}}/{{.Name}}/
// {{.Qfmt}}/{{.Afmt}}/{{.Ordinal}} shape web/templates/notetype_form.html already renders for
// db.Field/db.Template on the ordinary edit page, but built from the submitted, already-sorted
// []db.FieldEdit/[]db.TemplateEdit -- .Ordinal here is really "submitted position," not a stored
// row's ordinal, since these fields/templates have not been written yet.
type previewFieldRow struct {
	ID      pgtype.UUID
	Name    string
	Ordinal int32
}

type previewTemplateRow struct {
	ID      pgtype.UUID
	Name    string
	Qfmt    string
	Afmt    string
	Ordinal int32
}

func previewFields(fields []db.FieldEdit) []previewFieldRow {
	out := make([]previewFieldRow, len(fields))
	for i, f := range fields {
		out[i] = previewFieldRow{ID: f.ID, Name: f.Name, Ordinal: int32(i)}
	}
	return out
}

func previewTemplates(templates []db.TemplateEdit) []previewTemplateRow {
	out := make([]previewTemplateRow, len(templates))
	for i, t := range templates {
		out[i] = previewTemplateRow{ID: t.ID, Name: t.Name, Qfmt: t.Qfmt, Afmt: t.Afmt, Ordinal: int32(i)}
	}
	return out
}

// structuralChangePreview is the #89 confirmation page's data: what a structural field/template
// change will discard or add, computed once before the user confirms. AffectedDeckNames is safe
// to render unconditionally: reaching this page requires WRITABLE, which implies can_view on
// every deck holding a note that uses this note type (docs/plans/192-note-type-authority.md).
type structuralChangePreview struct {
	NoteCount            int64
	AffectedDeckNames    []string
	OtherUserCount       int64
	RemovedFieldNames    []string
	RemovedTemplateNames []string
	RemovedCardCount     int64
	AddedTemplateCount   int
	AddedCardEstimate    int64
}

func buildStructuralChangePreview(
	ctx context.Context, q *db.Queries, userID, noteTypeID pgtype.UUID,
	existingFields []db.Field, fields []db.FieldEdit,
	existingTemplates []db.Template, templates []db.TemplateEdit,
	noteCount int64,
) (structuralChangePreview, error) {
	otherUserCount, err := q.NoteTypeOtherUserCount(ctx, db.NoteTypeOtherUserCountParams{NoteTypeID: noteTypeID, OwnerID: userID})
	if err != nil {
		return structuralChangePreview{}, err
	}
	decksUsingNT, err := q.ListDecksUsingNoteType(ctx, db.ListDecksUsingNoteTypeParams{UserID: userID, NoteTypeID: noteTypeID})
	if err != nil {
		return structuralChangePreview{}, err
	}
	deckNames := make([]string, len(decksUsingNT))
	for i, d := range decksUsingNT {
		deckNames[i] = d.Name
	}

	submittedFieldIDs := make(map[pgtype.UUID]bool, len(fields))
	for _, f := range fields {
		if f.ID.Valid {
			submittedFieldIDs[f.ID] = true
		}
	}
	var removedFieldNames []string
	for _, f := range existingFields {
		if !submittedFieldIDs[f.ID] {
			removedFieldNames = append(removedFieldNames, f.Name)
		}
	}

	submittedTemplateIDs := make(map[pgtype.UUID]bool, len(templates))
	for _, t := range templates {
		if t.ID.Valid {
			submittedTemplateIDs[t.ID] = true
		}
	}
	var removedTemplateNames []string
	var removedTemplateIDs []pgtype.UUID
	for _, t := range existingTemplates {
		if !submittedTemplateIDs[t.ID] {
			removedTemplateNames = append(removedTemplateNames, t.Name)
			removedTemplateIDs = append(removedTemplateIDs, t.ID)
		}
	}

	var removedCardCount int64
	if len(removedTemplateIDs) > 0 {
		removedCardCount, err = q.CountCardsForTemplates(ctx, removedTemplateIDs)
		if err != nil {
			return structuralChangePreview{}, err
		}
	}

	addedTemplateCount := 0
	for _, t := range templates {
		if !t.ID.Valid {
			addedTemplateCount++
		}
	}

	return structuralChangePreview{
		NoteCount:            noteCount,
		AffectedDeckNames:    deckNames,
		OtherUserCount:       otherUserCount,
		RemovedFieldNames:    removedFieldNames,
		RemovedTemplateNames: removedTemplateNames,
		RemovedCardCount:     removedCardCount,
		AddedTemplateCount:   addedTemplateCount,
		AddedCardEstimate:    int64(addedTemplateCount) * noteCount,
	}, nil
}
