package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/media"
)

// DB-backed tests: skipped unless DATABASE_URL is set. Every test runs inside a pgx.Tx that is
// always rolled back, so tests neither pollute the dev database nor depend on each other.

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	seq      atomic.Int64
)

func nextSeq() int64 {
	return seq.Add(1)
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed test")
	}
	poolOnce.Do(func() {
		p, err := db.NewPool(context.Background(), dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		pool = p
	})
	return pool
}

func beginTx(t *testing.T) pgx.Tx {
	t.Helper()
	p := testPool(t)
	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return tx
}

func testEmail() string {
	return fmt.Sprintf("test-%d@example.com", nextSeq())
}

func countRows(t *testing.T, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// newTestHandler builds the same route+middleware stack as NewHandler, over a tx-backed auth
// service, without the DB pool (the /healthz route is not under test here). clocks is variadic so
// existing call sites are unaffected; pass one func to pin the clock for review-route tests.
// The securityHeaders wrap mirrors NewHandler's, so route tests exercise the same header stack
// production serves.
func newTestHandler(t *testing.T, tx pgx.Tx, cfg auth.Config, clocks ...func() time.Time) (http.Handler, *auth.Service) {
	t.Helper()
	a, err := auth.New(tx, cfg)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	pages, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	fragments, err := parseFragments()
	if err != nil {
		t.Fatalf("parseFragments: %v", err)
	}
	clock := time.Now
	if len(clocks) > 0 {
		clock = clocks[0]
	}
	mux := http.NewServeMux()
	blobs := media.New(t.TempDir())
	registerStaticRoutes(mux)
	registerAuthRoutes(mux, a, pages)
	registerSettingsRoutes(mux, a, tx, pages, blobs)
	registerDeckRoutes(mux, tx, pages, clock)
	registerAccessRoutes(mux, tx, pages)
	registerNoteTypeRoutes(mux, tx, pages)
	registerNoteRoutes(mux, tx, pages)
	registerNotePreviewRoutes(mux, tx, fragments)
	registerReviewRoutes(mux, tx, pages, fragments, clock)
	registerMediaRoutes(mux, tx, blobs)
	registerImportRoutes(mux, tx, pages, blobs, clock)
	registerExportRoutes(mux, tx, pages, clock)
	registerAIImportRoutes(mux, tx, pages)
	return securityHeaders(a.Middleware(mux)), a
}

func loginCookie(t *testing.T, tx pgx.Tx, a *auth.Service, email, password string) *http.Cookie {
	t.Helper()
	_, token, err := a.Signup(context.Background(), "9.9.9.9", email, password, "Test User")
	if err != nil {
		t.Fatalf("Signup fixture: %v", err)
	}
	return &http.Cookie{Name: auth.CookieName, Value: token}
}

func doRequest(handler http.Handler, method, path string, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = "example.com"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestRoutes_NoSession(t *testing.T) {
	tx := beginTx(t)
	handler, _ := newTestHandler(t, tx, auth.Config{})

	tests := []struct {
		method, path string
		wantStatus   int
		wantLocation string
	}{
		{"GET", "/", 303, "/login"},
		{"GET", "/login", 200, ""},
		{"GET", "/signup", 200, ""},
		{"GET", "/settings", 303, "/login"},
		{"GET", "/media/" + testSHA, 303, "/login"},
		{"GET", "/import", 303, "/login"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := doRequest(handler, tt.method, tt.path, "", nil, "")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantLocation != "" && w.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", w.Header().Get("Location"), tt.wantLocation)
			}
		})
	}
}

func TestRoutes_ValidSession(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	tests := []struct {
		method, path string
		wantStatus   int
		wantLocation string
	}{
		{"GET", "/", 303, "/decks"},
		{"GET", "/login", 303, "/decks"},
		{"GET", "/signup", 303, "/decks"},
		{"GET", "/settings", 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := doRequest(handler, tt.method, tt.path, "", cookie, "")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantLocation != "" && w.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", w.Header().Get("Location"), tt.wantLocation)
			}
		})
	}
}

func TestRoutes_ExpiredSession(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	if _, err := tx.Exec(context.Background(),
		`UPDATE sessions SET expires_at = now() - interval '1 hour'`,
	); err != nil {
		t.Fatalf("expire session: %v", err)
	}

	tests := []struct {
		method, path string
		wantStatus   int
		wantLocation string
	}{
		{"GET", "/", 303, "/login"},
		{"GET", "/login", 200, ""},
		{"GET", "/signup", 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := doRequest(handler, tt.method, tt.path, "", cookie, "")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantLocation != "" && w.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", w.Header().Get("Location"), tt.wantLocation)
			}
		})
	}
}

func TestLogout_ValidSessionDeletesRow(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/logout", "", cookie, "http://example.com")
	if w.Code != 303 {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	var userID string
	if err := tx.QueryRow(context.Background(), `SELECT id FROM users WHERE email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM sessions WHERE user_id = $1`, userID); n != 0 {
		t.Errorf("session row count = %d, want 0", n)
	}
}

func TestLogout_NoSession(t *testing.T) {
	tx := beginTx(t)
	handler, _ := newTestHandler(t, tx, auth.Config{})

	w := doRequest(handler, "POST", "/logout", "", nil, "http://example.com")
	if w.Code != 303 {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestPostWithoutOrigin_403(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	tests := []struct {
		name, path, body string
		cookie           *http.Cookie
	}{
		{"login", "/login", "email=" + email + "&password=correct-horse-battery", nil},
		{"signup", "/signup", "email=new-" + email + "&password=correct-horse-battery&display_name=New", nil},
		{"logout", "/logout", "", cookie},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "POST", tt.path, tt.body, tt.cookie, "")
			if w.Code != 403 {
				t.Errorf("status = %d, want 403", w.Code)
			}
		})
	}

	if n := countRows(t, tx, `SELECT count(*) FROM users WHERE lower(email) LIKE 'new-%'`); n != 0 {
		t.Error("no new user should have been created")
	}
}

func TestPostWithForeignOrigin_403(t *testing.T) {
	tx := beginTx(t)
	handler, _ := newTestHandler(t, tx, auth.Config{})
	email := testEmail()

	w := doRequest(handler, "POST", "/signup",
		"email="+email+"&password=correct-horse-battery&display_name=New",
		nil, "http://evil.com")
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM users WHERE lower(email) = lower($1)`, email); n != 0 {
		t.Error("no user should have been created")
	}
}

func TestLoginRateLimited(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	if _, _, err := a.Signup(context.Background(), "1.1.1.1", email, "correct-horse-battery", "Test"); err != nil {
		t.Fatalf("Signup fixture: %v", err)
	}

	body := "email=" + email + "&password=wrong-password"
	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		last = doRequest(handler, "POST", "/login", body, nil, "http://example.com")
	}
	if last.Code != 429 {
		t.Fatalf("6th attempt status = %d, want 429", last.Code)
	}
	if last.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header should be set")
	}
}

func TestSignupRateLimited(t *testing.T) {
	tx := beginTx(t)
	handler, _ := newTestHandler(t, tx, auth.Config{})

	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		body := fmt.Sprintf("email=%s&password=correct-horse-battery&display_name=Test", testEmail())
		last = doRequest(handler, "POST", "/signup", body, nil, "http://example.com")
	}
	if last.Code != 429 {
		t.Fatalf("6th attempt status = %d, want 429", last.Code)
	}
}

func TestLoginRateLimited_CaseInsensitiveEmail(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	if _, _, err := a.Signup(context.Background(), "1.1.1.1", email, "correct-horse-battery", "Test"); err != nil {
		t.Fatalf("Signup fixture: %v", err)
	}

	// Casing the email differently on every attempt must not reset the per-email limiter --
	// otherwise an attacker rotates case to get a fresh budget per variant.
	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		variant := email
		if i%2 == 0 {
			variant = strings.ToUpper(email)
		}
		last = doRequest(handler, "POST", "/login", "email="+variant+"&password=wrong-password", nil, "http://example.com")
	}
	if last.Code != 429 {
		t.Fatalf("6th attempt (case-varied) status = %d, want 429", last.Code)
	}
}

func TestSignupSetsCookieAndLandsHome(t *testing.T) {
	tx := beginTx(t)
	handler, _ := newTestHandler(t, tx, auth.Config{})
	email := testEmail()

	w := doRequest(handler, "POST", "/signup",
		"email="+email+"&password=correct-horse-battery&display_name=New User",
		nil, "http://example.com")
	if w.Code != 303 {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/decks" {
		t.Errorf("Location = %q, want /decks", loc)
	}
	resp := w.Result()
	if len(resp.Cookies()) != 1 {
		t.Fatalf("len(cookies) = %d, want 1", len(resp.Cookies()))
	}
	c := resp.Cookies()[0]
	if c.Name != auth.CookieName || !c.Secure || !c.HttpOnly {
		t.Errorf("cookie = %+v, want __Host- session attributes", c)
	}
}
