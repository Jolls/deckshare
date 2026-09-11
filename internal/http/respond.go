package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// respondJSON writes body as a JSON response with the given status code. Used by routes the
// client applies to its own in-memory state rather than swapping in server-rendered HTML (#223's
// cards/state route) -- unlike flags.go's fragment-typed responses.
func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// serverError writes the generic 500 response for an unexpected error and logs the cause. The
// client-facing body is unchanged and never carries err -- §2.7 and CLAUDE.md §10.1: the error
// detail goes to the operator, never to the caller.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	user, _ := auth.UserFromContext(r.Context())
	slog.Error("server error",
		"method", r.Method,
		"path", r.URL.Path,
		"user_id", user.ID.String(),
		"error", err,
	)
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
func handleQueryErr(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
	} else {
		serverError(w, r, err)
	}
	return true
}

// handleQueryErrPage is handleQueryErr for a page route: same pgx.ErrNoRows → 404 collapse, but
// rendered through notFoundPage instead of the bare-text notFound. Everything else delegates, so
// the ErrNoRows-else-500 policy stays defined in exactly one place.
func handleQueryErrPage(w http.ResponseWriter, r *http.Request, pages map[string]*template.Template, user db.User, err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		notFoundPage(w, pages, user)
		return true
	}
	return handleQueryErr(w, r, err)
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
func startTx(w http.ResponseWriter, r *http.Request, store db.Beginner) (pgx.Tx, bool) {
	tx, err := store.Begin(r.Context())
	if err != nil {
		serverError(w, r, fmt.Errorf("begin transaction: %w", err))
		return nil, false
	}
	return tx, true
}

// commitTx commits tx, writing a 500 and reporting false on failure.
func commitTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, fmt.Errorf("commit transaction: %w", err))
		return false
	}
	return true
}
