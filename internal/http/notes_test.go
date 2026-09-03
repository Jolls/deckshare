package http

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/auth"
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

// newReversedNoteTypeBody creates a note type with the same two fields as newNoteTypeBody's
// "Basic2" (Front, Back) but two templates -- the "Basic (and reversed card)"-shaped fixture
// #138's plan uses as its motivating example, field-compatible with "Basic2" for a note-type
// change.
func newReversedNoteTypeBody(name string) string {
	v := url.Values{}
	v.Set("name", name)
	v.Set("css", "")
	v.Add("field_name[]", "Front")
	v.Add("field_name[]", "Back")
	v.Add("template_name[]", "Card 1")
	v.Add("qfmt[]", "{{Front}}")
	v.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	v.Add("template_name[]", "Card 2")
	v.Add("qfmt[]", "{{Back}}")
	v.Add("afmt[]", "{{FrontSide}}<hr>{{Front}}")
	return v.Encode()
}

// The v1 note-type-change flow (#138): a note on a 2-template note type previews a change to a
// field-compatible 1-template note type (no DB change until confirmed), then confirms it -- one
// card is removed, the surviving card keeps its id but has its template_id repointed to the new
// note type's template.
func TestNoteRoutes_ChangeNoteType_ConfirmFlow(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie) // "Basic2": 1 template, Front/Back
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	w := doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create reversed note type status = %d: %s", w.Code, w.Body.String())
	}
	var reversedID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Reversed2'`).Scan(&reversedID); err != nil {
		t.Fatalf("lookup Reversed2: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", reversedID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	w = doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d: %s", w.Code, w.Body.String())
	}
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, reversedID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1`, noteID) != 2 {
		t.Fatal("note should start with 2 cards (one per template)")
	}
	var ordinal0CardID string
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE note_id = $1 AND ordinal = 0`, noteID).Scan(&ordinal0CardID); err != nil {
		t.Fatalf("lookup ordinal 0 card: %v", err)
	}

	// Unconfirmed: preview only, no DB change.
	editBody := url.Values{}
	editBody.Set("note_type_id", basicID)
	editBody.Add("field[]", "Q")
	editBody.Add("field[]", "A")
	editBody.Set("tags", "")
	w = doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("unconfirmed edit status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var gotNoteTypeID string
	if err := tx.QueryRow(ctx, `SELECT note_type_id FROM notes WHERE id = $1`, noteID).Scan(&gotNoteTypeID); err != nil {
		t.Fatalf("select note: %v", err)
	}
	if gotNoteTypeID != reversedID {
		t.Error("note_type_id should not change before confirmation")
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1`, noteID) != 2 {
		t.Error("card count should not change before confirmation")
	}

	// Confirmed.
	editBody.Set("confirm_note_type_change", "1")
	w = doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("confirmed edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if err := tx.QueryRow(ctx, `SELECT note_type_id FROM notes WHERE id = $1`, noteID).Scan(&gotNoteTypeID); err != nil {
		t.Fatalf("select note: %v", err)
	}
	if gotNoteTypeID != basicID {
		t.Errorf("note_type_id = %s, want %s", gotNoteTypeID, basicID)
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1`, noteID) != 1 {
		t.Error("one card should have been removed")
	}
	var survivingCardID, survivingTemplateID string
	if err := tx.QueryRow(ctx, `SELECT id, template_id FROM cards WHERE note_id = $1 AND ordinal = 0`, noteID).Scan(&survivingCardID, &survivingTemplateID); err != nil {
		t.Fatalf("lookup surviving card: %v", err)
	}
	if survivingCardID != ordinal0CardID {
		t.Errorf("surviving card id changed: got %s, want %s (original)", survivingCardID, ordinal0CardID)
	}
	var basicTemplateID string
	if err := tx.QueryRow(ctx, `SELECT id FROM templates WHERE note_type_id = $1`, basicID).Scan(&basicTemplateID); err != nil {
		t.Fatalf("lookup basic template: %v", err)
	}
	if survivingTemplateID != basicTemplateID {
		t.Errorf("surviving card template_id = %s, want %s (new note type's template)", survivingTemplateID, basicTemplateID)
	}
}

// A target note type with a different field count must be rejected with 400, and nothing changed.
func TestNoteRoutes_ChangeNoteType_FieldMismatch_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	noteBody := url.Values{}
	noteBody.Set("note_type_id", basicID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, basicID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	other := url.Values{}
	other.Set("name", "Solo2")
	other.Set("css", "")
	other.Add("field_name[]", "Solo")
	other.Add("template_name[]", "Card 1")
	other.Add("qfmt[]", "{{Solo}}")
	other.Add("afmt[]", "{{Solo}}")
	doRequest(handler, "POST", "/note-types", other.Encode(), cookie, "http://example.com")
	var soloID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Solo2'`).Scan(&soloID); err != nil {
		t.Fatalf("lookup Solo2: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("note_type_id", soloID)
	editBody.Add("field[]", "Q")
	editBody.Add("field[]", "A")
	w := doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var gotNoteTypeID string
	if err := tx.QueryRow(ctx, `SELECT note_type_id FROM notes WHERE id = $1`, noteID).Scan(&gotNoteTypeID); err != nil {
		t.Fatalf("select note: %v", err)
	}
	if gotNoteTypeID != basicID {
		t.Error("note_type_id should not change on a 400")
	}
}

// A target note type with a different is_cloze flag must be rejected with 400.
func TestNoteRoutes_ChangeNoteType_ClozeMismatch_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	noteBody := url.Values{}
	noteBody.Set("note_type_id", basicID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, basicID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	ntBody := url.Values{}
	ntBody.Set("name", "Cloze2")
	ntBody.Set("css", "")
	ntBody.Add("is_cloze", "on")
	ntBody.Add("field_name[]", "Text")
	ntBody.Add("template_name[]", "Cloze")
	ntBody.Add("qfmt[]", "{{cloze:Text}}")
	ntBody.Add("afmt[]", "{{cloze:Text}}")
	doRequest(handler, "POST", "/note-types", ntBody.Encode(), cookie, "http://example.com")
	var clozeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Cloze2'`).Scan(&clozeID); err != nil {
		t.Fatalf("lookup Cloze2: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("note_type_id", clozeID)
	editBody.Add("field[]", "Q")
	editBody.Add("field[]", "A")
	w := doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// #138's ownership precedent, mirroring CreateNote: a collaborator with full access (including
// can_manage_access) on the owner's shared deck still cannot switch a note to a note type owned
// by someone else -- isolates the ownership rule from the can_manage_access gate tested by
// TestNoteRoutes_ChangeNoteType_NoManageAccess_404, below.
func TestNoteRoutes_ChangeNoteType_TargetNoteTypeOwnedByOther_404(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	collabEmail := testEmail()
	collabCookie := loginCookie(t, tx, a, collabEmail, "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	w := doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create reversed note type status = %d: %s", w.Code, w.Body.String())
	}
	var reversedID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Reversed2'`).Scan(&reversedID); err != nil {
		t.Fatalf("lookup Reversed2: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", basicID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, basicID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	var collabID string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, collabEmail).Scan(&collabID); err != nil {
		t.Fatalf("lookup collaborator: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content, can_edit_settings, can_manage_access, can_delete)
		 VALUES ($1, $2, true, true, true, true, true, true)`,
		deckID, collabID,
	); err != nil {
		t.Fatalf("grant collaborator access: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("note_type_id", reversedID) // owned by the deck owner, not the collaborator
	editBody.Add("field[]", "Q2")
	editBody.Add("field[]", "A2")
	w = doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), collabCookie, "http://example.com")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// The #138 Resolved-decision gate: a collaborator with can_view + can_edit_content but not
// can_manage_access on the owner's shared deck is refused a note-type change even when targeting
// a note type they themselves own (so the ownership check alone would pass) -- isolates the
// can_manage_access gate specifically.
func TestNoteRoutes_ChangeNoteType_NoManageAccess_404(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	collabEmail := testEmail()
	collabCookie := loginCookie(t, tx, a, collabEmail, "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	noteBody := url.Values{}
	noteBody.Set("note_type_id", basicID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, basicID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	var collabID string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, collabEmail).Scan(&collabID); err != nil {
		t.Fatalf("lookup collaborator: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_edit_content) VALUES ($1, $2, true, true)`,
		deckID, collabID,
	); err != nil {
		t.Fatalf("grant collaborator access: %v", err)
	}

	// A note type the collaborator owns, field-compatible with Basic2, so the ownership check
	// alone would pass.
	w := doRequest(handler, "POST", "/note-types", newNoteTypeBody(), collabCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create collaborator's note type status = %d: %s", w.Code, w.Body.String())
	}
	var collabNoteTypeID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM note_types WHERE name = 'Basic2' AND owner_id = $1`, collabID,
	).Scan(&collabNoteTypeID); err != nil {
		t.Fatalf("lookup collaborator's note type: %v", err)
	}

	editBody := url.Values{}
	editBody.Set("note_type_id", collabNoteTypeID)
	editBody.Add("field[]", "Q2")
	editBody.Add("field[]", "A2")
	w = doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), collabCookie, "http://example.com")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// The concrete #138 regression for the "sev: critical" silently-corrupts-scheduling-state bucket
// (CLAUDE.md §15): a note-type change on a shared deck must not touch another user's
// user_card_state/review_log for a surviving card, and must cascade/orphan a removed card's
// per-user state the same way for every user with data on it, not just the caller.
func TestNoteRoutes_ChangeNoteType_MultiuserSchedulingStatePreserved(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	studentEmail := testEmail()
	loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	w := doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create reversed note type status = %d: %s", w.Code, w.Body.String())
	}
	var reversedID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Reversed2'`).Scan(&reversedID); err != nil {
		t.Fatalf("lookup Reversed2: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", reversedID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, reversedID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}
	var card1ID, card2ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE note_id = $1 AND ordinal = 0`, noteID).Scan(&card1ID); err != nil {
		t.Fatalf("lookup card1: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE note_id = $1 AND ordinal = 1`, noteID).Scan(&card2ID); err != nil {
		t.Fatalf("lookup card2: %v", err)
	}

	var ownerID, studentID string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, ownerEmail).Scan(&ownerID); err != nil {
		t.Fatalf("lookup owner: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, studentEmail).Scan(&studentID); err != nil {
		t.Fatalf("lookup student: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID,
	); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	// Seed both users' scheduling state directly for speed (both cards, both users), the same
	// shape the grading path would leave behind.
	for _, uid := range []string{ownerID, studentID} {
		for _, cid := range []string{card1ID, card2ID} {
			if _, err := tx.Exec(ctx,
				`INSERT INTO user_card_state (user_id, card_id, due) VALUES ($1, $2, now())`,
				uid, cid,
			); err != nil {
				t.Fatalf("seed user_card_state: %v", err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO review_log
					(user_id, card_id, rating, reviewed_at, state_before, learning_steps_before,
					 elapsed_days_before, scheduled_days_after, review_kind)
				 VALUES ($1, $2, 3, now(), 0, 0, 0, 0, 0)`,
				uid, cid,
			); err != nil {
				t.Fatalf("seed review_log: %v", err)
			}
		}
	}

	editBody := url.Values{}
	editBody.Set("note_type_id", basicID) // drops card2's template/ordinal
	editBody.Add("field[]", "Q")
	editBody.Add("field[]", "A")
	editBody.Set("confirm_note_type_change", "1")
	w = doRequest(handler, "POST", "/notes/"+noteID+"/edit", editBody.Encode(), ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("note-type change status = %d, want 303: %s", w.Code, w.Body.String())
	}

	if countRows(t, tx, `SELECT count(*) FROM cards WHERE id = $1`, card2ID) != 0 {
		t.Fatal("card2 should have been removed")
	}
	for _, uid := range []string{ownerID, studentID} {
		if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`, uid, card1ID) != 1 {
			t.Errorf("card1 user_card_state for user %s should be untouched", uid)
		}
		if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`, uid, card2ID) != 0 {
			t.Errorf("card2 user_card_state for user %s should have cascaded away", uid)
		}
		if countRows(t, tx, `SELECT count(*) FROM review_log WHERE user_id = $1 AND card_id = $2`, uid, card2ID) != 1 {
			t.Errorf("card2 review_log for user %s should survive orphaned", uid)
		}
	}
}

func TestNoteRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
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
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE deck_id = $1 ORDER BY id DESC LIMIT 1`, deckID).Scan(&noteID); err != nil {
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
	deckID := strings.TrimPrefix(deckPath, "/decks/")

	ntBody := url.Values{}
	ntBody.Set("name", "Cloze2")
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
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Cloze2'`).Scan(&noteTypeID); err != nil {
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
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE deck_id = $1 ORDER BY id DESC LIMIT 1`, deckID).Scan(&noteID); err != nil {
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

	deckPath := setupDeckAndNoteType(t, handler, cookie) // note type "Basic2" has 2 fields
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "only one field")
	w := doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if countRows(t, tx, `SELECT count(*) FROM notes WHERE deck_id = $1`, deckID) != 0 {
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
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Cloze2'`).Scan(&noteTypeID); err != nil {
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

// A note type left with zero templates (no longer reachable through the app since #89 made the
// edit handler require at least one template, same as create -- so this simulates a data anomaly
// via a direct SQL delete) must reject a note creation attempt with 400, not 500, matching
// desiredCards's ErrNoCards-adjacent "cloze note type has no template" handling.
func TestNoteRoutes_CreateAgainstNoteTypeWithNoTemplates_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM templates WHERE note_type_id = $1`, noteTypeID); err != nil {
		t.Fatalf("strip templates: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	w := doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
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
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var viewerID string
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

// #182: the note-type-change dropdown on note_form.html needs can_manage_access on top of the
// can_edit_content that already gates reaching the edit page -- a collaborator without it falls
// back to the hidden fixed note_type_id input, same as a caller with no compatible note types.
func TestNoteRoutes_NoteTypeDropdownGatedByCanManageAccess(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	collabEmail := testEmail()
	collabCookie := loginCookie(t, tx, a, collabEmail, "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	// A second field-compatible note type so NoteTypeOptions is non-empty -- otherwise the
	// dropdown would be absent regardless of can_manage_access, and the test would not isolate
	// the flag.
	w := doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create reversed note type status = %d: %s", w.Code, w.Body.String())
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", basicID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, basicID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	var collabID string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, collabEmail).Scan(&collabID); err != nil {
		t.Fatalf("lookup collaborator: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_edit_content) VALUES ($1, $2, true, true)`,
		deckID, collabID,
	); err != nil {
		t.Fatalf("grant collaborator access: %v", err)
	}

	w = doRequest(handler, "GET", "/notes/"+noteID+"/edit", "", collabCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("collaborator without can_manage_access GET edit status = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `<select id="note_type_id" name="note_type_id">`) {
		t.Errorf("collaborator without can_manage_access should not see the note-type dropdown:\n%s", w.Body.String())
	}

	if _, err := tx.Exec(ctx,
		`UPDATE deck_access SET can_manage_access = true WHERE deck_id = $1 AND user_id = $2`,
		deckID, collabID,
	); err != nil {
		t.Fatalf("grant can_manage_access: %v", err)
	}
	w = doRequest(handler, "GET", "/notes/"+noteID+"/edit", "", collabCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("collaborator with can_manage_access GET edit status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `<select id="note_type_id" name="note_type_id">`) {
		t.Errorf("collaborator with can_manage_access should see the note-type dropdown:\n%s", w.Body.String())
	}
}

func TestNoteRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE deck_id = $1 ORDER BY id DESC LIMIT 1`, deckID).Scan(&noteID); err != nil {
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
		{"stranger POST note preview", "POST", "/notes/" + noteID + "/preview", "note_type_id=" + noteTypeID + "&field[]=X&field[]=Y", http.StatusNotFound},
		{"stranger POST new-note preview in owner's deck", "POST", deckPath + "/notes/preview", "note_type_id=" + noteTypeID + "&field[]=X&field[]=Y", http.StatusNotFound},
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
