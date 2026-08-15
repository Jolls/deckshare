# Plan: #57 — CSP + card-content style/media policy for the reviewer

Phase 1, step 7 hardening (architecture.md §11). Card content is sanitised HTML that, under a shared deck, belongs to another user (architecture.md §8, §20). `internal/render/` is the allowlist; this issue adds the browser-enforced bound behind it.

## 0. Resolved decisions

### 0.1 Scope: one global middleware, one policy string — not per-route

`internal/http/http.go` wraps the mux in `a.Middleware(mux)` and nothing else. The CSP goes in a new `securityHeaders` middleware wrapping **outside** that:

```go
return securityHeaders(a.Middleware(mux)), nil
```

Three reasons this beats a reviewer-only header:

1. architecture.md §12 already states the repo's rule for this class of concern: *"CSRF and session population are enforced once, centrally, wrapping every request before it reaches a handler — never as a per-handler concern a new route could ship without by forgetting to call it."* A per-route CSP is precisely the thing route #14 forgets.
2. Outermost placement is what puts the header on the CSRF `403` from `auth.Service.Middleware` and on `404`/`500` error bodies, not only on handler responses.
3. Card HTML renders only on the reviewer today (`internal/review/batch.go` → `internal/http/review.go` is the sole `render.RenderCard` call site), but routes.md's Open question 2 leaves a note/note-type preview route open and #60 adds `/media/{sha256}`. A global policy needs no edit when those land.

The cost is that `'unsafe-eval'` (§0.3) applies to pages that don't need it. That is accepted: those pages render no untrusted content, so there is nothing on them for `'unsafe-eval'` to escalate. A second, stricter non-reviewer variant would double the number of header strings that can be wrong, against Simplicity First (CLAUDE.md rule 2).

### 0.2 The exact policy

```
default-src 'none'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'
```

Header name: `Content-Security-Policy`. Enforcing, not report-only (§0.7).

`default-src 'none'` rather than `'self'`, so every capability is named. It is what blocks `font-src`, `media-src`, `worker-src`, `frame-src`, `manifest-src` without listing them — and all four are correct to block here: `sanitisableElements` in `internal/render/sanitise.go` contains no `audio`/`video`/`iframe`/`object`, and `SanitiseCSS` drops every at-rule, so `@font-face` cannot exist in note-type CSS. **`object-src 'none'` is deliberately omitted**: it falls back to `default-src`, so writing it adds a directive that can drift without adding a constraint.

`frame-ancestors`, `form-action` and `base-uri` do **not** fall back to `default-src` and must be named — that is why those three appear and `object-src` does not.

### 0.3 `script-src 'self' 'unsafe-eval'` — and why the `'unsafe-eval'` is currently forced

Verified against the repo: **no template contains an inline `<script>`, an inline event handler, or a `javascript:` URL.** Every script is a `src` under `/static/` (`web/templates/layout.html:8-9`, `web/templates/review.html:42`). So `'self'` alone covers loading.

`'unsafe-eval'` is forced by the vendored htmx 2.0.10. `web/templates/review.html` uses on two elements:

```
hx-vals='js:{deck: enshuReview.deckId(), cursor: enshuReview.cursor()}'    # line 26
hx-vals='js:{events: enshuReview.takePending()}'                            # line 34
```

htmx evaluates the `js:` prefix through the `Function` constructor — confirmed in `web/static/htmx.min.js`:

```js
Function("event","return ("+e+")").call(r,s) : Function("return ("+e+")").call(r)
```

`Function()` is governed by `script-src`'s `'unsafe-eval'` exactly as `eval()` is. Without it htmx raises `htmx:evalDisallowedError`, both `hx-vals` resolve to `{}`, and **the reviewer's refill and grade-send both stop working**. There is no `'unsafe-eval'`-free CSP keyword that permits `Function` alone.

This is the plan's one genuinely contested value — see Open question 1 for the alternative and why it is not bundled here.

What `script-src 'self' 'unsafe-eval'` still buys, which is the point of the issue: injected inline `<script>` blocks, remote script origins, `javascript:` URLs, and inline event handlers (`onerror=`, `onload=`) are all refused. `'unsafe-eval'` only assists an attacker who *already* has script execution — which the rest of the directive is what denies.

### 0.4 `style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net` — accept `'unsafe-inline'`

**Recommendation: (a), accept `'unsafe-inline'`.** It is not merely pragmatic here, it is forced, and a nonce would make things strictly worse:

- `internal/render/sanitise.go:57` is `p.AllowAttrs("style").OnElements(sanitisableElements...)`. Sanitised card HTML therefore carries **inline `style=""` attributes on arbitrary elements**. An attribute cannot take a nonce — nonces apply to `<style>` and `<link>` elements only.
- CSP2+ specifies that **a nonce or hash source in `style-src` causes `'unsafe-inline'` to be ignored**. So nonceing the note-type `<style>` blocks in `web/templates/review.html:2` would not tighten anything; it would drop every card's inline styles while leaving the note-type CSS working. Strictly a regression.
- `'unsafe-hashes'` is not viable either: card inline styles are author-supplied and unbounded, so there is no finite hash set.
- Our own templates already use inline styles independently of card content (`web/templates/deck.html:25`, `web/templates/notetypes.html:18`: `style="display:inline"`), and htmx injects its `.htmx-indicator` rules via `head.insertAdjacentHTML("beforeend", "<style>…")`.

The severity trade is favourable and worth stating in the code comment: what `'unsafe-inline'` in `style-src` permits is CSS injection, which is *already* bounded by the property/value allowlist in `internal/render/css.go` (no `position`/`z-index`/`transform` → no overlay clickjacking; no bare `(` → no `url()`/`expression()`/`var()`; no URL-bearing properties). What CSP is actually being bought for — script injection, the mXSS vector architecture.md §8 is written against — is fully blocked by §0.3.

`https://cdn.jsdelivr.net` is required by `web/templates/layout.html:7`, which loads Pico CSS from jsDelivr. `web/static/README.md` records that this inconsistency (htmx vendored, Pico not) is deliberate and unaddressed. **Do not vendor Pico in this PR** — that is a separate change. Leave a comment in `security.go` saying this source is deletable the day Pico moves under `/static/`, and assert it in a test (§4.2) so the removal is prompted.

`'self'` is included so a future vendored stylesheet under `/static/` works without a CSP edit; `/static/` is already a live route.

### 0.5 `img-src 'self' data:` — card media is same-origin only

`'self'` is the media origin policy and the second half of what the issue asks for.

`internal/render/sanitise.go` currently permits both shapes on `<img src>`:
- **Relative** (`AllowRelativeURLs(true)`, line 53) — Anki's `<img src="x.jpg">` convention. Resolves same-origin. This is the intended path, and `/media/{sha256}` (routes.md, #60) is where it lands.
- **Absolute `http`/`https`** (`AllowURLSchemes`, line 51) — technically permitted today.

`img-src 'self'` refuses the second. The reason is specific to multiuser, not generic hardening: a shared deck's `<img src="https://attacker.example/px.jpg">` is a **tracking beacon that hands a third party of the deck author's choosing every reviewer's IP address, User-Agent and per-card review timing.** In Anki the card content is always your own, so the question never arises; under `deck_access` it is someone else's. That traces directly to the content/progress seam, so it is a *forced* deviation under CLAUDE.md §2.10 and gets a row in architecture.md §20 (§5.2).

`data:` is required by Pico CSS itself, which uses `url("data:image/svg+xml,…")` for form-control icons (select arrows, checkboxes, `<details>` markers). It is safe: an SVG loaded through `<img>`/`background-image` is script-disabled by spec, and `AllowURLSchemes("http","https","mailto")` keeps `data:` off card `<img src>` regardless — so this source only ever serves our own stylesheet.

**The sanitiser's scheme allowlist is NOT tightened in this PR.** See Open question 2 for the reasoning and the concrete follow-up.

### 0.6 The remaining directives

| Directive | Value | Why, specifically |
|---|---|---|
| `connect-src` | `'self'` | htmx's XHR to `/api/reviews/next` and `/api/reviews/batch`, plus `review.js:310`'s `navigator.sendBeacon('/api/reviews/batch', …)` — `sendBeacon` is governed by `connect-src`. Omitting this breaks grade delivery. |
| `form-action` | `'self'` | Every `<form>` in `web/templates/` posts same-origin. `form` is not in `sanitisableElements`, so this is defense in depth against an injected form exfiltrating a submitted `{{type:Field}}` answer. |
| `frame-ancestors` | `'none'` | `internal/render/css.go:21-27` names clickjacking against the rating buttons as the threat that keeps `position`/`z-index`/`transform` off the CSS allowlist. That comment closes the *inner* half; this closes the outer half — a hostile page framing `/decks/{id}/review` and overlaying "Easy". Directly in scope, not a general-headers sweep. **`X-Frame-Options` is deliberately not also set** — `frame-ancestors` supersedes it in every browser this app targets, and a second header is a second thing to drift. |
| `base-uri` | `'none'` | An injected `<base href="https://attacker.example/">` repoints *every relative URL on the page at once* — which matters here specifically because card media is relative by convention (§0.5). `<base>` is not in `sanitisableElements`, so this is the belt to that braces. `'none'` over `'self'` because the app never uses `<base>`. |

Not set, on purpose: `object-src` (redundant under `default-src 'none'`), `font-src` (ditto; Pico ships no webfont and `SanitiseCSS` drops `@font-face`), `upgrade-insecure-requests` (would fight local `http://` dev), `report-uri`/`report-to` (no reporting endpoint exists), and any non-CSP header (`X-Content-Type-Options`, `Referrer-Policy`, HSTS) — a general security-headers sweep is a different issue.

### 0.7 Enforcing from the first commit, not report-only

Report-only is a migration tool for a policy you cannot fully predict against traffic you already have. Neither applies: there are zero users (architecture.md §1), the policy above was derived from an exhaustive read of all 13 templates and both vendored JS files, and **no report collection endpoint exists** — so `Content-Security-Policy-Report-Only` would emit a header nobody ever reads and defer the protection indefinitely. Ship enforcing.

## 1. Files

| File | Change |
|---|---|
| `internal/http/security.go` | **new** — `contentSecurityPolicy` const + `securityHeaders` middleware |
| `internal/http/http.go` | modified — line 36 wrap; `NewHandler` doc comment |
| `internal/http/auth_test.go` | modified — line 106 wrap in `newTestHandler`; helper doc comment |
| `internal/http/security_test.go` | **new** — four tests (§4) |
| `docs/architecture.md` | modified — §1 paragraph, §20 row |
| `docs/routes.md` | modified — one Conventions bullet |
| `CHANGELOG.md` | modified — `## [0.1.11]` with `### Security` |

**No changes to `internal/render/`, `web/templates/`, or `web/static/`.** That is the whole point of the scoping in §0.3 and Open question 2 — this PR adds a header and its tests, nothing else.

## 2. `internal/http/security.go` (new)

```go
package http

import "net/http"

// contentSecurityPolicy bounds what sanitised card HTML -- which under a shared deck is another
// user's HTML (architecture.md §8, §20) -- can do once it reaches the browser. Defence in depth
// behind internal/render's allowlist, never a replacement for it.
//
// Every source below traces to something concrete in this repo. Directive by directive:
//
//	default-src 'none'      Deny by default, then name each capability. This is what blocks
//	                        font-src/media-src/worker-src/frame-src/manifest-src without listing
//	                        them, and all four are correct to block: sanitisableElements has no
//	                        audio/video/iframe/object, and SanitiseCSS drops every at-rule, so
//	                        @font-face cannot exist. object-src is not repeated -- it falls back
//	                        here, so writing it would add drift, not constraint. frame-ancestors,
//	                        form-action and base-uri do NOT fall back, which is why they appear.
//	script-src 'self'       All JS is served from /static/ (web/templates/layout.html,
//	                        review.html). No template has an inline <script>, an inline event
//	                        handler, or a javascript: URL.
//	          'unsafe-eval' Forced by htmx 2.0.10: hx-vals="js:{...}" on #review-refill and
//	                        #review-sender (web/templates/review.html) is evaluated through the
//	                        Function constructor, which 'unsafe-eval' governs. Without it htmx
//	                        raises htmx:evalDisallowedError and both refill and grade-send break.
//	                        This is the one source we want gone and cannot remove yet --
//	                        docs/plans/57-csp-reviewer.md, Open question 1.
//	style-src 'self'        For a stylesheet vendored under /static/ later.
//	          'unsafe-inline'  Forced, and a nonce would be strictly worse. Sanitised card HTML
//	                        carries inline style="" attributes on arbitrary elements
//	                        (sanitise.go's AllowAttrs("style")), and an attribute cannot take a
//	                        nonce -- while a nonce anywhere in style-src makes CSP ignore
//	                        'unsafe-inline' entirely. Nonceing review.html's note-type <style>
//	                        blocks would therefore kill every card's inline styles and tighten
//	                        nothing. What this permits is CSS injection, already bounded by the
//	                        property/value allowlist in internal/render/css.go; what CSP is here
//	                        for -- script injection -- is fully blocked by script-src above.
//	          https://cdn.jsdelivr.net  web/templates/layout.html loads Pico CSS from jsDelivr
//	                        (web/static/README.md records why Pico alone is not vendored).
//	                        Delete this source the day Pico moves under /static/.
//	img-src 'self'          The card-media origin policy. Card images resolve same-origin only:
//	                        Anki's relative-filename convention today (AllowRelativeURLs(true)),
//	                        /media/{sha256} once #60 lands. Remote origins are refused even
//	                        though sanitise.go still permits http/https on <img src> -- a shared
//	                        deck's <img src="https://attacker.example/px.jpg"> is a tracking
//	                        beacon handing a third party every reviewer's IP, UA and review
//	                        timing. Forced by multiuser; architecture.md §20 carries the row.
//	        data:           Pico's own form-control icons are data:image/svg+xml URIs. Safe: an
//	                        SVG loaded through <img>/background-image is script-disabled by spec,
//	                        and sanitise.go's scheme allowlist keeps data: off card <img src>.
//	connect-src 'self'      htmx's XHR to /api/reviews/{next,batch} and review.js's
//	                        navigator.sendBeacon to /api/reviews/batch (sendBeacon is governed by
//	                        connect-src -- omitting this silently breaks grade delivery).
//	form-action 'self'      Every form in web/templates/ posts same-origin. <form> is not in
//	                        sanitisableElements, so this guards against an injected form
//	                        exfiltrating a submitted {{type:Field}} answer.
//	frame-ancestors 'none'  internal/render/css.go names clickjacking against the rating buttons
//	                        as the reason position/z-index/transform are off the CSS allowlist;
//	                        that closes the inner half, this closes the outer. X-Frame-Options is
//	                        deliberately NOT also set -- frame-ancestors supersedes it.
//	base-uri 'none'         A <base> repoints every relative URL on the page at once, which
//	                        matters here precisely because card media is relative.
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

// securityHeaders sets the CSP on every response. It wraps OUTSIDE auth.Service.Middleware so the
// header is present on the CSRF 403 and on 404/500 error bodies too, not only on handler
// responses -- setting it before the inner handler runs is what guarantees it precedes any
// WriteHeader call below it.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}
```

Comment density is high for this package (CLAUDE.md §9 says handlers don't earn comments) — justified because every source here encodes a fact about a *different* file, and the whole failure mode of a CSP is someone loosening a source without knowing what it was holding.

## 3. Wiring

### 3.1 `internal/http/http.go`

Replace line 36:

```go
	return securityHeaders(a.Middleware(mux)), nil
```

And extend the `NewHandler` doc comment (lines 14-16) to:

```go
// NewHandler builds the application's top-level handler: the route mux wrapped in the auth
// middleware, so the CSRF check and session population run for every request
// (architecture.md §12), wrapped in turn by securityHeaders so the CSP is set on every
// response including the ones auth rejects (security.go).
```

### 3.2 `internal/http/auth_test.go`

`newTestHandler` mirrors `NewHandler`'s stack by hand, so the tests only see the header if it is added there too. Replace line 106:

```go
	return securityHeaders(a.Middleware(mux)), a
```

and append to that helper's doc comment: `The securityHeaders wrap mirrors NewHandler's, so route tests exercise the same header stack production serves.`

No existing test asserts the absence of any header, so this is additive.

## 4. Tests

New file `internal/http/security_test.go`. Per CLAUDE.md §10 this is `area: security` work; §10.5's table-driven pattern is followed, and `TestRoutes_NoSession` in `auth_test.go:138` is the shape being matched.

Two of the four tests are **pure** — no `DATABASE_URL`, so they run in CI unconditionally, unlike every other test in this package which `t.Skip`s without a database. That matters: the policy string is the artifact most worth guarding and it must not be guarded only by tests CI skips.

Shared helper:

```go
func parseCSP(t *testing.T, policy string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, d := range strings.Split(policy, ";") {
		fields := strings.Fields(d)
		if len(fields) == 0 {
			continue
		}
		if _, dup := out[fields[0]]; dup {
			t.Fatalf("directive %q appears twice; browsers honour only the first occurrence", fields[0])
		}
		out[fields[0]] = fields[1:]
	}
	return out
}
```

### 4.1 `TestContentSecurityPolicy_Directives` (pure)

Table-driven, one row per directive, asserting the exact source list with `slices.Equal` plus a `why` string that is printed on failure — so a failing assertion tells the next person what the source was holding rather than just diffing strings.

```go
tests := []struct{ directive string; want []string; why string }{
	{"default-src", []string{"'none'"}, "deny by default: font/media/worker/frame/object all fall back here"},
	{"script-src", []string{"'self'", "'unsafe-eval'"}, "all JS is served from /static/; 'unsafe-eval' is htmx's hx-vals js: Function() -- plan Open question 1"},
	{"style-src", []string{"'self'", "'unsafe-inline'", "https://cdn.jsdelivr.net"}, `card HTML carries inline style="" attributes that cannot take a nonce; layout.html loads Pico from jsDelivr`},
	{"img-src", []string{"'self'", "data:"}, "card media is same-origin only; data: is Pico's own form-control icons"},
	{"connect-src", []string{"'self'"}, "htmx XHR + review.js's navigator.sendBeacon to /api/reviews/*"},
	{"form-action", []string{"'self'"}, "every form in web/templates/ posts same-origin"},
	{"frame-ancestors", []string{"'none'"}, "clickjacking against the rating buttons -- internal/render/css.go"},
	{"base-uri", []string{"'none'"}, "a <base> would repoint every relative card-media URL at once"},
}
```

Close with a completeness assertion, so adding a directive without a row fails:

```go
if len(got) != len(tests) {
	t.Errorf("policy has %d directives, table covers %d -- add a row for the new one", len(got), len(tests))
}
```

### 4.2 `TestContentSecurityPolicy_NoLoosening` (pure)

The intent test. 4.1 pins the string; this pins the *properties*, so a widening edit trips a subtest that names what it broke.

- `script-src admits no inline script and no remote origin` — every source must be `'self'` or `'unsafe-eval'`; anything else fails with *"sanitisation already strips every script vector; CSP must not be the thing that lets one back in."*
- `img-src admits no external origin` — every source must be `'self'` or `data:`. **This is the card-media origin-policy regression test the issue asks for.** Comment on it that `internal/render` still permits `http`/`https` on `<img src>` because bluemonday's scheme allowlist is policy-global (verified: `urlPolicy` is `func(*url.URL) bool`, with no element context — `bluemonday@v1.0.27/policy.go:206`), so this directive is what actually refuses the load.
- `style-src names exactly one external origin` — filter sources not starting with `'`; assert `["https://cdn.jsdelivr.net"]`, failing with *"drop this when Pico is vendored under /static/"*. This is what prompts the cleanup rather than leaving a stale CDN allowance forever.
- `no wildcard anywhere` — no source in any directive contains `*`.

### 4.3 `TestSecurityHeaders_OnEveryResponse` (DB-backed)

Table-driven over routes chosen to cover each response path, asserting `w.Header().Get("Content-Security-Policy") == contentSecurityPolicy`:

| name | method | path | cookie | origin | status |
|---|---|---|---|---|---|
| public page | GET | `/login` | nil | "" | 200 |
| redirect | GET | `/` | nil | "" | 303 |
| static asset | GET | `/static/htmx.min.js` | nil | "" | 200 |
| deck list | GET | `/decks` | ✓ | "" | 200 |
| reviewer page | GET | `/decks/{deckID}/review` | ✓ | "" | 200 |
| refill fragment | GET | `/api/reviews/next?deck={deckID}&cursor=` | ✓ | "" | 200 |
| not found | GET | `/decks/00000000-0000-0000-0000-000000000000/review` | ✓ | "" | 404 |
| CSRF rejection | POST | `/decks` (`name=X`) | ✓ | `http://evil.example` | 403 |

Setup: `tx := beginTx(t)`, `handler, a := newTestHandler(t, tx, auth.Config{})`, `cookie := loginCookie(...)`, `deckID, _ := setupOneCard(t, tx, handler, cookie)` (existing helper, `review_test.go:36`). Use the existing `doRequest`. Empty `cursor=` is valid — `review.DecodeCursor("")` returns `Cursor{AtStart: true}` (`internal/review/types.go:91`).

**The CSRF-rejection row is the load-bearing one**: it passes only if `securityHeaders` sits outside `auth.Service.Middleware`. Comment it as such, or a future reorder silently drops the header from every rejected request.

### 4.4 `TestReviewPage_StyleSrcUnsafeInlineIsLoadBearing` (DB-backed)

Documents *why* §0.4 chose `'unsafe-inline'`, so anyone who tries to remove it sees what will stop rendering instead of discovering it in a browser.

1. Create a note type with non-empty CSS — build `url.Values` inline rather than editing `newNoteTypeBody` (`notetypes_test.go:12`), keeping the change surgical: `name=Basic`, `css=.card { color: red; }`, `field_name[]=Front`, `field_name[]=Back`, `template_name[]=Card 1`, `qfmt[]={{Front}}`, `afmt[]={{FrontSide}}<hr>{{Back}}`.
2. Create a deck, then a note whose `Front` field is `<span style="color: blue">Q</span>`.
3. `GET /decks/{id}/review`, assert 200, then assert on the body:
   - contains `<style>` and `color: red` — the note-type CSS block from `review.html:2`, which is why a `style-src` nonce cannot be added without breaking the next assertion;
   - matches `regexp.MustCompile(`style="[^"]*color[^"]*blue`)` — an inline `style` attribute surviving `sanitiseCardHTML` onto card markup, which is why `'unsafe-inline'` cannot be dropped. Use a regex, not an exact string: bluemonday's whitespace normalisation of the re-emitted attribute is not contractual.

Both assertions fail loudly with a message pointing at `security.go`'s `style-src` comment.

## 5. Documentation

### 5.1 `docs/architecture.md` §1 — new paragraph after the #56 paragraph

> **Build order step 7's security layer has landed** ([#57](https://github.com/Jolls/enshu/issues/57)): a global `Content-Security-Policy` set by `internal/http/security.go`'s `securityHeaders` middleware, wrapping outside `auth.Service.Middleware` so it covers rejected requests too. It is the browser-enforced bound behind §8's sanitisation, not a replacement for it: `script-src` refuses inline and remote script outright, `img-src 'self'` refuses remote card images (§20), and `frame-ancestors 'none'` closes the outer half of the clickjacking threat `internal/render/css.go`'s property allowlist closes from the inside. Two sources are concessions with recorded expiry conditions — `'unsafe-eval'`, forced by htmx's `hx-vals="js:…"` on the reviewer's two network-touching elements, and `https://cdn.jsdelivr.net`, forced by `layout.html`'s un-vendored Pico CSS. `style-src 'unsafe-inline'` is not a concession but a structural fact: sanitised card HTML carries inline `style=""` attributes, which cannot take a nonce, and a nonce in `style-src` makes CSP ignore `'unsafe-inline'` entirely. See [docs/plans/57-csp-reviewer.md](plans/57-csp-reviewer.md).

### 5.2 `docs/architecture.md` §20 — new row under "Forced by multiuser"

| | Anki | Enshu |
|---|---|---|
| Remote card media | The webview loads whatever the card HTML references, including images on remote hosts | `img-src 'self'` refuses them (`internal/http/security.go`). Card content is always your own in Anki; under `deck_access` it is someone else's, and a remote `<img>` is a beacon reporting every reviewer's IP, UA and review timing to a host the deck author picked. Relative Anki filenames and `/media/{sha256}` (#60) are unaffected |

### 5.3 `docs/routes.md` — one bullet in Conventions

> - **Response headers are global, not per-route.** `internal/http/security.go`'s `securityHeaders` middleware sets the `Content-Security-Policy` on every response, wrapping outside the auth middleware so rejected requests carry it too. A new route inherits it with no action; a route that needs a *different* policy does not exist and should be discussed before one is added.

### 5.4 `CHANGELOG.md`

New entry above `## [0.1.10]`, dated the merge date:

```
## [0.1.11] - YYYY-MM-DD

### Security
- A global `Content-Security-Policy` (`internal/http/security.go`), set by a middleware wrapping
  outside the auth middleware so it covers CSRF rejections and error responses too. Defence in
  depth behind `internal/render`'s sanitisation for card content that, under a shared deck,
  belongs to another user: `script-src` refuses inline and remote script, `img-src 'self'`
  refuses remote card images (a tracking beacon in the multiuser model — architecture.md §20),
  `frame-ancestors 'none'` refuses framing, and `base-uri 'none'` refuses a `<base>` rewrite of
  relative card media ([#57](https://github.com/Jolls/enshu/issues/57))
```

Then `git tag v0.1.11` per CLAUDE.md §14.

## 6. Implementation order

1. `internal/http/security.go`.
2. Wire `internal/http/http.go` and `internal/http/auth_test.go`.
3. `internal/http/security_test.go` §4.1 + §4.2 (pure — must pass without a database).
4. `internal/http/security_test.go` §4.3 + §4.4 (need `DATABASE_URL`).
5. `go build ./... && go vet ./... && golangci-lint run && go test ./...` (CLAUDE.md §14).
6. Docs (§5), then `CHANGELOG.md`.

**Manual verification for the user** (CLAUDE.md §14 — do not start a dev server as a verification step): with the app running, open `/decks/{id}/review`, open DevTools → Console, and confirm **zero** `Refused to …` CSP violations while: the page renders with Pico styling and note-type CSS applied; grading advances; a refill fires (grade past the 10-unseen threshold); and the grade batch POSTs successfully on the Network tab. Then repeat on `/decks`, `/note-types`, `/settings` and `/login`. Any violation names the exact directive to revisit — most likely `font-src` (if Pico ever gains a webfont) or `img-src` (a card referencing a remote image, which is the intended refusal, not a bug).

## 7. What this plan deliberately does not do

- **Does not vendor Pico CSS.** `web/static/README.md` records the CDN load as a known, deliberate inconsistency. Vendoring it is a separate change that happens to shorten `style-src`; §4.2 is what prompts it.
- **Does not touch `internal/render/`.** No sanitiser behaviour change — see Open question 2.
- **Does not touch the reviewer's client code.** See Open question 1.
- **Does not add non-CSP security headers.** `X-Content-Type-Options`, `Referrer-Policy`, HSTS, `Permissions-Policy` are a general-hardening issue, not "card-content style/media policy for the reviewer". `X-Frame-Options` specifically is omitted because `frame-ancestors` already covers it and two headers is two things to drift.

## Open questions — resolved

1. **`'unsafe-eval'` in `script-src`: RESOLVED — accept it now.** Ship the CSP as scoped in §0.3; do not touch `review.js`/`layout.html`'s htmx wiring in this PR. Separately, file a follow-up issue (not part of this PR) to investigate whether `json-enc`'s memoization-free re-evaluation of `hx-vals="js:..."` on `#review-sender` calls `enshuReview.takePending()` twice per grade-send, with the second (empty) call silently dropping graded events — needs a browser check before filing, not assumed from the minified source alone.
2. **`internal/render/sanitise.go` `<img src>` tightening: RESOLVED — no, not in this PR.** Leave `sanitise.go` as-is; `img-src 'self'` is enforced at the CSP layer only, and a refused remote `<img>` stays visible (with its URL) in the browser console rather than being silently stripped. File a follow-up issue for `internal/render` to decide drop-vs-block explicitly, rather than bundling a sanitiser behaviour change here.
3. **`style-src 'self'`: RESOLVED — keep it.** Retained even though nothing is served from `/static/*.css` yet, since `/static/` is a live route and this avoids a confusing CSP failure the day a stylesheet is vendored there.

Original reasoning for each, kept for context:

1. **`'unsafe-eval'` in `script-src` — accept it now, or remove the `js:` dependency first?**

   The plan ships **with** `'unsafe-eval'` (§0.3). The alternative, if you want `script-src 'self'` clean, is fully specified so it can be swapped in — but it is a change to the reviewer's network plumbing, which #56 landed days ago and which carries §2.6/§2.7 correctness properties (batching, backoff, idempotency). Riding it inside a header-adding security PR is exactly the scope creep CLAUDE.md rule 3 warns about.

   The alternative, concretely: add `<meta name="htmx-config" content='{"allowEval":false}'>` to `layout.html`; replace `#review-refill`'s `hx-vals` with hidden `<input>`s plus `hx-include`, with `review.js` writing the cursor input in `indexBatch()`; and move `#review-sender` off htmx entirely to a direct `fetch()` in `review.js`'s `flush()`, reusing the existing `onAfterRequest` reconciliation and backoff. The sender cannot be converted to `htmx:configRequest` instead — htmx 2's `detail.parameters` is a `FormData`, which flattens an array of objects to `"[object Object]"` entries, and the `json-enc` extension recovers their types only by re-reading `hx-vals`.

   **Which is why this is worth deciding rather than defaulting:** that same re-read looks like a live bug in `#review-sender` today, independent of CSP. `json-enc`'s `encodeParameters` calls `api.getExpressionVars(elt)`, which re-evaluates `hx-vals` with no memoisation — so `enshuReview.takePending()` (`web/static/review.js:316`) runs **twice per request**, and its early `if (state.pending.length === 0) return []` makes the second call return `[]`. The resulting body appears to be `{"events":[[],…]}` rather than the graded events, which `parseBatchRequest` would reject as a 400, which `onAfterRequest` treats as permanently undeliverable and drops. This is inference from the minified `htmx.min.js` and `json-enc.js`, not an observed failure — **it needs a browser check before anything is filed** — but if it holds it is a grade-loss bug and it makes "remove the `js:` dependency" the fix rather than a refactor. Recommend verifying this before choosing, and filing it as its own issue either way.

2. **Should `internal/render/sanitise.go` tighten `<img src>` to relative-only, in step with `img-src 'self'`?**

   The plan says **no, not in this PR** — but it is a real call, so here is what it turns on. bluemonday cannot express a per-element scheme policy (`urlPolicy` is `func(*url.URL) bool`, no element context, verified in `bluemonday@v1.0.27/policy.go:206`), and dropping `http`/`https` from `AllowURLSchemes` globally would also kill legitimate external `<a href>` links, which are wanted. The workable form is a `Matching` regex on the `src` attribute only — `p.AllowAttrs("src").Matching(relativeOnlyRe).OnElements("img")` — which is small, but changes the shipped, golden-file-tested behaviour of a package #55 just landed, and reopens plan-55 Resolved decision 3.

   There is also a genuine argument for keeping them: CSP already refuses the load, and leaving the `<img>` in the markup makes the refusal *visible* in the console with the offending URL, whereas silently stripping it at sanitisation time hides that a deck was trying. Recommend a follow-up issue on `internal/render` that decides drop-vs-block explicitly, rather than bundling a silent behaviour change here.

3. **`style-src 'self'` — keep or drop?** Nothing serves a stylesheet from `/static/` today, so it is strictly speculative under CLAUDE.md rule 2. Kept because `/static/` is a live route and vendoring Pico is a plausible near-term change that would otherwise fail confusingly. Trivial to drop if you want the policy minimal to the byte.

### Critical Files for Implementation
- `c:\Users\JohnJolly\Local\git\enshu\internal\http\security.go` (new)
- `c:\Users\JohnJolly\Local\git\enshu\internal\http\http.go`
- `c:\Users\JohnJolly\Local\git\enshu\internal\http\security_test.go` (new)
- `c:\Users\JohnJolly\Local\git\enshu\internal\http\auth_test.go`
- `c:\Users\JohnJolly\Local\git\enshu\web\templates\layout.html` (read-only reference: the jsDelivr Pico load and the `/static/` script tags that fix `style-src`/`script-src`)
