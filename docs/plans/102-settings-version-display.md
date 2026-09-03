# 102 — Add Version to Settings

Plan for [#102](https://github.com/Jolls/deckshare/issues/102). Resolved design: hardcoded const,
bumped alongside `CHANGELOG.md`. No other design is in scope.

## 1. Scope

Display a read-only application version string on the Settings page. The version is a
hardcoded Go string constant, manually bumped in the same commit as each `CHANGELOG.md` entry
going forward. No build-time injection, no git-describe, no version tracking anywhere else in
the app. Confirmed by grep: no existing version constant, variable, or reference to an app
version anywhere in `.go` files outside of `fsrs_version`/`SchemaVersion` (unrelated
FSRS/apkg concepts) — this is a net-new addition.

Current top `CHANGELOG.md` entry is `## [0.1.23] - 2026-08-15`. Per CLAUDE.md §14, this PR
gets its own version bump, so the new entry and the const both become **`0.1.24`**.

## 2. Const location and definition

Add directly in `internal/http/settings.go` (package `http`), immediately below the `import`
block, before `func registerSettingsRoutes(...)`:

```go
// appVersion is bumped by hand alongside each CHANGELOG.md entry -- see CLAUDE.md §14.
const appVersion = "0.1.24"
```

## 3. Handler edit

Add `"Version": appVersion` to **every** `map[string]any{...}` literal passed to
`render(w, pages["settings"], ...)` in `internal/http/settings.go` — all four handlers
(`GET /settings`, `POST /settings`, `POST /settings/password`, `POST /settings/fsrs`), every
response branch of each (success, validation failure, error). This matches the file's existing
convention of repeating `"User"` in every literal rather than introducing a shared base-map
helper.

## 4. Template edit

`web/templates/settings.html`: add a version line directly under the `<h1>`, before the first
`<section>`:

```html
{{define "content"}}
<h1>Account settings</h1>
<p>Version {{.Version}}</p>

<section>
    <h2>Profile</h2>
    ...
```

Unconditional (no `{{if}}` guard) — `appVersion` is always a non-empty compile-time constant.

## 5. `cmd/enshu/main.go` / `cmd/seed/main.go`

No changes — neither references any version-like construct today, and a `--version` CLI flag
is out of scope (issue only asks for Settings page display).

## 6. Tests

No new test required — displaying a hardcoded string is UI-only and not meaningfully
regression-prone (CLAUDE.md §5).

## 7. Changelog and version bump

Add a new top entry above `## [0.1.23]`:

```
## [0.1.24] - 2026-08-15

### Added
- App version now displayed on the Settings page, read from a hardcoded constant bumped
  alongside this changelog on every release, making it easy to tell which version is running
  ([#102](https://github.com/Jolls/deckshare/issues/102))
```

`appVersion` must match: `"0.1.24"`. After committing, tag `v0.1.24`.

## Open questions

None.
