package http

import (
	"io"
	"net/http"
	"regexp"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/media"
)

// sha256HexPath matches the {sha256} path segment. The value is untrusted (straight off the URL),
// so it is validated before it ever reaches the query layer or the blob store -- media.Store
// itself re-validates on Open, but rejecting a malformed value here means a 404 instead of a
// surfaced store error.
var sha256HexPath = regexp.MustCompile(`^[0-9a-f]{64}$`)

// registerMediaRoutes wires the content-addressed blob-serving route (docs/routes.md's Media
// section). Visibility is can_view of ANY deck referencing the blob -- GetMediaBlobForUser joins
// media_refs -> deck_access, the same authorisation path every cross-user read goes through
// (CLAUDE.md §9) -- so "blob doesn't exist" and "blob exists but isn't visible to this caller"
// both collapse to 404, matching decks.go's GetDeckForUser.
func registerMediaRoutes(mux *http.ServeMux, store db.Beginner, blobs *media.Store) {
	mux.Handle("GET /media/{sha256}", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sha := r.PathValue("sha256")
		if !sha256HexPath.MatchString(sha) {
			notFound(w)
			return
		}
		user, _ := auth.UserFromContext(r.Context())

		blob, err := db.New(store).GetMediaBlobForUser(r.Context(), db.GetMediaBlobForUserParams{Sha256: sha, UserID: user.ID})
		if handleQueryErr(w, err) {
			return
		}

		f, err := blobs.Open(sha)
		if err != nil {
			serverError(w)
			return
		}
		defer func() { _ = f.Close() }()

		// Long-lived and private: the address is the hash so the content behind it never
		// changes, but visibility is per-user (deck_access), so a shared cache must not serve
		// one user's fetch to another.
		w.Header().Set("Content-Type", blob.Mime)
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
	})))
}
