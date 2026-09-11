# #210 — Structured error and request logging

Plan only. No code in this document is committed; it is the instruction set for the implementing
session.

**Issue:** #210 — `serverError(w)` swallows every 500's cause; there is no request log.
**Scope:** `internal/http/`, `internal/auth/middleware.go`, `cmd/deckshare/main.go`.
**Out of scope (issue states it):** any metrics endpoint or `/debug` route.
**Invariants touched:** none of §2. §2.7 is *reinforced* — the client-facing 500 body must not
change, and no error text may reach the client. §17 forbids touching the FSRS package and the
server-side recompute path; nothing here does (`internal/review/` is not edited).

---

## 0. Current state, verified

### 0.1 `internal/http/respond.go`

```
serverError(w http.ResponseWriter)                                        respond.go:16
badRequest(w http.ResponseWriter)                                         respond.go:21
handleQueryErr(w http.ResponseWriter, err error) bool                     respond.go:30
handleQueryErrPage(w, pages map[string]*template.Template, user db.User, err error) bool
                                                                          respond.go:45
parseForm(w http.ResponseWriter, r *http.Request) bool                    respond.go:55
startTx(ctx context.Context, w http.ResponseWriter, store db.Beginner) (pgx.Tx, bool)
                                                                          respond.go:66
commitTx(ctx context.Context, w http.ResponseWriter, tx pgx.Tx) bool      respond.go:76
```

`serverError` writes exactly `http.Error(w, "internal server error", 500)`.

### 0.2 Call-site census (verified by grep, non-test files only)

| helper | call sites |
|---|---|
| `serverError` | **96** (93 in handler files + 3 inside `respond.go` itself) |
| `handleQueryErrPage` | 23 |
| `handleQueryErr` | 8 (7 handler sites + 1 inside `handleQueryErrPage`) |
| `startTx` | 14 |
| `commitTx` | 13 |
| `badRequest` | 35 (not changed — see §2.4) |

The issue says "86 times"; the current count is 96. The plan is written against 96.

Every `serverError` site sits inside an `http.HandlerFunc` closure registered by a `register*Routes`
function, **except four helper functions** (§0.3). In every closure, `r *http.Request` is already
in scope as the closure parameter, and in every case an `err` value is in scope at the call site —
including the `classifyFormError` `if !ok` sites (`auth.go:39`, `settings.go:98`, `settings.go:143`),
where the unclassified `err` is the value to log. **No call site needs a synthesised error.** This
was spot-checked at `review.go:115/122`, `settings.go:98/143`, `auth.go:39`, `aiimport.go:41/48`,
`notes.go:318/327`, `media.go:85`, `decks.go:386`.

### 0.3 Helpers that call `serverError`/`handleQueryErr` and are **not** handler closures

| helper | file:line | has `r`? |
|---|---|---|
| `handleAccessChangeErr(w, pages, user, err) (ok bool)` | `access.go:242` | no |
| `renderAccess(ctx, w, pages, q, user, deck, status, errMsg)` | `access.go:259` | no (takes `ctx`) |
| `respondNotePreview(w, err) bool` | `note_preview.go:100` | no |
| `finishBulk(w, r, pages, user, deckID, n, err)` | `notes.go:604` | **yes** — no signature change |

### 0.4 Middleware and wiring

`internal/http/http.go:46` — the entire stack is one line:

```go
return securityHeaders(a.Middleware(mux)), nil
```

`security.go:75-79` documents why `securityHeaders` wraps **outside** `a.Middleware`: so the CSP
header lands on the CSRF 403 too. `requestLog` needs the same reasoning and the same side of the
sandwich.

Test stacks mirror this line in two places and must be updated in lockstep:
`internal/http/auth_test.go:118` and `internal/http/media_test.go:31`
(both `return securityHeaders(a.Middleware(mux)), a`).

### 0.5 Existing log statements (all `log.Printf` on the stdlib default logger)

| site | what |
|---|---|
| `internal/auth/middleware.go:32` | CSRF rejection |
| `internal/auth/middleware.go:69` | session-renewal failure |
| `internal/http/templates.go:89` | `render` template failure (writes a bare 500) |
| `internal/http/templates.go:101` | `renderFragment` failure (writes a bare 500) |
| `internal/auth/auth.go:180` | default note-type seeding failure |
| `internal/auth/cleanup.go:20` | session cleanup failure |
| `internal/media/gc.go:52` | media GC failure |
| `internal/http/static.go:16`, `cmd/deckshare/main.go:28` | `log.Fatal` at startup |
| `cmd/deckshare/main.go:76` | "listening on %s" |
| `cmd/seed/main.go` (many) | seed tool output — **not** part of this change |

### 0.6 Acting-user accessor

`auth.UserFromContext(ctx) (db.User, bool)` — `internal/auth/middleware.go:21`. Used at 60 sites in
`internal/http/`. This is the accessor both new logging points use; **no new accessor is
introduced.**

The catch that shapes §3: `auth.Service.Middleware` injects the user into a *derived* context and
calls `next.ServeHTTP(w, r.WithContext(ctx))` (`middleware.go:55, :75`). A middleware wrapping
*outside* auth therefore never sees the user on its own `r`. §3.2 resolves this.

### 0.7 Other facts

- `go 1.26` (`go.mod`) — `log/slog` is stdlib, no dependency added.
- Env vars read today, all in `cmd/deckshare/main.go`: `DATABASE_URL`, `ADDR`, `MEDIA_ROOT`,
  `ORIGIN`. Convention: bare `SCREAMING_SNAKE`, no `DECKSHARE_` prefix, read with `os.Getenv`,
  defaulted inline. A new var must follow that shape.
- **No `http.ResponseWriter` wrapper exists anywhere in the repo** (grep for `WriteHeader(` on a
  wrapper type: nothing). `requestLog` must define one.
- No handler uses `http.Flusher`, `Hijacker`, `ServeContent`, or `ServeFile`. Two sites use
  `io.Copy(w, f)` (`media.go:96`, `settings.go:322`).

---

## 1. slog setup

**Where:** `cmd/deckshare/main.go`, at the top of `run()`, before anything else can log.

**Shape:** configure `slog.SetDefault(...)` once; every call site uses the package-level
`slog.Error`/`slog.Info`. **No logger is injected** through `NewHandler`, `auth.Service`, or any
helper.

Rationale, recorded so it is not re-litigated: dependency injection would mean threading a
`*slog.Logger` into `NewHandler`, all 15 `register*Routes` functions, `auth.Service`,
`media.GC`, and the four helpers in §0.3 — dozens of signature changes for a single process-wide
sink. Working rule 2 (simplicity first). Tests capture output by calling `slog.SetDefault` with a
`slog.NewTextHandler(&buf, ...)` and restoring via `t.Cleanup` (§6.1).

**Handler selection** (exact env var name is Open question 1; this is the shape):

```go
// in run(), before the pool is opened
var h slog.Handler
opts := &slog.HandlerOptions{Level: slog.LevelInfo}
if os.Getenv("<LOG_FORMAT_VAR>") == "json" {
    h = slog.NewJSONHandler(os.Stdout, opts)
} else {
    h = slog.NewTextHandler(os.Stdout, opts)
}
slog.SetDefault(slog.New(h))
```

- **Default when unset:** text (dev-friendly), per the issue's "text for dev, JSON for deployment".
  Deployment opts in. The Dockerfile / StartOS manifest is **not** edited in this PR; setting the
  var there is a follow-up noted in the PR body.
- **Destination:** `os.Stdout` for both handlers (12-factor; the container collects stdout).
  Note this is a deliberate change from `log`'s default of stderr.
- **Default level:** `slog.LevelInfo`. Level is not env-configurable in this PR (Open question 4).
- `log.Fatal` in `main()` and `static.go:16` stays as-is: it runs before/independently of the
  handler choice and its job is to die loudly.

**Migrating the existing statements** (§0.5) — all of these, in this PR, so the process has one
log format rather than two:

| site | becomes |
|---|---|
| `auth/middleware.go:32` | `slog.Warn("csrf rejected", "method", r.Method, "path", r.URL.Path, "origin", r.Header.Get("Origin"), "host", r.Host)` |
| `auth/middleware.go:69` | `slog.Warn("session renewal failed", "error", err)` |
| `http/templates.go:89` | `slog.Error("render template failed", "error", err)` |
| `http/templates.go:101` | `slog.Error("render fragment failed", "error", err)` |
| `auth/auth.go:180` | `slog.Error("seed default note types failed", "user_id", user.ID, "error", err)` |
| `auth/cleanup.go:20` | `slog.Error("session cleanup failed", "error", err)` |
| `media/gc.go:52` | `slog.Error("media gc failed", "error", err)` |
| `main.go:76` | `slog.Info("listening", "addr", addr)` |

The `"log"` import is dropped from `internal/auth/middleware.go`, `internal/auth/cleanup.go`,
`internal/auth/auth.go`, `internal/http/templates.go`, and `internal/media/gc.go`; it stays in
`cmd/deckshare/main.go` (`log.Fatal`) and `internal/http/static.go`.

**Level choice:** CSRF rejection and session-renewal failure are `Warn` — they are expected in
normal operation (a stale tab, a bot). Everything a `serverError` reports is `Error`.

**`render`/`renderFragment` keep their current signatures** (no `r` added). Rationale: `render` has
47 call sites across 11 files and `renderFragment` 4; threading `r` through all of them buys only
method/path, which the `requestLog` line for that same request already carries alongside the 500
status. Recorded here so the omission reads as a decision, not an oversight.

---

## 2. `serverError` and the helpers it reaches

### 2.1 New signature

```go
// serverError writes the generic 500 response for an unexpected error and logs the cause. The
// client-facing body is unchanged and never carries err -- §2.7 and CLAUDE.md §10.1: the error
// detail goes to the operator, never to the caller.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	user, _ := auth.UserFromContext(r.Context())
	slog.Error("server error",
		"method", r.Method,
		"path", r.URL.Path,
		"user_id", user.ID,
		"error", err,
	)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
```

Notes for the implementer:
- `auth.UserFromContext` returns the zero `db.User` when unauthenticated; `user.ID` is then the
  zero `pgtype.UUID`. Log it as-is rather than branching — but confirm how `pgtype.UUID` renders
  under both slog handlers and, if it prints as a struct rather than a UUID string, log
  `user.ID.String()` guarded on `user.ID.Valid` (see Open question 2 on field naming/rendering).
- `respond.go` gains imports `log/slog` and `github.com/Jolls/deckshare/internal/auth`. Check for
  an import cycle: `internal/auth` imports `internal/db` only, not `internal/http` — no cycle.
- The response body, status, and header order are byte-identical to today. `security_test.go` and
  the existing 500 assertions must keep passing untouched.

### 2.2 Helper signature changes (exhaustive)

| helper | from | to |
|---|---|---|
| `serverError` | `(w)` | `(w, r, err)` |
| `handleQueryErr` | `(w, err)` | `(w, r, err)` |
| `handleQueryErrPage` | `(w, pages, user, err)` | `(w, r, pages, user, err)` |
| `startTx` | `(ctx, w, store)` | `(r, w, store)` — see §2.3 |
| `commitTx` | `(ctx, w, tx)` | `(r, w, tx)` — see §2.3 |
| `handleAccessChangeErr` | `(w, pages, user, err)` | `(w, r, pages, user, err)` |
| `renderAccess` | `(ctx, w, pages, q, user, deck, status, errMsg)` | `(r, w, pages, q, user, deck, status, errMsg)` |
| `respondNotePreview` | `(w, err)` | `(w, r, err)` |
| `finishBulk` | — | unchanged; `r` already a parameter |
| `badRequest`, `notFound`, `notFoundPage`, `parseForm`, `render`, `renderFragment` | — | unchanged |

Parameter order convention to apply uniformly: `(w, r, …)` — matching `http.HandlerFunc` and the
existing `parseForm(w, r)` and `finishBulk(w, r, …)`.

### 2.3 `startTx` / `commitTx`: replace `ctx` with `r`

All 14 `startTx` and all 13 `commitTx` call sites pass `r.Context()` verbatim (verified — there is
no non-request caller). Adding `r` *alongside* `ctx` would make every site pass the same request
twice. Replace it: the helper does `r.Context()` internally.

```go
func startTx(r *http.Request, w http.ResponseWriter, store db.Beginner) (pgx.Tx, bool) {
	tx, err := store.Begin(r.Context())
	if err != nil {
		serverError(w, r, fmt.Errorf("begin transaction: %w", err))
		return nil, false
	}
	return tx, true
}

func commitTx(r *http.Request, w http.ResponseWriter, tx pgx.Tx) bool {
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, fmt.Errorf("commit transaction: %w", err))
		return false
	}
	return true
}
```

The `fmt.Errorf` wrap is what distinguishes a begin failure from a commit failure in the log, since
neither call site names the operation. `handleQueryErr`'s internal `serverError` call passes `err`
unwrapped (the query name is not available there; the path in the log line identifies the route).

Callers still `defer tx.Rollback(r.Context())` themselves — unchanged.

### 2.4 `badRequest` is deliberately not changed

35 call sites, all client-malformed-input. A 400 is not an operator-actionable event and the issue
does not ask for it; the `requestLog` line already records that a request returned 400 with its
method, path and user. Out of scope, recorded so it is not added mid-review.

### 2.5 Call sites to edit — exhaustive list

Every line below is a mechanical edit: `serverError(w)` → `serverError(w, r, err)` where `err` is
the error variable already in scope on the immediately preceding `if err != nil` (or the
`classifyFormError` input). **This cannot be done with a blind `sed`** — each site must be read to
confirm the in-scope error's name, and four of them (§0.3) require the enclosing helper's own
signature change first.

`serverError` — 96 sites:

- `access.go`: 87, 104, 251, 262
- `aiimport.go`: 41, 48, 64, 117, 167, 197
- `auth.go`: 39, 78, 98
- `decks.go`: 36, 42, 64, 73, 86, 146, 169, 183, 197, 203, 219, 229, 239, 249, 256, 261, 266, 386, 402, 450
- `export.go`: 50
- `flags.go`: 77, 102, 135
- `import.go`: 62
- `media.go`: 85
- `notes.go`: 101, 120, 151, 156, 174, 200, 224, 229, 234, 239, 246, 273, 278, 283, 318, 327, 349, 393, 419, 606
- `notetypes.go`: 87, 92, 151, 174, 179, 184, 240, 245, 252, 259, 288, 311
- `progress.go`: 86, 92, 106
- `respond.go`: 37, 69, 78
- `review.go`: 58, 63, 68, 74, 99, 106, 115, 122, 165, 197
- `settings.go`: 60, 98, 143, 193, 211, 264, 276, 282, 313

`handleQueryErrPage(w, …)` → `handleQueryErrPage(w, r, …)` — 23 sites:

- `access.go`: 55, 73
- `aiimport.go`: 59, 78, 112
- `decks.go`: 164, 294, 419
- `export.go`: 40
- `flags.go`: 92
- `notes.go`: 93, 115, 146, 219, 268, 308, 313, 412, 451
- `notetypes.go`: 169, 230
- `progress.go`: 68
- `review.go`: 48

`handleQueryErr(w, err)` → `handleQueryErr(w, r, err)` — 8 sites:

- `flags.go`: 71
- `media.go`: 79
- `note_preview.go`: 42, 46, 77, 108 (108 is inside `respondNotePreview`)
- `respond.go`: 50 (inside `handleQueryErrPage`)
- `review.go`: 159

`startTx(r.Context(), w, store)` → `startTx(r, w, store)` — 14 sites:

- `access.go`: 134, 172, 200
- `aiimport.go`: 181
- `decks.go`: 131
- `export.go`: 33
- `import.go`: 54
- `notes.go`: 178, 379, 445
- `notetypes.go`: 139, 270
- `review.go`: 189
- `settings.go`: 267

`commitTx(r.Context(), w, tx)` → `commitTx(r, w, tx)` — 13 sites:

- `access.go`: 153, 181, 209
- `aiimport.go`: 201
- `decks.go`: 149
- `import.go`: 65
- `notes.go`: 204, 397, 454
- `notetypes.go`: 154, 292
- `review.go`: 200
- `settings.go`: 285

`handleAccessChangeErr(w, …)` → `(w, r, …)` — `access.go`: 150, 178, 206.
`renderAccess(r.Context(), w, …)` → `renderAccess(r, w, …)` — `access.go`: 58, 84, 101.
`respondNotePreview(w, err)` → `(w, r, err)` — `note_preview.go`: 58, 89.

Line numbers are from the pre-change tree and drift as edits land; work bottom-up per file, or
re-grep after each file. `go build ./...` is the completeness check — every missed site is a
compile error, which is the point of changing the signature rather than adding an optional variant.

---

## 3. `requestLog` middleware

New file: `internal/http/logging.go` (keeps `security.go` about headers and `respond.go` about
responses).

### 3.1 Wrapping order

`internal/http/http.go:46` becomes:

```go
return requestLog(securityHeaders(a.Middleware(captureUser(mux)))), nil
```

Outermost, for the same reason `securityHeaders` sits outside `a.Middleware` (documented at
`security.go:75`): a request rejected by the CSRF check must still produce a log line, and the
duration must cover the whole stack. `NewHandler`'s doc comment (`http.go:15-18`) is extended with
one sentence saying so.

`internal/http/auth_test.go:118` and `internal/http/media_test.go:31` get the identical wrap, so
route tests exercise the real stack (their comments already state that intent).

### 3.2 Getting the user id out of auth's derived context

`a.Middleware` puts the user on a context it creates and passes *down* (`middleware.go:55, :75`), so
`requestLog`, sitting above it, cannot read it from its own `r`. Fix with a one-cell relay entirely
inside `internal/http`, using the existing `auth.UserFromContext` accessor and no change to
`internal/auth`:

```go
type userCellKey struct{}

// captureUser copies the authenticated user out of auth.Middleware's derived context into the cell
// requestLog placed above it. It is the innermost wrap, below auth.Middleware, because that is the
// only place the user is visible; requestLog is the outermost, because the CSRF 403 has to be
// logged too. The cell is written and read on the same goroutine (read only after ServeHTTP
// returns), so no synchronisation is needed.
func captureUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cell, ok := r.Context().Value(userCellKey{}).(*db.User); ok {
			if u, found := auth.UserFromContext(r.Context()); found {
				*cell = u
			}
		}
		next.ServeHTTP(w, r)
	})
}
```

Alternatives rejected, recorded so they are not revisited: (a) wrapping `requestLog` *inside*
`a.Middleware` — loses the CSRF-rejection log line the issue explicitly requires; (b) exporting a
setter from `internal/auth` — more surface for the same result, and the issue's instruction is to
use the existing accessor; (c) dropping user id from the request line — the issue names it as a
required field.

### 3.3 Status capture

No `ResponseWriter` wrapper exists in the repo; define one here.

```go
// statusRecorder remembers the status a handler wrote, so requestLog can report it. A handler that
// writes a body without calling WriteHeader implies 200, which is why status starts at 200.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
```

- No `Write` override is needed: the zero-value-avoiding `status: 200` initialiser covers the
  implicit case.
- No `Flusher`/`Hijacker`/`Pusher` passthrough: grep confirms no handler uses any of them.
- No `io.ReaderFrom` passthrough. `media.go:96` and `settings.go:322` `io.Copy(w, f)` lose the
  `ReadFrom` fast path and fall back to a 32 KiB buffered copy. Deliberate (working rule 2); noted
  here so a reviewer sees it was considered.

### 3.4 The middleware

```go
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var user db.User
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), userCellKey{}, &user)))
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"user_id", user.ID,
		)
	})
}
```

- `r.URL.Path` only — never `r.URL.RawQuery`. Query strings on this app carry deck ids, note ids
  and review cursors; the CSP `Referrer-Policy` comment at `security.go:85` makes the same call
  about not leaking ids outward, and a log file is the same kind of exposure.
- Duration as `duration_ms` integer (see Open question 2 for the naming decision).
- `/static/*` and `/healthz` are logged like everything else in this PR — see Open question 3.

---

## 4. Files changed

| file | change |
|---|---|
| `cmd/deckshare/main.go` | slog handler selection; `log.Printf` → `slog.Info` at :76 |
| `internal/http/logging.go` | **new** — `requestLog`, `statusRecorder`, `captureUser`, `userCellKey` |
| `internal/http/http.go` | wrap order at :46 + doc comment |
| `internal/http/respond.go` | 6 signature changes, `serverError` logs |
| `internal/http/templates.go` | 2 `log.Printf` → `slog.Error`; drop `"log"` import |
| `internal/http/access.go` | `handleAccessChangeErr`, `renderAccess` signatures + all call sites |
| `internal/http/note_preview.go` | `respondNotePreview` signature + call sites |
| `internal/http/{aiimport,auth,decks,export,flags,import,media,notes,notetypes,progress,review,settings}.go` | call-site updates only |
| `internal/auth/middleware.go` | 2 `log.Printf` → `slog`; drop `"log"` import |
| `internal/auth/auth.go`, `internal/auth/cleanup.go`, `internal/media/gc.go` | `log.Printf` → `slog` |
| `internal/http/auth_test.go`, `internal/http/media_test.go` | test stacks gain `requestLog`/`captureUser` |
| `internal/http/logging_test.go` | **new** — §6 |
| `.env.example` | document the new log-format var |
| `docs/architecture.md` | one line under §3 Stack recording `log/slog`, stdout, env-selected handler |
| `CHANGELOG.md` | one `### Added` entry citing #210 |

No migration. No schema change. No generated code.

---

## 5. Suggested implementation order

1. slog setup in `main.go` + migrate the seven existing `log.Printf` sites (§1). Build; behaviour
   visibly unchanged except format.
2. `internal/http/logging.go` + the wrap in `http.go` + the two test stacks (§3). Build; run
   `go test ./internal/http/...` — every existing route test should still pass, which is the
   evidence the middleware is transparent.
3. `respond.go` signatures (§2.1–2.3). This breaks the build in 12 files by design.
4. Fix call sites file by file, in the §2.5 order. `go build ./...` is green only when all 96 are
   done.
5. `access.go` / `note_preview.go` helper signatures (§0.3).
6. New tests (§6).
7. `go build ./... && go vet ./... && golangci-lint run && go test ./...` with `DATABASE_URL`
   exported (CLAUDE.md §16 — an unset `DATABASE_URL` silently skips every DB-backed test and still
   prints `ok`).

---

## 6. Tests

CLAUDE.md §10 priority 1 is "the client cannot write scheduling state"; priority 5 is access
control. This change is adjacent to both — it touches the response path for every authorisation
denial and every 500. Three tests are warranted; all go in a new `internal/http/logging_test.go`
except 6.2, which belongs with the auth middleware tests.

**6.1 Harness.** A helper that swaps the default logger for the duration of a test:

```go
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
```

This makes `slog.SetDefault` the reason package-level logging is testable at all — worth a comment
in the helper.

**6.2 A CSRF rejection is logged and still 403s.** Send a state-changing request with a bad
`Origin` through `newTestHandler`. Assert: status 403, body unchanged, and the captured output
contains both a `csrf rejected` line and a `request` line with `status=403`. This is the test that
pins the wrapping order — if `requestLog` is ever moved inside `a.Middleware`, the `request` line
disappears and this fails. The order is the whole point of §3.1, so the test must assert the
`request` line, not merely the `csrf rejected` one.

**6.3 A 500 logs the cause and leaks nothing.** Drive a handler into `serverError` (a closed pool
or a cancelled context on an existing DB-backed route is the cheapest trigger; failing that, call
`serverError` directly with an `httptest.NewRecorder` and a `httptest.NewRequest`). Assert:
- response body is exactly `internal server error\n` and status is 500 — **the anti-leak
  assertion**, and the one that must be read as protecting §2.7;
- the body does not contain the error's message text (assert on a distinctive sentinel string,
  e.g. `errors.New("sentinel-abc123")`, being absent from the body and present in the log);
- the log line carries `method`, `path`, and the acting `user_id`.

**6.4 `requestLog` reports the handler's status and the acting user.** Table-driven over a stub
handler that writes 200 / 404 / 500 / nothing-at-all (implicit 200), through the full
`requestLog(securityHeaders(a.Middleware(captureUser(mux))))` stack with a logged-in session.
Assert the status field matches and `user_id` is the session's user — this is what proves the
`captureUser` relay in §3.2 works, and it is the piece most likely to silently regress to a zero
UUID.

No new fixtures. Existing route tests are the regression net for "the response bytes did not
change" — if any of them start failing, `serverError`'s body changed and that is a §2.7 defect, not
a test to update.

---

## 7. Resolved decisions

1. **Env var: `LOG_FORMAT`**, values `text` (default) / `json`. Use option (a) throughout §1;
   `.env.example` and `docs/architecture.md` §3 document `LOG_FORMAT`.
2. **Field naming/rendering:**
   (a) `snake_case` keys.
   (b) `duration_ms` as an integer millisecond count.
   (c) `user_id` logs `""` when unauthenticated. Implementer must confirm how `pgtype.UUID`
   renders under both handlers before finalizing §2.1/§3.4: if `user.ID` doesn't stringify
   cleanly as a UUID, log `user.ID.String()` guarded on `user.ID.Valid`, else `""` — the goal is
   every `request`/`server error` log line always has a `user_id` string field, empty when there
   is no authenticated user.
3. **`requestLog` logs everything**, including `/static/*` and `/healthz` — no skip list, no
   `Debug`-level demotion, per option (a).
4. **Log level is fixed at `slog.LevelInfo`**, not env-configurable. No `LOG_LEVEL` var in this PR.
5. **`cmd/seed`'s `log.Printf` calls stay on `log`, unmigrated.** Out of scope — CLI progress
   output for a dev tool, not an operator log. Note this in the PR body so a reviewer doesn't
   flag it as inconsistent.

