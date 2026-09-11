package db

import (
	"context"
	"testing"
)

// TestNewPool_StatementTimeout verifies docs/plans/213-timeouts.md Decision 3's AfterConnect
// hook actually ran: a typo in the SET statement wouldn't otherwise surface until a query hangs
// in production. DB-backed: skipped unless DATABASE_URL is set (testPool, CLAUDE.md §16).
func TestNewPool_StatementTimeout(t *testing.T) {
	tx := beginTx(t)

	var got string
	if err := tx.QueryRow(context.Background(), "SHOW statement_timeout").Scan(&got); err != nil {
		t.Fatalf("SHOW statement_timeout: %v", err)
	}
	if got != "30s" {
		t.Errorf("statement_timeout = %q, want %q", got, "30s")
	}
}
