package db

import (
	"context"
	"testing"
)

// GrantDeckAccess embeds the caller's own can_view+can_manage_access as part of the INSERT
// statement (deck_access.sql), rather than trusting a separate, earlier authorization read --
// the same TOCTOU concern UpdateDeck's embedded WHERE and LockDeckForAccessChange's row lock
// exist to close. A caller lacking either flag must insert nothing and report zero rows, the
// same "caller lacks permission" signal RevokeDeckAccess/SetDeckAccess give via pgx.ErrNoRows
// (case 17, TestSetDeckAccess_CallerLacksManageAccess).
func TestGrantDeckAccess_CallerLacksAuthorization(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	owner := mustUser(t, tx)
	caller := mustUser(t, tx)
	target := mustUser(t, tx)
	deck := mustDeck(t, tx, owner)
	mustDeckAccess(t, tx, deck, owner, fullAccess())
	// can_manage_access without can_view -- the exact gap #83's review flagged.
	mustDeckAccess(t, tx, deck, caller, accessFlags{canManageAccess: true})

	q := New(tx)
	rows, err := q.GrantDeckAccess(ctx, GrantDeckAccessParams{
		DeckID: deck, CallerUserID: caller, TargetUserID: target,
		CanView: true,
	})
	if err != nil {
		t.Fatalf("GrantDeckAccess: %v", err)
	}
	if rows != 0 {
		t.Errorf("rows = %d, want 0 for an unauthorised caller", rows)
	}
	if countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deck, target) != 0 {
		t.Error("target should have no deck_access row after an unauthorised grant")
	}
}

// The success path, for contrast: a caller with both flags grants successfully in one statement.
func TestGrantDeckAccess_Authorized(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	owner := mustUser(t, tx)
	target := mustUser(t, tx)
	deck := mustDeck(t, tx, owner)
	mustDeckAccess(t, tx, deck, owner, fullAccess())

	q := New(tx)
	rows, err := q.GrantDeckAccess(ctx, GrantDeckAccessParams{
		DeckID: deck, CallerUserID: owner, TargetUserID: target,
		CanView: true, CanStudy: true,
	})
	if err != nil {
		t.Fatalf("GrantDeckAccess: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
	if countRows(t, tx, `SELECT count(*) FROM deck_access
		WHERE deck_id = $1 AND user_id = $2 AND can_view AND can_study`, deck, target) != 1 {
		t.Error("target's deck_access row should carry the granted flags")
	}
}
