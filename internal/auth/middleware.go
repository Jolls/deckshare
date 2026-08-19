package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
)

type ctxKey struct{}

// UserFromContext returns the authenticated user, if any.
func UserFromContext(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(db.User)
	return u, ok
}

// Middleware enforces the Origin check on state-changing requests and populates the session for
// every request. Wrapping is central and unconditional (architecture.md §12) -- a new route
// cannot ship without it.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStateChanging(r.Method) && !s.checkOrigin(r) {
			log.Printf("csrf: rejected %s %s (Origin=%q Host=%q)", r.Method, r.URL.Path, r.Header.Get("Origin"), r.Host)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		cookie, err := r.Cookie(CookieName)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		row, err := s.q.GetSessionUser(ctx, hashToken(cookie.Value))
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			ClearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}

		ctx = context.WithValue(ctx, ctxKey{}, row.User)

		if time.Until(row.ExpiresAt.Time) < RenewThreshold {
			newExpiry := time.Now().Add(SessionLifetime)
			err := s.q.RenewSession(ctx, db.RenewSessionParams{
				ID:        hashToken(cookie.Value),
				ExpiresAt: pgtype.Timestamptz{Time: newExpiry, Valid: true},
			})
			// Sliding renewal is cosmetic: the session the caller presented is still valid
			// until its existing expires_at either way. A transient failure on this UPDATE
			// (lock contention from a duplicate request, a statement timeout) should not
			// fail an otherwise-successful authenticated request -- log and continue without
			// the refreshed cookie rather than returning 500.
			if err != nil {
				log.Printf("session renewal: %v", err)
			} else {
				SetSessionCookie(w, cookie.Value)
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireUser rejects unauthenticated requests with a 303 to /login. This is a form-based HTML
// app, not a JSON API, so every route wrapped in RequireUser -- GET or POST alike -- sends an
// unauthenticated caller to the login page rather than a bare 401.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFromContext(r.Context()); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func (s *Service) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}

	if s.origins != nil {
		for _, o := range s.origins {
			if strings.EqualFold(parsed.Scheme, o.Scheme) && strings.EqualFold(parsed.Host, o.Host) {
				return true
			}
		}
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// SetSessionCookie writes the __Host- session cookie with token as its value.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionLifetime.Seconds()),
	})
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
