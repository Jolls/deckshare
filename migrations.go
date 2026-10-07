package deckshare

import (
	"embed"
	"io/fs"
)

// The server applies these at startup (internal/db.Migrate), so a deployed image needs no goose
// CLI step. The embed lives here, in the root package, because go:embed cannot reach migrations/
// from a subdirectory -- and a .go file inside migrations/ would be scanned by the goose CLI and
// read by sqlc as schema.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the goose files rooted at "." (00001_users.sql, ...).
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // unreachable: "migrations" is the embed pattern's own directory
	}
	return sub
}
