// Package apkg reads and writes .apkg/.colpkg files, producing and consuming an intermediate
// representation (IR) rather than sqlc-generated rows directly. Import is apkg -> IR -> db,
// export is db -> IR -> apkg (architecture.md §4).
package apkg
