// Command reset-password is the operator's password-reset tool (#225): it mints a single-use,
// time-limited reset link for one account and prints it, for the operator to relay to the
// account holder out of band. Operator-only -- run it on the host/container with DATABASE_URL in
// the environment. No HTTP route mints a reset link, and DeckShare never emails one.
//
// Usage:
//
//	go run ./cmd/reset-password <email>
//
// The link's base URL is the first comma-separated entry of ORIGIN (the same setting the
// server's CSRF check uses, .env.example), which must be set.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	flag.Parse()
	if flag.NArg() != 1 {
		return errors.New("usage: reset-password <email>")
	}
	email := flag.Arg(0)

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}

	baseURL, _, _ := strings.Cut(os.Getenv("ORIGIN"), ",")
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return errors.New("ORIGIN is required: its first entry is the base URL of the printed reset link")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	authSvc, err := auth.New(pool, auth.Config{})
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
	}

	// Taken before the token is stored, so the printed expiry is never later than the real one.
	expires := time.Now().Add(auth.PasswordResetLifetime)
	user, raw, err := authSvc.CreatePasswordReset(ctx, email)
	if err != nil {
		if errors.Is(err, auth.ErrNoSuchUser) {
			return fmt.Errorf("no account with email %q", email)
		}
		return fmt.Errorf("create password reset: %w", err)
	}

	fmt.Printf("Password reset link for %s <%s>:\n\n", user.DisplayName, user.Email)
	fmt.Printf("  %s/reset-password?token=%s\n\n", baseURL, raw)
	fmt.Printf("Valid once, expires %s (in %v).\n", expires.Format(time.RFC1123), auth.PasswordResetLifetime)
	fmt.Println("Relay it to the account holder yourself -- DeckShare sends no email.")
	return nil
}
