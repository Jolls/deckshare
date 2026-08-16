package http

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/Jolls/enshu/internal/auth"
)

// newNoteTypeBody's name is "Basic2", not "Basic" -- every test user already has a seeded
// "Basic" note type from signup (#97), so tests that create their own must use a name that
// doesn't collide with it.
func newNoteTypeBody() string {
	v := url.Values{}
	v.Set("name", "Basic2")
	v.Set("css", "")
	v.Add("field_name[]", "Front")
	v.Add("field_name[]", "Back")
	v.Add("template_name[]", "Card 1")
	v.Add("qfmt[]", "{{Front}}")
	v.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	return v.Encode()
}

func TestNoteTypeRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /note-types status = %d, want 303: %s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "GET", "/note-types", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /note-types status = %d, want 200", w.Code)
	}

	var noteTypeID string
	if err := tx.QueryRow(context.Background(), `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("note type row should exist: %v", err)
	}
	if countRows(t, tx, `SELECT count(*) FROM fields WHERE note_type_id = $1`, noteTypeID) != 2 {
		t.Error("both fields should have been created")
	}
	if countRows(t, tx, `SELECT count(*) FROM templates WHERE note_type_id = $1`, noteTypeID) != 1 {
		t.Error("template should have been created")
	}
}

// A note type structural edit (remove/reorder a field) is refused with 409 once the note type
// has notes (§0.5); allowed freely with zero notes.
func TestNoteTypeEdit_StructureLockedWithNotes(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")

	var noteTypeID, frontFieldID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 ORDER BY ordinal LIMIT 1`, noteTypeID).Scan(&frontFieldID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	w = doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d, want 303: %s", w.Code, w.Body.String())
	}

	// Attempt to remove the "Back" field (omit its field_id[]/field_name[] pair) while a note exists.
	editBody := url.Values{}
	editBody.Set("name", "Basic2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", frontFieldID)
	editBody.Add("field_name[]", "Front")
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

// A malformed field_id[]/template_id[] (not empty, not a valid UUID -- e.g. a forged or
// corrupted submission) must be rejected, not silently coerced into an "append" via a discarded
// Scan error.
func TestNoteTypeEdit_MalformedFieldID_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("name", "Basic2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", "not-a-uuid")
	editBody.Add("field_name[]", "Front")
	w := doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// Renaming a note type to collide with another of the caller's note types must 409, not 500.
func TestNoteTypeEdit_DuplicateName_409(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")
	other := url.Values{}
	other.Set("name", "Other")
	other.Set("css", "")
	other.Add("field_name[]", "Solo")
	other.Add("template_name[]", "Card 1")
	other.Add("qfmt[]", "{{Solo}}")
	other.Add("afmt[]", "{{Solo}}")
	doRequest(handler, "POST", "/note-types", other.Encode(), cookie, "http://example.com")

	var otherID, soloFieldID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Other'`).Scan(&otherID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1`, otherID).Scan(&soloFieldID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("name", "Basic2") // collides with the first note type
	editBody.Set("css", "")
	editBody.Add("field_id[]", soloFieldID)
	editBody.Add("field_name[]", "Solo")
	w := doRequest(handler, "POST", "/note-types/"+otherID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// Inserting a new field in the middle of the submitted list (not just appending at the end) must
// land at the submitted position, not silently at the end of the stored order.
func TestNoteTypeEdit_InsertFieldInMiddle_LandsAtSubmittedPosition(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")
	var noteTypeID, frontID, backID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Front'`, noteTypeID).Scan(&frontID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Back'`, noteTypeID).Scan(&backID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("name", "Basic2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", frontID)
	editBody.Add("field_name[]", "Front")
	editBody.Add("field_id[]", "") // new field inserted between Front and Back
	editBody.Add("field_name[]", "Middle")
	editBody.Add("field_id[]", backID)
	editBody.Add("field_name[]", "Back")
	w := doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("edit status = %d, want 303: %s", w.Code, w.Body.String())
	}

	rows, err := tx.Query(ctx, `SELECT name FROM fields WHERE note_type_id = $1 ORDER BY ordinal`, noteTypeID)
	if err != nil {
		t.Fatalf("query fields: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	want := []string{"Front", "Middle", "Back"}
	if len(names) != len(want) {
		t.Fatalf("field order = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("field order = %v, want %v", names, want)
			break
		}
	}
}

func TestNoteTypeRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newNoteTypeBody(), ownerCookie, "http://example.com")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	w := doRequest(handler, "GET", "/note-types/"+noteTypeID+"/edit", "", strangerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("stranger GET edit status = %d, want 404", w.Code)
	}
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/delete", "", strangerCookie, "http://example.com")
	if w.Code != http.StatusNotFound {
		t.Errorf("stranger POST delete status = %d, want 404", w.Code)
	}
}

// A cloze note type must never end up with more than one template: desiredCards's cloze branch
// only ever addresses the first template, so a second one creates cards that a later edit's
// SyncNoteCards treats as undesired and deletes -- discarding user_card_state/review_log.
func TestNoteTypeEdit_RejectsSecondTemplateOnClozeNoteType(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	ntBody := url.Values{}
	ntBody.Set("name", "Cloze2")
	ntBody.Set("css", "")
	ntBody.Add("is_cloze", "on")
	ntBody.Add("field_name[]", "Text")
	ntBody.Add("template_name[]", "Cloze")
	ntBody.Add("qfmt[]", "{{cloze:Text}}")
	ntBody.Add("afmt[]", "{{cloze:Text}}")
	doRequest(handler, "POST", "/note-types", ntBody.Encode(), cookie, "http://example.com")

	var noteTypeID, templateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Cloze2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup cloze note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, noteTypeID).Scan(&templateID); err != nil {
		t.Fatalf("lookup template: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("name", "Cloze2")
	editBody.Set("css", "")
	editBody.Add("template_id[]", templateID)
	editBody.Add("template_name[]", "Cloze")
	editBody.Add("qfmt[]", "{{cloze:Text}}")
	editBody.Add("afmt[]", "{{cloze:Text}}")
	editBody.Add("template_id[]", "") // append attempt: a second template
	editBody.Add("template_name[]", "Cloze 2")
	editBody.Add("qfmt[]", "{{cloze:Text}}")
	editBody.Add("afmt[]", "{{cloze:Text}}")
	w := doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if countRows(t, tx, `SELECT count(*) FROM templates WHERE note_type_id = $1`, noteTypeID) != 1 {
		t.Error("cloze note type should still have exactly one template")
	}
}
