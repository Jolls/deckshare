# Vendored static assets

Vendored, not loaded from a CDN, so the reviewer has no runtime dependency on an external host
(plan resolved decision 6, `docs/plans/56-reviewer-batch-grading.md`). `web/templates/layout.html`
still loads Pico CSS from jsDelivr; that inconsistency is deliberately not addressed here.

| File | Upstream | Version | SHA-256 | Licence |
|---|---|---|---|---|
| `htmx.min.js` | https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js | 2.0.10 | `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de` | 0BSD |

0BSD (BigSky Software), permissive and compatible with this project's own AGPLv3 licence
(`/LICENSE`); the 0BSD notice is preserved unmodified in the vendored file.

`htmx-ext-json-enc.js` was removed in #99: its same-key merge logic breaks on an array-valued
`hx-vals` result with 2+ elements (pushes the array into itself, throws, and htmx silently falls
back to a malformed default encoding -- docs/plans/99-grading-persistence.md). The review batch
POST is sent with a direct `fetch()` instead (`web/static/review.js`'s `flush()`).

To update: download the new version from the same upstream URL, recompute its SHA-256
(`sha256sum web/static/<file>`), and update this table.
