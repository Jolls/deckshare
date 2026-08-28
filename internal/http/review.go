package http

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	cardrender "github.com/Jolls/enshu/internal/render"
	"github.com/Jolls/enshu/internal/review"
)

const (
	initialBatchSize = 20
	refillBatchSize  = 20
	maxBatchBody     = 64 * 1024
	maxEventsPerPost = 100
)

// registerReviewRoutes wires the reviewer's three routes (architecture.md §6, docs/routes.md).
// now is injected so tests can pin the clock; production passes time.Now.
func registerReviewRoutes(mux *http.ServeMux, store db.Beginner, pages, fragments map[string]*template.Template, now func() time.Time) {
	mux.Handle("GET /decks/{id}/review", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForStudy(r.Context(), db.GetDeckForStudyParams{UserID: user.ID, DeckID: deckID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFound(w)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		clock := now()
		batch, err := buildStudyBatch(r.Context(), store, user.ID, deck, review.Cursor{AtStart: true}, initialBatchSize, clock)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		css, err := noteTypeCSS(r.Context(), q, user.ID, deckID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		render(w, pages["review"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "CSS": css, "Batch": toBatchView(batch),
		})
	})))

	// No auth.RequireUser on the two API routes: an expired session mid-review must answer with a
	// JSON 401 the client's sender can detect, not a 303-to-login that silently drops the events
	// riding along in the request (plan resolved decision 10).
	mux.Handle("GET /api/reviews/next", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			unauthenticatedJSON(w)
			return
		}
		var deckID pgtype.UUID
		if err := deckID.Scan(r.URL.Query().Get("deck")); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		cur, err := review.DecodeCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		q := db.New(store)
		deck, err := q.GetDeckForStudy(r.Context(), db.GetDeckForStudyParams{UserID: user.ID, DeckID: deckID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFound(w)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		clock := now()
		batch, err := buildStudyBatch(r.Context(), store, user.ID, deck, cur, refillBatchSize, clock)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		renderFragment(w, fragments["review_cards"], http.StatusOK, "review_cards", toBatchView(batch))
	}))

	mux.Handle("POST /api/reviews/batch", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			unauthenticatedJSON(w)
			return
		}
		events, ok := parseBatchRequest(w, r)
		if !ok {
			return
		}

		tx, err := store.Begin(r.Context())
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		results, err := review.GradeBatch(r.Context(), tx, user.ID, now(), events)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": toResultViews(results)})
	}))
}

func studyDayWindow(ctx context.Context, q *db.Queries, userID pgtype.UUID, now time.Time) (review.StudyDay, error) {
	row, err := q.GetStudyDayWindow(ctx, db.GetStudyDayWindowParams{
		Now: pgtype.Timestamptz{Time: now, Valid: true}, UserID: userID,
	})
	if err != nil {
		return review.StudyDay{}, err
	}
	return review.StudyDay{Start: row.StudyDayStart.Time, End: row.StudyDayEnd.Time}, nil
}

// buildStudyBatch resolves the study-day window and the caller's effective FSRS params for deck,
// then builds one batch from it -- the shared tail of GET /decks/{id}/review and
// GET /api/reviews/next, so the two can never fetch a batch under different settings.
func buildStudyBatch(ctx context.Context, store db.DBTX, userID pgtype.UUID, deck db.Deck,
	cur review.Cursor, limit int32, clock time.Time) (review.Batch, error) {
	q := db.New(store)
	window, err := studyDayWindow(ctx, q, userID, clock)
	if err != nil {
		return review.Batch{}, err
	}
	params, err := review.EffectiveParams(ctx, q, userID, deck.ID)
	if err != nil {
		return review.Batch{}, err
	}
	batch, err := review.BuildBatch(ctx, store, params, userID, deck.ID, deck.Name, window,
		review.NewPerDay(deck.Preset), review.RevPerDay(deck.Preset),
		review.ParseRevOrder(deck.Preset), review.ParseNewMix(deck.Preset), cur, limit, clock)
	if err != nil {
		return review.Batch{}, err
	}
	return batch, nil
}

// noteTypeCSS sanitises each note type's CSS blob once per page (never per card, #55) so a
// refilled card can never arrive before its styles.
func noteTypeCSS(ctx context.Context, q *db.Queries, userID, deckID pgtype.UUID) ([]template.CSS, error) {
	rows, err := q.ListNoteTypeCSSForDeck(ctx, db.ListNoteTypeCSSForDeckParams{UserID: userID, DeckID: deckID})
	if err != nil {
		return nil, err
	}
	css := make([]template.CSS, len(rows))
	for i, r := range rows {
		sanitised, _ := cardrender.SanitiseCSS(r.Css)
		css[i] = sanitised
	}
	return css, nil
}

func unauthenticatedJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"})
}

// -- Wire parsing [§2.7]: exactly these five fields; nothing else is ever read into an Event. --

type wireEvent struct {
	ID         string `json:"id"`
	CardID     string `json:"cardId"`
	Rating     int    `json:"rating"`
	ReviewedAt string `json:"reviewedAt"`
	DurationMs *int32 `json:"durationMs"`
}

type wireBatch struct {
	Events []wireEvent `json:"events"`
}

// errBadBatch is decodeBatch's single failure mode: every violation is answered identically
// (400, no detail, nothing written), so there is nothing for the error value to carry.
var errBadBatch = errors.New("http: malformed review batch")

// parseBatchRequest decodes and strictly validates the request body. Any failure writes 400 and
// returns ok=false; nothing is ever written to the database for a malformed batch.
func parseBatchRequest(w http.ResponseWriter, r *http.Request) ([]review.Event, bool) {
	events, err := decodeBatch(r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	return events, true
}

// decodeBatch is the validation itself. Decoding into the five-field wireEvent struct -- never
// DisallowUnknownFields, never map[string]any -- is the mechanism that makes extra
// client-supplied fields (stability, due, ...) silently ignored rather than rejected or stored
// (CLAUDE.md §10.1, §2.7).
func decodeBatch(r *http.Request) ([]review.Event, error) {
	var batch wireBatch
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBatchBody)).Decode(&batch); err != nil {
		return nil, errBadBatch
	}
	if len(batch.Events) < 1 || len(batch.Events) > maxEventsPerPost {
		return nil, errBadBatch
	}

	events := make([]review.Event, len(batch.Events))
	for i, we := range batch.Events {
		var id, cardID pgtype.UUID
		if err := id.Scan(we.ID); err != nil {
			return nil, errBadBatch
		}
		if err := cardID.Scan(we.CardID); err != nil {
			return nil, errBadBatch
		}
		rating := fsrs.Rating(we.Rating)
		if !rating.Valid() {
			return nil, errBadBatch
		}
		reviewedAt, err := time.Parse(time.RFC3339Nano, we.ReviewedAt)
		if err != nil {
			return nil, errBadBatch
		}
		if we.DurationMs != nil && (*we.DurationMs < 1 || *we.DurationMs > 3_600_000) {
			return nil, errBadBatch
		}
		events[i] = review.Event{
			ID:         id,
			CardID:     cardID,
			Rating:     rating,
			ReviewedAt: reviewedAt.UTC().Truncate(time.Microsecond),
			DurationMs: we.DurationMs,
		}
	}
	return events, nil
}

// -- View models: template/JSON shapes, kept out of internal/review so that package stays free
// of html/template and encoding concerns. --

type branchView struct {
	Due           string
	State         int
	ScheduledDays int32
}

func toBranchView(o fsrs.Outcome) branchView {
	return branchView{Due: o.Due.UTC().Format(time.RFC3339Nano), State: int(o.State), ScheduledDays: o.ScheduledDays}
}

type cardView struct {
	CardID   string
	Unseen   bool
	Question template.HTML
	Answer   template.HTML
	Again    branchView
	Hard     branchView
	Good     branchView
	Easy     branchView
}

func toCardView(c review.Card) cardView {
	return cardView{
		CardID:   c.CardID.String(),
		Unseen:   c.Unseen,
		Question: c.Question,
		Answer:   c.Answer,
		Again:    toBranchView(c.Preview.Again),
		Hard:     toBranchView(c.Preview.Hard),
		Good:     toBranchView(c.Preview.Good),
		Easy:     toBranchView(c.Preview.Easy),
	}
}

type batchView struct {
	Cards     []cardView
	Cursor    string
	Exhausted bool
}

func toBatchView(b review.Batch) batchView {
	cards := make([]cardView, len(b.Cards))
	for i, c := range b.Cards {
		cards[i] = toCardView(c)
	}
	return batchView{Cards: cards, Cursor: b.Cursor, Exhausted: b.Exhausted}
}

type resultView struct {
	ID     string `json:"id"`
	CardID string `json:"cardId"`
	Status string `json:"status"`
	After  any    `json:"after,omitempty"`
}

func toResultViews(results []review.Result) []resultView {
	views := make([]resultView, len(results))
	for i, r := range results {
		v := resultView{ID: r.ID.String(), CardID: r.CardID.String(), Status: string(r.Status)}
		if r.After != nil {
			v.After = r.After
		}
		views[i] = v
	}
	return views
}
