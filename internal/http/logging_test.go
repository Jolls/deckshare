package http

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/auth"
)

// captureLogs swaps the default logger for the duration of a test, so package-level slog calls
// (the only kind this codebase makes, §1 of docs/plans/210-error-logging.md) become observable.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestServerError_LogsCauseAndLeaksNothing pins §2.7: the client-facing 500 body must never carry
// the error, and the log line must carry the operator-facing detail plus who/what/where.
func TestServerError_LogsCauseAndLeaksNothing(t *testing.T) {
	buf := captureLogs(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/decks/x", nil)
	sentinel := errors.New("sentinel-abc123")
	serverError(w, r, sentinel)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	if got := w.Body.String(); got != "internal server error\n" {
		t.Errorf("body = %q, want %q", got, "internal server error\n")
	}
	if strings.Contains(w.Body.String(), "sentinel-abc123") {
		t.Error("response body must never contain the underlying error")
	}

	logged := buf.String()
	if !strings.Contains(logged, "sentinel-abc123") {
		t.Error("log line must contain the underlying error")
	}
	if !strings.Contains(logged, "method=GET") {
		t.Error("log line must contain the method")
	}
	if !strings.Contains(logged, "path=/decks/x") {
		t.Error("log line must contain the path")
	}
	if !strings.Contains(logged, "user_id=") {
		t.Error("log line must contain user_id")
	}
}

// TestRequestLog_ReportsStatusAndUser is table-driven over a stub handler that writes 200 / 404 /
// 500 / nothing-at-all (implicit 200), through the full production stack with a logged-in
// session -- proving the captureUser relay (§3.2) carries the session's user id up to requestLog,
// and that requestLog reports whatever status the handler actually wrote.
func TestRequestLog_ReportsStatusAndUser(t *testing.T) {
	tx := beginTx(t)
	a, err := auth.New(tx, auth.Config{})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	tests := []struct {
		name       string
		writeCode  int // 0 means "write nothing"
		wantStatus int
	}{
		{"200", http.StatusOK, http.StatusOK},
		{"404", http.StatusNotFound, http.StatusNotFound},
		{"500", http.StatusInternalServerError, http.StatusInternalServerError},
		{"implicit 200", 0, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureLogs(t)

			mux := http.NewServeMux()
			mux.Handle("GET /stub", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.writeCode != 0 {
					w.WriteHeader(tt.writeCode)
				}
			})))
			handler := requestLog(securityHeaders(a.Middleware(captureUser(mux))))

			r := httptest.NewRequest("GET", "/stub", nil)
			r.Host = "example.com"
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			logged := buf.String()
			if !strings.Contains(logged, "msg=request") {
				t.Fatalf("expected a request log line, got: %s", logged)
			}
			wantStatusField := "status=" + strconv.Itoa(tt.wantStatus)
			if !strings.Contains(logged, wantStatusField) {
				t.Errorf("log line missing %q: %s", wantStatusField, logged)
			}
			if strings.Contains(logged, "user_id=\"\"") || strings.Contains(logged, "user_id=\n") {
				t.Errorf("log line has empty user_id for a logged-in session: %s", logged)
			}
		})
	}
}
