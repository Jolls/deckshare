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

// IsRestrictViolation reports whether err is a parent-row delete blocked by an ON DELETE RESTRICT
// child on the named constraint -- e.g. a media_blobs delete blocked by media_refs.sha256. Distinct
// from IsForeignKeyViolation: Postgres 18 raises 23001 (restrict_violation) for an explicit RESTRICT
// block, not the 23503 a bad child insert raises, so both codes are matched rather than assumed.
func IsRestrictViolation(err error, constraint string) bool {
	return isPgErrOnConstraint(err, "23001", constraint) || isPgErrOnConstraint(err, "23503", constraint)
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
