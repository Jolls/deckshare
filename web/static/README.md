# Vendored static assets

Vendored, not loaded from a CDN, so the reviewer has no runtime dependency on an external host
(plan resolved decision 6, `docs/plans/56-reviewer-batch-grading.md`). `web/templates/layout.html`
still loads Pico CSS from jsDelivr; that inconsistency is deliberately not addressed here.

| File | Upstream | Version | SHA-256 | Licence |
|---|---|---|---|---|
| `htmx.min.js` | https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js | 2.0.10 | `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de` | 0BSD |
| `htmx-ext-json-enc.js` | https://unpkg.com/htmx-ext-json-enc@2.0.1/json-enc.js | 2.0.1 | `9899374573b1e3276618d4f46c5fff56b373b55f8bdde900f31ce3425dc478d3` | 0BSD |

Both are 0BSD (BigSky Software / htmx-extensions), permissive and compatible with this project's
own AGPLv3 licence (`/LICENSE`); the 0BSD notice is preserved unmodified in each vendored file.

To update: download the new version from the same upstream URL, recompute its SHA-256
(`sha256sum web/static/<file>`), and update this table.
