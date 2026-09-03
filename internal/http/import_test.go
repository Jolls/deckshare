package http

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/auth"
)

// mathFixture is the real schema-18 export used to close #61 -- see
// tests/fixtures/apkg/README.md. Reading it through the actual /import route (not apkg.Read
// directly) is what CLAUDE.md §10.3 calls the "real-fixture end-to-end" check: it exercises the
// multipart upload, the tx-wrapped apkg.Import call, and the resulting-deck redirect together.
const mathFixture = "../../tests/fixtures/apkg/mathematics-schema18.apkg"

func doUploadRequest(t *testing.T, handler http.Handler, path, fieldName, filename string, data []byte, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	r := httptest.NewRequest("POST", path, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Host = "example.com"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestImportRoutes_RealFixture_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	data, err := os.ReadFile(mathFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	w := doRequest(handler, "GET", "/import", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /import status = %d, want 200", w.Code)
	}

	w = doUploadRequest(t, handler, "/import", "file", "mathematics-schema18.apkg", data, cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /import status = %d, want 303, body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc == "/decks" {
		t.Fatalf("Location = %q, want /decks/{id} -- the deck the fixture's 11 cards landed in", loc)
	}

	w = doRequest(handler, "GET", loc, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, w.Code)
	}
	if !strings.Contains(w.Body.String(), "Limits, Functions, Continuity") {
		t.Errorf("deck page does not show the imported deck's name")
	}

	// Re-importing the same package is idempotent (CLAUDE.md §2.2): same deck, no duplicate rows.
	w = doUploadRequest(t, handler, "/import", "file", "mathematics-schema18.apkg", data, cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("re-import status = %d, want 303", w.Code)
	}
	if got := w.Header().Get("Location"); got != loc {
		t.Errorf("re-import Location = %q, want same deck %q", got, loc)
	}
}

func TestImportRoutes_NoFile(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/import", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Host = "example.com"
	r.Header.Set("Origin", "http://example.com")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestImportRoutes_MalformedPackage(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doUploadRequest(t, handler, "/import", "file", "not-a-package.apkg", []byte("not a zip file"), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestImportRoutes_NoOrigin_403(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doUploadRequest(t, handler, "/import", "file", "x.apkg", []byte("irrelevant"), cookie, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
