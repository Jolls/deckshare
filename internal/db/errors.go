package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation reports whether err is a Postgres 23505 (unique_violation) on the named
// constraint, so a duplicate deck or note-type name is an ordinary 409 rather than a 500
// (docs/schema.md).
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// IsForeignKeyViolation reports whether err is a Postgres 23503 (foreign_key_violation) on the
// named constraint -- e.g. a note-type delete blocked by notes.note_type_id.
func IsForeignKeyViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23503" && pgErr.ConstraintName == constraint
}
