package http

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	noterender "github.com/Jolls/deckshare/internal/render"
)

// errFieldCountMismatch is the one hard-error case buildNotePreview reports: the posted
// field[] count doesn't match the note type's field count. The real form always submits the
// right count -- this only fires on a malformed/direct POST -- so it maps to 400, matching how
// every other POST handler in notes.go treats a field-count mismatch.
var errFieldCountMismatch = errors.New("field count does not match note type")

// registerNotePreviewRoutes wires the two read-only, non-scheduling preview endpoints: render a
// note's card(s) from posted (possibly unsaved) field values without writing anything. Neither
// route touches cards, user_card_state, or review_log, and neither calls into internal/fsrs or
// internal/review (CLAUDE.md invariants §2.5/§2.7).
func registerNotePreviewRoutes(mux *http.ServeMux, store db.Beginner, fragments map[string]*template.Template) {
	mux.Handle("POST /notes/{id}/preview", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		deck, err := q.GetDeckForContentEdit(r.Context(), db.GetDeckForContentEditParams{UserID: user.ID, DeckID: note.DeckID})
		if handleQueryErr(w, err) {
			return
		}

		var noteTypeID pgtype.UUID
		if err := noteTypeID.Scan(r.PostForm.Get("note_type_id")); err != nil {
			badRequest(w)
			return
		}
		fieldValues := r.PostForm["field[]"]

		view, err := buildNotePreview(r.Context(), q, user.ID, noteTypeID, note.NoteTypeID, fieldValues, r.PostForm.Get("tags"), note.DeckID, deck.Name)
		if !respondNotePreview(w, err) {
			return
		}
		renderFragment(w, fragments["note_preview"], http.StatusOK, "note_preview", view)
	})))

	mux.Handle("POST /decks/{deckId}/notes/preview", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "deckId")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}

		q := db.New(store)
		deck, err := q.GetDeckForContentEdit(r.Context(), db.GetDeckForContentEditParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErr(w, err) {
			return
		}

		var noteTypeID pgtype.UUID
		if err := noteTypeID.Scan(r.PostForm.Get("note_type_id")); err != nil {
			badRequest(w)
			return
		}
		fieldValues := r.PostForm["field[]"]

		// No note exists yet, so there is no "current" note type to read unscoped -- every note
		// type offered on the new-note form is one the caller owns.
		view, err := buildNotePreview(r.Context(), q, user.ID, noteTypeID, pgtype.UUID{}, fieldValues, r.PostForm.Get("tags"), deckID, deck.Name)
		if !respondNotePreview(w, err) {
			return
		}
		renderFragment(w, fragments["note_preview"], http.StatusOK, "note_preview", view)
	})))
}

// respondNotePreview writes the error response for buildNotePreview's error, if any, and
// reports whether the caller should continue to render the view. The one hard-error case
// (errFieldCountMismatch) is 400; anything else is the same pgx.ErrNoRows-collapses-to-404 /
// bare-500 pattern handleQueryErr uses for every other query in this package.
func respondNotePreview(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, errFieldCountMismatch) {
		badRequest(w)
		return false
	}
	handleQueryErr(w, err)
	return false
}

// previewCardView is one rendered card for the fragment template. Number is 1-based, precomputed
// in Go rather than via a template function (working rule 2, minimum code).
type previewCardView struct {
	Number   int
	Question template.HTML
	Answer   template.HTML
}

// notePreviewView is the note_preview fragment's view model. A non-empty Notice means: show that
// message instead of Cards -- used for "no cloze marker yet" and "template has an error", both
// of which are normal live-typing states, not client errors (they're still HTTP 200).
type notePreviewView struct {
	CSS    template.CSS
	Cards  []previewCardView
	Notice string
}

// buildNotePreview renders noteTypeID's card(s) from fieldValues/tagsRaw exactly as posted --
// never reading the saved note -- without writing anything. Shared by both preview routes.
// deckID/deckName are used only for {{Deck}}/{{Subdeck}} and media resolution, and the caller
// must have already authorised deckID via GetNoteForContentEdit/GetDeckForContentEdit before
// calling this.
//
// The note-type lookup mirrors POST /notes/{id}/edit exactly: the note's *current* note type is
// read unscoped, because a collaborator editing a shared deck legitimately renders a note type
// owned by someone else (the edit page itself does the same at notes.go's GetNoteType call).
// Any *other* note type -- i.e. a note-type change staged in the dropdown, and every note type
// on the new-note form -- stays owner-scoped, since note types are owner-scoped by invariant
// §2.2. currentNoteTypeID is the zero UUID on the new-note route, where no note exists yet.
func buildNotePreview(ctx context.Context, q *db.Queries, ownerID pgtype.UUID, noteTypeID pgtype.UUID,
	currentNoteTypeID pgtype.UUID, fieldValues []string, tagsRaw string, deckID pgtype.UUID, deckName string,
) (notePreviewView, error) {
	var nt db.NoteType
	var err error
	if currentNoteTypeID.Valid && noteTypeID == currentNoteTypeID {
		nt, err = q.GetNoteType(ctx, noteTypeID)
	} else {
		nt, err = q.GetNoteTypeForOwner(ctx, db.GetNoteTypeForOwnerParams{ID: noteTypeID, OwnerID: ownerID})
	}
	if err != nil {
		return notePreviewView{}, err
	}
	fields, err := q.ListFieldsForNoteType(ctx, noteTypeID)
	if err != nil {
		return notePreviewView{}, err
	}
	templates, err := q.ListTemplatesForNoteType(ctx, noteTypeID)
	if err != nil {
		return notePreviewView{}, err
	}
	if len(fieldValues) != len(fields) {
		return notePreviewView{}, errFieldCountMismatch
	}

	desired, err := desiredCards(nt, templates, fieldValues)
	if err != nil {
		if errors.Is(err, errNoClozeMarkers) {
			return notePreviewView{Notice: "Add a {{c1::...}} marker to preview this note's cards."}, nil
		}
		return notePreviewView{}, err
	}

	noteFields := make([]noterender.Field, len(fields))
	for i, f := range fields {
		noteFields[i] = noterender.Field{Name: f.Name, Value: fieldValues[i]}
	}
	note := noterender.Note{
		Fields:   noteFields,
		Tags:     parseTags(tagsRaw),
		NoteType: nt.Name,
		Deck:     deckName,
		Subdeck:  previewLastSubdeck(deckName),
	}

	templatesByID := make(map[pgtype.UUID]db.Template, len(templates))
	for _, t := range templates {
		templatesByID[t.ID] = t
	}

	mediaRefs, err := q.ListMediaRefsForDeck(ctx, deckID)
	if err != nil {
		return notePreviewView{}, err
	}
	mediaByFilename := make(map[string]string, len(mediaRefs))
	for _, m := range mediaRefs {
		mediaByFilename[m.Filename] = m.Sha256
	}
	resolveMedia := func(filename string) (string, bool) {
		sha, ok := mediaByFilename[filename]
		return sha, ok
	}

	cards := make([]previewCardView, 0, len(desired))
	for i, d := range desired {
		tmpl := templatesByID[d.TemplateID]
		rendered, err := noterender.RenderCard(noterender.Template{Name: tmpl.Name, Qfmt: tmpl.Qfmt, Afmt: tmpl.Afmt}, note, d.Ordinal, nt.IsCloze)
		if err != nil {
			// A template syntax error is a note-type authoring problem, not a per-card one --
			// show a single notice instead of partial output.
			return notePreviewView{Notice: fmt.Sprintf("This note type's template has an error and can't be previewed: %v", err)}, nil
		}
		// Media rewrite first, widget splice second -- the same order as internal/review's
		// renderQueueRows. It matters: the type-answer widget must not be fed through the media
		// rewriter's HTML tokeniser (docs/plans/127-render-sanitization-audit.md), which is
		// exactly what happens if these two steps swap.
		rendered.Question.HTML = template.HTML(noterender.RewriteMediaSrcs(string(rendered.Question.HTML), resolveMedia))
		rendered.Answer.HTML = template.HTML(noterender.RewriteMediaSrcs(string(rendered.Answer.HTML), resolveMedia))
		cards = append(cards, previewCardView{
			Number:   i + 1,
			Question: noterender.TypeAnswerInput(rendered.Question),
			Answer:   noterender.TypeAnswerExpected(rendered.Answer),
		})
	}

	css, _ := noterender.SanitiseCSS(nt.Css)
	return notePreviewView{CSS: css, Cards: cards}, nil
}

// previewLastSubdeck is a private copy of internal/review's lastSubdeck -- that one is
// unexported, and this one-line "last '::' component" helper isn't worth giving internal/http a
// new dependency on internal/review for.
func previewLastSubdeck(name string) string {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		return name[i+2:]
	}
	return name
}
