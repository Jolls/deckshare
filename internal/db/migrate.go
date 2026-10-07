package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Migrate applies every pending goose migration in fsys and returns how many it applied. It uses
// the same goose_db_version table as the goose CLI, so a database prepared by either is accepted
// by the other. No session locker: the deployment is single-instance (architecture.md §3).
//
// It borrows pool through database/sql for the duration of the call and leaves it open.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (applied int, err error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() {
		if cerr := sqlDB.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("db: migrate: close: %w", cerr)
		}
	}()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return 0, fmt.Errorf("db: migrate: %w", err)
	}
	results, err := provider.Up(ctx)
	for _, r := range results {
		slog.Info("migration applied", "version", r.Source.Version, "file", filepath.Base(r.Source.Path), "duration", r.Duration)
	}
	if err != nil {
		var partial *goose.PartialError
		if errors.As(err, &partial) && partial.Failed != nil {
			return len(results), fmt.Errorf("db: migrate: %s: %w", filepath.Base(partial.Failed.Source.Path), partial.Err)
		}
		return len(results), fmt.Errorf("db: migrate: %w", err)
	}
	return len(results), nil
}
