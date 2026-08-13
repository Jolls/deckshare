// Package web embeds the server-rendered HTML templates.
package web

import "embed"

//go:embed templates/*.html
var Templates embed.FS
