package http

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/apkg"
	"github.com/Jolls/deckshare/internal/auth"
)

// TestExportRoute_GoldenPath imports the real schema-18 fixture through /import, then exports the
// resulting deck back out through /decks/{id}/export and re-reads the response with apkg.Read --
// the HTTP half of CLAUDE.md §10.3's round-trip target (#140).
func TestExportRoute_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	data, err := os.ReadFile(mathFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	w := doUploadRequest(t, handler, "/import", "file", "mathematics-schema18.apkg", data, cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /import status = %d, want 303, body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc == "/decks" {
		t.Fatalf("Location = %q, want /decks/{id}", loc)
	}
	deckID := strings.TrimPrefix(loc, "/decks/")

	w = doRequest(handler, "GET", loc+"/export", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s/export status = %d, want 200, body=%s", loc, w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="`) || !strings.HasSuffix(cd, `.apkg"`) {
		t.Errorf("Content-Disposition = %q, want an attachment .apkg filename", cd)
	}

	body := w.Body.Bytes()
	col, err := apkg.Read(bytes.NewReader(body), int64(len(body)), apkg.DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("re-reading the exported package: %v", err)
	}
	if len(col.Decks) != 1 {
		t.Errorf("exported package has %d decks, want 1", len(col.Decks))
	}
	// Derived from what the import actually produced rather than hardcoded: the export set is
	// exactly the cards whose own deck_id is this deck.
	wantCards := countRows(t, tx, `SELECT count(*) FROM cards WHERE deck_id = $1`, deckID)
	if wantCards == 0 {
		t.Fatal("the imported deck has no cards; the fixture assertion below would be vacuous")
	}
	if int64(len(col.Cards)) != wantCards {
		t.Errorf("exported package has %d cards, want %d", len(col.Cards), wantCards)
	}
}

// TestExportRoute_NoAccessIs404 is the authorisation half: a signed-in user with no deck_access
// row on someone else's deck gets the 404 page, not the package (CLAUDE.md §9).
func TestExportRoute_NoAccessIs404(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /decks status = %d, want 303", w.Code)
	}
	deckPath := w.Header().Get("Location")

	w = doRequest(handler, "GET", deckPath+"/export", "", strangerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger export status = %d, want 404", w.Code)
	}
	if bytes.HasPrefix(w.Body.Bytes(), []byte("PK\x03\x04")) {
		t.Error("stranger received a package body")
	}

	w = doRequest(handler, "GET", "/decks/not-a-uuid/export", "", ownerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("malformed id status = %d, want 404", w.Code)
	}
}

// TestExportFilename is a pure unit test of the Content-Disposition sanitiser -- no DB, so it runs
// with DATABASE_URL unset.
func TestExportFilename(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Japanese Core", "Japanese_Core.apkg"},
		{"Default::Sub", "Default_Sub.apkg"},
		{`a"b\c`, "a_b_c.apkg"},
		{"日本語", "deck.apkg"},
		{"", "deck.apkg"},
		{"...", "deck.apkg"},
	}
	for _, tt := range tests {
		if got := exportFilename(tt.in); got != tt.want {
			t.Errorf("exportFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	if got := exportFilename(strings.Repeat("x", 200)); len(got) > 85 {
		t.Errorf("exportFilename(200 chars) = %q (len %d), want <= 85", got, len(got))
	}
}
