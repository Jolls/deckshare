package review

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
)

// clampForwardWindow and rejectPastWindow are the reviewedAt believability bounds (resolved
// decision 1, architecture.md §6): a future-timestamped review sorts ahead of everything else in
// a replay and permanently corrupts review_log's ordering guarantee for that card, so the
// tolerance is a check against the server's own clock, not a shape check. 5 minutes covers real
// client clock skew; beyond it the clock is wrong, not skewed. The 30-day floor stops a clock
// stuck at epoch from injecting history at the head of the log.
const (
	clampForwardWindow = 5 * time.Minute
	rejectPastWindow   = 30 * 24 * time.Hour
)

// clampOrReject applies the reviewedAt policy. reject is terminal: the client's sender must not
// retry a rejected event. Every timestamp entering review logic is truncated to microseconds
// first -- Postgres timestamptz is microsecond-resolution, and comparing an untruncated in-memory
// value against a truncated stored one makes the last_review guard non-deterministic.
func clampOrReject(reviewedAt, now time.Time) (adjusted time.Time, reject bool) {
	reviewedAt = reviewedAt.Truncate(time.Microsecond)
	now = now.Truncate(time.Microsecond)
	switch {
	case reviewedAt.After(now.Add(clampForwardWindow)):
		return time.Time{}, true
	case reviewedAt.Before(now.Add(-rejectPastWindow)):
		return time.Time{}, true
	case reviewedAt.After(now):
		return now, false
	default:
		return reviewedAt, false
	}
}

// reviewKind maps the prior FSRS state onto review_log.review_kind (Anki revlog.type: 0 learning,
// 1 review, 2 relearning, 3 cram, 4 manual). Our reviewer never writes 3 or 4. Best-effort given
// docs/anki-schema.md's unverified revlog.type ordering (resolved decision 7): affects
// export/analysis fidelity only, since FSRS scheduling never reads this column.
func reviewKind(s fsrs.State) int16 {
	switch s {
	case fsrs.Review:
		return 1
	case fsrs.Relearning:
		return 2
	default: // New, Learning
		return 0
	}
}

func userCardStateRowToFSRS(row db.UserCardState) fsrs.CardState {
	cs := fsrs.CardState{
		Due:           row.Due.Time,
		Stability:     row.Stability,
		Difficulty:    row.Difficulty,
		State:         fsrs.State(row.State),
		Reps:          row.Reps,
		Lapses:        row.Lapses,
		ScheduledDays: row.ScheduledDays,
		LearningSteps: row.LearningSteps,
	}
	if row.LastReview.Valid {
		cs.LastReview = row.LastReview.Time
	}
	return cs
}

// currentState reads the stored scheduling state for (user, card); nil when the card has no row.
func currentState(ctx context.Context, q *db.Queries, userID, cardID pgtype.UUID) (*fsrs.CardState, error) {
	row, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: userID, CardID: cardID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	cs := userCardStateRowToFSRS(row)
	return &cs, nil
}

func durationMsArg(d *int32) pgtype.Int4 {
	if d == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *d, Valid: true}
}

// appendReviewLogRow appends this event's one review_log row (§2.5, append-only). Every column
// but the id is derived from state the server already holds (§2.7). ok is false when the id was
// already taken -- a pure retry, which must NOT be rescheduled from the row it already advanced.
func appendReviewLogRow(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID,
	ev Event, prior fsrs.CardState, outcome fsrs.Outcome, elapsedDaysBefore int32) (bool, error) {
	n, err := q.InsertReviewLog(ctx, db.InsertReviewLogParams{
		ID:                  ev.ID,
		UserID:              userID,
		CardID:              ev.CardID,
		Rating:              int16(ev.Rating),
		ReviewedAt:          pgtype.Timestamptz{Time: ev.ReviewedAt, Valid: true},
		DurationMs:          durationMsArg(ev.DurationMs),
		StateBefore:         int16(prior.State),
		LearningStepsBefore: prior.LearningSteps,
		StabilityBefore:     pgtype.Float8{Float64: prior.Stability, Valid: true},
		DifficultyBefore:    pgtype.Float8{Float64: prior.Difficulty, Valid: true},
		ElapsedDaysBefore:   elapsedDaysBefore,
		ScheduledDaysAfter:  outcome.ScheduledDays,
		FsrsVersion:         pgtype.Int2{Int16: int16(p.Version()), Valid: true},
		ReviewKind:          reviewKind(prior.State),
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// duplicateOf answers a retry: the id is already in review_log, so nothing further is written and
// the stored state is reported as it stands.
func duplicateOf(ctx context.Context, q *db.Queries, userID, cardID pgtype.UUID) (*fsrs.CardState, Status, error) {
	after, err := currentState(ctx, q, userID, cardID)
	if err != nil {
		return nil, "", err
	}
	return after, StatusDuplicate, nil
}

// beforeInLogOrder reports whether (aAt, aID) sorts before (bAt, bID) under review_log's own
// ordering -- ORDER BY reviewed_at, id, which is what ListReviewLogForCard returns and what
// replayStates requires of its input. One definition, so the in-batch sort and insertSorted's
// binary search cannot disagree about it.
func beforeInLogOrder(aAt time.Time, aID pgtype.UUID, bAt time.Time, bID pgtype.UUID) bool {
	if !aAt.Equal(bAt) {
		return aAt.Before(bAt)
	}
	return bytes.Compare(aID.Bytes[:], bID.Bytes[:]) < 0
}

// insertSorted inserts ev into rows (already sorted by (ReviewedAt, ID)) at its correct position,
// tie-broken on ID bytes to match ORDER BY reviewed_at, id in ListReviewLogForCard. Returns the
// merged slice and ev's index in it.
func insertSorted(rows []LoggedReview, ev LoggedReview) ([]LoggedReview, int) {
	idx := sort.Search(len(rows), func(i int) bool {
		return beforeInLogOrder(ev.ReviewedAt, ev.ID, rows[i].ReviewedAt, rows[i].ID)
	})
	merged := make([]LoggedReview, len(rows)+1)
	copy(merged, rows[:idx])
	merged[idx] = ev
	copy(merged[idx+1:], rows[idx:])
	return merged, idx
}

// orderedResults re-projects results (keyed by event id) back into the batch's original request
// order (architecture.md §6's response contract, plan §0.7: "in request order, not processing
// order").
func orderedResults(order []pgtype.UUID, results map[pgtype.UUID]Result) []Result {
	out := make([]Result, len(order))
	for i, id := range order {
		out[i] = results[id]
	}
	return out
}

// resultFor assembles one event's Result: the stored state as a DTO, plus the four branches that
// state produces at now. The preview is what lets the client relabel a card its learning-steps
// heuristic requeued (#142) -- the branches shipped with the card at batch-fetch time describe its
// pre-grade state. now, not ev.ReviewedAt: the labels describe intervals from the moment the user
// is looking at them, which is the same convention batch-fetch's preview uses.
func resultFor(p fsrs.Params, ev Event, status Status, after *fsrs.CardState, now time.Time) Result {
	res := Result{ID: ev.ID, CardID: ev.CardID, Status: status}
	if after == nil {
		return res
	}
	dto := cardStateToDTO(*after)
	res.After = &dto

	preview, err := fsrs.PreviewAll(p, *after, now)
	if err != nil {
		// Deliberately not fatal to the batch. A preview is an interval label; the grades already
		// applied in this transaction are review_log rows, which are unrecoverable if rolled back
		// (§2.5). The client simply keeps the branches it already has.
		return res
	}
	res.Preview = &preview
	return res
}

// GradeBatch is the whole of POST /api/reviews/batch's server side (architecture.md §6), except
// the wire-shape validation that must happen before an Event can even exist (internal/http's
// job). It must run inside a transaction it does not own -- the advisory locks it takes are held
// to that transaction's commit, which is what makes a concurrent grade of the same card wait
// rather than read a stale `before`. The caller commits.
//
// The four concurrency mechanisms, all here: locks acquired in sorted key order (deadlock
// avoidance), events applied in reviewed_at order, events already in review_log skipped, and a
// card whose stored last_review postdates the event replayed from review_log instead of
// scheduled forward.
func GradeBatch(ctx context.Context, tx pgx.Tx, userID pgtype.UUID, now time.Time, evs []Event) ([]Result, error) {
	q := db.New(tx)
	now = now.Truncate(time.Microsecond)

	// order/results are keyed by event id, not (event id, card id): GradeBatch dedupes cardIDs
	// below but not event ids, so two events sharing an id but naming different cards collide
	// here and in InsertReviewLog's PK. Reachable only by a client reusing its own event id
	// (impractical by accident given UUIDv7) -- self-harming only, review_log/§2.7 stay intact.
	// The second such event is answered `duplicate` with its review silently dropped; deliberately
	// left as behavior, not a bug, per the #126 audit.
	order := make([]pgtype.UUID, len(evs))
	results := make(map[pgtype.UUID]Result, len(evs))
	live := make([]Event, 0, len(evs))
	for i, ev := range evs {
		order[i] = ev.ID
		adjusted, reject := clampOrReject(ev.ReviewedAt, now)
		if reject {
			results[ev.ID] = Result{ID: ev.ID, CardID: ev.CardID, Status: StatusRejected}
			continue
		}
		ev.ReviewedAt = adjusted
		live = append(live, ev)
	}
	if len(live) == 0 {
		return orderedResults(order, results), nil
	}

	// Authorise: a card missing from ListStudyableCards is absent, invisible, or not studyable --
	// deliberately indistinguishable (docs/schema.md).
	cardIDSeen := make(map[pgtype.UUID]bool, len(live))
	cardIDs := make([]pgtype.UUID, 0, len(live))
	for _, ev := range live {
		if !cardIDSeen[ev.CardID] {
			cardIDSeen[ev.CardID] = true
			cardIDs = append(cardIDs, ev.CardID)
		}
	}
	studyable, err := q.ListStudyableCards(ctx, db.ListStudyableCardsParams{UserID: userID, CardIds: cardIDs})
	if err != nil {
		return nil, err
	}
	deckOf := make(map[pgtype.UUID]pgtype.UUID, len(studyable))
	for _, s := range studyable {
		deckOf[s.CardID] = s.DeckID
	}

	toGrade := make([]Event, 0, len(live))
	for _, ev := range live {
		if _, ok := deckOf[ev.CardID]; !ok {
			results[ev.ID] = Result{ID: ev.ID, CardID: ev.CardID, Status: StatusForbidden}
			continue
		}
		toGrade = append(toGrade, ev)
	}
	if len(toGrade) == 0 {
		return orderedResults(order, results), nil
	}

	// Locks, sorted ascending key order -- the deadlock-avoidance rule (architecture.md §6, plan §0.3).
	if err := acquireLocks(ctx, q, lockKeys(userID, toGrade)); err != nil {
		return nil, err
	}

	// Idempotency: skip events already in review_log. NOT scoped to user_id -- review_log.id is a
	// global PK.
	evIDs := make([]pgtype.UUID, len(toGrade))
	for i, ev := range toGrade {
		evIDs[i] = ev.ID
	}
	existing, err := q.ListExistingReviewLogIDs(ctx, evIDs)
	if err != nil {
		return nil, err
	}
	dup := make(map[pgtype.UUID]bool, len(existing))
	for _, id := range existing {
		dup[id] = true
	}

	paramsCache := NewParamsCache()
	fresh := make([]Event, 0, len(toGrade))
	for _, ev := range toGrade {
		if !dup[ev.ID] {
			fresh = append(fresh, ev)
			continue
		}
		p, err := paramsCache.Get(ctx, q, userID, deckOf[ev.CardID])
		if err != nil {
			return nil, err
		}
		after, status, err := duplicateOf(ctx, q, userID, ev.CardID)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = resultFor(p, ev, status, after, now)
	}
	if len(fresh) == 0 {
		return orderedResults(order, results), nil
	}

	// Apply in reviewed_at order so two grades of the same card in one batch converge correctly.
	sort.Slice(fresh, func(i, j int) bool {
		return beforeInLogOrder(fresh[i].ReviewedAt, fresh[i].ID, fresh[j].ReviewedAt, fresh[j].ID)
	})

	for _, ev := range fresh {
		p, err := paramsCache.Get(ctx, q, userID, deckOf[ev.CardID])
		if err != nil {
			return nil, err
		}

		after, status, err := gradeEvent(ctx, q, p, userID, ev)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = resultFor(p, ev, status, after, now)
	}

	return orderedResults(order, results), nil
}

// gradeEvent grades one event, under its lock already held. See plan §0.5 for the exact
// in-order/out-of-order sequence this implements.
func gradeEvent(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID, ev Event) (*fsrs.CardState, Status, error) {
	row, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: userID, CardID: ev.CardID})
	var before fsrs.CardState
	switch {
	case err == nil:
		before = userCardStateRowToFSRS(row)
	case errors.Is(err, pgx.ErrNoRows):
		// zero value: New, all zero -- go-fsrs's own zero value for a never-seen card.
	default:
		return nil, "", err
	}

	if before.LastReview.IsZero() || !ev.ReviewedAt.Before(before.LastReview) {
		return gradeInOrder(ctx, q, p, userID, ev, before)
	}
	return gradeOutOfOrder(ctx, q, p, userID, ev)
}

func gradeInOrder(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID, ev Event, before fsrs.CardState) (*fsrs.CardState, Status, error) {
	elapsedDaysBefore := fsrs.ElapsedDays(before, ev.ReviewedAt)
	outcome, err := fsrs.Schedule(p, before, ev.Rating, ev.ReviewedAt)
	if err != nil {
		return nil, "", err
	}

	inserted, err := appendReviewLogRow(ctx, q, p, userID, ev, before, outcome, elapsedDaysBefore)
	if err != nil {
		return nil, "", err
	}
	if !inserted {
		// The id was already taken. The (user, card) advisory lock serialises two *batches* for
		// the same card, so a competing batch cannot land here -- what does is one client reusing
		// an id, twice in this batch or across two cards (whose lock keys differ). review_log.id
		// is a global PK, so either way the second insert conflicts. Pure retry: write nothing
		// else, and do NOT reschedule from the row the first insert already advanced.
		return duplicateOf(ctx, q, userID, ev.CardID)
	}

	if _, err := q.UpsertUserCardStateOnReview(ctx, db.UpsertUserCardStateOnReviewParams{
		UserID:        userID,
		CardID:        ev.CardID,
		Due:           pgtype.Timestamptz{Time: outcome.Due, Valid: true},
		Stability:     outcome.Stability,
		Difficulty:    outcome.Difficulty,
		State:         int16(outcome.State),
		Reps:          outcome.Reps,
		Lapses:        outcome.Lapses,
		ElapsedDays:   elapsedDaysBefore,
		ScheduledDays: outcome.ScheduledDays,
		LearningSteps: outcome.LearningSteps,
		LastReview:    pgtype.Timestamptz{Time: ev.ReviewedAt, Valid: true},
	}); err != nil {
		return nil, "", err
	}

	// Re-read rather than trust outcome directly: the guarded upsert's WHERE clause is the
	// authority on what actually landed. Its guard is strict (last_review < EXCLUDED), and
	// gradeEvent routes an event whose reviewedAt EQUALS the stored last_review here rather than
	// to the out-of-order branch -- so a second grade at the same microsecond appends its
	// review_log row and leaves user_card_state untouched. That is deliberate: review_log stays
	// the complete truth (§2.5) and a replay reconciles. The DTO returned is the stored state,
	// not `outcome`.
	after, err := currentState(ctx, q, userID, ev.CardID)
	if err != nil {
		return nil, "", err
	}
	return after, StatusApplied, nil
}

func gradeOutOfOrder(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID, ev Event) (*fsrs.CardState, Status, error) {
	existingRows, err := q.ListReviewLogForCard(ctx, db.ListReviewLogForCardParams{UserID: userID, CardID: ev.CardID})
	if err != nil {
		return nil, "", err
	}
	merged, evIdx := insertSorted(loggedReviews(existingRows), LoggedReview{ID: ev.ID, Rating: ev.Rating, ReviewedAt: ev.ReviewedAt})

	priors, final, err := replayStates(p, merged)
	if err != nil {
		return nil, "", err
	}
	priorForEv := priors[evIdx]
	elapsedDaysBefore := fsrs.ElapsedDays(priorForEv, ev.ReviewedAt)
	outcomeForEv, err := fsrs.Schedule(p, priorForEv, ev.Rating, ev.ReviewedAt)
	if err != nil {
		return nil, "", err
	}

	inserted, err := appendReviewLogRow(ctx, q, p, userID, ev, priorForEv, outcomeForEv, elapsedDaysBefore)
	if err != nil {
		return nil, "", err
	}
	if !inserted {
		return duplicateOf(ctx, q, userID, ev.CardID)
	}

	lastReviewedAt := merged[len(merged)-1].ReviewedAt
	if err := writeReplayedState(ctx, q, userID, ev.CardID, priors[len(priors)-1], final, lastReviewedAt); err != nil {
		return nil, "", err
	}

	return &final, StatusApplied, nil
}
