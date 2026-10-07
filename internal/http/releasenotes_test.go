package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/deckshare/internal/auth"
)

func lastSeenOf(t *testing.T, tx pgx.Tx, id string) string {
	t.Helper()
	var s string
	if err := tx.QueryRow(context.Background(), `SELECT last_seen_version FROM users WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read last_seen_version: %v", err)
	}
	return s
}

func setLastSeen(t *testing.T, tx pgx.Tx, id, v string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `UPDATE users SET last_seen_version = $2 WHERE id = $1`, id, v); err != nil {
		t.Fatalf("set last_seen_version: %v", err)
	}
}

func TestReleaseNotesRoutes_AllowDeny(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	tests := []struct {
		name, method, path string
		cookie             *http.Cookie
		want               int
	}{
		{"GET no session", "GET", "/release-notes", nil, 303},
		{"GET valid session", "GET", "/release-notes", cookie, 200},
		{"POST dismiss no session", "POST", "/release-notes/dismiss", nil, 303},
		{"POST dismiss valid session", "POST", "/release-notes/dismiss", cookie, 303},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, tt.method, tt.path, "", tt.cookie, "http://example.com")
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

func TestWhatsNewBar_ShowsUntilSeen(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	id := userID(t, context.Background(), tx, email)

	if got := lastSeenOf(t, tx, id); got != appVersion {
		t.Errorf("new signup last_seen_version = %q, want %q", got, appVersion)
	}
	w := doRequest(handler, "GET", "/decks", "", cookie, "")
	if strings.Contains(w.Body.String(), "whats-new") {
		t.Error("new signup should not see the what's-new bar")
	}

	setLastSeen(t, tx, id, "")
	w = doRequest(handler, "GET", "/decks", "", cookie, "")
	if !strings.Contains(w.Body.String(), "whats-new") {
		t.Error("user who has not seen the notes should see the bar")
	}
	if !strings.Contains(w.Body.String(), `href="/release-notes">Version `+appVersion) {
		t.Error("footer should show the version and link to the notes")
	}

	w = doRequest(handler, "POST", "/release-notes/dismiss", "", cookie, "http://example.com")
	if w.Code != 303 {
		t.Fatalf("dismiss status = %d, want 303", w.Code)
	}
	if got := lastSeenOf(t, tx, id); got != appVersion {
		t.Errorf("after dismiss last_seen_version = %q, want %q", got, appVersion)
	}
	w = doRequest(handler, "GET", "/decks", "", cookie, "")
	if strings.Contains(w.Body.String(), "whats-new") {
		t.Error("bar should be gone after dismiss")
	}
}

func TestReleaseNotesPage_MarksSeenAndOnlyCallerRow(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ctx := context.Background()
	emailA, emailB := testEmail(), testEmail()
	cookieA := loginCookie(t, tx, a, emailA, "correct-horse-battery")
	loginCookie(t, tx, a, emailB, "correct-horse-battery")
	idA, idB := userID(t, ctx, tx, emailA), userID(t, ctx, tx, emailB)
	setLastSeen(t, tx, idA, "")
	setLastSeen(t, tx, idB, "")

	w := doRequest(handler, "GET", "/release-notes", "", cookieA, "")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "whats-new") {
		t.Error("the notes page itself should not show the bar")
	}
	if !strings.Contains(w.Body.String(), "Release notes") {
		t.Error("page missing heading")
	}
	if got := lastSeenOf(t, tx, idA); got != appVersion {
		t.Errorf("caller last_seen_version = %q, want %q", got, appVersion)
	}
	if got := lastSeenOf(t, tx, idB); got != "" {
		t.Errorf("other user's last_seen_version = %q, want unchanged", got)
	}
}
