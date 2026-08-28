// Package apkg reads and writes Anki .apkg/.colpkg packages through an intermediate
// representation (architecture.md §4, §7): import is apkg -> IR -> db, export is db -> IR ->
// apkg. The IR is where format quirks are normalised, so the schema-11 and schema-18 readers
// converge before any database code runs, and it is what unit tests assert against.
//
// No Anki-derived code (CLAUDE.md §2.8): this reader is written from docs/apkg-format.md and
// docs/anki-schema.md, both clean-room reconstructions. Never read ankitects/anki source into
// this package.
//
// Boundary: only dbwrite.go (IR -> db) and dbexport.go (db -> IR) may name internal/db types.
// read.go, write.go, media.go, protobuf.go, ankischema.go, ir.go and errors.go import no database
// package at all, which is what keeps a sqlc-generated row from leaking into the format layer.
package apkg
