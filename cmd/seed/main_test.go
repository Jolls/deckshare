package main

import (
	"strings"
	"testing"
)

// run() must refuse before touching the database; DATABASE_URL is deliberately bogus so a
// misplaced guard would fail differently (or connect) instead of returning the refusal.
func TestRun_RefusesWithoutOptIn(t *testing.T) {
	t.Setenv("DECKSHARE_ALLOW_SEED", "")
	t.Setenv("DATABASE_URL", "postgres://invalid.invalid/none")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "refusing to seed") {
		t.Errorf("run() error = %v, want the refusal", err)
	}
}

// #216: seed creates accounts with a known password, so it must refuse to run without an
// explicit opt-in rather than trusting DATABASE_URL alone.
func TestSeedAllowed(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{{"", false}, {"0", false}, {"true", false}, {"1", true}} {
		t.Setenv("DECKSHARE_ALLOW_SEED", tc.val)
		if got := seedAllowed(); got != tc.want {
			t.Errorf("DECKSHARE_ALLOW_SEED=%q: seedAllowed() = %v, want %v", tc.val, got, tc.want)
		}
	}
}
