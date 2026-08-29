package http

import (
	"context"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	"github.com/Jolls/enshu/internal/review"
)

// queueCounts is the New/Learning/Due summary shown on the decks list and the deck page (#80).
type queueCounts struct {
	New      int64
	Learning int64
	Due      int64
	Left     int64
}

func registerDeckRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template, now func() time.Time) {
	mux.Handle("GET /decks", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		q := db.New(store)
		decks, err := q.ListDecksForUser(r.Context(), user.ID)
		if err != nil {
			serverError(w)
			return
		}
		presetByDeck := make(map[pgtype.UUID][]byte, len(decks))
		deckIDs := make([]pgtype.UUID, len(decks))
		lookAheadMinutes := make([]int32, len(decks))
		for i, d := range decks {
			presetByDeck[d.ID] = d.Preset
			deckIDs[i] = d.ID
			lookAheadMinutes[i] = review.DueLookAheadMinutes(d.Preset)
		}
		n := now()
		window, err := studyDayWindow(r.Context(), q, user.ID, n)
		if err != nil {
			serverError(w)
			return
		}
		rows, err := q.CountQueueForUser(r.Context(), db.CountQueueForUserParams{
			UserID:           user.ID,
			StudyDayStart:    pgtype.Timestamptz{Time: window.Start, Valid: true},
			Now:              pgtype.Timestamptz{Time: n, Valid: true},
			DeckIds:          deckIDs,
			LookAheadMinutes: lookAheadMinutes,
		})
		if err != nil {
			serverError(w)
			return
		}
		introducedRows, err := q.CountNewIntroducedTodayForUser(r.Context(), db.CountNewIntroducedTodayForUserParams{
			UserID:        user.ID,
			StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
			StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
		})
		if err != nil {
			serverError(w)
			return
		}
		introduced := make(map[pgtype.UUID]int64, len(introducedRows))
		for _, row := range introducedRows {
			introduced[row.DeckID] = row.IntroducedCount
		}
		reviewedRows, err := q.CountReviewedTodayForUser(r.Context(), db.CountReviewedTodayForUserParams{
			UserID:        user.ID,
			StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
			StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
		})
		if err != nil {
			serverError(w)
			return
		}
		reviewed := make(map[pgtype.UUID]int64, len(reviewedRows))
		for _, row := range reviewedRows {
			reviewed[row.DeckID] = row.ReviewedCount
		}
		counts := make(map[pgtype.UUID]queueCounts, len(rows))
		for _, row := range rows {
			newRemaining := review.NewRemaining(review.NewPerDay(presetByDeck[row.DeckID]), introduced[row.DeckID])
			revRemaining := review.RevRemaining(review.RevPerDay(presetByDeck[row.DeckID]), reviewed[row.DeckID])
			counts[row.DeckID] = queueCounts{
				New: row.NewCount, Learning: row.LearningCount, Due: row.DueCount,
				Left: review.LeftToStudy(row.NewCount, row.LearningCount, row.DueCount, newRemaining, revRemaining),
			}
		}
		render(w, pages["decks"], http.StatusOK, map[string]any{"User": user, "Decks": decks, "Counts": counts})
	})))

	mux.Handle("GET /decks/new", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		render(w, pages["deck_new"], http.StatusOK, map[string]any{"User": user})
	})))

	mux.Handle("POST /decks", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if !parseForm(w, r) {
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		description := r.PostForm.Get("description")
		if name == "" || len(name) > 200 {
			render(w, pages["deck_new"], http.StatusBadRequest, map[string]any{
				"User": user, "Name": name, "Description": description,
				"Error": "Name must be between 1 and 200 characters",
			})
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		deck, err := db.CreateDeckWithAccess(r.Context(), tx, user.ID, name, description)
		if err != nil {
			if db.IsUniqueViolation(err, "decks_owner_id_name_key") {
				render(w, pages["deck_new"], http.StatusConflict, map[string]any{
					"User": user, "Name": name, "Description": description,
					"Error": "You already have a deck with that name",
				})
				return
			}
			serverError(w)
			return
		}
		if !commitTx(r.Context(), w, tx) {
			return
		}
		http.Redirect(w, r, "/decks/"+deck.ID.String(), http.StatusSeeOther)
	})))

	mux.Handle("GET /decks/{id}", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForUser(r.Context(), db.GetDeckForUserParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErr(w, err) {
			return
		}
		counts, err := q.CountDeckContents(r.Context(), db.CountDeckContentsParams{DeckID: deckID, UserID: user.ID})
		if err != nil {
			serverError(w)
			return
		}
		notes, err := q.ListNotesInDeck(r.Context(), db.ListNotesInDeckParams{UserID: user.ID, DeckID: deckID})
		if err != nil {
			serverError(w)
			return
		}
		params, err := review.EffectiveParams(r.Context(), q, user.ID, deckID)
		if err != nil {
			serverError(w)
			return
		}
		n := now()
		window, err := studyDayWindow(r.Context(), q, user.ID, n)
		if err != nil {
			serverError(w)
			return
		}
		queueRow, err := q.CountQueueForDeck(r.Context(), db.CountQueueForDeckParams{
			UserID:           user.ID,
			DeckID:           deckID,
			StudyDayStart:    pgtype.Timestamptz{Time: window.Start, Valid: true},
			Now:              pgtype.Timestamptz{Time: n, Valid: true},
			LookAheadMinutes: review.DueLookAheadMinutes(deck.Preset),
		})
		if err != nil {
			serverError(w)
			return
		}
		introducedToday, err := q.CountNewIntroducedToday(r.Context(), db.CountNewIntroducedTodayParams{
			UserID:        user.ID,
			DeckID:        deckID,
			StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
			StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
		})
		if err != nil {
			serverError(w)
			return
		}
		reviewedToday, err := q.CountReviewedToday(r.Context(), db.CountReviewedTodayParams{
			UserID:        user.ID,
			DeckID:        deckID,
			StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
			StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
		})
		if err != nil {
			serverError(w)
			return
		}
		newRemaining := review.NewRemaining(review.NewPerDay(deck.Preset), introducedToday)
		revRemaining := review.RevRemaining(review.RevPerDay(deck.Preset), reviewedToday)
		render(w, pages["deck"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "Counts": counts, "Notes": notes,
			"DesiredRetention": params.DesiredRetention(),
			"Queue": queueCounts{
				New: queueRow.NewCount, Learning: queueRow.LearningCount, Due: queueRow.DueCount,
				Left: review.LeftToStudy(queueRow.NewCount, queueRow.LearningCount, queueRow.DueCount, newRemaining, revRemaining),
			},
		})
	})))

	mux.Handle("GET /decks/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		deck, err := db.New(store).GetDeckForSettingsEdit(r.Context(), db.GetDeckForSettingsEditParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErr(w, err) {
			return
		}
		render(w, pages["deck_edit"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck,
			"NewPerDay": review.NewPerDay(deck.Preset), "RevPerDay": review.RevPerDay(deck.Preset),
			"RevOrder": review.ParseRevOrder(deck.Preset), "NewMix": review.ParseNewMix(deck.Preset),
			"DueLookAheadMinutes": review.DueLookAheadMinutes(deck.Preset),
		})
	})))

	mux.Handle("POST /decks/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		description := r.PostForm.Get("description")
		if name == "" || len(name) > 200 {
			badRequest(w)
			return
		}
		newPerDay := pgtype.Int4{} // absent or empty -> leave preset untouched
		if raw := strings.TrimSpace(r.PostForm.Get("new_per_day")); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 0 || int32(v) > review.MaxNewPerDay {
				badRequest(w)
				return
			}
			newPerDay = pgtype.Int4{Int32: int32(v), Valid: true}
		}
		revPerDay := pgtype.Int4{} // absent or empty -> leave preset untouched
		if raw := strings.TrimSpace(r.PostForm.Get("rev_per_day")); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 0 || int32(v) > review.MaxRevPerDay {
				badRequest(w)
				return
			}
			revPerDay = pgtype.Int4{Int32: int32(v), Valid: true}
		}
		newMix := pgtype.Text{} // absent or empty -> leave preset untouched
		if raw := strings.TrimSpace(r.PostForm.Get("new_mix")); raw != "" {
			if !review.NewMix(raw).Valid() {
				badRequest(w)
				return
			}
			newMix = pgtype.Text{String: raw, Valid: true}
		}
		revOrder := pgtype.Text{} // absent or empty -> leave preset untouched
		if raw := strings.TrimSpace(r.PostForm.Get("rev_order")); raw != "" {
			if !review.RevOrder(raw).Valid() {
				badRequest(w)
				return
			}
			revOrder = pgtype.Text{String: raw, Valid: true}
		}
		dueLookAheadMinutes := pgtype.Int4{} // absent or empty -> leave preset untouched
		if raw := strings.TrimSpace(r.PostForm.Get("due_look_ahead_minutes")); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 0 || int32(v) > review.MaxDueLookAheadMinutes {
				badRequest(w)
				return
			}
			dueLookAheadMinutes = pgtype.Int4{Int32: int32(v), Valid: true}
		}

		n, err := db.New(store).UpdateDeck(r.Context(), db.UpdateDeckParams{
			Name: name, Description: description, NewPerDay: newPerDay, RevPerDay: revPerDay,
			NewMix: newMix, RevOrder: revOrder, DueLookAheadMinutes: dueLookAheadMinutes,
			DeckID: deckID, UserID: user.ID,
		})
		if err != nil {
			if db.IsUniqueViolation(err, "decks_owner_id_name_key") {
				http.Error(w, "a deck with that name already exists", http.StatusConflict)
				return
			}
			serverError(w)
			return
		}
		if n == 0 {
			notFound(w)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/delete", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if handleQueryErr(w, deleteDeck(r.Context(), store, deckID, user.ID)) {
			return
		}
		http.Redirect(w, r, "/decks", http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/settings/fsrs", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}
		retention, atoiErr := strconv.ParseFloat(r.PostForm.Get("desired_retention"), 64)
		if atoiErr != nil {
			badRequest(w)
			return
		}
		params, err := fsrs.NewDefaultParams(retention)
		if err != nil {
			badRequest(w)
			return
		}

		n, err := db.New(store).UpsertDeckFsrsRetention(r.Context(), db.UpsertDeckFsrsRetentionParams{
			UserID: user.ID, DeckID: deckID, FsrsVersion: int16(params.Version()), DesiredRetention: retention,
		})
		if err != nil {
			serverError(w)
			return
		}
		if n == 0 {
			notFound(w)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))
}

func deleteDeck(ctx context.Context, store db.Beginner, deckID, userID pgtype.UUID) error {
	tx, err := store.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := db.DeleteDeck(ctx, tx, deckID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
