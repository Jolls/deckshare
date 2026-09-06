package http

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	noterender "github.com/Jolls/deckshare/internal/render"
)

var errNoClozeMarkers = errors.New("a cloze note must contain at least one {{c1::...}} marker")

// notesPageSize is the deck-detail notes list's page size (#90). One page comfortably covers the
// vast majority of decks; #238's 500-card classroom deck is the case pagination exists for.
const notesPageSize = 200

// noteCursor is the opaque keyset position over ListNotesInDeck's (sort_key, id) teaching order.
// AtStart, not a sentinel value in sortKey/id, marks the beginning of the list (mirrors
// review.Cursor's own AtStart field, internal/review/types.go) -- sortKey/id are meaningless
// when AtStart is true, so there is no numeric range to reason about being "below every real
// value" and no risk of a real row ever colliding with the start-of-list position.
type noteCursor struct {
	atStart bool
	sortKey int64
	id      pgtype.UUID
}

var noteCursorAtStart = noteCursor{atStart: true}

// encodeNoteCursor renders c as an opaque string safe to round-trip through a URL query
// parameter. The start-of-list cursor encodes as "" so the first page needs no query param.
func encodeNoteCursor(c noteCursor) string {
	if c.atStart {
		return ""
	}
	raw := strconv.FormatInt(c.sortKey, 10) + ":" + c.id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeNoteCursor parses a string produced by encodeNoteCursor. "" decodes to the start-of-list
// cursor. ok is false for a malformed cursor -- callers should answer 400 rather than guess.
func decodeNoteCursor(s string) (c noteCursor, ok bool) {
	if s == "" {
		return noteCursorAtStart, true
	}
	rawBytes, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return noteCursor{}, false
	}
	sortKeyPart, idPart, found := strings.Cut(string(rawBytes), ":")
	if !found {
		return noteCursor{}, false
	}
	sortKey, err := strconv.ParseInt(sortKeyPart, 10, 64)
	if err != nil {
		return noteCursor{}, false
	}
	var id pgtype.UUID
	if err := id.Scan(idPart); err != nil {
		return noteCursor{}, false
	}
	return noteCursor{sortKey: sortKey, id: id}, true
}

func registerNoteRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /decks/{deckId}/notes/new", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "deckId")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForContentEdit(r.Context(), db.GetDeckForContentEditParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		noteTypeIDStr := r.URL.Query().Get("note_type_id")
		if noteTypeIDStr == "" {
			noteTypes, err := q.ListNoteTypesForNoteForm(r.Context(), db.ListNoteTypesForNoteFormParams{DeckID: deckID, UserID: user.ID})
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
			notFoundPage(w, pages, user)
			return
		}
		nt, err := q.GetNoteTypeForRead(r.Context(), db.GetNoteTypeForReadParams{ID: noteTypeID, UserID: user.ID})
		if handleQueryErrPage(w, pages, user, err) {
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
			notFoundPage(w, pages, user)
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
		nt, err := q.GetNoteTypeForRead(r.Context(), db.GetNoteTypeForReadParams{ID: noteTypeID, UserID: user.ID})
		if handleQueryErrPage(w, pages, user, err) {
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
				notFoundPage(w, pages, user)
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
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		note, err := q.GetNoteForContentEdit(r.Context(), db.GetNoteForContentEditParams{UserID: user.ID, NoteID: noteID})
		if handleQueryErrPage(w, pages, user, err) {
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
		compatible, err := q.ListFieldCompatibleNoteTypesForUser(r.Context(), db.ListFieldCompatibleNoteTypesForUserParams{
			UserID: user.ID, IsCloze: nt.IsCloze, CurrentNoteTypeID: note.NoteTypeID,
		})
		if err != nil {
			serverError(w)
			return
		}
		render(w, pages["note_form"], http.StatusOK, map[string]any{
			"User": user, "Note": note, "NoteType": nt, "Fields": fields, "FieldValues": fieldValues,
			"Decks": decks, "NoteTypeOptions": compatible,
		})
	})))

	mux.Handle("POST /notes/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		noteID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		if !parseForm(w, r) {
			return
		}

		q := db.New(store)
		note, err := q.GetNoteForContentEdit(r.Context(), db.GetNoteForContentEditParams{UserID: user.ID, NoteID: noteID})
		if handleQueryErrPage(w, pages, user, err) {
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

		var targetNoteTypeID pgtype.UUID
		if err := targetNoteTypeID.Scan(r.PostForm.Get("note_type_id")); err != nil {
			badRequest(w)
			return
		}

		newNoteTypeID := note.NoteTypeID
		targetNT := nt
		targetTemplates := templates
		wantFieldCount := len(fields)

		if targetNoteTypeID != note.NoteTypeID {
			// A note-type change is a more destructive, cross-user-impacting action than an
			// ordinary content edit (it can delete cards and other users' user_card_state on a
			// shared deck), so it requires can_manage_access in addition to can_edit_content --
			// checked first, before anything else in this branch (#138 resolved decision).
			_, err := q.GetNoteForNoteTypeChange(r.Context(), db.GetNoteForNoteTypeChangeParams{
				UserID: user.ID, NoteID: noteID,
			})
			if handleQueryErrPage(w, pages, user, err) {
				return
			}

			targetNT, err = q.GetNoteTypeForRead(r.Context(), db.GetNoteTypeForReadParams{ID: targetNoteTypeID, UserID: user.ID})
			if handleQueryErrPage(w, pages, user, err) {
				return
			}
			targetFields, err := q.ListFieldsForNoteType(r.Context(), targetNoteTypeID)
			if err != nil {
				serverError(w)
				return
			}
			if !fieldsCompatible(nt, fields, targetNT, targetFields) {
				http.Error(w, "note types must have the same fields in the same order to switch between them", http.StatusBadRequest)
				return
			}
			targetTemplates, err = q.ListTemplatesForNoteType(r.Context(), targetNoteTypeID)
			if err != nil {
				serverError(w)
				return
			}
			wantFieldCount = len(targetFields)
			newNoteTypeID = targetNoteTypeID
		}

		fieldsJSON, checksum, err := validateNoteFields(fieldValues, wantFieldCount)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		desired, err := desiredCards(targetNT, targetTemplates, fieldValues)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tags := parseTags(r.PostForm.Get("tags"))

		if targetNoteTypeID != note.NoteTypeID && r.PostForm.Get("confirm_note_type_change") != "1" {
			existingOrdinals, err := q.ListCardsForNote(r.Context(), noteID)
			if err != nil {
				serverError(w)
				return
			}
			existingSet := make(map[int32]struct{}, len(existingOrdinals))
			for _, o := range existingOrdinals {
				existingSet[o] = struct{}{}
			}
			desiredSet := make(map[int32]struct{}, len(desired))
			for _, d := range desired {
				desiredSet[d.Ordinal] = struct{}{}
			}
			var removedCount, addedCount int
			for o := range existingSet {
				if _, ok := desiredSet[o]; !ok {
					removedCount++
				}
			}
			for o := range desiredSet {
				if _, ok := existingSet[o]; !ok {
					addedCount++
				}
			}
			render(w, pages["note_form"], http.StatusOK, map[string]any{
				"User": user, "Note": note, "NoteType": nt, "TargetNoteType": targetNT,
				"ConfirmNoteTypeChange": true, "RemovedCount": removedCount, "AddedCount": addedCount,
				"FieldValues": fieldValues, "Tags": r.PostForm.Get("tags"),
			})
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		err = db.UpdateNoteWithCards(r.Context(), tx, user.ID, noteID, newNoteTypeID, fieldsJSON, tags, checksum, desired)
		if err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				notFoundPage(w, pages, user)
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
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		note, err := q.GetNoteForContentEdit(r.Context(), db.GetNoteForContentEditParams{UserID: user.ID, NoteID: noteID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}
		deckID := note.DeckID

		n, err := q.DeleteNote(r.Context(), db.DeleteNoteParams{NoteID: noteID, UserID: user.ID})
		if err != nil {
			serverError(w)
			return
		}
		if n == 0 {
			notFoundPage(w, pages, user)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))

	mux.Handle("POST /notes/{id}/move", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		noteID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
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

		if handleQueryErrPage(w, pages, user, db.MoveNote(r.Context(), tx, user.ID, noteID, targetDeckID)) {
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+targetDeckID.String(), http.StatusSeeOther)
	})))
}

// fieldsCompatible implements the #138 v1 field-compatibility rule for a note-type change: the
// same is_cloze flag, and the same field names in the same ordinal order (which implies the same
// count). This is the authoritative, Go-side check -- the GET edit page's dropdown is populated
// from ListFieldCompatibleNoteTypesForUser, but the POST handler never trusts that the submitted
// note_type_id actually came from that list.
func fieldsCompatible(fromNT db.NoteType, fromFields []db.Field, toNT db.NoteType, toFields []db.Field) bool {
	if fromNT.IsCloze != toNT.IsCloze {
		return false
	}
	if len(fromFields) != len(toFields) {
		return false
	}
	for i := range fromFields {
		if fromFields[i].Name != toFields[i].Name {
			return false
		}
	}
	return true
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
	checksum = db.ComputeNoteChecksum(fieldValues[0])
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
