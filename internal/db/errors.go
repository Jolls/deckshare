package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation reports whether err is a Postgres 23505 (unique_violation) on the named
// constraint, so a duplicate deck or note-type name is an ordinary 409 rather than a 500
// (docs/schema.md).
func IsUniqueViolation(err error, constraint string) bool {
	return isPgErrOnConstraint(err, "23505", constraint)
}

// IsForeignKeyViolation reports whether err is a Postgres 23503 (foreign_key_violation) on the
// named constraint -- e.g. a note-type delete blocked by notes.note_type_id.
func IsForeignKeyViolation(err error, constraint string) bool {
	return isPgErrOnConstraint(err, "23503", constraint)
}

// isPgErrOnConstraint reports whether err carries a *pgconn.PgError with the given SQLSTATE code,
// raised by the named constraint.
func isPgErrOnConstraint(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == code && pgErr.ConstraintName == constraint
}
