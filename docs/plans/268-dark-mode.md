# Plan: #268 Dark mode (UI and card rendering)

Applied on the same branch after #270 (Pico vendored under `/static`). Do not touch the Pico `<link>` in `layout.html`.

Already decided: per-user column on `users` (default `'auto'`, CHECK constraint); picker is an "Appearance" section on `/settings` with a plain form POST to a new route; logged-out pages stay Auto. §7 depends on Q1/Q2; §6 on Q4; the column name on Q3. See "Resolved decisions" at the bottom.

## 0. Prerequisite checks (after #270 is in the branch)
Grep `web/static/pico.min.css` for: bare `[data-theme=light]` attribute selector (not only `:root[data-theme=light]`) — needed by §7(a); `--pico-ins-color` and `--pico-del-color` — needed by §6. If any is missing, stop and report.

## 1. Migration
`goose -dir migrations create -s users_color_scheme sql` → `migrations/00023_users_color_scheme.sql`:
```sql
-- +goose Up
-- #268: per-user colour scheme. 'auto' (default) renders no data-theme attribute, so Pico follows
-- prefers-color-scheme; 'light'/'dark' are rendered server-side as <html data-theme="..."> so
-- first paint is correct. Allowlist must match colorSchemes in internal/http/settings.go.
ALTER TABLE users ADD COLUMN color_scheme text NOT NULL DEFAULT 'auto'
    CONSTRAINT users_color_scheme_check CHECK (color_scheme IN ('auto', 'light', 'dark'));

-- +goose Down
ALTER TABLE users DROP COLUMN color_scheme;
```
Apply to a fresh DB and the populated dev DB.

## 2. Query and sqlc
Append to `internal/db/queries/users.sql`:
```sql
-- Appearance (#268). Keyed only on the session user's id -- no id ever comes from the form.
-- name: UpdateUserColorScheme :exec
UPDATE users SET color_scheme = $2 WHERE id = $1;
```
Run `go generate ./internal/db/`; expect changes in `models.go` (`User.ColorScheme`), `users.sql.go`, `sessions.sql.go` (`GetSessionUser` embed). Never hand-edit.

## 3. Handler — `internal/http/settings.go`
- Below `maxAvatarDimension`: `var colorSchemes = map[string]bool{"auto": true, "light": true, "dark": true}` (comment: must match `users_color_scheme_check`, exact match).
- `settingsView`: add `AppearanceError`, `AppearanceSuccess` after `FsrsSuccess`.
- Register `POST /settings/appearance` after `POST /settings/fsrs`, following the fsrs/password pattern: `auth.RequireUser`, `parseForm`, `currentRetention`, validate `color_scheme` against `colorSchemes` (400 with "Choose Light, Dark or Auto" re-rendering settings), `db.New(store).UpdateUserColorScheme` with the session user's ID, set `user.ColorScheme = scheme` so the response paints in the new scheme, render 200 with `AppearanceSuccess = "Appearance updated"`. CSRF is covered by the central Origin check.

## 4. `web/templates/settings.html`
Insert after the Profile `</section>` an "Appearance" `<section>` with `errorMsg`/`successMsg` templates and a form posting to `/settings/appearance`: a `Theme` fieldset of three radios (`auto` "Auto (match device)", `light`, `dark`) with `checked` from `.User.ColorScheme`, and an "Update appearance" button.

## 5. `web/templates/layout.html`
Line 2 becomes:
`<html lang="en"{{with .User}}{{if eq .ColorScheme "light" "dark"}} data-theme="{{.ColorScheme}}"{{end}}{{end}}>`
(`with` is false when `User` is absent → logged-out pages stay Auto; a zero `db.User{}` also renders no attribute.)

## 6. Message colours (Q4)
- `messages.html`: `<p style="color: red;">` → `<p class="msg-error">`; `<p style="color: green;">` → `<p class="msg-success">`.
- `import_ai.html:34`: `<ul style="color: red;">` → `<ul class="msg-error">`.
- `app.css`: append `.msg-error { color: var(--pico-del-color); }` and `.msg-success { color: var(--pico-ins-color); }` with a short comment.
Charts (#261) need no change (`currentColor` only; pinned by C4).

## 7. Card rendering (Q1)
### 7(a) Light surface
- `review.html:13`, `study.html:13`: `<section id="review-stage" hidden class="review-card deckshare-card" data-theme="light">` (keeps existing substring assertions in `review_test.go:831`, `:1063` matching).
- `note_preview.html:7`: `<article class="deckshare-card" data-theme="light">`.
- `review_cards.html` unchanged.
- `app.css`: append `:where(.deckshare-card[data-theme="light"]) { background-color: var(--pico-background-color); color: var(--pico-color); }` with a comment (cards are authored for a light page; zero specificity so note-type rules win).
- No Go, JS or sanitiser change.
### 7(b) Honour `.nightMode` only — `css.go` rewrites night rules into `html[data-theme=dark] S` and a `prefers-color-scheme` wrapped form; update `security.go` comment and the `css_test.go` "non-card selector prefixed" row.
### 7(c) Both — 7(b) plus per-card light fallback (plumb `note_type_id` into `review.Card`, refill night-awareness, `data-light-surface`, `review.js` toggling).

## 8. Docs
- `docs/routes.md` Settings: heading append `, appearance -- built (#268)`; `GET /settings` mentions appearance; new row `POST /settings/appearance | session | Set the caller's colour scheme (users.color_scheme: auto/light/dark, #268). Writes only the caller's own row (id from the session, never the form); unknown values 400. Rendered server-side as data-theme on <html>, none for auto`.
- `docs/architecture.md` §5: bullet on per-user UI preferences living on `users` (`color_scheme`).
- §8 (for 7(a)): "Dark mode (#268)" paragraph — card containers carry `data-theme="light"`; zero-specificity rule; note-type CSS cannot observe/change it (css.go refuses html/:root/attribute selectors; `data-*` not on the sanitiser allowlist); `.nightMode` rules are sanitised like any other class and never match.
- §20 (7(a)/7(c)): new row "Night-mode card styling" — cards render on a light surface in dark mode; `.nightMode` rules kept but never activate; deferred, not refused (shared `#review-stage`, Auto needs the OS scheme the server cannot see). 7(b): no row; describe emission in §8.
- `docs/schema.md`: users block add `color_scheme text DEFAULT 'auto'` + comment; sentence after CHECK paragraph.
- `docs/schema-diagram.md`: add `text color_scheme "auto | light | dark"` to the first `USERS` entity after `day_start_hour`.
- CHANGELOG handled at land time.

## 9. Test plan
DB-backed tests run in their own rolled-back `beginTx`, assert only on rows they created (CLAUDE.md §16).

### 9.1 Coverage audit (existing)
- `settings_test.go`: `TestSettingsRoutes_AllowDeny`, `TestPostSettingsWithoutOrigin_403`, `TestPostSettingsPasswordWithoutOrigin_403`, `TestSettingsProfileGoldenPath`, `TestSettingsPasswordGoldenPath`, `TestSettingsPasswordMismatch`, `TestSettingsFsrsGoldenPath`, `TestSettingsFsrsInvalidRetention`, avatar tests.
- `auth_test.go`: `TestRoutes_NoSession`, `TestRoutes_ValidSession`.
- `stats_test.go`: `TestStatsTemplateRenders`, `TestStatsRoute_ChartsInlineSVGNoScript`.
- `review_test.go`: `TestReviewPage_HiddenCardShape`, `TestStudyAll_MixesAcrossDecks`.
- `note_preview_test.go`: `TestNotePreview_NonCloze_WritesNoSchedulingState`, `TestNotePreview_Cloze_OneCardPerOrdinal`, `TestNotePreview_CSSIsSanitised`.
- `security_test.go`: CSP tests. `render/css_test.go`: `TestSanitiseCSS`, `TestSanitiseCSS_NoMarkupInOutput`. `render/sanitise_test.go`: `TestSanitiseCardHTML_KeepsSafeConstructs`, `TestSanitiseCardHTML_XSSFixtures`.
- Gaps: no `<html>` attribute assertion; chart colours unpinned; card HTML carrying `data-theme` unpinned; no `internal/db` test of a `users` CHECK.

### 9.2 Characterization tests (must pass on unchanged code; add first)
- **C1** `internal/http/layout_test.go` (new, template-only): `TestLayout_LoggedOutPagesHaveNoThemeAttr` — login, signup, reset_password pages contain `<html lang="en">` and no `data-theme`.
- **C2** `render/sanitise_test.go`: `TestSanitiseCardHTML_StripsDataTheme` — `<div data-theme="dark" class="nightMode">x</div>` loses `data-theme`, keeps `x` and `class="nightMode"`.
- **C3** `render/css_test.go`: new `TestSanitiseCSS` rows — `[data-theme=dark] .x`, `html[data-theme=dark] .card`, `:root .card` outputs contain no `data-theme`, `html` or `:root`.
- **C4** `stats_test.go`: `TestStatsCharts_NoFixedColours` — every `fill`/`stroke` value in both chart SVGs is `currentColor` or `none`.

### 9.3 Red tests
| ID | Test (file) | Assertion | Fails today because |
|---|---|---|---|
| R1 | `TestSettingsRoutes_AllowDeny` (2 rows) | POST `/settings/appearance` `color_scheme=dark`: no session → 303; valid → 200 | route not registered |
| R2 | `TestPostSettingsAppearanceWithoutOrigin_403` | no Origin → 403; caller's `color_scheme` still `auto` | column missing |
| R3 | `TestSettingsAppearanceGoldenPath` | POST dark/light/auto: 200, row equals value, body has `<html lang="en" data-theme="dark">` / `"light"` / plain `<html lang="en">` | no route, no column |
| R4 | `TestSettingsAppearance_OnlyCallerRow` | A posts `color_scheme=dark&user_id=<B>&id=<B>` → A `dark`, B `auto` | no route/column |
| R5 | `TestSettingsAppearance_RejectsUnknownValue` | `sepia`, empty, `DARK`, ` dark`, `auto;` → 400 with "Choose Light, Dark or Auto"; row unchanged | no route |
| R6 | `TestSettingsAppearance_AppliedOnNextPage` | after POST dark, `GET /decks` has `data-theme="dark"` | session user lacks scheme |
| R7 | `TestSettingsPage_AppearanceRadiosReflectStoredValue` | fresh user: `value="auto" checked`; after light: `value="light" checked` | no Appearance section |
| R8 | `TestLayout_DataThemeAttr` (layout_test.go) | `auto`/`""` → no attr; `light`/`dark` → attr | compile error: no `ColorScheme` |
| R9 | `TestUsersColorScheme_DefaultAuto` (`internal/db/users_test.go`) | new user's `color_scheme` = `auto` | column missing |
| R10 | `TestUsersColorScheme_CheckRejectsUnknown` | `UPDATE … 'sepia'` → pg error 23514, constraint `users_color_scheme_check` | column missing |

7(a) only: **R11** `TestReviewPage_CardStageIsLightSurface` (dark user, review page has `data-theme="dark"` on html and `class="review-card deckshare-card" data-theme="light"`); **R12** `TestStudyAll_CardStageIsLightSurface`; **R13** `TestNotePreview_CardIsLightSurface` (2 cloze articles carry `data-theme="light"`). If 7(b)/(c) is chosen, R11–R13 are replaced by `TestSanitiseCSS` night-rule rows (b) / per-card `data-light-surface` assertions (c).

### 9.4 Manual only
- First paint: Dark hard-reload `/decks` (no flash); Light while OS dark; Auto follows DevTools `prefers-color-scheme` on `/decks` and `/login`.
- Dark mode every page: decks, deck, deck_edit, access, progress, stats (charts + labels), flags, notetypes, notetype_form, note_form (live preview), import, import_ai (error list), settings (messages in each section), review and study (card surface, Show Answer, ratings, flag textarea, done screen), not_found.
- Cards: seeded Basic/Cloze; imported deck with inline `style="color:black"`; note type with `.nightMode` rules; headings, links, code and `{{type:}}` input inside a card.

### 9.5 Verify
`go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` with `DATABASE_URL` exported; confirm nothing silently skipped; migration applies to a fresh DB.

## Resolved decisions
- **Q1:** 7(a) light surface. Implement §7(a) only; §7(b)/(c) are not built. R11–R13 apply. §20 row required.
- **Q2:** no sanitiser change; C2/C3 pin the facts.
- **Q3:** column and form field are `color_scheme`.
- **Q4:** yes — implement §6.
