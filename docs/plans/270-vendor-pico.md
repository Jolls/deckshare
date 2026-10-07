# Plan: #270 Vendor Pico CSS under /static and drop the CDN

Single PR, shares a branch with #268. No schema, no Go logic change beyond a CSP constant and comments.

## Findings (verified in current code)
- `web/templates/layout.html:8` is the only template reference to an external host. `layout.html` is the sole layout.
- `/static/` is served by `internal/http/static.go` via `fs.Sub(web.Static, "static")`; `web/static.go` uses `//go:embed static` (whole directory). Dropping `pico.min.css` into `web/static/` is picked up automatically.
- Pico's `data:image/svg+xml` form-control icons remain, so `img-src ... data:` stays untouched.
- CSP is checked in `internal/http/security_test.go`: `TestContentSecurityPolicy_Directives` (line 43 row), `TestContentSecurityPolicy_NoLoosening` (subtest lines 86-97), `TestSecurityHeaders_OnEveryResponse` (compares to the constant; no change), `TestReviewPage_StyleSrcUnsafeInlineIsLoadBearing`.
- Stale CDN mentions to edit: `internal/http/security.go` lines 41-43 (comment) and 68 (constant); `internal/http/security_test.go` lines 43, 93-95; `web/static/README.md` lines 3-5; `docs/architecture.md` lines 85, 100-101; `docs/routes.md` line 144.
- Historical plan docs and old CHANGELOG entries are left untouched.

## Steps
1. **Vendor the file.** Download `https://cdn.jsdelivr.net/npm/@picocss/pico@<version>/css/pico.min.css` to `web/static/pico.min.css` (plain download, no reformatting). Compute SHA-256. Confirm the banner version matches.
2. **`web/templates/layout.html:8`**: replace the jsDelivr `<link>` with `<link rel="stylesheet" href="/static/pico.min.css">`. Keep it before the `app.css` link.
3. **`internal/http/security.go`**: constant line 68 becomes `"style-src 'self' 'unsafe-inline'; " +`; delete comment lines 41-43; line 31 comment also names `web/static/pico.min.css` (#270). Leave the `data:` comment.
4. **`internal/http/security_test.go`**: update the line-43 row to `'self' 'unsafe-inline'` with a reason mentioning Pico and app.css served from /static/; rename the NoLoosening subtest to `style-src admits no external origin` and assert no external sources; add the tests below.
5. **`web/static/README.md`**: remove the "still loads Pico CSS from jsDelivr" sentence; add a `pico.min.css` table row (upstream URL, version, SHA-256, MIT); extend the licence paragraph (copyright holder/years from the banner or npm LICENSE.md).
6. **Docs**: `docs/architecture.md` line 85 (Pico is vendored as of #270; drop the CDN clause); lines ~98-101 ("Two sources are concessions" becomes one, noting the jsDelivr source was removed in #270); `docs/routes.md` line 144 ("Vendored htmx, Alpine, Pico CSS and the app's JS/CSS"). The architecture.md stack-table row (line 194) is unchanged.
7. **CHANGELOG** is handled at land time (one entry for the batch).

## Test plan
### 1. Coverage audit
- `TestContentSecurityPolicy_Directives` (style-src row), `TestContentSecurityPolicy_NoLoosening/style-src names exactly one external origin` (must be flipped), the other NoLoosening subtests, `TestSecurityHeaders_OnEveryResponse` (static asset case; self-adjusting; needs DB), `TestReviewPage_StyleSrcUnsafeInlineIsLoadBearing`.
- Gap: nothing asserts templates contain no external host, or that a CSS file is served from `/static/`.

### 2. Characterization tests
- Existing Directives rows for default-src, script-src, img-src (`'self' data:`), connect-src, form-action, frame-ancestors, base-uri, plus the SecurityHeaders static-asset case. Run `go test ./internal/http/ -run 'ContentSecurityPolicy|SecurityHeaders'` first as the baseline. No new characterization tests.

### 3. Red tests
1. `TestContentSecurityPolicy_NoLoosening/style-src admits no external origin` — fails today: constant contains jsDelivr.
2. `TestContentSecurityPolicy_Directives/style-src` (updated row `'self' 'unsafe-inline'`) — fails today for the same reason.
3. `TestLayout_NoExternalHosts` (no DB) — read templates from `web.Templates`; assert no `<link>`/`<script>` `href`/`src` matches `(?:https?:)?//`. Fails today on layout.html line 8.
4. `TestStatic_PicoServed` (no DB) — `registerStaticRoutes` on a mux, GET `/static/pico.min.css` via `httptest`: 200, non-empty body containing `Pico`. Fails today with 404.

### 4. Manual-only
- Open `/login`, `/decks`, a deck page, `/stats`, a review page and note forms; compare with pre-change (buttons, tables, select arrows, checkboxes, dropdowns).
- Devtools Network: no requests to hosts other than the app origin; `/static/pico.min.css` is 200. Console: no CSP violations.
- If the pinned version differs from what `@2` resolved to, check for rendering changes.

## Resolved decisions
1. Pico version: latest 2.x on npm at implementation time (not 3.x); README, CHANGELOG and URL use the same exact x.y.z.
2. Skip the README checksum test.
3. Copyright line/years taken from the downloaded banner or npm LICENSE.md.
