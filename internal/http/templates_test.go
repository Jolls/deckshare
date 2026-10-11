package http

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #222: render/renderFragment must set Content-Type themselves. The recorder never sniffs (see
// TestDeckRoutes_NotFoundRendersStyledPage), so an unset header is directly observable; on a real
// server an empty fragment would sniff as text/plain.
func TestRender_SetsHTMLContentType(t *testing.T) {
	const want = "text/html; charset=utf-8"
	empty := template.Must(template.New("empty").Parse(""))
	page := template.Must(template.New("layout").Parse("<!doctype html>"))

	w := httptest.NewRecorder()
	render(w, page, http.StatusOK, nil)
	if got := w.Header().Get("Content-Type"); got != want {
		t.Errorf("render Content-Type = %q, want %q", got, want)
	}

	w = httptest.NewRecorder()
	renderFragment(w, empty, http.StatusOK, "empty", nil)
	if got := w.Header().Get("Content-Type"); got != want {
		t.Errorf("renderFragment (empty body) Content-Type = %q, want %q", got, want)
	}

	// The 500 path (template execution error) must not fall back to sniffing either.
	broken := template.Must(template.New("broken").Parse("{{.Missing}}"))
	w = httptest.NewRecorder()
	renderFragment(w, broken, http.StatusOK, "broken", struct{}{})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != want {
		t.Errorf("renderFragment (error path) Content-Type = %q, want %q", got, want)
	}
}
