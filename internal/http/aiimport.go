package http

import (
	"context"
	"fmt"
	"html/template"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/textimport"
)

const (
	maxAIImportBytes = 2 << 20 // pasted NDJSON text, not a binary upload -- cf. import.go's maxUploadBytes
	maxAIImportNotes = 500     // bounds one synchronous request; no job queue for MVP
)

// registerAIImportRoutes wires GET/POST /import/ai (docs/routes.md): pick a deck + note type,
// generate a prompt to run through the user's own AI, then parse and commit the pasted NDJSON
// reply -- issue #85. Every note is authored fresh via the same pipeline
// POST /decks/{deckId}/notes uses (notes.go), never internal/apkg -- there is no Anki source
// here, so the apkg IR's guid/anki_id/due-state baggage doesn't apply.
//
// GET renders one of three steps, driven entirely by which of deck_id/note_type_id resolved:
// neither -> pick both; deck_id only (e.g. linked from within a deck, deck.html's "Import via
// AI") -> deck fixed, pick a note type; both -> show the generated prompt.
func registerAIImportRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /import/ai", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		q := db.New(store)

		noteTypes, err := q.ListNoteTypesForOwner(r.Context(), user.ID)
		if err != nil {
			serverError(w)
			return
		}
		depth := parseClozeDepth(r.URL.Query().Get("depth"))

		deckID, deckOK := parseUUIDParam(r.URL.Query().Get("deck_id"))
		if !deckOK {
			decks, err := q.ListDecksForUser(r.Context(), user.ID)
			if err != nil {
				serverError(w)
				return
			}
			render(w, pages["import_ai"], http.StatusOK, map[string]any{
				"User": user, "Decks": decks, "NoteTypes": noteTypes,
				"ClozeDepths": textimport.ClozeDepths, "Depth": depth,
			})
			return
		}

		deck, err := q.GetDeckForContentEdit(r.Context(), db.GetDeckForContentEditParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		noteTypeID, ntOK := parseUUIDParam(r.URL.Query().Get("note_type_id"))
		if !ntOK {
			render(w, pages["import_ai"], http.StatusOK, map[string]any{
				"User": user, "Deck": deck, "NoteTypes": noteTypes,
				"ClozeDepths": textimport.ClozeDepths, "Depth": depth,
			})
			return
		}

		nt, fields, err := loadNoteType(r.Context(), q, user.ID, noteTypeID)
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		render(w, pages["import_ai"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "NoteType": nt, "Depth": depth,
			"Prompt": textimport.BuildPrompt(textimport.PromptOptions{
				NoteTypeName: nt.Name, FieldNames: fieldNames(fields), IsCloze: nt.IsCloze, ClozeDepth: depth,
			}),
		})
	})))

	mux.Handle("POST /import/ai", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())

		r.Body = http.MaxBytesReader(w, r.Body, maxAIImportBytes)
		if !parseForm(w, r) {
			return
		}

		var deckID, noteTypeID pgtype.UUID
		if err := deckID.Scan(r.PostForm.Get("deck_id")); err != nil {
			badRequest(w)
			return
		}
		if err := noteTypeID.Scan(r.PostForm.Get("note_type_id")); err != nil {
			badRequest(w)
			return
		}
		pasted := r.PostForm.Get("text")
		depth := parseClozeDepth(r.PostForm.Get("depth"))

		q := db.New(store)
		deck, nt, fields, err := loadDeckAndNoteType(r.Context(), q, user.ID, deckID, noteTypeID)
		if handleQueryErrPage(w, pages, user, err) {
			return
		}
		templates, err := q.ListTemplatesForNoteType(r.Context(), noteTypeID)
		if err != nil {
			serverError(w)
			return
		}

		rerender := func(status int, errs []string) {
			render(w, pages["import_ai"], status, map[string]any{
				"User": user, "Deck": deck, "NoteType": nt, "Depth": depth,
				"Prompt": textimport.BuildPrompt(textimport.PromptOptions{
					NoteTypeName: nt.Name, FieldNames: fieldNames(fields), IsCloze: nt.IsCloze, ClozeDepth: depth,
				}),
				"Pasted": pasted, "Errors": errs,
			})
		}

		notes, lineErrs := textimport.Parse(pasted)
		if len(notes) == 0 && len(lineErrs) == 0 {
			rerender(http.StatusBadRequest, []string{"paste your AI's reply first"})
			return
		}
		if len(notes) > maxAIImportNotes {
			rerender(http.StatusBadRequest, []string{fmt.Sprintf("too many cards in one paste (max %d)", maxAIImportNotes)})
			return
		}

		var errs []string
		for _, e := range lineErrs {
			errs = append(errs, e.Error())
		}

		type preparedNote struct {
			guid       string
			fieldsJSON []byte
			checksum   int64
			tags       []string
			desired    []db.DesiredCard
		}
		prepared := make([]preparedNote, 0, len(notes))
		for i, n := range notes {
			fieldsJSON, checksum, ferr := validateNoteFields(n.Fields, len(fields))
			if ferr != nil {
				errs = append(errs, fmt.Sprintf("card %d: %s", i+1, ferr))
				continue
			}
			desired, derr := desiredCards(nt, templates, n.Fields)
			if derr != nil {
				errs = append(errs, fmt.Sprintf("card %d: %s", i+1, derr))
				continue
			}
			guid, gerr := randomGuid()
			if gerr != nil {
				serverError(w)
				return
			}
			prepared = append(prepared, preparedNote{
				guid: guid, fieldsJSON: fieldsJSON, checksum: checksum,
				tags: dedupeTags(n.Tags), desired: desired,
			})
		}

		if len(errs) > 0 {
			rerender(http.StatusBadRequest, errs)
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		for _, p := range prepared {
			if _, err := db.CreateNoteWithCards(r.Context(), tx, db.CreateNoteParams{
				Guid:       p.guid,
				Fields:     p.fieldsJSON,
				Tags:       p.tags,
				Checksum:   p.checksum,
				UserID:     user.ID,
				NoteTypeID: noteTypeID,
				DeckID:     deckID,
			}, p.desired); err != nil {
				serverError(w)
				return
			}
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))
}

// loadDeckAndNoteType resolves and authorises both halves of the picker (owner-scoped, same
// checks as notes.go) -- the hidden deck_id/note_type_id form fields on the POST are never
// trusted without re-resolving them the same way the GET did.
func loadDeckAndNoteType(ctx context.Context, q *db.Queries, userID, deckID, noteTypeID pgtype.UUID) (db.Deck, db.NoteType, []db.Field, error) {
	deck, err := q.GetDeckForContentEdit(ctx, db.GetDeckForContentEditParams{UserID: userID, DeckID: deckID})
	if err != nil {
		return db.Deck{}, db.NoteType{}, nil, err
	}
	nt, fields, err := loadNoteType(ctx, q, userID, noteTypeID)
	if err != nil {
		return db.Deck{}, db.NoteType{}, nil, err
	}
	return deck, nt, fields, nil
}

func loadNoteType(ctx context.Context, q *db.Queries, userID, noteTypeID pgtype.UUID) (db.NoteType, []db.Field, error) {
	nt, err := q.GetNoteTypeForOwner(ctx, db.GetNoteTypeForOwnerParams{ID: noteTypeID, OwnerID: userID})
	if err != nil {
		return db.NoteType{}, nil, err
	}
	fields, err := q.ListFieldsForNoteType(ctx, noteTypeID)
	if err != nil {
		return db.NoteType{}, nil, err
	}
	return nt, fields, nil
}

func fieldNames(fields []db.Field) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

// parseClozeDepth never fails: an empty or unrecognised value falls back to
// textimport.DefaultClozeDepth, since a bad depth only means a slightly-off prompt, never a
// correctness issue -- nothing downstream of BuildPrompt reads it.
func parseClozeDepth(s string) textimport.ClozeDepth {
	d := textimport.ClozeDepth(s)
	for _, opt := range textimport.ClozeDepths {
		if opt.Value == d {
			return d
		}
	}
	return textimport.DefaultClozeDepth
}

func parseUUIDParam(s string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if s == "" || id.Scan(s) != nil {
		return pgtype.UUID{}, false
	}
	return id, true
}

// dedupeTags mirrors parseTags' dedup behaviour (notes.go) for an already-split slice --
// NDJSON's "tags" arrives as a []string, not the raw space-separated form the note-edit form
// posts.
func dedupeTags(raw []string) []string {
	seen := map[string]struct{}{}
	tags := make([]string, 0, len(raw))
	for _, t := range raw {
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		tags = append(tags, t)
	}
	return tags
}
