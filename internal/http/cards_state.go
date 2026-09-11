package http

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// registerCardStateRoutes wires POST /decks/{id}/cards/state (#223): suspend/unsuspend/bury/
// flag a card, writing the caller's own user_card_state row. can_study, not can_edit_content --
// this writes the caller's own scheduling state, not deck content (§2.1), and needs no new
// access flag. Called from the reviewer (review.js) via a plain POST (not htmx-fragment-typed
// like flags.go's create route, since the client applies the result to its own in-memory queue
// rather than swapping in server-rendered HTML) and from the deck note list's per-note control.
func registerCardStateRoutes(mux *http.ServeMux, store db.Beginner) {
	mux.Handle("POST /decks/{id}/cards/state", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}
		var cardID pgtype.UUID
		if err := cardID.Scan(r.PostForm.Get("cardId")); err != nil {
			badRequest(w)
			return
		}

		q := db.New(store)
		// Authorise-and-fetch, same shape as flags.go's GetCardForFlag: confirms card_id belongs
		// to deck_id and the caller holds can_study on it. A card missing, invisible, or
		// unstudyable all collapse to zero rows -> pgx.ErrNoRows -> 404 (docs/schema.md).
		card, err := q.GetCardForFlag(r.Context(), db.GetCardForFlagParams{
			UserID: user.ID, CardID: cardID, DeckID: deckID,
		})
		if handleQueryErr(w, r, err) {
			return
		}

		params := db.UpsertUserCardStateSettingsParams{UserID: user.ID, CardID: card.ID}
		switch {
		case r.PostForm.Has("suspend"):
			params.Suspended = pgtype.Bool{Bool: true, Valid: true}
		case r.PostForm.Has("unsuspend"):
			params.Suspended = pgtype.Bool{Bool: false, Valid: true}
		}
		if r.PostForm.Has("bury") {
			day, err := studyTomorrow(r.Context(), q, user.ID, time.Now())
			if err != nil {
				serverError(w, r, err)
				return
			}
			params.BuriedUntil = pgtype.Timestamptz{Time: day, Valid: true}
		} else if r.PostForm.Has("unbury") {
			params.ClearBuried = true
		}
		if raw := r.PostForm.Get("flag"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 7 {
				badRequest(w)
				return
			}
			params.Flag = pgtype.Int2{Int16: int16(n), Valid: true}
		}
		if !params.Suspended.Valid && !params.BuriedUntil.Valid && !params.ClearBuried && !params.Flag.Valid {
			badRequest(w) // no recognised field present
			return
		}

		row, err := q.UpsertUserCardStateSettings(r.Context(), params)
		if err != nil {
			serverError(w, r, err)
			return
		}
		var buriedUntil any
		if row.BuriedUntil.Valid {
			buriedUntil = row.BuriedUntil.Time.Format("2006-01-02")
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"suspended":   row.Suspended,
			"buriedUntil": buriedUntil,
			"flag":        row.Flag,
		})
	})))
}

// studyTomorrow computes "one day past the caller's own study day" (issue #223's bury
// semantics): GetStudyDayWindow (reviews.sql) is the one place the user's timezone/
// day_start_hour arithmetic already lives (architecture.md §5, reused rather than
// reimplemented per CLAUDE.md §9's day-boundary rule). The +24h shift happens here, in Go, so
// that UpsertUserCardStateSettings receives an already-shifted timestamptz and only has to cast
// it with ::date -- the same cast shape ListDueCardsForStudy/CountQueueForDeck use to compare
// buried_until against study_day_start -- rather than doing its own date arithmetic that could
// disagree with those reads' ::date truncation under a non-UTC session timezone.
func studyTomorrow(ctx context.Context, q *db.Queries, userID pgtype.UUID, now time.Time) (time.Time, error) {
	window, err := q.GetStudyDayWindow(ctx, db.GetStudyDayWindowParams{
		UserID: userID, Now: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return time.Time{}, err
	}
	return window.StudyDayStart.Time.Add(24 * time.Hour), nil
}
