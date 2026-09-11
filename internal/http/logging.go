package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

type userCellKey struct{}

// captureUser copies the authenticated user out of auth.Middleware's derived context into the cell
// requestLog placed above it. It is the innermost wrap, below auth.Middleware, because that is the
// only place the user is visible; requestLog is the outermost, because the CSRF 403 has to be
// logged too. The cell is written and read on the same goroutine (read only after ServeHTTP
// returns), so no synchronisation is needed.
func captureUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cell, ok := r.Context().Value(userCellKey{}).(*db.User); ok {
			if u, found := auth.UserFromContext(r.Context()); found {
				*cell = u
			}
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status a handler wrote, so requestLog can report it. A handler that
// writes a body without calling WriteHeader implies 200, which is why status starts at 200.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var user db.User
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		// Recover-log-rethrow: a handler panic must still produce a "request" line (that's the
		// whole point of wrapping outermost -- see http.go's doc comment), but the panic itself
		// still needs to reach net/http's own per-connection recover, which is what actually
		// closes the connection down.
		defer func() {
			if v := recover(); v != nil {
				slog.Error("request panicked",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", v,
				)
				panic(v)
			}
			slog.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"user_id", user.ID.String(),
			)
		}()
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), userCellKey{}, &user)))
	})
}
