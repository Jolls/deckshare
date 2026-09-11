# #211 — Trusted-proxy client IP for the rate-limit key

## Problem

`clientIP` (`internal/http/auth.go:114-120`) returns the `net.SplitHostPort` host of
`r.RemoteAddr` and nothing else. The expected deployment terminates TLS at a reverse proxy
(`.env.example`'s `ORIGIN` block; the `__Host-`/`Secure` session cookie makes plain HTTP
unusable), so every request's `RemoteAddr` is the proxy. Both IP buckets in
`internal/auth/auth.go` then collapse into one instance-wide bucket:
`loginIPLimit` (20 / 15 min) locks `/login` for everyone, `signupIPLimit` (5 / hour) caps the
whole instance at five signups an hour. The per-email bucket is unaffected (keyed on email),
so this is an availability failure, not a credential-stuffing one.

Reading `X-Forwarded-For` unconditionally is strictly worse: the key becomes attacker-chosen
and rate limiting is bypassed entirely. The fix must be opt-in and peer-gated.

## Current shape (confirmed by reading)

- `internal/http/auth.go`
  - `registerAuthRoutes(mux *http.ServeMux, a *auth.Service, pages map[string]*template.Template)`.
  - Two call sites of `clientIP(r)`: `POST /signup` (line 30) and `POST /login` (line 69).
  - `clientIP` (lines 114-120) is a free function; `net` is imported for it and for nothing
    else in the file.
- `internal/auth/auth.go` — `Signup(ctx, ip, …)` / `Login(ctx, ip, …)` take the IP as an opaque
  `string` and pass it straight to `limiter.Allow(key)` (`internal/auth/ratelimit.go:35`). The
  key is an arbitrary string; no format is assumed. **Nothing in `internal/auth` changes.**
- `internal/http/http.go` — `NewHandler(pool *pgxpool.Pool, a *auth.Service, blobs *media.Store)
  (http.Handler, error)`; calls `registerAuthRoutes(mux, a, pages)` at line 33. Sole caller is
  `cmd/deckshare/main.go:74`.
- `cmd/deckshare/main.go` — env is read inline in `run()`; `auth.New(pool, auth.Config{Origin:
  os.Getenv("ORIGIN")})` at line 65 is the precedent for "raw env string in, parsed once at
  construction, error returned on bad input".
- `internal/http/auth_test.go` — `newTestHandler` (line 83) builds the route stack itself and
  calls `registerAuthRoutes(mux, a, pages)` at line 104. `doRequest` (line 130) cannot set
  `RemoteAddr` or arbitrary headers; tests that need those build their own `httptest` request.
- `go 1.26`, so `net/netip` is available. No existing use of `netip` in the repo.

## §17 check

Nothing here touches the FSRS package, the server-side recompute path, `review_log`, migrations,
or the schema. Diff is confined to `internal/http` (one new file + two edits), `cmd/deckshare`,
`.env.example`, and `CHANGELOG.md`.

## Decisions

### 1. Config: `TRUSTED_PROXY`, comma-separated CIDRs, parsed in `internal/http`

- Env var name: `TRUSTED_PROXY` (as the issue specifies). Value: comma-separated CIDR prefixes,
  IPv4 and IPv6 mixed freely, e.g. `TRUSTED_PROXY="10.0.0.0/8,172.16.0.0/12,fd00::/8"`.
- Whitespace around each element is trimmed; empty elements (trailing comma, `" , "`) are
  skipped. An empty or unset value parses to `nil` — the default, which must behave
  byte-for-byte as today.
- **CIDR only.** A bare IP (`10.0.0.1`) is a parse error whose message says to write `/32` or
  `/128`. One accepted form, one code path.
- Parsing lives in `internal/http` next to its only consumer, and happens once at handler
  construction (mirroring `auth.New`'s treatment of `Config.Origin`), not per request. A bad
  value fails startup with a wrapped error rather than silently disabling the feature.

### 2. Threading: raw env string → `NewHandler` → `registerAuthRoutes` → `clientIP`

`clientIP` stays in `internal/http` (it is HTTP-layer request parsing; `internal/auth` takes an
opaque key string and must not learn about `*http.Request` for this). It stops being a bare free
function and becomes a method on the parsed prefix list, so the config travels with the value:

- `NewHandler(pool, a, blobs, trustedProxy string)` — parses, returns
  `fmt.Errorf("parse TRUSTED_PROXY: %w", err)` on failure.
- `registerAuthRoutes(mux, a, pages, trusted proxySet)` — the two handler closures capture
  `trusted` and call `trusted.clientIP(r)`.
- `cmd/deckshare/main.go` passes `os.Getenv("TRUSTED_PROXY")`.

No other `register*Routes` function changes; no other handler needs a client IP today.

### 3. The algorithm

```
func (t proxySet) clientIP(r *http.Request) string
```

1. `peer` := host of `r.RemoteAddr` via `net.SplitHostPort`; on error, `r.RemoteAddr` verbatim
   (preserves today's fallback exactly).
2. If `len(t) == 0` → return `peer`. **The header is not even read.** This is the unset default
   and the whole no-regression guarantee.
3. Parse `peer` with `netip.ParseAddr`; normalise with `.Unmap()` and `.WithZone("")`. If it
   does not parse, or it is not contained in any prefix of `t` → return `peer`. (Header ignored
   for an untrusted peer — spoof protection.)
4. The peer is trusted. Collect hops: `strings.Join(r.Header.Values("X-Forwarded-For"), ",")`
   then `strings.Split(…, ",")`. Joining `Values` matters because a chain may arrive as several
   `X-Forwarded-For` header lines; RFC 7230 makes that equivalent to one comma-joined line.
   If the joined string is empty (header absent) → return `peer`.
5. Walk the hops **right to left**. For each: `strings.TrimSpace`, then `netip.ParseAddr`,
   then `.Unmap().WithZone("")`.
   - unparseable → **stop walking and return `peer`** (see "malformed" below);
   - contained in any prefix of `t` → it is one of our own proxies; skip it and continue
     leftward;
   - otherwise → return `addr.String()` (canonical form, so `::ffff:203.0.113.9` and
     `203.0.113.9` are the same bucket, and `2001:db8::1` and `2001:0db8::1` are too).
6. Ran off the left end with every hop trusted → return `peer`.

Behaviour of each edge case, stated explicitly:

- **Multiple trusted proxies.** `t` is a set, not an ordered chain: a hop is skipped if it is
  inside *any* configured prefix, in any order and at any depth. A two-hop internal chain
  (`edge → sidecar → app`) works by listing both CIDRs; nothing has to describe their order.
- **Missing header.** Step 4 returns `peer` — identical to today for that request.
- **Malformed / unparseable entry.** Terminal: stop and return `peer`. Rationale: hops to the
  right of a trusted hop were appended by our own proxies and are always well-formed, so
  garbage can only appear in the attacker-controlled left portion of the chain. Continuing
  past it would walk into attacker-supplied data with the trust decision already spent;
  falling back to `peer` degrades exactly one request to today's (safe, over-restrictive)
  behaviour. A garbage entry to the *left* of a valid untrusted hop is never reached, because
  the walk returns at that valid hop first.
  - Corollary, deliberate: a port-suffixed hop (`203.0.113.9:41234`, which a few proxies emit)
    is "malformed" under this rule and falls back to `peer`. Not supported until something
    actually needs it.
- **Header containing only trusted-looking IPs.** Step 6 returns `peer`. This is the
  misconfiguration shape (a CIDR listed as trusted that real clients also sit inside); the
  result is today's collapsed bucket, never an attacker-chosen key.
- **IPv4-mapped IPv6** (`::ffff:10.0.0.5`): `.Unmap()` before the containment check, so it
  matches an IPv4 prefix like `10.0.0.0/8` as expected, and before formatting, so the bucket
  key is the dotted-quad.
- **Zoned IPv6** (`fe80::1%eth0`): `netip.Prefix.Contains` always reports false for a zoned
  address, so the zone is stripped with `.WithZone("")` before both the containment check and
  the returned key.

`X-Real-IP` is **not** read. It carries a single value with no chain, so it cannot
express "rightmost untrusted hop" and would only add a second, weaker code path.

## File changes

### `internal/http/clientip.go` (new)

```go
package http

import ("fmt"; "net"; "net/http"; "net/netip"; "strings")
```

- `type proxySet []netip.Prefix` — the parsed `TRUSTED_PROXY` value. `nil` means "trust
  nothing; never read the header".
- `func parseTrustedProxies(s string) (proxySet, error)` — split on `,`, `TrimSpace`, skip
  empty elements, `netip.ParsePrefix` each. On error return
  `fmt.Errorf("trusted proxy %q: %w", elem, err)`. Returns `nil, nil` for an empty/whitespace
  input.
- `func (t proxySet) contains(a netip.Addr) bool` — any-prefix containment.
- `func (t proxySet) clientIP(r *http.Request) string` — the algorithm in Decision 3.
- `func remoteHost(r *http.Request) string` — the current `clientIP` body verbatim
  (`net.SplitHostPort`, error → `r.RemoteAddr`).
- Comment density: this file encodes an external-protocol fact (XFF ordering and why the walk
  is right-to-left, why the header is unread when unset), so it earns comments — a short
  doc comment on `clientIP` covering the trust gate and the fallbacks.

### `internal/http/auth.go`

- Delete `clientIP` (lines 114-120) and drop the now-orphaned `"net"` import.
- Signature: `func registerAuthRoutes(mux *http.ServeMux, a *auth.Service, pages
  map[string]*template.Template, trusted proxySet)`.
- Line 30: `a.Signup(r.Context(), trusted.clientIP(r), email, password, displayName)`.
- Line 69: `a.Login(r.Context(), trusted.clientIP(r), email, password)`.
- Nothing else in the file changes.

### `internal/http/http.go`

- Signature: `func NewHandler(pool *pgxpool.Pool, a *auth.Service, blobs *media.Store,
  trustedProxy string) (http.Handler, error)`.
- Before building the mux:
  ```go
  trusted, err := parseTrustedProxies(trustedProxy)
  if err != nil { return nil, fmt.Errorf("parse TRUSTED_PROXY: %w", err) }
  ```
- Line 33 becomes `registerAuthRoutes(mux, a, pages, trusted)`.
- Extend the `NewHandler` doc comment with one sentence naming `trustedProxy` as the raw
  `TRUSTED_PROXY` env value and that empty means `RemoteAddr` only.

### `cmd/deckshare/main.go`

- Line 74: `apphttp.NewHandler(pool, authSvc, blobs, os.Getenv("TRUSTED_PROXY"))`. No new
  variable, no validation here — `NewHandler` already returns a wrapped error and `run()`
  already wraps it as `build handler: %w`, so a bad CIDR is a startup failure with the offending
  element in the message.

### `internal/http/auth_test.go`

- Line 104: `registerAuthRoutes(mux, a, pages, nil)` — every existing test keeps today's
  `RemoteAddr`-only behaviour, which is also the assertion that the default is unchanged.

### `internal/http/clientip_test.go` (new)

No DB, no `DATABASE_URL` skip. See "Tests".

### `.env.example`

Append a commented block after the `ORIGIN` block, in the same explanatory voice:

```
# Comma-separated CIDRs of reverse proxies whose X-Forwarded-For header this server may trust
# (#211). Unset (the default) means the rate-limit key is r.RemoteAddr only -- correct when the
# app is exposed directly, but behind a TLS-terminating proxy every request then looks like it
# came from the proxy, collapsing the per-IP login/signup limiters into one instance-wide
# bucket. Set this to the proxy's address range and the client IP is taken from the rightmost
# X-Forwarded-For hop that is NOT one of these ranges. Only ever set it to ranges you control:
# a too-wide value lets clients choose their own rate-limit key.
# TRUSTED_PROXY="10.0.0.0/8,172.16.0.0/12,fd00::/8"
```

### `CHANGELOG.md`

New `## [0.3.11] - <merge date>` entry above `0.3.10`, `### Fixed`:

```
- The login and signup per-IP rate limiters no longer collapse into one instance-wide bucket
  behind a reverse proxy: set `TRUSTED_PROXY` to the proxy's CIDRs and the limiter key comes
  from the rightmost untrusted `X-Forwarded-For` hop. Unset (the default) is unchanged —
  `X-Forwarded-For` is never read ([#211](https://github.com/Jolls/deckshare/issues/211)).
```

Tag `v0.3.11` after the version-bump commit (CLAUDE.md §14).

## Tests

### `TestClientIP` — table over `(peer, TRUSTED_PROXY, X-Forwarded-For) -> key`

Each case builds `httptest.NewRequest("POST", "/login", nil)`, sets `r.RemoteAddr`, sets header
values with `r.Header.Add` (Add, not Set, so the multi-line case is expressible), parses the
CIDR string with `parseTrustedProxies`, and compares `trusted.clientIP(r)` to `want`.

Fields: `name, remoteAddr, trustedProxy string`, `xff []string`, `want string`.

| # | remoteAddr | trustedProxy | xff | want | asserts |
|---|---|---|---|---|---|
| 1 | `203.0.113.5:5555` | `""` | — | `203.0.113.5` | today's behaviour, default path |
| 2 | `203.0.113.5:5555` | `""` | `1.2.3.4` | `203.0.113.5` | header never read when unset |
| 3 | `203.0.113.5:5555` | `10.0.0.0/8` | `1.2.3.4` | `203.0.113.5` | spoofed header from untrusted peer ignored |
| 4 | `10.0.0.5:5555` | `10.0.0.0/8` | — | `10.0.0.5` | trusted peer, header absent |
| 5 | `10.0.0.5:5555` | `10.0.0.0/8` | `203.0.113.9` | `203.0.113.9` | trusted single hop |
| 6 | `10.0.0.5:5555` | `10.0.0.0/8` | `203.0.113.9, 10.0.0.6` | `203.0.113.9` | inner trusted hop skipped |
| 7 | `10.0.0.5:5555` | `10.0.0.0/8,172.16.0.0/12` | `203.0.113.9, 172.16.4.4, 10.0.0.6` | `203.0.113.9` | chain across two trusted CIDRs |
| 8 | `10.0.0.5:5555` | `10.0.0.0/8` | `1.2.3.4, 203.0.113.9` | `203.0.113.9` | client-forged left hop not reached |
| 9 | `10.0.0.5:5555` | `10.0.0.0/8` | `10.0.0.6, 10.0.0.7` | `10.0.0.5` | all hops trusted → fall back to peer |
| 10 | `10.0.0.5:5555` | `10.0.0.0/8` | `203.0.113.9, not-an-ip` | `10.0.0.5` | malformed rightmost → fall back to peer |
| 11 | `10.0.0.5:5555` | `10.0.0.0/8` | `junk, 203.0.113.9` | `203.0.113.9` | malformed left hop never reached |
| 12 | `10.0.0.5:5555` | `10.0.0.0/8` | `203.0.113.9` **and** `10.0.0.6` as two header lines | `203.0.113.9` | `Header.Values` joined, not just the first line |
| 13 | `10.0.0.5:5555` | `10.0.0.0/8` | `"  203.0.113.9  "` | `203.0.113.9` | whitespace trimmed |
| 14 | `10.0.0.5:5555` | `10.0.0.0/8` | `::ffff:203.0.113.9` | `203.0.113.9` | IPv4-mapped hop canonicalised |
| 15 | `10.0.0.5:5555` | `10.0.0.0/8` | `203.0.113.9, ::ffff:10.0.0.6` | `203.0.113.9` | IPv4-mapped inner hop matches the IPv4 trusted CIDR after `Unmap` and is skipped |
| 16 | `[2001:db8::1]:443` | `2001:db8::/32` | `2606:4700::1234` | `2606:4700::1234` | IPv6 peer + IPv6 CIDR |
| 17 | `10.0.0.5:5555` | `10.0.0.0/8` | `fe80::1%eth0` | `fe80::1` | zone stripped from the returned key |
| 18 | `unixsocket` (no port) | `10.0.0.0/8` | `1.2.3.4` | `unixsocket` | unparseable peer → verbatim, header ignored |
| 19 | `10.0.0.5:5555` | `10.0.0.0/8` | `` (present but empty value) | `10.0.0.5` | empty header value → peer |

### `TestParseTrustedProxies`

- `""` → `nil`, no error.
- `"   "` → `nil`, no error.
- `"10.0.0.0/8"` → 1 prefix.
- `"10.0.0.0/8, 172.16.0.0/12 ,fd00::/8"` → 3 prefixes (whitespace tolerated).
- `"10.0.0.0/8,"` → 1 prefix (empty element skipped).
- `"10.0.0.1"` → error (bare IP rejected).
- `"garbage"` → error.
- `"10.0.0.0/33"` → error.
- Error cases assert only that `err != nil` and that the message contains the offending element.

### `TestSignupRateLimit_PerForwardedClient` (DB-backed, `internal/http/auth_test.go`)

The test that proves the actual bug is fixed — distinct clients behind one trusted proxy get
distinct buckets. Build a minimal stack rather than extending `newTestHandler`:

```go
tx := beginTx(t)
a, _ := auth.New(tx, auth.Config{})
pages, _ := parseTemplates()
trusted, _ := parseTrustedProxies("10.0.0.0/8")
mux := http.NewServeMux()
registerAuthRoutes(mux, a, pages, trusted)
handler := a.Middleware(mux)
```

Then issue 6 `POST /signup` requests, each with `r.RemoteAddr = "10.0.0.5:5555"`,
`Origin: http://example.com`, `Host: example.com`, a unique `testEmail()`, and
`X-Forwarded-For` set to a distinct public IP per request (`203.0.113.1` … `203.0.113.6`).
Assert every response is 303, not 429 — with `signupIPLimit` at 5, a 429 on the sixth means the
buckets are still collapsed. Contrast with the existing `TestSignupRateLimited`, which keeps
asserting the 429 for the shared-`RemoteAddr` case.

## Verification

1. `go build ./...`, `go vet ./...`, `golangci-lint run`.
2. `$env:DATABASE_URL=…; go test ./internal/http/... -run 'ClientIP|TrustedProxies|RateLimit' -v`
   — confirm nothing reports `SKIP` for the DB-backed case (CLAUDE.md §16).
3. `go test ./...` with `DATABASE_URL` exported.
4. Manual (for the user, not run by the agent): start with `TRUSTED_PROXY` unset and confirm
   login/signup still work; set `TRUSTED_PROXY="127.0.0.0/8"` and confirm a request with
   `X-Forwarded-For: 203.0.113.9` from localhost is limited on its own bucket; set
   `TRUSTED_PROXY="10.0.0.0/33"` and confirm the process refuses to start with the offending
   element named.

## Resolved decisions

1. **Header: `X-Forwarded-For` only.** Confirmed — matches the issue's spec. No `Forwarded`
   (RFC 7239) support in this pass; additive later if a proxy needs it.
2. **Scope: rate-limit key only.** `TRUSTED_PROXY` does not gate request logging,
   `X-Forwarded-Proto`, or `X-Forwarded-Host`. Broadening it is a separate issue.
3. **Env var name: `TRUSTED_PROXY` (singular), final.** Used verbatim as specified; not
   renamed to `TRUSTED_PROXIES`.
