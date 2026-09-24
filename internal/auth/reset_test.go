package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/jackc/pgx/v5"

	"github.com/Jolls/deckshare/internal/db"
)

const resetOldPassword = "correct-horse-battery"

// signupForReset creates an account with resetOldPassword and returns it with its email.
func signupForReset(t *testing.T, s *Service) (db.User, string) {
	t.Helper()
	email := testEmail()
	user, _, err := s.Signup(context.Background(), "1.2.3.4", email, resetOldPassword, "Ada")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	return user, email
}

func mintReset(t *testing.T, s *Service, email string) string {
	t.Helper()
	_, raw, err := s.CreatePasswordReset(context.Background(), email)
	if err != nil {
		t.Fatalf("CreatePasswordReset: %v", err)
	}
	return raw
}

func passwordVerifies(t *testing.T, tx pgx.Tx, user db.User, password string) bool {
	t.Helper()
	reloaded, err := db.New(tx).GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	match, err := argon2id.ComparePasswordAndHash(password, reloaded.PasswordHash)
	if err != nil {
		t.Fatalf("ComparePasswordAndHash: %v", err)
	}
	return match
}

func assertPasswordUnchanged(t *testing.T, tx pgx.Tx, user db.User) {
	t.Helper()
	reloaded, err := db.New(tx).GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if reloaded.PasswordHash != user.PasswordHash {
		t.Error("password_hash should be unchanged")
	}
}

func TestCreatePasswordReset_StoresOnlyTheHash(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)

	got, raw, err := s.CreatePasswordReset(ctx, email)
	if err != nil {
		t.Fatalf("CreatePasswordReset: %v", err)
	}
	if got.ID != user.ID {
		t.Error("CreatePasswordReset should return the account at that email")
	}

	if n := countRows(t, tx, `SELECT count(*) FROM password_reset_tokens WHERE id = $1`, raw); n != 0 {
		t.Error("the raw token must never be stored")
	}
	var id string
	var expiresAt time.Time
	if err := tx.QueryRow(ctx,
		`SELECT id, expires_at FROM password_reset_tokens WHERE user_id = $1`, user.ID,
	).Scan(&id, &expiresAt); err != nil {
		t.Fatalf("select reset token for user: %v", err)
	}
	if id != hashToken(raw) {
		t.Errorf("stored id = %q, want SHA-256 hex of the raw token", id)
	}
	if until := time.Until(expiresAt); until < 23*time.Hour || until > PasswordResetLifetime {
		t.Errorf("expires_at ~24h out, got %v", until)
	}
}

func TestCreatePasswordReset_UnknownEmail(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)

	_, _, err := s.CreatePasswordReset(context.Background(), testEmail())
	if !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("CreatePasswordReset error = %v, want ErrNoSuchUser", err)
	}
	// created_at defaults to now(), which is fixed for the life of a transaction -- so this
	// counts only rows written by this test's transaction, not the whole table.
	if n := countRows(t, tx, `SELECT count(*) FROM password_reset_tokens WHERE created_at = now()`); n != 0 {
		t.Errorf("reset token rows written = %d, want 0", n)
	}
}

func TestCreatePasswordReset_InvalidEmail(t *testing.T) {
	s, err := New(nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _, err = s.CreatePasswordReset(context.Background(), "not-an-email")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("CreatePasswordReset error = %v, want *ValidationError", err)
	}
}

func TestCreatePasswordReset_SupersedesOutstandingTokens(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)

	first := mintReset(t, s, email)
	second := mintReset(t, s, email)

	if n := countRows(t, tx, `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, user.ID); n != 1 {
		t.Errorf("reset tokens for user = %d, want 1", n)
	}
	if _, err := s.ResetPassword(ctx, "1.2.3.4", first, "brand-new-password"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("ResetPassword with superseded link error = %v, want ErrInvalidResetToken", err)
	}
	if _, err := s.ResetPassword(ctx, "1.2.3.4", second, "brand-new-password"); err != nil {
		t.Fatalf("ResetPassword with newest link: %v", err)
	}
}

func TestResetPassword_HappyPath(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)
	raw := mintReset(t, s, email)

	token, err := s.ResetPassword(ctx, "1.2.3.4", raw, "brand-new-password")
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}

	if !passwordVerifies(t, tx, user, "brand-new-password") {
		t.Error("new password should verify")
	}
	if passwordVerifies(t, tx, user, resetOldPassword) {
		t.Error("old password should no longer verify")
	}
	row, err := db.New(tx).GetSessionUser(ctx, hashToken(token))
	if err != nil {
		t.Fatalf("GetSessionUser: %v", err)
	}
	if row.User.ID != user.ID {
		t.Error("returned session should belong to the reset account")
	}
}

func TestResetPassword_IsSingleUse(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)
	raw := mintReset(t, s, email)

	if _, err := s.ResetPassword(ctx, "1.2.3.4", raw, "first-new-password"); err != nil {
		t.Fatalf("first ResetPassword: %v", err)
	}
	if _, err := s.ResetPassword(ctx, "1.2.3.4", raw, "second-new-password"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("second ResetPassword error = %v, want ErrInvalidResetToken", err)
	}
	if !passwordVerifies(t, tx, user, "first-new-password") {
		t.Error("password should still be the one the first reset set")
	}
}

// A holder who changes their own password must also lock out a reset link that leaked.
func TestChangePassword_RevokesOutstandingResetLink(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)
	raw := mintReset(t, s, email)

	if _, err := s.ChangePassword(ctx, user.ID, user.PasswordHash, resetOldPassword, "holder-chose-this"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := s.ResetPassword(ctx, "1.2.3.4", raw, "attacker-chose-this"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("ResetPassword after ChangePassword error = %v, want ErrInvalidResetToken", err)
	}
	if !passwordVerifies(t, tx, user, "holder-chose-this") {
		t.Error("password should still be the one ChangePassword set")
	}
}

func TestResetPassword_Expired(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)
	raw := mintReset(t, s, email)

	if _, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET expires_at = now() - interval '1 hour' WHERE id = $1`,
		hashToken(raw),
	); err != nil {
		t.Fatalf("expire reset token: %v", err)
	}

	if ok, err := s.PasswordResetValid(ctx, raw); err != nil || ok {
		t.Errorf("PasswordResetValid = %v, %v; want false, nil", ok, err)
	}
	if _, err := s.ResetPassword(ctx, "1.2.3.4", raw, "brand-new-password"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("ResetPassword error = %v, want ErrInvalidResetToken", err)
	}
	assertPasswordUnchanged(t, tx, user)
}

// Same regression shape as TestChangePassword_InvalidatesOtherSessions: a reset is the remedy
// for a compromised account, so every prior session -- including a stolen one -- must die.
func TestResetPassword_PurgesAllSessions(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)

	_, tokenA, err := s.Login(ctx, "1.2.3.4", email, resetOldPassword)
	if err != nil {
		t.Fatalf("Login A: %v", err)
	}
	_, tokenB, err := s.Login(ctx, "5.6.7.8", email, resetOldPassword)
	if err != nil {
		t.Fatalf("Login B: %v", err)
	}
	raw := mintReset(t, s, email)

	newToken, err := s.ResetPassword(ctx, "1.2.3.4", raw, "brand-new-password")
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}

	if n := countRows(t, tx, `SELECT count(*) FROM sessions WHERE user_id = $1`, user.ID); n != 1 {
		t.Errorf("sessions for user = %d, want 1 (the replacement)", n)
	}
	q := db.New(tx)
	if _, err := q.GetSessionUser(ctx, hashToken(newToken)); err != nil {
		t.Errorf("replacement session should resolve, got %v", err)
	}
	for name, tok := range map[string]string{"login session A": tokenA, "login session B": tokenB} {
		if _, err := q.GetSessionUser(ctx, hashToken(tok)); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s should be purged, GetSessionUser err = %v, want pgx.ErrNoRows", name, err)
		}
	}
}

// Guards the validate-before-consume ordering: a typo'd short password must not burn the link.
func TestResetPassword_ShortPasswordDoesNotConsumeToken(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)
	raw := mintReset(t, s, email)

	_, err := s.ResetPassword(ctx, "1.2.3.4", raw, "short77")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("ResetPassword error = %v, want *ValidationError", err)
	}
	assertPasswordUnchanged(t, tx, user)

	if _, err := s.ResetPassword(ctx, "1.2.3.4", raw, "brand-new-password"); err != nil {
		t.Fatalf("ResetPassword after a rejected short password: %v", err)
	}
}

func TestResetPassword_GarbageToken(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	user, email := signupForReset(t, s)
	raw := mintReset(t, s, email)

	for _, garbage := range []string{"", "not-a-real-reset-token"} {
		if _, err := s.ResetPassword(ctx, "1.2.3.4", garbage, "brand-new-password"); !errors.Is(err, ErrInvalidResetToken) {
			t.Errorf("ResetPassword(%q) error = %v, want ErrInvalidResetToken", garbage, err)
		}
	}
	assertPasswordUnchanged(t, tx, user)
	if ok, err := s.PasswordResetValid(ctx, raw); err != nil || !ok {
		t.Errorf("the account's real link should be untouched: PasswordResetValid = %v, %v", ok, err)
	}
}

func TestResetPassword_OnlyTouchesItsOwnUser(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	_, emailA := signupForReset(t, s)
	userB, emailB := signupForReset(t, s)
	rawA := mintReset(t, s, emailA)
	rawB := mintReset(t, s, emailB)
	sessionsB := countRows(t, tx, `SELECT count(*) FROM sessions WHERE user_id = $1`, userB.ID)

	if _, err := s.ResetPassword(ctx, "1.2.3.4", rawA, "brand-new-password"); err != nil {
		t.Fatalf("ResetPassword A: %v", err)
	}

	assertPasswordUnchanged(t, tx, userB)
	if n := countRows(t, tx, `SELECT count(*) FROM sessions WHERE user_id = $1`, userB.ID); n != sessionsB {
		t.Errorf("B's sessions = %d, want %d (untouched)", n, sessionsB)
	}
	if ok, err := s.PasswordResetValid(ctx, rawB); err != nil || !ok {
		t.Errorf("B's outstanding link should survive A's reset: PasswordResetValid = %v, %v", ok, err)
	}
}

func TestResetPassword_RateLimited(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	var last error
	for i := 0; i <= resetPasswordIPLimit; i++ {
		_, last = s.ResetPassword(ctx, "1.2.3.4", "not-a-real-reset-token", "brand-new-password")
	}
	var rle *RateLimitError
	if !errors.As(last, &rle) {
		t.Fatalf("attempt %d error = %v, want *RateLimitError", resetPasswordIPLimit+1, last)
	}
}
