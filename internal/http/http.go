// Package http holds HTTP handlers, one file per aggregate: decks, notes, review, access.
package http

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/enshu/internal/auth"
)

// NewHandler builds the application's top-level handler: the route mux wrapped in the auth
// middleware, so the CSRF check and session population run for every request
// (architecture.md §12).
func NewHandler(pool *pgxpool.Pool, a *auth.Service) (http.Handler, error) {
	pages, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(pool))
	registerAuthRoutes(mux, a, pages)
	registerSettingsRoutes(mux, a, pages)
	registerDeckRoutes(mux, pool, pages)
	registerNoteTypeRoutes(mux, pool, pages)
	registerNoteRoutes(mux, pool, pages)
	return a.Middleware(mux), nil
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
