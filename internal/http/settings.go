package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"image"
	_ "image/jpeg" // registers the JPEG decoder used by image.DecodeConfig below
	"io"
	"math"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare"
	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
	"github.com/Jolls/deckshare/internal/media"
	"github.com/Jolls/deckshare/internal/review"
)

// maxAvatarUploadBytes bounds the raw multipart request body. The client resizes and re-encodes
// to JPEG before upload, so a well-behaved request is well under this; it's a backstop against a
// bypassed or JS-less client, not the expected size.
const maxAvatarUploadBytes = 5 << 20

// maxAvatarDimension is the server-enforced backstop on decoded image width/height, independent
// of the client-side canvas resize -- which a request can skip entirely.
const maxAvatarDimension = 2048

// appVersion is read from the top CHANGELOG.md entry at startup, so it can never drift from
// the version already bumped alongside every PR (CLAUDE.md §14).
var appVersion = deckshare.Version()

// currentRetention looks up the user's global desired-retention setting, falling back to the
// package default when none has been set yet (ErrNoRows). Shared by every settings render that
// needs to show the FSRS section's current value alongside some other section's result.
func currentRetention(ctx context.Context, store db.Beginner, userID pgtype.UUID) (float64, error) {
	retention, err := db.New(store).GetGlobalFsrsRetention(ctx, userID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		return review.DefaultDesiredRetention, nil
	}
	return retention, nil
}

// settingsView is the render data for pages["settings"]. Every /settings handler branch
// builds one via buildSettingsView and renders it directly, so a field the template needs
// on every render (Version, DesiredRetention, User) cannot be silently omitted by a branch
// that only means to set one section's error/success message (#218).
type settingsView struct {
	User             db.User
	Version          string
	DesiredRetention float64
	// BodyClass unconditionally read by layout.html:13; a struct field, unlike a map key,
	// errors at template execution if absent rather than rendering empty, so it must be
	// declared even though /settings never needs a non-default value (unlike review.go's
	// "hide-account-bar").
	BodyClass string

	AvatarError     string
	AvatarSuccess   string
	ProfileError    string
	ProfileSuccess  string
	PasswordError   string
	PasswordSuccess string
	FsrsError       string
	FsrsSuccess     string
}

// buildSettingsView assembles the fields every /settings render needs regardless of which
// section's form was submitted. Callers set whichever section-specific Error/Success field
// applies before rendering; the rest stay at their zero value (""), which errorMsg/successMsg
// already render as nothing.
func buildSettingsView(user db.User, retention float64) settingsView {
	return settingsView{
		User:             user,
		Version:          appVersion,
		DesiredRetention: retention,
	}
}

func registerSettingsRoutes(mux *http.ServeMux, a *auth.Service, store db.Beginner, pages map[string]*template.Template, blobs *media.Store) {
	mux.Handle("GET /settings", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		retention, err := currentRetention(r.Context(), store, user.ID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		render(w, pages["settings"], http.StatusOK, buildSettingsView(user, retention))
	})))

	mux.Handle("POST /settings", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		retention, err := currentRetention(r.Context(), store, user.ID)
		if err != nil {
			serverError(w, r, err)
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
			view := buildSettingsView(user, retention)
			view.ProfileError = "Day start hour must be a valid number"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}

		if err := a.UpdateProfile(r.Context(), user.ID, displayName, timezone, int16(dayStartHour)); err != nil {
			status, msg, _, ok := classifyFormError(err, nil)
			if !ok {
				serverError(w, r, err)
				return
			}
			view := buildSettingsView(user, retention)
			view.ProfileError = msg
			render(w, pages["settings"], status, view)
			return
		}

		view := buildSettingsView(user, retention)
		view.ProfileSuccess = "Profile updated"
		render(w, pages["settings"], http.StatusOK, view)
	})))

	mux.Handle("POST /settings/password", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		retention, err := currentRetention(r.Context(), store, user.ID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		currentPassword := r.PostForm.Get("current_password")
		newPassword := r.PostForm.Get("new_password")
		confirmPassword := r.PostForm.Get("confirm_password")

		if newPassword != confirmPassword {
			view := buildSettingsView(user, retention)
			view.PasswordError = "Passwords do not match"
			render(w, pages["settings"], http.StatusBadRequest, view)
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
				serverError(w, r, err)
				return
			}
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			view := buildSettingsView(user, retention)
			view.PasswordError = msg
			render(w, pages["settings"], status, view)
			return
		}

		// Must precede render: render calls w.WriteHeader, after which headers are frozen.
		auth.SetSessionCookie(w, token)
		view := buildSettingsView(user, retention)
		view.PasswordSuccess = "Password changed"
		render(w, pages["settings"], http.StatusOK, view)
	})))

	mux.Handle("POST /settings/fsrs", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		retention, atoiErr := strconv.ParseFloat(r.PostForm.Get("desired_retention"), 64)
		if atoiErr != nil {
			view := buildSettingsView(user, retention)
			view.FsrsError = "Desired retention must be a number"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}

		params, err := fsrs.NewDefaultParams(retention)
		if err != nil {
			view := buildSettingsView(user, retention)
			view.FsrsError = "Desired retention must be between 0 and 1"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}

		q := db.New(store)
		if err := q.UpsertGlobalFsrsRetention(r.Context(), db.UpsertGlobalFsrsRetentionParams{
			UserID: user.ID, FsrsVersion: int16(params.Version()), DesiredRetention: retention,
		}); err != nil {
			serverError(w, r, err)
			return
		}

		view := buildSettingsView(user, retention)
		view.FsrsSuccess = "Retention target updated"
		render(w, pages["settings"], http.StatusOK, view)
	})))

	// The old avatar (if any) is not deleted here: it's simply no longer referenced by this row,
	// and the hourly media GC sweep -- which already checks users.avatar_sha256 (#176) -- reclaims
	// it on its next tick if nothing else references it. Dedup means an unchanged re-upload just
	// re-points at the same row anyway.
	mux.Handle("POST /settings/avatar", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		retention, err := currentRetention(r.Context(), store, user.ID)
		if err != nil {
			serverError(w, r, err)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxAvatarUploadBytes)
		if err := r.ParseMultipartForm(maxAvatarUploadBytes); err != nil {
			view := buildSettingsView(user, retention)
			view.AvatarError = "Image too large"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}
		file, _, err := r.FormFile("avatar")
		if err != nil {
			view := buildSettingsView(user, retention)
			view.AvatarError = "Choose an image to upload"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}
		defer func() { _ = file.Close() }()

		data, err := io.ReadAll(file)
		if err != nil {
			view := buildSettingsView(user, retention)
			view.AvatarError = "Could not read upload"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}

		// Decoding also validates format: the client always re-encodes to JPEG before upload, so
		// anything else means a bypassed client rather than a legitimate format to support.
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "jpeg" {
			view := buildSettingsView(user, retention)
			view.AvatarError = "Avatar must be a JPEG image"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}
		if cfg.Width > maxAvatarDimension || cfg.Height > maxAvatarDimension {
			view := buildSettingsView(user, retention)
			view.AvatarError = "Image dimensions too large"
			render(w, pages["settings"], http.StatusBadRequest, view)
			return
		}

		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])

		// blobs.Put is a filesystem write, not part of the DB transaction below (same split as
		// apkg's importMedia). CreateMediaBlob and UpdateUserAvatar share one transaction so the GC
		// sweep never observes the blob row committed but not yet referenced by this user -- which,
		// left as two separate statements, is a window where the sweep could unlink the file before
		// the avatar pointer is set, stranding avatar_sha256 on a blob with no bytes on disk.
		if err := blobs.Put(sha, data); err != nil {
			serverError(w, r, err)
			return
		}
		tx, ok := startTx(w, r, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()
		q := db.New(tx)
		if err := q.CreateMediaBlob(r.Context(), db.CreateMediaBlobParams{
			Sha256: sha, SizeBytes: int64(len(data)), Mime: "image/jpeg",
		}); err != nil {
			serverError(w, r, err)
			return
		}
		if err := q.UpdateUserAvatar(r.Context(), db.UpdateUserAvatarParams{
			ID: user.ID, AvatarSha256: pgtype.Text{String: sha, Valid: true},
		}); err != nil {
			serverError(w, r, err)
			return
		}
		if !commitTx(w, r, tx) {
			return
		}

		user.AvatarSha256 = pgtype.Text{String: sha, Valid: true}
		view := buildSettingsView(user, retention)
		view.AvatarSuccess = "Avatar updated"
		render(w, pages["settings"], http.StatusOK, view)
	})))

	// Self-only (no cohort/sharing concept exists yet to define "who else may see this user").
	// Unlike GET /media/{sha256}, the address here is per-user and mutable, so the response is
	// private and revalidated every time rather than immutably cached.
	mux.Handle("GET /settings/avatar", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !user.AvatarSha256.Valid {
			notFound(w)
			return
		}
		sha := user.AvatarSha256.String
		etag := `"` + sha + `"`
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		f, err := blobs.Open(sha)
		if err != nil {
			serverError(w, r, err)
			return
		}
		defer func() { _ = f.Close() }()

		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
	})))
}
