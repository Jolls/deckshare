// Package http holds HTTP handlers, one file per aggregate: decks, notes, review, media, access.
package http

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/media"
)

// NewHandler builds the application's top-level handler: the route mux wrapped in the auth
// middleware, so the CSRF check and session population run for every request
// (architecture.md §12), wrapped in turn by securityHeaders so the CSP is set on every
// response including the ones auth rejects (security.go).
func NewHandler(pool *pgxpool.Pool, a *auth.Service, blobs *media.Store) (http.Handler, error) {
	pages, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	fragments, err := parseFragments()
	if err != nil {
		return nil, fmt.Errorf("parse fragments: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(pool))
	registerStaticRoutes(mux)
	registerAuthRoutes(mux, a, pages)
	registerSettingsRoutes(mux, a, pool, pages, blobs)
	registerDeckRoutes(mux, pool, pages, time.Now)
	registerAccessRoutes(mux, pool, pages)
	registerProgressRoutes(mux, pool, pages, time.Now)
	registerFlagRoutes(mux, pool, pages, fragments)
	registerNoteTypeRoutes(mux, pool, pages)
	registerNoteRoutes(mux, pool, pages)
	registerNotePreviewRoutes(mux, pool, fragments)
	registerReviewRoutes(mux, pool, pages, fragments, time.Now)
	registerMediaRoutes(mux, pool, blobs)
	registerImportRoutes(mux, pool, pages, blobs, time.Now)
	registerExportRoutes(mux, pool, pages, time.Now)
	registerAIImportRoutes(mux, pool, pages)
	return securityHeaders(a.Middleware(mux)), nil
}

func healthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
