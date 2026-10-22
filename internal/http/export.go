package http

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Jolls/deckshare/internal/apkg"
	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// exportTimeout bounds the DB-holding portion of /decks/{id}/export (the transaction, not the
// header write or buffer send) -- see docs/plans/213-timeouts.md Decision 4.
const exportTimeout = 60 * time.Second

// maxExportBytes caps the size of the generated (compressed) package. It is a ceiling on the
// output, not on peak memory -- bytes.Buffer grows by doubling and apkg.Write builds the
// collection in memory first, so one export can briefly hold a multiple of this. The value
// matches apkg.DefaultArchiveLimits().MaxTotalBytes, which counts DEcompressed bytes, so it is
// the right order of magnitude for re-importing what we emit, not a guarantee that it will.
const maxExportBytes = 500 << 20

var errExportTooLarge = errors.New("export exceeds maxExportBytes")

// cappedBuffer is a bytes.Buffer that refuses to grow past max, so an oversize export fails
// inside apkg.Write instead of allocating without bound.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.Len()+len(p) > c.max {
		return 0, errExportTooLarge
	}
	return c.Buffer.Write(p)
}

// registerExportRoutes wires GET /decks/{id}/export (docs/routes.md): one deck's content plus the
// CALLER's own progress on it, serialised db -> IR -> .apkg and sent as a download. can_view, not
// can_edit_* -- exporting reads the deck, it does not change it. Authorisation is enforced inside
// apkg.Export's queries (export.sql's deck_access joins, CLAUDE.md §9); no access surfaces here as
// a wrapped pgx.ErrNoRows and collapses to 404, the same as every other deck route.
func registerExportRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template, now func() time.Time, slots chan struct{}) {
	mux.Handle("GET /decks/{id}/export", auth.RequireUser(concurrencyLimit(slots, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}

		// Read-only: the transaction exists because apkg.Export requires one (it reads eight
		// statements that must see one consistent snapshot), and it is only ever rolled back --
		// there is deliberately no commitTx here.
		exportCtx, cancel := context.WithTimeout(r.Context(), exportTimeout)
		defer cancel()
		exportReq := r.WithContext(exportCtx)

		tx, ok := startTx(w, exportReq, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(exportCtx) }()

		col, err := apkg.Export(exportCtx, tx, deckID, user.ID, now())
		if handleQueryErrPage(w, exportReq, pages, user, err) {
			return
		}

		// Buffered rather than streamed straight into w: a Write failure partway through would
		// otherwise land after the 200 and the Content-Disposition, leaving the browser a
		// truncated .apkg it believes is complete. Packages are already held whole in memory on
		// the /import side. The buffer is capped at maxExportBytes (#214).
		buf := cappedBuffer{max: maxExportBytes}
		if err := apkg.Write(col, &buf); err != nil {
			if errors.Is(err, errExportTooLarge) {
				slog.Warn("export over size ceiling",
					"deck_id", deckID.String(), "user_id", user.ID.String(), "limit_bytes", maxExportBytes)
				http.Error(w, "deck too large to export", http.StatusInternalServerError)
				return
			}
			serverError(w, exportReq, err)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+exportFilename(col.Decks[0].Name)+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = buf.WriteTo(w)
	}))))
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
