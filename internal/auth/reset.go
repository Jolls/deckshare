package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
)

// PasswordResetLifetime is how long an operator-issued reset link stays usable. The operator
// relays it by hand, so this is a human-latency window, not a machine one (#225).
const PasswordResetLifetime = 24 * time.Hour

var (
	ErrInvalidResetToken = errors.New("auth: invalid or expired reset token")
	ErrNoSuchUser        = errors.New("auth: no account with that email")
)

// CreatePasswordReset mints a one-time reset token for the account at email and returns the user
// plus the RAW token; only its SHA-256 hash is stored. Operator-only: no HTTP route calls this,
// so there is no rate limiter and no timing-safe dummy path -- an unknown email is an honest
// error to the operator's terminal, not an oracle exposed to the network.
//
// Issuing a link supersedes any the account already has outstanding, and changes nothing else:
// the password and every session stay as they are until the link is used.
func (s *Service) CreatePasswordReset(ctx context.Context, email string) (db.User, string, error) {
	email = strings.TrimSpace(email)
	if msg, ok := validateEmail(email); !ok {
		return db.User{}, "", &ValidationError{Msg: msg}
	}

	user, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, "", ErrNoSuchUser
		}
		return db.User{}, "", fmt.Errorf("get user by email: %w", err)
	}

	raw, err := newToken()
	if err != nil {
		return db.User{}, "", fmt.Errorf("generate reset token: %w", err)
	}

	tx, err := s.beginner.Begin(ctx)
	if err != nil {
		return db.User{}, "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.DeletePasswordResetTokensForUser(ctx, user.ID); err != nil {
		return db.User{}, "", fmt.Errorf("delete password reset tokens for user: %w", err)
	}
	if err := qtx.CreatePasswordResetToken(ctx, db.CreatePasswordResetTokenParams{
		ID:     hashToken(raw),
		UserID: user.ID,
		ExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(PasswordResetLifetime),
			Valid: true,
		},
	}); err != nil {
		return db.User{}, "", fmt.Errorf("create password reset token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.User{}, "", fmt.Errorf("commit password reset token: %w", err)
	}
	return user, raw, nil
}

// PasswordResetValid reports whether rawToken names a live reset token, without consuming it.
func (s *Service) PasswordResetValid(ctx context.Context, rawToken string) (bool, error) {
	if _, err := s.q.GetPasswordResetToken(ctx, hashToken(rawToken)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("get password reset token: %w", err)
	}
	return true, nil
}

// ResetPassword consumes a one-time operator-issued token and sets the account's password. On
// success it invalidates every session for the account and returns the raw token of a
// replacement session for the browser that completed the reset -- the same atomicity contract as
// ChangePassword: a failure between the password write and the session purge would leave the new
// password live alongside every old session, which is the whole point of the purge.
//
// There is no current-password check: possession of the token is the credential.
func (s *Service) ResetPassword(ctx context.Context, ip, rawToken, newPassword string) (string, error) {
	// Before any token lookup, so a too-short password does not burn the link.
	if msg, ok := validatePassword(newPassword); !ok {
		return "", &ValidationError{Msg: msg}
	}

	// The token is 256 bits of crypto/rand, so guessing is not the threat; this bounds the
	// argon2 CPU a stream of submissions can burn.
	if ok, retryAfter := s.resetPassword.Allow(ip); !ok {
		return "", &RateLimitError{RetryAfter: retryAfter}
	}

	// Cheap lookup first so a bogus, expired or used link never pays for argon2. The consume below
	// stays authoritative if the token is taken in between.
	if ok, err := s.PasswordResetValid(ctx, rawToken); err != nil {
		return "", err
	} else if !ok {
		return "", ErrInvalidResetToken
	}

	newHash, err := argon2id.CreateHash(newPassword, argon2id.DefaultParams)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.beginner.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	// Both the authorisation and the single-use enforcement. First in the transaction, so a later
	// failure rolls the consumption back and the link stays usable.
	userID, err := qtx.ConsumePasswordResetToken(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrInvalidResetToken
		}
		return "", fmt.Errorf("consume password reset token: %w", err)
	}
	if err := qtx.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: newHash,
	}); err != nil {
		return "", fmt.Errorf("update user password: %w", err)
	}
	// sessions_user_id_idx (migration 00002) exists for exactly this query.
	if _, err := qtx.DeleteSessionsForUser(ctx, userID); err != nil {
		return "", fmt.Errorf("delete sessions for user: %w", err)
	}
	token, err := createSession(ctx, qtx, userID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit password reset: %w", err)
	}
	return token, nil
}
