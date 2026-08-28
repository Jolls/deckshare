package http

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	noterender "github.com/Jolls/enshu/internal/render"
)

var errNoClozeMarkers = errors.New("a cloze note must contain at least one {{c1::...}} marker")

func registerNoteRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /decks/{deckId}/notes/new", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "deckId")
		if !ok {
			notFound(w)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForContentEdit(r.Context(), db.GetDeckForContentEditParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErr(w, err) {
			return
		}

		noteTypeIDStr := r.URL.Query().Get("note_type_id")
		if noteTypeIDStr == "" {
			noteTypes, err := q.ListNoteTypesForOwner(r.Context(), user.ID)
			if err != nil {
				serverError(w)
				return
			}
			render(w, pages["note_form"], http.StatusOK, map[string]any{
				"User": user, "Deck": deck, "NoteTypes": noteTypes, "PickNoteType": true,
			})
			return
		}
		var noteTypeID pgtype.UUID
		if err := noteTypeID.Scan(noteTypeIDStr); err != nil {
			notFound(w)
			return
		}
		nt, err := q.GetNoteTypeForOwner(r.Context(), db.GetNoteTypeForOwnerParams{ID: noteTypeID, OwnerID: user.ID})
		if handleQueryErr(w, err) {
			return
		}
		fields, err := q.ListFieldsForNoteType(r.Context(), noteTypeID)
		if err != nil {
			serverError(w)
			return
		}
		render(w, pages["note_form"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "NoteType": nt, "Fields": fields, "IsNew": true,
		})
	})))

	mux.Handle("POST /decks/{deckId}/notes", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "deckId")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}
		var noteTypeID pgtype.UUID
		if err := noteTypeID.Scan(r.PostForm.Get("note_type_id")); err != nil {
			badRequest(w)
			return
		}

		q := db.New(store)
		nt, err := q.GetNoteTypeForOwner(r.Context(), db.GetNoteTypeForOwnerParams{ID: noteTypeID, OwnerID: user.ID})
		if handleQueryErr(w, err) {
			return
		}
		templates, err := q.ListTemplatesForNoteType(r.Context(), noteTypeID)
		if err != nil {
			serverError(w)
			return
		}
		fields, err := q.ListFieldsForNoteType(r.Context(), noteTypeID)
		if err != nil {
			serverError(w)
			return
		}

		fieldValues := r.PostForm["field[]"]
		fieldsJSON, checksum, err := validateNoteFields(fieldValues, len(fields))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		desired, err := desiredCards(nt, templates, fieldValues)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tags := parseTags(r.PostForm.Get("tags"))
		guid, err := randomGuid()
		if err != nil {
			serverError(w)
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		_, err = db.CreateNoteWithCards(r.Context(), tx, db.CreateNoteParams{
			Guid:       guid,
			Fields:     fieldsJSON,
			Tags:       tags,
			Checksum:   checksum,
			UserID:     user.ID,
			NoteTypeID: noteTypeID,
			DeckID:     deckID,
		}, desired)
		if err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				notFound(w)
			case errors.Is(err, db.ErrNoCards):
				http.Error(w, "a cloze note must keep at least one cloze marker", http.StatusBadRequest)
			default:
				serverError(w)
			}
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))

	mux.Handle("GET /notes/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		noteID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		q := db.New(store)
		note, err := q.GetNoteForContentEdit(r.Context(), db.GetNoteForContentEditParams{UserID: user.ID, NoteID: noteID})
		if handleQueryErr(w, err) {
			return
		}
		nt, err := q.GetNoteType(r.Context(), note.NoteTypeID)
		if err != nil {
			serverError(w)
			return
		}
		fields, err := q.ListFieldsForNoteType(r.Context(), note.NoteTypeID)
		if err != nil {
			serverError(w)
			return
		}
		var fieldValues []string
		if err := json.Unmarshal(note.Fields, &fieldValues); err != nil {
			serverError(w)
			return
		}
		decks, err := q.ListDecksForUser(r.Context(), user.ID)
		if err != nil {
			serverError(w)
			return
		}
		render(w, pages["note_form"], http.StatusOK, map[string]any{
			"User": user, "Note": note, "NoteType": nt, "Fields": fields, "FieldValues": fieldValues,
			"Decks": decks,
		})
	})))

	mux.Handle("POST /notes/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		noteID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}

		q := db.New(store)
		note, err := q.GetNoteForContentEdit(r.Context(), db.GetNoteForContentEditParams{UserID: user.ID, NoteID: noteID})
		if handleQueryErr(w, err) {
			return
		}
		nt, err := q.GetNoteType(r.Context(), note.NoteTypeID)
		if err != nil {
			serverError(w)
			return
		}
		templates, err := q.ListTemplatesForNoteType(r.Context(), note.NoteTypeID)
		if err != nil {
			serverError(w)
			return
		}
		fields, err := q.ListFieldsForNoteType(r.Context(), note.NoteTypeID)
		if err != nil {
			serverError(w)
			return
		}

		fieldValues := r.PostForm["field[]"]
		fieldsJSON, checksum, err := validateNoteFields(fieldValues, len(fields))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		desired, err := desiredCards(nt, templates, fieldValues)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tags := parseTags(r.PostForm.Get("tags"))

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		err = db.UpdateNoteWithCards(r.Context(), tx, user.ID, noteID, fieldsJSON, tags, checksum, desired)
		if err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				notFound(w)
			case errors.Is(err, db.ErrNoCards):
				http.Error(w, "a cloze note must keep at least one cloze marker", http.StatusBadRequest)
			default:
				serverError(w)
			}
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+note.DeckID.String(), http.StatusSeeOther)
	})))

	mux.Handle("POST /notes/{id}/delete", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		noteID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		q := db.New(store)
		note, err := q.GetNoteForContentEdit(r.Context(), db.GetNoteForContentEditParams{UserID: user.ID, NoteID: noteID})
		if handleQueryErr(w, err) {
			return
		}
		deckID := note.DeckID

		n, err := q.DeleteNote(r.Context(), db.DeleteNoteParams{NoteID: noteID, UserID: user.ID})
		if err != nil {
			serverError(w)
			return
		}
		if n == 0 {
			notFound(w)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))

	mux.Handle("POST /notes/{id}/move", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		noteID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}
		var targetDeckID pgtype.UUID
		if err := targetDeckID.Scan(r.PostForm.Get("target_deck_id")); err != nil {
			badRequest(w)
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		if handleQueryErr(w, db.MoveNote(r.Context(), tx, user.ID, noteID, targetDeckID)) {
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+targetDeckID.String(), http.StatusSeeOther)
	})))
}

// desiredCards is the one place that decides what cards a note has (architecture.md §8's card
// generation shape): one card per template for a non-cloze note type, N cards by distinct cloze
// ordinal for a cloze note type.
func desiredCards(nt db.NoteType, templates []db.Template, fieldValues []string) ([]db.DesiredCard, error) {
	if !nt.IsCloze {
		desired := make([]db.DesiredCard, 0, len(templates))
		for _, t := range templates {
			desired = append(desired, db.DesiredCard{Ordinal: t.Ordinal, TemplateID: t.ID})
		}
		return desired, nil
	}
	if len(templates) == 0 {
		return nil, errors.New("cloze note type has no template")
	}
	clozeNumbers := noterender.ClozeOrdinals(fieldValues)
	if len(clozeNumbers) == 0 {
		return nil, errNoClozeMarkers
	}
	desired := make([]db.DesiredCard, 0, len(clozeNumbers))
	for _, n := range clozeNumbers {
		desired = append(desired, db.DesiredCard{Ordinal: n - 1, TemplateID: templates[0].ID})
	}
	return desired, nil
}

const maxFieldBytes = 64 * 1024

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// validateNoteFields marshals field values to the notes.fields jsonb shape and computes the
// Anki-style checksum (truncated SHA-1 of the first field with HTML tags stripped). wantCount is
// the note type's field count: notes.fields is a positional array indexed by fields.ordinal
// (docs/schema.md), so a submission with the wrong number of values -- the HTML form always
// renders exactly one per field, but nothing stops a direct POST -- would otherwise write a
// misaligned array.
func validateNoteFields(fieldValues []string, wantCount int) (fieldsJSON []byte, checksum int64, err error) {
	if len(fieldValues) != wantCount {
		return nil, 0, fmt.Errorf("expected %d fields, got %d", wantCount, len(fieldValues))
	}
	if len(fieldValues) == 0 || strings.TrimSpace(fieldValues[0]) == "" {
		return nil, 0, errors.New("the first field must not be empty")
	}
	for _, v := range fieldValues {
		if len(v) > maxFieldBytes {
			return nil, 0, errors.New("a field is too large")
		}
	}
	fieldsJSON, err = json.Marshal(fieldValues)
	if err != nil {
		return nil, 0, err
	}
	stripped := htmlTagRe.ReplaceAllString(fieldValues[0], "")
	sum := sha1.Sum([]byte(stripped)) //nolint:gosec // Anki csum compatibility, not a security use of SHA-1
	checksum = int64(binary.BigEndian.Uint32(sum[:4]))
	return fieldsJSON, checksum, nil
}

func parseTags(raw string) []string {
	fields := strings.Fields(raw)
	seen := map[string]struct{}{}
	tags := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		tags = append(tags, f)
	}
	return tags
}

func randomGuid() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
