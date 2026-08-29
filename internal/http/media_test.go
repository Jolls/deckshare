package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/media"
)

// testSHA is a well-formed (but never-written) sha256 hex digest, used by tests that only need a
// path-shaped value -- e.g. checking the unauthenticated redirect fires before any DB lookup.
const testSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// newMediaTestHandler is a slimmed-down version of newTestHandler: just auth middleware plus the
// media route, over a caller-supplied store the test can write blob bytes into directly (the
// shared newTestHandler builds its own throwaway store and never exposes it).
func newMediaTestHandler(t *testing.T, tx pgx.Tx, store *media.Store) (http.Handler, *auth.Service) {
	t.Helper()
	a, err := auth.New(tx, auth.Config{})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	mux := http.NewServeMux()
	registerMediaRoutes(mux, tx, store)
	return securityHeaders(a.Middleware(mux)), a
}

// seedMediaRef writes data into store and links it to a fresh deck owned by ownerID, returning
// the blob's sha256 hex digest.
func seedMediaRef(t *testing.T, ctx context.Context, tx pgx.Tx, store *media.Store, ownerID string, data []byte) string {
	t.Helper()
	return seedMediaRefMime(t, ctx, tx, store, ownerID, data, "image/jpeg", "cat.jpg")
}

// seedMediaRefMime writes data into store and links it to a fresh deck owned by ownerID under the
// given stored MIME type and filename, returning the blob's sha256 hex digest.
func seedMediaRefMime(t *testing.T, ctx context.Context, tx pgx.Tx, store *media.Store, ownerID string, data []byte, mimeType, filename string) string {
	t.Helper()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if err := store.Put(sha, data); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	var deckID string
	if err := tx.QueryRow(ctx, `INSERT INTO decks (owner_id, name) VALUES ($1, 'D') RETURNING id`, ownerID).Scan(&deckID); err != nil {
		t.Fatalf("insert deck: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, deckID, ownerID); err != nil {
		t.Fatalf("grant access: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_blobs (sha256, size_bytes, mime) VALUES ($1, $2, $3)`, sha, len(data), mimeType); err != nil {
		t.Fatalf("insert media_blobs: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_refs (deck_id, filename, sha256) VALUES ($1, $2, $3)`, deckID, filename, sha); err != nil {
		t.Fatalf("insert media_refs: %v", err)
	}
	return sha
}

func userID(t *testing.T, ctx context.Context, tx pgx.Tx, email string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatalf("lookup user %q: %v", email, err)
	}
	return id
}

func TestMediaRoute_ServesVisibleBlob(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	store := media.New(t.TempDir())
	handler, a := newMediaTestHandler(t, tx, store)

	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	ownerID := userID(t, ctx, tx, ownerEmail)

	data := []byte("a fake jpeg")
	sha := seedMediaRef(t, ctx, tx, store, ownerID, data)

	w := doRequest(handler, "GET", "/media/"+sha, "", ownerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != string(data) {
		t.Errorf("body = %q, want %q", w.Body.String(), data)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "" {
		t.Error("Cache-Control header missing")
	}
}

// A user with no deck_access row on any deck referencing the blob must get 404, not the bytes --
// the same collapse-existence-into-404 rule decks.go's GetDeckForUser already applies.
func TestMediaRoute_DeniesInvisibleBlob(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	store := media.New(t.TempDir())
	handler, a := newMediaTestHandler(t, tx, store)

	ownerEmail := testEmail()
	loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	ownerID := userID(t, ctx, tx, ownerEmail)
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	sha := seedMediaRef(t, ctx, tx, store, ownerID, []byte("a fake jpeg"))

	w := doRequest(handler, "GET", "/media/"+sha, "", strangerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// A viewer granted can_view on the referencing deck can fetch the same blob the owner can --
// visibility is per-deck-access, not per-owner.
func TestMediaRoute_ViewerWithDeckAccessCanFetch(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	store := media.New(t.TempDir())
	handler, a := newMediaTestHandler(t, tx, store)

	ownerEmail := testEmail()
	loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	ownerID := userID(t, ctx, tx, ownerEmail)
	viewerEmail := testEmail()
	viewerCookie := loginCookie(t, tx, a, viewerEmail, "correct-horse-battery")
	viewerID := userID(t, ctx, tx, viewerEmail)

	sha := seedMediaRef(t, ctx, tx, store, ownerID, []byte("a fake jpeg"))
	var deckID string
	if err := tx.QueryRow(ctx, `SELECT deck_id FROM media_refs WHERE sha256 = $1`, sha).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck for ref: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, deckID, viewerID); err != nil {
		t.Fatalf("grant viewer access: %v", err)
	}

	w := doRequest(handler, "GET", "/media/"+sha, "", viewerCookie, "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestMediaRoute_RejectsMalformedHash(t *testing.T) {
	tx := beginTx(t)
	store := media.New(t.TempDir())
	handler, a := newMediaTestHandler(t, tx, store)
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	// A literal ".." path segment never reaches this handler at all -- net/http's ServeMux
	// cleans it and redirects before route matching -- so it isn't exercised here; the cases
	// below are the ones that DO reach sha256HexPath's validation.
	for _, sha := range []string{"not-hex", "abc", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b8"} {
		w := doRequest(handler, "GET", "/media/"+sha, "", cookie, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("GET /media/%s status = %d, want 404", sha, w.Code)
		}
	}
}

// A media blob stored with a script-capable MIME type (SVG and HTML chief among them, since both
// are derivable from a filename extension by detectMediaMime) must never be served with that
// Content-Type -- the vulnerability this issue fixes. The fix relabels, not withholds: the bytes
// still come back unchanged, just as application/octet-stream.
func TestMediaRoute_ForcesUnsafeMimeToOctetStream(t *testing.T) {
	for _, mimeType := range []string{"image/svg+xml", "text/html", "text/plain", "application/javascript"} {
		t.Run(mimeType, func(t *testing.T) {
			tx := beginTx(t)
			ctx := context.Background()
			store := media.New(t.TempDir())
			handler, a := newMediaTestHandler(t, tx, store)

			ownerEmail := testEmail()
			ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
			ownerID := userID(t, ctx, tx, ownerEmail)

			data := []byte("<script>alert(1)</script>")
			sha := seedMediaRefMime(t, ctx, tx, store, ownerID, data, mimeType, "x.dat")

			w := doRequest(handler, "GET", "/media/"+sha, "", ownerCookie, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("Content-Type = %q, want application/octet-stream", ct)
			}
			if w.Body.String() != string(data) {
				t.Errorf("body = %q, want %q", w.Body.String(), data)
			}
		})
	}
}

// The allowlist must not be drawn so narrow that it silently breaks legitimate card images and
// audio -- these must pass through with their stored Content-Type unchanged.
func TestMediaRoute_AllowsSafeMimeTypes(t *testing.T) {
	for _, mimeType := range []string{
		"image/png", "image/gif", "image/webp", "audio/mpeg",
		// The two values below are what mime.TypeByExtension(".ico")'s builtin table and
		// http.DetectContentType's WAV/Ogg sniffing fallback actually produce -- not just
		// the extension-derived forms already covered above. See detectMediaMime
		// (internal/apkg/dbwrite.go).
		"image/vnd.microsoft.icon", "audio/wave", "application/ogg",
	} {
		t.Run(mimeType, func(t *testing.T) {
			tx := beginTx(t)
			ctx := context.Background()
			store := media.New(t.TempDir())
			handler, a := newMediaTestHandler(t, tx, store)

			ownerEmail := testEmail()
			ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
			ownerID := userID(t, ctx, tx, ownerEmail)

			data := []byte("fake media bytes")
			sha := seedMediaRefMime(t, ctx, tx, store, ownerID, data, mimeType, "x.dat")

			w := doRequest(handler, "GET", "/media/"+sha, "", ownerCookie, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); ct != mimeType {
				t.Errorf("Content-Type = %q, want %q", ct, mimeType)
			}
		})
	}
}
