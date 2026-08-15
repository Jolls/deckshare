// Package apkg reads and writes Anki .apkg/.colpkg packages through an intermediate
// representation (architecture.md §4, §7): import is apkg -> IR -> db, export is db -> IR ->
// apkg. The IR is where format quirks are normalised, so the schema-11 and schema-18 readers
// converge before any database code runs, and it is what unit tests assert against.
//
// No Anki-derived code (CLAUDE.md §2.8): this reader is written from docs/apkg-format.md and
// docs/anki-schema.md, both clean-room reconstructions. Never read ankitects/anki source into
// this package.
package apkg
