package http

import (
	"net/http"
	"testing"

	"github.com/Jolls/enshu/internal/auth"
)

func TestDeckRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck&description=desc", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /decks status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc == "/decks" {
		t.Fatalf("Location = %q, want /decks/{id}", loc)
	}

	w = doRequest(handler, "GET", loc, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, w.Code)
	}

	w = doRequest(handler, "GET", "/decks", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /decks status = %d, want 200", w.Code)
	}

	w = doRequest(handler, "POST", loc+"/edit", "name=Renamed&description=", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303", w.Code)
	}

	w = doRequest(handler, "POST", loc+"/delete", "", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete status = %d, want 303", w.Code)
	}

	w = doRequest(handler, "GET", loc, "", cookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET deleted deck status = %d, want 404", w.Code)
	}
}

// A deck that exists but is invisible to the caller must 404, never 403 -- one row per route
// asserting exactly that (CLAUDE.md §10.5).
func TestDeckRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	tests := []struct {
		name, method, path, body string
		cookie                   *http.Cookie
		wantStatus               int
	}{
		{"no session GET deck", "GET", deckPath, "", nil, http.StatusSeeOther},
		{"stranger GET deck", "GET", deckPath, "", strangerCookie, http.StatusNotFound},
		{"stranger GET edit", "GET", deckPath + "/edit", "", strangerCookie, http.StatusNotFound},
		{"stranger POST edit", "POST", deckPath + "/edit", "name=Hijacked", strangerCookie, http.StatusNotFound},
		{"stranger POST delete", "POST", deckPath + "/delete", "", strangerCookie, http.StatusNotFound},
		{"nonexistent deck", "GET", "/decks/00000000-0000-0000-0000-000000000000", "", ownerCookie, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := ""
			if tt.method == "POST" {
				origin = "http://example.com"
			}
			w := doRequest(handler, tt.method, tt.path, tt.body, tt.cookie, origin)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestDeckRoutes_DuplicateName_409(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	doRequest(handler, "POST", "/decks", "name=Dup", cookie, "http://example.com")
	w := doRequest(handler, "POST", "/decks", "name=Dup", cookie, "http://example.com")
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}
