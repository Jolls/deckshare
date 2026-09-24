# #233 — CI: add govulncheck, gosec, and go test -race

## Summary

Three additions to `.github/workflows/ci.yml`'s `build` job:

1. `govulncheck ./...` — new step, tool pinned by tag (matches goose/sqlc convention).
2. `gosec` — enabled as a **golangci-lint linter** (`enable: [gosec]` in `.golangci.yml`),
   not a standalone `go install gosec@...` step. This matters: golangci-lint's gosec
   integration honors this repo's existing `//nolint:gosec` suppression comments
   (`cmd/seed/main.go:37`, `internal/db/notes.go:21`); the standalone `gosec` binary does
   **not** — it only recognizes its own `// #nosec` comment syntax. Verified empirically:
   running standalone `gosec ./...` against this tree still flags both of those
   already-suppressed lines; running it through `golangci-lint run` with `gosec` enabled
   does not. Using golangci-lint also means no second pinned tool version to track — gosec
   rides the already-pinned `golangci-lint-action@v7` / `version: v2.12.2`.
3. `go test -race` — see Open Questions; recommendation is to **replace** the existing
   `go test ./...` step in place rather than add a second, parallel step.

## Exploration performed (not part of the change)

Installed `gosec@latest` locally and ran it against the tree several ways to size the
noise mentioned in the issue:

- `gosec ./...` (default ruleset, whole tree): 116 issues, almost all inside
  `.local/inspect/main.go` — a gitignored scratch file (`.local/` — see `.gitignore`),
  not part of the real codebase.
- `gosec -exclude-dir=.local ./...`: 92 issues.
- Broken down by rule (`-fmt=json`): **G115** (integer overflow on narrowing conversion)
  accounts for 49 of those, **G109** (same class, via `strconv.Atoi`) another 12 — 61 of
  92 issues (66%) are this one class. Sampling the hits confirms it's noise: they're
  deliberate, already-bounds-checked narrowings from `int64`/`int`/`uint64` down to the
  `int16`/`int32` Postgres column types (`pgtype.Int2`/`Int4`), spread across
  `internal/apkg`, `internal/review`, `internal/fsrs`, `internal/http` — far too many
  call sites for per-site `//nolint:gosec` to be the right tool. This is exactly the "if
  it turns out noisy... adding it with a narrow ruleset" case the issue anticipated.
- Reproduced through `golangci-lint run` with `gosec` enabled (same pinned v2.12.2):
  confirms `settings.gosec.excludes: [G115, G109]` suppresses that class, and confirms
  the existing `//nolint:gosec` comments are respected through this path (they are not
  through standalone `gosec`).
- **Revised in review:** "not a real finding anywhere it fires" did not hold in
  `internal/apkg` — several narrowings there take values straight from an untrusted file
  (e.g. `int16(ease)` on a revlog row) with no bounds check. The G115/G109 exclusion is
  therefore scoped with `path-except` to everything *except* `internal/apkg` and
  `internal/fsrs` (CLAUDE.md §9's correctness-critical packages), where each conversion is
  either bounds-checked or carries a per-line `//nolint:gosec` naming its guard. The diff
  below is the original version.
- `-exclude-generated` (standalone) drops `internal/db/*.sql.go`'s sqlc-generated files
  entirely, killing a G101 false positive on a generated `UPDATE users SET
  password_hash = $2` query string ("potential hardcoded credential"). golangci-lint
  skips generated files by default already, so no equivalent setting is needed there.
- After excluding G115/G109 (config) and gosec on `_test.go` files (config — see below),
  **19 real findings remain** across non-test, non-generated code. Of those, 6 are all in
  `scripts/run-app/main.go`, a local dev-only launcher script never built into the
  shipped server. The remaining 13 split into "false positive, needs `//nolint:gosec`
  with a reason" and one "trivial real hardening, just fix it."

## `.golangci.yml` diff

```yaml
 version: "2"
 linters:
   default: standard
+  enable:
+    - gosec
+  settings:
+    gosec:
+      # Deliberate, already-bounds-checked narrowing conversions to Postgres pgtype
+      # column widths (int64/int -> int16/int32) throughout internal/apkg, internal/review,
+      # internal/fsrs, internal/http. Confirmed via local exploration (docs/plans/233-*.md)
+      # that this is 66% of gosec's default-ruleset output on this tree and not a real
+      # finding anywhere it fires.
+      excludes:
+        - G115
+        - G109
+  exclusions:
+    rules:
+      # Test-only weak-RNG (property-based FSRS tests seeding math/rand/v2) and loose
+      # tmp-file permissions in test fixtures are not a security surface.
+      - path: _test\.go
+        linters:
+          - gosec
+      # scripts/run-app is a local dev-only launcher (never built into the shipped
+      # server): hardcoded local Postgres URL matching docker-compose/CI's own fixture
+      # password, exec.Command of fixed dev tooling, and relaxed file perms on a
+      # developer's own machine are not a security surface either.
+      - path: scripts/run-app/
+        linters:
+          - gosec
```

(`version: "2"` schema already in use; `settings.gosec.excludes` and
`exclusions.rules[].path`/`.linters` are both confirmed working against the pinned
v2.12.2 binary.)

## `//nolint:gosec` additions needed

Following the phrasing precedent at `cmd/seed/main.go:37` (`//nolint:gosec // Anki csum
compatibility, not a security use of SHA-1`):

| File:line | Rule | Comment to add |
|---|---|---|
| `cmd/seed/main.go:759` (`sum := sha1.Sum(...)`) | G401 | `//nolint:gosec // Anki csum compatibility, not a security use of SHA-1` — same pattern as `internal/db/notes.go:21`; this call site was missed when that suppression was added. |
| `internal/db/notes.go:5` (`"crypto/sha1"` import) | G505 | `//nolint:gosec // Anki csum compatibility, not a security use of SHA-1` — the existing suppression is on the usage at line 21; golangci-lint's gosec flags the import separately (`cmd/seed/main.go:37` already has this on its import line). |
| `internal/apkg/read.go:92` (`f, err := os.Open(path)`) | G304 | `//nolint:gosec // path is a trusted local filesystem path supplied by the caller (import tooling/tests), not raw network input` |
| `internal/http/import.go:36` (`r.ParseMultipartForm(32 << 20)`) | G120 | `//nolint:gosec // body size is already capped by the http.MaxBytesReader wrap above` |
| `internal/http/settings.go:247` (`r.ParseMultipartForm(maxAvatarUploadBytes)`) | G120 | `//nolint:gosec // body size is already capped by the http.MaxBytesReader wrap above` |
| `internal/http/notes.go:652` (`http.Redirect(w, r, dest+"#notes", ...)`) | G710 | `//nolint:gosec // dest is built from a server-known deck UUID and this file's own cursor alphabet, never echoed user input (see finishBulk doc comment)` |
| `internal/media/store.go:119` (`return os.Open(path)`) | G304 | `//nolint:gosec // path is validated by sha256Hex.MatchString in path() above, not attacker-controlled` |
| `internal/render/css.go:293` (`return template.CSS(result), dropped`) | G203 | `//nolint:gosec // result already passed through SanitiseCSS in this function, not raw input` |
| `internal/render/render.go:126` (`return Rendered{HTML: template.HTML(html), ...}`) | G203 | `//nolint:gosec // html already passed through sanitiseCardHTML above (see calls at lines 95/108)` |
| `internal/render/typeanswer.go:20` (`return template.HTML(strings.Replace(...))`) | G203 | `//nolint:gosec // r.HTML is already-sanitised card output; this only substitutes a locally-built, escaped widget string into it` |

One item is a fix, not a suppression:

- **`internal/media/store.go:47`** — `os.MkdirAll(dir, 0o755)` (G301). This is the media
  blob store directory, not dev tooling; tightening to `0o750` is a one-line, zero-behavior-
  change hardening (gosec's literal ask, and the same spirit as the missing `http.Server`
  timeouts the issue cites as gosec's value-add). Recommend just changing it rather than
  suppressing.

That's 10 `//nolint:gosec` additions + 1 permission fix = 11 touched lines, all outside
`.github/workflows/ci.yml`/`.golangci.yml`, needed to land gosec green on day one.

## `.github/workflows/ci.yml` diff

```yaml
       - run: go build ./...
+      - name: Install govulncheck
+        run: go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
+      - run: govulncheck ./...
       - name: Install goose and sqlc
         run: |
           go install github.com/pressly/goose/v3/cmd/goose@v3.27.3
           go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
       - name: Apply migrations to a fresh database
         run: goose -dir migrations postgres "$DATABASE_URL" up
       - name: sqlc generate must match committed output
         run: |
           sqlc generate
           git diff --exit-code -- internal/db
       - uses: golangci/golangci-lint-action@v7
         with:
           version: v2.12.2
-      - run: go test ./...
+      - run: go test -race ./...
```

(`govulncheck` needs no DB, so it's placed right after `go build ./...` rather than after
the migration/sqlc steps — cheapest, most independent check runs first, matching the
existing ordering logic of "build, then things needing the DB, then lint, then test.")

## Resolved decision

Q1: **Option A** — replace `go test ./...` with `go test -race ./...` in place (confirmed by the
user). Q2 is informational; keep the `govulncheck@v1.8.0` pin. No open questions remain.

## Open questions (historical, resolved above)

1. **`go test -race`: replace the existing step, or run alongside it?**
   Recommendation: **replace** (the diff above does this) — `go test -race ./...` as the
   sole test step, not in addition to a plain `go test ./...`.
   - Option A (recommended): replace `go test ./...` with `go test -race ./...`. One test
     run, every push gets race coverage, no duplicated wall-clock. The issue's own framing
     ("Roughly free over the existing suite") reads as justification for turning race
     detection on by default, not for running the suite twice.
   - Option B: keep `go test ./...` and add `go test -race ./...` as a second step. Doubles
     test wall-clock (this suite includes DB-backed tests against a real Postgres service
     container, plus goose migrations — not a fast unit-only suite) in exchange for a
     faster non-race signal landing first if `-race` alone is meaningfully slower here.
     No local measurement was taken of this repo's actual `-race` overhead (would need a
     live DB via `go run ./scripts/run-app`, out of scope for a planning-only session per
     CLAUDE.md §16's "don't run the app to verify").

   If Option B is preferred, the diff is additive (`go test ./...` stays, `go test -race
   ./...` is a new line after it) instead of the in-place edit shown above.

2. **Tool version pins.** `govulncheck` pinned to `v1.8.0` (latest tag as of 2026-09-12,
   confirmed via `go list -m -versions golang.org/x/vuln`) to match this file's existing
   convention of pinning `goose@v3.27.3` / `sqlc@v1.31.1` rather than floating on
   `@latest`. No separate gosec pin is needed since it rides `golangci-lint-action@v7`'s
   already-pinned `version: v2.12.2`. Not raised as ambiguous — flagging only because
   `govulncheck` pins go stale silently (no Dependabot/renovate config in this repo today)
   and nothing in this issue's scope addresses that.
