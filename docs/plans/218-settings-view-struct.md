# Plan: #218 — settingsView struct for `/settings` handlers

## Status check (done before planning)

Symptom 3 (stale `appVersion` constant) is **already resolved**, by PR "Derive Settings version
display from CHANGELOG.md instead of a hand-maintained const" (merged prior to this issue being
planned):

- `internal/http/settings.go:39`: `var appVersion = deckshare.Version()`.
- `version.go` (`deckshare.Version()`) reads the top `## [x.y.z]` entry out of the embedded
  `CHANGELOG.md` via `go:embed`, so it can't drift from the changelog.
- CLAUDE.md §14 has no "bump `appVersion`" step to remove — the changelog section only
  describes updating `CHANGELOG.md` and tagging; there is no separate constant-bump instruction
  in the current file.

So this plan implements **only** the `settingsView` struct fix for symptoms 1 and 2. No changes
to `version.go` or CLAUDE.md are needed.

## Root cause (symptoms 1 and 2)

`internal/http/settings.go` renders every `/settings` response via
`render(w, pages["settings"], status, map[string]any{...})`, and each handler branch hand-builds
its own map literal. `web/templates/settings.html` unconditionally reads `.Version` (line 4) and
`.DesiredRetention` (line 61) regardless of which section triggered the render. Nothing enforces
that every map literal carries both keys, so:

- The 4 map literals in `POST /settings` (profile) omit `DesiredRetention`.
- The 2 map literals in `POST /settings/password` omit `DesiredRetention`.
- All 6 map literals in `POST /settings/avatar` omit `Version`.

A missing key in a `map[string]any` passed to `html/template` renders as `<no value>` rather than
failing to compile or erroring at runtime.

## Template field inventory (from `web/templates/settings.html`)

| Field | Line(s) | Always present? |
|---|---|---|
| `.Version` | 4 | yes, every render |
| `.User.AvatarSha256.Valid` | 10 | yes (needs `.User`) |
| `.AvatarError` | 8 | avatar section only |
| `.AvatarSuccess` | 9 | avatar section only |
| `.User.DisplayName` | 25 | yes (needs `.User`) |
| `.User.Timezone` | 28 | yes (needs `.User`) |
| `.User.DayStartHour` | 31 | yes (needs `.User`) |
| `.ProfileError` | 21 | profile section only |
| `.ProfileSuccess` | 22 | profile section only |
| `.PasswordError` | 39 | password section only |
| `.PasswordSuccess` | 40 | password section only |
| `.DesiredRetention` | 61 | yes, every render |
| `.FsrsError` | 57 | fsrs section only |
| `.FsrsSuccess` | 58 | fsrs section only |

`{{template "errorMsg" .X}}` / `{{template "successMsg" .X}}` already tolerate an empty string
(zero value) — confirmed by existing branches that omit e.g. `ProfileError` on non-profile
renders today. So the struct's message fields can default to `""` with no template changes
needed.

`user` in every handler is `db.User` (value type, from `auth.UserFromContext`, `internal/auth/middleware.go:21`).

## Design

### 1. New type + constructor in `internal/http/settings.go`

Add near the top of the file (after the existing doc comments, before `registerSettingsRoutes`):

```go
// settingsView is the render data for pages["settings"]. Every /settings handler branch
// builds one via buildSettingsView and renders it directly, so a field the template needs
// on every render (Version, DesiredRetention, User) cannot be silently omitted by a branch
// that only means to set one section's error/success message (#218).
type settingsView struct {
	User             db.User
	Version          string
	DesiredRetention float64

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
```

### 2. Call sites — replace each `map[string]any{...}` literal

`render(w, pages["settings"], status, map[string]any{...})` becomes
`render(w, pages["settings"], status, view)` where `view` is built once per branch (or once at
the top of the handler when the retention lookup happens before any branching) and mutated for
the specific Error/Success field before each `render` call.

**`GET /settings` (line ~56-64):**
```go
user, _ := auth.UserFromContext(r.Context())
retention, err := currentRetention(r.Context(), store, user.ID)
if err != nil {
	serverError(w, r, err)
	return
}
render(w, pages["settings"], http.StatusOK, buildSettingsView(user, retention))
```
(No message fields — same as today, just via the struct.)

**`POST /settings` (line ~66-114):** this handler does not have `retention` in scope today — it
never called `currentRetention`. Add that call once at the top (mirrors what `GET /settings` and
`POST /settings/avatar` already do), then build the view once and set only the relevant field per
branch:

```go
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

user.DisplayName = displayName
user.Timezone = timezone
if atoiErr == nil && dayStartHour >= math.MinInt16 && dayStartHour <= math.MaxInt16 {
	user.DayStartHour = int16(dayStartHour)
}

if atoiErr != nil || dayStartHour < math.MinInt16 || dayStartHour > math.MaxInt16 {
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
```

**`POST /settings/password` (line ~116-164):** same pattern — add a `currentRetention` call
after `parseForm`/before the branches (currently missing entirely), then per branch:

```go
retention, err := currentRetention(r.Context(), store, user.ID)
if err != nil {
	serverError(w, r, err)
	return
}
...
if newPassword != confirmPassword {
	view := buildSettingsView(user, retention)
	view.PasswordError = "Passwords do not match"
	render(w, pages["settings"], http.StatusBadRequest, view)
	return
}

token, err := a.ChangePassword(...)
if err != nil {
	...
	view := buildSettingsView(user, retention)
	view.PasswordError = msg
	render(w, pages["settings"], status, view)
	return
}

auth.SetSessionCookie(w, token)
view := buildSettingsView(user, retention)
view.PasswordSuccess = "Password changed"
render(w, pages["settings"], http.StatusOK, view)
```

**`POST /settings/fsrs` (line ~166-201):** already computes `retention` locally (it's the form
input, not `currentRetention` — correct, since the FSRS form's whole point is to change it).
Replace each of the 3 map literals:

```go
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
...
view := buildSettingsView(user, retention)
view.FsrsSuccess = "Retention target updated"
render(w, pages["settings"], http.StatusOK, view)
```

**`POST /settings/avatar` (line ~207-293):** already calls `currentRetention` up front; just
drop `Version` entirely since `buildSettingsView` supplies it. Replace all 6 map literals:

```go
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
...
data, err := io.ReadAll(file)
if err != nil {
	view := buildSettingsView(user, retention)
	view.AvatarError = "Could not read upload"
	render(w, pages["settings"], http.StatusBadRequest, view)
	return
}

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
...
// (blob/tx handling unchanged)
...
user.AvatarSha256 = pgtype.Text{String: sha, Valid: true}
view := buildSettingsView(user, retention)
view.AvatarSuccess = "Avatar updated"
render(w, pages["settings"], http.StatusOK, view)
```

### 3. `notFoundPage` and other pages

Out of scope per the issue ("the same view-struct treatment is worth considering for the other
multi-branch page handlers, but `/settings` is the one where it actually bites today"). No change
to `templates.go`'s `map[string]any{"User": user}` usage in `notFoundPage`, or to any other page
handler.

### 4. Template

No changes needed. `errorMsg`/`successMsg` already handle an empty string; `.Version` and
`.DesiredRetention` will now be populated on every branch instead of sometimes being an absent
map key.

## Files touched

- `internal/http/settings.go` — add `settingsView` struct + `buildSettingsView`; replace all 17
  `map[string]any{...}` render-data literals across the 5 handlers with a `settingsView` built via
  `buildSettingsView` and mutated per branch; add the missing `currentRetention` call to
  `POST /settings` and `POST /settings/password` (currently absent, which is *why* they omit
  `DesiredRetention` today).

No other files change. No migration, no schema change, no template change.

## Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` (per CLAUDE.md §14
  pre-commit sequence).
- Existing handler tests (if any cover `/settings/*`) should still pass unchanged since response
  status codes and message strings are unchanged — only how the render data is assembled changes.
- Manual verification steps to describe to the user (not to run per CLAUDE.md §14 "don't run the
  app"):
  1. Log in, go to Settings, submit a display-name change — Desired retention field should still
     show the current value (previously blank/`<no value>` bug — but note bug 1 as described
     needs the *value* attribute of the number input to show something other than `<no value>`;
     confirm by viewing page source, not just the rendered widget, since a blank number input and
     one containing the literal text `<no value>` look different).
  2. Submit a password change — same check.
  3. Upload an avatar (or trigger an avatar error, e.g. non-JPEG) — "Version x.y.z" line at top
     of page should show the real version, not "Version <no value>".

## Open questions

1. **Should `buildSettingsView` take `user db.User` by value or should handlers keep mutating
   `user` in place before calling it?** The plan above keeps today's existing pattern (handlers
   mutate the local `user` variable for sticky form values, e.g. in `POST /settings` on
   validation failure) and just passes that mutated value into `buildSettingsView`. This matches
   current behavior exactly; flagging only because it means `buildSettingsView` does not itself
   own "what displays as the sticky value," the handler still does.
2. **`POST /settings` and `POST /settings/password` currently never call `currentRetention`
   at all** (confirmed by reading the code — this is the direct cause of symptom 1, not just a
   missing map key). This plan adds that call to both, matching what `GET /settings` and
   `POST /settings/avatar` already do. This is a one-line-per-handler behavior addition (an extra
   DB read on every profile/password POST) rather than a pure refactor — flagging in case a
   cheaper alternative (e.g. threading retention through some other already-fetched value) is
   preferred, though no such value is currently fetched in either handler.
3. **Whether to extend `settingsView` treatment to `notFoundPage` or other multi-branch pages** —
   the issue explicitly defers this ("worth considering... but not today"), so this plan leaves
   it out. Confirm that deferral stands.
