package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Beginner is a DBTX that can also open a transaction: *pgxpool.Pool in production, pgx.Tx in
// tests (where Begin opens a savepoint), which is what lets handler tests run inside a
// rollback-only transaction the way internal/http's existing tests already do.
type Beginner interface {
	DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}
