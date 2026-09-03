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

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	"github.com/Jolls/enshu/internal/media"
	"github.com/Jolls/enshu/internal/review"
)

// maxAvatarUploadBytes bounds the raw multipart request body. The client resizes and re-encodes
// to JPEG before upload, so a well-behaved request is well under this; it's a backstop against a
// bypassed or JS-less client, not the expected size.
const maxAvatarUploadBytes = 5 << 20

// maxAvatarDimension is the server-enforced backstop on decoded image width/height, independent
// of the client-side canvas resize -- which a request can skip entirely.
const maxAvatarDimension = 2048

// appVersion is bumped by hand alongside each CHANGELOG.md entry -- see CLAUDE.md §14.
const appVersion = "0.2.20"

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

func registerSettingsRoutes(mux *http.ServeMux, a *auth.Service, store db.Beginner, pages map[string]*template.Template, blobs *media.Store) {
	mux.Handle("GET /settings", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		retention, err := currentRetention(r.Context(), store, user.ID)
		if err != nil {
			serverError(w)
			return
		}
		render(w, pages["settings"], http.StatusOK, map[string]any{"User": user, "DesiredRetention": retention, "Version": appVersion})
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
				"Version":      appVersion,
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
				"Version":      appVersion,
				"ProfileError": msg,
			})
			return
		}

		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User":           user,
			"Version":        appVersion,
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
				"Version":       appVersion,
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
				"Version":       appVersion,
				"PasswordError": msg,
			})
			return
		}

		// Must precede render: render calls w.WriteHeader, after which headers are frozen.
		auth.SetSessionCookie(w, token)
		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User":            user,
			"Version":         appVersion,
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
				"User": user, "DesiredRetention": retention, "Version": appVersion,
				"FsrsError": "Desired retention must be a number",
			})
			return
		}

		params, err := fsrs.NewDefaultParams(retention)
		if err != nil {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention, "Version": appVersion,
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
			"User": user, "DesiredRetention": retention, "Version": appVersion,
			"FsrsSuccess": "Retention target updated",
		})
	})))

	// The old avatar (if any) is not deleted here: it's simply no longer referenced by this row,
	// and the hourly media GC sweep -- which already checks users.avatar_sha256 (#176) -- reclaims
	// it on its next tick if nothing else references it. Dedup means an unchanged re-upload just
	// re-points at the same row anyway.
	mux.Handle("POST /settings/avatar", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		retention, err := currentRetention(r.Context(), store, user.ID)
		if err != nil {
			serverError(w)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxAvatarUploadBytes)
		if err := r.ParseMultipartForm(maxAvatarUploadBytes); err != nil {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention, "AvatarError": "Image too large",
			})
			return
		}
		file, _, err := r.FormFile("avatar")
		if err != nil {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention, "AvatarError": "Choose an image to upload",
			})
			return
		}
		defer func() { _ = file.Close() }()

		data, err := io.ReadAll(file)
		if err != nil {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention, "AvatarError": "Could not read upload",
			})
			return
		}

		// Decoding also validates format: the client always re-encodes to JPEG before upload, so
		// anything else means a bypassed client rather than a legitimate format to support.
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "jpeg" {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention, "AvatarError": "Avatar must be a JPEG image",
			})
			return
		}
		if cfg.Width > maxAvatarDimension || cfg.Height > maxAvatarDimension {
			render(w, pages["settings"], http.StatusBadRequest, map[string]any{
				"User": user, "DesiredRetention": retention, "AvatarError": "Image dimensions too large",
			})
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
			serverError(w)
			return
		}
		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()
		q := db.New(tx)
		if err := q.CreateMediaBlob(r.Context(), db.CreateMediaBlobParams{
			Sha256: sha, SizeBytes: int64(len(data)), Mime: "image/jpeg",
		}); err != nil {
			serverError(w)
			return
		}
		if err := q.UpdateUserAvatar(r.Context(), db.UpdateUserAvatarParams{
			ID: user.ID, AvatarSha256: pgtype.Text{String: sha, Valid: true},
		}); err != nil {
			serverError(w)
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}

		user.AvatarSha256 = pgtype.Text{String: sha, Valid: true}
		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User": user, "DesiredRetention": retention, "AvatarSuccess": "Avatar updated",
		})
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
			serverError(w)
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
