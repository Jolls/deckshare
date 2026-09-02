package http

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

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

	// maxExtraRounds bounds the client-controlled extraRounds query param (#172): the
	// boundary-validation clamp that keeps newPerDay*(1+extraRounds) well clear of int32
	// overflow and caps how much a manually-crafted query string can inflate the effective
	// daily allowance.
	maxExtraRounds int32 = 20
)

// registerReviewRoutes wires the reviewer's three routes (architecture.md §6, docs/routes.md).
// now is injected so tests can pin the clock; production passes time.Now.
func registerReviewRoutes(mux *http.ServeMux, store db.Beginner, pages, fragments map[string]*template.Template, now func() time.Time) {
	mux.Handle("GET /decks/{id}/review", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForStudy(r.Context(), db.GetDeckForStudyParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		clock := now()
		batch, err := buildStudyBatch(r.Context(), store, user.ID, deck, review.Cursor{AtStart: true}, initialBatchSize, clock, 0)
		if err != nil {
			serverError(w)
			return
		}
		css, err := noteTypeCSS(r.Context(), q, user.ID, deckID, nil)
		if err != nil {
			serverError(w)
			return
		}

		render(w, pages["review"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "CSS": css, "Batch": toBatchView(batch),
			"BodyClass": "hide-account-bar",
		})
	})))

	// GET /study (#169): a one-shot mix across every deck the user can study. Each deck's own
	// newPerDay/revPerDay still bounds what BuildBatch draws from it (review.RevPerDay is passed
	// separately, inside buildStudyBatchInWindow) -- initialBatchSize here is only the page-size
	// limit, kept distinct from that budget so a deck whose budget is exhausted can still surface
	// its learning/relearning cards, which BuildBatch's effectiveLimit deliberately never caps
	// (see its doc comment). Deliberately not paginated: the merge point is *after* per-deck Card
	// rendering, since FSRS params (§2.3) and media resolution are per-deck -- extending this to
	// refill would need a composite cursor carrying each deck's own sub-cursor, which #169 doesn't
	// ask for.
	mux.Handle("GET /study", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		q := db.New(store)
		decks, err := q.ListStudyableDecksForUser(r.Context(), user.ID)
		if err != nil {
			serverError(w)
			return
		}

		clock := now()
		window, err := studyDayWindow(r.Context(), q, user.ID, clock)
		if err != nil {
			serverError(w)
			return
		}
		var cards []review.Card
		var css []template.CSS
		seenNoteType := make(map[pgtype.UUID]bool)
		for _, deck := range decks {
			batch, err := buildStudyBatchInWindow(r.Context(), store, user.ID, deck, window, review.Cursor{AtStart: true}, initialBatchSize, clock, 0)
			if err != nil {
				serverError(w)
				return
			}
			cards = append(cards, batch.Cards...)

			deckCSS, err := noteTypeCSS(r.Context(), q, user.ID, deck.ID, seenNoteType)
			if err != nil {
				serverError(w)
				return
			}
			css = append(css, deckCSS...)
		}
		shuffleCards(cards)

		render(w, pages["study"], http.StatusOK, map[string]any{
			"User": user, "CSS": css,
			"Batch":     toBatchView(review.Batch{Cards: cards, Exhausted: true}),
			"BodyClass": "hide-account-bar",
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
			badRequest(w)
			return
		}
		cur, err := review.DecodeCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			badRequest(w)
			return
		}
		extraRounds := parseExtraRounds(r.URL.Query().Get("extraRounds"))

		q := db.New(store)
		deck, err := q.GetDeckForStudy(r.Context(), db.GetDeckForStudyParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErr(w, err) {
			return
		}
		clock := now()
		batch, err := buildStudyBatch(r.Context(), store, user.ID, deck, cur, refillBatchSize, clock, extraRounds)
		if err != nil {
			serverError(w)
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
		// #178: the session cookie is browser-wide, so a tab can outlive the account it was
		// opened under. ?u= is the client asserting which account it *believes* it is grading
		// as; a mismatch refuses the write and never shapes one (§2.7).
		if !actingUserMatches(r, user.ID) {
			sessionChangedJSON(w)
			return
		}
		events, ok := parseBatchRequest(w, r)
		if !ok {
			return
		}

		tx, ok := startTx(r.Context(), w, store)
		if !ok {
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		results, err := review.GradeBatch(r.Context(), tx, user.ID, now(), events)
		if err != nil {
			serverError(w)
			return
		}
		if !commitTx(r.Context(), w, tx) {
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

// parseExtraRounds validates the client-controlled extraRounds query param (#172) at the
// boundary: empty, unparseable, or negative is treated as 0 -- no 400, matching how an empty
// cursor is treated as "start" -- and any valid value is clamped to maxExtraRounds.
func parseExtraRounds(raw string) int32 {
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	if int32(n) > maxExtraRounds {
		return maxExtraRounds
	}
	return int32(n)
}

// buildStudyBatch resolves the study-day window and the caller's effective FSRS params for deck,
// then builds one batch from it -- the shared tail of GET /decks/{id}/review and
// GET /api/reviews/next, so the two can never fetch a batch under different settings.
func buildStudyBatch(ctx context.Context, store db.DBTX, userID pgtype.UUID, deck db.Deck,
	cur review.Cursor, limit int32, clock time.Time, extraRounds int32) (review.Batch, error) {
	window, err := studyDayWindow(ctx, db.New(store), userID, clock)
	if err != nil {
		return review.Batch{}, err
	}
	return buildStudyBatchInWindow(ctx, store, userID, deck, window, cur, limit, clock, extraRounds)
}

// buildStudyBatchInWindow is buildStudyBatch given an already-resolved study-day window. GET
// /study's per-deck loop (#169) calls this directly instead of buildStudyBatch: the window
// depends only on the user, not the deck, so resolving it once per request rather than once per
// contributing deck saves a redundant GetStudyDayWindow round trip per deck.
func buildStudyBatchInWindow(ctx context.Context, store db.DBTX, userID pgtype.UUID, deck db.Deck,
	window review.StudyDay, cur review.Cursor, limit int32, clock time.Time, extraRounds int32) (review.Batch, error) {
	q := db.New(store)
	params, err := review.EffectiveParams(ctx, q, userID, deck.ID)
	if err != nil {
		return review.Batch{}, err
	}
	batch, err := review.BuildBatch(ctx, store, params, userID, deck.ID, deck.Name, window,
		review.NewPerDay(deck.Preset), review.RevPerDay(deck.Preset),
		review.ParseRevOrder(deck.Preset), review.ParsePriority(deck.Preset), cur, limit, clock,
		review.DueLookAheadMinutes(deck.Preset), extraRounds)
	if err != nil {
		return review.Batch{}, err
	}
	return batch, nil
}

// shuffleCards randomises a mixed session's cross-deck ordering in place (#169). Not the FSRS
// rev.order='random' seed (hashSeedFor, internal/review/batch.go) -- that shuffles within one
// deck's own queue and stays stable across a study day; this just interleaves already-selected
// decks' slices for one page view, so an unseeded PRNG is fine.
func shuffleCards(cards []review.Card) {
	rand.Shuffle(len(cards), func(i, j int) { cards[i], cards[j] = cards[j], cards[i] })
}

// noteTypeCSS sanitises each note type's CSS blob once per page (never per card, #55) so a
// refilled card can never arrive before its styles. seen, if non-nil, is a cross-call dedup set
// keyed by note-type id -- GET /study (#169) shares one across its whole per-deck loop so a note
// type used by several of the user's decks is still only sanitised and emitted once.
func noteTypeCSS(ctx context.Context, q *db.Queries, userID, deckID pgtype.UUID, seen map[pgtype.UUID]bool) ([]template.CSS, error) {
	rows, err := q.ListNoteTypeCSSForDeck(ctx, db.ListNoteTypeCSSForDeckParams{UserID: userID, DeckID: deckID})
	if err != nil {
		return nil, err
	}
	css := make([]template.CSS, 0, len(rows))
	for _, r := range rows {
		if seen != nil {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
		}
		sanitised, _ := cardrender.SanitiseCSS(r.Css)
		css = append(css, sanitised)
	}
	return css, nil
}

func unauthenticatedJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"})
}

// sessionChangedJSON answers a batch whose ?u= names an account other than the session's (#178).
// The session cookie is browser-wide, so the acting account can change under a tab still holding
// graded events; without this, a deck shared with the new account would take those grades into the
// wrong user's user_card_state and review_log -- silent, unrecoverable (CLAUDE.md §2.5). 409 rather
// than 403: nothing about the request is unauthorised, it is stale, and review.js must not retry it.
func sessionChangedJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "session_changed"})
}

// actingUserMatches reports whether the optional ?u= query parameter names the session user.
// Empty/absent is tolerated by design so the server side can land ahead of the client side (#178,
// docs/plans/175-multi-user-session-switching.md §11 issue B step 1). An unparseable value is a
// mismatch, not a 400: this is a rejection-only staleness check and one failure mode is enough.
func actingUserMatches(r *http.Request, userID pgtype.UUID) bool {
	raw := r.URL.Query().Get("u")
	if raw == "" {
		return true
	}
	var claimed pgtype.UUID
	if err := claimed.Scan(raw); err != nil {
		return false
	}
	return claimed.Valid && claimed.Bytes == userID.Bytes
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
		badRequest(w)
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
	Due           string `json:"due"`
	State         int    `json:"state"`
	ScheduledDays int32  `json:"scheduledDays"`
}

func toBranchView(o fsrs.Outcome) branchView {
	return branchView{Due: o.Due.UTC().Format(time.RFC3339Nano), State: int(o.State), ScheduledDays: o.ScheduledDays}
}

// previewView is fsrs.Preview on the wire -- the same three fields per branch that the hidden
// card node carries as data-* attributes, so the client parses one branch shape, not two.
type previewView struct {
	Again branchView `json:"again"`
	Hard  branchView `json:"hard"`
	Good  branchView `json:"good"`
	Easy  branchView `json:"easy"`
}

func toPreviewView(p fsrs.Preview) previewView {
	return previewView{
		Again: toBranchView(p.Again),
		Hard:  toBranchView(p.Hard),
		Good:  toBranchView(p.Good),
		Easy:  toBranchView(p.Easy),
	}
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
	ID      string       `json:"id"`
	CardID  string       `json:"cardId"`
	Status  string       `json:"status"`
	After   any          `json:"after,omitempty"`
	Preview *previewView `json:"preview,omitempty"`
}

func toResultViews(results []review.Result) []resultView {
	views := make([]resultView, len(results))
	for i, r := range results {
		v := resultView{ID: r.ID.String(), CardID: r.CardID.String(), Status: string(r.Status)}
		if r.After != nil {
			v.After = r.After
		}
		if r.Preview != nil {
			pv := toPreviewView(*r.Preview)
			v.Preview = &pv
		}
		views[i] = v
	}
	return views
}
