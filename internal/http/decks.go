package http

import (
	"context"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
	"github.com/Jolls/deckshare/internal/review"
)

// queueCounts is the New/Learning/Due summary shown on the decks list and the deck page (#80).
// New is capped to the deck's remaining daily new-card allowance (#106), so it reflects what's
// actually servable today rather than the deck's total unseen-card count.
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
		n := now()
		window, err := studyDayWindow(r.Context(), q, user.ID, n)
		if err != nil {
			serverError(w)
			return
		}
		presetByDeck := make(map[pgtype.UUID][]byte, len(decks))
		deckIDs := make([]pgtype.UUID, len(decks))
		lookAheadMinutes := make([]int32, len(decks))
		classDays := make([]int32, len(decks))
		for i, d := range decks {
			presetByDeck[d.ID] = d.Preset
			deckIDs[i] = d.ID
			lookAheadMinutes[i] = review.DueLookAheadMinutes(d.Preset)
			classDays[i] = review.ReleaseGateDay(d.Preset, window.LocalDate)
		}
		rows, err := q.CountQueueForUser(r.Context(), db.CountQueueForUserParams{
			UserID:           user.ID,
			StudyDayStart:    pgtype.Timestamptz{Time: window.Start, Valid: true},
			Now:              pgtype.Timestamptz{Time: n, Valid: true},
			DeckIds:          deckIDs,
			LookAheadMinutes: lookAheadMinutes,
			CurrentClassDays: classDays,
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
		var totalLeft int64
		for _, row := range rows {
			preset := presetByDeck[row.DeckID]
			newRemaining := review.NewRemaining(review.NewPerDay(preset), introduced[row.DeckID])
			totalRemaining := review.RevRemaining(review.RevPerDay(preset), introduced[row.DeckID]+reviewed[row.DeckID])
			left := review.LeftToStudy(row.NewCount, row.LearningCount, row.DueCount, review.ParsePriority(preset), newRemaining, totalRemaining)
			counts[row.DeckID] = queueCounts{
				New: min(row.NewCount, int64(newRemaining)), Learning: row.LearningCount, Due: row.DueCount,
				Left: left,
			}
			totalLeft += left
		}
		render(w, pages["decks"], http.StatusOK, map[string]any{
			"User": user, "Decks": decks, "Counts": counts, "TotalLeft": totalLeft,
		})
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
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForUser(r.Context(), db.GetDeckForUserParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}
		counts, err := q.CountDeckContents(r.Context(), db.CountDeckContentsParams{DeckID: deckID, UserID: user.ID})
		if err != nil {
			serverError(w)
			return
		}
		notesCursor, ok := decodeNoteCursor(r.URL.Query().Get("notesCursor"))
		if !ok {
			badRequest(w)
			return
		}
		noteRows, err := q.ListNotesInDeck(r.Context(), db.ListNotesInDeckParams{
			UserID: user.ID, DeckID: deckID,
			AtStart: notesCursor.atStart, CursorSortKey: notesCursor.sortKey, CursorID: notesCursor.id,
			LimitCount: notesPageSize + 1, // +1 to detect a next page without a second query
		})
		if err != nil {
			serverError(w)
			return
		}
		hasMoreNotes := len(noteRows) > notesPageSize
		if hasMoreNotes {
			noteRows = noteRows[:notesPageSize]
		}
		var nextNotesCursor string
		if hasMoreNotes {
			last := noteRows[len(noteRows)-1]
			nextNotesCursor = encodeNoteCursor(noteCursor{sortKey: last.SortKey, id: last.ID})
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
		// Resolved once and reused by the queue count, the calendar view, and the template: the
		// same walk over the same calendar three times would otherwise be three chances to drift.
		calendar := review.ParseCalendar(deck.Preset)
		classDay := calendar.CurrentClassDay(window.LocalDate)
		queueRow, err := q.CountQueueForDeck(r.Context(), db.CountQueueForDeckParams{
			UserID:           user.ID,
			DeckID:           deckID,
			StudyDayStart:    pgtype.Timestamptz{Time: window.Start, Valid: true},
			Now:              pgtype.Timestamptz{Time: n, Valid: true},
			LookAheadMinutes: review.DueLookAheadMinutes(deck.Preset),
			CurrentClassDay:  calendar.Gate(classDay),
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
		totalRemaining := review.RevRemaining(review.RevPerDay(deck.Preset), introducedToday+reviewedToday)
		// The disclosure line (#87 §0.4): how many other users can see this viewer's progress on
		// this deck. Not gated on deck.CanViewProgress -- it is about who can see the *viewer's*
		// progress, not whether the viewer themselves holds the flag.
		otherProgressViewers, err := q.CountOtherProgressViewers(r.Context(), db.CountOtherProgressViewersParams{UserID: user.ID, DeckID: deckID})
		if err != nil {
			serverError(w)
			return
		}
		// Feeds the Flags nav badge (deck.html), gated there on CanViewFlags -- queried
		// unconditionally, same as otherProgressViewers above, rather than branching on the flag.
		openFlags, err := q.CountOpenFlagsForDeck(r.Context(), deckID)
		if err != nil {
			serverError(w)
			return
		}
		classDays, unassignedNotes, err := deckClassDays(r.Context(), q, user.ID, deckID, calendar, classDay)
		if err != nil {
			serverError(w)
			return
		}
		unlock, err := nextUnlock(r.Context(), q, user.ID, deckID, calendar, window.LocalDate)
		if err != nil {
			serverError(w)
			return
		}
		render(w, pages["deck"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "Counts": counts, "Notes": noteRows,
			"ClassDays": classDays, "UnassignedNotes": unassignedNotes,
			"CurrentClassDay": classDay, "NextUnlock": unlock,
			"HasMoreNotes": hasMoreNotes, "NextNotesCursor": nextNotesCursor,
			"NotesCursor":          encodeNoteCursor(notesCursor),
			"NotesPaged":           !notesCursor.atStart,
			"DesiredRetention":     params.DesiredRetention(),
			"OtherProgressViewers": otherProgressViewers,
			"OpenFlags":            openFlags,
			"Queue": queueCounts{
				New: min(queueRow.NewCount, int64(newRemaining)), Learning: queueRow.LearningCount, Due: queueRow.DueCount,
				Left: review.LeftToStudy(queueRow.NewCount, queueRow.LearningCount, queueRow.DueCount, review.ParsePriority(deck.Preset), newRemaining, totalRemaining),
			},
		})
	})))

	mux.Handle("GET /decks/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		deck, err := db.New(store).GetDeckForSettingsEdit(r.Context(), db.GetDeckForSettingsEditParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}
		render(w, pages["deck_edit"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck,
			"NewPerDay": review.NewPerDay(deck.Preset), "RevPerDay": review.RevPerDay(deck.Preset),
			"RevOrder": review.ParseRevOrder(deck.Preset), "Priority": review.ParsePriority(deck.Preset),
			"DueLookAheadMinutes": review.DueLookAheadMinutes(deck.Preset),
			"Calendar":            calendarForm(review.ParseCalendar(deck.Preset)),
		})
	})))

	mux.Handle("POST /decks/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
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
		priority := pgtype.Text{} // absent or empty -> leave preset untouched
		if raw := strings.TrimSpace(r.PostForm.Get("priority")); raw != "" {
			if !review.Priority(raw).Valid() {
				badRequest(w)
				return
			}
			priority = pgtype.Text{String: raw, Valid: true}
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

		// The class calendar (#242) is the one setting a form can REMOVE, so removal is its own
		// explicit control rather than an inference from a blank field: a start date left empty
		// while meeting days are ticked is an incomplete calendar (400), not a request to delete
		// the term's pacing, and silently discarding it either way is the worse failure. A
		// calendar section that is entirely empty leaves the setting untouched, like every other
		// field here.
		var calendarJSON []byte
		clearCalendar := r.PostForm.Get("calendar_clear") != ""
		startDate := strings.TrimSpace(r.PostForm.Get("calendar_start_date"))
		hasCalendarInput := startDate != "" || len(r.PostForm["calendar_weekday"]) > 0 ||
			strings.TrimSpace(r.PostForm.Get("calendar_skip")) != ""
		if !clearCalendar && hasCalendarInput {
			calendar, err := review.NewCalendar(startDate,
				r.PostForm["calendar_weekday"], r.PostForm.Get("calendar_skip"))
			if err != nil {
				badRequest(w)
				return
			}
			calendarJSON, err = calendar.JSON()
			if err != nil {
				serverError(w)
				return
			}
		}

		n, err := db.New(store).UpdateDeck(r.Context(), db.UpdateDeckParams{
			Name: name, Description: description, NewPerDay: newPerDay, RevPerDay: revPerDay,
			Priority: priority, RevOrder: revOrder, DueLookAheadMinutes: dueLookAheadMinutes,
			Calendar: calendarJSON, ClearCalendar: clearCalendar,
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
			notFoundPage(w, pages, user)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/delete", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		if handleQueryErrPage(w, pages, user, deleteDeck(r.Context(), store, deckID, user.ID)) {
			return
		}
		http.Redirect(w, r, "/decks", http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/settings/fsrs", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
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
			notFoundPage(w, pages, user)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))
}

// calendarFormView is the deck settings form's class-calendar section (#242): the start date and
// skip list as the form's own text, and one checkbox per weekday. ISO 8601 numbering (Mon=1 ..
// Sun=7), Monday first, matching decks.preset.calendar's stored shape. StartDate is empty for an
// unconfigured calendar rather than a formatted zero time, which is also what the template keys
// the "remove the calendar" checkbox on.
type calendarFormView struct {
	StartDate string
	Weekdays  []weekdayView
	Skip      string
}

type weekdayView struct {
	ISO     int32
	Name    string
	Checked bool
}

// weekdayNames is the checkbox row, Monday first, so its index+1 is the ISO 8601 day number the
// form posts and decks.preset.calendar stores.
var weekdayNames = [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

func calendarForm(cal review.Calendar) calendarFormView {
	form := calendarFormView{Skip: strings.Join(cal.SkipDates(), "\n")}
	if cal.Configured() {
		form.StartDate = cal.StartDate.Format(review.CalendarDateLayout)
	}
	form.Weekdays = make([]weekdayView, len(weekdayNames))
	for i, name := range weekdayNames {
		iso := int32(i + 1)
		form.Weekdays[i] = weekdayView{ISO: iso, Name: name, Checked: slices.Contains(cal.Weekdays, iso)}
	}
	return form
}

// classDayView is one class meeting on the deck page's calendar (#242): its date, how many notes
// are assigned to it, and whether it has already unlocked on the viewer's own clock. An empty
// meeting is legitimate -- a review day, or an exam -- so it is listed rather than hidden.
type classDayView struct {
	Day      int32
	Date     string
	Notes    int64
	Unlocked bool
}

// calendarPreviewMeetings is the floor on how many meetings the deck page's calendar view lists
// (#242). A just-configured calendar has nothing assigned and no meeting reached yet, and that is
// exactly the moment a teacher wants to check that "Tue/Thu from 8 Sep" resolved to the dates she
// meant -- so the section must not be empty then. Four rows show a two-day-a-week pattern twice.
const calendarPreviewMeetings int32 = 4

// deckClassDays builds the deck page's calendar view, and returns the count of notes assigned to
// no lesson alongside it (release_day NULL or 0 -- both mean available immediately, so
// CountNotesByReleaseDay folds them together). An unpaced deck has neither: the query doesn't run
// at all, so the near-totality of decks pay nothing for this section.
//
// The list runs to whichever is further out -- the last lesson a note is assigned to, or the
// meeting the class has actually reached -- so a teacher sees both her unfinished assignments and
// the meetings that have already passed without any.
func deckClassDays(ctx context.Context, q *db.Queries, userID, deckID pgtype.UUID,
	calendar review.Calendar, currentClassDay int32) ([]classDayView, int64, error) {
	if !calendar.Configured() {
		return nil, 0, nil
	}
	rows, err := q.CountNotesByReleaseDay(ctx, db.CountNotesByReleaseDayParams{UserID: userID, DeckID: deckID})
	if err != nil {
		return nil, 0, err
	}
	var unassigned int64
	var lastAssigned int32
	byDay := make(map[int32]int64, len(rows))
	for _, row := range rows {
		if row.ReleaseDay == 0 {
			unassigned = row.NoteCount
			continue
		}
		byDay[row.ReleaseDay] = row.NoteCount
		lastAssigned = max(lastAssigned, row.ReleaseDay)
	}
	dates := calendar.MeetingDates(max(lastAssigned, currentClassDay, calendarPreviewMeetings))
	views := make([]classDayView, len(dates))
	for i, d := range dates {
		day := int32(i + 1)
		views[i] = classDayView{
			Day: day, Date: d.Format("Mon 2 Jan 2006"), Notes: byDay[day], Unlocked: day <= currentClassDay,
		}
	}
	return views, unassigned, nil
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
