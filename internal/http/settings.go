package http

import (
	"errors"
	"html/template"
	"math"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	"github.com/Jolls/enshu/internal/review"
)

func registerSettingsRoutes(mux *http.ServeMux, a *auth.Service, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /settings", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		retention, err := db.New(store).GetGlobalFsrsRetention(r.Context(), user.ID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				serverError(w)
				return
			}
			retention = review.DefaultDesiredRetention
		}
		render(w, pages["settings"], http.StatusOK, map[string]any{"User": user, "DesiredRetention": retention})
	})))

	mux.Handle("POST /settings", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		displayName := r.PostForm.Get("display_name")
		timezone := r.PostForm.Get("timezone")
		dayStartHour, atoiErr := strconv.Atoi(r.PostForm.Get("day_start_hour"))

		// Sticky display values: whatever was submitted, valid or not, is what re-renders --
		// set once here so every branch below (bad input, validation failure, success) shows
		// the same thing without repeating the assignment.
		user.DisplayName = displayName
		user.Timezone = timezone
		if atoiErr == nil && dayStartHour >= math.MinInt16 && dayStartHour <= math.MaxInt16 {
			user.DayStartHour = int16(dayStartHour)
		}

		if atoiErr != nil || dayStartHour < math.MinInt16 || dayStartHour > math.MaxInt16 {
			// This only guards the int16 conversion below against overflow -- the actual
			// 0-23 business rule is enforced once, downstream in a.UpdateProfile.
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User":         user,
				"ProfileError": "Day start hour must be a valid number",
			})
			return
		}

		if err := a.UpdateProfile(r.Context(), user.ID, displayName, timezone, int16(dayStartHour)); err != nil {
			status, msg, _, ok := classifyFormError(err, nil)
			if !ok {
				serverError(w)
				return
			}
			render(w, pages["settings"], status, map[string]any{
				"User":         user,
				"ProfileError": msg,
			})
			return
		}

		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User":           user,
			"ProfileSuccess": "Profile updated",
		})
	})))

	mux.Handle("POST /settings/password", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		currentPassword := r.PostForm.Get("current_password")
		newPassword := r.PostForm.Get("new_password")
		confirmPassword := r.PostForm.Get("confirm_password")

		if newPassword != confirmPassword {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User":          user,
				"PasswordError": "Passwords do not match",
			})
			return
		}

		token, err := a.ChangePassword(r.Context(), user.ID, user.PasswordHash, currentPassword, newPassword)
		if err != nil {
			status, msg, retryAfter, ok := classifyFormError(err, func(e error) (int, string, bool) {
				if errors.Is(e, auth.ErrInvalidCredentials) {
					return http.StatusUnauthorized, "Current password is incorrect", true
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
			render(w, pages["settings"], status, map[string]any{
				"User":          user,
				"PasswordError": msg,
			})
			return
		}

		// Must precede render: render calls w.WriteHeader, after which headers are frozen.
		auth.SetSessionCookie(w, token)
		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User":            user,
			"PasswordSuccess": "Password changed",
		})
	})))

	mux.Handle("POST /settings/fsrs", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		retention, atoiErr := strconv.ParseFloat(r.PostForm.Get("desired_retention"), 64)
		if atoiErr != nil {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention,
				"FsrsError": "Desired retention must be a number",
			})
			return
		}

		params, err := fsrs.NewDefaultParams(retention)
		if err != nil {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention,
				"FsrsError": "Desired retention must be between 0 and 1",
			})
			return
		}

		q := db.New(store)
		if err := q.UpsertGlobalFsrsRetention(r.Context(), db.UpsertGlobalFsrsRetentionParams{
			UserID: user.ID, FsrsVersion: int16(params.Version()), DesiredRetention: retention,
		}); err != nil {
			serverError(w)
			return
		}

		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User": user, "DesiredRetention": retention,
			"FsrsSuccess": "Retention target updated",
		})
	})))
}
