package review

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
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

func currentStateDTO(ctx context.Context, q *db.Queries, userID, cardID pgtype.UUID) (*CardStateDTO, error) {
	row, err := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: userID, CardID: cardID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	dto := cardStateToDTO(userCardStateRowToFSRS(row))
	return &dto, nil
}

func durationMsArg(d *int32) pgtype.Int4 {
	if d == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *d, Valid: true}
}

// insertSorted inserts ev into rows (already sorted by (ReviewedAt, ID)) at its correct position,
// tie-broken on ID bytes to match ORDER BY reviewed_at, id in ListReviewLogForCard. Returns the
// merged slice and ev's index in it.
func insertSorted(rows []LoggedReview, ev LoggedReview) ([]LoggedReview, int) {
	idx := sort.Search(len(rows), func(i int) bool {
		if !rows[i].ReviewedAt.Equal(ev.ReviewedAt) {
			return rows[i].ReviewedAt.After(ev.ReviewedAt)
		}
		return bytes.Compare(rows[i].ID.Bytes[:], ev.ID.Bytes[:]) > 0
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

	fresh := make([]Event, 0, len(toGrade))
	for _, ev := range toGrade {
		if !dup[ev.ID] {
			fresh = append(fresh, ev)
			continue
		}
		dto, err := currentStateDTO(ctx, q, userID, ev.CardID)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = Result{ID: ev.ID, CardID: ev.CardID, Status: StatusDuplicate, After: dto}
	}
	if len(fresh) == 0 {
		return orderedResults(order, results), nil
	}

	// Apply in reviewed_at order so two grades of the same card in one batch converge correctly.
	sort.Slice(fresh, func(i, j int) bool {
		if !fresh[i].ReviewedAt.Equal(fresh[j].ReviewedAt) {
			return fresh[i].ReviewedAt.Before(fresh[j].ReviewedAt)
		}
		return bytes.Compare(fresh[i].ID.Bytes[:], fresh[j].ID.Bytes[:]) < 0
	})

	paramsCache := make(map[pgtype.UUID]fsrs.Params, len(deckOf))
	for _, ev := range fresh {
		deckID := deckOf[ev.CardID]
		p, ok := paramsCache[deckID]
		if !ok {
			p, err = EffectiveParams(ctx, q, userID, deckID)
			if err != nil {
				return nil, err
			}
			paramsCache[deckID] = p
		}

		after, status, err := gradeEvent(ctx, q, p, userID, ev)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = Result{ID: ev.ID, CardID: ev.CardID, Status: status, After: after}
	}

	return orderedResults(order, results), nil
}

// gradeEvent grades one event, under its lock already held. See plan §0.5 for the exact
// in-order/out-of-order sequence this implements.
func gradeEvent(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID, ev Event) (*CardStateDTO, Status, error) {
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

func gradeInOrder(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID, ev Event, before fsrs.CardState) (*CardStateDTO, Status, error) {
	elapsedDaysBefore := fsrs.ElapsedDays(before, ev.ReviewedAt)
	outcome, err := fsrs.Schedule(p, before, ev.Rating, ev.ReviewedAt)
	if err != nil {
		return nil, "", err
	}

	inserted, err := q.InsertReviewLog(ctx, db.InsertReviewLogParams{
		ID:                  ev.ID,
		UserID:              userID,
		CardID:              ev.CardID,
		Rating:              int16(ev.Rating),
		ReviewedAt:          pgtype.Timestamptz{Time: ev.ReviewedAt, Valid: true},
		DurationMs:          durationMsArg(ev.DurationMs),
		StateBefore:         int16(before.State),
		LearningStepsBefore: before.LearningSteps,
		StabilityBefore:     pgtype.Float8{Float64: before.Stability, Valid: true},
		DifficultyBefore:    pgtype.Float8{Float64: before.Difficulty, Valid: true},
		ElapsedDaysBefore:   elapsedDaysBefore,
		ScheduledDaysAfter:  outcome.ScheduledDays,
		FsrsVersion:         pgtype.Int2{Int16: int16(p.Version()), Valid: true},
		ReviewKind:          reviewKind(before.State),
	})
	if err != nil {
		return nil, "", err
	}
	if inserted == 0 {
		// A concurrent inserter won the race for this id; this is a pure retry -- write nothing
		// else, must NOT be rescheduled from the row it already advanced.
		dto, err := currentStateDTO(ctx, q, userID, ev.CardID)
		if err != nil {
			return nil, "", err
		}
		return dto, StatusDuplicate, nil
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
	// authority on what actually landed if a newer last_review is somehow already stored.
	dto, err := currentStateDTO(ctx, q, userID, ev.CardID)
	if err != nil {
		return nil, "", err
	}
	return dto, StatusApplied, nil
}

func gradeOutOfOrder(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID, ev Event) (*CardStateDTO, Status, error) {
	existingRows, err := q.ListReviewLogForCard(ctx, db.ListReviewLogForCardParams{UserID: userID, CardID: ev.CardID})
	if err != nil {
		return nil, "", err
	}
	logged := make([]LoggedReview, len(existingRows))
	for i, r := range existingRows {
		logged[i] = LoggedReview{ID: r.ID, Rating: fsrs.Rating(r.Rating), ReviewedAt: r.ReviewedAt.Time.UTC()}
	}
	merged, evIdx := insertSorted(logged, LoggedReview{ID: ev.ID, Rating: ev.Rating, ReviewedAt: ev.ReviewedAt})

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

	inserted, err := q.InsertReviewLog(ctx, db.InsertReviewLogParams{
		ID:                  ev.ID,
		UserID:              userID,
		CardID:              ev.CardID,
		Rating:              int16(ev.Rating),
		ReviewedAt:          pgtype.Timestamptz{Time: ev.ReviewedAt, Valid: true},
		DurationMs:          durationMsArg(ev.DurationMs),
		StateBefore:         int16(priorForEv.State),
		LearningStepsBefore: priorForEv.LearningSteps,
		StabilityBefore:     pgtype.Float8{Float64: priorForEv.Stability, Valid: true},
		DifficultyBefore:    pgtype.Float8{Float64: priorForEv.Difficulty, Valid: true},
		ElapsedDaysBefore:   elapsedDaysBefore,
		ScheduledDaysAfter:  outcomeForEv.ScheduledDays,
		FsrsVersion:         pgtype.Int2{Int16: int16(p.Version()), Valid: true},
		ReviewKind:          reviewKind(priorForEv.State),
	})
	if err != nil {
		return nil, "", err
	}
	if inserted == 0 {
		dto, err := currentStateDTO(ctx, q, userID, ev.CardID)
		if err != nil {
			return nil, "", err
		}
		return dto, StatusDuplicate, nil
	}

	lastReviewedAt := merged[len(merged)-1].ReviewedAt
	elapsedDaysFinal := fsrs.ElapsedDays(priors[len(priors)-1], lastReviewedAt)

	if err := q.UpsertUserCardStateFromReplay(ctx, db.UpsertUserCardStateFromReplayParams{
		UserID:        userID,
		CardID:        ev.CardID,
		Due:           pgtype.Timestamptz{Time: final.Due, Valid: true},
		Stability:     final.Stability,
		Difficulty:    final.Difficulty,
		State:         int16(final.State),
		Reps:          final.Reps,
		Lapses:        final.Lapses,
		ElapsedDays:   elapsedDaysFinal,
		ScheduledDays: final.ScheduledDays,
		LearningSteps: final.LearningSteps,
		LastReview:    pgtype.Timestamptz{Time: lastReviewedAt, Valid: true},
	}); err != nil {
		return nil, "", err
	}

	dto := cardStateToDTO(final)
	return &dto, StatusApplied, nil
}
