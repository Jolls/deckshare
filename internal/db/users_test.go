package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestUsersColorScheme_DefaultAuto(t *testing.T) {
	tx := beginTx(t)
	id := mustUser(t, tx)
	var got string
	if err := tx.QueryRow(context.Background(), `SELECT color_scheme FROM users WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read color_scheme: %v", err)
	}
	if got != "auto" {
		t.Errorf("color_scheme = %q, want auto", got)
	}
}

func TestUsersColorScheme_CheckRejectsUnknown(t *testing.T) {
	tx := beginTx(t)
	id := mustUser(t, tx)
	_, err := tx.Exec(context.Background(), `UPDATE users SET color_scheme = 'sepia' WHERE id = $1`, id)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "users_color_scheme_check" {
		t.Errorf("err = %v, want check violation on users_color_scheme_check", err)
	}
}

func TestUsersAccent_DefaultAzureAndCheckRejectsUnknown(t *testing.T) {
	tx := beginTx(t)
	id := mustUser(t, tx)
	var got string
	if err := tx.QueryRow(context.Background(), `SELECT accent FROM users WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read accent: %v", err)
	}
	if got != "azure" {
		t.Errorf("accent = %q, want azure", got)
	}
	_, err := tx.Exec(context.Background(), `UPDATE users SET accent = 'magenta' WHERE id = $1`, id)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "users_accent_check" {
		t.Errorf("err = %v, want check violation on users_accent_check", err)
	}
}
