# Issue #126 — Audit: review loop / scheduling write path

Audit zone: `internal/review/` (`batch.go`, `grade.go`, `interleave.go`, `lock.go`,
`newlimit.go`, `order.go`, `replay.go`, `types.go`, `params.go`, `doc.go`),
`internal/http/review.go`, `web/static/review.js`. ~3.3k LOC.
Simplification/cleanup only — **no behavior change intended**.

Every edit below is a literal find/replace or a `git mv`; none require a design decision.
Anything that did is in **Open questions**, not decided here.

Two things this plan does not do, per the issue: nothing removes or bypasses the server-side
recompute path (`ReplayCard`/`replayStates`, CLAUDE.md §17), and nothing lets a client-supplied
value reach a precomputed FSRS field (§2.7). The wire struct in `internal/http/review.go`
(`wireEvent`, 5 fields) is unchanged by every edit here.

---

## Audit result 1 — the four concurrency mechanisms

Traced end to end. **All four are necessary and none is redundant with another.**

| Mechanism | Where | Verdict |
|---|---|---|
| Advisory lock per `(user, card)`, held to commit | `lock.go` (`lockKey`/`sortedKeys`/`acquireLocks`/`LockCards`), `LockCardForGrade` = `pg_advisory_xact_lock`, taken at `grade.go:189` | Necessary. Advisory, not `FOR UPDATE`, because a never-seen card has no row — documented in both `lock.go:13-19` and the SQL. Sorted-key acquisition documented at `lock.go:27-30` including the hash-collision-makes-a-cycle-constructible reason. Also reused by `internal/apkg/dbwrite.go:91`. |
| `reviewed_at` ordering | `grade.go:225-230` (sort `fresh`), `ListReviewLogForCard`'s `ORDER BY reviewed_at, id`, `UpsertUserCardStateOnReview`'s `last_review <` guard | Necessary and distinct from the lock: the lock serialises *concurrent* batches, this orders events *within* one. |
| `review_log.id` idempotency | `ListExistingReviewLogIDs` pre-check (`grade.go:199`, deliberately not user-scoped) + `InsertReviewLog … ON CONFLICT DO NOTHING :execrows` (`grade.go:300`, `grade.go:377`) | Both halves necessary — see finding **C2** for why the `ON CONFLICT` backstop is *not* dead under the lock. |
| Out-of-order replay | `gradeEvent`'s branch (`grade.go:268`) → `gradeOutOfOrder` → `insertSorted` + `replayStates` | Necessary. Fabricating a `*_before` writes permanently wrong training data (§2.5). |

`doc.go` names all four in one paragraph and `GradeBatch`'s doc comment repeats them at the call
site. That is the right amount — **no additional overview comment is recommended** (CLAUDE.md §9:
review logic doesn't earn comments the way `apkg` does).

Three places where the *interaction* is genuinely under-documented or documented wrongly:

- **C1 — `internal/db/queries/user_card_state.sql`'s `UpsertUserCardStateFromReplay` comment is
  false.** It says "Only `internal/review.ReplayCard` may call this." `gradeOutOfOrder`
  (`grade.go:388`) calls it too. Fixed in edit **6**.
- **C2 — `grade.go:301-302`'s "A concurrent inserter won the race for this id" is misleading.**
  Under the `(user, card)` advisory lock a *competing batch* for the same card cannot reach that
  line: the lock is taken (`grade.go:189`) before `ListExistingReviewLogIDs`, and under READ
  COMMITTED each subsequent statement takes a fresh snapshot, so anything the competitor committed
  while we waited is already visible to the pre-check. The path is reachable only when one client
  reuses an id — twice in one batch, or across two cards (different lock keys). That is exactly
  why the backstop is not dead code, and it is not obvious. Fixed in edit **7**.
- **C3 — equal `reviewed_at` logs but does not advance state.** `gradeEvent:268` routes
  `ev.ReviewedAt == before.LastReview` to `gradeInOrder` (the condition is `!Before`), and
  `UpsertUserCardStateOnReview`'s guard is strictly `last_review < EXCLUDED.last_review`. So a
  second grade at the same microsecond appends its `review_log` row, leaves `user_card_state`
  untouched, and returns `applied` with the *stored* state. That is correct — `review_log` stays
  the complete truth (§2.5) and a replay reconciles — but it is invisible from the code. The
  existing comment at `grade.go:327-328` says "if a newer `last_review` is somehow already
  stored", which under-states it. Fixed in edit **8**.

---

## Audit result 2 — `web/static/review.js` vs invariant §2.6

**Read end to end. No scheduling logic has crept client-side. §2.6 holds.**

- Zero FSRS arithmetic. Grepping the file for `stability`, `difficulty`, `retriev`, `elapsed`,
  `reps`, `lapse`, `Math.pow/exp/log`, `**` returns only `formatInterval`'s display rounding
  (`review.js:149-155`) over the server's own `scheduledDays`.
- `grade()` (`review.js:196-220`) is a pure lookup: `card.branches[rating]`, where `branches` was
  built in `cardToSlot`/`branch` (`review.js:84-106`) by reading the `data-*-due` /
  `data-*-state` / `data-*-scheduled-days` attributes the server rendered. No `await` between the
  keypress and `showNext()`.
- No FSRS value is ever put on the wire back: the event built at `review.js:206-212` is exactly
  `{id, cardId, rating, reviewedAt, durationMs}` — §2.7's five fields, nothing else.
- `maybeRequeue` (`review.js:225-250`) is architecture.md §6's explicitly sanctioned local
  learning-steps heuristic ("cosmetic, not authoritative"). It branches on the *precomputed*
  `branch.state ∈ {1,3}` and `branch.due`, computes nothing, and writes nothing.

Two precise findings, documented only — a fix for either would change behavior and needs its own
issue (both listed under Open questions):

- **J1 — `review.js:234`**: `Date.parse(c.branches[3].due)` uses *other, not-yet-graded* cards'
  Good-branch due as a queue-position proxy for `cardsDueBefore`. Still a lookup of a
  server-computed value, still purely in-session, but the value is counterfactual (those cards
  have not been graded). This is the closest thing in the file to client-side ordering policy;
  it stays inside §6's cosmetic carve-out.
- **J2 — `review.js:247-249`**: the requeued slot reuses `card.branches` verbatim, so the
  `data-interval-for` labels shown on a card's second appearance in a session are the ones
  precomputed for its pre-first-grade state.

---

## Safe mechanical edits

### 1. `git mv internal/review/newlimit.go internal/review/preset.go`

and `git mv internal/review/newlimit_test.go internal/review/preset_test.go`.

Rationale, not a preference: the file has held `RevPerDay`/`RevRemaining` since #115 and the
shared `deckPreset`/`parseDeckPreset` since #116 — `order.go`'s `ParseRevOrder`/`ParseNewMix`
both call `parseDeckPreset`, which is defined here. The name says "new limit" and the file's own
doc comment (`newlimit.go:18-19`) already says "`deckPreset` is the whole of `decks.preset`".
Contents unchanged apart from edit 2. No import, no identifier, and no test name changes.

### 2. `internal/review/preset.go` (was `newlimit.go`) — `NewPerDay`/`RevPerDay` use `parseDeckPreset`

Lines 43-46, old:

```go
func NewPerDay(preset []byte) int32 {
	var p deckPreset
	if err := json.Unmarshal(preset, &p); err != nil || p.New == nil || p.New.PerDay == nil {
		return DefaultNewPerDay
	}
```

new:

```go
func NewPerDay(preset []byte) int32 {
	p, ok := parseDeckPreset(preset)
	if !ok || p.New == nil || p.New.PerDay == nil {
		return DefaultNewPerDay
	}
```

Lines 67-70, old:

```go
func RevPerDay(preset []byte) int32 {
	var p deckPreset
	if err := json.Unmarshal(preset, &p); err != nil || p.Rev == nil || p.Rev.PerDay == nil {
		return DefaultRevPerDay
	}
```

new:

```go
func RevPerDay(preset []byte) int32 {
	p, ok := parseDeckPreset(preset)
	if !ok || p.Rev == nil || p.Rev.PerDay == nil {
		return DefaultRevPerDay
	}
```

Behaviour identical: `parseDeckPreset` returns `ok == false` on exactly the `json.Unmarshal` error
these two inlined, and its `deckPreset{}` zero value has `New`/`Rev` nil, so the second and third
conditions are unchanged. `encoding/json` stays imported (`parseDeckPreset` uses it). This makes
all four preset readers share one parse path, which was the stated point of `parseDeckPreset`'s
doc comment when #116 introduced it.

### 3. `internal/review/grade.go` — one definition of `review_log` sort order

Add above `insertSorted` (currently line 98):

```go
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
```

Replace `insertSorted`'s predicate (lines 102-107), old:

```go
	idx := sort.Search(len(rows), func(i int) bool {
		if !rows[i].ReviewedAt.Equal(ev.ReviewedAt) {
			return rows[i].ReviewedAt.After(ev.ReviewedAt)
		}
		return bytes.Compare(rows[i].ID.Bytes[:], ev.ID.Bytes[:]) > 0
	})
```

new:

```go
	idx := sort.Search(len(rows), func(i int) bool {
		return beforeInLogOrder(ev.ReviewedAt, ev.ID, rows[i].ReviewedAt, rows[i].ID)
	})
```

Replace the batch sort (lines 225-230), old:

```go
	sort.Slice(fresh, func(i, j int) bool {
		if !fresh[i].ReviewedAt.Equal(fresh[j].ReviewedAt) {
			return fresh[i].ReviewedAt.Before(fresh[j].ReviewedAt)
		}
		return bytes.Compare(fresh[i].ID.Bytes[:], fresh[j].ID.Bytes[:]) < 0
	})
```

new:

```go
	sort.Slice(fresh, func(i, j int) bool {
		return beforeInLogOrder(fresh[i].ReviewedAt, fresh[i].ID, fresh[j].ReviewedAt, fresh[j].ID)
	})
```

Equivalence, checked term by term: `Equal` is symmetric; with equality excluded,
`a.Before(b) ⟺ b.After(a)`; `bytes.Compare(a,b) < 0 ⟺ bytes.Compare(b,a) > 0`. `bytes` and `sort`
both stay imported.

### 4. `internal/review/replay.go` — extract the two blocks `grade.go` duplicates

Add after `LoggedReview` (line 19):

```go
// loggedReviews reduces ListReviewLogForCard's rows to what a replay folds. The query's
// ORDER BY reviewed_at, id is what makes the result already sorted the way replayStates requires;
// UTC() normalises pgtype's location so a later comparison against an incoming event's timestamp
// is on one clock.
func loggedReviews(rows []db.ListReviewLogForCardRow) []LoggedReview {
	out := make([]LoggedReview, len(rows))
	for i, r := range rows {
		out[i] = LoggedReview{ID: r.ID, Rating: fsrs.Rating(r.Rating), ReviewedAt: r.ReviewedAt.Time.UTC()}
	}
	return out
}

// writeReplayedState persists a replay's tail: final is the state after the last row, lastPrior
// the state immediately before it, lastReviewedAt its reviewed_at. It writes through the
// UNGUARDED UpsertUserCardStateFromReplay -- a rebuild from review_log is the newest truth for
// the card by construction (architecture.md §6) -- so it must only ever be reached with the
// (user, card) advisory lock held. Both callers (ReplayCard, GradeBatch's out-of-order branch)
// satisfy that.
func writeReplayedState(ctx context.Context, q *db.Queries, userID, cardID pgtype.UUID,
	lastPrior, final fsrs.CardState, lastReviewedAt time.Time) error {
	return q.UpsertUserCardStateFromReplay(ctx, db.UpsertUserCardStateFromReplayParams{
		UserID:        userID,
		CardID:        cardID,
		Due:           pgtype.Timestamptz{Time: final.Due, Valid: true},
		Stability:     final.Stability,
		Difficulty:    final.Difficulty,
		State:         int16(final.State),
		Reps:          final.Reps,
		Lapses:        final.Lapses,
		ElapsedDays:   fsrs.ElapsedDays(lastPrior, lastReviewedAt),
		ScheduledDays: final.ScheduledDays,
		LearningSteps: final.LearningSteps,
		LastReview:    pgtype.Timestamptz{Time: lastReviewedAt, Valid: true},
	})
}
```

Then in `ReplayCard`, replace lines 75-103, old:

```go
	logged := make([]LoggedReview, len(rows))
	for i, r := range rows {
		logged[i] = LoggedReview{ID: r.ID, Rating: fsrs.Rating(r.Rating), ReviewedAt: r.ReviewedAt.Time.UTC()}
	}

	priors, final, err := replayStates(p, logged)
	if err != nil {
		return fsrs.CardState{}, err
	}

	lastReviewedAt := logged[len(logged)-1].ReviewedAt
	elapsedDays := fsrs.ElapsedDays(priors[len(priors)-1], lastReviewedAt)

	if err := q.UpsertUserCardStateFromReplay(ctx, db.UpsertUserCardStateFromReplayParams{
		UserID:        userID,
		CardID:        cardID,
		Due:           pgtype.Timestamptz{Time: final.Due, Valid: true},
		Stability:     final.Stability,
		Difficulty:    final.Difficulty,
		State:         int16(final.State),
		Reps:          final.Reps,
		Lapses:        final.Lapses,
		ElapsedDays:   elapsedDays,
		ScheduledDays: final.ScheduledDays,
		LearningSteps: final.LearningSteps,
		LastReview:    pgtype.Timestamptz{Time: lastReviewedAt, Valid: true},
	}); err != nil {
		return fsrs.CardState{}, err
	}

	return final, nil
```

new:

```go
	logged := loggedReviews(rows)
	priors, final, err := replayStates(p, logged)
	if err != nil {
		return fsrs.CardState{}, err
	}
	lastReviewedAt := logged[len(logged)-1].ReviewedAt
	if err := writeReplayedState(ctx, q, userID, cardID, priors[len(priors)-1], final, lastReviewedAt); err != nil {
		return fsrs.CardState{}, err
	}
	return final, nil
```

`ReplayCard`'s own doc comment (lines 58-64) is unchanged — it is accurate and is the CLAUDE.md
§17 marker.

### 5. `internal/review/grade.go` — dedupe the two `InsertReviewLog` blocks and the duplicate answer

Add after `durationMsArg` (line 96):

```go
// insertReviewLogRow appends this event's one review_log row (§2.5, append-only). Every column
// but the id is derived from state the server already holds (§2.7). ok is false when the id was
// already taken -- a pure retry, which must NOT be rescheduled from the row it already advanced.
func insertReviewLogRow(ctx context.Context, q *db.Queries, p fsrs.Params, userID pgtype.UUID,
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
func duplicateOf(ctx context.Context, q *db.Queries, userID, cardID pgtype.UUID) (*CardStateDTO, Status, error) {
	dto, err := currentStateDTO(ctx, q, userID, cardID)
	if err != nil {
		return nil, "", err
	}
	return dto, StatusDuplicate, nil
}
```

In `gradeInOrder`, replace lines 281-308 (the `q.InsertReviewLog(...)` call through the
`if inserted == 0 { ... }` block) with:

```go
	inserted, err := insertReviewLogRow(ctx, q, p, userID, ev, before, outcome, elapsedDaysBefore)
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
```

In `gradeOutOfOrder`, replace lines 358-383 the same way:

```go
	inserted, err := insertReviewLogRow(ctx, q, p, userID, ev, priorForEv, outcomeForEv, elapsedDaysBefore)
	if err != nil {
		return nil, "", err
	}
	if !inserted {
		return duplicateOf(ctx, q, userID, ev.CardID)
	}
```

and replace lines 337-345's conversion plus lines 385-403's write, old:

```go
	logged := make([]LoggedReview, len(existingRows))
	for i, r := range existingRows {
		logged[i] = LoggedReview{ID: r.ID, Rating: fsrs.Rating(r.Rating), ReviewedAt: r.ReviewedAt.Time.UTC()}
	}
	merged, evIdx := insertSorted(logged, LoggedReview{ID: ev.ID, Rating: ev.Rating, ReviewedAt: ev.ReviewedAt})
```

new:

```go
	merged, evIdx := insertSorted(loggedReviews(existingRows), LoggedReview{ID: ev.ID, Rating: ev.Rating, ReviewedAt: ev.ReviewedAt})
```

and old:

```go
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
```

new:

```go
	lastReviewedAt := merged[len(merged)-1].ReviewedAt
	if err := writeReplayedState(ctx, q, userID, ev.CardID, priors[len(priors)-1], final, lastReviewedAt); err != nil {
		return nil, "", err
	}
```

Net effect on `grade.go`: ~-45 lines, no change to any value written. `bytes`, `sort`, `db`,
`fsrs`, `pgtype`, `pgx`, `errors`, `context`, `time` all still used.

### 6. `internal/db/queries/user_card_state.sql` — correct the `UpsertUserCardStateFromReplay` comment (finding C1)

Old:

```sql
-- The replay writer: unguarded, because a rebuild from review_log IS the newest truth for this card by
-- construction (architecture.md §6). Only internal/review.ReplayCard may call this.
```

New:

```sql
-- The replay writer: unguarded, because a rebuild from review_log IS the newest truth for this card by
-- construction (architecture.md §6). Reached only through internal/review's writeReplayedState -- from
-- ReplayCard and from GradeBatch's out-of-order branch -- and only with the (user, card) advisory lock
-- held. Never call it from anywhere the lock is not already taken.
```

Then run `go generate ./...` and commit the regenerated `internal/db/user_card_state.sql.go`
(CLAUDE.md §16 — do not hand-edit the generated file; its copy of this comment sits at line 76).
This is the one edit in this plan that touches `internal/db/`, zone #122's surface; it is a
factual correction caused by this zone's code, not a change of behaviour.

### 7. `internal/review/grade.go` — (comment covered by edit 5's `!inserted` block, finding C2)

No separate edit; the corrected wording is inlined in edit 5.

### 8. `internal/review/grade.go` — document the equal-`reviewed_at` case (finding C3)

Lines 327-328, old:

```go
	// Re-read rather than trust outcome directly: the guarded upsert's WHERE clause is the
	// authority on what actually landed if a newer last_review is somehow already stored.
```

new:

```go
	// Re-read rather than trust outcome directly: the guarded upsert's WHERE clause is the
	// authority on what actually landed. Its guard is strict (last_review < EXCLUDED), and
	// gradeEvent routes an event whose reviewedAt EQUALS the stored last_review here rather than
	// to the out-of-order branch -- so a second grade at the same microsecond appends its
	// review_log row and leaves user_card_state untouched. That is deliberate: review_log stays
	// the complete truth (§2.5) and a replay reconciles. The DTO returned is the stored state,
	// not `outcome`.
```

This is the only comment this plan adds to `internal/review`; the mechanism is invisible from the
code and spans three files (`grade.go`'s branch condition, the SQL guard, the re-read).

### 9. `internal/http/review.go` — extract the two GET handlers' shared body

Add after `studyDayWindow` (line 175):

```go
// buildStudyBatch resolves the study-day window and the caller's effective FSRS params for deck,
// then builds one batch from it -- the shared tail of GET /decks/{id}/review and
// GET /api/reviews/next, so the two can never fetch a batch under different settings.
func buildStudyBatch(ctx context.Context, store db.DBTX, userID pgtype.UUID, deck db.Deck,
	cur review.Cursor, limit int32, clock time.Time) (review.StudyDay, review.Batch, error) {
	q := db.New(store)
	window, err := studyDayWindow(ctx, q, userID, clock)
	if err != nil {
		return review.StudyDay{}, review.Batch{}, err
	}
	params, err := review.EffectiveParams(ctx, q, userID, deck.ID)
	if err != nil {
		return review.StudyDay{}, review.Batch{}, err
	}
	batch, err := review.BuildBatch(ctx, store, params, userID, deck.ID, deck.Name, window,
		review.NewPerDay(deck.Preset), review.RevPerDay(deck.Preset),
		review.ParseRevOrder(deck.Preset), review.ParseNewMix(deck.Preset), cur, limit, clock)
	if err != nil {
		return review.StudyDay{}, review.Batch{}, err
	}
	return window, batch, nil
}
```

In `GET /decks/{id}/review`, replace lines 50-68 (from `clock := now()` through the `BuildBatch`
error block) with:

```go
		clock := now()
		window, batch, err := buildStudyBatch(r.Context(), store, user.ID, deck, review.Cursor{AtStart: true}, initialBatchSize, clock)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
```

In `GET /api/reviews/next`, replace lines 111-129 with:

```go
		clock := now()
		_, batch, err := buildStudyBatch(r.Context(), store, user.ID, deck, cur, refillBatchSize, clock)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
```

Behaviour-identical by construction: all three folded-in errors already mapped to 500 in both
handlers, and the `pgx.ErrNoRows` → 404 mapping stays where it is, on the `GetDeckForStudy` call
that each handler keeps. `GetDeckForStudy` returns `db.Deck` (`SELECT d.*`), which carries `ID`,
`Name`, and `Preset` — the three fields `buildStudyBatch` needs. ~-22 lines, and the four
`deck.Preset` readers now appear once instead of twice. `clock` is still declared in each handler
because each still needs it for `now()` ordering relative to the deck lookup.

### 10. `internal/http/review.go` — split `parseBatchRequest`'s validation from its response

Replace lines 212-262 in full, old — eight repetitions of
`http.Error(w, "bad request", http.StatusBadRequest); return nil, false` — with:

```go
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
```

Every check, every bound, and every accepted/rejected input is unchanged — the §2.7 validation is
now readable as one list without eight interleaved response blocks. `errors` is already imported
(line 6). The `-- Wire parsing [§2.7] --` banner comment at line 198 and the `wireEvent`/
`wireBatch` structs are untouched.

### 11. `web/static/review.js` — one scan for the next pending card

Add above `showNext` (line 110), inside the `-- Advance --` section:

```js
  // Index of the next card to show: the first slot not yet graded this session. -1 when every
  // slot is done.
  function firstPendingIndex() {
    for (var i = 0; i < state.queue.length; i++) {
      if (!state.queue[i].done) return i;
    }
    return -1;
  }
```

`showNext`, lines 110-115, old:

```js
  function showNext() {
    var idx = -1;
    for (var i = 0; i < state.queue.length; i++) {
      if (!state.queue[i].done) { idx = i; break; }
    }
    var stage = document.getElementById('review-stage');
```

new:

```js
  function showNext() {
    var idx = firstPendingIndex();
    var stage = document.getElementById('review-stage');
```

`maybeShowDone`, lines 267-276, old:

```js
  function maybeShowDone() {
    var hasUpcoming = false;
    for (var i = 0; i < state.queue.length; i++) {
      if (!state.queue[i].done) { hasUpcoming = true; break; }
    }
    if (!hasUpcoming && state.exhausted) {
      var done = document.getElementById('review-done');
      if (done) done.hidden = false;
    }
  }
```

new:

```js
  function maybeShowDone() {
    if (firstPendingIndex() === -1 && state.exhausted) {
      var done = document.getElementById('review-done');
      if (done) done.hidden = false;
    }
  }
```

Both loops were byte-for-byte the same "first index with `!done`" scan with different early exits;
`firstPendingIndex() === -1` is exactly `!hasUpcoming`.

### 12. `web/static/review.js` — correct `takePending`'s doc comment

Lines 334-336, old:

```js
  // Drains and returns state.pending, stashing the result in state.inFlight so a failure can put
  // it back. Called exactly once per flush cycle (flush() itself, or flushOnUnload's sendBeacon
  // fallback -- never both, since each checks state.pending/state.inFlight first).
```

new:

```js
  // Drains and returns state.pending, stashing the result in state.inFlight so a failure can put
  // it back. flush() will not call this while a request is outstanding; flushOnUnload deliberately
  // can -- a pagehide beacon has to go out even mid-flight -- and its overwrite of state.inFlight
  // is harmless, because onBatchSettled puts back the array its own closure captured, not
  // state.inFlight.
```

The old text is wrong: `flushOnUnload` (line 322) tests only `state.pending.length` and
`navigator.sendBeacon`, never `state.inFlight`, so "never both" does not hold. The behaviour is
still correct, for the reason the new text gives (`onBatchSettled(sent, ...)` at line 344 reads its
`sent` parameter, not `state.inFlight`) — this is a comment fix, not a bug fix.

### 13. `web/static/review.js` — drop the orphaned `takePending` export

Lines 427-431, old:

```js
  window.enshuReview = {
    deckId: function () { return state.deckId; },
    cursor: function () { return state.cursor; },
    takePending: takePending,
  };
```

new:

```js
  window.enshuReview = {
    deckId: function () { return state.deckId; },
    cursor: function () { return state.cursor; },
  };
```

`takePending` was exported for the `#review-sender` element's `hx-vals='js:{events:
enshuReview.takePending()}'`, which #99 deleted when the batch POST moved to a direct `fetch()`
(`web/static/README.md`, `docs/plans/99-grading-persistence.md`). Verified unreferenced:
`grep -rn "takePending\|enshuReview" web/ internal/ tests/` returns only `review.js`'s own three
internal call sites (lines 300, 326, 337) and `review.html:28`'s `deckId()`/`cursor()`. There is
no e2e suite (`tests/` holds only `fixtures/apkg/`). The `takePending` function itself stays — it
is still called by `flush()` and `flushOnUnload()`. CSP is unaffected: `'unsafe-eval'` is still
required by `review.html:28`'s surviving `hx-vals="js:…"`.

### 14. `docs/routes.md:117` — the refill cursor description is stale since #116

Old:

> Opaque keyset cursor over `(due, cardId)` + deck id, query params `deck`/`cursor`;

New:

> Opaque keyset cursor + deck id, query params `deck`/`cursor`; the cursor's shape follows the
> deck's `rev.order`/`new.mix` preset — `(group_bit, sort_key, cardId)` for the single-query
> modes, a pair of independent sub-cursors for `mixed` (§6, `internal/review/types.go`'s `Cursor`);

### 15. `docs/architecture.md:496-497` — name the identifier `replayReviews` actually is

`replayReviews` appears in architecture.md four times (lines 212, 478, 496, 634) and in
`docs/plans/`; no such identifier exists in the code, which has had `review.ReplayCard` (over the
pure `replayStates`) since #56. Plans are historical records and are not touched. Amend the one
definitive paragraph, old:

> **The server-side recompute path is load-bearing.** `replayReviews` replays `review_log`

new:

> **The server-side recompute path is load-bearing.** `replayReviews` — `internal/review`'s
> `ReplayCard`, over the pure `replayStates` — replays `review_log`

The other three mentions keep the shorthand, which now resolves.

---

## Read and explicitly not changed

- **`internal/review/types.go`.** The `Cursor` type's nine fields cover two mutually exclusive
  modes and look like more surface than needed, but the cursor is an *opaque wire value*: any
  change to `EncodeCursor`/`DecodeCursor`'s format invalidates cursors in flight in open tabs —
  observable behaviour. `encodeSubCursor`/`decodeSubCursor` are already shared between the two
  modes, and the `groupBitArg`/`keyArg`/`cardIDArg` triple and its mixed-mode counterparts each
  carry a doc comment explaining the sentinel (`-1` group bit, `-Inf` key, all-zero UUID). No edit.
- **`internal/review/batch.go`'s `queueRowFromDue`/`queueRowFromReview`/`queueRowFromNew`
  (lines 73-104).** Three near-identical mappers, but over three *distinct* `sqlc`-generated
  structs. Go has no way to unify them without either an interface (methods cannot be added to
  generated types) or reflection. Recording this so it is not re-attempted.
- **`internal/review/lock.go`.** 71 lines, one cohesive mechanism, `LockCards` exported for
  `internal/apkg/dbwrite.go:91`. Right-sized; keep as its own file.
- **`internal/review/doc.go`.** Names all four concurrency mechanisms in one paragraph and is
  accurate. No edit.
- **`internal/review/params.go`, `interleave.go`, `grade.go`'s `clampOrReject`/`reviewKind`.**
  Read in full; each is minimal and correctly commented.
- **`internal/db/queries/reviews.sql` and `review_log.sql`.** Read in full. The idempotency,
  lock, `CountNewIntroducedToday`/`CountReviewedToday` and `ListDueCardsForStudy` comments are
  accurate and load-bearing. No edit (and `internal/db/` is zone #122's surface anyway).
- **`web/templates/review.html`, `review_cards.html`.** Zone #129's surface; the only findings
  against them (`data-unseen`, `data-study-day-end`) are in Open questions.

---

## Verification steps (for the implementing session)

1. `go build ./...` — edits 1-5 and 9-10 are pure refactors; the `git mv` in edit 1 changes no
   package, import, or identifier.
2. `go generate ./...` after edit 6, then confirm `git diff internal/db/user_card_state.sql.go`
   shows *only* the comment line changing. Commit the regenerated file (CLAUDE.md §16).
3. `go vet ./...` and `golangci-lint run`.
4. `go test ./...`. `internal/review` and `internal/http`'s review tests need `DATABASE_URL`
   (`dbtest_test.go:25` skips without it); run
   `bash .claude/skills/run-app/reset-db.sh` first if local Postgres has stale rows (CLAUDE.md
   §16). The behaviour-critical ones to watch:
   - `internal/http/review_test.go` `TestReviewBatch_ClientCannotWriteSchedulingState` (§10.1),
     `TestReviewBatch_Idempotency` (§10.4), `TestReviewBatch_OutOfOrderReplay`,
     `TestReviewBatch_ReviewedAtClampAndReject`, `TestReviewBatch_MalformedIs400` (covers edit
     10's eight validation paths), `TestReviewPage_HiddenCardShape` (asserts the `data-*` attrs
     edits 9/13 do not touch), `TestReviewNext_KeysetAndExhaustion`.
   - `internal/review` `TestReplayCard_ArrivalOrderConverges` (edit 4), `TestNewPerDay`/
     `TestRevPerDay` (edit 2, in the renamed `preset_test.go`), `TestLockKeys_*`.
   - `internal/fsrs/consistency_test.go` (§10.2 batch-preview/grade-time parity) — untouched by
     this plan but the canary if anything drifted.
5. No schema change, no migration.
6. Manual check (the user's, per CLAUDE.md §14.4): open a deck's reviewer, grade several cards
   with the keyboard, confirm the interval labels still populate, a refill still arrives, the
   "No more cards due" panel still appears, and DevTools shows one `POST /api/reviews/batch` per
   flush with a `{events:[…]}` body — edit 13 removes an export that the page must not have been
   using.

---

## Resolved decisions

All open questions below were resolved with the user before implementation.

1. **File-split call.** Rename only: `newlimit.go` → `preset.go`, as already specified in edit 1.
   Do **not** merge `order.go` into `preset.go`, and do **not** fold `interleave.go` into
   `batch.go`. Leave the four files split as they are today apart from the rename.
2. **Four §6 payload elements computed but never read** (`review.Card.Prior`, `review.Batch.StudyDayEnd`,
   `data-study-day-end`, the `Unseen` chain). **Delete the dead ends** — this is a no-behavior-change
   simplification audit and nothing consumes these values, so removing them changes no wire
   contract for any actual reader. New edit **16** below specifies the removal.

   **Amended mid-implementation:** `batch_test.go` turned out to assert on `.Unseen` in ~12 places
   (real coverage of new-vs-review classification/interleaving), unlike `.Prior`/`StudyDayEnd`,
   which are genuinely untested. Re-asked the user; decision: **keep** `Card.Unseen`,
   `cardView.Unseen`, and the `data-unseen` attribute. Only `Card.Prior`, `Batch.StudyDayEnd`, the
   `data-study-day-end` attribute, and `review.js:88`'s dead `slot.unseen` assignment (JS never
   reads `slot.unseen` even though it reads the `data-unseen` attribute into it) are deleted.
3. **`review.js:404` `confirmedAfter`.** **Remove the dead write** — nothing reads it, so it
   provides no actual reconciliation today. New edit **17** below.
4. **Finding J2 (stale requeue interval labels).** File a follow-up issue rather than fix here (a
   real fix needs fresh server-computed previews on requeue, which is a behavior change). The
   implementing session should file this issue when landing #126, referencing `review.js:247-249`
   and this plan's finding J2.
5. **Duplicate event ids within one batch.** Leave the behavior as-is (rejecting with 400 would be
   a wire-behavior change, out of scope) but add a clarifying comment recording why the
   `ON CONFLICT` backstop path is reachable only by a client reusing its own id. New edit **18**
   below.
6. **`serverError(w)` helper.** Skip — its natural home is next to `notFound()` in `pathparam.go`,
   zone #128's surface. Do not add it in #126.
7. **`sort` → `slices` modernisation.** Skip in this zone. Tracked repo-wide in
   [#139](https://github.com/Jolls/deckshare/issues/139) (filed during this audit) rather than done
   piecemeal per zone.
8. **`BuildBatch`'s 13 parameters.** No change — left as direct parameters, not wrapped in a
   `BuildBatchParams` struct.
9. **`GradeBatch` has no package-level test file.** Leave as-is — the critical invariants
   (CLAUDE.md §10.1/§10.4) are already tested via `internal/http/review_test.go`; no new test file
   required by this audit.

---

## Additional edits from resolved decisions

### Edit 16 — delete the four unread §6 payload elements (resolved decision 2)

- `internal/review/batch.go`: remove `Card.Prior` (declared ~line 32, populated ~line 431, never
  read by `toCardView` or any template) and `Batch.StudyDayEnd` (declared ~line 41, populated
  ~lines 210 and 279; the page's actual `StudyDayEnd` comes from `window.End` instead). Remove
  both fields, their population sites, and any now-unused local variables that fed only them.
- `web/templates/review.html:37`: remove the `data-study-day-end` attribute — `review.js` never
  reads it (only `dataset.deckId`).
- The `Unseen` chain: `Card.Unseen` (`batch.go`) → `cardView.Unseen` → the `data-unseen` attribute
  (`review.html`/`review_cards.html`) → `review.js:88`'s `slot.unseen`, which is assigned but never
  read (`review.js` computes its own "unseen" locally as `!done && !repeat`, a different predicate
  than `data-unseen`'s "never introduced"). Remove `Card.Unseen`, `cardView.Unseen`, the
  `data-unseen` attribute, and `review.js:88`'s dead assignment to `slot.unseen`. Do not touch
  `review.js`'s own local unseen-counting logic — that predicate is real and used.
- These touch `web/templates/review.html` / `review_cards.html`, nominally zone #129's surface;
  land them here since they are a direct consequence of this audit's finding and are pure
  deletions of unread server-emitted fields, not template redesign.
- Verify with `grep -rn "StudyDayEnd\|\.Prior\b\|data-unseen\|data-study-day-end\|\.Unseen\b" internal/review internal/http web/templates web/static` returning nothing after the edit (aside from any unrelated same-named identifiers — inspect each hit).

### Edit 17 — delete `review.js`'s dead `confirmedAfter` write (resolved decision 3)

Remove the `confirmedAfter` variable/field written by `applyAfter` (`review.js:404` and its
assignment site) since nothing reads it. Confirm with
`grep -n "confirmedAfter" web/static/review.js` returning nothing afterward.

### Edit 18 — comment on the duplicate-event-id path (resolved decision 5)

In `internal/review/grade.go`, near where `order`/`results` are built from batch events
(`grade.go:140-141`), add a comment recording that `GradeBatch` dedupes `cardID`s but not event
ids, so two events sharing an id but naming different cards collide; the second is answered
`duplicate` with its review silently dropped. Note this is reachable only by a client reusing its
own event id (impractical by accident given UUIDv7), is self-harming only, and leaves
`review_log`/§2.7 intact — deliberately left as behavior, not a bug, per the #126 audit.
