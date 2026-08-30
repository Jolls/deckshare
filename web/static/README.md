# Vendored static assets

Vendored, not loaded from a CDN, so the reviewer has no runtime dependency on an external host
(plan resolved decision 6, `docs/plans/56-reviewer-batch-grading.md`). `web/templates/layout.html`
still loads Pico CSS from jsDelivr; that inconsistency is deliberately not addressed here.

| File | Upstream | Version | SHA-256 | Licence |
|---|---|---|---|---|
| `htmx.min.js` | https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js | 2.0.10 | `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de` | 0BSD |
| `alpine.min.js` | https://cdn.jsdelivr.net/npm/alpinejs@3.17.0/dist/cdn.min.js | 3.17.0 | `7c29241d5cc021779f412e32a4e611450e2d072f1513d45066c566cb4d4e76f8` | MIT |

0BSD (BigSky Software) and MIT (Alpine.js), both permissive and compatible with this project's
own AGPLv3 licence (`/LICENSE`). htmx's 0BSD notice is preserved unmodified in the vendored
file; Alpine's `cdn.min.js` build carries no licence banner (the MIT text lives in the npm
package, not the CDN artefact) -- recorded here instead.

`app.css` (issue #166) is hand-written, not vendored -- it holds the responsive breakpoints
Pico.css doesn't cover.

`htmx-ext-json-enc.js` was removed in #99: its same-key merge logic breaks on an array-valued
`hx-vals` result with 2+ elements (pushes the array into itself, throws, and htmx silently falls
back to a malformed default encoding -- docs/plans/99-grading-persistence.md). The review batch
POST is sent with a direct `fetch()` instead (`web/static/review.js`'s `flush()`).

To update: download the new version from the same upstream URL, recompute its SHA-256
(`sha256sum web/static/<file>`), and update this table.
