# Vendored static assets

Vendored, not loaded from a CDN, so the reviewer has no runtime dependency on an external host
(plan resolved decision 6, `docs/plans/56-reviewer-batch-grading.md`).

| File | Upstream | Version | SHA-256 | Licence |
|---|---|---|---|---|
| `htmx.min.js` | https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js | 2.0.10 | `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de` | 0BSD |
| `alpine.min.js` | https://cdn.jsdelivr.net/npm/alpinejs@3.17.0/dist/cdn.min.js | 3.17.0 | `7c29241d5cc021779f412e32a4e611450e2d072f1513d45066c566cb4d4e76f8` | MIT |
| `pico.min.css` | https://cdn.jsdelivr.net/npm/@picocss/pico@2.1.1/css/pico.min.css | 2.1.1 | `fbc9a63fc9fc9f72d12fd7fc9806e11fa9f77ae4f9cad146b27003a1119ba3db` | MIT |

0BSD (BigSky Software), MIT (Alpine.js) and MIT (Pico CSS, Copyright 2019-2025), all permissive and compatible with this project's
own AGPLv3 licence (`/LICENSE`). htmx's 0BSD notice is preserved unmodified in the vendored
file; Alpine's `cdn.min.js` build carries no licence banner (the MIT text lives in the npm
package, not the CDN artefact) -- recorded here instead. Pico's banner names the licence and
copyright (2019-2025) but not the full MIT text, which is in the npm package's `LICENSE.md`.

`app.css` (issue #166) is hand-written, not vendored -- it holds the responsive breakpoints
Pico.css doesn't cover.

`htmx-ext-json-enc.js` was removed in #99: its same-key merge logic breaks on an array-valued
`hx-vals` result with 2+ elements (pushes the array into itself, throws, and htmx silently falls
back to a malformed default encoding -- docs/plans/99-grading-persistence.md). The review batch
POST is sent with a direct `fetch()` instead (`web/static/review.js`'s `flush()`).

`favicon.png` is the app icon (hand-supplied artwork, not vendored), downscaled to 256x256 and
converted to PNG from the source image; referenced from `web/templates/layout.html`.

To update: download the new version from the same upstream URL, recompute its SHA-256
(`sha256sum web/static/<file>`), and update this table.
