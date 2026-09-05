package http

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
	"github.com/Jolls/deckshare/internal/review"
)

// Judgement calls, not derived numbers (docs/plans/87-instructor-dashboard.md §5.2, §5.3) --
// named here so they can move without touching a query or a migration.
const (
	progressWindowDays = 30 // "Pass rate 30d" / "Reviews 30d" / lapse-hotspot window
	progressPageSize   = 25
	hotspotMinReviews  = 5  // floor: at least this many review-state answers on a card...
	hotspotMinStudents = 3  // ...from at least this many distinct students
	hotspotLimit       = 10 // top N cards shown

	noDataDisplay = "—"
)

// studentProgressRow is one roster row, Recall/PassRate folded in Go from ListStudentCardStateForDeck
// and ListStudentProgressForDeck respectively. The *Display fields are what progress.html renders
// -- computed here rather than in the template, since no template FuncMap is registered for
// arithmetic/formatting (internal/http/templates.go) -- and read noDataDisplay ("--"), never 0%,
// when the underlying pointer is nil: the difference between "hasn't started" and "is failing" is
// the whole point of the page (§2.3 of the plan).
type studentProgressRow struct {
	UserID                   pgtype.UUID
	Email                    string
	DisplayName              string
	Seen, New, Learning, Due int64
	Recall                   *float64 // kept (unlike PassRate/LastStudied) because sortStudents reads it
	RecallDisplay            string
	PassRateDisplay          string
	Reviews                  int64
	LastStudiedDisplay       string
}

type lapseHotspotRow struct {
	NoteID           pgtype.UUID
	Label            string
	Students         int64
	Reviews          int64
	AgainRateDisplay string
}

func registerProgressRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template, now func() time.Time) {
	mux.Handle("GET /decks/{id}/progress", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForProgress(r.Context(), db.GetDeckForProgressParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		n := now()
		// Clamped well below math.MaxInt32/progressPageSize so page*progressPageSize below can
		// never overflow int32 into a negative OFFSET -- no real roster needs anywhere near this
		// many pages, so clamping here is simpler than widening OffsetCount's SQL type.
		page := min(parsePositiveInt(r.URL.Query().Get("page"), 0), 1_000_000)
		progressRows, err := q.ListStudentProgressForDeck(r.Context(), db.ListStudentProgressForDeckParams{
			DeckID:           deckID,
			Now:              pgtype.Timestamptz{Time: n, Valid: true},
			LookAheadMinutes: review.DueLookAheadMinutes(deck.Preset),
			WindowDays:       progressWindowDays,
			LimitCount:       progressPageSize,
			OffsetCount:      int32(page) * progressPageSize,
		})
		if err != nil {
			serverError(w)
			return
		}

		students, totalCount, err := foldStudentProgress(r.Context(), q, deckID, n, progressRows)
		if err != nil {
			serverError(w)
			return
		}
		sortStudents(students, r.URL.Query().Get("sort"))

		hotspots, err := q.ListLapseHotspotsForDeck(r.Context(), db.ListLapseHotspotsForDeckParams{
			DeckID:      deckID,
			Now:         pgtype.Timestamptz{Time: n, Valid: true},
			WindowDays:  progressWindowDays,
			MinReviews:  hotspotMinReviews,
			MinStudents: hotspotMinStudents,
			LimitCount:  hotspotLimit,
		})
		if err != nil {
			serverError(w)
			return
		}
		hotspotRows := make([]lapseHotspotRow, len(hotspots))
		for i, h := range hotspots {
			hotspotRows[i] = lapseHotspotRow{
				NoteID: h.NoteID, Label: h.Label, Students: h.StudentCount, Reviews: h.ReviewCount,
				AgainRateDisplay: formatPercent(h.AgainRate),
			}
		}

		render(w, pages["progress"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "AsOf": n,
			"Students": students, "RosterSize": totalCount,
			"Page": page, "PrevPage": page - 1, "NextPage": page + 1,
			"HasNextPage":       (int64(page)+1)*progressPageSize < totalCount,
			"Hotspots":          hotspotRows,
			"HotspotMinReviews": hotspotMinReviews, "HotspotMinStudents": hotspotMinStudents,
		})
	})))
}

// recallAccumulator sums predicted retrievability across a student's seen cards so the mean can
// be taken once every card has been folded in.
type recallAccumulator struct {
	sum float64
	n   int
}

// foldStudentProgress computes each row's Recall (mean predicted retrievability across the
// student's seen cards, §1.1/§1.2 of the plan) from ListStudentCardStateForDeck, scoped to
// exactly the page of students being rendered -- never the whole roster. FSRS params for the
// whole page are resolved in one batched query (review.EffectiveParamsForUsers) rather than one
// round trip per student -- they are still per (user, deck), so nothing is cached across pages or
// decks, only across the cards within one student's fold. totalCount is read off the first row
// (the window-function count is the same on every row); an empty page reports 0.
func foldStudentProgress(ctx context.Context, q *db.Queries, deckID pgtype.UUID, now time.Time, rows []db.ListStudentProgressForDeckRow) ([]studentProgressRow, int64, error) {
	if len(rows) == 0 {
		return nil, 0, nil
	}
	userIDs := make([]pgtype.UUID, len(rows))
	for i, row := range rows {
		userIDs[i] = row.UserID
	}
	cardStates, err := q.ListStudentCardStateForDeck(ctx, db.ListStudentCardStateForDeckParams{DeckID: deckID, UserIds: userIDs})
	if err != nil {
		return nil, 0, err
	}
	paramsByUser, err := review.EffectiveParamsForUsers(ctx, q, userIDs, deckID)
	if err != nil {
		return nil, 0, err
	}
	recall := make(map[pgtype.UUID]*recallAccumulator, len(rows))
	for _, cs := range cardStates {
		r, err := fsrs.Retrievability(paramsByUser[cs.UserID], fsrs.CardState{
			Stability: cs.Stability, Difficulty: cs.Difficulty,
			State: fsrs.State(cs.State), LastReview: cs.LastReview.Time,
		}, now)
		if err != nil {
			return nil, 0, err
		}
		acc := recall[cs.UserID]
		if acc == nil {
			acc = &recallAccumulator{}
			recall[cs.UserID] = acc
		}
		acc.sum += r
		acc.n++
	}

	students := make([]studentProgressRow, len(rows))
	for i, row := range rows {
		s := studentProgressRow{
			UserID: row.UserID, Email: row.Email, DisplayName: row.DisplayName,
			Seen: row.SeenCount, New: row.NewCount, Learning: row.LearningCount, Due: row.DueCount,
			Reviews: row.ReviewCount,
		}
		if acc := recall[row.UserID]; acc != nil {
			mean := acc.sum / float64(acc.n)
			s.Recall = &mean
			s.RecallDisplay = formatPercent(mean)
		} else {
			s.RecallDisplay = noDataDisplay
		}
		if row.ReviewCount > 0 {
			s.PassRateDisplay = formatPercent(float64(row.PassCount) / float64(row.ReviewCount))
		} else {
			s.PassRateDisplay = noDataDisplay
		}
		if row.LastStudied.Valid {
			s.LastStudiedDisplay = row.LastStudied.Time.Format("2006-01-02")
		} else {
			s.LastStudiedDisplay = noDataDisplay
		}
		students[i] = s
	}
	return students, rows[0].TotalCount, nil
}

// formatPercent renders a 0..1 fraction as a whole-percent string.
func formatPercent(frac float64) string {
	return fmt.Sprintf("%.0f%%", frac*100)
}

// sortStudents applies the ?sort= query param (docs/plans/87-instructor-dashboard.md §2.1) to the
// already-fetched page: Recall is Go-computed, so it is never something SQL can ORDER BY, and
// sorting the one page already in memory keeps this a plain slice sort rather than a second
// round trip. Unrecognised or empty values fall back to the default: Recall ascending (weakest
// first, §2.2), with a student that has no Recall yet (nil) sorting last regardless of direction.
//
// This only reorders the current page, not the whole roster -- the roster itself is fetched in a
// fixed (email, §1.2) order and paginated in SQL before Recall is ever computed, which is the
// explicit cost-bound tradeoff §1.2 makes (never fetch every seen-card row for a roster larger
// than one page). The practical consequence: on a roster bigger than progressPageSize, "weakest
// first" finds the weakest student *on the viewed page*, not deck-wide -- true for any classroom
// that fits on one page, which is the common case this dashboard targets.
func sortStudents(students []studentProgressRow, sortBy string) {
	less := func(i, j int) bool {
		a, b := students[i], students[j]
		if a.Recall == nil {
			return false
		}
		if b.Recall == nil {
			return true
		}
		return *a.Recall < *b.Recall
	}
	switch sortBy {
	case "due":
		less = func(i, j int) bool { return students[i].Due > students[j].Due }
	case "reviews":
		less = func(i, j int) bool { return students[i].Reviews > students[j].Reviews }
	case "name":
		less = func(i, j int) bool { return students[i].Email < students[j].Email }
	}
	sort.SliceStable(students, less)
}

// parsePositiveInt parses raw as a non-negative int, falling back to def on empty, unparseable,
// or negative input -- the same "boundary tolerates, never 400s" posture as parseExtraRounds
// (internal/http/review.go).
func parsePositiveInt(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}
