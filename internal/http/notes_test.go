package http

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/Jolls/enshu/internal/auth"
)

func setupDeckAndNoteType(t *testing.T, handler http.Handler, cookie *http.Cookie) (deckPath string) {
	t.Helper()
	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath = w.Header().Get("Location")

	w = doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note type status = %d: %s", w.Code, w.Body.String())
	}
	return deckPath
}

func TestNoteRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types LIMIT 1`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Question")
	noteBody.Add("field[]", "Answer")
	noteBody.Set("tags", "tag1 tag2")
	w := doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d, want 303: %s", w.Code, w.Body.String())
	}

	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes LIMIT 1`).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1`, noteID) != 1 {
		t.Error("one card should have been generated (single non-cloze template)")
	}

	w = doRequest(handler, "GET", "/notes/"+noteID+"/edit", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET edit status = %d, want 200", w.Code)
	}

	editBody := url.Values{}
	editBody.Set("note_type_id", noteTypeID)
	editBody.Add("field[]", "Updated Q")
	editBody.Add("field[]", "Updated A")
	editBody.Set("tags", "tag1")
	w = doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("edit status = %d, want 303: %s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "POST", "/notes/"+noteID+"/delete", "", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", w.Code)
	}
	if countRows(t, tx, `SELECT count(*) FROM notes WHERE id = $1`, noteID) != 0 {
		t.Error("note should be gone")
	}
}

func TestNoteRoutes_ClozeGeneratesCardsByOrdinal(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	ntBody := url.Values{}
	ntBody.Set("name", "Cloze")
	ntBody.Set("css", "")
	ntBody.Add("is_cloze", "on")
	ntBody.Add("field_name[]", "Text")
	ntBody.Add("template_name[]", "Cloze")
	ntBody.Add("qfmt[]", "{{cloze:Text}}")
	ntBody.Add("afmt[]", "{{cloze:Text}}")
	w = doRequest(handler, "POST", "/note-types", ntBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create cloze note type status = %d: %s", w.Code, w.Body.String())
	}
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE is_cloze`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup cloze note type: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "{{c1::one}} and {{c2::two}}")
	w = doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create cloze note status = %d: %s", w.Code, w.Body.String())
	}

	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes LIMIT 1`).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1`, noteID) != 2 {
		t.Error("two cards should have been generated, one per cloze number")
	}
}

// notes.fields is a positional array indexed by fields.ordinal (docs/schema.md); a submission
// with the wrong number of values must be rejected, not silently written misaligned.
func TestNoteRoutes_FieldCountMismatch_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie) // note type "Basic" has 2 fields
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types LIMIT 1`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "only one field")
	w := doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if countRows(t, tx, `SELECT count(*) FROM notes`) != 0 {
		t.Error("no note should have been created")
	}
}

func TestNoteRoutes_ClozeWithoutMarkers_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	ntBody := url.Values{}
	ntBody.Set("name", "Cloze2")
	ntBody.Set("css", "")
	ntBody.Add("is_cloze", "on")
	ntBody.Add("field_name[]", "Text")
	ntBody.Add("template_name[]", "Cloze")
	ntBody.Add("qfmt[]", "{{cloze:Text}}")
	ntBody.Add("afmt[]", "{{cloze:Text}}")
	doRequest(handler, "POST", "/note-types", ntBody.Encode(), cookie, "http://example.com")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE is_cloze`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup cloze note type: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "no markers here")
	w = doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A note type left with zero templates (reachable by a structural edit while it has no notes,
// since the edit handler -- unlike create -- doesn't require at least one template) must reject
// a note creation attempt with 400, not 500, matching the edit path's ErrNoCards handling.
func TestNoteRoutes_CreateAgainstNoteTypeWithNoTemplates_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types LIMIT 1`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("name", "Basic")
	editBody.Set("css", "")
	editBody.Add("field_id[]", "")
	editBody.Add("field_name[]", "Front")
	editBody.Add("field_id[]", "")
	editBody.Add("field_name[]", "Back")
	// no template_name[]/qfmt[]/afmt[] -- strips the note type down to zero templates
	w := doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("strip templates status = %d, want 303: %s", w.Code, w.Body.String())
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	w = doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// A collaborator with can_view but not can_edit_content must not see or use the add-note form:
// GET .../notes/new authorises the same way POST .../notes already does.
func TestNoteRoutes_ViewOnlyCollaborator_CannotAddNote(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	viewerEmail := testEmail()
	viewerCookie := loginCookie(t, tx, a, viewerEmail, "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	var deckID, viewerID string
	if err := tx.QueryRow(ctx, `SELECT id FROM decks LIMIT 1`).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, viewerEmail).Scan(&viewerID); err != nil {
		t.Fatalf("lookup viewer: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, deckID, viewerID); err != nil {
		t.Fatalf("grant view access: %v", err)
	}

	w := doRequest(handler, "GET", deckPath+"/notes/new", "", viewerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("view-only GET notes/new status = %d, want 404", w.Code)
	}
}

func TestNoteRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types LIMIT 1`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes LIMIT 1`).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	tests := []struct {
		name, method, path, body string
		wantStatus               int
	}{
		{"stranger GET edit", "GET", "/notes/" + noteID + "/edit", "", http.StatusNotFound},
		{"stranger POST edit", "POST", "/notes/" + noteID + "/edit", "note_type_id=" + noteTypeID + "&field[]=X&field[]=Y", http.StatusNotFound},
		{"stranger POST delete", "POST", "/notes/" + noteID + "/delete", "", http.StatusNotFound},
		{"stranger GET new note in owner's deck", "GET", deckPath + "/notes/new", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, tt.method, tt.path, tt.body, strangerCookie, "http://example.com")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}
