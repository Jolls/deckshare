package http

import (
	"errors"
	"html/template"
	"net"
	"net/http"

	"github.com/Jolls/enshu/internal/auth"
)

func registerAuthRoutes(mux *http.ServeMux, a *auth.Service, pages map[string]*template.Template) {
	mux.HandleFunc("GET /signup", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.UserFromContext(r.Context()); ok {
			http.Redirect(w, r, "/decks", http.StatusSeeOther)
			return
		}
		render(w, pages["signup"], http.StatusOK, map[string]any{})
	})

	mux.HandleFunc("POST /signup", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			badRequest(w)
			return
		}
		email := r.PostForm.Get("email")
		password := r.PostForm.Get("password")
		displayName := r.PostForm.Get("display_name")

		_, token, err := a.Signup(r.Context(), clientIP(r), email, password, displayName)
		if err != nil {
			status, msg, retryAfter, ok := classifyFormError(err, func(e error) (int, string, bool) {
				if errors.Is(e, auth.ErrEmailTaken) {
					return http.StatusConflict, "That email is already registered", true
				}
				return 0, "", false
			})
			if !ok {
				serverError(w)
				return
			}
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			render(w, pages["signup"], status, map[string]any{"Email": email, "DisplayName": displayName, "Error": msg})
			return
		}

		auth.SetSessionCookie(w, token)
		http.Redirect(w, r, "/decks", http.StatusSeeOther)
	})

	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.UserFromContext(r.Context()); ok {
			http.Redirect(w, r, "/decks", http.StatusSeeOther)
			return
		}
		render(w, pages["login"], http.StatusOK, map[string]any{})
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			badRequest(w)
			return
		}
		email := r.PostForm.Get("email")
		password := r.PostForm.Get("password")

		_, token, err := a.Login(r.Context(), clientIP(r), email, password)
		if err != nil {
			status, msg, retryAfter, ok := classifyFormError(err, func(e error) (int, string, bool) {
				if errors.Is(e, auth.ErrInvalidCredentials) {
					return http.StatusUnauthorized, "Email or password is incorrect", true
				}
				return 0, "", false
			})
			if !ok {
				serverError(w)
				return
			}
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			render(w, pages["login"], status, map[string]any{"Email": email, "Error": msg})
			return
		}

		auth.SetSessionCookie(w, token)
		http.Redirect(w, r, "/decks", http.StatusSeeOther)
	})

	mux.Handle("POST /logout", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		if cookie, err := r.Cookie(auth.CookieName); err == nil {
			token = cookie.Value
		}
		if err := a.Logout(r.Context(), token); err != nil {
			serverError(w)
			return
		}
		auth.ClearSessionCookie(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.UserFromContext(r.Context()); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/decks", http.StatusSeeOther)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
