package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/deckshare/internal/db"
)

// DB-backed tests: skipped unless DATABASE_URL is set. Every test runs inside a pgx.Tx that is
// always rolled back, so tests neither pollute the dev database nor depend on each other. One
// pgxpool for the package, following internal/db/deletion_test.go's pattern.

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	seq      atomic.Int64
)

func nextSeq() int64 {
	return seq.Add(1)
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed test")
	}
	poolOnce.Do(func() {
		p, err := db.NewPool(context.Background(), dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		pool = p
	})
	return pool
}

func beginTx(t *testing.T) pgx.Tx {
	t.Helper()
	p := testPool(t)
	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return tx
}

func newTestService(t *testing.T, tx pgx.Tx) *Service {
	t.Helper()
	s, err := New(tx, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func testEmail() string {
	return fmt.Sprintf("test-%d@example.com", nextSeq())
}

func countRows(t *testing.T, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

func TestSignup_CreatesUserAndSession(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	email := testEmail()
	user, token, err := s.Signup(ctx, "1.2.3.4", email, "correct-horse-battery", "Ada Lovelace")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if user.Email != email {
		t.Errorf("user.Email = %q, want %q", user.Email, email)
	}
	match, err := argon2id.ComparePasswordAndHash("correct-horse-battery", user.PasswordHash)
	if err != nil {
		t.Fatalf("ComparePasswordAndHash: %v", err)
	}
	if !match {
		t.Error("stored hash should verify against the submitted password")
	}

	row, err := db.New(tx).GetSessionUser(ctx, hashToken(token))
	if err != nil {
		t.Fatalf("GetSessionUser: %v", err)
	}
	if row.User.ID != user.ID {
		t.Error("session should belong to the created user")
	}
	if until := time.Until(row.ExpiresAt.Time); until < 29*24*time.Hour || until > 30*24*time.Hour {
		t.Errorf("expires_at ~30 days out, got %v", until)
	}
}

func TestSignup_DuplicateEmail(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	email := testEmail()
	if _, _, err := s.Signup(ctx, "1.2.3.4", email, "correct-horse-battery", "First"); err != nil {
		t.Fatalf("first Signup: %v", err)
	}

	_, _, err := s.Signup(ctx, "1.2.3.4", email, "another-password", "Second")
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("second Signup error = %v, want ErrEmailTaken", err)
	}

	if n := countRows(t, tx, `SELECT count(*) FROM users WHERE lower(email) = lower($1)`, email); n != 1 {
		t.Errorf("user row count = %d, want 1", n)
	}
}

func TestSignup_ConflictInsertPath(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	email := testEmail()
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Existing')`,
		email,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	s := newTestService(t, tx)
	_, _, err := s.Signup(ctx, "1.2.3.4", email, "correct-horse-battery", "New")
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("Signup error = %v, want ErrEmailTaken", err)
	}
}

func TestLogin_Success(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	email := testEmail()
	created, _, err := s.Signup(ctx, "1.2.3.4", email, "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	user, token, err := s.Login(ctx, "1.2.3.4", email, "correct-horse-battery")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if user.ID != created.ID {
		t.Error("Login should return the same user")
	}
	if token == "" {
		t.Error("Login should return a session token")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	email := testEmail()
	if _, _, err := s.Signup(ctx, "1.2.3.4", email, "correct-horse-battery", "Ada"); err != nil {
		t.Fatalf("Signup: %v", err)
	}
	before := countRows(t, tx, `SELECT count(*) FROM sessions`)

	_, _, err := s.Login(ctx, "1.2.3.4", email, "wrong-password")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
	if after := countRows(t, tx, `SELECT count(*) FROM sessions`); after != before {
		t.Errorf("session row count = %d, want unchanged (%d)", after, before)
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	before := countRows(t, tx, `SELECT count(*) FROM sessions`)

	_, _, err := s.Login(ctx, "1.2.3.4", testEmail(), "any-password-at-all")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
	if after := countRows(t, tx, `SELECT count(*) FROM sessions`); after != before {
		t.Errorf("session row count = %d, want unchanged (%d)", after, before)
	}
}

func TestLogin_UnknownEmailStillHashes(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	email := testEmail()
	if _, _, err := s.Signup(ctx, "1.2.3.4", email, "correct-horse-battery", "Ada"); err != nil {
		t.Fatalf("Signup: %v", err)
	}

	start := time.Now()
	_, _, _ = s.Login(ctx, "1.2.3.4", email, "wrong-password")
	knownDur := time.Since(start)

	start = time.Now()
	_, _, _ = s.Login(ctx, "5.6.7.8", testEmail(), "wrong-password")
	unknownDur := time.Since(start)

	if unknownDur < knownDur/2 {
		t.Errorf("unknown-email path took %v, known-email path took %v; expected comparable cost", unknownDur, knownDur)
	}
}

func TestSession_ExpiredIsRejected(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	email := testEmail()
	var userID any
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Test') RETURNING id`,
		email,
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	sessionID := "expired-session"
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, now() - interval '1 hour')`,
		sessionID, userID,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	_, err := db.New(tx).GetSessionUser(ctx, sessionID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSessionUser error = %v, want pgx.ErrNoRows", err)
	}
}

func TestLogout_DeletesRow(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	_, token, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if err := s.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM sessions WHERE id = $1`, hashToken(token)); n != 0 {
		t.Errorf("session row count = %d, want 0", n)
	}

	if err := s.Logout(ctx, token); err != nil {
		t.Errorf("Logout on already-deleted session: %v, want nil", err)
	}
}

func TestUpdateProfile_Success(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if err := s.UpdateProfile(ctx, user.ID, "New Name", "America/New_York", 6); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	updated, err := db.New(tx).GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if updated.DisplayName != "New Name" {
		t.Errorf("DisplayName = %q, want %q", updated.DisplayName, "New Name")
	}
	if updated.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q, want %q", updated.Timezone, "America/New_York")
	}
	if updated.DayStartHour != 6 {
		t.Errorf("DayStartHour = %d, want 6", updated.DayStartHour)
	}
}

func TestUpdateProfile_InvalidTimezone(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	err = s.UpdateProfile(ctx, user.ID, "New Name", "Not/AZone", 6)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("UpdateProfile error = %v, want *ValidationError", err)
	}

	unchanged, err := db.New(tx).GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if unchanged.DisplayName != "Ada" {
		t.Error("row should be unchanged")
	}
}

func TestChangePassword_Success(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if _, err := s.ChangePassword(ctx, user.ID, user.PasswordHash, "correct-horse-battery", "new-correct-horse-battery"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	updated, err := db.New(tx).GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	match, err := argon2id.ComparePasswordAndHash("new-correct-horse-battery", updated.PasswordHash)
	if err != nil {
		t.Fatalf("ComparePasswordAndHash: %v", err)
	}
	if !match {
		t.Error("new password should verify")
	}
	oldMatch, err := argon2id.ComparePasswordAndHash("correct-horse-battery", updated.PasswordHash)
	if err != nil {
		t.Fatalf("ComparePasswordAndHash: %v", err)
	}
	if oldMatch {
		t.Error("old password should no longer verify")
	}
}

func TestChangePassword_WrongCurrentPassword(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	_, err = s.ChangePassword(ctx, user.ID, user.PasswordHash, "wrong-current-password", "new-correct-horse-battery")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ChangePassword error = %v, want ErrInvalidCredentials", err)
	}

	unchanged, err := db.New(tx).GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if unchanged.PasswordHash != user.PasswordHash {
		t.Error("hash should be unchanged")
	}
}

func TestChangePassword_WeakNewPassword(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	_, err = s.ChangePassword(ctx, user.ID, user.PasswordHash, "correct-horse-battery", "short")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("ChangePassword error = %v, want *ValidationError", err)
	}

	unchanged, err := db.New(tx).GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if unchanged.PasswordHash != user.PasswordHash {
		t.Error("hash should be unchanged")
	}
}

func TestChangePassword_RateLimited(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	var last error
	for i := 0; i < 6; i++ {
		_, last = s.ChangePassword(ctx, user.ID, user.PasswordHash, "wrong-current-password", "new-correct-horse-battery")
	}
	var rle *RateLimitError
	if !errors.As(last, &rle) {
		t.Fatalf("6th ChangePassword error = %v, want *RateLimitError", last)
	}
}

// The regression for the defect #123 found: a stolen session cookie used to survive a password
// change, which made the one remedy a user has do nothing. Asserts the purge (both prior sessions
// dead) and the reissue (the returned token works), i.e. the acting browser stays signed in.
func TestChangePassword_InvalidatesOtherSessions(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	email := testEmail()
	user, tokenA, err := s.Signup(ctx, "1.2.3.4", email, "correct-horse-battery", "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_, tokenB, err := s.Login(ctx, "5.6.7.8", email, "correct-horse-battery")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	newToken, err := s.ChangePassword(ctx, user.ID, user.PasswordHash, "correct-horse-battery", "new-correct-horse-battery")
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id = $1", user.ID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("sessions for user = %d, want 1 (the replacement)", count)
	}

	q := db.New(tx)
	if _, err := q.GetSessionUser(ctx, hashToken(newToken)); err != nil {
		t.Errorf("replacement session should resolve, got %v", err)
	}
	for name, tok := range map[string]string{"signup session": tokenA, "login session": tokenB} {
		if _, err := q.GetSessionUser(ctx, hashToken(tok)); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s should be purged, GetSessionUser err = %v, want pgx.ErrNoRows", name, err)
		}
	}
}
