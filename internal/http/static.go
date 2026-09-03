package http

import (
	"io/fs"
	"log"
	"net/http"

	"github.com/Jolls/deckshare/web"
)

// registerStaticRoutes serves the vendored JS under /static/ -- public, no session, since these
// are the same bytes for everyone and the login page loads htmx too.
func registerStaticRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		log.Fatalf("static: %v", err)
	}
	fileServer := http.FileServerFS(sub)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})))
}
