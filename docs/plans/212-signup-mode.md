# #212 — `SIGNUP_MODE=open|closed`

Scope: minimum-viable only. `open` (default, today's behavior) and `closed`. The `invite` mode
from the issue is explicitly deferred — not designed, not scaffolded here.

## Behavior spec

- `SIGNUP_MODE` unset or `"open"`: byte-for-byte current behavior.
- `SIGNUP_MODE="closed"`: `GET /signup` returns a bare-text 404 (`notFound`, not `notFoundPage`
  — see Decision 3); `POST /signup` returns a bare-text 403, before `a.Signup` is called, so no
  row is written to `users`. Both checks apply unconditionally — including to an already
  logged-in caller — so a closed instance's `/signup` behaves as if the route doesn't exist,
  matching the issue's "404s" wording.
- Any other `SIGNUP_MODE` value: `auth.New` returns an error, so `run()` in `cmd/deckshare/main.go`
  fails at startup (via its existing `fmt.Errorf("init auth: %w", err)` wrap) rather than
  silently falling back to `open`. See Decision 1.

## Decisions (not left to implementation-time judgment)

1. **Invalid value fails fast at startup**, matching the existing precedent in the same
   `Config`/`New` for `Origin`: `internal/auth/auth.go`'s `New` already returns
   `fmt.Errorf("parse Config.Origin: %w", err)` for an unparseable `Origin`, and
   `internal/auth/auth_test.go` has `TestNew_InvalidOrigin` asserting exactly that. `SIGNUP_MODE`
   gates account creation, so a silent fallback to `open` on an operator typo would be a silent
   security regression — fail-fast is the safer and precedented choice. Case-sensitive exact
   match against `"open"` / `"closed"`; empty string means `open`.
2. **Lives on `auth.Config` / `auth.Service`, not threaded as a new parameter.** `auth.Config`
   already carries deployment knobs (`Origin`) into `auth.New`, and every call site that builds
   a handler — production (`cmd/deckshare/main.go`) and tests (`newTestHandler` in
   `internal/http/auth_test.go`) — already passes an `auth.Config` value. Adding a field there
   needs no signature changes to `registerAuthRoutes`, `NewHandler`, or `newTestHandler`.
3. **`GET /signup` closed-mode 404 uses the bare-text `notFound` helper, not the styled
   `notFoundPage`.** `notFoundPage(w, pages, user)` (`internal/http/templates.go`) always sets
   `"User": user` in the template data, and `layout.html`'s `{{if .User}}{{template "header" .}}`
   check is truthy for *any* struct value in Go's `html/template` (structs are never "empty"
   regardless of field values) — so passing a zero-value `db.User{}` for an anonymous visitor
   would render a broken header (empty display name, dead avatar link), not hide it. Every
   existing `notFoundPage` call site is behind `auth.RequireUser`, where `user` is always a real
   authenticated row; `/signup` is a public route and can be anonymous. Bare `notFound(w)` (used
   by `pathparam.go`'s doc comment for the same "no real user in context" cases) sidesteps this
   cleanly, at the cost of not matching the documented "page route → styled 404" convention in
   `templates.go`'s comment above `notFoundPage`. Flagged for confirmation — see Open questions.

## File changes

1. **`internal/auth/auth.go`**
   - Add `SignupMode string` field to `Config` (near `Origin`), with a doc comment: empty or
     `"open"` is today's behavior; `"closed"` disables new-account creation at the route layer.
   - Add `signupMode string` field to `Service`.
   - In `New`, after the existing `Origin` parsing block and before building `&Service{...}`:
     normalize `cfg.SignupMode` (default to `"open"` when empty), validate it's exactly `"open"`
     or `"closed"`, return `fmt.Errorf("invalid Config.SignupMode %q: must be \"open\" or \"closed\"", cfg.SignupMode)`
     otherwise. Store the normalized value into the new `Service.signupMode` field in the
     returned struct literal.
   - Add a small accessor: `func (s *Service) SignupOpen() bool { return s.signupMode != "closed" }`.

2. **`internal/http/auth.go`**
   - In the `GET /signup` handler (`registerAuthRoutes`), add as the first statement:
     ```go
     if !a.SignupOpen() {
         notFound(w)
         return
     }
     ```
     (before the existing "already logged in → redirect" check, per behavior spec above).
   - In the `POST /signup` handler, add as the first statement:
     ```go
     if !a.SignupOpen() {
         http.Error(w, "forbidden", http.StatusForbidden)
         return
     }
     ```
     (before `r.ParseForm()`, so nothing is parsed or written for a closed instance).

3. **`cmd/deckshare/main.go`**
   - In `run()`, change the `auth.New` call site:
     ```go
     authSvc, err := auth.New(pool, auth.Config{
         Origin:     os.Getenv("ORIGIN"),
         SignupMode: os.Getenv("SIGNUP_MODE"),
     })
     ```
     No other change — the existing `fmt.Errorf("init auth: %w", err)` wrap already surfaces a
     validation failure as a fatal startup error via `main()`'s `log.Fatal(err)`.

4. **`.env.example`**
   - Add an entry after the `MEDIA_ROOT` block (or after `ORIGIN`, either is fine — pick one),
     following the existing comment style (references CLAUDE.md/architecture section, states the
     default, states the deployment scenario):
     ```
     # Gates new-account creation (CLAUDE.md §2, docs/plans/212-signup-mode.md). "open" (the
     # default when unset) is today's behavior -- anyone can register. Set to "closed" for a
     # single-classroom or single-user deployment after seeding the account(s) you need; GET
     # /signup then 404s and POST /signup 403s. Any other value fails the server at startup.
     # SIGNUP_MODE="closed"
     ```

5. **`internal/auth/auth_test.go`**
   - Add `TestNew_InvalidSignupMode`, mirroring the existing `TestNew_InvalidOrigin`:
     ```go
     func TestNew_InvalidSignupMode(t *testing.T) {
         if _, err := New(nil, Config{SignupMode: "sometimes"}); err == nil {
             t.Error("New with an invalid Config.SignupMode should error")
         }
     }
     ```

6. **`internal/http/auth_test.go`**
   - Add two tests using the existing `newTestHandler(t, tx, cfg)` / `doRequest(...)` /
     `countRows(...)` helpers already in this file (same pattern as `TestPostWithoutOrigin_403`
     and `TestPostWithForeignOrigin_403`):
     ```go
     func TestSignupMode_ClosedBlocksGetAndPost(t *testing.T) {
         tx := beginTx(t)
         handler, _ := newTestHandler(t, tx, auth.Config{SignupMode: "closed"})
         email := testEmail()

         if w := doRequest(handler, "GET", "/signup", "", nil, ""); w.Code != 404 {
             t.Errorf("GET /signup status = %d, want 404", w.Code)
         }

         w := doRequest(handler, "POST", "/signup",
             "email="+email+"&password=correct-horse-battery&display_name=New",
             nil, "http://example.com")
         if w.Code != 403 {
             t.Errorf("POST /signup status = %d, want 403", w.Code)
         }
         if n := countRows(t, tx, `SELECT count(*) FROM users WHERE lower(email) = lower($1)`, email); n != 0 {
             t.Error("no user should have been created")
         }
     }

     func TestSignupMode_OpenExplicitUnaffected(t *testing.T) {
         tx := beginTx(t)
         handler, _ := newTestHandler(t, tx, auth.Config{SignupMode: "open"})
         if w := doRequest(handler, "GET", "/signup", "", nil, ""); w.Code != 200 {
             t.Errorf("GET /signup status = %d, want 200", w.Code)
         }
     }
     ```
   - No change needed to the existing `TestRoutes_NoSession` (`Config{}`, i.e. `SignupMode: ""` →
     defaults to open) — it already asserts `GET /signup` → 200, which continues to cover the
     unset-defaults-to-open case byte-for-byte.

7. **`docs/routes.md`**
   - Update the two `/signup` rows' description column to note the closed-mode behavior, e.g.
     append "(404 when `SIGNUP_MODE=closed`)" / "(403 when `SIGNUP_MODE=closed`)" to the existing
     `Signup form` / `Create account, ...` text.

8. **`docs/architecture.md`** §3 (Stack table)
   - Append a clause to the existing `Auth` row: "; new-account creation gated by `SIGNUP_MODE`
     (open default / closed — docs/plans/212-signup-mode.md)."

9. **`CHANGELOG.md`**
   - Add a `### Added` bullet under the next unreleased version entry (per CLAUDE.md §14, bump
     patch `z`): `SIGNUP_MODE=open|closed` env var to disable new-account registration on a
     closed instance ([#212](https://github.com/Jolls/deckshare/issues/212)).

## Testing

Covers CLAUDE.md §10 item 5 (access control, table-driven allow/deny):
- `internal/auth/auth_test.go::TestNew_InvalidSignupMode` — construction-time validation.
- `internal/http/auth_test.go::TestSignupMode_ClosedBlocksGetAndPost` — closed mode denies both
  GET and POST, and confirms no row is written (mirrors the existing no-side-effect assertions
  in `TestPostWithoutOrigin_403` / `TestPostWithForeignOrigin_403`).
- `internal/http/auth_test.go::TestSignupMode_OpenExplicitUnaffected` — explicit `"open"` behaves
  like unset (already covered by `TestRoutes_NoSession`).

Verify with `go build ./...`, `go vet ./...`, `golangci-lint run`, and
`go test ./...` with `DATABASE_URL` set (CLAUDE.md §16 — otherwise the DB-backed tests above
silently skip).

## Resolved decision

1. **`GET /signup` closed-mode 404: bare `notFound`, confirmed.** `notFoundPage`'s signature is
   not touched as part of #212 — that cross-cutting fix (making it handle an anonymous caller)
   is out of scope here.
