# Plan: #52 — Auth + accounts

Phase 1, step 3 (architecture.md §11). Implements hand-rolled sessions per architecture.md §12:
`__Host-`-prefixed cookie, sliding expiration, `argon2id` password hashing, `Origin`-header CSRF
check, per-key rate limiting, timing-safe login/signup, and an in-process expired-session cleanup
ticker.

Builds on #50/#51 (schema + `sqlc` output). Nothing in `internal/auth/` exists yet beyond a package
doc stub; `internal/http/http.go` has only `NewMux` + `/healthz`; `web/templates/` is empty.

---

## 0. Resolved decisions (no judgment calls left downstream)

### 0.1 No new migration is needed

`migrations/00001_users.sql` and `00002_sessions.sql` already carry everything this step requires:

- `users`: `id`, `email`, `password_hash`, `display_name`, `timezone`, `day_start_hour`,
  `created_at`, plus `CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email))` — the
  atomicity guarantee the issue names.
- `sessions`: `id text PRIMARY KEY` (SHA-256 hex of the token), `user_id` (CASCADE),
  `expires_at timestamptz NOT NULL`, `created_at`. Sliding expiration is an `UPDATE` of
  `expires_at`; cleanup is `DELETE … WHERE expires_at < now()`.

**Deliberately not adding an index on `sessions.expires_at`.** The cleanup job runs hourly against
a table holding at most one row per active login on a single-instance Phase 1 deployment; a
sequential scan there is free, and an unused index is speculative work (CLAUDE.md rule 2). If the
sessions table ever grows past ~10⁵ rows, add it then as a new migration — never by editing
`00002` (CLAUDE.md §9/§17).

`display_name` is `NOT NULL` with no default, so the **signup form must collect it**. That settles
"email + password only, or a name too": the schema already decided.

### 0.2 Cookie

Exactly one name, hard-coded as a package constant in `internal/auth`:

```
__Host-enshu_session
```

Attributes on every `Set-Cookie` (login, signup, and sliding renewal):

| Attribute | Value | Why |
|---|---|---|
| `Path` | `/` | Required by the `__Host-` prefix. |
| `Domain` | **absent** | Required by the `__Host-` prefix — never set `Cookie.Domain`. |
| `Secure` | `true` | Required by the `__Host-` prefix. Browsers treat `http://localhost` as a secure context, so dev over plain HTTP still works. |
| `HttpOnly` | `true` | No JS reads the session token. |
| `SameSite` | `http.SameSiteLaxMode` | Lax keeps top-level GET navigation authenticated; the `Origin` check (§0.4) is the defence for state-changing requests. |
| `MaxAge` | `int(SessionLifetime.Seconds())` | 30 days (§0.3). |

Logout clears it with the same name/path and `MaxAge: -1`, empty value.

### 0.3 Session lifetime and sliding renewal

- `SessionLifetime = 30 * 24 * time.Hour`
- `RenewThreshold  = 15 * 24 * time.Hour`

On every authenticated request, after the session is loaded and found valid: if
`time.Until(expiresAt) < RenewThreshold`, `UPDATE sessions SET expires_at = now() + 30d` **and**
re-issue the `Set-Cookie` with a fresh `MaxAge`. Otherwise write no cookie and do no DB write.
These are architecture.md §12's own example values; they are settled here, not open.

### 0.4 CSRF: `Origin`-header check

Applies to every request whose method is **not** `GET`, `HEAD`, or `OPTIONS`. Enforced centrally in
the middleware (§4.4), never per handler (architecture.md §12).

Logic, in order:

1. Read `Origin`. If absent or empty → **403**. (Every browser sends `Origin` on POST; a missing
   one is a non-browser client or an ancient one, and neither is a supported form submitter.)
2. Parse with `url.Parse`. On error → **403**.
3. Build the expected origin:
   - If `Service.Config.Origin` is non-empty (env `ORIGIN`, e.g. `https://enshu.example`), compare
     the full scheme+host string, case-insensitively on the host, exactly.
   - If `Config.Origin` is empty (dev default), compare `parsed.Host` against `r.Host`,
     case-insensitively. Host-vs-Host is the standard fallback and is sound as long as the reverse
     proxy sets `Host` correctly.
4. Mismatch → **403** with body `forbidden` and no further processing.

No allowlist, no exemptions, no token-in-form double-submit — the `Origin` check is the whole
mechanism (architecture.md §3, §12).

### 0.5 Rate limiting: which key, and why

**Recommendation, implemented as specified:**

| Endpoint | Keys checked (all must pass) | Default budget |
|---|---|---|
| `POST /login` | client IP **and** `lower(email)` | IP: 20 / 15 min. Email: 5 / 15 min. |
| `POST /signup` | client IP **only** | 5 / hour |

Reasoning:

- **Login is keyed on both.** The email key is what stops credential-stuffing a single known
  account from a botnet of IPs; the IP key is what stops one host spraying many accounts. Either
  alone leaves the other attack unbounded.
- **Signup is keyed on IP only, deliberately.** An email-keyed signup limiter answers differently
  for an address that has been tried before, which is an enumeration oracle in the same family
  architecture.md §12 spends four bullets closing. IP-keying is the only key that does not vary
  with the account's existence.
- The email key on login is **not** an oracle: the limiter is consulted before the account lookup
  and its counter increments identically for existing and non-existing addresses.

Client IP = `net.SplitHostPort(r.RemoteAddr)` host. **Do not read `X-Forwarded-For`** — it is
attacker-controlled unless a trusted-proxy config exists, and none does. Add one when a deployment
needs it, as its own issue.

Mechanism: hand-rolled fixed-window counter, no new dependency.

- `map[string]*window` guarded by a `sync.Mutex`, where `window{count int; resetAt time.Time}`.
- `Allow(key string) bool`: if now ≥ `resetAt`, reset `count=0, resetAt=now+window`; increment;
  return `count <= limit`.
- Clock is an injectable `now func() time.Time` field (defaults to `time.Now`) so tests are
  deterministic — no sleeps.
- `Sweep()` deletes entries whose `resetAt` is past; called by the same cleanup ticker (§0.7), which
  is what bounds memory.
- Over-limit response: **429** with `Retry-After: <seconds until resetAt>`.

Per-process and in-memory is correct for Phase 1's single instance (architecture.md §12); revisit
only if the deployment goes multi-instance.

### 0.6 Timing-safe login and signup

**Shared constant**, computed once in `auth.New` (not `init`, so an error is returnable):

```go
const dummyPassword = "enshu-timing-safe-dummy-password"
// s.dummyHash, err = argon2id.CreateHash(dummyPassword, argon2id.DefaultParams)
```

Hash params are `argon2id.DefaultParams` everywhere — signup, login, and the dummy — so all three
cost the same.

**Login (`Service.Login`):**

1. Validate input shape first (§0.8). Rejections here are deterministic on the attacker's own input
   and never on account existence — this is explicitly permitted by architecture.md §12.
2. Rate-limit check (§0.5).
3. `GetUserByEmail(lower(email))`. On `pgx.ErrNoRows`, set `hash = s.dummyHash` and `found = false`;
   on success, `hash = user.PasswordHash`, `found = true`. Any other error → 500.
4. `match, err := argon2id.ComparePasswordAndHash(password, hash)` — **run unconditionally**, in
   both branches.
5. `if !found || !match` → identical `ErrInvalidCredentials`, identical response (re-render the
   login form with "Email or password is incorrect", status **401**). Never distinguish the two.
6. On success → `createSession` (§0.9) → 303 redirect.

**Signup (`Service.Signup`):**

1. Validate input shape (§0.8).
2. Rate-limit check.
3. Run these two **in parallel**, both unconditionally, using a `sync.WaitGroup` and two
   result/err variables (no new dependency; `golang.org/x/sync` stays indirect):
   - goroutine A: `hash, hashErr = argon2id.CreateHash(password, argon2id.DefaultParams)`
   - goroutine B: `exists, existsErr = q.EmailExists(ctx, email)`
   `wg.Wait()`, then check both errors.
4. If `exists` → return `ErrEmailTaken` (**409**, form re-rendered with "That email is already
   registered"). The hash has already been paid for, so a duplicate costs the same wall clock as a
   real signup.
5. Otherwise `CreateUser` with `ON CONFLICT (lower(email)) DO NOTHING RETURNING …`. **Zero rows
   returned → also `ErrEmailTaken`, same 409.** The unique index is the real guarantee; step 4 is a
   convenience path and step 5 is what makes the race correct.
6. On success → `createSession` → 303 redirect to `/`.

### 0.7 Session cleanup ticker

`internal/auth/cleanup.go`:

```go
// Run deletes expired sessions and sweeps the rate limiters until ctx is cancelled.
func (s *Service) Run(ctx context.Context, interval time.Duration)
```

- `time.NewTicker(interval)`, `defer ticker.Stop()`, `select` on `ctx.Done()` and `ticker.C`.
- On each tick: `n, err := s.q.DeleteExpiredSessions(ctx)`; on error `log.Printf("session cleanup: %v", err)`
  and continue (never return, never panic). Then `s.loginIPLimiter.Sweep()`,
  `s.loginEmailLimiter.Sweep()`, `s.signupIPLimiter.Sweep()`.
- **Interval: 1 hour.** Expired sessions are already rejected at read time (§0.9), so cleanup is
  housekeeping, not correctness — hourly is ample and costs one `DELETE` per hour.
- Started in `cmd/enshu/run()` as `go svc.Run(ctx, time.Hour)` immediately after the service is
  built, using the same `signal.NotifyContext` ctx that already shuts the server down.

### 0.8 Input validation (identical rules on both endpoints)

Applied before any DB or hashing work, deterministic on input alone:

- `email`: trimmed of surrounding space; non-empty; `len ≤ 254`; parses via `net/mail.ParseAddress`
  and `addr.Address == email` after trim. Stored as submitted; uniqueness is on `lower(email)`.
- `password`: `8 ≤ len(password) ≤ 256` bytes. The upper bound bounds argon2 DoS; there is no
  bcrypt-style 72-byte truncation to work around.
- `display_name` (signup only): trimmed; non-empty; `len ≤ 100`.
- Any failure → re-render the form with a field error, status **400**.

### 0.9 Session token

- 32 bytes from `crypto/rand.Read` → `base64.RawURLEncoding.EncodeToString` = the cookie value
  (the raw token, which never reaches the database).
- Row id = `hex.EncodeToString(sha256.Sum256([]byte(token))[:])`.
- Lookup joins and filters on expiry in SQL (`AND expires_at > now()`), so an expired row is
  indistinguishable from an absent one at the Go layer and no clock comparison is duplicated.

### 0.10 Post-login landing page

routes.md says `GET /` redirects to `/decks` when authed, but `/decks` is Phase 1 **step 5** and
does not exist — redirecting there now yields a 404. **Decision:** for step 3, `GET /` renders a
minimal `home.html` for an authed user (greeting by `display_name`, a logout button, a line saying
deck management arrives in step 5) and redirects to `/login` otherwise. Signup and login both
redirect (303) to `/`. Add a one-line note to routes.md; step 5 replaces the placeholder with the
`/decks` redirect.

---

## 1. Dependency change

```
go get github.com/alexedwards/argon2id@latest
go mod tidy
```

This pulls `golang.org/x/crypto` as a direct-of-indirect dependency. Commit `go.mod` and `go.sum`.
No other dependency is added — Pico CSS is loaded from CDN in the layout template (architecture.md
§3), htmx is not needed for auth forms (plain HTML forms, per routes.md conventions).

---

## 2. SQL queries to add (then regenerate)

### 2.1 `internal/db/queries/sessions.sql` — append

```sql
-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3);

-- name: GetSessionUser :one
SELECT sqlc.embed(u), s.expires_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.expires_at > now();

-- name: RenewSession :exec
UPDATE sessions SET expires_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now();
```

(`sqlc.embed(u)` gives a `GetSessionUserRow{User User; ExpiresAt pgtype.Timestamptz}` — one round
trip for session + user, which is what every authenticated request needs.)

### 2.2 `internal/db/queries/users.sql` — append

```sql
-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(sqlc.arg(email));

-- name: EmailExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower(sqlc.arg(email)));

-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name)
VALUES ($1, $2, $3)
ON CONFLICT (lower(email)) DO NOTHING
RETURNING *;
```

### 2.3 Regenerate

`go generate ./internal/db` (runs `sqlc generate -f ../../sqlc.yaml`). This rewrites
`internal/db/sessions.sql.go`, `internal/db/users.sql.go`, and `internal/db/querier.go`
(`emit_interface: true`). **Never hand-edit generated files** (CLAUDE.md §16); commit the output.

---

## 3. New package files — `internal/auth/`

### 3.1 `internal/auth/auth.go`

```go
package auth

const (
    CookieName      = "__Host-enshu_session"
    SessionLifetime = 30 * 24 * time.Hour
    RenewThreshold  = 15 * 24 * time.Hour
)

var (
    ErrInvalidCredentials = errors.New("auth: invalid email or password")
    ErrEmailTaken         = errors.New("auth: email already registered")
    ErrRateLimited        = errors.New("auth: too many attempts")
)

// Config carries the deployment-dependent knobs. Zero values are the dev defaults.
type Config struct {
    Origin string // e.g. "https://enshu.example"; empty means compare Origin's host to r.Host
}

type Service struct {
    q         *db.Queries
    cfg       Config
    dummyHash string

    loginIP     *limiter
    loginEmail  *limiter
    signupIP    *limiter
}

// New builds the service over any DBTX -- a *pgxpool.Pool in production, a pgx.Tx in tests.
// It computes the fixed dummy argon2id hash once, which is what makes login timing-safe.
func New(dbtx db.DBTX, cfg Config) (*Service, error)
```

`New` builds the three limiters with the §0.5 budgets and calls
`argon2id.CreateHash(dummyPassword, argon2id.DefaultParams)`, wrapping any error as
`fmt.Errorf("compute dummy hash: %w", err)`.

Also in this file:

- `func (s *Service) Signup(ctx context.Context, ip, email, password, displayName string) (db.User, string, error)`
  — returns the user and the **raw session token**, per §0.6.
- `func (s *Service) Login(ctx context.Context, ip, email, password string) (db.User, string, error)`
  — per §0.6.
- `func (s *Service) Logout(ctx context.Context, token string) error` — hashes the token, calls
  `DeleteSession`. Deleting an absent row is not an error.
- `func (s *Service) createSession(ctx context.Context, userID pgtype.UUID) (string, error)` — §0.9.
- `func hashToken(token string) string` — §0.9.
- `func newToken() (string, error)` — §0.9.
- `func validateEmail`, `validatePassword`, `validateDisplayName` — §0.8, each returning a
  user-safe message string plus a bool.

### 3.2 `internal/auth/ratelimit.go`

```go
type limiter struct {
    mu     sync.Mutex
    limit  int
    window time.Duration
    now    func() time.Time // injectable for tests
    seen   map[string]*bucket
}

type bucket struct {
    count   int
    resetAt time.Time
}

func newLimiter(limit int, window time.Duration) *limiter
func (l *limiter) Allow(key string) (ok bool, retryAfter time.Duration)
func (l *limiter) Sweep()
```

Exactly as described in §0.5. No exported surface — the handlers go through `Service.Login` /
`Service.Signup`, which consult the limiters themselves and return `ErrRateLimited` plus a
retry-after duration. Keep the return shape as `(db.User, string, error)` where a rate-limit
rejection surfaces as a `RateLimitError` type wrapping `ErrRateLimited`:

```go
type RateLimitError struct{ RetryAfter time.Duration }
func (e *RateLimitError) Error() string { return "auth: too many attempts" }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }
```

so the handler recovers the retry-after duration via `errors.As`.

### 3.3 `internal/auth/middleware.go`

```go
type ctxKey struct{}

// UserFromContext returns the authenticated user, if any.
func UserFromContext(ctx context.Context) (db.User, bool)

// Middleware enforces the Origin check on state-changing requests and populates the session
// for every request. Wrapping is central and unconditional (architecture.md §12) -- a new route
// cannot ship without it.
func (s *Service) Middleware(next http.Handler) http.Handler

// RequireUser rejects unauthenticated requests with a 303 to /login, for every method -- this
// is a form-based HTML app, not a JSON API, so a POST that arrives unauthenticated (e.g. a
// session that expired mid-page) redirects to login rather than surfacing a bare 401. Consistent
// with the POST /logout and POST /settings* rows in §4.5 and §9.7's route tables.
func RequireUser(next http.Handler) http.Handler
```

`Middleware` order, per request:

1. If method is state-changing → `checkOrigin(r)`; on failure write 403 and return (before any DB
   work).
2. Read the `__Host-enshu_session` cookie. Absent → call `next` with no user in context.
3. `GetSessionUser(ctx, hashToken(value))`. `pgx.ErrNoRows` (absent **or** expired) → clear the
   cookie and call `next` with no user. Other error → 500.
4. Put the user in the request context.
5. If `time.Until(row.ExpiresAt.Time) < RenewThreshold` → `RenewSession` with
   `now + SessionLifetime` and re-issue the cookie **before** calling `next` (headers must be
   written before the handler writes a body).
6. `next.ServeHTTP(w, r.WithContext(ctx))`.

Also `func (s *Service) checkOrigin(r *http.Request) bool` implementing §0.4, and
`func SetSessionCookie(w http.ResponseWriter, token string)` /
`func ClearSessionCookie(w http.ResponseWriter)` implementing §0.2 (exported so the handlers can
call them after login/signup/logout).

### 3.4 `internal/auth/cleanup.go`

`func (s *Service) Run(ctx context.Context, interval time.Duration)` exactly as §0.7.

### 3.5 `internal/auth/doc.go`

Leave the existing package comment as-is. Do not add a second package clause.

---

## 4. HTTP layer

### 4.1 New: `web/embed.go`

```go
// Package web embeds the server-rendered HTML templates.
package web

import "embed"

//go:embed templates/*.html
var Templates embed.FS
```

`//go:embed` cannot reach above its own package directory, which is why this file lives in `web/`
rather than in `internal/http/`.

### 4.2 New: `internal/http/templates.go`

```go
func parseTemplates() (map[string]*template.Template, error)
```

Parses each page as its own set with `layout.html`, per §4.3. `html/template`, auto-escaping — no
`text/template` anywhere.

### 4.3 New templates — `web/templates/`

- `layout.html` — `{{define "layout"}}`: `<!doctype html>`, `<meta name="viewport">`, Pico CSS from
  CDN, `<main class="container">{{template "content" .}}</main>`.
- `login.html` — `{{define "content"}}` with `<form method="post" action="/login">`: email,
  password, submit. Renders `{{.Error}}` when non-empty and re-fills `{{.Email}}` (never the
  password). Link to `/signup`.
- `signup.html` — same shape, `action="/signup"`, fields email, display name, password. Re-fills
  email and display name.
- `home.html` — greeting by `.User.DisplayName`, `<form method="post" action="/logout">` with a
  submit button, and the step-5 placeholder line (§0.10).

Each page template defines `content` and is executed via the `layout` template. Because
`ParseFS` puts them all in one set with a shared `content` name, execute them as **separate
template sets**: parse per page (`template.ParseFS(web.Templates, "templates/layout.html", "templates/login.html")`
etc.) into a `map[string]*template.Template` keyed `"login"`, `"signup"`, `"home"`. That is the
standard way around `html/template`'s flat namespace and is what `parseTemplates` returns.

Add `func render(w http.ResponseWriter, t *template.Template, status int, data any)` in
`templates.go`: render into a `bytes.Buffer` first, and only on success write the status and copy
the buffer — so a template error never emits a half-page with a 200.

### 4.4 Rewrite: `internal/http/http.go`

```go
// NewHandler builds the application's top-level handler: the route mux wrapped in the auth
// middleware, so the CSRF check and session population run for every request (architecture.md §12).
func NewHandler(pool *pgxpool.Pool, a *auth.Service) (http.Handler, error) {
    pages, err := parseTemplates()
    if err != nil { return nil, fmt.Errorf("parse templates: %w", err) }

    mux := http.NewServeMux()
    mux.HandleFunc("GET /healthz", healthHandler(pool))
    registerAuthRoutes(mux, a, pages)
    return a.Middleware(mux), nil
}
```

`healthHandler` is unchanged. The old exported `NewMux` goes away — its only caller is `main.go`,
updated in §4.6.

### 4.5 New: `internal/http/auth.go`

```go
func registerAuthRoutes(mux *http.ServeMux, a *auth.Service, pages map[string]*template.Template)
```

registers exactly these, matching routes.md's Auth table:

| Method | Path | Handler behaviour |
|---|---|---|
| `GET` | `/signup` | Authed → 303 `/`. Else render `signup` (200). |
| `POST` | `/signup` | `r.ParseForm`; `a.Signup(...)`. Success → `auth.SetSessionCookie`, 303 `/`. `ErrEmailTaken` → render `signup` with 409. Validation error → 400. `RateLimitError` → 429 + `Retry-After`. Other → 500. |
| `GET` | `/login` | Authed → 303 `/`. Else render `login` (200). |
| `POST` | `/login` | `a.Login(...)`. Success → cookie + 303 `/`. `ErrInvalidCredentials` → render `login` with 401 and the single generic message. Validation → 400. Rate limit → 429. |
| `POST` | `/logout` | `a.Logout(ctx, cookieValue)`, `auth.ClearSessionCookie`, 303 `/login`. Wrapped in `auth.RequireUser`. |
| `GET` | `/` | Authed → render `home` (200). Else 303 `/login`. |

All redirects use `http.StatusSeeOther` (303) so a form POST is not re-submitted on back/refresh.
Handlers stay thin — parse, delegate, respond (architecture.md §4); no comments (CLAUDE.md §9).

Client IP passed to the service: `host, _, _ := net.SplitHostPort(r.RemoteAddr)` — if
`SplitHostPort` errors, use `r.RemoteAddr` verbatim as the key.

### 4.6 Edit: `cmd/enshu/main.go`

Inside `run()`, after the pool is opened and before the server is constructed:

```go
authSvc, err := auth.New(pool, auth.Config{Origin: os.Getenv("ORIGIN")})
if err != nil {
    return fmt.Errorf("init auth: %w", err)
}
go authSvc.Run(ctx, time.Hour)

handler, err := apphttp.NewHandler(pool, authSvc)
if err != nil {
    return fmt.Errorf("build handler: %w", err)
}
srv := &http.Server{Addr: addr, Handler: handler}
```

New imports: `github.com/Jolls/enshu/internal/auth`. `ctx` is the existing
`signal.NotifyContext`, so the ticker stops on SIGINT/SIGTERM with no extra plumbing.

---

## 5. Tests

Priority mapping: this is CLAUDE.md §10.5 (access control — table-driven allow/deny; "add a row on
every new endpoint") plus the security surface CLAUDE.md rule 5 and §15's `area: security` label
call out. It does **not** touch FSRS or `.apkg`, so §10's always-ship-a-test exception is not the
reason — the reason is that every failure mode here is silent: a cookie missing `Secure`, an
`Origin` check that accepts a mismatch, or a login that answers differently for unknown accounts
all look fine in a browser.

### 5.1 `internal/auth/auth_test.go` — no DB needed

- `TestValidate` — table-driven over email/password/display-name inputs: empty, whitespace-only,
  254/255-char email, 7/8/256/257-char password, non-parsing email. Assert accept/reject.
- `TestHashToken` — same token hashes identically, different tokens differ, output is 64 hex chars.
- `TestNewToken` — two calls differ; decodes to 32 bytes.
- `TestSetSessionCookie` — call against an `httptest.ResponseRecorder`, parse the `Set-Cookie`, and
  assert **every** attribute: name is exactly `__Host-enshu_session`, `Secure`, `HttpOnly`,
  `SameSite == Lax`, `Path == "/"`, `Domain == ""`, `MaxAge == 2592000`. This is the test that keeps
  the `__Host-` prefix honest.
- `TestClearSessionCookie` — `MaxAge < 0`, empty value, same name/path.

### 5.2 `internal/auth/ratelimit_test.go` — no DB needed

Table-driven with an injected fake clock (`l.now = func() time.Time { return fake }`):

- N calls under the limit all allow; the (limit+1)-th denies with a positive `retryAfter`.
- Advancing the fake clock past the window resets the counter.
- Distinct keys do not interfere.
- `Sweep()` removes expired buckets and keeps live ones (assert `len(l.seen)`).
- Concurrency smoke: 100 goroutines × `Allow` on one key under `-race`, total allowed == limit.

### 5.3 `internal/auth/middleware_test.go` — no DB needed for the CSRF half

`TestCheckOrigin`, table-driven allow/deny (CLAUDE.md §10.5 shape), over the matrix of
{method: GET, HEAD, OPTIONS, POST, DELETE} × {Origin: absent, empty, matching, wrong host, wrong
scheme, wrong port, unparseable} × {Config.Origin: set, empty}. Assert 403 vs. pass-through, and
that a denied request never reaches the wrapped handler (use a handler that flips a bool).

### 5.4 `internal/auth/session_test.go` — DB-backed

Copy the `testPool` / `beginTx` / `nextSeq` helper block from `internal/db/deletion_test.go`
(skip unless `DATABASE_URL` is set; every test runs inside a `pgx.Tx` that is always rolled back).
Test-package-local copies, not an exported helper — Go's convention, and the reason `Service` takes
a `db.DBTX` (§3.1) is precisely so `auth.New(tx, auth.Config{})` works here.

- `TestSignup_CreatesUserAndSession` — user row exists with `lower(email)` matching; password hash
  verifies with `argon2id.ComparePasswordAndHash`; a `sessions` row exists whose id is
  `hashToken(returnedToken)`; `expires_at` is ~30 days out.
- `TestSignup_DuplicateEmail` — second signup with a different casing of the same address returns
  `ErrEmailTaken`; exactly one user row exists.
- `TestSignup_ConflictInsertPath` — insert a user directly, then call `Signup` for the same address
  → `ErrEmailTaken` from the `ON CONFLICT DO NOTHING` zero-rows branch (§0.6 step 5).
- `TestLogin_Success` / `TestLogin_WrongPassword` / `TestLogin_UnknownEmail` — the latter two both
  return `ErrInvalidCredentials`, indistinguishable, and create **no** session row.
- `TestLogin_UnknownEmailStillHashes` — assert the unknown-email path takes at least, say, half the
  wall time of the known-email path. Time-based assertions are flaky by nature, so keep the bound
  loose and generous; its job is to catch an early `return` being added above the hash, not to
  measure anything.
- `TestSession_ExpiredIsRejected` — insert a session with `expires_at = now() - 1h`;
  `GetSessionUser` returns `pgx.ErrNoRows`.
- `TestLogout_DeletesRow`.

### 5.5 `internal/http/auth_test.go` — DB-backed, end-to-end through the handler

Same skip/tx helper block (a local copy). Build the handler over a tx-backed service and drive it
with `httptest.NewRecorder`.

Table-driven **allow/deny** per CLAUDE.md §10.5, one row per endpoint × auth state:

| Route | No session | Valid session | Expired session |
|---|---|---|---|
| `GET /` | 303 → `/login` | 200 | 303 → `/login` |
| `GET /login` | 200 | 303 → `/` | 200 |
| `GET /signup` | 200 | 303 → `/` | 200 |
| `POST /logout` | 303 → `/login` (RequireUser) | 303 → `/login`, row deleted | 303 → `/login` |

Plus:

- `TestPostWithoutOrigin_403` and `TestPostWithForeignOrigin_403` on `/login`, `/signup`, `/logout`
  — and assert no user row and no session row were created.
- `TestLoginRateLimited` — 6 wrong-password POSTs for one email → the 6th is 429 with `Retry-After`.
- `TestSignupRateLimited` — 6 signups from one `RemoteAddr` → the 6th is 429.
- `TestSlidingRenewal` — two sub-cases: a session expiring in 20 days gets **no** `Set-Cookie` and
  an unchanged `expires_at`; a session expiring in 10 days gets a `Set-Cookie` and a pushed-out
  `expires_at`.
- `TestSignupSetsCookieAndLandsHome` — 303 to `/`, `Set-Cookie` present with the §0.2 attributes.

### 5.6 `internal/auth/cleanup_test.go` — DB-backed

- `TestDeleteExpiredSessions` — three rows (expired, expiring-now-ish, live); after the query only
  the live one survives, and the live user's other data is untouched.

---

## 6. Docs to update in the same PR

- `docs/routes.md` — Auth table: mark the six routes as built; add the §0.10 note that `GET /`
  renders a placeholder until step 5 supplies `/decks`.
- `docs/architecture.md` §1 — add a "Build order step 3 has landed (#52)" paragraph in the same
  shape as the existing step-1 and step-2 ones, naming `internal/auth/`, the cookie, the CSRF
  check, and the cleanup ticker.
- `CHANGELOG.md` — new version entry with `### Added` (session auth, signup/login/logout
  routes, first HTML templates, cleanup ticker) and `### Security` (`__Host-` cookie hardening,
  timing-safe login/signup, `Origin` CSRF check, per-key rate limiting), each linking
  ([#52](https://github.com/Jolls/enshu/issues/52)).

---

## 7. Implementation order

1. `go get github.com/alexedwards/argon2id`; `go mod tidy`.
2. Add the queries (§2), run `go generate ./internal/db`, commit generated output.
3. `internal/auth/ratelimit.go` + its test — self-contained, no DB, fastest feedback loop.
4. `internal/auth/auth.go` (validation, token, hashing, Signup/Login/Logout) + §5.1/§5.4 tests.
5. `internal/auth/middleware.go` + §5.3 tests.
6. `internal/auth/cleanup.go` + §5.6 test.
7. `web/embed.go`, `web/templates/*.html`, `internal/http/templates.go`.
8. `internal/http/auth.go`, rewrite `internal/http/http.go`, update `cmd/enshu/main.go`.
9. `internal/http/auth_test.go` (§5.5).
10. Docs (§6).
11. Pre-commit sequence (CLAUDE.md §14): `go build ./...`, `go vet ./...`, `golangci-lint run`,
    `go test ./...` (with `DATABASE_URL` set against the compose database so the DB-backed tests
    actually run, not skip), then the review-pass question — recommend `/code-review high`, this is
    auth.

---

## 8. Anticipated traps

- **`sqlc` and `ON CONFLICT (lower(email))`.** The conflict target is an expression index;
  Postgres accepts `ON CONFLICT (lower(email))` and `sqlc` parses it, but verify `sqlc generate`
  succeeds before building the rest on top of it. If it balks, fall back to detecting SQLSTATE
  `23505` via `pgconn.PgError` in the Go layer (the `isForeignKeyViolation` helper in
  `deletion_test.go` is the existing pattern to copy).
- **Renewal must write headers before the handler does.** Do the `Set-Cookie` in the middleware
  *before* `next.ServeHTTP`, never after.
- **`__Host-` over plain HTTP.** Browsers accept `Secure` cookies from `http://localhost` but
  **not** from `http://192.168.x.x` or a bare LAN hostname. If manual testing happens off
  localhost, it needs TLS or a proxy — say so in the manual-verification notes rather than
  weakening the cookie.
- **Do not add a "remember me" checkbox, email verification, or password reset.** All three are out
  of scope for this issue and each needs its own decision (CLAUDE.md rule 2).
- **`golangci-lint` standard set will flag unchecked errors.** `_ = err` is banned by CLAUDE.md §9;
  the cleanup ticker logs its error rather than swallowing it.

---

## 9. `/settings` account routes (in-scope per Resolved Decision 3)

routes.md scopes Phase 1 step 3 settings work to **profile + password only** — the FSRS-default
routes (`POST /settings/fsrs`, `POST /decks/{id}/settings/fsrs`) are explicitly step 9
(`user_fsrs_params` doesn't exist as a concept in the UI yet) and stay out of this PR.

### 9.1 New error type — `internal/auth/auth.go`

```go
// ValidationError carries a user-safe message for one bad field. Signup, Login, UpdateProfile,
// and ChangePassword all return this for shape failures so the HTTP layer has one type to check.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
```

`validateEmail`, `validatePassword`, `validateDisplayName` (§3.1) return `(string, bool)` as
already specified; every call site that rejects wraps the message as `&ValidationError{Msg: msg}`.
Handlers detect it with `errors.As(err, &ve)` and respond 400 with `ve.Msg`.

### 9.2 New validator — `validateTimezone`, in `internal/auth/auth.go`

```go
func validateTimezone(tz string) (string, bool) {
    if tz == "" {
        return "Unknown timezone", false
    }
    if _, err := time.LoadLocation(tz); err != nil {
        return "Unknown timezone", false
    }
    return "", true
}
```

(`time.LoadLocation("")` does not error — it resolves to a location — so the empty check is explicit.)

### 9.3 SQL — `internal/db/queries/users.sql` — append

```sql
-- name: UpdateUserProfile :exec
UPDATE users SET display_name = $2, timezone = $3, day_start_hour = $4 WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;
```

Regenerate with the same `go generate ./internal/db` call as §2.3 — one regen covers both sets of
added queries.

### 9.4 Service methods — `internal/auth/auth.go`

```go
// UpdateProfile validates and persists display_name, timezone, and day_start_hour.
func (s *Service) UpdateProfile(ctx context.Context, userID pgtype.UUID, displayName, timezone string, dayStartHour int16) error

// ChangePassword verifies currentPassword against currentHash (the caller's already-loaded
// db.User.PasswordHash, via auth.UserFromContext — this method takes no DB read of its own for
// the current hash), then validates and persists newPassword. Returns ErrInvalidCredentials if
// currentPassword does not match — never distinguishes "wrong password" from any other failure
// in the response the handler builds, consistent with Login (§0.6), since this endpoint is also
// a password-guessing surface.
func (s *Service) ChangePassword(ctx context.Context, userID pgtype.UUID, currentHash, currentPassword, newPassword string) error
```

`UpdateProfile`:
1. `validateDisplayName(displayName)`, `validateTimezone(timezone)`, and
   `0 <= dayStartHour <= 23` (else `&ValidationError{Msg: "Day start hour must be 0-23"}`) — first
   failure wins, same order as listed.
2. `s.q.UpdateUserProfile(ctx, ...)`. DB error → wrapped, not swallowed.

`ChangePassword`:
1. `validatePassword(newPassword)` (§0.8's existing 8–256 byte rule) — failure → `ValidationError`.
2. `match, err := argon2id.ComparePasswordAndHash(currentPassword, currentHash)`; `err != nil` →
   wrapped 500-class error; `!match` → `ErrInvalidCredentials`.
3. `argon2id.CreateHash(newPassword, argon2id.DefaultParams)`.
4. `s.q.UpdateUserPassword(ctx, userID, newHash)`.

No session invalidation on password change (other logged-in devices, if any, keep their sessions)
— out of scope; a `POST /settings/sessions/revoke-all` route would be a follow-up issue, not part
of #52.

### 9.5 Template — `web/templates/settings.html`

`{{define "content"}}`, one page, two `<form>`s:
- Profile form, `method="post" action="/settings"`: display name (prefilled `.User.DisplayName`),
  timezone (prefilled `.User.Timezone`, plain text input — no timezone picker widget), day start
  hour (prefilled `.User.DayStartHour`, `<input type="number" min="0" max="23">`). Submit.
- Password form, `method="post" action="/settings/password"`: current password, new password,
  confirm new password (all `type="password"`, never prefilled). Submit.
- `{{.ProfileError}}` / `{{.ProfileSuccess}}` render above the profile form;
  `{{.PasswordError}}` / `{{.PasswordSuccess}}` above the password form — independent, so a
  password-form error doesn't blank a just-shown profile success message and vice versa.

### 9.6 Handlers — new `internal/http/settings.go`

```go
func registerSettingsRoutes(mux *http.ServeMux, a *auth.Service, pages map[string]*template.Template)
```

All three wrapped in `auth.RequireUser`:

| Method | Path | Behaviour |
|---|---|---|
| `GET` | `/settings` | Render `settings` (200) with the current user's fields. |
| `POST` | `/settings` | Parse form; `a.UpdateProfile(ctx, user.ID, displayName, timezone, int16(dayStartHour))`. Success → re-render with `ProfileSuccess = "Profile updated"` (200). `*auth.ValidationError` → re-render with `ProfileError = ve.Msg`, posted values retained (400). Other error → 500. `day_start_hour` form value that fails `strconv.Atoi` or is outside 0–23 → `ProfileError` 400 before calling the service. |
| `POST` | `/settings/password` | Parse form; confirm `new_password == confirm_password` else `PasswordError = "Passwords do not match"` (400) without calling the service. Otherwise `a.ChangePassword(ctx, user.ID, user.PasswordHash, currentPassword, newPassword)`. Success → re-render with `PasswordSuccess = "Password changed"` (200), fields cleared. `ErrInvalidCredentials` → `PasswordError = "Current password is incorrect"` (401). `*auth.ValidationError` → `PasswordError = ve.Msg` (400). Other → 500. |

Registered in `internal/http/http.go`'s `NewHandler` alongside `registerAuthRoutes` (§4.4).

### 9.7 Tests

- `internal/auth/auth_test.go` — add `TestValidateTimezone` to the existing table-driven
  `TestValidate` (valid: `"UTC"`, `"America/New_York"`; invalid: `""`, `"Not/AZone"`).
- `internal/auth/session_test.go` (or a new `internal/auth/settings_test.go`, DB-backed, same
  tx-per-test helper as §5.4):
  - `TestUpdateProfile_Success` — row reflects new `display_name`/`timezone`/`day_start_hour`.
  - `TestUpdateProfile_InvalidTimezone` — `*ValidationError`, row unchanged.
  - `TestChangePassword_Success` — new password verifies via `argon2id.ComparePasswordAndHash`;
    old password no longer verifies.
  - `TestChangePassword_WrongCurrentPassword` — `ErrInvalidCredentials`, hash unchanged.
  - `TestChangePassword_WeakNewPassword` — `*ValidationError`, hash unchanged.
- `internal/http/settings_test.go` (DB-backed, same helper as §5.5):
  - Table-driven allow/deny per CLAUDE.md §10.5: `GET /settings`, `POST /settings`,
    `POST /settings/password` each with {no session → 303 `/login`; valid session → handled}.
  - `TestPostSettingsWithoutOrigin_403` / `TestPostSettingsPasswordWithoutOrigin_403` — same CSRF
    pattern as §5.5, and assert the row is unchanged.
  - `TestSettingsProfileGoldenPath` — POST valid profile fields, assert 200 + row updated.
  - `TestSettingsPasswordGoldenPath` — POST valid password change, assert 200, then a fresh
    `Login` with the new password succeeds and the old password fails.
  - `TestSettingsPasswordMismatch` — `new_password != confirm_password` → 400, row unchanged,
    service never called (verifiable since `ErrInvalidCredentials`/`ValidationError` wouldn't be
    distinguishable from this path otherwise — assert via unchanged `password_hash`).

### 9.8 Files this section adds/touches, folded into §7's implementation order and the plan's
file list

- New: `web/templates/settings.html`, `internal/http/settings.go`, `internal/auth/settings_test.go`
  (or folded into `session_test.go` — implementer's call, same package either way),
  `internal/http/settings_test.go`.
- Edited: `internal/auth/auth.go` (§9.1/9.2/9.4), `internal/db/queries/users.sql` (§9.3),
  `internal/http/http.go` (register the new routes), `web/templates/settings.html` is new so no
  existing template is touched.
- Implementation order: build `/settings` **after** step 6 (cleanup ticker) and **before** step 9
  (`internal/http/auth_test.go`) in §7's numbered list — i.e. insert as a new step 7 (
  `internal/db` query additions + `auth.go` additions + `settings_test.go`), new step 8
  (`settings.html` + `internal/http/settings.go` + its test), renumbering the old steps 7–11 to
  9–13.

---

## Resolved decisions

1. **Rate-limit budgets: accept as proposed.** Login 20/15min per IP + 5/15min per email; signup
   5/hour per IP (§0.5), unchanged.
2. **`ORIGIN` stays optional, with the `Host`-header fallback (§0.4).** No fail-fast requirement
   added to `run()`.
3. **`/settings` account routes are in scope for this PR.** See §4.7/§4.8/§5.7 below — added to
   the plan with the same zero-judgment-call precision as the rest.
4. **No email verification, confirmed.** No `users.email_verified_at` column, no SMTP dependency.
   Plan proceeds exactly as written in §0.1/§0.6.

---

### Critical Files for Implementation
- `internal/auth/auth.go` (new; `doc.go` is the existing stub in that package)
- `internal/http/http.go`
- `internal/db/queries/sessions.sql`
- `internal/db/queries/users.sql`
- `cmd/enshu/main.go`
