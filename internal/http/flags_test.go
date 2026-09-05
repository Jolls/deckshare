package http

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/auth"
)

func TestFlagRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	studentEmail := testEmail()
	studentCookie := loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	flagPath := "/decks/" + deckID + "/cards/flags"
	form := url.Values{}
	form.Set("cardId", cardID)
	form.Set("comment", "This card's answer looks wrong")
	w := doRequest(handler, "POST", flagPath, form.Encode(), studentCookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("POST flag status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Flagged") {
		t.Errorf("expected confirmation in body: %s", w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM card_flags
		WHERE card_id = $1 AND flagged_by_user_id = $2 AND status = 'open'`, cardID, studentID); n != 1 {
		t.Fatalf("expected exactly one open flag, got %d", n)
	}

	// Resubmitting while the flag is still open replaces the comment rather than adding a row.
	form.Set("comment", "Actually, the whole card is malformed")
	w = doRequest(handler, "POST", flagPath, form.Encode(), studentCookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("resubmit flag status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM card_flags WHERE card_id = $1 AND flagged_by_user_id = $2`,
		cardID, studentID); n != 1 {
		t.Fatalf("resubmission should replace the open flag, not add a row; got %d rows", n)
	}
	var comment string
	if err := tx.QueryRow(ctx, `SELECT comment FROM card_flags WHERE card_id = $1 AND flagged_by_user_id = $2`,
		cardID, studentID).Scan(&comment); err != nil {
		t.Fatalf("read back comment: %v", err)
	}
	if comment != "Actually, the whole card is malformed" {
		t.Errorf("comment = %q, want the resubmitted text", comment)
	}

	listPath := "/decks/" + deckID + "/flags"
	w = doRequest(handler, "GET", listPath, "", ownerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: %s", listPath, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), studentEmail) || !strings.Contains(w.Body.String(), "malformed") {
		t.Errorf("open list missing the flag:\n%s", w.Body.String())
	}

	var flagID string
	if err := tx.QueryRow(ctx, `SELECT id FROM card_flags WHERE card_id = $1 AND flagged_by_user_id = $2`,
		cardID, studentID).Scan(&flagID); err != nil {
		t.Fatalf("lookup flag id: %v", err)
	}

	resolvePath := listPath + "/" + flagID + "/resolve"
	w = doRequest(handler, "POST", resolvePath, "", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST resolve status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != listPath {
		t.Errorf("Location = %q, want %q", loc, listPath)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM card_flags WHERE id = $1 AND status = 'resolved' AND resolved_by_user_id IS NOT NULL`,
		flagID); n != 1 {
		t.Error("resolve should have marked the flag resolved")
	}

	w = doRequest(handler, "GET", listPath, "", ownerCookie, "")
	if strings.Contains(w.Body.String(), "malformed") {
		t.Error("resolved flag should not appear on the default (open) list")
	}
	w = doRequest(handler, "GET", listPath+"?status=resolved", "", ownerCookie, "")
	if !strings.Contains(w.Body.String(), "malformed") {
		t.Error("resolved flag should appear on the resolved list")
	}
}

// Every flags route collapses "deck absent", "deck invisible", and "caller lacks the relevant
// flag" into one 404 (CLAUDE.md §10.5, docs/schema.md) -- and specifically can_study without
// can_view_flags must not grant list/resolve, and can_manage_access without can_view_flags must
// not either (the same shape #87's plan required for can_view_progress).
func TestFlagRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	viewOnlyEmail := testEmail()
	viewOnlyCookie := loginCookie(t, tx, a, viewOnlyEmail, "correct-horse-battery")
	viewOnlyID := userID(t, ctx, tx, viewOnlyEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`,
		deckID, viewOnlyID); err != nil {
		t.Fatalf("grant view-only access: %v", err)
	}

	studentEmail := testEmail()
	studentCookie := loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	managerEmail := testEmail()
	managerCookie := loginCookie(t, tx, a, managerEmail, "correct-horse-battery")
	managerID := userID(t, ctx, tx, managerEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_manage_access) VALUES ($1, $2, true, true)`,
		deckID, managerID); err != nil {
		t.Fatalf("grant manager access: %v", err)
	}

	flagPath := "/decks/" + deckID + "/cards/flags"
	form := url.Values{}
	form.Set("cardId", cardID)
	form.Set("comment", "flag for access-control test")

	createTests := []struct {
		name       string
		cookie     *http.Cookie
		wantStatus int
	}{
		{"stranger create", strangerCookie, http.StatusNotFound},
		{"view-only (no can_study) create", viewOnlyCookie, http.StatusNotFound},
		{"student create", studentCookie, http.StatusOK},
	}
	for _, tt := range createTests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "POST", flagPath, form.Encode(), tt.cookie, "http://example.com")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}

	var flagID string
	if err := tx.QueryRow(ctx, `SELECT id FROM card_flags WHERE card_id = $1 AND flagged_by_user_id = $2`,
		cardID, studentID).Scan(&flagID); err != nil {
		t.Fatalf("lookup flag id: %v", err)
	}

	listPath := "/decks/" + deckID + "/flags"
	getTests := []struct {
		name       string
		cookie     *http.Cookie
		wantStatus int
	}{
		{"stranger GET list", strangerCookie, http.StatusNotFound},
		{"student (can_study, no can_view_flags) GET list", studentCookie, http.StatusNotFound},
		{"manager (can_manage_access, no can_view_flags) GET list", managerCookie, http.StatusNotFound},
		{"owner GET list", ownerCookie, http.StatusOK},
	}
	for _, tt := range getTests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "GET", listPath, "", tt.cookie, "")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}

	resolveTests := []struct {
		name       string
		cookie     *http.Cookie
		wantStatus int
	}{
		{"stranger resolve", strangerCookie, http.StatusNotFound},
		{"manager (no can_view_flags) resolve", managerCookie, http.StatusNotFound},
	}
	for _, tt := range resolveTests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "POST", listPath+"/"+flagID+"/resolve", "", tt.cookie, "http://example.com")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}

	// A flag id that's real, but for a different deck than the URL names, must 404 -- the URL's
	// {id} segment is bound into ResolveCardFlag's WHERE, not just derived from the flag itself.
	otherDeckPath := "/decks"
	w := doRequest(handler, "POST", otherDeckPath, "name=Other Deck", ownerCookie, "http://example.com")
	otherDeckID := strings.TrimPrefix(w.Header().Get("Location"), "/decks/")
	w = doRequest(handler, "POST", "/decks/"+otherDeckID+"/flags/"+flagID+"/resolve", "", ownerCookie, "http://example.com")
	if w.Code != http.StatusNotFound {
		t.Errorf("resolving deck A's flag via deck B's URL status = %d, want 404: %s", w.Code, w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM card_flags WHERE id = $1 AND status = 'open'`, flagID); n != 1 {
		t.Error("the cross-deck resolve attempt must not have resolved the flag")
	}
}
