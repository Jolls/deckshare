# Issue #123 — Audit: auth, sessions & access control

Audit zone: `internal/auth/` (`auth.go` 391, `middleware.go` 146, `ratelimit.go` 63,
`notetypes.go` 37, `cleanup.go` 28, `doc.go` 2, plus six `_test.go` files),
`internal/http/auth.go` (130), `internal/http/security.go` (81). Line counts verified against the
current tree.

Default intent is simplification/cleanup with **no behavior change**. One genuine security defect
was found; it is in its own section (§2), marked behavior-changing, and is *not* bundled into the
cleanup section (§3).

Every edit below is a literal find/replace. Anything needing a design decision is in
**Open questions**, not decided here.

---

## 1. What was checked and found correct — no edits proposed

Stated explicitly so the reader knows these were covered, not skipped.

**Session cookie flags** (`internal/auth/middleware.go:122-146`). `Name = "__Host-enshu_session"`
(`auth.go:26`), `Path: "/"`, `Secure: true`, `HttpOnly: true`,
`SameSite: http.SameSiteLaxMode`, no `Domain`. All three `__Host-` prerequisites are actually
present, on both the set and the clear path. Asserted by
`internal/auth/auth_test.go:133-191` (`TestSetSessionCookie`, `TestClearSessionCookie`), which
checks `Secure`, `HttpOnly`, `SameSite`, `Path`, empty `Domain`, and `MaxAge`.

**Session token handling** (`auth.go:255-285`). 32 bytes from `crypto/rand`, base64url-encoded;
what is stored is `hex(sha256(token))`, so a DB read never discloses a usable session — matching
`migrations/00002_sessions.sql:3-4`'s comment. Expiry is enforced *in SQL*
(`internal/db/queries/sessions.sql:11`: `AND s.expires_at > now()`), not in Go, so app-process
clock skew cannot extend a session. Covered by `session_test.go:238-262`
(`TestSession_ExpiredIsRejected`) and `internal/http/auth_test.go:202-233`.

**Constant-time comparison.** `crypto/subtle` is correctly *absent* — there is no hand-rolled
secret comparison anywhere in the zone. Password checks go through
`argon2id.ComparePasswordAndHash`, which uses `subtle.ConstantTimeEq`/`ConstantTimeCompare`
internally (verified in the vendored module source). Session lookup is a Postgres primary-key
match on the SHA-256 of the *presented* token; a timing side channel there would leak at most the
stored hash, never the raw token. No change needed.

**Session fixation.** Both `Signup` (`auth.go:185`) and `Login` (`auth.go:239`) mint a fresh
token via `createSession`. No code path anywhere adopts a caller-supplied session id — the only
read of the cookie value is `hashToken(cookie.Value)` for lookup (`middleware.go:44`). Clean.

**CSRF.** `Service.Middleware` (`middleware.go:29-77`) runs the `Origin` check *before* session
population and *before* the handler, and fails closed on a missing `Origin`
(`checkOrigin` returns `false` for `origin == ""`, `middleware.go:102-104`). Wrapping is central
and unconditional (`internal/http/http.go:41`). Covered by `middleware_test.go:36-149` and
`internal/http/auth_test.go:265-307` / `settings_test.go:40-70`. `form-action 'self'` in the CSP
is the second layer.

**Timing-safe login/signup.** Login verifies against a fixed startup dummy hash when no account
matches (`auth.go:219-237`, `New` at `auth.go:93`); signup runs the existence check and the
argon2id hash concurrently and unconditionally (`auth.go:139-165`), and turns the
`ON CONFLICT (lower(email)) DO NOTHING` → `pgx.ErrNoRows` race into `ErrEmailTaken`
(`auth.go:172-177`). Covered by `session_test.go:139-156`, `215-236`.

**Rate limiting.** `ratelimit.go` is a mutex-guarded fixed-window counter with an injectable
clock; budgets are consumed *before* any credential check
(`auth.go:203-217`, `135-137`, `367-369`), the per-email key is lowercased so case rotation
cannot mint a fresh budget (`auth.go:215`; regression test
`internal/http/auth_test.go:344-365`), and `Run` (`cleanup.go`) sweeps buckets and expired
sessions hourly. `internal/http/auth.go:116-122`'s `clientIP` reads `r.RemoteAddr` only and never
trusts `X-Forwarded-For` — correct as written (see Open question 5).

**CSP** (`internal/http/security.go`). Read directive by directive against the comment block and
`security_test.go:33-108`. `default-src 'none'`, `script-src` with no `'unsafe-inline'`,
`form-action 'self'`, `frame-ancestors 'none'`, `base-uri 'none'`, `img-src` with no remote
origin. The two concessions (`'unsafe-eval'`, `https://cdn.jsdelivr.net`) carry recorded expiry
conditions. `securityHeaders` wraps **outside** `a.Middleware` (`internal/http/http.go:41`), so
the header is present on CSRF 403s — pinned by `security_test.go:129`. **No edits, and no
collapsing of CSP against `internal/render`'s sanitisation:** the layering is deliberate
defense-in-depth per the issue's review focus.

**`internal/auth/notetypes.go`, `cleanup.go`, `doc.go`, `ratelimit.go`:** read in full, no
duplication or dead code found. No edits proposed.

**Not a finding, recorded elsewhere:** `POST /signup` returns 409 "That email is already
registered" (`internal/http/auth.go:36`), which is an explicit account-enumeration oracle. It is
the documented intent — architecture.md §12 says the handler's job is "to turn that constraint
violation into a clean 409". Left alone; see Open question 7.

---

## 2. Behavior-changing security fix — sessions survive a password change

**This section changes behavior on purpose. It is not cleanup. Land it as its own commit.**

### 2.1 The defect

`Service.ChangePassword` (`internal/auth/auth.go:362-391`) updates `users.password_hash` and
returns. It never touches the `sessions` table. `internal/http/settings.go:98-122` likewise just
re-renders the page.

Consequence: an attacker holding a stolen session cookie keeps access **after** the victim
changes their password. Because `Middleware` slides the expiry on every request
(`middleware.go:57-73`, 30-day lifetime renewed under 15 days remaining), an actively-used stolen
session never expires on its own. Changing the password is the one remedy a user has, and it does
nothing. This is the standard "invalidate sessions on credential change" control (OWASP Session
Management).

Two pieces of evidence that this was intended and simply never built:

- `migrations/00002_sessions.sql:11-12` creates `sessions_user_id_idx` with the comment
  *"Invalidate all of a user's sessions; also backs the CASCADE on user delete (#51)."* The index
  exists; the query does not. **No migration is needed.**
- `docs/plans/52-auth-accounts.md:756-758` records the deferral verbatim: *"No session
  invalidation on password change … out of scope; a `POST /settings/sessions/revoke-all` route
  would be a follow-up issue, not part of #52."* That follow-up was never filed. See Open
  questions 1 and 2 before applying.

### 2.2 Edit — new query

`internal/db/queries/sessions.sql`, append after line 20:

```sql

-- name: DeleteSessionsForUser :execrows
DELETE FROM sessions WHERE user_id = $1;
```

Then regenerate: `go generate ./internal/db/...` (`//go:generate sqlc generate -f ../../sqlc.yaml`
lives at `internal/db/pool.go:4`). Commit the regenerated `internal/db/sessions.sql.go` and
`internal/db/querier.go`; do not hand-edit them (CLAUDE.md §16). Expected generated signature:
`func (q *Queries) DeleteSessionsForUser(ctx context.Context, userID pgtype.UUID) (int64, error)`.

### 2.3 Edit — make `createSession` transaction-capable

`ChangePassword` must write the password update, the session purge, and the replacement session in
one transaction, or a mid-way failure leaves the password changed with every session still live.
`createSession` currently hardcodes `s.q`.

`internal/auth/auth.go:255-272` — replace:

```go
func (s *Service) createSession(ctx context.Context, userID pgtype.UUID) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	err = s.q.CreateSession(ctx, db.CreateSessionParams{
```

with:

```go
func createSession(ctx context.Context, q *db.Queries, userID pgtype.UUID) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	err = q.CreateSession(ctx, db.CreateSessionParams{
```

(The rest of the function body is unchanged. It uses nothing else from `s`, so dropping the
receiver is safe.)

Update the two existing call sites:

- `internal/auth/auth.go:185` — Old: `token, err := s.createSession(ctx, user.ID)`
  New: `token, err := createSession(ctx, s.q, user.ID)`
- `internal/auth/auth.go:239` — Old: `token, err := s.createSession(ctx, user.ID)`
  New: `token, err := createSession(ctx, s.q, user.ID)`

No import changes: `db`, `pgtype`, `time`, `fmt` all stay in use.

### 2.4 Edit — `ChangePassword` returns a fresh token

`internal/auth/auth.go:362` — Old:

```go
func (s *Service) ChangePassword(ctx context.Context, userID pgtype.UUID, currentHash, currentPassword, newPassword string) error {
```

New:

```go
func (s *Service) ChangePassword(ctx context.Context, userID pgtype.UUID, currentHash, currentPassword, newPassword string) (string, error) {
```

Every early return in the body gains an empty-string first value. Lines 363-382, Old:

```go
	if msg, ok := validatePassword(newPassword); !ok {
		return &ValidationError{Msg: msg}
	}

	if ok, retryAfter := s.changePassword.Allow(userID.String()); !ok {
		return &RateLimitError{RetryAfter: retryAfter}
	}

	match, err := argon2id.ComparePasswordAndHash(currentPassword, currentHash)
	if err != nil {
		return fmt.Errorf("compare password: %w", err)
	}
	if !match {
		return ErrInvalidCredentials
	}

	newHash, err := argon2id.CreateHash(newPassword, argon2id.DefaultParams)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
```

New:

```go
	if msg, ok := validatePassword(newPassword); !ok {
		return "", &ValidationError{Msg: msg}
	}

	if ok, retryAfter := s.changePassword.Allow(userID.String()); !ok {
		return "", &RateLimitError{RetryAfter: retryAfter}
	}

	match, err := argon2id.ComparePasswordAndHash(currentPassword, currentHash)
	if err != nil {
		return "", fmt.Errorf("compare password: %w", err)
	}
	if !match {
		return "", ErrInvalidCredentials
	}

	newHash, err := argon2id.CreateHash(newPassword, argon2id.DefaultParams)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
```

Lines 384-391, Old:

```go
	if err := s.q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: newHash,
	}); err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	return nil
}
```

New:

```go
	// The password update, the session purge, and the replacement session are one transaction:
	// a failure between them would leave the new password in place with every old session --
	// including a stolen one -- still live, which is the whole point of the purge.
	tx, err := s.beginner.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if err := qtx.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: newHash,
	}); err != nil {
		return "", fmt.Errorf("update user password: %w", err)
	}
	// Every session for this account dies with the old password, the acting browser's included;
	// it gets a fresh one immediately below, so the tab that made the change stays signed in.
	// sessions_user_id_idx (migration 00002) exists for exactly this query.
	if _, err := qtx.DeleteSessionsForUser(ctx, userID); err != nil {
		return "", fmt.Errorf("delete sessions for user: %w", err)
	}
	token, err := createSession(ctx, qtx, userID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit password change: %w", err)
	}
	return token, nil
}
```

Also update the doc comment at `auth.go:356-361` — append one sentence after the existing text:
`On success it invalidates every session for the account and returns the raw token of a
replacement session for the caller's own browser.`

Compile check: `s.beginner` is `db.Beginner` (`internal/db/tx.go`), already a `Service` field
(`auth.go:74`) and already used this way in `notetypes.go:17`. `(*db.Queries).WithTx(pgx.Tx)`
exists (`internal/db/db.go:28-32`). No new imports in `auth.go`; none orphaned.

### 2.5 Edit — handler reissues the cookie

`internal/http/settings.go:98` — Old:

```go
		if err := a.ChangePassword(r.Context(), user.ID, user.PasswordHash, currentPassword, newPassword); err != nil {
```

New:

```go
		token, err := a.ChangePassword(r.Context(), user.ID, user.PasswordHash, currentPassword, newPassword)
		if err != nil {
```

`internal/http/settings.go:119-122` — Old:

```go
		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User":            user,
			"PasswordSuccess": "Password changed",
		})
```

New:

```go
		// Must precede render: render calls w.WriteHeader, after which headers are frozen.
		auth.SetSessionCookie(w, token)
		render(w, pages["settings"], http.StatusOK, map[string]any{
			"User":            user,
			"PasswordSuccess": "Password changed",
		})
```

`auth` is already imported (`settings.go:12`). No orphaned imports.

Note for the implementer: the inner `err` at line 99 (`status, msg, retryAfter, ok :=
classifyFormError(err, ...)`) still refers to the same variable — the `:=` on the new line 98
declares `token` and `err` in the handler scope, and `err` is not otherwise declared in that
closure, so this compiles without shadowing surprises. Verify with `go vet`.

### 2.6 Test changes required

Existing tests that **must** be updated (they break to compile otherwise):

- `internal/auth/session_test.go:350` — `if err := s.ChangePassword(...); err != nil {`
  → `if _, err := s.ChangePassword(...); err != nil {`
- `internal/auth/session_test.go:384` — `err = s.ChangePassword(...)`
  → `_, err = s.ChangePassword(...)`
- `internal/auth/session_test.go:408` — `err = s.ChangePassword(...)`
  → `_, err = s.ChangePassword(...)`
- `internal/auth/session_test.go:435` — `last = s.ChangePassword(...)`
  → `_, last = s.ChangePassword(...)`

New test, `internal/auth/session_test.go` (DB-backed, follows the existing `beginTx` /
`newTestService` pattern):

- `TestChangePassword_InvalidatesOtherSessions` — `Signup` (session A), then `Login` (session B),
  then `ChangePassword`. Assert: `countRows(t, tx, "SELECT count(*) FROM sessions WHERE user_id = $1", user.ID)`
  is `1`; `db.New(tx).GetSessionUser(ctx, hashToken(newToken))` succeeds; `GetSessionUser` on
  `hashToken(tokenA)` and `hashToken(tokenB)` both return `pgx.ErrNoRows`.

New assertions in `internal/http/settings_test.go:88-107` (`TestSettingsPasswordGoldenPath`):
the 200 response carries exactly one `auth.CookieName` cookie whose `Value` differs from the
request cookie's, and a follow-up `GET /settings` sent with the **old** cookie returns 303
to `/login`.

Access-control table (CLAUDE.md §10.5) needs no new row — no new route.

### 2.7 Changelog

CLAUDE.md §14 requires one `CHANGELOG.md` entry per PR. Under the branch's version heading:

```
### Security
- Changing a password now invalidates every other session for that account and reissues the acting browser's session cookie ([#123](https://github.com/Jolls/enshu/issues/123))
```

Do not edit `docs/plans/52-auth-accounts.md` — historical plans are records of what was decided
then, not living documents.

---

## 3. Cleanup — no behavior change

### 3.1 `Service.cfg` is written and never read

`New` stores `cfg` on the struct (`auth.go:110`), but no code — production or test — ever reads
`s.cfg`. `checkOrigin` reads `s.origins` only (`middleware.go:111-119`), which `New` derives from
`cfg.Origin` at construction (`auth.go:97-106`). Verified by grepping `s\.cfg` across all `*.go`:
zero hits. The `Config` *type* stays (it is `New`'s parameter).

`internal/auth/auth.go:72-77` — Old:

```go
type Service struct {
	q         *db.Queries
	beginner  db.Beginner
	cfg       Config
	dummyHash string
```

New:

```go
type Service struct {
	q         *db.Queries
	beginner  db.Beginner
	dummyHash string
```

(`dummyHash` remains the longest name, so gofmt alignment of the other fields is unchanged.)

`internal/auth/auth.go:107-112` — delete the single line `		cfg:            cfg,`. The literal's
key alignment is driven by `changePassword:` and is unaffected.

`internal/auth/middleware_test.go:62` — Old: `			s := &Service{cfg: Config{Origin: tt.configOrigin}}`
New: `			s := &Service{}`
(the `if tt.configOrigin != ""` block on the following lines still builds `s.origins` and is
unchanged.)

`internal/auth/middleware_test.go:86`, `:106`, `:130` — each Old: `	s := &Service{cfg: Config{}}`
New: `	s := &Service{}`

No orphaned imports: `middleware_test.go` still uses `net/url` and `strings` in `TestCheckOrigin`;
`auth.go` still uses everything it imports. Covered by `middleware_test.go`'s existing
`TestCheckOrigin` / `TestMiddleware_*` and by `auth_test.go:10-14`'s `TestNew_InvalidOrigin`.

### 3.2 `SetSessionCookie` / `ClearSessionCookie` duplicate the flag set

Two `http.Cookie` literals differing only in `Value` and `MaxAge`. The risk is not the three
duplicated lines — it is that the *set* path and the *clear* path can drift on a security flag
independently, and the `__Host-` prefix requires all three of `Secure`/`Path=/`/no-`Domain` on
both.

`internal/auth/middleware.go:122-146` — Old:

```go
// SetSessionCookie writes the __Host- session cookie with token as its value.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionLifetime.Seconds()),
	})
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
```

New:

```go
// sessionCookie is the single definition of the session cookie's attributes. Secure, Path=/ and
// the absence of Domain are the three things the __Host- prefix requires the browser to enforce
// (architecture.md §12) -- having one constructor is what stops the set and clear paths from
// drifting apart on any of them.
func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

// SetSessionCookie writes the __Host- session cookie with token as its value.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, sessionCookie(token, int(SessionLifetime.Seconds())))
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, sessionCookie("", -1))
}
```

Emitted headers are byte-identical. Fully covered by the existing
`internal/auth/auth_test.go:133-191`; no new test needed.

### 3.3 `formatRetryAfter` lives away from its only caller

`formatRetryAfter` is defined at `internal/http/auth.go:124-130` and called exactly once, from
`internal/http/errors.go:27`. Moving it next to its caller is what lets `auth.go` drop two
imports.

Delete `internal/http/auth.go:124-130` in full:

```go
func formatRetryAfter(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}
```

**Orphaned by this change** — `internal/http/auth.go:3-12`, Old:

```go
import (
	"errors"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Jolls/enshu/internal/auth"
)
```

New:

```go
import (
	"errors"
	"html/template"
	"net"
	"net/http"

	"github.com/Jolls/enshu/internal/auth"
)
```

(Verified: after the deletion, `strconv` and `time` have no other use in `auth.go`. `errors`,
`html/template`, `net` and `net/http` all remain used — `errors.Is` at :35 and :74,
`map[string]*template.Template` at :14, `net.SplitHostPort` at :117, `http.*` throughout.)

`internal/http/errors.go:3-8` — Old:

```go
import (
	"errors"
	"net/http"

	"github.com/Jolls/enshu/internal/auth"
)
```

New:

```go
import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Jolls/enshu/internal/auth"
)
```

Then append the deleted function verbatim to the end of `internal/http/errors.go`.

Behavior identical. Indirectly covered by `internal/http/auth_test.go:309-328`
(`TestLoginRateLimited` asserts a non-empty `Retry-After`); no new test needed.

---

## 4. Considered and rejected

- **`db.GetSession`** (`internal/db/queries/sessions.sql:1-2`, generated at
  `internal/db/sessions.sql.go:50-54`, declared in `querier.go:124`) has zero callers — the
  middleware uses `GetSessionUser` instead. Pre-existing dead code, not orphaned by anything in
  this plan, so CLAUDE.md §3 says mention it and leave it. Deleting it also means a sqlc
  regeneration in an audit PR. See Open question 6.
- **`cleanup.go`'s four `Sweep()` calls** could become a loop over a `[]*limiter`. Four lines
  become three plus a helper; the four named fields are addressed individually at their real call
  sites, so a slice adds a second way to reach the same objects. Not worth it.
- **Merging `POST /signup` and `POST /login`** (`internal/http/auth.go:23-53` and `:63-92`). Same
  skeleton, but the domain-error closure, the render page, and the render payload
  (`Email`+`DisplayName`+`Error` vs `Email`+`Error`) all differ; a merge needs a params struct for
  two call sites. See Open question 8.
- **The `if !ok { 500 }` + `if retryAfter != "" { Set }` pair** repeats at
  `internal/http/auth.go:40-46`, `:79-85` and `internal/http/settings.go:105-111`. Folding it into
  `classifyFormError` means passing `w` into a pure classifier; and the fourth call site
  (`settings.go:62`) passes a nil domain func and discards `retryAfter`, so it would not share the
  helper anyway.
- **`Signup`/`Login`'s shared validation prologue** (`auth.go:123-133` vs `:195-201`). Signup also
  validates the display name and uses different limiters in a different order. Extracting would
  need a flags/closure parameter for two callers.
- **`limiter.Allow` increments `b.count` even once over budget** (`ratelimit.go:45`). Harmless —
  `resetAt` is fixed at bucket creation, so an over-limit key still unblocks at the window edge
  (proved by `ratelimit_test.go:29-45`); overflowing an `int` needs 2^63 requests inside one
  window.
- **`ErrRateLimited`** (`auth.go:46`) is reachable only through `RateLimitError.Unwrap`
  (`auth.go:59`) — handlers use `errors.As(&rle)`. It is the public `errors.Is` target for the
  package's error API, so it is not dead. Keep.
- **`checkOrigin`'s dev fallback compares host only, ignoring scheme** (`middleware.go:119`), so
  with `ORIGIN` unset `http://x` matches a request to `https://x`. This is the documented dev
  fallback (`auth.go:66`) and the pinned behavior in `middleware_test.go:45`; production sets
  `ORIGIN` (`cmd/enshu/main.go:50`), where scheme *is* compared (`middleware.go:113`). No change.
- **`Login` does not delete the browser's previous session row**, so repeat logins accumulate
  rows. Not a security issue (each token is independently unguessable) and `Run`'s hourly
  `DeleteExpiredSessions` reaps them. Note that §2.4's purge makes this moot for the
  password-change path specifically.
- **Rotating the session token on sliding renewal** (`middleware.go:57-73`). Renewal currently
  extends `expires_at` and re-sets the same cookie value. Periodic rotation is a defence against a
  token that leaked *before* the rotation, but with a 256-bit `crypto/rand` token that is
  `HttpOnly` + `Secure` + `__Host-`, and with §2's purge closing the credential-change case, the
  added write-per-renewal is not justified. No change.
- **`X-Frame-Options`** is deliberately not set because `frame-ancestors 'none'` supersedes it —
  documented at `internal/http/security.go:59-60`. Correct; no change.

---

## 5. Verification steps (for the implementing session)

1. `go generate ./internal/db/...` after §2.2, then confirm `git status` shows only
   `internal/db/sessions.sql.go` and `internal/db/querier.go` changed under `internal/db/`.
   No migration is added — `sessions_user_id_idx` already exists (`migrations/00002_sessions.sql:12`).
2. `go build ./...`, `go vet ./...`, `golangci-lint run`.
3. `go test ./...` **with `DATABASE_URL` set** — the whole of `internal/auth`'s session suite and
   all of `internal/http`'s route tests skip silently without it, which would hide every §2
   regression. If the dev DB has leftover rows, run `bash .claude/skills/run-app/reset-db.sh`
   first (CLAUDE.md §16).
4. Targeted: `go test ./internal/auth/ -run 'ChangePassword|Session|Cookie|CheckOrigin|Middleware'`
   and `go test ./internal/http/ -run 'Settings|Logout|Routes|SecurityHeaders|ContentSecurityPolicy'`.
5. `internal/http/security_test.go:59-61` fails if the CSP gains a directive without a matching
   table row — this plan proposes no CSP change, so it must stay green untouched.
6. `CHANGELOG.md` updated per §2.7 with the version bump and tag per CLAUDE.md §14.

---

## 6. Open questions

1. **§2 variant.** As written, the acting browser stays signed in (all sessions purged, a
   replacement minted and cookie reissued). Alternative: purge all sessions including the acting
   one, `ClearSessionCookie`, and 303 to `/login` — simpler (`ChangePassword` keeps its
   single-value signature, no transaction needed) and more conservative, at the cost of signing
   the user out of the tab they just used. Which?
2. **Scope of §2.** `docs/plans/52-auth-accounts.md:756-758` deferred this deliberately to a
   follow-up (`POST /settings/sessions/revoke-all`) that was never filed. Land it inside this
   audit PR, or file it as its own `sev: high` / `area: security` issue and keep #123 to §3's
   cleanup only?
3. **Non-CSP security headers.** `securityHeaders` sets only `Content-Security-Policy`.
   `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin` are one line each.
   `docs/plans/57-csp-reviewer.md:96,376` explicitly deferred them: *"a general security-headers
   sweep is a different issue."* Is #123 that issue, or does the deferral stand?
4. **`/media/{sha256}` serves imported blobs as active content.** `detectMediaMime`
   (`internal/apkg/dbwrite.go:121-126`) derives the MIME from the *filename extension*, so an
   imported `.apkg` carrying `x.svg` or `x.html` is served from `/media/{sha}` as
   `image/svg+xml` / `text/html` — same-origin, and under a shared deck it is another user's
   file. Today only the CSP's `script-src` (no `'unsafe-inline'`) stops script in it from running,
   which means CSP is load-bearing rather than defence-in-depth here.
   `internal/http/media.go` is outside this issue's stated file scope. File a separate issue
   (candidate fixes: `Content-Disposition: attachment`, an allowlist of served MIME types, or a
   per-response `sandbox` directive), or widen #123 to cover it?
5. **Rate-limit keys behind NAT.** `clientIP` uses `r.RemoteAddr` only (correct — trusting
   `X-Forwarded-For` unconditionally would be an outright bypass), but that means a classroom
   behind one public IP shares one bucket: `signupIP` is 5/hour (`auth.go:36-37`) and `loginIP` is
   20/15min (`auth.go:32-33`), and login consumes budget on success too. Leave as is, raise the
   limits, or add an opt-in "trusted proxy" config that permits `X-Forwarded-For`?
6. **`db.GetSession`** has no callers. Leave it (CLAUDE.md §3: pre-existing dead code, mention
   don't delete), or delete the stanza from `internal/db/queries/sessions.sql` and regenerate?
7. **Signup's 409 enumeration oracle.** `POST /signup` answers "That email is already registered"
   (`internal/http/auth.go:36`), which discloses account existence and undercuts the timing-safe
   signup path just above it. architecture.md §12 records the clean 409 as the intent. Confirm it
   stays, or collapse it to a generic message (which would need a different UX for the legitimate
   duplicate-email case)?
8. **`POST /signup` / `POST /login` handler merge** (`internal/http/auth.go:23-53`, `:63-92`) —
   rejected in §4 as needing a params struct for two call sites. Worth doing anyway, or leave?

---

## Resolved decisions

Answers to the Open questions above, decided with the user before implementation. These are the
spec; where they conflict with the sections above, these win.

### R1. Session purge — land it here, acting browser stays signed in

Resolves Open questions 1 and 2.

Apply §2 exactly as written: purge every session for the account, mint a replacement, reissue the
cookie, all in one transaction, so the tab that changed the password stays logged in. Land it
inside this PR rather than filing the never-created follow-up — the hole is real and open now.

Commit it separately from §3's cleanup, and label it `### Security` in the changelog per §2.7.

### R2. Add both non-CSP security headers

Resolves Open question 3. #123 *is* the security-headers sweep #57 deferred.

In `internal/http/security.go`'s `securityHeaders`, alongside the existing
`Content-Security-Policy` set, add:

```go
	// Stop a browser from second-guessing our Content-Type. Load-bearing for /media/{sha256},
	// which serves imported .apkg blobs with a MIME derived from the filename extension.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Don't leak deck or note ids in the Referer on outbound navigation.
	w.Header().Set("Referrer-Policy", "same-origin")
```

Add matching assertions to `internal/http/security_test.go` next to the existing CSP checks.
Note `security_test.go:59-61` fails if the *CSP* gains an undocumented directive — these are
separate headers, so that guard is unaffected.

### R3. `/media/{sha256}` active-content exposure — file a separate issue

Resolves Open question 4. Do **not** widen this PR.

File a new issue, `sev: high` + `area: security` + `area: apkg`, recording: `detectMediaMime`
(`internal/apkg/dbwrite.go:121-126`) derives MIME from the filename extension, so an imported
`.apkg` carrying `x.svg` or `x.html` is served from `/media/{sha}` same-origin as active content —
another user's file under a shared deck. CSP's `script-src` is currently the only thing stopping
script in it, which makes CSP load-bearing rather than defence-in-depth. Candidate fixes:
`Content-Disposition: attachment`, an allowlist of served MIME types, or a per-response `sandbox`
directive. Note that R2's `nosniff` narrows but does not close this — it stops MIME *sniffing*,
not a correctly-declared `image/svg+xml` that contains script.

Filed as [#133](https://github.com/Jolls/enshu/issues/133). Reference it in this PR body so the finding is traceable to the audit that
found it.

### R4. Rate-limit keys behind NAT — leave as is, record the finding

Resolves Open question 5. No edit.

`clientIP` reading `r.RemoteAddr` only is correct; trusting `X-Forwarded-For` unconditionally
would be an outright bypass. The shared-bucket consequence for a classroom behind one public IP
is real but is a tuning/product decision, not an audit finding, and the per-email limiters still
cover the actual credential-stuffing case. Revisit against Milestone 3's classroom cohorts with
real deployment numbers rather than guessing at limits now.

### R5. `db.GetSession` — keep

Resolves Open question 6, via #122's R1. #122 deletes only the five *unscoped* getters
(`GetDeck`, `GetDeckAccess`, `GetField`, `GetNote`, `GetTemplate`); `GetSession` is not one of
them, so CLAUDE.md rule 3 governs and it stays. No edit in either group.

### R6. Signup's 409 — stays

Resolves Open question 7. No edit. architecture.md §12 records turning the constraint violation
into a clean 409 as the deliberate intent; a generic message would need a different UX for the
legitimate duplicate-email case, which is a product change, not an audit finding.

### R7. `POST /signup` / `POST /login` merge — leave

Resolves Open question 8. No edit, for the reason already given in §4: the domain-error closure,
render page, and render payload all differ, so a merge needs a params struct for two call sites.

### R8. Sequencing against #122

#122 is applied first and sets `emit_interface: false`, deleting `internal/db/querier.go`. §2.2's
instruction to commit a regenerated `querier.go` is therefore void — regenerate and commit
`internal/db/sessions.sql.go` only.

#122 also sets `emit_json_tags: false` as the **last** edit of the whole batch, in its own commit,
so run that after everything in this group is in place.
