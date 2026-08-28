package http

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Jolls/enshu/internal/auth"
)

func TestAIImportRoutes_GoldenPath(t *testing.T) {
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

	w := doRequest(handler, "GET", "/import/ai", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /import/ai (pick step) status = %d, want 200", w.Code)
	}

	// Linked from within a deck (deck.html's "Import via AI"): deck_id alone should skip
	// straight to picking a note type, not re-ask for the deck.
	w = doRequest(handler, "GET", "/import/ai?deck_id="+deckID, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /import/ai (deck-locked step) status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "note_type_id") {
		t.Error("deck-locked step should still offer a note-type picker")
	}
	if strings.Contains(w.Body.String(), `<select id="deck_id"`) {
		t.Error("deck-locked step should not re-ask for the deck with an editable picker")
	}

	w = doRequest(handler, "GET", "/import/ai?deck_id="+deckID+"&note_type_id="+noteTypeID, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /import/ai (prompt step) status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Front") {
		t.Error("generated prompt should mention the note type's field names")
	}

	body := url.Values{}
	body.Set("deck_id", deckID)
	body.Set("note_type_id", noteTypeID)
	body.Set("text", "{\"fields\":[\"Q1\",\"A1\"],\"tags\":[\"x\"]}\n{\"fields\":[\"Q2\",\"A2\"]}\n")
	w = doRequest(handler, "POST", "/import/ai", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /import/ai status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM notes WHERE deck_id = $1`, deckID) != 2 {
		t.Error("two notes should have been created")
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE deck_id = $1`, deckID) != 2 {
		t.Error("two cards should have been created (one template each)")
	}
}

func TestAIImportRoutes_BadLine_AllOrNothing(t *testing.T) {
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

	body := url.Values{}
	body.Set("deck_id", deckID)
	body.Set("note_type_id", noteTypeID)
	body.Set("text", "{\"fields\":[\"Q1\",\"A1\"]}\nnot json\n{\"fields\":[\"Q2\",\"A2\"]}\n")
	w := doRequest(handler, "POST", "/import/ai", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM notes WHERE deck_id = $1`, deckID) != 0 {
		t.Error("no note should have been created when any line fails")
	}
}

func TestAIImportRoutes_FieldCountMismatch_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie) // "Basic2" has 2 fields
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	body := url.Values{}
	body.Set("deck_id", deckID)
	body.Set("note_type_id", noteTypeID)
	body.Set("text", `{"fields":["only one field"]}`)
	w := doRequest(handler, "POST", "/import/ai", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if countRows(t, tx, `SELECT count(*) FROM notes WHERE deck_id = $1`, deckID) != 0 {
		t.Error("no note should have been created")
	}
}

func TestAIImportRoutes_ClozeWithoutMarkers_400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckID := strings.TrimPrefix(w.Header().Get("Location"), "/decks/")

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

	body := url.Values{}
	body.Set("deck_id", deckID)
	body.Set("note_type_id", noteTypeID)
	body.Set("text", `{"fields":["no markers here"]}`)
	w = doRequest(handler, "POST", "/import/ai", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestAIImportRoutes_AccessControl(t *testing.T) {
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

	tests := []struct {
		name, method, path, body string
		wantStatus               int
	}{
		{"stranger GET prompt", "GET", "/import/ai?deck_id=" + deckID + "&note_type_id=" + noteTypeID, "", http.StatusNotFound},
		{
			"stranger POST import", "POST", "/import/ai",
			"deck_id=" + deckID + "&note_type_id=" + noteTypeID + "&text=" + url.QueryEscape(`{"fields":["Q","A"]}`),
			http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, tt.method, tt.path, tt.body, strangerCookie, "http://example.com")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
	if countRows(t, tx, `SELECT count(*) FROM notes WHERE deck_id = $1`, deckID) != 0 {
		t.Error("stranger's request must not have created a note")
	}
}

