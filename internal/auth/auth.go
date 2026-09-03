package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
)

const (
	CookieName      = "__Host-deckshare_session"
	SessionLifetime = 30 * 24 * time.Hour
	RenewThreshold  = 15 * 24 * time.Hour

	dummyPassword = "deckshare-timing-safe-dummy-password"

	loginIPLimit     = 20
	loginIPWindow    = 15 * time.Minute
	loginEmailLimit  = 5
	loginEmailWindow = 15 * time.Minute
	signupIPLimit    = 5
	signupIPWindow   = time.Hour

	changePasswordLimit  = 5
	changePasswordWindow = 15 * time.Minute
)

var (
	ErrInvalidCredentials = errors.New("auth: invalid email or password")
	ErrEmailTaken         = errors.New("auth: email already registered")
	ErrRateLimited        = errors.New("auth: too many attempts")
)

// ValidationError carries a user-safe message for one bad field. Signup, Login, UpdateProfile,
// and ChangePassword all return this for shape failures so the HTTP layer has one type to check.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// RateLimitError is returned when a caller has exceeded a rate-limit budget.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "auth: too many attempts" }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Config carries the deployment-dependent knobs. Zero values are the dev defaults.
type Config struct {
	// Origin is a comma-separated list of allowed origins, e.g. "https://deckshare.example" or
	// "https://deckshare.example,https://abc123.onion" for an instance reachable at more than one
	// address (StartOS commonly exposes the same instance over LAN and Tor simultaneously).
	// Empty means compare the request Origin's host to r.Host.
	Origin string
}

// Service is the auth package's entry point: session, signup, login, logout, and account
// settings all go through it.
type Service struct {
	q         *db.Queries
	beginner  db.Beginner
	dummyHash string
	// origins is cfg.Origin split and parsed once at construction, rather than on every
	// state-changing request in checkOrigin -- it's a fixed deployment setting, never
	// rechecked mid-process. Nil when cfg.Origin is empty (the dev fallback: compare against
	// r.Host per request).
	origins []*url.URL

	loginIP        *limiter
	loginEmail     *limiter
	signupIP       *limiter
	changePassword *limiter
}

// New builds the service over any Beginner -- a *pgxpool.Pool in production, a pgx.Tx in tests
// (where Begin opens a savepoint). It computes the fixed dummy argon2id hash once, which is what
// makes login timing-safe.
func New(dbtx db.Beginner, cfg Config) (*Service, error) {
	dummyHash, err := argon2id.CreateHash(dummyPassword, argon2id.DefaultParams)
	if err != nil {
		return nil, fmt.Errorf("compute dummy hash: %w", err)
	}
	var origins []*url.URL
	if cfg.Origin != "" {
		for _, s := range strings.Split(cfg.Origin, ",") {
			parsed, err := url.Parse(strings.TrimSpace(s))
			if err != nil {
				return nil, fmt.Errorf("parse Config.Origin: %w", err)
			}
			origins = append(origins, parsed)
		}
	}
	return &Service{
		q:              db.New(dbtx),
		beginner:       dbtx,
		dummyHash:      dummyHash,
		origins:        origins,
		loginIP:        newLimiter(loginIPLimit, loginIPWindow),
		loginEmail:     newLimiter(loginEmailLimit, loginEmailWindow),
		signupIP:       newLimiter(signupIPLimit, signupIPWindow),
		changePassword: newLimiter(changePasswordLimit, changePasswordWindow),
	}, nil
}

// Signup validates the input, creates the user and its first session, and returns the raw
// session token.
func (s *Service) Signup(ctx context.Context, ip, email, password, displayName string) (db.User, string, error) {
	email = strings.TrimSpace(email)
	if msg, ok := validateEmail(email); !ok {
		return db.User{}, "", &ValidationError{Msg: msg}
	}
	if msg, ok := validatePassword(password); !ok {
		return db.User{}, "", &ValidationError{Msg: msg}
	}
	displayName = strings.TrimSpace(displayName)
	if msg, ok := validateDisplayName(displayName); !ok {
		return db.User{}, "", &ValidationError{Msg: msg}
	}

	if ok, retryAfter := s.signupIP.Allow(ip); !ok {
		return db.User{}, "", &RateLimitError{RetryAfter: retryAfter}
	}

	var (
		hash      string
		hashErr   error
		exists    bool
		existsErr error
		wg        sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		hash, hashErr = argon2id.CreateHash(password, argon2id.DefaultParams)
	}()
	go func() {
		defer wg.Done()
		exists, existsErr = s.q.EmailExists(ctx, email)
	}()
	wg.Wait()

	if hashErr != nil {
		return db.User{}, "", fmt.Errorf("hash password: %w", hashErr)
	}
	if existsErr != nil {
		return db.User{}, "", fmt.Errorf("check email exists: %w", existsErr)
	}
	if exists {
		return db.User{}, "", ErrEmailTaken
	}

	user, err := s.q.CreateUser(ctx, db.CreateUserParams{
		Email:        email,
		PasswordHash: hash,
		DisplayName:  displayName,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, "", ErrEmailTaken
		}
		return db.User{}, "", fmt.Errorf("create user: %w", err)
	}

	// Best-effort: a user with no note types can't create a single note, but a seeding failure
	// here must never take down signup itself.
	if err := s.seedDefaultNoteTypes(ctx, user.ID); err != nil {
		log.Printf("seed default note types for user %s: %v", user.ID, err)
	}

	token, err := createSession(ctx, s.q, user.ID)
	if err != nil {
		return db.User{}, "", err
	}
	return user, token, nil
}

// Login validates the input, verifies the password in constant time regardless of whether the
// account exists, and on success returns the user and a fresh session token.
func (s *Service) Login(ctx context.Context, ip, email, password string) (db.User, string, error) {
	email = strings.TrimSpace(email)
	if msg, ok := validateEmail(email); !ok {
		return db.User{}, "", &ValidationError{Msg: msg}
	}
	if msg, ok := validatePassword(password); !ok {
		return db.User{}, "", &ValidationError{Msg: msg}
	}

	if ok, retryAfter := s.loginIP.Allow(ip); !ok {
		return db.User{}, "", &RateLimitError{RetryAfter: retryAfter}
	}
	// The per-email bucket is global, not IP-scoped, and is consumed before any credential
	// check -- so anyone who knows a victim's email can keep it locked out of /login
	// indefinitely by sending loginEmailLimit wrong passwords every loginEmailWindow, no
	// guessing required. Accepted tradeoff for now: it's a self-resetting denial of
	// availability, not a credential leak, and it's what stops distributed credential
	// stuffing against one known account regardless of how many IPs the attacker rotates
	// through (loginIP alone would not catch that). Revisit if targeted lockout abuse is
	// ever observed -- e.g. widening the window/threshold, or requiring a second signal
	// (CAPTCHA, email confirmation) before this bucket engages.
	if ok, retryAfter := s.loginEmail.Allow(strings.ToLower(email)); !ok {
		return db.User{}, "", &RateLimitError{RetryAfter: retryAfter}
	}

	user, err := s.q.GetUserByEmail(ctx, email)
	found := true
	hash := s.dummyHash
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, "", fmt.Errorf("get user by email: %w", err)
		}
		found = false
	} else {
		hash = user.PasswordHash
	}

	match, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		return db.User{}, "", fmt.Errorf("compare password: %w", err)
	}
	if !found || !match {
		return db.User{}, "", ErrInvalidCredentials
	}

	token, err := createSession(ctx, s.q, user.ID)
	if err != nil {
		return db.User{}, "", err
	}
	return user, token, nil
}

// Logout deletes the session identified by the raw cookie token. Deleting an absent row is not
// an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	if err := s.q.DeleteSession(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func createSession(ctx context.Context, q *db.Queries, userID pgtype.UUID) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	err = q.CreateSession(ctx, db.CreateSessionParams{
		ID:     hashToken(token),
		UserID: userID,
		ExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(SessionLifetime),
			Valid: true,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func validateEmail(email string) (string, bool) {
	if email == "" {
		return "Email is required", false
	}
	if len(email) > 254 {
		return "Email is too long", false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "Enter a valid email address", false
	}
	return "", true
}

func validatePassword(password string) (string, bool) {
	if len(password) < 8 {
		return "Password must be at least 8 characters", false
	}
	if len(password) > 256 {
		return "Password is too long", false
	}
	return "", true
}

func validateDisplayName(name string) (string, bool) {
	if name == "" {
		return "Name is required", false
	}
	if len(name) > 100 {
		return "Name is too long", false
	}
	return "", true
}

func validateTimezone(tz string) (string, bool) {
	if tz == "" {
		return "Unknown timezone", false
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "Unknown timezone", false
	}
	return "", true
}

// UpdateProfile validates and persists display_name, timezone, and day_start_hour.
func (s *Service) UpdateProfile(ctx context.Context, userID pgtype.UUID, displayName, timezone string, dayStartHour int16) error {
	displayName = strings.TrimSpace(displayName)
	if msg, ok := validateDisplayName(displayName); !ok {
		return &ValidationError{Msg: msg}
	}
	if msg, ok := validateTimezone(timezone); !ok {
		return &ValidationError{Msg: msg}
	}
	if dayStartHour < 0 || dayStartHour > 23 {
		return &ValidationError{Msg: "Day start hour must be 0-23"}
	}

	err := s.q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
		ID:           userID,
		DisplayName:  displayName,
		Timezone:     timezone,
		DayStartHour: dayStartHour,
	})
	if err != nil {
		return fmt.Errorf("update user profile: %w", err)
	}
	return nil
}

// ChangePassword verifies currentPassword against currentHash (the caller's already-loaded
// db.User.PasswordHash, via UserFromContext -- this method takes no DB read of its own for the
// current hash), then validates and persists newPassword. Returns ErrInvalidCredentials if
// currentPassword does not match -- never distinguishes "wrong password" from any other failure
// in the response the handler builds, consistent with Login, since this endpoint is also a
// password-guessing surface. On success it invalidates every session for the account and returns
// the raw token of a replacement session for the caller's own browser.
func (s *Service) ChangePassword(ctx context.Context, userID pgtype.UUID, currentHash, currentPassword, newPassword string) (string, error) {
	if msg, ok := validatePassword(newPassword); !ok {
		return "", &ValidationError{Msg: msg}
	}

	if ok, retryAfter := s.changePassword.Allow(userID.String()); !ok {
		return "", &RateLimitError{RetryAfter: retryAfter}
	}

	match, err := argon2id.ComparePasswordAndHash(currentPassword, currentHash)
	if err != nil {
		return "", fmt.Errorf("compare password: %w", err)
	}
	if !match {
		return "", ErrInvalidCredentials
	}

	newHash, err := argon2id.CreateHash(newPassword, argon2id.DefaultParams)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	// The password update, the session purge, and the replacement session are one transaction:
	// a failure between them would leave the new password in place with every old session --
	// including a stolen one -- still live, which is the whole point of the purge.
	tx, err := s.beginner.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if err := qtx.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: newHash,
	}); err != nil {
		return "", fmt.Errorf("update user password: %w", err)
	}
	// Every session for this account dies with the old password, the acting browser's included;
	// it gets a fresh one immediately below, so the tab that made the change stays signed in.
	// sessions_user_id_idx (migration 00002) exists for exactly this query.
	if _, err := qtx.DeleteSessionsForUser(ctx, userID); err != nil {
		return "", fmt.Errorf("delete sessions for user: %w", err)
	}
	token, err := createSession(ctx, qtx, userID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit password change: %w", err)
	}
	return token, nil
}
