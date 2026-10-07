package http

import (
	"bytes"
	"context"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
	"github.com/Jolls/deckshare/internal/review"
)

// The stats page's deck list is ListStudyableDecksForUser (#261): can_view alone must not
// contribute a deck, since a read-only share has nothing the user can study.
func TestListStudyableDecksForUser_CanStudyOnly(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	viewerEmail := testEmail()
	loginCookie(t, tx, a, viewerEmail, "correct-horse-battery")
	viewerID := userID(t, ctx, tx, viewerEmail)

	viewOnly := strings.TrimPrefix(doRequest(handler, "POST", "/decks", "name=View Only", ownerCookie, "http://example.com").Header().Get("Location"), "/decks/")
	studyable := strings.TrimPrefix(doRequest(handler, "POST", "/decks", "name=Studyable", ownerCookie, "http://example.com").Header().Get("Location"), "/decks/")
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view) VALUES ($1, $2, true)`, viewOnly, viewerID); err != nil {
		t.Fatalf("grant view-only: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`, studyable, viewerID); err != nil {
		t.Fatalf("grant study: %v", err)
	}

	decks, err := db.New(tx).ListStudyableDecksForUser(ctx, pgUUID(t, viewerID))
	if err != nil {
		t.Fatalf("ListStudyableDecksForUser: %v", err)
	}
	if len(decks) != 1 || decks[0].ID != pgUUID(t, studyable) {
		t.Errorf("decks = %+v, want only the can_study deck %s", decks, studyable)
	}
}

var (
	statsTrRe  = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	statsTdRe  = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	statsTagRe = regexp.MustCompile(`<[^>]+>`)
	statsSvgRe = regexp.MustCompile(`(?s)<svg.*?</svg>`)
)

// statsCells returns the text of each <td> in the first <tr> containing marker. A deck row reads
// [name, recall, pass rate, reviews, due]; the overall row (marker "overall-row") drops the name.
func statsCells(t *testing.T, body, marker string) []string {
	t.Helper()
	for _, tr := range statsTrRe.FindAllStringSubmatch(body, -1) {
		if !strings.Contains(tr[0], marker) {
			continue
		}
		var cells []string
		for _, td := range statsTdRe.FindAllStringSubmatch(tr[1], -1) {
			cells = append(cells, strings.TrimSpace(html.UnescapeString(statsTagRe.ReplaceAllString(td[1], ""))))
		}
		return cells
	}
	t.Fatalf("no row containing %q in body:\n%s", marker, body)
	return nil
}

func wantCells(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cells = %q, want %q", got, want)
	}
}

// statsEnv is the shared fixture for the /stats tests: one transaction, a handler whose clock is
// pinned to now, and an owner who authors decks. Every assertion is scoped to rows it created.
type statsEnv struct {
	t          *testing.T
	tx         pgx.Tx
	ctx        context.Context
	handler    http.Handler
	a          *auth.Service
	now        time.Time
	owner      *http.Cookie
	noteTypeID string
}

func newStatsEnv(t *testing.T) *statsEnv {
	t.Helper()
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return now })
	e := &statsEnv{t: t, tx: tx, ctx: context.Background(), handler: handler, a: a, now: now}
	ownerEmail := testEmail()
	e.owner = loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	if w := doRequest(handler, "POST", "/note-types", newNoteTypeBody(), e.owner, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Fatalf("create note type status = %d: %s", w.Code, w.Body.String())
	}
	if err := tx.QueryRow(e.ctx, `SELECT id FROM note_types WHERE owner_id = $1 AND name = 'Basic2'`,
		userID(t, e.ctx, tx, ownerEmail)).Scan(&e.noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	return e
}

// deck creates a deck owned by e.owner with n single-card notes, returning its id and card ids.
func (e *statsEnv) deck(name string, n int) (string, []string) {
	e.t.Helper()
	w := doRequest(e.handler, "POST", "/decks", "name="+name, e.owner, "http://example.com")
	path := w.Header().Get("Location")
	id := strings.TrimPrefix(path, "/decks/")
	addNotes(e.t, e.handler, e.owner, path, e.noteTypeID, n)
	return id, lookupCardIDs(e.t, e.ctx, e.tx, id)
}

func (e *statsEnv) user() (*http.Cookie, string) {
	e.t.Helper()
	email := testEmail()
	c := loginCookie(e.t, e.tx, e.a, email, "correct-horse-battery")
	return c, userID(e.t, e.ctx, e.tx, email)
}

func (e *statsEnv) grant(deckID, uid string, study bool) {
	e.t.Helper()
	if _, err := e.tx.Exec(e.ctx, `INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, $3)`,
		deckID, uid, study); err != nil {
		e.t.Fatalf("grant access: %v", err)
	}
}

func (e *statsEnv) seedState(uid, cardID string, due, last time.Time, stability float64) {
	e.t.Helper()
	if _, err := e.tx.Exec(e.ctx, `INSERT INTO user_card_state
		(user_id, card_id, due, stability, difficulty, state, reps, last_review)
		VALUES ($1, $2, $3, $4, 5, 2, 1, $5)`, uid, cardID, due, stability, last); err != nil {
		e.t.Fatalf("seed user_card_state: %v", err)
	}
}

func (e *statsEnv) stats(c *http.Cookie) string {
	e.t.Helper()
	w := doRequest(e.handler, "GET", "/stats", "", c, "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /stats status = %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// §10.5 access-control row for /stats: any logged-in user, no deck required; anonymous goes to login.
func TestStatsRoute_AccessControl(t *testing.T) {
	e := newStatsEnv(t)
	e.deck("Owned", 1)
	emptyCookie, _ := e.user()
	tests := []struct {
		name       string
		cookie     *http.Cookie
		wantStatus int
	}{
		{"no session", nil, http.StatusSeeOther},
		{"user with no decks", emptyCookie, http.StatusOK},
		{"owner with a deck", e.owner, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := doRequest(e.handler, "GET", "/stats", "", tt.cookie, ""); w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestStatsRoute_OnlyOwnNumbers(t *testing.T) {
	e := newStatsEnv(t)
	deckID, cards := e.deck("SharedDeck", 2)
	aCookie, aID := e.user()
	bCookie, bID := e.user()
	e.grant(deckID, aID, true)
	e.grant(deckID, bID, true)
	at := e.now.Add(-time.Hour)
	seedReviewLogRow(t, e.ctx, e.tx, aID, cards[0], 1, at)
	seedReviewLogRow(t, e.ctx, e.tx, aID, cards[1], 3, at)
	seedReviewLogRow(t, e.ctx, e.tx, bID, cards[0], 3, at)
	seedReviewLogRow(t, e.ctx, e.tx, bID, cards[1], 3, at)
	seedReviewLogRow(t, e.ctx, e.tx, bID, cards[1], 3, at)

	got := statsCells(t, e.stats(aCookie), "SharedDeck")
	wantCells(t, got, "SharedDeck", noDataDisplay, "50%", "2", "0")
	got = statsCells(t, e.stats(bCookie), "SharedDeck")
	wantCells(t, got, "SharedDeck", noDataDisplay, "100%", "3", "0")
}

func TestStatsRoute_ExcludesDecksWithoutCanStudy(t *testing.T) {
	e := newStatsEnv(t)
	hiddenID, hiddenCards := e.deck("HiddenDeck", 1)
	visibleID, visibleCards := e.deck("VisibleDeck", 1)
	cookie, uid := e.user()
	e.grant(hiddenID, uid, false)
	e.grant(visibleID, uid, true)
	at := e.now.Add(-time.Hour)
	seedReviewLogRow(t, e.ctx, e.tx, uid, hiddenCards[0], 3, at)
	seedReviewLogRow(t, e.ctx, e.tx, uid, visibleCards[0], 3, at)

	body := e.stats(cookie)
	if strings.Contains(body, "HiddenDeck") {
		t.Errorf("view-only deck listed on stats page:\n%s", body)
	}
	statsCells(t, body, "VisibleDeck")
	wantCells(t, statsCells(t, body, "overall-row"), noDataDisplay, "100%", "1", "0")
}

func TestStatsRoute_MetricsMatchSeededData(t *testing.T) {
	e := newStatsEnv(t)
	deckID, cards := e.deck("MetricsDeck", 3)
	cookie, uid := e.user()
	e.grant(deckID, uid, true)

	// card 0: seen and due; card 1: seen, not due; card 2: never seen.
	last0, last1 := e.now.Add(-72*time.Hour), e.now.Add(-48*time.Hour)
	e.seedState(uid, cards[0], e.now.Add(-24*time.Hour), last0, 10)
	e.seedState(uid, cards[1], e.now.Add(720*time.Hour), last1, 20)
	seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 1, e.now.Add(-time.Hour))
	seedReviewLogRow(t, e.ctx, e.tx, uid, cards[1], 3, e.now.Add(-time.Hour))

	params, err := review.EffectiveParams(e.ctx, db.New(e.tx), pgUUID(t, uid), pgUUID(t, deckID))
	if err != nil {
		t.Fatalf("EffectiveParams: %v", err)
	}
	var recall float64
	for _, s := range []struct {
		last      time.Time
		stability float64
	}{{last0, 10}, {last1, 20}} {
		r, err := fsrs.Retrievability(params, fsrs.CardState{Stability: s.stability, Difficulty: 5, State: fsrs.State(2), LastReview: s.last}, e.now)
		if err != nil {
			t.Fatalf("Retrievability: %v", err)
		}
		recall += r / 2
	}

	body := e.stats(cookie)
	wantCells(t, statsCells(t, body, "MetricsDeck"), "MetricsDeck", formatPercent(recall), "50%", "2", "1")
	wantCells(t, statsCells(t, body, "overall-row"), formatPercent(recall), "50%", "2", "1")
}

func TestStatsRoute_NoDataShowsDash(t *testing.T) {
	e := newStatsEnv(t)
	deckID, _ := e.deck("EmptyDeck", 1)
	cookie, uid := e.user()
	e.grant(deckID, uid, true)

	body := e.stats(cookie)
	wantCells(t, statsCells(t, body, "EmptyDeck"), "EmptyDeck", noDataDisplay, noDataDisplay, "0", "0")
	wantCells(t, statsCells(t, body, "overall-row"), noDataDisplay, noDataDisplay, "0", "0")
}

func TestStatsRoute_ReviewsOutsideWindowExcluded(t *testing.T) {
	e := newStatsEnv(t)
	deckID, cards := e.deck("WindowDeck", 1)
	cookie, uid := e.user()
	e.grant(deckID, uid, true)
	seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 3, e.now.AddDate(0, 0, -31))
	seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 1, e.now.AddDate(0, 0, -29))

	wantCells(t, statsCells(t, e.stats(cookie), "WindowDeck"), "WindowDeck", noDataDisplay, "0%", "1", "0")
}

// Mirror of TestListStudentProgressForDeck_DayBoundaryIsPerStudent for the stats page's Due.
func TestStatsRoute_DueUsesOwnDayBoundary(t *testing.T) {
	e := newStatsEnv(t)
	deckID, cards := e.deck("BoundaryDeck", 1)
	aCookie, aID := e.user()
	bCookie, bID := e.user()
	setUserTimezone(t, e.ctx, e.tx, aID, "UTC", 4)
	setUserTimezone(t, e.ctx, e.tx, bID, "Pacific/Kiritimati", 4)
	lastReview := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	for _, uid := range []string{aID, bID} {
		e.grant(deckID, uid, true)
		e.seedState(uid, cards[0], lastReview, lastReview, 10)
	}

	// A has not studied in their study day yet (rollover 04:00 UTC); B already has (rollover
	// 14 hours earlier).
	if got := statsCells(t, e.stats(aCookie), "BoundaryDeck"); got[4] != "1" {
		t.Errorf("A (UTC) due = %q, want 1", got[4])
	}
	if got := statsCells(t, e.stats(bCookie), "BoundaryDeck"); got[4] != "0" {
		t.Errorf("B (Kiritimati) due = %q, want 0", got[4])
	}
}

func TestStatsRoute_ChartsInlineSVGNoScript(t *testing.T) {
	e := newStatsEnv(t)
	deckID, cards := e.deck("ChartDeck", 1)
	cookie, uid := e.user()
	e.grant(deckID, uid, true)
	// Two distinct study days (rollover 04:00 UTC): 06-15 09:00 and 06-14 09:00.
	seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 3, e.now.Add(-time.Hour))
	seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 1, e.now.Add(-25*time.Hour))

	svgs := statsSvgRe.FindAllString(e.stats(cookie), -1)
	if len(svgs) != 2 {
		t.Fatalf("svg count = %d, want 2", len(svgs))
	}
	for _, s := range svgs {
		if strings.Contains(s, "<script") {
			t.Errorf("svg contains a script: %s", s)
		}
	}
	if got := strings.Count(svgs[0], `class="bar"`); got != progressWindowDays {
		t.Errorf("reviews-per-day bars = %d, want %d (zero days plot as empty bars)", got, progressWindowDays)
	}
	if got := strings.Count(svgs[1], `class="pt"`); got != 2 {
		t.Errorf("pass-rate points = %d, want 2 (days with no reviews are gaps)", got)
	}
}

func TestStatsRoute_ReadOnly(t *testing.T) {
	e := newStatsEnv(t)
	deckID, cards := e.deck("ReadOnlyDeck", 1)
	cookie, uid := e.user()
	e.grant(deckID, uid, true)
	e.seedState(uid, cards[0], e.now, e.now.Add(-time.Hour), 10)
	seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 3, e.now.Add(-time.Hour))
	count := func() (int64, int64) {
		return countRows(t, e.tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1`, uid),
			countRows(t, e.tx, `SELECT count(*) FROM review_log WHERE user_id = $1`, uid)
	}
	stateBefore, logBefore := count()

	e.stats(cookie)
	if w := doRequest(e.handler, "POST", "/stats", "x=1", cookie, "http://example.com"); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
		t.Errorf("POST /stats status = %d, want 405 or 404", w.Code)
	}
	if stateAfter, logAfter := count(); stateAfter != stateBefore || logAfter != logBefore {
		t.Errorf("rows changed: user_card_state %d->%d, review_log %d->%d", stateBefore, stateAfter, logBefore, logAfter)
	}
}

// Template-only guard (no DB), modelled on TestProgressTemplateRenders: a field-path typo only
// surfaces at Execute time.
func TestStatsTemplateRenders(t *testing.T) {
	pages, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	if pages["stats"] == nil {
		t.Fatal("no stats page template registered")
	}
	var buf bytes.Buffer
	err = pages["stats"].ExecuteTemplate(&buf, "layout", map[string]any{
		"User": db.User{}, "AsOf": time.Now(),
		"Overall": statsRow{RecallDisplay: "82%", PassRateDisplay: noDataDisplay, Reviews: 12, Due: 3},
		"Decks": []statsRow{
			{Name: "Spanish 101", RecallDisplay: "82%", PassRateDisplay: "90%", Reviews: 12, Due: 3},
		},
		"ReviewsChart":  template.HTML(`<svg><rect class="bar"></rect></svg>`),
		"PassRateChart": template.HTML(`<svg><circle class="pt"></circle></svg>`),
	})
	if err != nil {
		t.Fatalf("execute stats template: %v", err)
	}
	body := buf.String()
	for _, want := range []string{"Spanish 101", "82%", noDataDisplay, "overall-row", `class="bar"`, `class="pt"`} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body missing %q:\n%s", want, body)
		}
	}
}

// The chart's day buckets follow the caller's own study day (timezone + day_start_hour), computed
// inside the query -- asserted directly rather than through the SVG. Each case puts one review
// just before and one just after that user's rollover.
func TestListStatsDailyReviewsForUser_BucketsByOwnStudyDay(t *testing.T) {
	e := newStatsEnv(t)
	deckID, cards := e.deck("BucketDeck", 1)
	tests := []struct {
		name     string
		timezone string
		before   time.Time // last instant of the previous study day
		after    time.Time // first instant of the next study day
		wantPrev string
		wantNext string
	}{
		{"UTC", "UTC", time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC), time.Date(2026, 6, 15, 4, 30, 0, 0, time.UTC), "2026-06-14", "2026-06-15"},
		{"Kiritimati", "Pacific/Kiritimati", time.Date(2026, 6, 15, 13, 30, 0, 0, time.UTC), time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC), "2026-06-15", "2026-06-16"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, uid := e.user()
			e.grant(deckID, uid, true)
			setUserTimezone(t, e.ctx, e.tx, uid, tt.timezone, 4)
			seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 3, tt.before)
			seedReviewLogRow(t, e.ctx, e.tx, uid, cards[0], 1, tt.after)

			rows, err := db.New(e.tx).ListStatsDailyReviewsForUser(e.ctx, db.ListStatsDailyReviewsForUserParams{
				UserID: pgUUID(t, uid), FirstDay: pgtype.Date{Time: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), Valid: true},
			})
			if err != nil {
				t.Fatalf("ListStatsDailyReviewsForUser: %v", err)
			}
			got := map[string]int64{}
			for _, r := range rows {
				got[r.Day.Time.Format("2006-01-02")] = r.ReviewCount
			}
			if len(got) != 2 || got[tt.wantPrev] != 1 || got[tt.wantNext] != 1 {
				t.Errorf("days = %v, want one review on %s and one on %s", got, tt.wantPrev, tt.wantNext)
			}
		})
	}
}

// The charts follow the colour scheme only because every fill/stroke is currentColor (#261); a
// hard-coded colour here would be unreadable in one of the modes (#268).
func TestStatsCharts_NoFixedColours(t *testing.T) {
	days := []statsDay{
		{Date: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Reviews: 10, Passes: 8},
		{Date: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), Reviews: 0},
		{Date: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), Reviews: 4, Passes: 4},
	}
	colour := regexp.MustCompile(`(?:fill|stroke)="([^"]*)"`)
	for name, svg := range map[string]template.HTML{"reviews": reviewsChartSVG(days), "passRate": passRateChartSVG(days)} {
		for _, m := range colour.FindAllStringSubmatch(string(svg), -1) {
			if m[1] != "currentColor" && m[1] != "none" {
				t.Errorf("%s chart has fixed colour %s", name, m[0])
			}
		}
	}
}
