package http

import (
	"html/template"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare"
	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// showWhatsNew reports whether a user who last saw lastSeen has release notes they haven't read.
// Compares against the newest *noted* version, so internal-only releases never raise the bar.
func showWhatsNew(lastSeen string) bool {
	return deckshare.VersionLess(lastSeen, deckshare.LatestNotedVersion())
}

type releaseNotesView struct {
	User      db.User
	Notes     []deckshare.ReleaseNote
	BodyClass string
}

func registerReleaseNotesRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	// Opening the page counts as reading it: the write is the caller's own row, and the version
	// is the running binary's, never client input.
	mux.Handle("GET /release-notes", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if err := markVersionSeen(r, store, user.ID); err != nil {
			serverError(w, r, err)
			return
		}
		user.LastSeenVersion = appVersion // so this very response has no what's-new bar
		render(w, pages["release_notes"], http.StatusOK, releaseNotesView{User: user, Notes: deckshare.ReleaseNotes()})
	})))

	mux.Handle("POST /release-notes/dismiss", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if err := markVersionSeen(r, store, user.ID); err != nil {
			serverError(w, r, err)
			return
		}
		http.Redirect(w, r, "/decks", http.StatusSeeOther)
	})))
}

func markVersionSeen(r *http.Request, store db.Beginner, userID pgtype.UUID) error {
	return db.New(store).UpdateUserLastSeenVersion(r.Context(), db.UpdateUserLastSeenVersionParams{
		ID: userID, LastSeenVersion: appVersion,
	})
}
