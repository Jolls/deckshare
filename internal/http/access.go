package http

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
)

// accessFlags is the six deck_access permissions as a form carries them. can_view being a
// practical prerequisite for the other five is an application-level convention that the schema
// deliberately does not enforce (migrations/00007_deck_access.sql), and this package does not
// enforce it either -- the form defaults it checked and leaves the choice to the manager.
type accessFlags struct {
	CanView         bool
	CanStudy        bool
	CanEditContent  bool
	CanEditSettings bool
	CanManageAccess bool
	CanDelete       bool
}

func flagsFromForm(form url.Values) accessFlags {
	return accessFlags{
		CanView:         form.Get("can_view") == "on",
		CanStudy:        form.Get("can_study") == "on",
		CanEditContent:  form.Get("can_edit_content") == "on",
		CanEditSettings: form.Get("can_edit_settings") == "on",
		CanManageAccess: form.Get("can_manage_access") == "on",
		CanDelete:       form.Get("can_delete") == "on",
	}
}

func registerAccessRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /decks/{id}/access", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForAccessManage(r.Context(), db.GetDeckForAccessManageParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}
		renderAccess(r.Context(), w, pages, q, user, deck, http.StatusOK, "")
	})))

	mux.Handle("POST /decks/{id}/access", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		if !parseForm(w, r) {
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForAccessManage(r.Context(), db.GetDeckForAccessManageParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		// Looked up separately rather than folded into the INSERT so "no such account" stays a
		// form error, distinct from the deck-level 404. Granting to an address with no account
		// is out of scope -- there is no pending-invite flow (#83).
		email := strings.TrimSpace(r.PostForm.Get("email"))
		target, err := q.GetUserByEmail(r.Context(), email)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				renderAccess(r.Context(), w, pages, q, user, deck, http.StatusBadRequest, "No account with that email address")
				return
			}
			serverError(w)
			return
		}

		flags := flagsFromForm(r.PostForm)
		rows, err := grantDeckAccess(r.Context(), store, db.GrantDeckAccessParams{
			DeckID: deckID, CallerUserID: user.ID, TargetUserID: target.ID,
			CanView: flags.CanView, CanStudy: flags.CanStudy, CanEditContent: flags.CanEditContent,
			CanEditSettings: flags.CanEditSettings, CanManageAccess: flags.CanManageAccess,
			CanDelete: flags.CanDelete,
		})
		if err != nil {
			if db.IsUniqueViolation(err, "deck_access_pkey") {
				renderAccess(r.Context(), w, pages, q, user, deck, http.StatusConflict, "That user already has access to this deck")
				return
			}
			serverError(w)
			return
		}
		if rows == 0 {
			// The caller's can_view/can_manage_access was revoked between the GetDeckForAccessManage
			// read above and this insert -- GrantDeckAccess re-checks it atomically, so treat the
			// race the same as any other loss of access.
			notFoundPage(w, pages, user)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String()+"/access", http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/access/{userId}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		targetUserID, ok := pathUUID(r, "userId")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		if !parseForm(w, r) {
			return
		}
		flags := flagsFromForm(r.PostForm)

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		// SetDeckAccess authorises, locks the deck, applies the change, and re-counts holders in
		// one transaction; on ErrLastAccessHolder the change is already applied and only the
		// rollback above undoes it (internal/db/deletion.go).
		err := db.SetDeckAccess(r.Context(), tx, user.ID, db.UpdateDeckAccessRowParams{
			DeckID: deckID, TargetUserID: targetUserID,
			CanView: flags.CanView, CanStudy: flags.CanStudy, CanEditContent: flags.CanEditContent,
			CanEditSettings: flags.CanEditSettings, CanManageAccess: flags.CanManageAccess,
			CanDelete: flags.CanDelete,
		})
		if !handleAccessChangeErr(w, pages, user, err) {
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String()+"/access", http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/access/{userId}/delete", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		targetUserID, ok := pathUUID(r, "userId")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		if !handleAccessChangeErr(w, pages, user, db.RevokeDeckAccess(r.Context(), tx, deckID, user.ID, targetUserID)) {
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String()+"/access", http.StatusSeeOther)
	})))
}

// grantDeckAccess runs the INSERT in its own transaction (a savepoint, since store is the shared
// per-test tx under test) so a duplicate-grant 23505 is contained: the rollback happens before
// this returns, leaving the caller's connection/transaction usable for the 409 re-render's
// ListDeckAccessForDeck instead of inheriting an aborted one. Caller authorization is re-verified
// inside GrantDeckAccess itself (deck_access.sql), not by this wrapper.
func grantDeckAccess(ctx context.Context, store db.Beginner, arg db.GrantDeckAccessParams) (int64, error) {
	tx, err := store.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := db.New(tx).GrantDeckAccess(ctx, arg)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return rows, nil
}

// handleAccessChangeErr maps a db.SetDeckAccess/db.RevokeDeckAccess error to a response and
// reports whether the caller may continue. pgx.ErrNoRows covers both "the caller lacks
// can_manage_access on this deck" and "the target has no row" -- collapsed to 404 by the same
// rule as every other deck route (docs/schema.md).
func handleAccessChangeErr(w http.ResponseWriter, pages map[string]*template.Template, user db.User, err error) (ok bool) {
	switch {
	case err == nil:
		return true
	case errors.Is(err, pgx.ErrNoRows):
		notFoundPage(w, pages, user)
	case errors.Is(err, db.ErrLastAccessHolder):
		http.Error(w, "a deck must keep at least one member who can manage access and one who can delete it", http.StatusConflict)
	default:
		serverError(w)
	}
	return false
}

// renderAccess draws the access page with a freshly read collaborator list, so the POST error
// re-renders show the same rows a plain GET would. deck has already been authorised by
// GetDeckForAccessManage, so the list needs no further permission check of its own.
func renderAccess(ctx context.Context, w http.ResponseWriter, pages map[string]*template.Template, q *db.Queries, user db.User, deck db.Deck, status int, errMsg string) {
	collaborators, err := q.ListDeckAccessForDeck(ctx, deck.ID)
	if err != nil {
		serverError(w)
		return
	}
	render(w, pages["access"], status, map[string]any{
		"User": user, "Deck": deck, "Collaborators": collaborators, "Error": errMsg,
	})
}
