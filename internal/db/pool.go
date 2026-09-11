// Package db holds sqlc-generated queries and the hand-written pool/connection setup.
package db

//go:generate sqlc generate -f ../../sqlc.yaml

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool against dsn. Every pooled connection gets a
// statement_timeout (docs/plans/213-timeouts.md Decision 3) so a runaway query fails before it
// can consume a handler's whole request budget.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parsing dsn: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET statement_timeout = '30s'")
		return err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
