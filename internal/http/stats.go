package http

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
	"github.com/Jolls/deckshare/internal/review"
)

// statsRow is one line of the learner stats page (#261): the overall summary or one deck. The
// *Display fields follow studentProgressRow's convention -- noDataDisplay, never 0%, when there is
// nothing to average.
type statsRow struct {
	Name            string
	DeckID          pgtype.UUID
	RecallDisplay   string
	PassRateDisplay string
	Reviews         int64
	Due             int64
}

// statsDay is one study day of the two history charts. Days with no reviews are kept (Reviews 0)
// so the reviews chart plots them as empty bars; the pass-rate chart skips them.
type statsDay struct {
	Date    time.Time
	Reviews int64
	Passes  int64
}

func registerStatsRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template, now func() time.Time) {
	mux.Handle("GET /stats", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		ctx := r.Context()
		q := db.New(store)
		n := now()

		decks, err := q.ListStudyableDecksForUser(ctx, user.ID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		window, err := studyDayWindow(ctx, q, user.ID, n)
		if err != nil {
			serverError(w, r, err)
			return
		}

		// Due is CountQueueForUser's DueCount, built exactly as GET /decks builds it, so the two
		// pages can never disagree about the same card.
		deckIDs := make([]pgtype.UUID, len(decks))
		lookAheadMinutes := make([]int32, len(decks))
		classDays := make([]int32, len(decks))
		for i, d := range decks {
			deckIDs[i] = d.ID
			lookAheadMinutes[i] = review.DueLookAheadMinutes(d.Preset)
			classDays[i] = review.ReleaseGateDay(d.Preset, window.LocalDate)
		}
		queueRows, err := q.CountQueueForUser(ctx, db.CountQueueForUserParams{
			UserID:           user.ID,
			StudyDayStart:    pgtype.Timestamptz{Time: window.Start, Valid: true},
			Now:              pgtype.Timestamptz{Time: n, Valid: true},
			DeckIds:          deckIDs,
			LookAheadMinutes: lookAheadMinutes,
			CurrentClassDays: classDays,
		})
		if err != nil {
			serverError(w, r, err)
			return
		}
		due := make(map[pgtype.UUID]int64, len(queueRows))
		for _, row := range queueRows {
			due[row.DeckID] = row.DueCount
		}

		// Recall now: mean retrievability over seen cards, the same fold as foldStudentProgress.
		// One overall accumulator, so a big deck weighs more than a small one.
		cardStates, err := q.ListStatsCardStateForUser(ctx, user.ID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		paramsByDeck := make(map[pgtype.UUID]fsrs.Params, len(decks))
		recallByDeck := make(map[pgtype.UUID]*recallAccumulator, len(decks))
		var overallRecall recallAccumulator
		for _, cs := range cardStates {
			params, ok := paramsByDeck[cs.DeckID]
			if !ok {
				params, err = review.EffectiveParams(ctx, q, user.ID, cs.DeckID)
				if err != nil {
					serverError(w, r, err)
					return
				}
				paramsByDeck[cs.DeckID] = params
			}
			rt, err := fsrs.Retrievability(params, fsrs.CardState{
				Stability: cs.Stability, Difficulty: cs.Difficulty,
				State: fsrs.State(cs.State), LastReview: cs.LastReview.Time,
			}, n)
			if err != nil {
				serverError(w, r, err)
				return
			}
			acc := recallByDeck[cs.DeckID]
			if acc == nil {
				acc = &recallAccumulator{}
				recallByDeck[cs.DeckID] = acc
			}
			acc.sum += rt
			acc.n++
			overallRecall.sum += rt
			overallRecall.n++
		}

		counts, err := q.ListStatsReviewCountsForUser(ctx, db.ListStatsReviewCountsForUserParams{
			UserID: user.ID, Now: pgtype.Timestamptz{Time: n, Valid: true}, WindowDays: progressWindowDays,
		})
		if err != nil {
			serverError(w, r, err)
			return
		}
		countsByDeck := make(map[pgtype.UUID]db.ListStatsReviewCountsForUserRow, len(counts))
		var overallReviews, overallPasses, overallDue int64
		for _, c := range counts {
			countsByDeck[c.DeckID] = c
			overallReviews += c.ReviewCount
			overallPasses += c.PassCount
		}

		rows := make([]statsRow, len(decks))
		for i, d := range decks {
			c := countsByDeck[d.ID]
			rows[i] = statsRow{
				Name: d.Name, DeckID: d.ID,
				RecallDisplay:   recallDisplay(recallByDeck[d.ID]),
				PassRateDisplay: passRateDisplay(c.PassCount, c.ReviewCount),
				Reviews:         c.ReviewCount, Due: due[d.ID],
			}
			overallDue += due[d.ID]
		}

		days, err := statsDays(ctx, q, user.ID, window.LocalDate)
		if err != nil {
			serverError(w, r, err)
			return
		}

		render(w, pages["stats"], http.StatusOK, map[string]any{
			"User": user, "AsOf": n,
			"Overall": statsRow{
				RecallDisplay:   recallDisplay(&overallRecall),
				PassRateDisplay: passRateDisplay(overallPasses, overallReviews),
				Reviews:         overallReviews, Due: overallDue,
			},
			"Decks":         rows,
			"ReviewsChart":  reviewsChartSVG(days),
			"PassRateChart": passRateChartSVG(days),
		})
	})))
}

func recallDisplay(acc *recallAccumulator) string {
	if acc == nil || acc.n == 0 {
		return noDataDisplay
	}
	return formatPercent(acc.sum / float64(acc.n))
}

func passRateDisplay(passes, reviews int64) string {
	if reviews == 0 {
		return noDataDisplay
	}
	return formatPercent(float64(passes) / float64(reviews))
}

// statsDays returns exactly progressWindowDays study days ending at today (today's local study
// date), oldest first, zero-filled where the user did not review.
func statsDays(ctx context.Context, q *db.Queries, userID pgtype.UUID, today time.Time) ([]statsDay, error) {
	first := today.AddDate(0, 0, -(progressWindowDays - 1))
	rows, err := q.ListStatsDailyReviewsForUser(ctx, db.ListStatsDailyReviewsForUserParams{
		UserID: userID, FirstDay: pgtype.Date{Time: first, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	byDay := make(map[string]db.ListStatsDailyReviewsForUserRow, len(rows))
	for _, row := range rows {
		byDay[row.Day.Time.Format("2006-01-02")] = row
	}
	days := make([]statsDay, progressWindowDays)
	for i := range days {
		d := first.AddDate(0, 0, i)
		row := byDay[d.Format("2006-01-02")]
		days[i] = statsDay{Date: d, Reviews: row.ReviewCount, Passes: row.PassCount}
	}
	return days, nil
}

// Chart geometry, shared by both charts so their day columns line up.
const (
	chartW      = 600
	chartH      = 160
	chartTop    = 20
	chartBottom = 140 // baseline y
	chartSlot   = chartW / progressWindowDays
)

// reviewsChartSVG draws reviews per day as bars, one per day including empty ones. Only numbers
// and dates are interpolated, so the result is safe as template.HTML.
func reviewsChartSVG(days []statsDay) template.HTML {
	var peak int64
	for _, d := range days {
		peak = max(peak, d.Reviews)
	}
	var b strings.Builder
	chartOpen(&b, "Reviews per day, last 30 days")
	fmt.Fprintf(&b, `<text x="0" y="12" font-size="11" fill="currentColor">%d</text>`, peak)
	for i, d := range days {
		h := 0
		if peak > 0 {
			h = int(float64(chartBottom-chartTop) * float64(d.Reviews) / float64(peak))
		}
		fmt.Fprintf(&b, `<rect class="bar" x="%d" y="%d" width="%d" height="%d" fill="currentColor"><title>%s: %d reviews</title></rect>`,
			i*chartSlot+2, chartBottom-h, chartSlot-4, h, d.Date.Format("2006-01-02"), d.Reviews)
	}
	chartClose(&b, days)
	return template.HTML(b.String()) //nolint:gosec // G203: built only from integers and formatted dates, nothing user-controlled
}

// passRateChartSVG draws pass rate per day as points joined by lines. A day with no reviews has
// no point and breaks the line -- there is no rate to plot, which is not the same as 0%.
func passRateChartSVG(days []statsDay) template.HTML {
	var b strings.Builder
	chartOpen(&b, "Pass rate per day, last 30 days")
	b.WriteString(`<text x="0" y="12" font-size="11" fill="currentColor">100%</text>`)
	var run []string
	flush := func() {
		if len(run) > 1 {
			fmt.Fprintf(&b, `<polyline class="line" fill="none" stroke="currentColor" stroke-width="2" points="%s"/>`, strings.Join(run, " "))
		}
		run = run[:0]
	}
	var points strings.Builder
	for i, d := range days {
		if d.Reviews == 0 {
			flush()
			continue
		}
		rate := float64(d.Passes) / float64(d.Reviews)
		x := i*chartSlot + chartSlot/2
		y := chartBottom - int(float64(chartBottom-chartTop)*rate)
		run = append(run, fmt.Sprintf("%d,%d", x, y))
		fmt.Fprintf(&points, `<circle class="pt" cx="%d" cy="%d" r="3" fill="currentColor"><title>%s: %s (%d reviews)</title></circle>`,
			x, y, d.Date.Format("2006-01-02"), formatPercent(rate), d.Reviews)
	}
	flush()
	b.WriteString(points.String())
	chartClose(&b, days)
	return template.HTML(b.String()) //nolint:gosec // G203: built only from integers and formatted dates, nothing user-controlled
}

func chartOpen(b *strings.Builder, label string) {
	fmt.Fprintf(b, `<svg class="chart" viewBox="0 0 %d %d" role="img" aria-label="%s" xmlns="http://www.w3.org/2000/svg">`, chartW, chartH, label)
	fmt.Fprintf(b, `<line x1="0" y1="%d" x2="%d" y2="%d" stroke="currentColor" stroke-opacity="0.4"/>`, chartBottom, chartW, chartBottom)
}

func chartClose(b *strings.Builder, days []statsDay) {
	fmt.Fprintf(b, `<text x="0" y="%d" font-size="11" fill="currentColor">%s</text>`, chartH-4, days[0].Date.Format("2006-01-02"))
	fmt.Fprintf(b, `<text x="%d" y="%d" font-size="11" fill="currentColor" text-anchor="end">%s</text>`, chartW, chartH-4, days[len(days)-1].Date.Format("2006-01-02"))
	b.WriteString(`</svg>`)
}
