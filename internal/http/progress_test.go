package http

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// TestProgressTemplateRenders exercises progress.html directly against representative data,
// including the nil-pointer "no data yet" rows (§2.3 of the plan) -- a template field-path typo
// only surfaces at Execute time, and every other progress test below needs a live database, so
// this one guards the template alone.
func TestProgressTemplateRenders(t *testing.T) {
	pages, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	deck := db.GetDeckForProgressRow{Name: "Spanish 101", CanEditContent: true}
	students := []studentProgressRow{
		{Email: "seen@example.com", DisplayName: "Seen Student", Seen: 10, New: 5, Learning: 2, Due: 3,
			RecallDisplay: "82%", PassRateDisplay: "90%", Reviews: 12, LastStudiedDisplay: "2026-09-01"},
		{Email: "unseen@example.com", Seen: 0, New: 15,
			RecallDisplay: noDataDisplay, PassRateDisplay: noDataDisplay, LastStudiedDisplay: noDataDisplay},
	}
	hotspots := []lapseHotspotRow{
		{Label: "¿Cómo estás?", Students: 4, Reviews: 9, AgainRateDisplay: "56%"},
	}
	var buf bytes.Buffer
	err = pages["progress"].ExecuteTemplate(&buf, "layout", map[string]any{
		"User": db.User{}, "Deck": deck, "AsOf": time.Now(),
		"Students": students, "RosterSize": len(students),
		"Page": 0, "PrevPage": -1, "NextPage": 1, "HasNextPage": false,
		"Hotspots": hotspots, "HotspotMinReviews": hotspotMinReviews, "HotspotMinStudents": hotspotMinStudents,
	})
	if err != nil {
		t.Fatalf("execute progress template: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Seen Student") || !strings.Contains(body, noDataDisplay) {
		t.Errorf("rendered body missing expected content:\n%s", body)
	}
}

// Every 404 case collapses "deck absent", "deck invisible", and "caller lacks can_view_progress"
// into one outcome (CLAUDE.md §10.5, docs/schema.md) -- and specifically the cases #87's plan
// calls out (§0.1): can_manage_access alone doesn't grant progress read, neither does can_study
// alone.
func TestProgressRoute_AccessControl(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	instructorEmail := testEmail()
	instructorCookie := loginCookie(t, tx, a, instructorEmail, "correct-horse-battery")
	instructorID := userID(t, ctx, tx, instructorEmail)
	managerEmail := testEmail()
	managerCookie := loginCookie(t, tx, a, managerEmail, "correct-horse-battery")
	managerID := userID(t, ctx, tx, managerEmail)
	studentEmail := testEmail()
	studentCookie := loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)

	w := doRequest(handler, "POST", "/decks", "name=Roster Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	progressPath := deckPath + "/progress"

	// can_view_progress, without can_study: the instructor need not be on the roster themselves.
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_view_progress) VALUES ($1, $2, true, true)`,
		deckID, instructorID); err != nil {
		t.Fatalf("grant instructor access: %v", err)
	}
	// can_manage_access alone must not grant progress read (§0.1 of the plan).
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_manage_access) VALUES ($1, $2, true, true)`,
		deckID, managerID); err != nil {
		t.Fatalf("grant manager access: %v", err)
	}
	// can_study alone (the roster itself) must not grant progress read.
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	const nilUUID = "00000000-0000-0000-0000-000000000000"
	tests := []struct {
		name       string
		cookie     *http.Cookie
		path       string
		wantStatus int
	}{
		{"no session", nil, progressPath, http.StatusSeeOther},
		{"owner (full access)", ownerCookie, progressPath, http.StatusOK},
		{"instructor with can_view_progress", instructorCookie, progressPath, http.StatusOK},
		{"manager without can_view_progress", managerCookie, progressPath, http.StatusNotFound},
		{"student (can_study only)", studentCookie, progressPath, http.StatusNotFound},
		{"nonexistent deck", ownerCookie, "/decks/" + nilUUID + "/progress", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "GET", tt.path, "", tt.cookie, "")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

// The roster only ever lists can_study holders; the rendered page must name the collaborator and
// use the singular "student" for a roster of one (§2.2 of the plan).
func TestProgressRoute_RosterAndPassRate(t *testing.T) {
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
	addNotes(t, handler, ownerCookie, deckPath, noteTypeID, 2)
	cardIDs := lookupCardIDs(t, ctx, tx, deckID)

	studentEmail := testEmail()
	loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant student access: %v", err)
	}

	// Pass rate is independent of the day boundary -- it is a flat window off "now" -- so seeding
	// well within the last 30 days is enough: one Again (fail) and one Good (pass) gives 50%.
	reviewedAt := time.Now().Add(-time.Hour)
	seedReviewLogRow(t, ctx, tx, studentID, cardIDs[0], 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, studentID, cardIDs[1], 3, reviewedAt)

	w := doRequest(handler, "GET", deckPath+"/progress", "", ownerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET progress status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, studentEmail) {
		t.Errorf("roster missing the can_study collaborator:\n%s", body)
	}
	if strings.Contains(body, "1 students") {
		t.Error("roster size should read singular for one student")
	}
	if !strings.Contains(body, "50%") {
		t.Errorf("50%% pass rate (1 Again, 1 Good) not found in body:\n%s", body)
	}
}

// The day boundary is per-student (users.timezone/day_start_hour), computed inside the query
// (§1.3 of the plan) -- the easiest thing to get silently wrong, so this asserts the query
// directly rather than through rendered HTML. Two students hold identical user_card_state rows
// on the same card at the same instant; only their timezones differ, and that alone must flip
// whether the card counts as "due" vs. "already studied today".
func TestListStudentProgressForDeck_DayBoundaryIsPerStudent(t *testing.T) {
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
	cardID := lookupCardIDs(t, ctx, tx, deckID)[0]

	// fixedNow 10:00 UTC. Student A (UTC, day_start_hour 4): today's rollover is 04:00 UTC.
	// Student B (Pacific/Kiritimati, UTC+14, day_start_hour 4): local time is already the next
	// calendar day, so today's rollover is 04:00 local = 2026-06-14T14:00Z -- 14 hours earlier.
	// last_review sits between the two rollovers: after B's (so B already "studied today") but
	// before A's (so A has not).
	fixedNow := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	lastReview := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)

	studentAEmail := testEmail()
	loginCookie(t, tx, a, studentAEmail, "correct-horse-battery")
	studentAID := userID(t, ctx, tx, studentAEmail)
	setUserTimezone(t, ctx, tx, studentAID, "UTC", 4)

	studentBEmail := testEmail()
	loginCookie(t, tx, a, studentBEmail, "correct-horse-battery")
	studentBID := userID(t, ctx, tx, studentBEmail)
	setUserTimezone(t, ctx, tx, studentBID, "Pacific/Kiritimati", 4)

	for _, uid := range []string{studentAID, studentBID} {
		if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
			deckID, uid); err != nil {
			t.Fatalf("grant access: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_card_state
			(user_id, card_id, due, stability, difficulty, state, reps, last_review)
			VALUES ($1, $2, $3, 10, 5, 2, 1, $3)`, uid, cardID, lastReview); err != nil {
			t.Fatalf("seed user_card_state: %v", err)
		}
	}

	rows, err := db.New(tx).ListStudentProgressForDeck(ctx, db.ListStudentProgressForDeckParams{
		DeckID: pgUUID(t, deckID), Now: pgtype.Timestamptz{Time: fixedNow, Valid: true},
		LookAheadMinutes: 0, WindowDays: progressWindowDays,
		LimitCount: progressPageSize, OffsetCount: 0,
	})
	if err != nil {
		t.Fatalf("ListStudentProgressForDeck: %v", err)
	}
	due := map[string]int64{}
	for _, r := range rows {
		due[r.UserID.String()] = r.DueCount
	}
	if due[pgUUID(t, studentAID).String()] != 1 {
		t.Errorf("student A (UTC) due count = %d, want 1 (card not yet studied in A's study day)", due[pgUUID(t, studentAID).String()])
	}
	if due[pgUUID(t, studentBID).String()] != 0 {
		t.Errorf("student B (Kiritimati) due count = %d, want 0 (already studied in B's study day)", due[pgUUID(t, studentBID).String()])
	}
}

// Learning must apply the same suspended/buried exclusions as Due (and CountQueueForDeck,
// reviews.sql) -- otherwise a card suspended or buried on the student's own /decks/{id} queue
// would still inflate this deck's Learning column.
func TestListStudentProgressForDeck_LearningExcludesSuspendedAndBuried(t *testing.T) {
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
	addNotes(t, handler, ownerCookie, deckPath, noteTypeID, 2)
	cardIDs := lookupCardIDs(t, ctx, tx, deckID)
	suspendedCard, buriedCard := cardIDs[0], cardIDs[1]

	studentEmail := testEmail()
	loginCookie(t, tx, a, studentEmail, "correct-horse-battery")
	studentID := userID(t, ctx, tx, studentEmail)
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckID, studentID); err != nil {
		t.Fatalf("grant access: %v", err)
	}

	fixedNow := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state
		(user_id, card_id, due, stability, difficulty, state, reps, suspended) VALUES ($1, $2, $3, 10, 5, 1, 1, true)`,
		studentID, suspendedCard, fixedNow); err != nil {
		t.Fatalf("seed suspended learning card: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_card_state
		(user_id, card_id, due, stability, difficulty, state, reps, buried_until) VALUES ($1, $2, $3, 10, 5, 1, 1, $4)`,
		studentID, buriedCard, fixedNow, fixedNow.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("seed buried learning card: %v", err)
	}

	rows, err := db.New(tx).ListStudentProgressForDeck(ctx, db.ListStudentProgressForDeckParams{
		DeckID: pgUUID(t, deckID), Now: pgtype.Timestamptz{Time: fixedNow, Valid: true},
		LookAheadMinutes: 0, WindowDays: progressWindowDays,
		LimitCount: progressPageSize, OffsetCount: 0,
	})
	if err != nil {
		t.Fatalf("ListStudentProgressForDeck: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.UserID != pgUUID(t, studentID) {
			continue
		}
		found = true
		if r.LearningCount != 0 {
			t.Errorf("LearningCount = %d, want 0 (both learning-state cards are suspended/buried)", r.LearningCount)
		}
	}
	if !found {
		t.Fatalf("student row not found in roster: %+v", rows)
	}
}

// A card with 4 reviews, or 5 reviews from only 2 students, must not appear -- the ≥5-review/
// ≥3-student floor exists so one bad night can't top the chart (§1.4, §4 of the plan).
func TestListLapseHotspotsForDeck_Threshold(t *testing.T) {
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
	addNotes(t, handler, ownerCookie, deckPath, noteTypeID, 2)
	cardIDs := lookupCardIDs(t, ctx, tx, deckID)
	belowReviewFloor, belowStudentFloor := cardIDs[0], cardIDs[1]

	students := make([]string, 3)
	for i := range students {
		email := testEmail()
		loginCookie(t, tx, a, email, "correct-horse-battery")
		uid := userID(t, ctx, tx, email)
		if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
			deckID, uid); err != nil {
			t.Fatalf("grant access: %v", err)
		}
		students[i] = uid
	}

	reviewedAt := time.Now().Add(-time.Hour)
	// 4 reviews across all 3 students -- below the review floor.
	seedReviewLogRow(t, ctx, tx, students[0], belowReviewFloor, 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, students[1], belowReviewFloor, 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, students[2], belowReviewFloor, 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, students[0], belowReviewFloor, 3, reviewedAt)
	// 5 reviews from only 2 students -- below the student floor.
	seedReviewLogRow(t, ctx, tx, students[0], belowStudentFloor, 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, students[0], belowStudentFloor, 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, students[0], belowStudentFloor, 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, students[1], belowStudentFloor, 1, reviewedAt)
	seedReviewLogRow(t, ctx, tx, students[1], belowStudentFloor, 1, reviewedAt)

	hotspots, err := db.New(tx).ListLapseHotspotsForDeck(ctx, db.ListLapseHotspotsForDeckParams{
		DeckID: pgUUID(t, deckID), Now: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		WindowDays: progressWindowDays, MinReviews: hotspotMinReviews, MinStudents: hotspotMinStudents,
		LimitCount: hotspotLimit,
	})
	if err != nil {
		t.Fatalf("ListLapseHotspotsForDeck: %v", err)
	}
	if len(hotspots) != 0 {
		t.Errorf("hotspots = %+v, want none (both cards are below one of the two floors)", hotspots)
	}
}

func lookupCardIDs(t *testing.T, ctx context.Context, tx pgx.Tx, deckID string) []string {
	t.Helper()
	rows, err := tx.Query(ctx, `SELECT id FROM cards WHERE deck_id = $1 ORDER BY ordinal`, deckID)
	if err != nil {
		t.Fatalf("lookup cards: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan card id: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func setUserTimezone(t *testing.T, ctx context.Context, tx pgx.Tx, userID, timezone string, dayStartHour int) {
	t.Helper()
	if _, err := tx.Exec(ctx, `UPDATE users SET timezone = $2, day_start_hour = $3 WHERE id = $1`, userID, timezone, dayStartHour); err != nil {
		t.Fatalf("set user timezone: %v", err)
	}
}

func seedReviewLogRow(t *testing.T, ctx context.Context, tx pgx.Tx, userID, cardID string, rating int, reviewedAt time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO review_log
		(user_id, card_id, rating, reviewed_at, state_before, learning_steps_before, elapsed_days_before, scheduled_days_after, review_kind)
		VALUES ($1, $2, $3, $4, 2, 0, 1, 1, 0)`, userID, cardID, rating, reviewedAt); err != nil {
		t.Fatalf("seed review_log: %v", err)
	}
}

func pgUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}
