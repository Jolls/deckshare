package http

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Jolls/enshu/internal/apkg"
	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
)

// registerExportRoutes wires GET /decks/{id}/export (docs/routes.md): one deck's content plus the
// CALLER's own progress on it, serialised db -> IR -> .apkg and sent as a download. can_view, not
// can_edit_* -- exporting reads the deck, it does not change it. Authorisation is enforced inside
// apkg.Export's queries (export.sql's deck_access joins, CLAUDE.md §9); no access surfaces here as
// a wrapped pgx.ErrNoRows and collapses to 404, the same as every other deck route.
func registerExportRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template, now func() time.Time) {
	mux.Handle("GET /decks/{id}/export", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}

		// Read-only: the transaction exists because apkg.Export requires one (it reads eight
		// statements that must see one consistent snapshot), and it is only ever rolled back --
		// there is deliberately no commitTx here.
		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		col, err := apkg.Export(r.Context(), tx, deckID, user.ID, now())
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		// Buffered rather than streamed straight into w: a Write failure partway through would
		// otherwise land after the 200 and the Content-Disposition, leaving the browser a
		// truncated .apkg it believes is complete. Packages are already held whole in memory on
		// the /import side.
		var buf bytes.Buffer
		if err := apkg.Write(col, &buf); err != nil {
			serverError(w)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+exportFilename(col.Decks[0].Name)+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = buf.WriteTo(w)
	})))
}

// exportFilename turns a deck name into a Content-Disposition-safe ASCII filename. Deliberately
// minimal: anything outside [A-Za-z0-9._-] (quotes, backslashes, path separators, CR/LF, and every
// non-ASCII rune) becomes "_", runs collapse, and the result is capped -- there is no filename*
// RFC 5987 form and no transliteration, because the name is a convenience for the downloading user,
// not an identifier anything reads back.
func exportFilename(deckName string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range deckName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	name := strings.Trim(b.String(), "_.")
	if len(name) > 80 {
		name = strings.Trim(name[:80], "_.")
	}
	if name == "" {
		name = "deck"
	}
	return name + ".apkg"
}
