// Package db holds sqlc-generated queries and the hand-written pool/connection setup.
package db

//go:generate sqlc generate -f ../../sqlc.yaml

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool against dsn.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn)
}
