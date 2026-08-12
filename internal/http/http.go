// Package http holds HTTP handlers, one file per aggregate: decks, notes, review, access.
package http

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewMux builds the application's top-level router.
func NewMux(pool *pgxpool.Pool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(pool))
	return mux
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
