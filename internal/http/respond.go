package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/enshu/internal/db"
)

// serverError writes the generic 500 response for an unexpected error, without leaking it to
// the client.
func serverError(w http.ResponseWriter) {
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// badRequest writes the generic 400 response for a malformed request.
func badRequest(w http.ResponseWriter) {
	http.Error(w, "bad request", http.StatusBadRequest)
}

// handleQueryErr writes the response for a query error and reports whether it wrote one: 404 if
// the row is absent or not visible to this caller (pgx.ErrNoRows, which a GetXForUser/
// GetXForOwner query returns for both cases via its deck_access join -- CLAUDE.md §9), otherwise
// a bare 500. err == nil always reports false. The caller must return immediately when this
// reports true.
func handleQueryErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
	} else {
		serverError(w)
	}
	return true
}

// parseForm calls r.ParseForm, writing a 400 and reporting false if the request body is
// malformed. The caller must return immediately when this reports false.
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		badRequest(w)
		return false
	}
	return true
}

// startTx begins a transaction, writing a 500 and reporting ok=false on failure. On success the
// caller must defer tx.Rollback(ctx) (a no-op after a successful commitTx) before doing anything
// else with tx.
func startTx(ctx context.Context, w http.ResponseWriter, store db.Beginner) (pgx.Tx, bool) {
	tx, err := store.Begin(ctx)
	if err != nil {
		serverError(w)
		return nil, false
	}
	return tx, true
}

// commitTx commits tx, writing a 500 and reporting false on failure.
func commitTx(ctx context.Context, w http.ResponseWriter, tx pgx.Tx) bool {
	if err := tx.Commit(ctx); err != nil {
		serverError(w)
		return false
	}
	return true
}
