package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/deckshare/internal/auth"
)

// grantViewer inserts a can_view-only deck_access row directly, the same fixture shape the deck
// tests use for a collaborator who is deliberately short of a permission.
func grantViewer(t *testing.T, tx pgx.Tx, deckID, userID string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, deckID, userID); err != nil {
		t.Fatalf("grant viewer access: %v", err)
	}
}

func TestAccessRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	mateEmail := testEmail()
	loginCookie(t, tx, a, mateEmail, "correct-horse-battery")
	mateID := userID(t, ctx, tx, mateEmail)

	w := doRequest(handler, "POST", "/decks", "name=Shared Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	accessPath := deckPath + "/access"

	w = doRequest(handler, "GET", accessPath, "", ownerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: %s", accessPath, w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), mateEmail) {
		t.Error("collaborator listed before any grant")
	}

	w = doRequest(handler, "POST", accessPath, "email="+mateEmail+"&can_view=on&can_study=on", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST grant status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != accessPath {
		t.Errorf("Location = %q, want %q", loc, accessPath)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access
		WHERE deck_id = $1 AND user_id = $2 AND can_view AND can_study
		  AND NOT can_edit_content AND NOT can_edit_settings AND NOT can_manage_access AND NOT can_delete`,
		deckID, mateID); n != 1 {
		t.Error("grant should have written exactly the chosen flags")
	}

	w = doRequest(handler, "GET", accessPath, "", ownerCookie, "")
	if !strings.Contains(w.Body.String(), mateEmail) {
		t.Errorf("collaborator missing from the list:\n%s", w.Body.String())
	}

	w = doRequest(handler, "POST", accessPath+"/"+mateID+"/edit",
		"can_view=on&can_study=on&can_edit_content=on", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access
		WHERE deck_id = $1 AND user_id = $2 AND can_edit_content`, deckID, mateID); n != 1 {
		t.Error("edit should have set can_edit_content")
	}

	w = doRequest(handler, "POST", accessPath+"/"+mateID+"/delete", "", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deckID, mateID); n != 0 {
		t.Error("revoke should have deleted the row")
	}
}

// Every access route collapses "deck absent", "deck invisible", and "caller lacks
// can_manage_access" into one 404 (CLAUDE.md §10.5, docs/schema.md).
func TestAccessRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	viewerEmail := testEmail()
	viewerCookie := loginCookie(t, tx, a, viewerEmail, "correct-horse-battery")
	viewerID := userID(t, ctx, tx, viewerEmail)
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	managerEmail := testEmail()
	managerCookie := loginCookie(t, tx, a, managerEmail, "correct-horse-battery")
	managerID := userID(t, ctx, tx, managerEmail)
	blindEmail := testEmail()
	blindCookie := loginCookie(t, tx, a, blindEmail, "correct-horse-battery")
	blindID := userID(t, ctx, tx, blindEmail)

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	accessPath := deckPath + "/access"
	grantViewer(t, tx, deckID, viewerID)

	// A non-owner manager: can_view + can_manage_access and nothing else. Managing access is not
	// an owner-only capability -- decks.owner_id is not a permission source (docs/routes.md).
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_manage_access)
		VALUES ($1, $2, true, true)`, deckID, managerID); err != nil {
		t.Fatalf("grant manager access: %v", err)
	}
	// can_manage_access WITHOUT can_view. GetDeckForAccessManage requires both, matching every
	// other authorise-and-fetch query in decks.sql, so this caller cannot read the page.
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_manage_access)
		VALUES ($1, $2, true)`, deckID, blindID); err != nil {
		t.Fatalf("grant view-less manager access: %v", err)
	}

	const nilUUID = "00000000-0000-0000-0000-000000000000"
	tests := []struct {
		name, method, path, body string
		cookie                   *http.Cookie
		wantStatus               int
	}{
		{"no session GET", "GET", accessPath, "", nil, http.StatusSeeOther},
		{"owner GET", "GET", accessPath, "", ownerCookie, http.StatusOK},
		{"non-owner manager GET", "GET", accessPath, "", managerCookie, http.StatusOK},
		{"manager without can_view GET", "GET", accessPath, "", blindCookie, http.StatusNotFound},
		{"viewer GET", "GET", accessPath, "", viewerCookie, http.StatusNotFound},
		{"stranger GET", "GET", accessPath, "", strangerCookie, http.StatusNotFound},
		{"nonexistent deck GET", "GET", "/decks/" + nilUUID + "/access", "", ownerCookie, http.StatusNotFound},
		{"viewer POST grant", "POST", accessPath, "email=" + viewerEmail + "&can_view=on", viewerCookie, http.StatusNotFound},
		{"stranger POST grant", "POST", accessPath, "email=" + viewerEmail + "&can_view=on", strangerCookie, http.StatusNotFound},
		{"viewer POST edit", "POST", accessPath + "/" + viewerID + "/edit", "can_view=on&can_manage_access=on", viewerCookie, http.StatusNotFound},
		{"stranger POST edit", "POST", accessPath + "/" + viewerID + "/edit", "can_view=on&can_manage_access=on", strangerCookie, http.StatusNotFound},
		{"viewer POST delete", "POST", accessPath + "/" + viewerID + "/delete", "", viewerCookie, http.StatusNotFound},
		{"stranger POST delete", "POST", accessPath + "/" + viewerID + "/delete", "", strangerCookie, http.StatusNotFound},
		{"viewer POST reset", "POST", accessPath + "/" + viewerID + "/reset", "", viewerCookie, http.StatusNotFound},
		{"stranger POST reset", "POST", accessPath + "/" + viewerID + "/reset", "", strangerCookie, http.StatusNotFound},
		{"owner POST edit unknown target", "POST", accessPath + "/" + nilUUID + "/edit", "can_view=on", ownerCookie, http.StatusNotFound},
		{"owner POST delete unknown target", "POST", accessPath + "/" + nilUUID + "/delete", "", ownerCookie, http.StatusNotFound},
		{"owner POST edit malformed target", "POST", accessPath + "/not-a-uuid/edit", "can_view=on", ownerCookie, http.StatusNotFound},
		// LockDeckForAccessChange requires can_view alongside can_manage_access, matching
		// GetDeckForAccessManage -- otherwise this caller, already 404'd above on GET, could still
		// edit or revoke another collaborator's access (#83 review).
		{"manager without can_view POST edit", "POST", accessPath + "/" + viewerID + "/edit", "can_view=on&can_manage_access=on", blindCookie, http.StatusNotFound},
		{"manager without can_view POST delete", "POST", accessPath + "/" + viewerID + "/delete", "", blindCookie, http.StatusNotFound},
		{"manager without can_view POST reset", "POST", accessPath + "/" + viewerID + "/reset", "", blindCookie, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := ""
			if tt.method == "POST" {
				origin = "http://example.com"
			}
			w := doRequest(handler, tt.method, tt.path, tt.body, tt.cookie, origin)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}

	// The viewer's own row must be untouched by every rejected request above: a 404 that still
	// wrote is worse than a 200 that didn't.
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access
		WHERE deck_id = $1 AND user_id = $2 AND can_view AND NOT can_manage_access`, deckID, viewerID); n != 1 {
		t.Error("viewer's deck_access row should be unchanged after the rejected requests")
	}
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1`, deckID); n != 4 {
		t.Errorf("deck should still have exactly its 4 fixture rows, got %d -- an unauthorised grant got through", n)
	}
}

func TestAccessRoute_GrantUnknownEmail(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")

	w = doRequest(handler, "POST", deckPath+"/access", "email=nobody-here@example.com&can_view=on", ownerCookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No account with that email") {
		t.Errorf("body should carry the inline form error:\n%s", w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1`, deckID); n != 1 {
		t.Error("no row should have been written for an email with no account")
	}
}

func TestAccessRoute_GrantDuplicate409(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	mateEmail := testEmail()
	loginCookie(t, tx, a, mateEmail, "correct-horse-battery")
	mateID := userID(t, ctx, tx, mateEmail)

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")

	body := "email=" + mateEmail + "&can_view=on"
	if w = doRequest(handler, "POST", deckPath+"/access", body, ownerCookie, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Fatalf("first grant status = %d, want 303: %s", w.Code, w.Body.String())
	}
	w = doRequest(handler, "POST", deckPath+"/access", body+"&can_manage_access=on", ownerCookie, "http://example.com")
	if w.Code != http.StatusConflict {
		t.Fatalf("second grant status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access
		WHERE deck_id = $1 AND user_id = $2 AND can_view AND NOT can_manage_access`, deckID, mateID); n != 1 {
		t.Error("the rejected re-grant must not have changed the existing row")
	}
}

// The last-holder guard is enforced in internal/db (#51 and its own tests); what matters here is
// that the HTTP layer surfaces ErrLastAccessHolder as 409 rather than leaking a 500, and that the
// refused change is rolled back.
func TestAccessRoutes_LastHolderGuard409(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	ownerID := userID(t, ctx, tx, ownerEmail)

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	accessPath := deckPath + "/access"

	t.Run("downgrading the only manage holder", func(t *testing.T) {
		w := doRequest(handler, "POST", accessPath+"/"+ownerID+"/edit", "can_view=on&can_study=on", ownerCookie, "http://example.com")
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		if n := countRows(t, tx, `SELECT count(*) FROM deck_access
			WHERE deck_id = $1 AND user_id = $2 AND can_manage_access AND can_delete`, deckID, ownerID); n != 1 {
			t.Error("refused downgrade must be rolled back")
		}
	})

	t.Run("revoking the only holder", func(t *testing.T) {
		w := doRequest(handler, "POST", accessPath+"/"+ownerID+"/delete", "", ownerCookie, "http://example.com")
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		if n := countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deckID, ownerID); n != 1 {
			t.Error("refused revoke must be rolled back")
		}
	})
}

// A second can_manage_access/can_delete holder makes both changes legal again -- the guard counts
// holders, it does not privilege decks.owner_id.
func TestAccessRoutes_SecondManagerUnblocksRevoke(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	ownerID := userID(t, ctx, tx, ownerEmail)
	mateEmail := testEmail()
	mateCookie := loginCookie(t, tx, a, mateEmail, "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	accessPath := deckPath + "/access"

	w = doRequest(handler, "POST", accessPath,
		"email="+mateEmail+"&can_view=on&can_manage_access=on&can_delete=on", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("grant co-manager status = %d, want 303: %s", w.Code, w.Body.String())
	}

	// The new co-manager can now revoke the original owner's row.
	w = doRequest(handler, "POST", accessPath+"/"+ownerID+"/delete", "", mateCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("co-manager revoke status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM deck_access WHERE deck_id = $1 AND user_id = $2`, deckID, ownerID); n != 0 {
		t.Error("owner's row should have been revoked")
	}
	// And the former owner has lost the deck entirely -- deck_access is the only source of reach.
	if w := doRequest(handler, "GET", deckPath, "", ownerCookie, ""); w.Code != http.StatusNotFound {
		t.Errorf("former owner GET deck status = %d, want 404", w.Code)
	}
}

// Reset deletes a collaborator's user_card_state rows for one deck's cards, scoped to exactly
// that (deck, user) pair -- a same-user row on another deck and an other-user row on the same
// deck must both survive -- and leaves review_log untouched (#189, CLAUDE.md §2.5).
func TestAccessRoute_Reset(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	ownerID := userID(t, ctx, tx, ownerEmail)
	mateEmail := testEmail()
	loginCookie(t, tx, a, mateEmail, "correct-horse-battery")
	mateID := userID(t, ctx, tx, mateEmail)

	deckPathA := setupDeckAndNoteType(t, handler, ownerCookie)
	deckIDA := strings.TrimPrefix(deckPathA, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, ownerCookie, deckPathA, noteTypeID, 1)
	var cardA string
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE deck_id = $1`, deckIDA).Scan(&cardA); err != nil {
		t.Fatalf("lookup card A: %v", err)
	}

	w := doRequest(handler, "POST", "/decks", "name=Deck B", ownerCookie, "http://example.com")
	deckPathB := w.Header().Get("Location")
	deckIDB := strings.TrimPrefix(deckPathB, "/decks/")
	addNotes(t, handler, ownerCookie, deckPathB, noteTypeID, 1)
	var cardB string
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE deck_id = $1`, deckIDB).Scan(&cardB); err != nil {
		t.Fatalf("lookup card B: %v", err)
	}

	if w = doRequest(handler, "POST", deckPathA+"/access",
		"email="+mateEmail+"&can_view=on&can_study=on", ownerCookie, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Fatalf("grant mate access to deck A status = %d: %s", w.Code, w.Body.String())
	}

	// mate's progress on card A (deck A, the reset target), mate's progress on card B (deck B,
	// must survive), and the owner's own progress on card A (different user, same deck, must
	// survive).
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due) VALUES ($1, $2, now())`, mateID, cardA); err != nil {
		t.Fatalf("seed mate/cardA state: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due) VALUES ($1, $2, now())`, mateID, cardB); err != nil {
		t.Fatalf("seed mate/cardB state: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due) VALUES ($1, $2, now())`, ownerID, cardA); err != nil {
		t.Fatalf("seed owner/cardA state: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO review_log
		(user_id, card_id, rating, reviewed_at, state_before, learning_steps_before, elapsed_days_before, scheduled_days_after, review_kind)
		VALUES ($1, $2, 3, now(), 0, 0, 0, 1, 0)`, mateID, cardA); err != nil {
		t.Fatalf("seed review_log row: %v", err)
	}

	w = doRequest(handler, "POST", deckPathA+"/access/"+mateID+"/reset", "", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("reset status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != deckPathA+"/access" {
		t.Errorf("Location = %q, want %q", loc, deckPathA+"/access")
	}

	if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`, mateID, cardA); n != 0 {
		t.Error("mate's state on the reset deck's card should be gone")
	}
	if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`, mateID, cardB); n != 1 {
		t.Error("mate's state on a different deck's card must survive")
	}
	if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`, ownerID, cardA); n != 1 {
		t.Error("a different user's state on the same deck's card must survive")
	}
	if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE user_id = $1 AND card_id = $2`, mateID, cardA); n != 1 {
		t.Error("review_log must be untouched by a reset (CLAUDE.md §2.5)")
	}

	// Resetting a collaborator who never studied this deck is a no-op, not an error.
	w = doRequest(handler, "POST", deckPathA+"/access/"+mateID+"/reset", "", ownerCookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("second reset status = %d, want 303: %s", w.Code, w.Body.String())
	}
}
