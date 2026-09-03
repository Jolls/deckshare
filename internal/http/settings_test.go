package http

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"

	"github.com/Jolls/enshu/internal/auth"
)

// smallJPEG returns a tiny (dim x dim) encoded JPEG, for tests that need real, decodable image
// bytes rather than a hand-rolled byte string.
func smallJPEG(t *testing.T, dim int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, dim, dim))
	for y := 0; y < dim; y++ {
		for x := 0; x < dim; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// smallPNG returns a tiny encoded PNG -- a well-formed image, but the wrong format for an avatar
// upload, which must always be JPEG (the client always re-encodes to JPEG before sending).
func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestSettingsRoutes_AllowDeny(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	tests := []struct {
		name, method, path, body string
		cookie                   *http.Cookie
		wantStatus               int
	}{
		{"GET /settings no session", "GET", "/settings", "", nil, 303},
		{"GET /settings valid session", "GET", "/settings", "", cookie, 200},
		{"POST /settings no session", "POST", "/settings", "display_name=X&timezone=UTC&day_start_hour=4", nil, 303},
		{"POST /settings/password no session", "POST", "/settings/password", "current_password=a&new_password=bbbbbbbb&confirm_password=bbbbbbbb", nil, 303},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := ""
			if tt.method == "POST" {
				origin = "http://example.com"
			}
			w := doRequest(handler, tt.method, tt.path, tt.body, tt.cookie, origin)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestPostSettingsWithoutOrigin_403(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings", "display_name=Changed&timezone=UTC&day_start_hour=5", cookie, "")
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM users WHERE display_name = 'Changed'`); n != 0 {
		t.Error("profile should not have been updated")
	}
}

func TestPostSettingsPasswordWithoutOrigin_403(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings/password",
		"current_password=correct-horse-battery&new_password=new-correct-horse-battery&confirm_password=new-correct-horse-battery",
		cookie, "")
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}

	if _, _, err := a.Login(context.Background(), "1.2.3.4", email, "correct-horse-battery"); err != nil {
		t.Error("old password should still work: password was changed despite missing Origin")
	}
}

func TestSettingsProfileGoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings",
		"display_name=Updated Name&timezone=America/New_York&day_start_hour=6",
		cookie, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM users WHERE display_name = 'Updated Name' AND timezone = 'America/New_York' AND day_start_hour = 6`); n != 1 {
		t.Error("row should reflect the update")
	}
}

func TestSettingsPasswordGoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings/password",
		"current_password=correct-horse-battery&new_password=new-correct-horse-battery&confirm_password=new-correct-horse-battery",
		cookie, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if _, _, err := a.Login(context.Background(), "1.2.3.4", email, "new-correct-horse-battery"); err != nil {
		t.Errorf("login with new password should succeed: %v", err)
	}
	if _, _, err := a.Login(context.Background(), "1.2.3.4", email, "correct-horse-battery"); err == nil {
		t.Error("login with old password should fail")
	}

	// The password change purges every session and reissues one, so the response must carry a
	// replacement cookie and the pre-change cookie must no longer authenticate (#123).
	// Last-wins, as a browser would apply them: on a session old enough to trip RenewThreshold
	// the middleware also emits a Set-Cookie for the OLD token before the handler emits the new
	// one, so "exactly one session cookie" is not an invariant worth asserting here.
	var reissued *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			reissued = c
		}
	}
	if reissued == nil {
		t.Fatal("response should reissue the session cookie")
	}
	if reissued.Value == cookie.Value {
		t.Error("reissued session cookie should differ from the pre-change one")
	}
	if old := doRequest(handler, "GET", "/settings", "", cookie, ""); old.Code != 303 {
		t.Errorf("pre-change cookie status = %d, want 303 to /login", old.Code)
	}
}

func TestSettingsFsrsGoldenPath(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings/fsrs", "desired_retention=0.85", cookie, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	id := userID(t, ctx, tx, email)
	if n := countRows(t, tx, `SELECT count(*) FROM user_fsrs_params WHERE user_id = $1 AND deck_id IS NULL AND desired_retention = 0.85`, id); n != 1 {
		t.Error("global row should reflect the update")
	}

	// Re-posting updates the same row rather than inserting a second one.
	w = doRequest(handler, "POST", "/settings/fsrs", "desired_retention=0.9", cookie, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM user_fsrs_params WHERE user_id = $1 AND deck_id IS NULL`, id); n != 1 {
		t.Error("second post should update, not duplicate, the global row")
	}
}

func TestSettingsFsrsInvalidRetention(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings/fsrs", "desired_retention=1.5", cookie, "http://example.com")
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	id := userID(t, ctx, tx, email)
	if n := countRows(t, tx, `SELECT count(*) FROM user_fsrs_params WHERE user_id = $1`, id); n != 0 {
		t.Error("no row should have been written for an out-of-range retention")
	}
}

func TestSettingsPasswordMismatch(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings/password",
		"current_password=correct-horse-battery&new_password=aaaaaaaa&confirm_password=bbbbbbbb",
		cookie, "http://example.com")
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	if _, _, err := a.Login(context.Background(), "1.2.3.4", email, "correct-horse-battery"); err != nil {
		t.Error("password hash should be unchanged after a mismatched confirmation")
	}
}

func TestSettingsAvatarRoutes_NoSession(t *testing.T) {
	tx := beginTx(t)
	handler, _ := newTestHandler(t, tx, auth.Config{})

	if w := doRequest(handler, "GET", "/settings/avatar", "", nil, ""); w.Code != 303 {
		t.Errorf("GET status = %d, want 303", w.Code)
	}
	if w := doUploadRequest(t, handler, "/settings/avatar", "avatar", "avatar.jpg", smallJPEG(t, 8), nil, "http://example.com"); w.Code != 303 {
		t.Errorf("POST status = %d, want 303", w.Code)
	}
}

func TestSettingsAvatarGoldenPath(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	// No avatar yet.
	if w := doRequest(handler, "GET", "/settings/avatar", "", cookie, ""); w.Code != 404 {
		t.Fatalf("GET before upload status = %d, want 404", w.Code)
	}

	first := smallJPEG(t, 8)
	w := doUploadRequest(t, handler, "/settings/avatar", "avatar", "avatar.jpg", first, cookie, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("upload status = %d, want 200: %s", w.Code, w.Body.String())
	}

	get := doRequest(handler, "GET", "/settings/avatar", "", cookie, "")
	if get.Code != 200 {
		t.Fatalf("GET after upload status = %d, want 200", get.Code)
	}
	if ct := get.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if !bytes.Equal(get.Body.Bytes(), first) {
		t.Error("served bytes should match the uploaded avatar")
	}
	id := userID(t, ctx, tx, email)
	firstSha := countRows(t, tx, `SELECT count(*) FROM users WHERE id = $1 AND avatar_sha256 IS NOT NULL`, id)
	if firstSha != 1 {
		t.Error("avatar_sha256 should be set after upload")
	}

	// Replacing the avatar re-points the user row at the new blob; the old blob's row is left for
	// the GC sweep (internal/media/gc_test.go's TestListUnreferencedMediaBlobs_ExcludesAvatars
	// already covers the sweep excluding a *current* avatar) rather than deleted synchronously here.
	second := smallJPEG(t, 16)
	w = doUploadRequest(t, handler, "/settings/avatar", "avatar", "avatar2.jpg", second, cookie, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("replace upload status = %d, want 200: %s", w.Code, w.Body.String())
	}
	get = doRequest(handler, "GET", "/settings/avatar", "", cookie, "")
	if !bytes.Equal(get.Body.Bytes(), second) {
		t.Error("served bytes should reflect the replacement avatar, not the original")
	}
}

func TestSettingsAvatarRejectsNonJPEG(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doUploadRequest(t, handler, "/settings/avatar", "avatar", "avatar.png", smallPNG(t), cookie, "http://example.com")
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := doRequest(handler, "GET", "/settings/avatar", "", cookie, ""); w.Code != 404 {
		t.Error("no avatar should have been stored")
	}
}

func TestSettingsAvatarRejectsOversizedUpload(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	oversized := make([]byte, maxAvatarUploadBytes+1)
	w := doUploadRequest(t, handler, "/settings/avatar", "avatar", "avatar.jpg", oversized, cookie, "http://example.com")
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
