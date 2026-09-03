package auth

import (
	"context"
	"testing"

	"github.com/Jolls/deckshare/internal/db"
)

func TestDeleteExpiredSessions(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	email1 := testEmail()
	var userID any
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Test') RETURNING id`,
		email1,
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	expired := "expired-session"
	expiringSoon := "expiring-soon-session"
	live := "live-session"
	for _, tt := range []struct {
		id     string
		offset string
	}{
		{expired, "now() - interval '1 hour'"},
		{expiringSoon, "now() - interval '1 second'"},
		{live, "now() + interval '1 day'"},
	} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, `+tt.offset+`)`,
			tt.id, userID,
		); err != nil {
			t.Fatalf("insert session %s: %v", tt.id, err)
		}
	}

	q := db.New(tx)
	n, err := q.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if n != 2 {
		t.Errorf("rows deleted = %d, want 2", n)
	}

	if got := countRows(t, tx, `SELECT count(*) FROM sessions WHERE id = $1`, expired); got != 0 {
		t.Error("expired session should be gone")
	}
	if got := countRows(t, tx, `SELECT count(*) FROM sessions WHERE id = $1`, expiringSoon); got != 0 {
		t.Error("just-expired session should be gone")
	}
	if got := countRows(t, tx, `SELECT count(*) FROM sessions WHERE id = $1`, live); got != 1 {
		t.Error("live session should survive")
	}
	if got := countRows(t, tx, `SELECT count(*) FROM users WHERE id = $1`, userID); got != 1 {
		t.Error("user row should be untouched")
	}
}
