package http

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/web"
)

// externalAsset matches a <link>/<script> whose href/src names another host.
var externalAsset = regexp.MustCompile(`(?i)<(?:link|script)[^>]+(?:href|src)="(?:https?:)?//`)

func TestLayout_NoExternalHosts(t *testing.T) {
	files, err := fs.Glob(web.Templates, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := fs.ReadFile(web.Templates, f)
		if err != nil {
			t.Fatal(err)
		}
		if m := externalAsset.Find(b); m != nil {
			t.Errorf("%s loads an external asset: %s -- vendor it under web/static/", f, m)
		}
	}
}

func TestStatic_PicoServed(t *testing.T) {
	mux := http.NewServeMux()
	registerStaticRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/pico.min.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/pico.min.css = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Pico") {
		t.Error("body does not look like Pico CSS (no banner)")
	}
}
