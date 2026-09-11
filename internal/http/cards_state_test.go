package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// TestCardStateRoutes_GoldenPath covers the never-seen-card upsert, suspend/unsuspend, bury, and
// flag paths (#223) -- all writes to user_card_state.suspended/.buried_until/.flag, never to
// review_log (§2.7: these are settings, not reviews).
func TestCardStateRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	studentEmail := testEmail()
	studentCookie := loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	statePath := "/decks/" + deckID + "/cards/state"

	// Never-seen card, suspend: no prior user_card_state row -- this must upsert, not update.
	form := url.Values{}
	form.Set("cardId", cardID)
	form.Set("suspend", "1")
	w := doRequest(handler, "POST", statePath, form.Encode(), studentCookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("suspend never-seen card status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var suspended bool
	var state int16
	if err := tx.QueryRow(ctx, `SELECT suspended, state FROM user_card_state WHERE user_id = $1 AND card_id = $2`,
		studentID, cardID).Scan(&suspended, &state); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if !suspended {
		t.Error("suspended should be true")
	}
	if state != 0 {
		t.Errorf("state = %d, want 0 (new-card default)", state)
	}
	var dueValid bool
	if err := tx.QueryRow(ctx, `SELECT due IS NOT NULL FROM user_card_state WHERE user_id = $1 AND card_id = $2`,
		studentID, cardID).Scan(&dueValid); err != nil {
		t.Fatalf("read back due: %v", err)
	}
	if !dueValid {
		t.Error("due should be set (not NULL) on first insert")
	}

	// Unsuspend the same row.
	form = url.Values{}
	form.Set("cardId", cardID)
	form.Set("unsuspend", "1")
	w = doRequest(handler, "POST", statePath, form.Encode(), studentCookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("unsuspend status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if err := tx.QueryRow(ctx, `SELECT suspended FROM user_card_state WHERE user_id = $1 AND card_id = $2`,
		studentID, cardID).Scan(&suspended); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if suspended {
		t.Error("suspended should be false after unsuspend")
	}

	// Bury: buried_until should land on the same day GetStudyDayWindow computes as "tomorrow",
	// derived by calling the same query rather than re-deriving the arithmetic in Go with
	// time.Now() (CLAUDE.md §9 day-boundary rule; the plan's own guidance).
	var studentUUID pgtype.UUID
	if err := studentUUID.Scan(studentID); err != nil {
		t.Fatalf("scan student uuid: %v", err)
	}
	q := db.New(tx)
	window, err := q.GetStudyDayWindow(ctx, db.GetStudyDayWindowParams{
		UserID: studentUUID, Now: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("GetStudyDayWindow: %v", err)
	}
	wantTomorrow := window.StudyDayStart.Time.Add(24 * time.Hour)

	form = url.Values{}
	form.Set("cardId", cardID)
	form.Set("bury", "1")
	w = doRequest(handler, "POST", statePath, form.Encode(), studentCookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("bury status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var buriedMatches bool
	if err := tx.QueryRow(ctx,
		`SELECT buried_until = $3::timestamptz::date FROM user_card_state WHERE user_id = $1 AND card_id = $2`,
		studentID, cardID, wantTomorrow).Scan(&buriedMatches); err != nil {
		t.Fatalf("read back buried_until: %v", err)
	}
	if !buriedMatches {
		t.Error("buried_until should equal GetStudyDayWindow's study_day_start + 24h, cast to date")
	}

	// Flag: set to a value 0-7.
	form = url.Values{}
	form.Set("cardId", cardID)
	form.Set("flag", "3")
	w = doRequest(handler, "POST", statePath, form.Encode(), studentCookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("flag status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var flag int16
	if err := tx.QueryRow(ctx, `SELECT flag FROM user_card_state WHERE user_id = $1 AND card_id = $2`,
		studentID, cardID).Scan(&flag); err != nil {
		t.Fatalf("read back flag: %v", err)
	}
	if flag != 3 {
		t.Errorf("flag = %d, want 3", flag)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, ok := resp["flag"].(float64); !ok || got != 3 {
		t.Errorf("response flag = %v, want 3", resp["flag"])
	}
}

// TestCardStateRoutes_FlagRange asserts the handler pre-validates flag range in Go (400), before
// the query (and its CHECK constraint) ever runs.
func TestCardStateRoutes_FlagRange(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	studentEmail := testEmail()
	studentCookie := loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	statePath := "/decks/" + deckID + "/cards/state"
	for _, badFlag := range []string{"8", "-1"} {
		form := url.Values{}
		form.Set("cardId", cardID)
		form.Set("flag", badFlag)
		w := doRequest(handler, "POST", statePath, form.Encode(), studentCookie, "http://example.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("flag=%s status = %d, want 400: %s", badFlag, w.Code, w.Body.String())
		}
	}
	if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1 AND card_id = $2`,
		studentID, cardID); n != 0 {
		t.Error("an out-of-range flag must not have written a row")
	}
}

// TestCardStateRoutes_AccessControl mirrors TestFlagRoutes_AccessControl's shape (CLAUDE.md
// §10.5): stranger/view-only/manager-without-can_study all collapse to 404, and a card real but
// belonging to a different deck than the URL's {id} also 404s.
func TestCardStateRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	ownerID := userID(t, ctx, tx, ownerEmail)
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	viewOnlyEmail := testEmail()
	viewOnlyCookie := loginCookie(t, tx, a, viewOnlyEmail, "correct-horse-battery")
	viewOnlyID := userID(t, ctx, tx, viewOnlyEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`,
		deckID, viewOnlyID); err != nil {
		t.Fatalf("grant view-only access: %v", err)
	}

	studentEmail := testEmail()
	studentCookie := loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	managerEmail := testEmail()
	managerCookie := loginCookie(t, tx, a, managerEmail, "correct-horse-battery")
	managerID := userID(t, ctx, tx, managerEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_manage_access) VALUES ($1, $2, true, true)`,
		deckID, managerID); err != nil {
		t.Fatalf("grant manager access: %v", err)
	}

	statePath := "/decks/" + deckID + "/cards/state"
	form := url.Values{}
	form.Set("cardId", cardID)
	form.Set("suspend", "1")

	tests := []struct {
		name       string
		cookie     *http.Cookie
		wantStatus int
	}{
		{"stranger", strangerCookie, http.StatusNotFound},
		{"view-only (no can_study)", viewOnlyCookie, http.StatusNotFound},
		{"manager (can_manage_access, no can_study)", managerCookie, http.StatusNotFound},
		{"student", studentCookie, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "POST", statePath, form.Encode(), tt.cookie, "http://example.com")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}

	// A card real but belonging to a different deck than the URL's {id} must 404 -- the URL's
	// deck id is bound into GetCardForFlag's join, same as flags.go reuses it for.
	w := doRequest(handler, "POST", "/decks", "name=Other Deck", ownerCookie, "http://example.com")
	otherDeckID := w.Header().Get("Location")
	otherDeckID = otherDeckID[len("/decks/"):]
	w = doRequest(handler, "POST", "/decks/"+otherDeckID+"/cards/state", form.Encode(), ownerCookie, "http://example.com")
	if w.Code != http.StatusNotFound {
		t.Errorf("suspending deck A's card via deck B's URL status = %d, want 404: %s", w.Code, w.Body.String())
	}
	// Scoped to the owner's own row: the earlier "student" subtest legitimately suspended this
	// same card for studentID, so an unscoped card_id-only count would find that unrelated row
	// and misread it as evidence the cross-deck bypass succeeded.
	if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1 AND user_id = $2 AND suspended`,
		cardID, ownerID); n != 0 {
		t.Error("the cross-deck suspend attempt must not have suspended the card")
	}
}
