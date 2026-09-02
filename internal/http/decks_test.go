package http

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Jolls/enshu/internal/auth"
)

func TestDeckRoutes_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck&description=desc", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /decks status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc == "/decks" {
		t.Fatalf("Location = %q, want /decks/{id}", loc)
	}

	w = doRequest(handler, "GET", loc, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", loc, w.Code)
	}

	w = doRequest(handler, "GET", "/decks", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /decks status = %d, want 200", w.Code)
	}

	w = doRequest(handler, "POST", loc+"/edit", "name=Renamed&description=", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303", w.Code)
	}

	w = doRequest(handler, "POST", loc+"/delete", "", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete status = %d, want 303", w.Code)
	}

	w = doRequest(handler, "GET", loc, "", cookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET deleted deck status = %d, want 404", w.Code)
	}
}

// A deck that exists but is invisible to the caller must 404, never 403 -- one row per route
// asserting exactly that (CLAUDE.md §10.5).
func TestDeckRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	tests := []struct {
		name, method, path, body string
		cookie                   *http.Cookie
		wantStatus               int
	}{
		{"no session GET deck", "GET", deckPath, "", nil, http.StatusSeeOther},
		{"stranger GET deck", "GET", deckPath, "", strangerCookie, http.StatusNotFound},
		{"stranger GET edit", "GET", deckPath + "/edit", "", strangerCookie, http.StatusNotFound},
		{"stranger POST edit", "POST", deckPath + "/edit", "name=Hijacked", strangerCookie, http.StatusNotFound},
		{"stranger POST delete", "POST", deckPath + "/delete", "", strangerCookie, http.StatusNotFound},
		{"nonexistent deck", "GET", "/decks/00000000-0000-0000-0000-000000000000", "", ownerCookie, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := ""
			if tt.method == "POST" {
				origin = "http://example.com"
			}
			w := doRequest(handler, tt.method, tt.path, tt.body, tt.cookie, origin)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestDeckFsrsRoute_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")

	w = doRequest(handler, "POST", deckPath+"/settings/fsrs", "desired_retention=0.8", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != deckPath {
		t.Errorf("Location = %q, want %q", loc, deckPath)
	}

	id := userID(t, ctx, tx, email)
	if n := countRows(t, tx, `SELECT count(*) FROM user_fsrs_params WHERE user_id = $1 AND deck_id = $2 AND desired_retention = 0.8`, id, deckID); n != 1 {
		t.Error("per-deck row should reflect the update")
	}
}

// A caller with can_view but not can_study on the deck must 404, the same collapse-existence
// rule as every other deck route (CLAUDE.md §10.5) -- and no row should be written.
func TestDeckFsrsRoute_DeniesWithoutCanStudy(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	viewerEmail := testEmail()
	viewerCookie := loginCookie(t, tx, a, viewerEmail, "correct-horse-battery")
	viewerID := userID(t, ctx, tx, viewerEmail)

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")

	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, deckID, viewerID); err != nil {
		t.Fatalf("grant viewer access: %v", err)
	}

	w = doRequest(handler, "POST", deckPath+"/settings/fsrs", "desired_retention=0.8", viewerCookie, "http://example.com")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM user_fsrs_params WHERE user_id = $1 AND deck_id = $2`, viewerID, deckID); n != 0 {
		t.Error("no row should have been written without can_study")
	}
}

// The queue summary (#80) counts New, Learning, and Due using the same eligibility rules as the
// review queue itself (reviews.sql). state/due/last_review are set directly rather than through
// grading, since a card graded today is deliberately excluded from today's queue (same-day
// last_review guard) and would not exercise the Learning/Due buckets otherwise.
func TestDeckQueueCounts(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC) // study day: 03-02 04:00Z .. 03-03 04:00Z
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 3)

	rows, err := tx.Query(ctx, `SELECT id FROM cards WHERE deck_id = $1 ORDER BY id`, deckID)
	if err != nil {
		t.Fatalf("list cards: %v", err)
	}
	var cardIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan card id: %v", err)
		}
		cardIDs = append(cardIDs, id)
	}
	rows.Close()
	if len(cardIDs) != 3 {
		t.Fatalf("got %d cards, want 3", len(cardIDs))
	}
	id := userID(t, ctx, tx, email)

	// cardIDs[0] stays unseen (New). cardIDs[1] is mid-learning from yesterday, due now.
	// cardIDs[2] is a review card from yesterday, due now.
	yesterday := clock.Add(-24 * time.Hour)
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due, state, last_review)
		VALUES ($1, $2, $3, 1, $4)`, id, cardIDs[1], clock, yesterday); err != nil {
		t.Fatalf("insert learning state: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due, state, last_review)
		VALUES ($1, $2, $3, 2, $4)`, id, cardIDs[2], clock, yesterday); err != nil {
		t.Fatalf("insert review state: %v", err)
	}

	w := doRequest(handler, "GET", deckPath, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", deckPath, w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "New: 1") || !strings.Contains(body, "Learning: 1") || !strings.Contains(body, "Due: 1") {
		t.Errorf("deck page body missing expected queue summary:\n%s", body)
	}

	w = doRequest(handler, "GET", "/decks", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /decks status = %d", w.Code)
	}
	body := w.Body.String()
	rowRe := regexp.MustCompile(`<td>3</td>\s*<td>1</td>\s*<td>1</td>\s*<td>1</td>`)
	if !rowRe.MatchString(body) {
		t.Errorf("/decks body missing expected Cards/New/Learning/Due row:\n%s", body)
	}
}

// A collaborator with can_view but not can_study must not see queue counts for a deck they
// cannot actually study (#80) -- the same collapse-to-nothing rule the review queue itself
// applies (ListDueCardsForStudy requires can_view AND can_study).
func TestDeckQueueCounts_RequiresCanStudy(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	viewerEmail := testEmail()
	viewerCookie := loginCookie(t, tx, a, viewerEmail, "correct-horse-battery")
	viewerID := userID(t, ctx, tx, viewerEmail)

	deckID, _ := setupOneCard(t, tx, handler, ownerCookie)
	deckPath := "/decks/" + deckID

	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, deckID, viewerID); err != nil {
		t.Fatalf("grant viewer access: %v", err)
	}

	w := doRequest(handler, "GET", deckPath, "", viewerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", deckPath, w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "New: 0") || !strings.Contains(body, "Learning: 0") || !strings.Contains(body, "Due: 0") {
		t.Errorf("view-only collaborator should see zero queue counts, got:\n%s", body)
	}
}

// The per-deck daily new-card limit (#101): editable from the deck edit page, persists, and an
// absent field on a later edit leaves the stored value untouched rather than resetting it.
func TestDeckEditRoute_NewPerDay(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="20"`) {
		t.Fatalf("new deck should default to new_per_day=20, got status %d:\n%s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&new_per_day=5", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if !strings.Contains(w.Body.String(), `value="5"`) {
		t.Errorf("edit page should reflect new_per_day=5:\n%s", w.Body.String())
	}

	t.Run("absent field leaves value untouched", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=", cookie, "http://example.com")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `value="5"`) {
			t.Errorf("new_per_day should remain 5 when the field is absent:\n%s", w.Body.String())
		}
	})

	for _, v := range []string{"abc", "-1", "10000"} {
		t.Run("rejects new_per_day="+v, func(t *testing.T) {
			w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&new_per_day="+v, cookie, "http://example.com")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
			if !strings.Contains(w.Body.String(), `value="5"`) {
				t.Errorf("deck should be unchanged after a rejected new_per_day:\n%s", w.Body.String())
			}
		})
	}
}

func TestDeckEditRoute_RevPerDay(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="200"`) {
		t.Fatalf("new deck should default to rev_per_day=200, got status %d:\n%s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&rev_per_day=50", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if !strings.Contains(w.Body.String(), `value="50"`) {
		t.Errorf("edit page should reflect rev_per_day=50:\n%s", w.Body.String())
	}

	t.Run("absent field leaves value untouched", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=", cookie, "http://example.com")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `value="50"`) {
			t.Errorf("rev_per_day should remain 50 when the field is absent:\n%s", w.Body.String())
		}
	})

	for _, v := range []string{"abc", "-1", "10000"} {
		t.Run("rejects rev_per_day="+v, func(t *testing.T) {
			w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&rev_per_day="+v, cookie, "http://example.com")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
			if !strings.Contains(w.Body.String(), `value="50"`) {
				t.Errorf("deck should be unchanged after a rejected rev_per_day:\n%s", w.Body.String())
			}
		})
	}

	t.Run("new_per_day and rev_per_day update independently", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&new_per_day=7", cookie, "http://example.com")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `value="7"`) || !strings.Contains(w.Body.String(), `value="50"`) {
			t.Errorf("new_per_day should be 7 and rev_per_day should remain 50:\n%s", w.Body.String())
		}
	})
}

func TestDeckEditRoute_DueLookAheadMinutes(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="0"`) {
		t.Fatalf("new deck should default to due_look_ahead_minutes=0, got status %d:\n%s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&due_look_ahead_minutes=45", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if !strings.Contains(w.Body.String(), `value="45"`) {
		t.Errorf("edit page should reflect due_look_ahead_minutes=45:\n%s", w.Body.String())
	}

	t.Run("absent field leaves value untouched", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=", cookie, "http://example.com")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `value="45"`) {
			t.Errorf("due_look_ahead_minutes should remain 45 when the field is absent:\n%s", w.Body.String())
		}
	})

	for _, v := range []string{"abc", "-1", "1441"} {
		t.Run("rejects due_look_ahead_minutes="+v, func(t *testing.T) {
			w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&due_look_ahead_minutes="+v, cookie, "http://example.com")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
			if !strings.Contains(w.Body.String(), `value="45"`) {
				t.Errorf("deck should be unchanged after a rejected due_look_ahead_minutes:\n%s", w.Body.String())
			}
		})
	}
}

func TestDeckEditRoute_RevOrder(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<option value="due" selected>`) {
		t.Fatalf("new deck should default to rev_order=due, got status %d:\n%s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&rev_order=random", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if !strings.Contains(w.Body.String(), `<option value="random" selected>`) {
		t.Errorf("edit page should reflect rev_order=random:\n%s", w.Body.String())
	}

	t.Run("absent field leaves value untouched", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=", cookie, "http://example.com")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `<option value="random" selected>`) {
			t.Errorf("rev_order should remain random when the field is absent:\n%s", w.Body.String())
		}
	})

	t.Run("rejects unrecognised rev_order", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&rev_order=bogus", cookie, "http://example.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `<option value="random" selected>`) {
			t.Errorf("deck should be unchanged after a rejected rev_order:\n%s", w.Body.String())
		}
	})
}

func TestDeckEditRoute_Priority(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=My Deck", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<option value="due" selected>`) {
		t.Fatalf("new deck should default to priority=due, got status %d:\n%s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&priority=mixed", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}
	w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
	if !strings.Contains(w.Body.String(), `<option value="mixed" selected>`) {
		t.Errorf("edit page should reflect priority=mixed:\n%s", w.Body.String())
	}

	t.Run("absent field leaves value untouched", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=", cookie, "http://example.com")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303: %s", w.Code, w.Body.String())
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `<option value="mixed" selected>`) {
			t.Errorf("priority should remain mixed when the field is absent:\n%s", w.Body.String())
		}
	})

	t.Run("rejects unrecognised priority", func(t *testing.T) {
		w := doRequest(handler, "POST", deckPath+"/edit", "name=My Deck&description=&priority=bogus", cookie, "http://example.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		w = doRequest(handler, "GET", deckPath+"/edit", "", cookie, "")
		if !strings.Contains(w.Body.String(), `<option value="mixed" selected>`) {
			t.Errorf("deck should be unchanged after a rejected priority:\n%s", w.Body.String())
		}
	})
}

// "Left today" (#137) reflects the daily new-card cap, not just the raw New queue count: a deck
// with more New cards than its new.perDay allowance shows a smaller Left figure than New on both
// the deck page and the /decks list.
func TestDeckQueueCounts_LeftRespectsNewPerDayCap(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 3) // 3 New cards, cap will be set to 1

	w := doRequest(handler, "POST", deckPath+"/edit", "name=D&description=&new_per_day=1", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}

	w = doRequest(handler, "GET", deckPath, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", deckPath, w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "New: 1") || !strings.Contains(body, "Left today: 1") {
		t.Errorf("deck page should show New: 1 (capped by new_per_day=1, #106) and Left today: 1:\n%s", body)
	}

	w = doRequest(handler, "GET", "/decks", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /decks status = %d", w.Code)
	}
	body := w.Body.String()
	rowRe := regexp.MustCompile(`<td>3</td>\s*<td>1</td>\s*<td>0</td>\s*<td>0</td>\s*<td>1</td>`)
	if !rowRe.MatchString(body) {
		t.Errorf("/decks body missing expected Cards/New/Learning/Due/Left row (New and Left both capped to 1, #106):\n%s", body)
	}
}

// #106: once the day's new-card allowance is fully used up, the displayed New count drops to 0
// -- it must not keep reporting the deck's total unseen-card count -- while Due, which the daily
// new-card limit never touches, is unaffected.
func TestDeckQueueCounts_NewCappedToZeroWhenAllowanceExhausted(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC) // study day: 03-02 04:00Z .. 03-03 04:00Z
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 3)

	rows, err := tx.Query(ctx, `SELECT id FROM cards WHERE deck_id = $1 ORDER BY id`, deckID)
	if err != nil {
		t.Fatalf("list cards: %v", err)
	}
	var cardIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan card id: %v", err)
		}
		cardIDs = append(cardIDs, id)
	}
	rows.Close()
	if len(cardIDs) != 3 {
		t.Fatalf("got %d cards, want 3", len(cardIDs))
	}
	id := userID(t, ctx, tx, email)

	w := doRequest(handler, "POST", deckPath+"/edit", "name=D&description=&new_per_day=1", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want 303: %s", w.Code, w.Body.String())
	}

	// cardIDs[0] was introduced and answered earlier today, consuming the day's one-card new
	// allowance -- already studied today, so it drops out of today's queue entirely (same rule
	// ListDueCardsForStudy applies). cardIDs[1] stays unseen (New, but the allowance is gone).
	// cardIDs[2] is a review card from yesterday, due now -- Due must be unaffected by the cap.
	if _, err := tx.Exec(ctx, `INSERT INTO review_log (user_id, card_id, rating, reviewed_at, state_before,
		learning_steps_before, elapsed_days_before, scheduled_days_after, review_kind)
		VALUES ($1, $2, 3, $3, 0, 0, 0, 0, 0)`, id, cardIDs[0], clock); err != nil {
		t.Fatalf("insert review_log row: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due, state, last_review)
		VALUES ($1, $2, $3, 2, $4)`, id, cardIDs[0], clock, clock); err != nil {
		t.Fatalf("insert review state for introduced card: %v", err)
	}
	yesterday := clock.Add(-24 * time.Hour)
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state (user_id, card_id, due, state, last_review)
		VALUES ($1, $2, $3, 2, $4)`, id, cardIDs[2], clock, yesterday); err != nil {
		t.Fatalf("insert due review state: %v", err)
	}

	w = doRequest(handler, "GET", deckPath, "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", deckPath, w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "New: 0") || !strings.Contains(body, "Due: 1") {
		t.Errorf("deck page should show New: 0 (allowance exhausted) and Due: 1 (unaffected):\n%s", body)
	}

	w = doRequest(handler, "GET", "/decks", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /decks status = %d", w.Code)
	}
	body := w.Body.String()
	rowRe := regexp.MustCompile(`<td>3</td>\s*<td>0</td>\s*<td>0</td>\s*<td>1</td>\s*<td>1</td>`)
	if !rowRe.MatchString(body) {
		t.Errorf("/decks body missing expected Cards/New/Learning/Due/Left row (New: 0, Due: 1):\n%s", body)
	}
}

// #182: a 404 from a deck route renders the styled not_found page (header/nav, vague copy) via
// notFoundPage/handleQueryErrPage, not the bare-text response the pre-#182 notFound wrote. One
// check is enough to cover the wiring; it does not need repeating per route (docs/plans/
// 182-deck-edit-permission-gating.md). httptest.ResponseRecorder does not reproduce the real
// server's Content-Type sniffing here (render calls WriteHeader before Write, so the recorder's
// own sniff-on-first-Write never fires) -- render body content is the reliable signal instead.
func TestDeckRoutes_NotFoundRendersStyledPage(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "GET", "/decks/00000000-0000-0000-0000-000000000000", "", cookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	body := w.Body.String()
	if strings.TrimSpace(body) == "not found" {
		t.Errorf("body is the old bare-text 404, want the styled not_found page:\n%s", body)
	}
	if !strings.Contains(body, "don't have access") || !strings.Contains(body, "Back to decks") {
		t.Errorf("body should render the styled not_found page:\n%s", body)
	}
}

// #182: deck.html and decks.html hide the edit/manage-access/add-note/import-AI controls and the
// per-note edit/delete controls -- display only, the query-layer check is unchanged -- from a
// caller who lacks the corresponding deck_access flag.
func TestDeckRoutes_ControlsGatedByPermission(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, ownerCookie, deckPath, noteTypeID, 1)
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE deck_id = $1`, deckID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	w := doRequest(handler, "GET", deckPath, "", ownerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("owner GET deck status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Edit deck", "Manage access", "Add note", "Import via AI", "/notes/" + noteID + "/edit"} {
		if !strings.Contains(body, want) {
			t.Errorf("owner (all flags) body missing %q:\n%s", want, body)
		}
	}

	w = doRequest(handler, "GET", "/decks", "", ownerCookie, "")
	if !strings.Contains(w.Body.String(), "/decks/"+deckID+"/edit") {
		t.Errorf("owner /decks body missing edit link for their own deck:\n%s", w.Body.String())
	}

	viewerEmail := testEmail()
	viewerCookie := loginCookie(t, tx, a, viewerEmail, "correct-horse-battery")
	viewerID := userID(t, ctx, tx, viewerEmail)
	grantViewer(t, tx, deckID, viewerID)

	w = doRequest(handler, "GET", deckPath, "", viewerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("viewer GET deck status = %d", w.Code)
	}
	body = w.Body.String()
	for _, unwanted := range []string{deckPath + "/edit", deckPath + "/access", deckPath + "/notes/new", "/notes/" + noteID + "/edit"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("view-only collaborator body should not contain %q:\n%s", unwanted, body)
		}
	}

	w = doRequest(handler, "GET", "/decks", "", viewerCookie, "")
	if strings.Contains(w.Body.String(), "/decks/"+deckID+"/edit") {
		t.Errorf("view-only collaborator /decks body should not contain edit link:\n%s", w.Body.String())
	}

	settingsEmail := testEmail()
	settingsCookie := loginCookie(t, tx, a, settingsEmail, "correct-horse-battery")
	settingsID := userID(t, ctx, tx, settingsEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_edit_settings) VALUES ($1, $2, true, true)`, deckID, settingsID); err != nil {
		t.Fatalf("grant can_edit_settings access: %v", err)
	}
	w = doRequest(handler, "GET", deckPath, "", settingsCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("can_edit_settings collaborator GET deck status = %d", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, deckPath+"/edit") {
		t.Errorf("can_edit_settings collaborator body should contain the edit link:\n%s", body)
	}
	for _, unwanted := range []string{deckPath + "/access", deckPath + "/notes/new"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("can_edit_settings-only collaborator body should not contain %q:\n%s", unwanted, body)
		}
	}
}

// #182: deck_edit.html's Delete button needs can_delete on top of the can_edit_settings that
// already gates reaching the page.
func TestDeckEditRoute_DeleteButtonGatedByCanDelete(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	deleteAction := `action="` + deckPath + `/delete"`

	settingsOnlyEmail := testEmail()
	settingsOnlyCookie := loginCookie(t, tx, a, settingsOnlyEmail, "correct-horse-battery")
	settingsOnlyID := userID(t, ctx, tx, settingsOnlyEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_edit_settings) VALUES ($1, $2, true, true)`, deckID, settingsOnlyID); err != nil {
		t.Fatalf("grant can_edit_settings access: %v", err)
	}
	w = doRequest(handler, "GET", deckPath+"/edit", "", settingsOnlyCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("can_edit_settings collaborator GET edit status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), deleteAction) {
		t.Errorf("collaborator without can_delete should not see the delete button:\n%s", w.Body.String())
	}

	bothEmail := testEmail()
	bothCookie := loginCookie(t, tx, a, bothEmail, "correct-horse-battery")
	bothID := userID(t, ctx, tx, bothEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_edit_settings, can_delete) VALUES ($1, $2, true, true, true)`, deckID, bothID); err != nil {
		t.Fatalf("grant can_edit_settings+can_delete access: %v", err)
	}
	w = doRequest(handler, "GET", deckPath+"/edit", "", bothCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("both-flags collaborator GET edit status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), deleteAction) {
		t.Errorf("collaborator with can_delete should see the delete button:\n%s", w.Body.String())
	}
}

func TestDeckRoutes_DuplicateName_409(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	doRequest(handler, "POST", "/decks", "name=Dup", cookie, "http://example.com")
	w := doRequest(handler, "POST", "/decks", "name=Dup", cookie, "http://example.com")
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}
