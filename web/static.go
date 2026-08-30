package web

import "embed"

// Static holds vendored JS (htmx, Alpine.js, the reviewer's queue module) and CSS served under
// /static/ -- see web/static/README.md for versions and licences. Vendored, not CDN-loaded, so
// the reviewer has no runtime dependency on an external host.
//
//go:embed static
var Static embed.FS
