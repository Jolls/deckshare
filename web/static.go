package web

import "embed"

// Static holds vendored JS (htmx, the reviewer's queue module) served under /static/ -- see
// web/static/README.md for versions and licences. Vendored, not CDN-loaded, so the reviewer has
// no runtime dependency on an external host.
//
//go:embed static
var Static embed.FS
