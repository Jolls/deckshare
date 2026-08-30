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

// A note type structural edit (remove/reorder a field or template) once the note type has notes
// (#89) shows a confirmation page instead of applying immediately, and applies nothing until the
// caller resubmits with confirm_structural_change=1.
func TestNoteTypeEdit_StructuralChangeWithNotes_RequiresConfirmation(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")

	var noteTypeID, frontFieldID, templateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 ORDER BY ordinal LIMIT 1`, noteTypeID).Scan(&frontFieldID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, noteTypeID).Scan(&templateID); err != nil {
		t.Fatalf("lookup template: %v", err)
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
	editBody.Add("field_position[]", "0")
	editBody.Add("template_id[]", templateID)
	editBody.Add("template_name[]", "Card 1")
	editBody.Add("qfmt[]", "{{Front}}")
	editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	editBody.Add("template_position[]", "0")
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (preview page): %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM fields WHERE note_type_id = $1`, noteTypeID) != 2 {
		t.Error("no field should have been removed without confirmation")
	}

	editBody.Set("confirm_structural_change", "1")
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("confirmed edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM fields WHERE note_type_id = $1`, noteTypeID) != 1 {
		t.Error("the Back field should have been removed after confirmation")
	}
	var fieldsJSON string
	if err := tx.QueryRow(ctx, `SELECT fields::text FROM notes WHERE note_type_id = $1`, noteTypeID).Scan(&fieldsJSON); err != nil {
		t.Fatalf("select note fields: %v", err)
	}
	if fieldsJSON != `["Q"]` {
		t.Errorf("note fields = %s, want [\"Q\"] (Back's value discarded)", fieldsJSON)
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

	var otherID, soloFieldID, soloTemplateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Other'`).Scan(&otherID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1`, otherID).Scan(&soloFieldID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, otherID).Scan(&soloTemplateID); err != nil {
		t.Fatalf("lookup template: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("name", "Basic2") // collides with the first note type
	editBody.Set("css", "")
	editBody.Add("field_id[]", soloFieldID)
	editBody.Add("field_name[]", "Solo")
	editBody.Add("field_position[]", "0")
	editBody.Add("template_id[]", soloTemplateID)
	editBody.Add("template_name[]", "Card 1")
	editBody.Add("qfmt[]", "{{Solo}}")
	editBody.Add("afmt[]", "{{Solo}}")
	editBody.Add("template_position[]", "0")
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
	var noteTypeID, frontID, backID, templateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Front'`, noteTypeID).Scan(&frontID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Back'`, noteTypeID).Scan(&backID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, noteTypeID).Scan(&templateID); err != nil {
		t.Fatalf("lookup template: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("name", "Basic2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", frontID)
	editBody.Add("field_name[]", "Front")
	editBody.Add("field_position[]", "0")
	editBody.Add("field_id[]", "") // new field inserted between Front and Back
	editBody.Add("field_name[]", "Middle")
	editBody.Add("field_position[]", "1")
	editBody.Add("field_id[]", backID)
	editBody.Add("field_name[]", "Back")
	editBody.Add("field_position[]", "2")
	editBody.Add("template_id[]", templateID)
	editBody.Add("template_name[]", "Card 1")
	editBody.Add("qfmt[]", "{{Front}}")
	editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	editBody.Add("template_position[]", "0")
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

	// #89: a stranger's structural edit attempt (with or without confirm_structural_change) must
	// still 404 -- note-type ownership is the only gate (§10's "no new gate" decision), and this
	// confirms it wasn't accidentally loosened.
	var frontFieldID, templateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 ORDER BY ordinal LIMIT 1`, noteTypeID).Scan(&frontFieldID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, noteTypeID).Scan(&templateID); err != nil {
		t.Fatalf("lookup template: %v", err)
	}
	structuralBody := url.Values{}
	structuralBody.Set("name", "Basic2")
	structuralBody.Set("css", "")
	structuralBody.Add("field_id[]", frontFieldID)
	structuralBody.Add("field_name[]", "Front")
	structuralBody.Add("field_position[]", "0")
	structuralBody.Add("template_id[]", templateID)
	structuralBody.Add("template_name[]", "Card 1")
	structuralBody.Add("qfmt[]", "{{Front}}")
	structuralBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	structuralBody.Add("template_position[]", "0")
	for _, confirm := range []string{"", "1"} {
		body := url.Values{}
		for k, v := range structuralBody {
			body[k] = v
		}
		if confirm != "" {
			body.Set("confirm_structural_change", confirm)
		}
		w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", body.Encode(), strangerCookie, "http://example.com")
		if w.Code != http.StatusNotFound {
			t.Errorf("stranger structural edit (confirm=%q) status = %d, want 404", confirm, w.Code)
		}
	}
	if countRows(t, tx, `SELECT count(*) FROM fields WHERE note_type_id = $1`, noteTypeID) != 2 {
		t.Error("stranger's structural edit attempts should not have changed anything")
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

// #89: a template reorder confirmed while the note type has notes must keep each card's id and
// template_id fixed and only move cards.ordinal -- the end-to-end version of
// TestRemapNoteTypeCards_ReorderKeepsCardIdentityAndRepointsOrdinal, through the HTTP confirm flow.
func TestNoteTypeEdit_TemplateReorderWithNotes_PreservesCardIdentityAcrossConfirm(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), cookie, "http://example.com")
	var noteTypeID, frontID, backID, template1ID, template2ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Reversed2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Front'`, noteTypeID).Scan(&frontID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Back'`, noteTypeID).Scan(&backID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1 AND name = 'Card 1'`, noteTypeID).Scan(&template1ID); err != nil {
		t.Fatalf("lookup template 1: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1 AND name = 'Card 2'`, noteTypeID).Scan(&template2ID); err != nil {
		t.Fatalf("lookup template 2: %v", err)
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

	var card1ID, card2ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE template_id = $1`, template1ID).Scan(&card1ID); err != nil {
		t.Fatalf("lookup card1: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE template_id = $1`, template2ID).Scan(&card2ID); err != nil {
		t.Fatalf("lookup card2: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due) VALUES ((SELECT owner_id FROM note_types WHERE id = $1), $2, now())`, noteTypeID, card1ID); err != nil {
		t.Fatalf("seed user_card_state: %v", err)
	}

	// Swap templates: Card 2 to position 0, Card 1 to position 1.
	editBody := url.Values{}
	editBody.Set("name", "Reversed2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", frontID)
	editBody.Add("field_name[]", "Front")
	editBody.Add("field_position[]", "0")
	editBody.Add("field_id[]", backID)
	editBody.Add("field_name[]", "Back")
	editBody.Add("field_position[]", "1")
	editBody.Add("template_id[]", template2ID)
	editBody.Add("template_name[]", "Card 2")
	editBody.Add("qfmt[]", "{{Back}}")
	editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Front}}")
	editBody.Add("template_position[]", "0")
	editBody.Add("template_id[]", template1ID)
	editBody.Add("template_name[]", "Card 1")
	editBody.Add("qfmt[]", "{{Front}}")
	editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	editBody.Add("template_position[]", "1")
	editBody.Add("confirm_structural_change", "1")
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("edit status = %d, want 303: %s", w.Code, w.Body.String())
	}

	var ord1, ord2 int32
	var tid1, tid2 string
	if err := tx.QueryRow(ctx, `SELECT ordinal, template_id FROM cards WHERE id = $1`, card1ID).Scan(&ord1, &tid1); err != nil {
		t.Fatalf("select card1: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT ordinal, template_id FROM cards WHERE id = $1`, card2ID).Scan(&ord2, &tid2); err != nil {
		t.Fatalf("select card2: %v", err)
	}
	if ord1 != 1 || tid1 != template1ID {
		t.Errorf("card1: ordinal=%d template_id=%s, want ordinal=1 template_id=%s (fixed identity)", ord1, tid1, template1ID)
	}
	if ord2 != 0 || tid2 != template2ID {
		t.Errorf("card2: ordinal=%d template_id=%s, want ordinal=0 template_id=%s (fixed identity)", ord2, tid2, template2ID)
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, card1ID) != 1 {
		t.Error("card1's user_card_state should survive the reorder untouched")
	}
}

// #89, end-to-end version of TestRemapNoteTypeCards_RemovedTemplate...: removing a template while
// the note type has notes, through the confirm flow, must delete the backed cards (cascading
// user_card_state) and leave review_log orphaned, not deleted.
func TestNoteTypeEdit_TemplateRemovalWithNotes_DeletesCardsOrphansReviewLog(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), cookie, "http://example.com")
	var noteTypeID, frontID, backID, template1ID, template2ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Reversed2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Front'`, noteTypeID).Scan(&frontID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Back'`, noteTypeID).Scan(&backID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1 AND name = 'Card 1'`, noteTypeID).Scan(&template1ID); err != nil {
		t.Fatalf("lookup template 1: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1 AND name = 'Card 2'`, noteTypeID).Scan(&template2ID); err != nil {
		t.Fatalf("lookup template 2: %v", err)
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

	var card2ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE template_id = $1`, template2ID).Scan(&card2ID); err != nil {
		t.Fatalf("lookup card2: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due) VALUES ((SELECT owner_id FROM note_types WHERE id = $1), $2, now())`, noteTypeID, card2ID); err != nil {
		t.Fatalf("seed user_card_state: %v", err)
	}
	var reviewLogID string
	if err := tx.QueryRow(ctx, `INSERT INTO review_log
			(user_id, card_id, rating, reviewed_at, state_before, learning_steps_before, elapsed_days_before, scheduled_days_after, review_kind)
		VALUES ((SELECT owner_id FROM note_types WHERE id = $1), $2, 3, now(), 0, 0, 0, 0, 0) RETURNING id`, noteTypeID, card2ID).Scan(&reviewLogID); err != nil {
		t.Fatalf("seed review_log: %v", err)
	}

	// Remove Card 2 -- only Card 1 is submitted.
	editBody := url.Values{}
	editBody.Set("name", "Reversed2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", frontID)
	editBody.Add("field_name[]", "Front")
	editBody.Add("field_position[]", "0")
	editBody.Add("field_id[]", backID)
	editBody.Add("field_name[]", "Back")
	editBody.Add("field_position[]", "1")
	editBody.Add("template_id[]", template1ID)
	editBody.Add("template_name[]", "Card 1")
	editBody.Add("qfmt[]", "{{Front}}")
	editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	editBody.Add("template_position[]", "0")

	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (preview page): %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE id = $1`, card2ID) != 1 {
		t.Error("card2 should not have been removed without confirmation")
	}

	editBody.Set("confirm_structural_change", "1")
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("confirmed edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE id = $1`, card2ID) != 0 {
		t.Error("card2 should be gone after confirmation")
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, card2ID) != 0 {
		t.Error("user_card_state should have cascaded away")
	}
	if countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1 AND card_id = $2`, reviewLogID, card2ID) != 1 {
		t.Error("review_log should survive, orphaned, with card_id unchanged")
	}
	if countRows(t, tx, `SELECT count(*) FROM templates WHERE id = $1`, template2ID) != 0 {
		t.Error("the now-cardless template row should also be gone")
	}
}

// A pure rename and a pure trailing append while notes exist must apply immediately, with no
// confirmation page -- regression guard against the #89 confirmation gate becoming too broad.
func TestNoteTypeEdit_PureAppendOrRenameWithNotes_NoConfirmationNeeded(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID, frontID, backID, templateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Front'`, noteTypeID).Scan(&frontID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Back'`, noteTypeID).Scan(&backID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, noteTypeID).Scan(&templateID); err != nil {
		t.Fatalf("lookup template: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	w := doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d, want 303: %s", w.Code, w.Body.String())
	}

	editBody := url.Values{}
	editBody.Set("name", "Basic2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", frontID)
	editBody.Add("field_name[]", "Front Renamed")
	editBody.Add("field_position[]", "0")
	editBody.Add("field_id[]", backID)
	editBody.Add("field_name[]", "Back")
	editBody.Add("field_position[]", "1")
	editBody.Add("field_id[]", "")
	editBody.Add("field_name[]", "Extra")
	editBody.Add("field_position[]", "2")
	editBody.Add("template_id[]", templateID)
	editBody.Add("template_name[]", "Card 1")
	editBody.Add("qfmt[]", "{{Front}}")
	editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	editBody.Add("template_position[]", "0")
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (no confirmation needed): %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM fields WHERE note_type_id = $1`, noteTypeID) != 3 {
		t.Error("the appended field should have been created")
	}
}

// A non-numeric field_position[] (a forged or corrupted submission) must be rejected, not
// silently coerced.
func TestNoteTypeEdit_MalformedFieldPosition_400(t *testing.T) {
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
	editBody.Add("field_id[]", "")
	editBody.Add("field_name[]", "Front")
	editBody.Add("field_position[]", "not-a-number")
	w := doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A submission that blanks every field name, or every template name, must 400 -- new validation
// #89 adds to the edit path (previously only the create path required at least one of each).
func TestNoteTypeEdit_EmptyFieldsOrTemplates_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newNoteTypeBody(), cookie, "http://example.com")
	var noteTypeID, frontID, templateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Front'`, noteTypeID).Scan(&frontID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, noteTypeID).Scan(&templateID); err != nil {
		t.Fatalf("lookup template: %v", err)
	}

	t.Run("all fields blank", func(t *testing.T) {
		editBody := url.Values{}
		editBody.Set("name", "Basic2")
		editBody.Set("css", "")
		editBody.Add("field_id[]", frontID)
		editBody.Add("field_name[]", "")
		editBody.Add("field_position[]", "0")
		editBody.Add("template_id[]", templateID)
		editBody.Add("template_name[]", "Card 1")
		editBody.Add("qfmt[]", "{{Front}}")
		editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
		editBody.Add("template_position[]", "0")
		w := doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("all templates blank", func(t *testing.T) {
		editBody := url.Values{}
		editBody.Set("name", "Basic2")
		editBody.Set("css", "")
		editBody.Add("field_id[]", frontID)
		editBody.Add("field_name[]", "Front")
		editBody.Add("field_position[]", "0")
		editBody.Add("template_id[]", templateID)
		editBody.Add("template_name[]", "")
		editBody.Add("qfmt[]", "{{Front}}")
		editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
		editBody.Add("template_position[]", "0")
		w := doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), cookie, "http://example.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// The #89-scale version of #138's multiuser regression: a note-type template removal must cascade
// user_card_state and orphan review_log for EVERY user on a shared deck, not just the owner --
// CLAUDE.md §15's "silently corrupts review_log or user_card_state" sev:critical bucket.
func TestNoteTypeEdit_TemplateRemovalWithNotes_MultiuserSchedulingStateSurvives(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	mateEmail := testEmail()
	loginCookie(t, tx, a, mateEmail, "correct-horse-battery")
	ctx := context.Background()

	doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), ownerCookie, "http://example.com")
	var noteTypeID, frontID, backID, template1ID, template2ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Reversed2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Front'`, noteTypeID).Scan(&frontID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM fields WHERE note_type_id = $1 AND name = 'Back'`, noteTypeID).Scan(&backID); err != nil {
		t.Fatalf("lookup field: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1 AND name = 'Card 1'`, noteTypeID).Scan(&template1ID); err != nil {
		t.Fatalf("lookup template 1: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1 AND name = 'Card 2'`, noteTypeID).Scan(&template2ID); err != nil {
		t.Fatalf("lookup template 2: %v", err)
	}

	w := doRequest(handler, "POST", "/decks", "name=D", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	var deckID string
	if err := tx.QueryRow(ctx, `SELECT id FROM decks WHERE name = 'D'`).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck: %v", err)
	}
	mateID := userID(t, ctx, tx, mateEmail)
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, mateID); err != nil {
		t.Fatalf("grant mate access: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	w = doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d, want 303: %s", w.Code, w.Body.String())
	}

	var survivingCardID, removedCardID string
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE template_id = $1`, template1ID).Scan(&survivingCardID); err != nil {
		t.Fatalf("lookup surviving card: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE template_id = $1`, template2ID).Scan(&removedCardID); err != nil {
		t.Fatalf("lookup removed card: %v", err)
	}

	ownerID := userID(t, ctx, tx, ownerEmail)
	reviewLogIDs := map[string]string{}
	for _, u := range []string{ownerID, mateID} {
		for _, c := range []string{survivingCardID, removedCardID} {
			if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due) VALUES ($1, $2, now())`, u, c); err != nil {
				t.Fatalf("seed user_card_state(%s,%s): %v", u, c, err)
			}
			var rl string
			if err := tx.QueryRow(ctx, `INSERT INTO review_log
					(user_id, card_id, rating, reviewed_at, state_before, learning_steps_before, elapsed_days_before, scheduled_days_after, review_kind)
				VALUES ($1, $2, 3, now(), 0, 0, 0, 0, 0) RETURNING id`, u, c).Scan(&rl); err != nil {
				t.Fatalf("seed review_log(%s,%s): %v", u, c, err)
			}
			reviewLogIDs[u+c] = rl
		}
	}

	editBody := url.Values{}
	editBody.Set("name", "Reversed2")
	editBody.Set("css", "")
	editBody.Add("field_id[]", frontID)
	editBody.Add("field_name[]", "Front")
	editBody.Add("field_position[]", "0")
	editBody.Add("field_id[]", backID)
	editBody.Add("field_name[]", "Back")
	editBody.Add("field_position[]", "1")
	editBody.Add("template_id[]", template1ID)
	editBody.Add("template_name[]", "Card 1")
	editBody.Add("qfmt[]", "{{Front}}")
	editBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	editBody.Add("template_position[]", "0")
	editBody.Add("confirm_structural_change", "1")
	w = doRequest(handler, "POST", "/note-types/"+noteTypeID+"/edit", editBody.Encode(), ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("confirmed edit status = %d, want 303: %s", w.Code, w.Body.String())
	}

	for _, u := range []string{ownerID, mateID} {
		if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`, u, survivingCardID) != 1 {
			t.Errorf("user %s: surviving card's user_card_state should be untouched", u)
		}
		if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`, u, removedCardID) != 0 {
			t.Errorf("user %s: removed card's user_card_state should be gone", u)
		}
		rl := reviewLogIDs[u+removedCardID]
		if countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1 AND card_id = $2`, rl, removedCardID) != 1 {
			t.Errorf("user %s: removed card's review_log should survive, orphaned", u)
		}
	}
}
