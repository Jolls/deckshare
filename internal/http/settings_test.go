package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/Jolls/enshu/internal/auth"
)

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
