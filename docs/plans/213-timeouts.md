# #213 — HTTP server and DB timeouts

## Problem

`cmd/deckshare/main.go:69` builds `&http.Server{}` with no `ReadHeaderTimeout`,
`ReadTimeout`, `WriteTimeout`, `IdleTimeout`, or `MaxHeaderBytes` — the Slowloris shape
`gosec` G112 flags. `db.NewPool` (`internal/db/pool.go:13`) is a bare `pgxpool.New(ctx, dsn)`
with no `statement_timeout`. No handler derives a `context.WithTimeout`, so a slow query holds
its goroutine, pool connection, and (with no `WriteTimeout`) socket indefinitely. `POST
/import` runs synchronously inside one transaction holding a pool connection for the whole
read+import; with `max(4, NumCPU)` pool connections, a handful of concurrent imports exhausts
the pool and stalls every other route.

## Current shape (confirmed by reading)

- `cmd/deckshare/main.go`: `addr` from `ADDR` env (default `:3000`), `dsn` from
  `DATABASE_URL` (required, no parsing/mutation — passed straight to `db.NewPool`). Server is
  built at line 69, `ListenAndServe()`'d in a goroutine, shut down via
  `srv.Shutdown(context.WithTimeout(10s))` on signal.
- `internal/db/pool.go`: `NewPool(ctx, dsn) -> pgxpool.New(ctx, dsn)`, no `pgxpool.Config`
  touched, no `AfterConnect` hook exists yet.
- `internal/http/import.go` — `POST /import`: `MaxBytesReader` caps the raw upload at 550 MiB
  (`maxUploadBytes`, ahead of `apkg.DefaultArchiveLimits()`'s 500 MiB decompressed / 5,000
  member ceiling — the largest real package inspected had 546 media files, no member over 100
  MiB). Flow: `ParseMultipartForm` → `apkg.Read` (in-memory, no DB, bounded by those archive
  limits) → `startTx` → `apkg.Import` (all DB writes, single tx) → `commitTx` → redirect. The
  DB-holding portion is `apkg.Import` alone; `apkg.Read` happens before the transaction opens.
- `internal/http/export.go` — `GET /decks/{id}/export`: `startTx` (read-only, always rolled
  back) → `apkg.Export` (eight read statements against one snapshot) → `apkg.Write` into an
  in-memory `bytes.Buffer` (deliberately buffered, not streamed, so a write failure can't leave
  a truncated download believed complete) → headers + `buf.WriteTo(w)`. The DB-holding portion
  is `apkg.Export`; encoding to the buffer and the final write to `w` happen after the
  transaction's queries but `apkg.Export`'s tx is still open (rolled back via `defer`) during
  the buffer write — see Decision 4 below for where the timeout should therefore be scoped.
- No existing `context.WithTimeout` call anywhere in `internal/http`.
- `go.mod`: `github.com/jackc/pgx/v5 v5.10.0` — current `pgxpool.Config.AfterConnect` signature
  is `func(ctx context.Context, conn *pgx.Conn) error`.

## §17 check

No changes touch the FSRS scheduling package (`internal/fsrs` or wherever it lives) or the
server-side recompute path (§2.7/§6). This is server plumbing (`http.Server`, `pgxpool`, two
handlers) only. Not anticipating any diff outside `cmd/deckshare/main.go`,
`internal/db/pool.go`, `internal/http/import.go`, `internal/http/export.go`, and their tests.

## Decisions

### 1. `http.Server` fields (`cmd/deckshare/main.go`)

```go
srv := &http.Server{
    Addr:              addr,
    Handler:           handler,
    ReadHeaderTimeout: 5 * time.Second,
    IdleTimeout:       120 * time.Second,
    MaxHeaderBytes:    1 << 20, // 1 MiB; same as net/http's own DefaultMaxHeaderBytes, made explicit
}
```

- `ReadHeaderTimeout: 5s` — this is the field that actually closes Slowloris (G112's target):
  time allowed between accepting the connection and finishing reading request headers. 5s is
  generous for any real client (headers are a few KB) and doesn't touch body upload time at
  all, so it does not conflict with `/import`'s large uploads.
- `IdleTimeout: 120s` — how long a keep-alive connection may sit idle between requests. Long
  enough not to punish a browser tab left open between reviewer actions; short enough to reap
  connections held open with no work happening (the other half of the Slowloris-family
  exposure — a connection that finished one request and then never sends another).
  `net/http` falls back to `ReadTimeout` if `IdleTimeout` is zero, and per Decision 2 we're not
  setting `ReadTimeout`, so this needs an explicit value.
- `MaxHeaderBytes: 1 << 20` — matches `net/http`'s own default; set explicitly rather than
  relying on the zero-value fallback so the intent is on the page next to the other three
  fields, and so a future reader doesn't have to know the stdlib default to know what's
  enforced here.

### 2. `ReadTimeout` / `WriteTimeout`: leave unset, use per-handler `context.WithTimeout`

Recommendation: **do not set a blanket `ReadTimeout`/`WriteTimeout` on `http.Server`.** Use
explicit `context.WithTimeout` in the two slow handlers instead (Decision 4), and leave every
other handler to inherit the request's own context with no deadline beyond what the mux/pool
already bound.

Reasoning:
- `ReadTimeout` covers the *entire* request read including the body. `/import` accepts uploads
  up to 550 MiB; a single blanket value would have to be sized for the slowest legitimate
  upload on the slowest legitimate connection, which makes it useless as a Slowloris defense
  for every other route (it would have to be minutes-long to not clip a real upload on a weak
  connection) — the same problem the issue calls out.
  `ReadHeaderTimeout` already closes the actual Slowloris gap (header-only, not body), so
  `ReadTimeout` isn't doing defensive work `ReadHeaderTimeout` doesn't already do; its only
  remaining job would be bounding total body-read time, which per-handler timeouts do more
  precisely.
- `WriteTimeout` covers from the end of reading the request headers through the end of
  writing the response — for `/decks/{id}/export` that includes the DB read, the in-memory
  `apkg.Write` encode, and the full response write. A blanket value again has to be sized for
  the largest legitimate export, defeating its purpose as a general safety net for small,
  fast handlers.
- Per-handler `context.WithTimeout` bounds exactly the work each handler actually does (the DB
  transaction, in `/import`'s case also the archive read), independent of client network speed,
  and ties the deadline to `r.Context()` so it's cancelled on client disconnect same as today.
  It is the mechanism the issue's own proposed-fix list treats as the alternative to a blanket
  value, and it composes better with `apkg.DefaultArchiveLimits()` already bounding read size
  rather than read time.

Every other handler continues to have no explicit deadline beyond `ReadHeaderTimeout` +
`IdleTimeout` closing the connection-level exposure, and (after Decision 3) `statement_timeout`
bounding any individual query. That is the intended layering: connection-level limits at the
`http.Server`, query-level limits at the DB, and two explicit request-level limits only where a
handler's legitimate work is long enough to need one.

### 3. `statement_timeout`: `pgxpool.Config.AfterConnect`, not the DSN

Set via `pgxpool.Config.AfterConnect` with a `SET statement_timeout` on each new connection,
not a `statement_timeout` DSN query parameter.

```go
// internal/db/pool.go
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
    cfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, fmt.Errorf("db: parsing dsn: %w", err)
    }
    cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
        _, err := conn.Exec(ctx, "SET statement_timeout = '30s'")
        return err
    }
    return pgxpool.NewWithConfig(ctx, cfg)
}
```

Reasoning over the DSN-param route:
- A DSN `statement_timeout` query param is passed through to Postgres as a startup parameter
  string and is easy to get silently wrong (units, quoting, whether pgx even forwards an
  unrecognised param) without a compile-time signal. `AfterConnect` is plain Go, checked by the
  compiler, and shows up in a stack trace / log if the `SET` ever fails.
  `pgxpool.ParseConfig` still accepts `DATABASE_URL`'s existing DSN form unchanged, so this is
  not a caller-visible change (no new env var, no doc update needed).
- `SET statement_timeout` at session level is the same mechanism `AfterConnect` is designed
  for (per-connection setup run once, cached for the life of the pooled connection) and reads
  as one clear line next to where the pool is built.
- `NewPool`'s signature (`ctx, dsn`) stays unchanged, so no caller in `cmd/deckshare/main.go`
  needs to change beyond the value already passed in.

### 4. Per-handler deadlines

`/import` (`internal/http/import.go`, `POST /import` handler): wrap only the
DB-holding portion, not the multipart parse or the in-memory `apkg.Read`.

```go
importCtx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
defer cancel()

tx, ok := startTx(importCtx, w, store)
if !ok {
    return
}
defer func() { _ = tx.Rollback(importCtx) }()

result, err := apkg.Import(importCtx, tx, user.ID, col, now(), blobs)
...
if !commitTx(importCtx, w, tx) {
    return
}
```

`apkg.Read` stays on `r.Context()` (unbounded beyond the request's own lifecycle) since it's
pure CPU/memory work already bounded by `apkg.DefaultArchiveLimits()`'s byte/member ceilings,
not a DB hold — timing it out wouldn't free a pool connection, only add a second failure mode
for large-but-legitimate packages to hit.

`/decks/{id}/export` (`internal/http/export.go`, `GET /decks/{id}/export` handler): wrap the
transaction (`apkg.Export`'s reads and the rollback), not the header write / buffer send.

```go
exportCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
defer cancel()

tx, ok := startTx(exportCtx, w, store)
if !ok {
    return
}
defer func() { _ = tx.Rollback(exportCtx) }()

col, err := apkg.Export(exportCtx, tx, deckID, user.ID, now())
if handleQueryErrPage(w, pages, user, err) {
    return
}
```

`apkg.Write` (encode into `buf`) and the final `buf.WriteTo(w)` stay outside the timeout: both
are in-memory/socket operations after the transaction's queries have already returned, so a
`context.WithTimeout` on them would do nothing but add an unused-parameter footprint (`Write`
and `WriteTo` don't take a context) — the transaction itself is already rolled back via
`defer` by the time those run, so no pool connection is held across them.

## Open questions

The two per-handler durations above are not derivable from the codebase — no request-duration
metrics or prior incident data exist to size them precisely. Recommending the values used above,
with alternatives:

1. **`/import` transaction timeout** — options: **60s**, **90s (recommended)**, **120s**.
   `apkg.DefaultArchiveLimits()` bounds the *read* (pre-transaction) to 500 MiB / 5,000
   members, not the DB write time; there's no fixture or prod data showing how long
   `apkg.Import` takes against a package at that ceiling. 90s gives real imports (the one
   inspected export: 546 media files) comfortable headroom while still failing well before a
   client-side HTTP timeout or a human giving up and refreshing.
2. **`/decks/{id}/export` transaction timeout** — options: **30s**, **60s (recommended)**,
   **90s**. Export's eight read statements are lighter than import's writes, so a shorter
   bound than import's is reasonable, but again no measured p99 exists to anchor it precisely.
3. **`statement_timeout` value** — options: **15s**, **30s (recommended)**, **60s**. Applies to
   every query on the pool, not just import/export, so it needs to be safely above the slowest
   *legitimate* single statement anywhere in the app (none of which are known to approach even
   15s) while still being well under the per-handler timeouts above so a single runaway query
   fails before it can consume the whole handler budget. 30s satisfies that ordering against
   both recommended handler timeouts (60s, 90s) with room to spare.

## Resolved decisions

1. **`/import` transaction timeout: 90s.** Use `90 * time.Second` in Decision 4.
2. **`/decks/{id}/export` transaction timeout: 60s.** Use `60 * time.Second` in Decision 4.
3. **`statement_timeout`: 30s.** Use `'30s'` in the `AfterConnect` `SET` in Decision 3.

(All three match the recommended values already used in the code samples above — no code change
needed as a result of this resolution, just confirming them as final.)

## Regression tests (CLAUDE.md §10)

Not in the FSRS or `.apkg` reader/writer packages, so §10's "always ships a test" exception
doesn't apply — but §10 item 1 priority aside, these are worth a small test per CLAUDE.md
rule 5 (non-obvious, silent-break potential: a misconfigured timeout wouldn't fail loudly, it
would just make the app behave like today's unbounded version under load):

1. **`http.Server` field assertions** — a lightweight test in `cmd/deckshare` (or wherever
   `run()`'s server construction is reachable/refactored to be testable) asserting
   `ReadHeaderTimeout`, `IdleTimeout`, and `MaxHeaderBytes` are non-zero and match the chosen
   constants. Cheap, catches an accidental revert or copy-paste drop of a field.
2. **`/import` and `/decks/{id}/export` still succeed under the new context deadline** — extend
   the existing table-driven tests in `internal/http/import_test.go` and
   `internal/http/export_test.go` (both already exist and presumably exercise the happy path)
   to confirm a normal-sized fixture still completes within the new timeout — i.e. no
   regression from wrapping `r.Context()` in `context.WithTimeout` rather than passing it
   through unchanged. Doesn't need a new slow-path fixture to prove the *timeout* fires (that
   would need a deliberately-slow DB stub, which is more machinery than this issue's scope
   justifies) — the goal is catching an accidentally-too-short constant or a context wired to
   the wrong scope, not proving Go's own `context.WithTimeout` behaviour.
3. **`statement_timeout` is actually applied** — if `DATABASE_URL` is set (§16: DB-backed tests
   tolerate a populated DB and are skipped otherwise), a small test in `internal/db` that opens
   a pool via `NewPool` and asserts `SHOW statement_timeout` on a connection returns the
   configured value. Directly verifies Decision 3's `AfterConnect` hook actually ran, which a
   typo in the `SET` statement wouldn't otherwise surface until a query hangs in production.

No test is proposed for the *DSN-param* alternative since Decision 3 rejects it.
