# 225 — Operator-issued password reset (CLI + one-time link)

Plan for [#225](https://github.com/Jolls/deckshare/issues/225). **Not implemented — this is the
design.** Every value marked *(OQ-n)* is deferred to the Open questions section at the bottom and
must be answered before implementation starts; nothing below silently picks one.

## 0. Scope, as resolved on the issue

- An **operator CLI**, run on the host/container with `DATABASE_URL` in the environment, is the
  only way a reset is initiated. There is no end-user-reachable "forgot password" trigger.
- The CLI looks the user up by email, mints a single-use, time-limited token, stores its **hash**,
  and prints a **link** the operator relays out of band.
- The student opens the link once and sets a new password. Completing the reset purges every
  session for the account and issues a fresh one, in one transaction — the exact shape
  `auth.Service.ChangePassword` already uses (`internal/auth/auth.go:380-432`).
- No SMTP, no email verification, no admin role, no new authenticated admin HTTP surface.

### What this does not change

- No new `deck_access` flag, no new permission, no cross-user read path (CLAUDE.md §9).
- Invariants §2.1–§2.10 are untouched: this adds one auth-artifact table alongside `sessions`, in
  the same shape (hash at rest, `ON DELETE CASCADE` from `users`).

---

## 1. Migration

**File:** `migrations/00022_password_reset_tokens.sql` (00021 is the current head; `goose -dir
migrations create -s password_reset_tokens sql` produces the number — CLAUDE.md §9,
`migrations/README.md`).

New table only — no `ALTER TABLE`, so the populated-database `NOT NULL` trap in docs/schema.md's
migration checklist item 1 does not apply.

```sql
-- +goose Up
-- Operator-issued password reset (#225). Deliberately mirrors `sessions` (migration 00002): the
-- primary key is the SHA-256 hex of the token, so the raw token exists only in the link the
-- operator relays -- a database read discloses nothing usable, and a stolen backup cannot be
-- replayed into an account takeover.
--
-- Not a self-service flow: rows here can only be created by the operator CLI (cmd/... , #225),
-- never by an HTTP request. There is no route that mints one.
CREATE TABLE password_reset_tokens (
    id         text        PRIMARY KEY,  -- SHA-256 hex of the reset token
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,  -- an auth
                                         -- artifact, not content; nothing survives its user,
                                         -- same reasoning as sessions.user_id (#51)
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Purge every outstanding token for one account (issuing a new link, and the hourly sweep's
-- per-user case); also backs the CASCADE on user delete.
CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

-- +goose Down
DROP TABLE password_reset_tokens;
```

Single-use is enforced by **consuming the row with a `DELETE ... RETURNING`** (§3), not by a
`used_at` column — *(OQ-5)* covers the alternative.

### Docs that ship with the migration

- **`docs/schema.md`** — add to the listing block that currently ends at `sessions` (around line
  381, "Per-user state" section) :

  ```
  password_reset_tokens
                   id text pk,           -- SHA-256 hex of the reset token; the raw token
                                          -- exists only in the operator-relayed link
                   user_id, expires_at, created_at
                   -- INDEX (user_id)    -- purge a user's outstanding tokens
                   -- operator-minted only (#225); no HTTP route creates a row
  ```

  Also add a row to the **Deletion policy** table (around line 290, where `card_flags`' FK rows
  live): `password_reset_tokens.user_id → users` / CASCADE / "an auth artifact, not content —
  same reasoning as `sessions.user_id`."
- **`docs/schema-diagram.md`** — hand-maintained ("Regenerate this by hand if the shape in
  `schema.md` changes"), but the precedent is mixed: `card_flags` (migration 00019) was never
  added to it. *(OQ-9)*
- **`docs/routes.md`** — two new rows in the **Auth** table (§5 below).
- **`docs/architecture.md` §1** — one paragraph recording that operator password reset landed,
  in the same style as the existing "has landed" paragraphs.
- **`CHANGELOG.md`** — one `### Added` line under a new `## [0.1.X]` entry (CLAUDE.md §14).

---

## 2. Queries + generated code

**New file:** `internal/db/queries/password_reset_tokens.sql`

```sql
-- name: CreatePasswordResetToken :exec
INSERT INTO password_reset_tokens (id, user_id, expires_at) VALUES ($1, $2, $3);

-- Peek without consuming: backs GET /reset-password, which renders the form but must not spend
-- the token (a prefetching browser or a link preview would otherwise burn it).
-- name: GetPasswordResetToken :one
SELECT user_id FROM password_reset_tokens WHERE id = $1 AND expires_at > now();

-- Single-use, atomically: the DELETE is the authorisation. Two concurrent submissions of the same
-- link race on one row and exactly one gets RETURNING output; the other sees pgx.ErrNoRows.
-- name: ConsumePasswordResetToken :one
DELETE FROM password_reset_tokens WHERE id = $1 AND expires_at > now() RETURNING user_id;

-- name: DeletePasswordResetTokensForUser :execrows
DELETE FROM password_reset_tokens WHERE user_id = $1;

-- name: DeleteExpiredPasswordResetTokens :execrows
DELETE FROM password_reset_tokens WHERE expires_at < now();
```

Run `go generate ./...` and commit `internal/db/password_reset_tokens.sql.go` unedited (CLAUDE.md
§16). Do not hand-write it.

---

## 3. `internal/auth` — the service layer

**New file:** `internal/auth/reset.go`. Reuses the package's existing `newToken()`,
`hashToken()`, `validatePassword()`, `createSession()` (all in `auth.go`) — nothing is
reimplemented.

```go
// PasswordResetLifetime is how long an operator-issued reset link stays usable. (OQ-3)
const PasswordResetLifetime = ? * time.Hour

var ErrInvalidResetToken = errors.New("auth: invalid or expired reset token")
```

### 3.1 `CreatePasswordReset` — called by the CLI only

```go
// CreatePasswordReset mints a one-time reset token for the account at email and returns the user
// plus the RAW token; only its SHA-256 hash is stored. Operator-only: no HTTP route calls this,
// so there is no rate limiter and no timing-safe dummy path -- an unknown email is an honest
// error to the operator's terminal, not an oracle exposed to the network.
func (s *Service) CreatePasswordReset(ctx context.Context, email string) (db.User, string, error)
```

Sequence:
1. `strings.TrimSpace(email)`, `validateEmail` → `*ValidationError` on failure.
2. `s.q.GetUserByEmail` (already case-insensitive on `lower(email)`) → `pgx.ErrNoRows` is wrapped
   as a distinct `ErrNoSuchUser` so the CLI can print "no account with that email" rather than a
   stack-shaped error.
3. Inside one transaction (`s.beginner.Begin`, same `defer tx.Rollback` shape as
   `ChangePassword`): `DeletePasswordResetTokensForUser` *(OQ-6)*, then
   `CreatePasswordResetToken(hashToken(raw), user.ID, now+PasswordResetLifetime)`.
4. Return `(user, raw, nil)`.

Does **not** touch `users.password_hash` and does **not** purge sessions — issuing a link changes
nothing about the account until the link is used. *(OQ-7)*

### 3.2 `PasswordResetValid` — backs the GET page

```go
// PasswordResetValid reports whether rawToken names a live reset token, without consuming it.
func (s *Service) PasswordResetValid(ctx context.Context, rawToken string) (bool, error)
```

`GetPasswordResetToken(hashToken(rawToken))`; `pgx.ErrNoRows` → `(false, nil)`; any other error is
returned (the handler 500s rather than rendering "invalid link" on a database fault).

### 3.3 `ResetPassword` — the transaction, mirroring `ChangePassword`

```go
// ResetPassword consumes a one-time operator-issued token and sets the account's password. On
// success it invalidates every session for the account and returns the raw token of a
// replacement session for the browser that completed the reset -- the same atomicity contract as
// ChangePassword (auth.go): a failure between the password write and the session purge would
// leave the new password live alongside every old session, which is the whole point of the purge.
func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) (string, error)
```

Sequence, in order:
1. `validatePassword(newPassword)` → `*ValidationError`. **Before** any token lookup, so a
   too-short password does not burn the link.
2. Rate limit *(OQ-8)*.
3. `argon2id.CreateHash(newPassword, argon2id.DefaultParams)` — outside the transaction, exactly
   as `ChangePassword` does, so the hash cost is not held inside an open transaction.
4. `tx, err := s.beginner.Begin(ctx)`; `defer func() { _ = tx.Rollback(ctx) }()`;
   `qtx := s.q.WithTx(tx)`.
5. `userID, err := qtx.ConsumePasswordResetToken(ctx, hashToken(rawToken))` —
   `pgx.ErrNoRows` → `ErrInvalidResetToken`. This is both the authorisation and the single-use
   enforcement, and it is the first statement in the transaction so a later failure rolls the
   consumption back (the link stays usable if the password write fails).
6. `qtx.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: userID, PasswordHash: newHash})`.
7. `qtx.DeletePasswordResetTokensForUser(ctx, userID)` — any *other* outstanding link for this
   account dies with the password it was issued against. *(OQ-6)*
8. `qtx.DeleteSessionsForUser(ctx, userID)` — backed by `sessions_user_id_idx` (migration 00002),
   same comment as `ChangePassword`.
9. `token, err := createSession(ctx, qtx, userID)`.
10. `tx.Commit(ctx)`; return `token`.

No `ComparePasswordAndHash` step: possession of the token *is* the credential. There is no
"current password" to check, which is the one structural difference from `ChangePassword`.

### 3.4 Sweep

`internal/auth/cleanup.go`'s `Run` already deletes expired sessions hourly. Add, in the same
`case <-ticker.C` block:

```go
if _, err := s.q.DeleteExpiredPasswordResetTokens(ctx); err != nil {
    slog.Error("password reset token cleanup failed", "error", err)
}
```

Expiry is enforced by the `expires_at > now()` predicate in both queries regardless; the sweep is
hygiene, not correctness — the same relationship `DeleteExpiredSessions` has to `GetSessionUser`.

---

## 4. The CLI

Shape is *(OQ-1)*; both options below are fully specified so the choice is mechanical.

### Option A — new binary `cmd/reset-password/main.go` (matches `cmd/seed` exactly)

```
go run ./cmd/reset-password <email>
```

`main()` → `log.Fatal(run())`; `run()`:
1. `dsn := os.Getenv("DATABASE_URL")`; empty → `errors.New("DATABASE_URL is required")` (verbatim
   `cmd/seed/main.go:122-125` shape).
2. Base URL resolution — *(OQ-2)*.
3. `flag.Parse()`; exactly one positional arg (the email), else a usage error naming the form.
4. `pool, err := db.NewPool(ctx, dsn)`; `defer pool.Close()`.
5. `authSvc, err := auth.New(pool, auth.Config{})` — `Config{}` is what `cmd/seed` passes; the
   `Origin`/`SignupMode` fields are irrelevant to this path (no HTTP, no signup).
6. `user, raw, err := authSvc.CreatePasswordReset(ctx, email)`.
7. Print, on stdout, via `fmt.Printf` (not `slog`/`log` — this is the tool's output, not a log
   line; `cmd/seed` uses `log.Printf` for progress, which is stderr-shaped noise here):

   ```
   Password reset link for Student D <studentd@ds.com>:

     https://deckshare.example/reset-password?token=<raw>

   Valid once, expires <RFC1123 local time> (in 1h0m0s).
   Relay it to the account holder yourself -- DeckShare sends no email.
   ```

   Printing the resolved display name and email back is the operator's confirmation they picked
   the right account before relaying anything.

The package doc comment follows `cmd/seed`'s: what it is for, that it is operator-only, and that
it never emails.

### Option B — subcommand on `cmd/deckshare`

`run()` in `cmd/deckshare/main.go` gains a dispatch at the top:

```go
if len(os.Args) > 1 && os.Args[1] == "reset-password" { return runResetPassword(os.Args[2:]) }
```

with `runResetPassword` in a new `cmd/deckshare/resetpassword.go`, body identical to Option A's
`run()` minus the server wiring. Matches the issue's literal `deckshare reset-password <email>`
and needs no second binary in the `Dockerfile`; costs the server binary an argv branch it has
never had, and diverges from the `cmd/seed` precedent.

---

## 5. HTTP routes

Both routes are **public** (no `auth.RequireUser`) and live in `internal/http/auth.go`'s existing
`registerAuthRoutes`, next to `/login` — same file, same registration function, same
`classifyFormError` error shape. `registerAuthRoutes`' signature is unchanged
(`mux, a, pages, trusted`); `trusted.clientIP(r)` is already available if *(OQ-8)* wants a
per-IP limiter.

### `GET /reset-password`

```go
mux.HandleFunc("GET /reset-password", func(w http.ResponseWriter, r *http.Request) {
    token := r.URL.Query().Get("token")   // (OQ-4)
    ok, err := a.PasswordResetValid(r.Context(), token)
    if err != nil { serverError(w, r, err); return }
    if !ok {
        render(w, pages["reset_password"], <status (OQ-10)>, map[string]any{"Invalid": true})
        return
    }
    render(w, pages["reset_password"], http.StatusOK, map[string]any{"Token": token})
})
```

Note: no redirect-to-`/decks`-when-already-signed-in branch, unlike `GET /login` and
`GET /signup`. *(OQ-11)*

### `POST /reset-password`

```go
mux.HandleFunc("POST /reset-password", func(w http.ResponseWriter, r *http.Request) {
    if err := r.ParseForm(); err != nil { badRequest(w); return }
    token := r.PostForm.Get("token")
    newPassword := r.PostForm.Get("new_password")
    confirm := r.PostForm.Get("confirm_password")
    if newPassword != confirm {
        render(w, pages["reset_password"], http.StatusBadRequest,
            map[string]any{"Token": token, "Error": "Passwords do not match"})
        return
    }
    sessionToken, err := a.ResetPassword(r.Context(), token, newPassword)
    if err != nil {
        status, msg, retryAfter, ok := classifyFormError(err, func(e error) (int, string, bool) {
            if errors.Is(e, auth.ErrInvalidResetToken) {
                return http.StatusBadRequest, "This reset link is invalid, expired, or already used. Ask for a new one.", true
            }
            return 0, "", false
        })
        if !ok { serverError(w, r, err); return }
        if retryAfter != "" { w.Header().Set("Retry-After", retryAfter) }
        render(w, pages["reset_password"], status, map[string]any{"Token": token, "Error": msg})
        return
    }
    auth.SetSessionCookie(w, sessionToken)
    http.Redirect(w, r, "/decks", http.StatusSeeOther)
})
```

The confirm-field mismatch check lives in the handler, matching `POST /settings/password`
(`internal/http/settings.go:165-169`) — it is a form concern, not a service one.

CSRF: `POST /reset-password` is state-changing, so `auth.Service.Middleware`'s `Origin` check
applies unchanged. Browsers send `Origin` on same-origin form POSTs, which is exactly what
`POST /login` already relies on — no carve-out needed, and none should be added.

### `docs/routes.md` — Auth table rows

```
| GET | `/reset-password` | public | Render the set-new-password form for a valid operator-issued token (#225). The token is never minted over HTTP — only by the operator CLI. Peeks, never consumes |
| POST | `/reset-password` | public | Consume the one-time token, set the password, purge every session for the account, start a fresh one (same transaction as `POST /settings/password`) |
```

---

## 6. Template

**New file:** `web/templates/reset_password.html`, modelled directly on `login.html` (14 lines) —
it is a public, session-less page, so it must pass the same minimal data shape: `layout.html`
renders the account header only `{{if .User}}`, and reads `{{.BodyClass}}`, both of which a
`map[string]any` handles by rendering empty. Do **not** use a struct here (the `settingsView`
pattern) — that would require declaring `User`/`BodyClass` for a page that has neither.

Wiring in `internal/http/templates.go`:
- add `"reset_password"` to the page-name slice in `parseTemplates` (line 37-43);
- add `"reset_password": {"templates/messages.html"}` to `pagePartials` — `login` and `signup`
  both pull `messages.html` in for `{{template "errorMsg" .Error}}`.

Content:

```html
{{define "content"}}
<h1>Set a new password</h1>
{{if .Invalid}}
<p>This reset link is invalid, expired, or has already been used. Ask whoever runs this
DeckShare instance for a new one.</p>
<p><a href="/login">Back to log in</a></p>
{{else}}
{{template "errorMsg" .Error}}
<form method="post" action="/reset-password">
    <input type="hidden" name="token" value="{{.Token}}">

    <label for="new_password">New password</label>
    <input type="password" id="new_password" name="new_password" autocomplete="new-password" required>

    <label for="confirm_password">Confirm new password</label>
    <input type="password" id="confirm_password" name="confirm_password" autocomplete="new-password" required>

    <button type="submit">Set password</button>
</form>
{{end}}
{{end}}
```

`html/template` auto-escapes `{{.Token}}` in the attribute context, so a crafted `?token=` value
cannot break out of the hidden input.

---

## 7. Tests

Security-relevant auth code, so CLAUDE.md §5's "suggest a test" is not optional here — this ships
with tests. All DB-backed tests follow the existing package harness: `beginTx(t)` +
`newTestService(t, tx)` in `internal/auth`, `beginTx(t)` + `newTestHandler(t, tx, cfg)` in
`internal/http`, every assertion scoped to rows the test created (CLAUDE.md §16 — no table-wide
`count(*)`, no unscoped `LIMIT 1`). Confirm they actually ran: `DATABASE_URL` set, and
`go test ./internal/auth/... ./internal/http/... -v | Select-String -Pattern skip` clean.

### `internal/auth/reset_test.go` (new)

| Test | Asserts |
|---|---|
| `TestCreatePasswordReset_StoresOnlyTheHash` | The returned raw token is absent from `password_reset_tokens`; the stored `id` equals its SHA-256 hex. This is the property that makes a DB read useless to an attacker. |
| `TestCreatePasswordReset_UnknownEmail` | `ErrNoSuchUser`, and zero rows written for anyone. |
| `TestCreatePasswordReset_InvalidEmail` | `*ValidationError`, no DB round trip needed. |
| `TestResetPassword_HappyPath` | New password verifies via `argon2id.ComparePasswordAndHash` against the reloaded `users.password_hash`; returned session token resolves through `GetSessionUser` to the same user. |
| `TestResetPassword_IsSingleUse` | Second call with the same raw token → `ErrInvalidResetToken`, and the password is unchanged from the first reset. |
| `TestResetPassword_Expired` | Token whose `expires_at` was written in the past → `ErrInvalidResetToken`, password unchanged. (Backdate by updating the row directly inside the tx.) |
| `TestResetPassword_PurgesAllSessions` | Create two sessions for the user beforehand (`Login` twice); after the reset, neither old token resolves, and exactly one session row exists **for that user id**. |
| `TestResetPassword_ShortPasswordDoesNotConsumeToken` | 7-char password → `*ValidationError`, and the token still works afterwards. Guards the step-1-before-step-5 ordering in §3.3. |
| `TestResetPassword_GarbageToken` | `""` and a random non-token → `ErrInvalidResetToken`, no panic, no rows touched. |
| `TestCreatePasswordReset_SupersedesOutstandingTokens` | Only if *(OQ-6)* is answered "purge": the first link stops working once a second is issued. |
| `TestResetPassword_OnlyTouchesItsOwnUser` | Two seeded users; resetting A leaves B's `password_hash` and B's session rows intact. |

### `internal/http/auth_test.go` (additions)

| Test | Asserts |
|---|---|
| `TestGetResetPassword_ValidToken` | 200, body contains the hidden token input. |
| `TestGetResetPassword_InvalidOrMissingToken` | The *(OQ-10)* status, "invalid" copy, and **no** form. |
| `TestGetResetPassword_DoesNotConsume` | Two GETs, then a successful POST — proves the peek/consume split. |
| `TestPostResetPassword_Success` | 303 to `/decks`, `__Host-deckshare_session` cookie set, and the cookie authenticates a follow-up `GET /decks`. |
| `TestPostResetPassword_ConfirmMismatch` | 400, "Passwords do not match", token still usable. |
| `TestPostResetPassword_UsedToken` | 400 with the invalid-link copy. |
| `TestPostResetPassword_MissingOrigin` | 403 from the CSRF middleware — the route inherits it and must not be exempt. |

### Migration

`go run ./scripts/run-app reset-db` then `goose ... up` against a fresh database (docs/schema.md
checklist item 2). Item 1's populated-database concern does not apply — new table, no
`ADD COLUMN`.

---

## 8. Full file list

**Create**
- `migrations/00022_password_reset_tokens.sql`
- `internal/db/queries/password_reset_tokens.sql`
- `internal/db/password_reset_tokens.sql.go` *(generated — `go generate ./...`, commit unedited)*
- `internal/auth/reset.go`
- `internal/auth/reset_test.go`
- `web/templates/reset_password.html`
- `cmd/reset-password/main.go` *(Option A)* **or** `cmd/deckshare/resetpassword.go` *(Option B)* — *(OQ-1)*

**Edit**
- `internal/auth/cleanup.go` — sweep expired reset tokens
- `internal/http/auth.go` — `GET`/`POST /reset-password` inside `registerAuthRoutes`
- `internal/http/templates.go` — `"reset_password"` in `parseTemplates`, entry in `pagePartials`
- `internal/http/auth_test.go` — route tests
- `docs/schema.md` — table listing + deletion-policy row
- `docs/routes.md` — two Auth rows
- `docs/architecture.md` §1 — "has landed" paragraph
- `CHANGELOG.md` — one `### Added` line, `([#225](…/issues/225))`
- `docs/schema-diagram.md` — *(OQ-9)*
- `.env.example` — only if *(OQ-2)* introduces an env var
- `Dockerfile` — only if *(OQ-1)* picks Option A and the reset binary must ship in the image

**Pre-commit (CLAUDE.md §14):** `go build ./...`, `go vet ./...`, `golangci-lint run`,
`go test ./...` with `DATABASE_URL` exported; branch `feature/225-password-reset-cli`; review pass
recommended at `/code-review high` (auth + schema, per §14 step 3 and §7's model guidance — this
is Opus-shaped work).

---

## Resolved decisions

These override anything above; implement exactly as stated. OQ-1..4 were answered by the user;
OQ-5..12 take the plan's drafted default (auto-mode call, user may redirect).

- **OQ-1:** (a) new binary `cmd/reset-password/main.go`, matching `cmd/seed`. Skip §4 Option B. Add the
  Dockerfile line only if `cmd/seed` already has one; otherwise don't touch the Dockerfile.
- **OQ-2:** (a) reuse `ORIGIN`, first comma-separated entry. If unset, the CLI exits non-zero with a
  clear error (no localhost fallback).
- **OQ-3:** (b) 24 hours.
- **OQ-4:** (a) `/reset-password?token=<raw>`. No redirect to a token-free URL.
- **OQ-5:** (a) `DELETE ... RETURNING` on consume.
- **OQ-6:** (a) issuing a new link deletes the account's outstanding tokens first.
- **OQ-7:** (a) issuing does not touch sessions or the password.
- **OQ-8:** (b) per-IP limiter on `POST /reset-password`, 10 per 15 min, keyed on `trusted.clientIP(r)`,
  same `newLimiter` shape as `changePassword`; add it to `cleanup.go`'s `Sweep` list.
- **OQ-9:** (a) add `PASSWORD_RESET_TOKENS` to the diagrams; do not backfill `card_flags`.
- **OQ-10:** (a) 200 with the explanatory body.
- **OQ-11:** (a) render the form regardless of an existing session.
- **OQ-12:** print display name and absolute expiry along with the link.

## Open questions (historical, all resolved above)

Every one of these was answered before implementation.

**OQ-1 — CLI shape.**
(a) New binary `cmd/reset-password/main.go`, invoked `go run ./cmd/reset-password <email>` — exactly
the `cmd/seed` precedent, no argv branching in the server binary, but needs its own `Dockerfile`
line to be present in a deployed container.
(b) Subcommand on the existing server binary: `deckshare reset-password <email>`, dispatched on
`os.Args[1]` in `cmd/deckshare/main.go` — matches the issue's literal wording and ships in the
image for free, but gives the server binary a mode it has never had.

**OQ-2 — Where the printed link's base URL comes from.**
(a) Reuse `ORIGIN` (already in `.env.example`, already the CSRF source of truth), taking the first
comma-separated entry; error out if unset.
(b) New `BASE_URL` env var, required by the CLI only.
(c) A required `--base-url` flag on the command, no env var at all.
(d) Optional `--base-url` flag overriding (a). Sub-question for (a)/(d): with `ORIGIN` unset (the
dev default), does the CLI fail, or print a `http://localhost:3000`-shaped link?

**OQ-3 — Token lifetime (`PasswordResetLifetime`).** The operator relays the link by hand — chat,
in person, a note — so the window is a human-latency question, not a machine one.
(a) 1 hour (the issue's example; tight, and a student who reads the message the next morning is
locked out again). (b) 24 hours. (c) 72 hours. (d) 7 days.

**OQ-4 — How the token rides in the link.**
(a) Query parameter `/reset-password?token=<raw>`. (b) Path segment `/reset-password/{token}`.
Both put the secret in the URL, so both are equally exposed to browser history and any
`Referer`; `Referrer-Policy: same-origin` (`internal/http/security.go`) suppresses cross-origin
leakage but still sends the full URL to same-origin destinations, and `requestLog`
(`internal/http/logging.go:56,63`) logs `r.URL.Path` only — which means (b) writes the token into
the application log and (a) does not. Sub-question either way: should the handler redirect to a
token-free URL after rendering (stashing the token in the form only), to keep it out of history?

**OQ-5 — Single-use mechanism.**
(a) `DELETE ... RETURNING` on consume (as drafted): atomic, self-cleaning, leaves no record that a
reset happened. (b) Keep the row with a `used_at timestamptz` column and consume via
`UPDATE ... WHERE used_at IS NULL RETURNING`: same atomicity, plus an operator-visible trail of
which links were used and when, at the cost of a table that grows and needs its own retention rule.

**OQ-6 — Does issuing a new link kill outstanding ones for that account?**
(a) Yes — `DeletePasswordResetTokensForUser` before the insert (as drafted): at most one live link
per account, so a relayed-then-superseded link cannot be used later. (b) No — multiple links may
be outstanding, any of which works until it expires. (Independently: §3.3 step 7 purges the rest
on a *successful* reset — is that wanted under (b)?)

**OQ-7 — Does issuing a link lock the account immediately?**
(a) No (as drafted): the account is untouched until the link is used — the student can still log in
if they remember the password after all. (b) Yes — `CreatePasswordReset` also purges the user's
sessions, appropriate if "forgot password" is being treated as possibly "account compromised".

**OQ-8 — Rate limiting `POST /reset-password`.** The token is 256 bits of `crypto/rand`, so
guessing is not a real threat; the surface is argon2 CPU burn from repeated submissions.
(a) None. (b) Per-IP limiter on the `Service` (same `newLimiter` shape as `changePassword`), keyed
on `trusted.clientIP(r)`, e.g. 10 per 15 min. (c) Per-token limiter, so one link cannot be
hammered. If (b) or (c): add the new limiter to `cleanup.go`'s `Sweep` list alongside the other four.

**OQ-9 — `docs/schema-diagram.md`.** It says "regenerate by hand", but `card_flags` (migration
00019) was never added to it.
(a) Add `PASSWORD_RESET_TOKENS` to the full-schema and "Media & auth" diagrams. (b) Match the
`card_flags` precedent and leave it, accepting the diagram is behind. (c) Add this table *and*
backfill `card_flags` in the same PR.

**OQ-10 — HTTP status for an invalid/expired/used link on `GET /reset-password`.**
(a) 200 with the explanatory body (kindest to a student who just clicked a link). (b) 400.
(c) 410 Gone (semantically precise for a consumed one-time resource, but the handler cannot
distinguish "used" from "never existed" — and deliberately should not).

**OQ-11 — A signed-in visitor hits `/reset-password`.**
(a) Render the form anyway (as drafted) — the person at the keyboard may be resetting an account
whose stale session is still in the browser. (b) Redirect to `/decks`, matching `GET /login` and
`GET /signup`. (c) Render, but clear the existing session cookie first.

**OQ-12 — CLI output detail.** Should the command also print the account's display name and the
absolute expiry timestamp (as drafted, so the operator can confirm the right account before
relaying), or only the bare link — on the grounds that terminal scrollback and shell history are
themselves a disclosure surface?
