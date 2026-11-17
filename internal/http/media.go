package http

import (
	"io"
	"mime"
	"net/http"
	"regexp"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/media"
)

// sha256HexPath matches the {sha256} path segment. The value is untrusted (straight off the URL),
// so it is validated before it ever reaches the query layer or the blob store -- media.Store
// itself re-validates on Open, but rejecting a malformed value here means a 404 instead of a
// surfaced store error.
var sha256HexPath = regexp.MustCompile(`^[0-9a-f]{64}$`)

// safeMediaContentTypes is what /media/{sha256} will hand a browser as Content-Type verbatim --
// every type a card can legitimately reference inline (<img src> today; audio/video once
// playback lands, #<TBD>) and nothing a browser will parse as markup or execute as script.
// Named allowlist, not a denylist of "known-dangerous" types, matching the scheme-allowlist
// pattern in internal/render/sanitise.go and security.go's CSP sourcing.
var safeMediaContentTypes = map[string]bool{
	"image/png":                true,
	"image/jpeg":               true,
	"image/gif":                true,
	"image/webp":               true,
	"image/bmp":                true,
	"image/tiff":               true,
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true, // mime.TypeByExtension(".ico")'s builtin-table value; the OS mime registry sometimes overrides it to image/x-icon instead, so both must be allowed
	"audio/mpeg":               true,
	"audio/ogg":                true,
	"audio/wave":               true, // http.DetectContentType's sniffed value for WAV bytes when detectMediaMime falls back to sniffing (no/unrecognized extension)
	"audio/wav":                true,
	"audio/x-wav":              true,
	"audio/webm":               true,
	"audio/mp4":                true,
	"audio/aac":                true,
	"audio/flac":               true,
	"application/ogg":          true, // http.DetectContentType's sniffed value for Ogg container bytes (audio or video) when detectMediaMime falls back to sniffing
	"video/mp4":                true,
	"video/webm":               true,
	"video/ogg":                true,
}

// svgMediaContentType is the one script-capable type served as itself, because flag/diagram decks
// reference standalone .svg files from <img src>. Inside <img> a browser runs SVG with script and
// external loads disabled. The other way in is a top-level navigation or iframe to
// /media/{sha256}, closed three ways: Content-Disposition: attachment (browsers ignore it for
// <img> loads, but a direct navigation downloads instead of rendering), and svgMediaCSP as
// defence in depth -- sandbox gives an opaque origin with scripts off, and frame-ancestors/
// base-uri/form-action repeat what security.go's global CSP would have set, because a
// per-response CSP header replaces that one rather than adding to it.
const (
	svgMediaContentType = "image/svg+xml"
	svgMediaCSP         = "default-src 'none'; sandbox; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
)

// mediaContentType returns what to send as Content-Type for a blob whose recorded MIME is
// stored. Anything not on safeMediaContentTypes -- text/html chief among them, since it is
// filename-extension-derivable and script-capable -- is forced to application/octet-stream so
// nosniff keeps the browser from ever treating the bytes as markup. image/svg+xml is the
// exception, passed through only together with svgMediaCSP (see registerMediaRoutes).
// detectMediaMime (internal/apkg/dbwrite.go) still records the real type; this is a presentation
// decision made fresh on every response, not a data correction, so it also covers blobs imported
// before this allowlist existed.
func mediaContentType(stored string) string {
	base, _, err := mime.ParseMediaType(stored)
	if err != nil || (!safeMediaContentTypes[base] && base != svgMediaContentType) {
		return "application/octet-stream"
	}
	return stored
}

func isSVGContentType(ct string) bool {
	base, _, err := mime.ParseMediaType(ct)
	return err == nil && base == svgMediaContentType
}

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
		if handleQueryErr(w, r, err) {
			return
		}

		f, err := blobs.Open(sha)
		if err != nil {
			serverError(w, r, err)
			return
		}
		defer func() { _ = f.Close() }()

		// Long-lived and private: the address is the hash so the content behind it never
		// changes, but visibility is per-user (deck_access), so a shared cache must not serve
		// one user's fetch to another.
		ct := mediaContentType(blob.Mime)
		w.Header().Set("Content-Type", ct)
		if isSVGContentType(ct) {
			w.Header().Set("Content-Security-Policy", svgMediaCSP)
			w.Header().Set("Content-Disposition", "attachment")
		}
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
	})))
}
