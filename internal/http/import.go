package http

import (
	"html/template"
	"net/http"
	"time"

	"github.com/Jolls/enshu/internal/apkg"
	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/media"
)

// maxUploadBytes bounds the raw multipart request body -- ahead of apkg.DefaultArchiveLimits,
// which bounds decompressed member bytes, not the compressed upload itself.
const maxUploadBytes = 550 << 20

// registerImportRoutes wires GET/POST /import (docs/routes.md): upload a .apkg, synchronous
// read -> IR -> db (CLAUDE.md §9's Simplicity First -- no job queue for MVP), redirect to the
// resulting deck.
func registerImportRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template, blobs *media.Store, now func() time.Time) {
	mux.Handle("GET /import", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		render(w, pages["import"], http.StatusOK, map[string]any{"User": user})
	})))

	mux.Handle("POST /import", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())

		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			render(w, pages["import"], http.StatusBadRequest, map[string]any{
				"User": user, "Error": "Upload too large or malformed",
			})
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			render(w, pages["import"], http.StatusBadRequest, map[string]any{
				"User": user, "Error": "Choose a .apkg file to import",
			})
			return
		}
		defer func() { _ = file.Close() }()

		col, err := apkg.Read(file, header.Size, apkg.DefaultArchiveLimits())
		if err != nil {
			render(w, pages["import"], http.StatusBadRequest, map[string]any{
				"User": user, "Error": "Could not read package: " + err.Error(),
			})
			return
		}

		tx, err := store.Begin(r.Context())
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		result, err := apkg.Import(r.Context(), tx, user.ID, col, now(), blobs)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, resultingDeckPath(result.Decks), http.StatusSeeOther)
	})))
}

// resultingDeckPath picks the deck /import redirects to: whichever deck this import's cards
// actually landed in (the largest ImportedDeck.CardCount), since a package's own Decks list
// doesn't say that -- Anki collections routinely carry an untouched "Default" deck alongside the
// one actually exported. Falls back to the deck list if nothing resolved (e.g. an empty package).
func resultingDeckPath(decks []apkg.ImportedDeck) string {
	var best *apkg.ImportedDeck
	for i := range decks {
		if best == nil || decks[i].CardCount > best.CardCount {
			best = &decks[i]
		}
	}
	if best == nil || best.CardCount == 0 {
		return "/decks"
	}
	return "/decks/" + best.ID.String()
}
