package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	"github.com/Jolls/enshu/internal/review"
)

// -- fixtures and helpers --------------------------------------------------

var evSeq atomic.Int64

// newTestEventID returns a fresh, valid UUID string -- the client-generated review_log.id.
func newTestEventID() string {
	return fmt.Sprintf("018f5b3e-0000-7000-8000-%012d", evSeq.Add(1))
}

// setupOneCard creates a deck, a "Basic2" note type, and one note (one card) via the ordinary
// handler routes, and returns the deck and card ids as path-ready strings.
func setupOneCard(t *testing.T, tx pgx.Tx, handler http.Handler, cookie *http.Cookie) (deckID, cardID string) {
	t.Helper()
	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID = strings.TrimPrefix(deckPath, "/decks/")

	var noteTypeID string
	if err := tx.QueryRow(context.Background(), `SELECT id FROM note_types WHERE name = 'Basic2' ORDER BY id DESC LIMIT 1`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	w := doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d: %s", w.Code, w.Body.String())
	}
	if err := tx.QueryRow(context.Background(),
		`SELECT id FROM cards WHERE deck_id = $1 ORDER BY id DESC LIMIT 1`, deckID,
	).Scan(&cardID); err != nil {
		t.Fatalf("lookup card: %v", err)
	}
	return deckID, cardID
}

func addNotes(t *testing.T, handler http.Handler, cookie *http.Cookie, deckPath, noteTypeID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		body := url.Values{}
		body.Set("note_type_id", noteTypeID)
		body.Add("field[]", fmt.Sprintf("Q%d", i))
		body.Add("field[]", fmt.Sprintf("A%d", i))
		w := doRequest(handler, "POST", deckPath+"/notes", body.Encode(), cookie, "http://example.com")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("create note %d status = %d: %s", i, w.Code, w.Body.String())
		}
	}
}

func doJSON(handler http.Handler, method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Host = "example.com"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func gradeCard(t *testing.T, handler http.Handler, cookie *http.Cookie, cardID string, rating int, reviewedAt time.Time) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":%d,"reviewedAt":%q}]}`,
		newTestEventID(), cardID, rating, reviewedAt.Format(time.RFC3339Nano))
	w := doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("grade status = %d: %s", w.Code, w.Body.String())
	}
}

var cardAttrRe = regexp.MustCompile(`(data-[a-z-]+)="([^"]*)"`)

func extractCardAttrs(t *testing.T, html, cardID string) map[string]string {
	t.Helper()
	tagRe := regexp.MustCompile(`<article[^>]*data-card-id="` + regexp.QuoteMeta(cardID) + `"[^>]*>`)
	tag := tagRe.FindString(html)
	if tag == "" {
		t.Fatalf("no <article> found for card %s in:\n%s", cardID, html)
	}
	attrs := map[string]string{}
	for _, m := range cardAttrRe.FindAllStringSubmatch(tag, -1) {
		attrs[m[1]] = m[2]
	}
	return attrs
}

func extractCardIDs(html string) []string {
	re := regexp.MustCompile(`data-card-id="([^"]+)"`)
	var ids []string
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func extractCursor(t *testing.T, html string) string {
	t.Helper()
	re := regexp.MustCompile(`data-cursor="([^"]*)"`)
	m := re.FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("no data-cursor found in:\n%s", html)
	}
	return m[1]
}

// -- §9.1: the single highest-priority test in the repo (CLAUDE.md §10.1) -------------------

func TestReviewBatch_ClientCannotWriteSchedulingState(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	_, cardID := setupOneCard(t, tx, handler, cookie)

	evID := newTestEventID()
	body := fmt.Sprintf(`{"events":[{
		"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q,"durationMs":1500,
		"stability":9999,"difficulty":1,"due":"2099-01-01T00:00:00Z","state":2,
		"reps":500,"lapses":7,"scheduledDays":9999,"elapsedDays":5,"learningSteps":3,
		"fsrsVersion":1,"ankiId":42,"after":{"stability":9999}
	}]}`, evID, cardID, clock.Format(time.RFC3339Nano))

	w := doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (extras must be ignored, not rejected): %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"applied"`) {
		t.Fatalf("body = %s, want one applied result", w.Body.String())
	}

	// Independently compute the expected outcome the same way the server must have.
	params, err := fsrs.NewDefaultParams(review.DefaultDesiredRetention)
	if err != nil {
		t.Fatalf("NewDefaultParams: %v", err)
	}
	want, err := fsrs.Schedule(params, fsrs.CardState{}, fsrs.Good, clock)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}

	row := getUserCardStateByCardID(t, tx, cardID)
	if row.Stability != want.Stability || row.Difficulty != want.Difficulty ||
		row.State != int16(want.State) || row.Reps != want.Reps || row.Lapses != want.Lapses ||
		row.ScheduledDays != want.ScheduledDays || row.LearningSteps != want.LearningSteps {
		t.Errorf("user_card_state = %+v, want derived from independent fsrs.Schedule = %+v", row, want)
	}
	if !row.Due.Time.Equal(want.Due) {
		t.Errorf("user_card_state.due = %v, want %v", row.Due.Time, want.Due)
	}

	if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE stability = 9999 OR reps = 500`); n != 0 {
		t.Error("injected stability/reps must not appear anywhere in user_card_state")
	}
	if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE stability_before = 9999 OR duration_ms = 500`); n != 0 {
		t.Error("injected values must not appear anywhere in review_log")
	}

	var stateBefore, learningStepsBefore, reviewKind int16
	var stabilityBefore, difficultyBefore float64
	var elapsedDaysBefore, scheduledDaysAfter int32
	var fsrsVersion *int16
	var ankiID *int64
	var durationMs *int32
	if err := tx.QueryRow(ctx, `SELECT state_before, learning_steps_before, stability_before, difficulty_before,
		elapsed_days_before, scheduled_days_after, fsrs_version, review_kind, anki_id, duration_ms
		FROM review_log WHERE id = $1`, evID,
	).Scan(&stateBefore, &learningStepsBefore, &stabilityBefore, &difficultyBefore,
		&elapsedDaysBefore, &scheduledDaysAfter, &fsrsVersion, &reviewKind, &ankiID, &durationMs); err != nil {
		t.Fatalf("lookup review_log row: %v", err)
	}
	if stateBefore != 0 || stabilityBefore != 0 || difficultyBefore != 0 || elapsedDaysBefore != 0 || learningStepsBefore != 0 {
		t.Errorf("review_log *_before should all be zero for a never-seen card: state=%d stability=%v difficulty=%v elapsed=%d steps=%d",
			stateBefore, stabilityBefore, difficultyBefore, elapsedDaysBefore, learningStepsBefore)
	}
	if scheduledDaysAfter != want.ScheduledDays {
		t.Errorf("scheduled_days_after = %d, want %d", scheduledDaysAfter, want.ScheduledDays)
	}
	if fsrsVersion == nil || *fsrsVersion != 6 {
		t.Errorf("fsrs_version = %v, want 6", fsrsVersion)
	}
	if reviewKind != 0 {
		t.Errorf("review_kind = %d, want 0 (New)", reviewKind)
	}
	if ankiID != nil {
		t.Error("anki_id should be NULL for a reviewer-produced row")
	}
	if durationMs == nil || *durationMs != 1500 {
		t.Errorf("duration_ms = %v, want 1500 (the one legitimate extra field)", durationMs)
	}
}

// -- §10.1 continued (#178): a batch sent under a session that has switched accounts ------------

// The session cookie is browser-wide, so a tab left open as A keeps grading after the browser
// switches to B. With B holding can_study on the shared deck, GradeBatch would authorise and write
// those grades into B's user_card_state and review_log -- plausible-looking, unrecoverable (§2.5).
// The ?u= staleness check must refuse the batch outright; nothing is written for either account.
func TestReviewBatch_RejectsSwitchedAwaySession(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })

	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	sharedEmail := testEmail()
	sharedCookie := loginCookie(t, tx, a, sharedEmail, "correct-horse-battery")

	var ownerID, sharedID, deckUUID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, ownerEmail).Scan(&ownerID); err != nil {
		t.Fatalf("lookup owner: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, sharedEmail).Scan(&sharedID); err != nil {
		t.Fatalf("lookup shared user: %v", err)
	}
	if err := deckUUID.Scan(deckID); err != nil {
		t.Fatalf("scan deck id: %v", err)
	}
	// can_study, deliberately: without it the batch would be answered `forbidden` by the existing
	// authorisation and this test would pass for the wrong reason.
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckUUID, sharedID); err != nil {
		t.Fatalf("grant can_study: %v", err)
	}

	body := func() string {
		return fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`,
			newTestEventID(), cardID, clock.Format(time.RFC3339Nano))
	}

	t.Run("mismatched u is refused and writes nothing", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch?u="+ownerID.String(), body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE card_id = $1`, cardID); n != 0 {
			t.Errorf("review_log rows for this card = %d, want 0 (for either account)", n)
		}
		if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, cardID); n != 0 {
			t.Errorf("user_card_state rows for this card = %d, want 0", n)
		}
	})

	t.Run("unparseable u is refused", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch?u=not-a-uuid", body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE card_id = $1`, cardID); n != 0 {
			t.Errorf("review_log rows for this card = %d, want 0", n)
		}
	})

	// The two accepting cases: matching u, and no u at all (the server side lands before the client
	// side, so a missing u must behave exactly as it does today).
	t.Run("matching u is applied", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch?u="+sharedID.String(), body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"applied"`) {
			t.Fatalf("status = %d, body = %s, want 200 applied", w.Code, w.Body.String())
		}
	})

	t.Run("absent u is applied", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch", body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"applied"`) {
			t.Fatalf("status = %d, body = %s, want 200 applied", w.Code, w.Body.String())
		}
	})
}

// -- #142: the grade response's preview is computed from the state the grade stored ------------

func TestReviewBatch_PreviewIsRecomputedFromStoredState(t *testing.T) {
	tx := beginTx(t)
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	_, cardID := setupOneCard(t, tx, handler, cookie)

	// Easy graduates a New card straight to Review with a multi-day interval, so the post-grade
	// preview differs from the pre-grade one in nine of the twelve wire fields (Again becomes
	// Relearning/10 min, Hard and Good become Review with scheduledDays >= 15). Grading Again
	// instead would leave the card in Learning with the same remaining steps, and go-fsrs's
	// Again/Hard/Good branches would then be wire-identical before and after (scheduler_basic.go's
	// newState vs learningState reach the same applyStep delays) -- a test that could not fail.
	evID := newTestEventID()
	body := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":4,"reviewedAt":%q}]}`,
		evID, cardID, clock.Format(time.RFC3339Nano))
	w := doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var decoded struct {
		Results []struct {
			Status  string `json:"status"`
			Preview *struct {
				Again, Hard, Good, Easy struct {
					Due           string `json:"due"`
					State         int    `json:"state"`
					ScheduledDays int32  `json:"scheduledDays"`
				}
			} `json:"preview"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v: %s", err, w.Body.String())
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(decoded.Results))
	}
	got := decoded.Results[0]
	if got.Status != "applied" {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	if got.Preview == nil {
		t.Fatal("preview = nil, want non-nil for an applied result")
	}

	// Independently recompute from the state actually persisted -- building `stored` from the
	// database row rather than from the response's `after` pins the preview to what was actually
	// stored, and catches a preview taken from an in-memory Outcome the guarded upsert rejected.
	params, err := fsrs.NewDefaultParams(review.DefaultDesiredRetention)
	if err != nil {
		t.Fatalf("NewDefaultParams: %v", err)
	}
	row := getUserCardStateByCardID(t, tx, cardID)
	stored := fsrs.CardState{
		Due: row.Due.Time, Stability: row.Stability, Difficulty: row.Difficulty,
		State: fsrs.State(row.State), Reps: row.Reps, Lapses: row.Lapses,
		ScheduledDays: row.ScheduledDays, LearningSteps: row.LearningSteps,
		LastReview: row.LastReview.Time,
	}
	want, err := fsrs.PreviewAll(params, stored, clock)
	if err != nil {
		t.Fatalf("PreviewAll: %v", err)
	}

	type branchFields struct {
		Due           string
		State         int
		ScheduledDays int32
	}
	gotBranches := map[string]branchFields{
		"again": {got.Preview.Again.Due, got.Preview.Again.State, got.Preview.Again.ScheduledDays},
		"hard":  {got.Preview.Hard.Due, got.Preview.Hard.State, got.Preview.Hard.ScheduledDays},
		"good":  {got.Preview.Good.Due, got.Preview.Good.State, got.Preview.Good.ScheduledDays},
		"easy":  {got.Preview.Easy.Due, got.Preview.Easy.State, got.Preview.Easy.ScheduledDays},
	}
	wantBranches := map[string]branchFields{
		"again": {want.Again.Due.UTC().Format(time.RFC3339Nano), int(want.Again.State), want.Again.ScheduledDays},
		"hard":  {want.Hard.Due.UTC().Format(time.RFC3339Nano), int(want.Hard.State), want.Hard.ScheduledDays},
		"good":  {want.Good.Due.UTC().Format(time.RFC3339Nano), int(want.Good.State), want.Good.ScheduledDays},
		"easy":  {want.Easy.Due.UTC().Format(time.RFC3339Nano), int(want.Easy.State), want.Easy.ScheduledDays},
	}
	for branch, g := range gotBranches {
		if w := wantBranches[branch]; g != w {
			t.Errorf("branch %s = %+v, want %+v", branch, g, w)
		}
	}

	// Meaningfulness guard: if the pre-grade and post-grade previews agree on all twelve wire
	// fields, the fixture no longer separates them and this test could pass vacuously.
	stale, err := fsrs.PreviewAll(params, fsrs.CardState{}, clock)
	if err != nil {
		t.Fatalf("PreviewAll (stale): %v", err)
	}
	staleBranches := map[string]branchFields{
		"again": {stale.Again.Due.UTC().Format(time.RFC3339Nano), int(stale.Again.State), stale.Again.ScheduledDays},
		"hard":  {stale.Hard.Due.UTC().Format(time.RFC3339Nano), int(stale.Hard.State), stale.Hard.ScheduledDays},
		"good":  {stale.Good.Due.UTC().Format(time.RFC3339Nano), int(stale.Good.State), stale.Good.ScheduledDays},
		"easy":  {stale.Easy.Due.UTC().Format(time.RFC3339Nano), int(stale.Easy.State), stale.Easy.ScheduledDays},
	}
	if reflect.DeepEqual(staleBranches, wantBranches) {
		t.Fatal("stale (pre-grade) and fresh (post-grade) previews agree on all fields -- fixture no longer separates them")
	}
}

func getUserCardStateByCardID(t *testing.T, tx pgx.Tx, cardID string) db.UserCardState {
	t.Helper()
	var row db.UserCardState
	err := tx.QueryRow(context.Background(), `SELECT user_id, card_id, due, stability, difficulty, state, reps, lapses,
		elapsed_days, scheduled_days, learning_steps, last_review, suspended, buried_until, flag
		FROM user_card_state WHERE card_id = $1`, cardID,
	).Scan(&row.UserID, &row.CardID, &row.Due, &row.Stability, &row.Difficulty, &row.State, &row.Reps, &row.Lapses,
		&row.ElapsedDays, &row.ScheduledDays, &row.LearningSteps, &row.LastReview, &row.Suspended, &row.BuriedUntil, &row.Flag)
	if err != nil {
		t.Fatalf("lookup user_card_state: %v", err)
	}
	return row
}

// -- §9.2: idempotency (CLAUDE.md §10.4) -----------------------------------------------------

func TestReviewBatch_Idempotency(t *testing.T) {
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	t.Run("same batch sent twice", func(t *testing.T) {
		tx := beginTx(t)
		handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
		cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
		_, cardID := setupOneCard(t, tx, handler, cookie)

		evID := newTestEventID()
		body := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, evID, cardID, clock.Format(time.RFC3339Nano))
		doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
		w2 := doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
		if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), `"status":"duplicate"`) {
			t.Errorf("replay status = %d, body = %s, want 200 duplicate", w2.Code, w2.Body.String())
		}
		if !strings.Contains(w2.Body.String(), `"preview":`) {
			t.Errorf("replay body = %s, want a preview object on the duplicate result too (#142)", w2.Body.String())
		}
		if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1`, evID); n != 1 {
			t.Errorf("review_log rows for id = %d, want 1", n)
		}
	})

	t.Run("two-event batch sent shuffled vs sorted", func(t *testing.T) {
		tx := beginTx(t)
		handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
		cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
		deckA, cardA := setupOneCard(t, tx, handler, cookie)

		id1, id2 := newTestEventID(), newTestEventID()
		t1, t2 := clock, clock.Add(1*time.Minute)
		sorted := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q},{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`,
			id1, cardA, t1.Format(time.RFC3339Nano), id2, cardA, t2.Format(time.RFC3339Nano))

		wA := doJSON(handler, "POST", "/api/reviews/batch", sorted, cookie, "http://example.com")
		stateAfterFirst := getUserCardStateByCardID(t, tx, cardA)

		// A second, independently-seeded card graded via the shuffled order must converge to the
		// same final state as the first card graded in sorted order. Fresh event ids: review_log.id
		// is a global PK, so reusing id1/id2 here would hit the idempotency skip instead of grading.
		_, cardB := setupSecondCard(t, tx, handler, cookie, deckA)
		id1b, id2b := newTestEventID(), newTestEventID()
		shuffledForB := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q},{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`,
			id2b, cardB, t2.Format(time.RFC3339Nano), id1b, cardB, t1.Format(time.RFC3339Nano))
		wB := doJSON(handler, "POST", "/api/reviews/batch", shuffledForB, cookie, "http://example.com")
		if wA.Code != http.StatusOK || wB.Code != http.StatusOK {
			t.Fatalf("status A=%d B=%d", wA.Code, wB.Code)
		}
		stateB := getUserCardStateByCardID(t, tx, cardB)
		if stateAfterFirst.Stability != stateB.Stability || stateAfterFirst.Due.Time != stateB.Due.Time ||
			stateAfterFirst.Reps != stateB.Reps {
			t.Errorf("sorted-order state %+v diverged from shuffled-order state %+v", stateAfterFirst, stateB)
		}
	})

	t.Run("e1 then e2 then e1 replayed after e2 already landed", func(t *testing.T) {
		tx := beginTx(t)
		handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
		cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
		_, cardID := setupOneCard(t, tx, handler, cookie)

		id1, id2 := newTestEventID(), newTestEventID()
		t1, t2 := clock, clock.Add(1*time.Minute)
		e1 := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, id1, cardID, t1.Format(time.RFC3339Nano))
		e2 := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, id2, cardID, t2.Format(time.RFC3339Nano))

		doJSON(handler, "POST", "/api/reviews/batch", e1, cookie, "http://example.com")
		doJSON(handler, "POST", "/api/reviews/batch", e2, cookie, "http://example.com")
		stateAfterBoth := getUserCardStateByCardID(t, tx, cardID)

		w3 := doJSON(handler, "POST", "/api/reviews/batch", e1, cookie, "http://example.com")
		if !strings.Contains(w3.Body.String(), `"status":"duplicate"`) {
			t.Errorf("replaying e1 after e2 landed: body = %s, want duplicate", w3.Body.String())
		}
		stateAfterReplay := getUserCardStateByCardID(t, tx, cardID)
		if stateAfterBoth.Due.Time != stateAfterReplay.Due.Time || stateAfterBoth.Reps != stateAfterReplay.Reps {
			t.Errorf("replaying e1 must not change stored state: before=%+v after=%+v", stateAfterBoth, stateAfterReplay)
		}
		if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1`, id1); n != 1 {
			t.Errorf("review_log rows for e1 = %d, want 1", n)
		}
	})
}

// -- §9.3: out-of-order replay --------------------------------------------------------------

func TestReviewBatch_OutOfOrderReplay(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// now tracks the server clock at each request's arrival, independent of reviewedAt -- e2's
	// reviewedAt is 10 minutes after base, so "now" must have advanced there too by the time e2's
	// request arrives, or the clamp/reject policy would treat it as an implausible future
	// timestamp (resolved decision 1) rather than the out-of-order-arrival scenario under test.
	now := base
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return now })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, cookie)

	id1, id2 := newTestEventID(), newTestEventID()
	t1, t2 := base, base.Add(10*time.Minute)

	// Grade e2 first (out of order relative to e1).
	now = t2
	e2 := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, id2, cardID, t2.Format(time.RFC3339Nano))
	doJSON(handler, "POST", "/api/reviews/batch", e2, cookie, "http://example.com")
	var e2StateBeforeReplay int16
	if err := tx.QueryRow(ctx, `SELECT state_before FROM review_log WHERE id = $1`, id2).Scan(&e2StateBeforeReplay); err != nil {
		t.Fatalf("lookup e2 state_before: %v", err)
	}

	// Now grade e1, which predates e2 -- triggers the replay path.
	e1 := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, id1, cardID, t1.Format(time.RFC3339Nano))
	w := doJSON(handler, "POST", "/api/reviews/batch", e1, cookie, "http://example.com")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"applied"`) {
		t.Fatalf("out-of-order grade status = %d, body = %s", w.Code, w.Body.String())
	}

	var e2StateBeforeAfter int16
	if err := tx.QueryRow(ctx, `SELECT state_before FROM review_log WHERE id = $1`, id2).Scan(&e2StateBeforeAfter); err != nil {
		t.Fatalf("lookup e2 state_before after replay: %v", err)
	}
	if e2StateBeforeAfter != e2StateBeforeReplay {
		t.Errorf("e2's state_before changed by the later replay: was %d, now %d (review_log is append-only)", e2StateBeforeReplay, e2StateBeforeAfter)
	}

	var e1StateBefore int16
	if err := tx.QueryRow(ctx, `SELECT state_before FROM review_log WHERE id = $1`, id1).Scan(&e1StateBefore); err != nil {
		t.Fatalf("lookup e1 state_before: %v", err)
	}
	if e1StateBefore != int16(fsrs.New) {
		t.Errorf("e1's replayed state_before = %d, want %d (New)", e1StateBefore, fsrs.New)
	}

	replayedFinal := getUserCardStateByCardID(t, tx, cardID)

	// Control: a second card graded e1 then e2 in the natural order must converge to the same state.
	_, controlCard := setupSecondCard(t, tx, handler, cookie, deckID)
	e1c := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, newTestEventID(), controlCard, t1.Format(time.RFC3339Nano))
	e2c := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, newTestEventID(), controlCard, t2.Format(time.RFC3339Nano))
	doJSON(handler, "POST", "/api/reviews/batch", e1c, cookie, "http://example.com")
	doJSON(handler, "POST", "/api/reviews/batch", e2c, cookie, "http://example.com")
	controlFinal := getUserCardStateByCardID(t, tx, controlCard)

	if replayedFinal.Due.Time != controlFinal.Due.Time || replayedFinal.Stability != controlFinal.Stability ||
		replayedFinal.Reps != controlFinal.Reps || replayedFinal.State != controlFinal.State {
		t.Errorf("replayed final state %+v != in-order control state %+v", replayedFinal, controlFinal)
	}
}

// setupSecondCard adds a second note (and its card) to a deck the caller already created,
// reusing the deck's only note type -- for tests that need two independent cards for the same
// user.
func setupSecondCard(t *testing.T, tx pgx.Tx, handler http.Handler, cookie *http.Cookie, deckID string) (deckPath, cardID string) {
	t.Helper()
	ctx := context.Background()
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	deckPath = "/decks/" + deckID
	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", "Q2")
	body.Add("field[]", "A2")
	w := doRequest(handler, "POST", deckPath+"/notes", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create second note status = %d: %s", w.Code, w.Body.String())
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM cards ORDER BY id DESC LIMIT 1`).Scan(&cardID); err != nil {
		t.Fatalf("lookup second card: %v", err)
	}
	return deckPath, cardID
}

// -- §9.4: reviewedAt clamp/reject (resolved decision 1) ------------------------------------

func TestReviewBatch_ReviewedAtClampAndReject(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	_, cardID := setupOneCard(t, tx, handler, cookie)

	tests := []struct {
		name        string
		offset      time.Duration
		wantStatus  string
		wantWritten bool
	}{
		{"30s future: clamp", 30 * time.Second, "applied", true},
		{"4m future: clamp", 4 * time.Minute, "applied", true},
		{"1h future: reject", 1 * time.Hour, "rejected", false},
		{"40d past: reject", -40 * 24 * time.Hour, "rejected", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reviewedAt := clock.Add(tt.offset)
			evID := newTestEventID()
			body := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, evID, cardID, reviewedAt.Format(time.RFC3339Nano))
			w := doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"status":"`+tt.wantStatus+`"`) {
				t.Errorf("body = %s, want status %s", w.Body.String(), tt.wantStatus)
			}
			n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1`, evID)
			if tt.wantWritten && n != 1 {
				t.Errorf("row count = %d, want 1 (written)", n)
			}
			if !tt.wantWritten && n != 0 {
				t.Errorf("row count = %d, want 0 (nothing written)", n)
			}
		})
	}

	t.Run("clamped reviewedAt is stored as now, not the sent future value", func(t *testing.T) {
		evID := newTestEventID()
		future := clock.Add(4 * time.Minute)
		body := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, evID, cardID, future.Format(time.RFC3339Nano))
		doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
		var storedAt time.Time
		if err := tx.QueryRow(ctx, `SELECT reviewed_at FROM review_log WHERE id = $1`, evID).Scan(&storedAt); err != nil {
			t.Fatalf("lookup stored reviewed_at: %v", err)
		}
		if !storedAt.Equal(clock) {
			t.Errorf("stored reviewed_at = %v, want clamped to %v", storedAt, clock)
		}
	})
}

// -- §9.5: malformed batches are 400, nothing partially applied -----------------------------

func TestReviewBatch_MalformedIs400(t *testing.T) {
	tx := beginTx(t)
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	_, cardID := setupOneCard(t, tx, handler, cookie)
	reviewedAt := clock.Format(time.RFC3339Nano)

	tests := []struct {
		name string
		body string
	}{
		{"rating 0", fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":0,"reviewedAt":%q}]}`, newTestEventID(), cardID, reviewedAt)},
		{"rating 5", fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":5,"reviewedAt":%q}]}`, newTestEventID(), cardID, reviewedAt)},
		{"missing cardId", fmt.Sprintf(`{"events":[{"id":%q,"rating":3,"reviewedAt":%q}]}`, newTestEventID(), reviewedAt)},
		{"reviewedAt not RFC3339", fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":"yesterday"}]}`, newTestEventID(), cardID)},
		{"durationMs -1", fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q,"durationMs":-1}]}`, newTestEventID(), cardID, reviewedAt)},
		{"durationMs too large", fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q,"durationMs":99999999}]}`, newTestEventID(), cardID, reviewedAt)},
		{"empty events", `{"events":[]}`},
		{"101 events", tooManyEventsBody(cardID, reviewedAt)},
		{"100 KiB body", `{"events":[{"padding":"` + strings.Repeat("x", 100*1024) + `"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doJSON(handler, "POST", "/api/reviews/batch", tt.body, cookie, "http://example.com")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}

	t.Run("second event malformed: nothing applied, not even the valid first", func(t *testing.T) {
		good := newTestEventID()
		body := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q},{"id":"not-a-uuid","cardId":%q,"rating":3,"reviewedAt":%q}]}`,
			good, cardID, reviewedAt, cardID, reviewedAt)
		w := doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1`, good); n != 0 {
			t.Error("the valid first event must not be written when the second is malformed")
		}
	})

	if n := countRows(t, tx, `SELECT count(*) FROM review_log`); n != 0 {
		t.Errorf("review_log count = %d, want 0 (nothing in this test should have been written)", n)
	}
}

func tooManyEventsBody(cardID, reviewedAt string) string {
	var b strings.Builder
	b.WriteString(`{"events":[`)
	for i := 0; i < 101; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}`, newTestEventID(), cardID, reviewedAt)
	}
	b.WriteString(`]}`)
	return b.String()
}

// -- §9.6: access control (CLAUDE.md §10.5) --------------------------------------------------

func TestReviewRoutes_AccessControl(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	viewOnlyEmail := testEmail()
	viewOnlyCookie := loginCookie(t, tx, a, viewOnlyEmail, "correct-horse-battery")
	var viewOnlyID, deckUUID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, viewOnlyEmail).Scan(&viewOnlyID); err != nil {
		t.Fatalf("lookup view-only user: %v", err)
	}
	if err := deckUUID.Scan(deckID); err != nil {
		t.Fatalf("scan deck id: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, deckUUID, viewOnlyID); err != nil {
		t.Fatalf("grant view-only access: %v", err)
	}

	batchBody := func() string {
		return fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`, newTestEventID(), cardID, clock.Format(time.RFC3339Nano))
	}

	getTests := []struct {
		name       string
		path       string
		cookie     *http.Cookie
		wantStatus int
	}{
		{"no session GET page", "/decks/" + deckID + "/review", nil, http.StatusSeeOther},
		{"stranger GET page", "/decks/" + deckID + "/review", strangerCookie, http.StatusNotFound},
		{"view-only (no can_study) GET page", "/decks/" + deckID + "/review", viewOnlyCookie, http.StatusNotFound},
		{"owner GET page", "/decks/" + deckID + "/review", ownerCookie, http.StatusOK},
		{"no session GET next", "/api/reviews/next?deck=" + deckID + "&cursor=", nil, http.StatusUnauthorized},
		{"stranger GET next", "/api/reviews/next?deck=" + deckID + "&cursor=", strangerCookie, http.StatusNotFound},
		{"owner GET next", "/api/reviews/next?deck=" + deckID + "&cursor=", ownerCookie, http.StatusOK},
		// #172: extraRounds must not widen what a user can see -- same 404 as the un-flagged
		// request for a no-access (stranger) or view-only (no can_study) deck (CLAUDE.md §10.5).
		{"stranger GET next w/ extraRounds", "/api/reviews/next?deck=" + deckID + "&cursor=&extraRounds=1", strangerCookie, http.StatusNotFound},
		{"view-only (no can_study) GET next w/ extraRounds", "/api/reviews/next?deck=" + deckID + "&cursor=&extraRounds=1", viewOnlyCookie, http.StatusNotFound},
	}
	for _, tt := range getTests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "GET", tt.path, "", tt.cookie, "")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}

	t.Run("no session POST batch", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch", batchBody(), nil, "http://example.com")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	for _, cookie := range []*http.Cookie{strangerCookie, viewOnlyCookie} {
		w := doJSON(handler, "POST", "/api/reviews/batch", batchBody(), cookie, "http://example.com")
		if w.Code != http.StatusOK {
			t.Fatalf("forbidden-grade status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"status":"forbidden"`) {
			t.Errorf("expected forbidden status in body: %s", w.Body.String())
		}
	}
	if n := countRows(t, tx, `SELECT count(*) FROM review_log`); n != 0 {
		t.Errorf("review_log count = %d, want 0 (forbidden attempts write nothing)", n)
	}

	w := doJSON(handler, "POST", "/api/reviews/batch", batchBody(), ownerCookie, "http://example.com")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"applied"`) {
		t.Errorf("owner grade status = %d, body = %s, want 200 applied", w.Code, w.Body.String())
	}
}

// -- §9.7: hidden-card shape parity between the page and refill fragment --------------------

func TestReviewPage_HiddenCardShape(t *testing.T) {
	tx := beginTx(t)
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, cookie)

	pageResp := doRequest(handler, "GET", "/decks/"+deckID+"/review", "", cookie, "")
	if pageResp.Code != http.StatusOK {
		t.Fatalf("page status = %d: %s", pageResp.Code, pageResp.Body.String())
	}
	if !strings.Contains(pageResp.Body.String(), `class="enshu-card card"`) {
		t.Error("card wrapper missing the enshu-card scope class")
	}
	// #194: #review-stage is what review.js actually copies revealed card HTML into -- if it
	// isn't also scoped to enshu-card, every note-type CSS rule matches nothing once a card is
	// shown, even though the rule and the markup both look correct in isolation.
	if !strings.Contains(pageResp.Body.String(), `id="review-stage" hidden class="review-card enshu-card"`) {
		t.Error("#review-stage missing the enshu-card scope class (#194)")
	}
	// #178: review.js reads the acting account id off its own script tag and sends it as ?u=.
	if !strings.Contains(pageResp.Body.String(), `data-user-id="`) {
		t.Error("review page must render the acting user id onto the review.js script tag (#178)")
	}
	pageAttrs := extractCardAttrs(t, pageResp.Body.String(), cardID)

	wantKeys := []string{
		"data-card-id", "data-unseen",
		"data-again-due", "data-again-state", "data-again-scheduled-days",
		"data-hard-due", "data-hard-state", "data-hard-scheduled-days",
		"data-good-due", "data-good-state", "data-good-scheduled-days",
		"data-easy-due", "data-easy-state", "data-easy-scheduled-days",
	}
	for _, k := range wantKeys {
		if _, ok := pageAttrs[k]; !ok {
			t.Errorf("page card missing %s", k)
		}
	}

	// Nothing has been graded yet, so a cursor="" refill returns the same card -- fetched via the
	// other rendering path (renderFragment on the standalone fragment template set).
	refillResp := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=", "", cookie, "")
	if refillResp.Code != http.StatusOK {
		t.Fatalf("refill status = %d: %s", refillResp.Code, refillResp.Body.String())
	}
	refillAttrs := extractCardAttrs(t, refillResp.Body.String(), cardID)

	if !reflect.DeepEqual(pageAttrs, refillAttrs) {
		t.Errorf("page and refill attribute sets differ:\npage:   %v\nrefill: %v", pageAttrs, refillAttrs)
	}
}

// -- §9.8: keyset paging and exhaustion -------------------------------------------------------

func TestReviewNext_KeysetAndExhaustion(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 25)
	// Raise the deck's daily new-card allowance (#101) above the 25 seeded here -- this test is
	// about keyset pagination and exhaustion, not the cap, and the default of 20 would otherwise
	// truncate page1 at the cap instead of the page size.
	if _, err := tx.Exec(ctx, `UPDATE decks SET preset = '{"new":{"perDay":30}}' WHERE id = $1`, deckID); err != nil {
		t.Fatalf("raise new-card limit: %v", err)
	}

	page1 := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=", "", cookie, "")
	if page1.Code != http.StatusOK {
		t.Fatalf("page1 status = %d: %s", page1.Code, page1.Body.String())
	}
	ids1 := extractCardIDs(page1.Body.String())
	if len(ids1) != 20 {
		t.Fatalf("page1 len = %d, want 20", len(ids1))
	}
	if !strings.Contains(page1.Body.String(), `data-exhausted="false"`) {
		t.Error("page1 should not be exhausted")
	}

	cursor1 := extractCursor(t, page1.Body.String())
	page2 := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor="+url.QueryEscape(cursor1), "", cookie, "")
	if page2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d: %s", page2.Code, page2.Body.String())
	}
	ids2 := extractCardIDs(page2.Body.String())
	if len(ids2) != 5 {
		t.Fatalf("page2 len = %d, want 5", len(ids2))
	}
	if !strings.Contains(page2.Body.String(), `data-exhausted="true"`) {
		t.Error("page2 should be exhausted")
	}

	seen := map[string]bool{}
	for _, id := range ids1 {
		seen[id] = true
	}
	for _, id := range ids2 {
		if seen[id] {
			t.Errorf("card %s appears in both pages", id)
		}
	}
}

// -- §9.9: study-day exclusion ----------------------------------------------------------------

func TestReviewNext_ExcludesCardsReviewedThisStudyDay(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, cookie)

	gradeCard(t, handler, cookie, cardID, 3, clock)

	// Force the card due immediately regardless of what FSRS actually scheduled -- isolates the
	// study-day exclusion predicate (last_review < study_day_start) from FSRS's own interval
	// choice for a first "Good" rating.
	if _, err := tx.Exec(ctx, `UPDATE user_card_state SET due = $1 WHERE card_id = $2`, clock, cardID); err != nil {
		t.Fatalf("force due: %v", err)
	}

	refill := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=", "", cookie, "")
	if refill.Code != http.StatusOK {
		t.Fatalf("refill status = %d: %s", refill.Code, refill.Body.String())
	}
	if strings.Contains(refill.Body.String(), cardID) {
		t.Error("a card graded and forced due now must still be excluded within the same study day")
	}

	// Advance past the next rollover (default day_start_hour=4, timezone UTC): a new study day.
	clock = clock.Add(24 * time.Hour)
	refill2 := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=", "", cookie, "")
	if refill2.Code != http.StatusOK {
		t.Fatalf("refill2 status = %d: %s", refill2.Code, refill2.Body.String())
	}
	if !strings.Contains(refill2.Body.String(), cardID) {
		t.Error("card due and past the study-day rollover should reappear")
	}
}

// TestGetStudyDayWindow_LateEveningCountsAsPreviousDay is the §9.9 sub-case the HTTP-level test
// above can't isolate cleanly: with day_start_hour = 4, a 23:30 local review must count as the
// previous study day, not the one about to start at midnight.
func TestGetStudyDayWindow_LateEveningCountsAsPreviousDay(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	a, err := auth.New(tx, auth.Config{})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	email := testEmail()
	loginCookie(t, tx, a, email, "correct-horse-battery")
	if _, err := tx.Exec(ctx, `UPDATE users SET day_start_hour = 4, timezone = 'UTC' WHERE email = $1`, email); err != nil {
		t.Fatalf("set day_start_hour: %v", err)
	}
	var userID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("lookup user: %v", err)
	}

	q := db.New(tx)
	late := time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC)
	window, err := q.GetStudyDayWindow(ctx, db.GetStudyDayWindowParams{
		Now: pgtype.Timestamptz{Time: late, Valid: true}, UserID: userID,
	})
	if err != nil {
		t.Fatalf("GetStudyDayWindow: %v", err)
	}

	wantStart := time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 3, 2, 4, 0, 0, 0, time.UTC)
	if !window.StudyDayStart.Time.Equal(wantStart) {
		t.Errorf("study_day_start = %v, want %v", window.StudyDayStart.Time, wantStart)
	}
	if !window.StudyDayEnd.Time.Equal(wantEnd) {
		t.Errorf("study_day_end = %v, want %v", window.StudyDayEnd.Time, wantEnd)
	}
}

// -- #169: GET /study, the mixed-deck session ------------------------------------------------

// cardIDsForDeck returns every card id belonging to deckID, for asserting which deck a
// /study response's cards actually came from.
func cardIDsForDeck(t *testing.T, tx pgx.Tx, deckID string) map[string]bool {
	t.Helper()
	rows, err := tx.Query(context.Background(), `SELECT id FROM cards WHERE deck_id = $1`, deckID)
	if err != nil {
		t.Fatalf("query cards for deck %s: %v", deckID, err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan card id: %v", err)
		}
		ids[id] = true
	}
	return ids
}

func TestStudyAll_MixesAcrossDecks(t *testing.T) {
	tx := beginTx(t)
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	deckAID, _ := setupOneCard(t, tx, handler, cookie)
	var noteTypeID string
	if err := tx.QueryRow(context.Background(), `SELECT id FROM note_types WHERE name = 'Basic2' ORDER BY id DESC LIMIT 1`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, "/decks/"+deckAID, noteTypeID, 2) // deck A: 3 cards total
	if w := doRequest(handler, "POST", "/decks/"+deckAID+"/edit", "name=D&description=&rev_per_day=1&priority=new", cookie, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Fatalf("edit deck A status = %d: %s", w.Code, w.Body.String())
	}

	w := doRequest(handler, "POST", "/decks", "name=E", cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create deck B status = %d: %s", w.Code, w.Body.String())
	}
	deckBID := strings.TrimPrefix(w.Header().Get("Location"), "/decks/")
	addNotes(t, handler, cookie, "/decks/"+deckBID, noteTypeID, 3) // deck B: 3 cards total
	if w := doRequest(handler, "POST", "/decks/"+deckBID+"/edit", "name=E&description=&rev_per_day=2&priority=due", cookie, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Fatalf("edit deck B status = %d: %s", w.Code, w.Body.String())
	}

	resp := doRequest(handler, "GET", "/study", "", cookie, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /study status = %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `data-exhausted="true"`) {
		t.Error("mixed session must always be Exhausted (one-shot, no refill)")
	}
	// #178: review.js reads the acting account id off its own script tag and sends it as ?u=.
	if !strings.Contains(body, `data-user-id="`) {
		t.Error("study page must render the acting user id onto the review.js script tag (#178)")
	}
	// #194: see the matching assertion in TestReviewPage_HiddenCardShape.
	if !strings.Contains(body, `id="review-stage" hidden class="review-card enshu-card"`) {
		t.Error("#review-stage missing the enshu-card scope class (#194)")
	}

	gotIDs := extractCardIDs(body)
	if len(gotIDs) != 3 {
		t.Fatalf("got %d cards, want 3 (1 from deck A's rev_per_day=1 + 2 from deck B's rev_per_day=2): %v", len(gotIDs), gotIDs)
	}
	deckACards := cardIDsForDeck(t, tx, deckAID)
	deckBCards := cardIDsForDeck(t, tx, deckBID)
	var fromA, fromB int
	for _, id := range gotIDs {
		switch {
		case deckACards[id]:
			fromA++
		case deckBCards[id]:
			fromB++
		default:
			t.Errorf("card %s belongs to neither deck A nor deck B", id)
		}
	}
	if fromA != 1 || fromB != 2 {
		t.Errorf("got %d from deck A, %d from deck B, want 1 and 2", fromA, fromB)
	}
}

func TestStudyAll_ExcludesViewOnlyAccess(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ownerDeckID, ownerCardID := setupOneCard(t, tx, handler, ownerCookie)

	viewOnlyEmail := testEmail()
	viewOnlyCookie := loginCookie(t, tx, a, viewOnlyEmail, "correct-horse-battery")
	var viewOnlyUserID, ownerDeckUUID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, viewOnlyEmail).Scan(&viewOnlyUserID); err != nil {
		t.Fatalf("lookup view-only user: %v", err)
	}
	if err := ownerDeckUUID.Scan(ownerDeckID); err != nil {
		t.Fatalf("scan owner deck id: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, ownerDeckUUID, viewOnlyUserID); err != nil {
		t.Fatalf("grant view-only access: %v", err)
	}
	viewOnlyDeckID, viewOnlyCardID := setupOneCard(t, tx, handler, viewOnlyCookie)

	resp := doRequest(handler, "GET", "/study", "", viewOnlyCookie, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /study status = %d: %s", resp.Code, resp.Body.String())
	}
	gotIDs := extractCardIDs(resp.Body.String())
	for _, id := range gotIDs {
		if id == ownerCardID {
			t.Error("a deck with can_view but not can_study must not contribute cards to the mix")
		}
	}
	if len(gotIDs) != 1 || gotIDs[0] != viewOnlyCardID {
		t.Errorf("got cards %v, want exactly [%s] (the view-only user's own deck %s)", gotIDs, viewOnlyCardID, viewOnlyDeckID)
	}
}

func TestStudyAll_ExhaustedDailyBudgetContributesNothing(t *testing.T) {
	tx := beginTx(t)
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, cookie)

	if w := doRequest(handler, "POST", "/decks/"+deckID+"/edit", "name=D&description=&rev_per_day=1", cookie, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Fatalf("edit deck status = %d: %s", w.Code, w.Body.String())
	}
	gradeCard(t, handler, cookie, cardID, 3, clock) // spends the deck's only rev_per_day slot for today

	resp := doRequest(handler, "GET", "/study", "", cookie, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /study status = %d: %s", resp.Code, resp.Body.String())
	}
	if gotIDs := extractCardIDs(resp.Body.String()); len(gotIDs) != 0 {
		t.Errorf("got cards %v, want none: today's rev_per_day=1 was already spent grading %s", gotIDs, cardID)
	}
}

// TestStudyAll_GradingParity confirms grading a card sourced from the mixed session is the same
// deck-agnostic path as grading it from the card's own deck page (§2.7: review_log/
// user_card_state are keyed (user_id, card_id), no deck column).
func TestStudyAll_GradingParity(t *testing.T) {
	tx := beginTx(t)
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	_, cardID := setupOneCard(t, tx, handler, cookie)

	resp := doRequest(handler, "GET", "/study", "", cookie, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /study status = %d: %s", resp.Code, resp.Body.String())
	}
	gotIDs := extractCardIDs(resp.Body.String())
	if len(gotIDs) != 1 || gotIDs[0] != cardID {
		t.Fatalf("got cards %v, want exactly [%s]", gotIDs, cardID)
	}

	body := fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`,
		newTestEventID(), cardID, clock.Format(time.RFC3339Nano))
	w := doJSON(handler, "POST", "/api/reviews/batch", body, cookie, "http://example.com")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"applied"`) {
		t.Fatalf("grade status = %d, body = %s, want 200 applied", w.Code, w.Body.String())
	}
	if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE card_id = $1`, cardID); n != 1 {
		t.Errorf("review_log rows for card %s = %d, want 1", cardID, n)
	}
}

// -- #172: "Keep studying" -- extraRounds re-grants one more full preset for this fetch only ----

func TestReviewNext_ExtraRoundsGrantsOnePreset(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 10)
	// A small preset (rather than seeding 200 cards, TestReviewNext_KeysetAndExhaustion's
	// pattern) so the deck can genuinely be driven to its cap.
	if _, err := tx.Exec(ctx, `UPDATE decks SET preset = '{"new":{"perDay":2},"rev":{"perDay":2}}' WHERE id = $1`, deckID); err != nil {
		t.Fatalf("set small preset: %v", err)
	}

	firstPage := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=", "", cookie, "")
	if firstPage.Code != http.StatusOK {
		t.Fatalf("first fetch status = %d: %s", firstPage.Code, firstPage.Body.String())
	}
	firstIDs := extractCardIDs(firstPage.Body.String())
	if len(firstIDs) != 2 {
		t.Fatalf("first fetch got %d cards, want 2 (new.perDay)", len(firstIDs))
	}
	for _, id := range firstIDs {
		gradeCard(t, handler, cookie, id, 3, clock) // spends the day's new.perDay=2 allowance
	}

	atCap := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=", "", cookie, "")
	if atCap.Code != http.StatusOK {
		t.Fatalf("at-cap fetch status = %d: %s", atCap.Code, atCap.Body.String())
	}
	if ids := extractCardIDs(atCap.Body.String()); len(ids) != 0 {
		t.Fatalf("at-cap fetch got %d cards, want 0", len(ids))
	}
	if !strings.Contains(atCap.Body.String(), `data-exhausted="true"`) {
		t.Error("at-cap fetch should be exhausted")
	}

	extra := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=&extraRounds=1", "", cookie, "")
	if extra.Code != http.StatusOK {
		t.Fatalf("extraRounds=1 fetch status = %d: %s", extra.Code, extra.Body.String())
	}
	extraIDs := extractCardIDs(extra.Body.String())
	if len(extraIDs) != 2 {
		t.Fatalf("extraRounds=1 fetch got %d cards, want 2 (one more preset's worth)", len(extraIDs))
	}
	for _, id := range extraIDs {
		for _, graded := range firstIDs {
			if id == graded {
				t.Errorf("extraRounds=1 re-served already-graded card %s", id)
			}
		}
	}
}

func TestReviewNext_ExtraRoundsClampedToMax(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 50)
	if _, err := tx.Exec(ctx, `UPDATE decks SET preset = '{"new":{"perDay":2},"rev":{"perDay":2}}' WHERE id = $1`, deckID); err != nil {
		t.Fatalf("set small preset: %v", err)
	}

	atMax := doRequest(handler, "GET", fmt.Sprintf("/api/reviews/next?deck=%s&cursor=&extraRounds=%d", deckID, maxExtraRounds), "", cookie, "")
	if atMax.Code != http.StatusOK {
		t.Fatalf("extraRounds=maxExtraRounds status = %d: %s", atMax.Code, atMax.Body.String())
	}
	huge := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=&extraRounds=999999", "", cookie, "")
	if huge.Code != http.StatusOK {
		t.Fatalf("extraRounds=999999 status = %d, want 200 (no error, overflow, or panic): %s", huge.Code, huge.Body.String())
	}

	atMaxIDs, hugeIDs := extractCardIDs(atMax.Body.String()), extractCardIDs(huge.Body.String())
	if !reflect.DeepEqual(atMaxIDs, hugeIDs) {
		t.Errorf("extraRounds=999999 got %v, want identical to extraRounds=maxExtraRounds (%d): %v", hugeIDs, maxExtraRounds, atMaxIDs)
	}
	// Both requests fetch against the same unconsumed state (neither grades anything), so an
	// unclamped 999999 would still have to be well clear of int32 overflow -- a mismatch or a
	// non-200 status here is the failure this test exists to catch.
	if len(atMaxIDs) == 0 {
		t.Fatal("test setup: expected at least one card served at the clamped cap")
	}
}

func TestReviewNext_ExtraRoundsNegativeOrGarbageIsZero(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 10)
	if _, err := tx.Exec(ctx, `UPDATE decks SET preset = '{"new":{"perDay":2},"rev":{"perDay":2}}' WHERE id = $1`, deckID); err != nil {
		t.Fatalf("set small preset: %v", err)
	}

	omitted := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=", "", cookie, "")
	negative := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=&extraRounds=-5", "", cookie, "")
	garbage := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=&extraRounds=abc", "", cookie, "")
	for _, w := range []*httptest.ResponseRecorder{omitted, negative, garbage} {
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
	}

	omittedIDs := extractCardIDs(omitted.Body.String())
	if len(omittedIDs) != 2 {
		t.Fatalf("omitted got %d cards, want 2 (new.perDay, no extra round)", len(omittedIDs))
	}
	if got := extractCardIDs(negative.Body.String()); !reflect.DeepEqual(got, omittedIDs) {
		t.Errorf("extraRounds=-5 got %v, want identical to omitting the param (%v)", got, omittedIDs)
	}
	if got := extractCardIDs(garbage.Body.String()); !reflect.DeepEqual(got, omittedIDs) {
		t.Errorf("extraRounds=abc got %v, want identical to omitting the param (%v)", got, omittedIDs)
	}
}

// TestReviewNext_ExtraRoundsIsSelectionOnly: an extra-round fetch never writes anything -- the
// deck's user_card_state and review_log row counts for the test's own user are unchanged before
// any grading happens (Q3, resolved decisions). extraRounds never appears in the grade body at
// all (§10.1, §2.7), which TestReviewBatch_ClientCannotWriteSchedulingState already proves
// independently for the wire shape; this test is the read-path half of that guarantee.
func TestReviewNext_ExtraRoundsIsSelectionOnly(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	addNotes(t, handler, cookie, deckPath, noteTypeID, 5)

	var userID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("lookup user: %v", err)
	}

	resp := doRequest(handler, "GET", "/api/reviews/next?deck="+deckID+"&cursor=&extraRounds=5", "", cookie, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
	}
	if len(extractCardIDs(resp.Body.String())) == 0 {
		t.Fatal("test setup: expected at least one card served")
	}

	if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1`, userID); n != 0 {
		t.Errorf("user_card_state rows for user = %d, want 0 (an extra-round fetch selects, never writes)", n)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE user_id = $1`, userID); n != 0 {
		t.Errorf("review_log rows for user = %d, want 0 (an extra-round fetch selects, never writes)", n)
	}
}
