package http

import (
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
)

func registerNoteTypeRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /note-types", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		noteTypes, err := db.New(store).ListNoteTypesForOwner(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		render(w, pages["notetypes"], http.StatusOK, map[string]any{"User": user, "NoteTypes": noteTypes})
	})))

	mux.Handle("GET /note-types/new", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		render(w, pages["notetype_form"], http.StatusOK, map[string]any{"User": user, "IsNew": true})
	})))

	mux.Handle("POST /note-types", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
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

		tx, err := store.Begin(r.Context())
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		_, err = db.CreateNoteTypeWithFieldsAndTemplates(r.Context(), tx, user.ID, name, css, isCloze, 0, fieldNames, templates)
		if err != nil {
			if db.IsUniqueViolation(err, "note_types_owner_id_name_key") {
				http.Error(w, "you already have a note type with that name", http.StatusConflict)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/note-types", http.StatusSeeOther)
	})))

	mux.Handle("GET /note-types/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		id, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		q := db.New(store)
		nt, err := q.GetNoteTypeForOwner(r.Context(), db.GetNoteTypeForOwnerParams{ID: id, OwnerID: user.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFound(w)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		fields, err := q.ListFieldsForNoteType(r.Context(), id)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		templates, err := q.ListTemplatesForNoteType(r.Context(), id)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		render(w, pages["notetype_form"], http.StatusOK, map[string]any{
			"User": user, "NoteType": nt, "Fields": fields, "Templates": templates,
		})
	})))

	mux.Handle("POST /note-types/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		id, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		css := r.PostForm.Get("css")
		if name == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		fieldIDs := r.PostForm["field_id[]"]
		fieldNames := r.PostForm["field_name[]"]
		if len(fieldIDs) != len(fieldNames) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fields := make([]db.FieldEdit, 0, len(fieldNames))
		for i, fname := range fieldNames {
			fname = strings.TrimSpace(fname)
			if fname == "" {
				continue
			}
			var fieldID pgtype.UUID
			if fieldIDs[i] != "" {
				if err := fieldID.Scan(fieldIDs[i]); err != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
			}
			fields = append(fields, db.FieldEdit{ID: fieldID, Name: fname})
		}

		templateIDs := r.PostForm["template_id[]"]
		templateNames := r.PostForm["template_name[]"]
		templateQfmts := r.PostForm["qfmt[]"]
		templateAfmts := r.PostForm["afmt[]"]
		if len(templateIDs) != len(templateNames) || len(templateNames) != len(templateQfmts) || len(templateNames) != len(templateAfmts) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		templates := make([]db.TemplateEdit, 0, len(templateNames))
		for i, tname := range templateNames {
			tname = strings.TrimSpace(tname)
			if tname == "" {
				continue
			}
			var templateID pgtype.UUID
			if templateIDs[i] != "" {
				if err := templateID.Scan(templateIDs[i]); err != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
			}
			templates = append(templates, db.TemplateEdit{ID: templateID, Name: tname, Qfmt: templateQfmts[i], Afmt: templateAfmts[i]})
		}

		tx, err := store.Begin(r.Context())
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		err = db.UpdateNoteType(r.Context(), tx, user.ID, id, name, css, 0, fields, templates)
		if err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				notFound(w)
			case errors.Is(err, db.ErrNoteTypeStructureLocked):
				http.Error(w, "remove or reorder fields/templates only while the note type has no notes", http.StatusConflict)
			case errors.Is(err, db.ErrClozeNoteTypeSingleTemplate):
				http.Error(w, "a cloze note type must have exactly one template", http.StatusBadRequest)
			case errors.Is(err, db.ErrFieldNotFound), errors.Is(err, db.ErrTemplateNotFound):
				http.Error(w, "bad request", http.StatusBadRequest)
			case db.IsUniqueViolation(err, "note_types_owner_id_name_key"):
				http.Error(w, "you already have a note type with that name", http.StatusConflict)
			default:
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/note-types", http.StatusSeeOther)
	})))

	mux.Handle("POST /note-types/{id}/delete", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		id, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		n, err := db.New(store).DeleteNoteType(r.Context(), db.DeleteNoteTypeParams{ID: id, OwnerID: user.ID})
		if err != nil {
			if db.IsForeignKeyViolation(err, "notes_note_type_id_fkey") {
				http.Error(w, "delete or re-type its notes first", http.StatusConflict)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if n == 0 {
			notFound(w)
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
