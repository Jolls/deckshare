# #137 — Dashboard: cards left to study (respecting daily limits)

## Problem

`web/templates/decks.html` and `web/templates/deck.html` show raw New / Learning / Due queue
counts (`internal/http/decks.go`, `queueCounts`, sourced from `CountQueueForUser` /
`CountQueueForDeck` in `internal/db/queries/reviews.sql`). Those counts ignore the per-deck daily
new/review caps (`internal/review/preset.go`, #101/#115), so when a deck's cap is smaller than its
raw New or Due count, the dashboard overstates what's actually left to study today.

## Formula

Per deck, "cards left to study today" is:

```
min(New, NewRemaining) + Learning + min(Due, RevRemaining)
```

Verified against `internal/review/batch.go`'s `BuildBatch`/`ListDueCardsForStudy` (in
`internal/db/queries/reviews.sql`): only never-seen (New) and review-state (`state = 2`, Due)
cards are capped by the daily allowance; Learning/relearning (`state IN (1, 3)`) are never capped
— see the "Learning/relearning cards are never capped" line in `BuildBatch`'s doc comment and the
`AND (scored.state IS DISTINCT FROM 2 OR ...)` guard in `ListDueCardsForStudy`, which only
restricts `state = 2` rows.

`NewRemaining`/`RevRemaining` (`internal/review/preset.go`) already compute the per-deck allowance
left today, given `perDay` (from `NewPerDay`/`RevPerDay`, reading `decks.preset`) and how many
cards of that type were already introduced/reviewed today. Their existing caller is
`BuildBatch` (`internal/review/batch.go:153-174`), which gets `introducedToday`/`reviewedToday`
from `CountNewIntroducedToday` / `CountReviewedToday` (`internal/db/queries/reviews.sql`) — both
scoped to one `(user_id, deck_id)`. Neither has a "for all of the user's decks, grouped by deck"
variant, which the `/decks` list page needs (it renders every visible deck's counts from one
`CountQueueForUser` call, not one `CountQueueForDeck` call per row — see `internal/http/decks.go:40-53`).

## Changes

### 1. `internal/db/queries/reviews.sql` — two new grouped-by-user queries

Add immediately after `CountReviewedToday` (currently ends at line 337), modeled directly on the
existing `CountQueueForDeck` → `CountQueueForUser` pattern (lines 339-372: same filters, minus the
`deck_id` predicate, grouped by `c.deck_id`):

```sql
-- Same as CountNewIntroducedToday, grouped by deck, for the /decks list (#137). One query for
-- every deck the user can view rather than one CountNewIntroducedToday call per row.
-- name: CountNewIntroducedTodayForUser :many
SELECT c.deck_id                                     AS deck_id,
       count(DISTINCT rl.card_id)::bigint            AS introduced_count
FROM review_log rl
JOIN cards c       ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND rl.state_before = 0
  AND rl.reviewed_at >= sqlc.arg(study_day_start)::timestamptz
  AND rl.reviewed_at <  sqlc.arg(study_day_end)::timestamptz
GROUP BY c.deck_id;

-- Same as CountReviewedToday, grouped by deck, for the /decks list (#137). One query for every
-- deck the user can view rather than one CountReviewedToday call per row.
-- name: CountReviewedTodayForUser :many
SELECT c.deck_id                                     AS deck_id,
       count(DISTINCT rl.card_id)::bigint            AS reviewed_count
FROM review_log rl
JOIN cards c       ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND rl.state_before = 2
  AND rl.reviewed_at >= sqlc.arg(study_day_start)::timestamptz
  AND rl.reviewed_at <  sqlc.arg(study_day_end)::timestamptz
GROUP BY c.deck_id;
```

The single-deck page (`/decks/{id}`) reuses the existing `CountNewIntroducedToday` /
`CountReviewedToday` (already scoped to one deck) — no new query needed there.

Regenerate after editing: `go generate ./internal/db/...` (runs `sqlc generate -f
../../sqlc.yaml`, per `internal/db/pool.go`'s directive and CLAUDE.md §16). Commit the generated
`internal/db/reviews.sql.go` diff unedited (adds `CountNewIntroducedTodayForUser`,
`CountReviewedTodayForUser`, their `*Row`/`*Params` types).

### 2. `internal/review/preset.go` — new pure helper `LeftToStudy`

Add after `RevRemaining` (currently ends line 86):

```go
// LeftToStudy is how many of a deck's New/Learning/Due cards are actually left to study today
// (#137): New and Due are capped at today's remaining daily allowance (NewRemaining/RevRemaining
// above); Learning/relearning cards are never capped by the daily-limit system (same rule
// BuildBatch and ListDueCardsForStudy enforce -- state 1/3 rows are excluded from both the
// new-card and review-card cap checks).
func LeftToStudy(newCount, learningCount, dueCount int64, newRemaining, revRemaining int32) int64 {
	newLeft := newCount
	if int64(newRemaining) < newLeft {
		newLeft = int64(newRemaining)
	}
	dueLeft := dueCount
	if int64(revRemaining) < dueLeft {
		dueLeft = int64(revRemaining)
	}
	return newLeft + learningCount + dueLeft
}
```

Test (append to `internal/review/preset_test.go`, table-driven like `TestNewRemaining`): cases for
new count under/over/equal to remaining, due count under/over/equal to remaining, learning always
passed through unchanged, and zero remaining on either axis.

### 3. `internal/http/decks.go` — wire it into both routes

Extend `queueCounts` (line 20-24) with a fourth field:

```go
type queueCounts struct {
	New      int64
	Learning int64
	Due      int64
	Left     int64
}
```

**`GET /decks` handler (lines 27-54).** After the existing `CountQueueForUser` call, add the two
new grouped queries and fold them in when building `counts`:

```go
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
```

`presetByDeck` doesn't exist yet — build it from `decks` (the `[]db.ListDecksForUserRow` already
fetched at the top of the handler, line 30; `Preset []byte` is already a field on that row, see
`internal/db/decks.sql.go:157-167`):

```go
presetByDeck := make(map[pgtype.UUID][]byte, len(decks))
for _, d := range decks {
	presetByDeck[d.ID] = d.Preset
}
```

Place this map build right after the `decks, err := q.ListDecksForUser(...)` call, before it's
needed.

**`GET /decks/{id}` handler (lines 100-147).** `deck` (from `q.GetDeckForUser`, a `db.Deck`) already
carries `Preset`. After the existing `queueRow, err := q.CountQueueForDeck(...)` call, add:

```go
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
```

And change the `"Queue"` entry in the final `render(...)` call (line 145) to:

```go
"Queue": queueCounts{
	New: queueRow.NewCount, Learning: queueRow.LearningCount, Due: queueRow.DueCount,
	Left: review.LeftToStudy(queueRow.NewCount, queueRow.LearningCount, queueRow.DueCount, newRemaining, revRemaining),
},
```

`review` is already imported in `decks.go` (line 16), so no new import needed. `pgtype` is
already imported (line 11).

### 4. Templates

**`web/templates/decks.html`** — add a "Left" column to the table (after `Due`, before the edit
link), following the existing per-row pattern (lines 8, 16-18):

```html
<tr><th>Name</th><th>Cards</th><th>New</th><th>Learning</th><th>Due</th><th>Left today</th><th></th></tr>
```
```html
<td>{{$counts.Due}}</td>
<td>{{$counts.Left}}</td>
```

**`web/templates/deck.html`** — extend the existing summary line (line 5):

```html
<p>New: {{.Queue.New}} · Learning: {{.Queue.Learning}} · Due: {{.Queue.Due}} · Left today: {{.Queue.Left}}</p>
```

This adds the figure alongside the existing New/Learning/Due breakdown rather than replacing any
of it (default per the task brief — no strong reason found to remove the raw breakdown; it's
still useful for seeing what's capped vs. what's genuinely absent).

## Tests

- `internal/review/preset_test.go`: `TestLeftToStudy`, table-driven (see formula section above).
- `internal/http/decks_test.go`: extend or add a test seeding a deck with a `new.perDay` cap
  smaller than its New-queue count (via `POST /decks/{id}/edit` with `new_per_day=N`, the existing
  path exercised by `TestDeckEditRoute_NewPerDay`), then asserting the rendered `/decks` and
  `/decks/{id}` HTML shows a "Left today" figure less than the raw New count. Match the existing
  golden-path style in `decks_test.go` (`doRequest` + HTML substring/regexp assertions) rather than
  introducing a new test harness.

No FSRS-package or `.apkg` code is touched, so CLAUDE.md §10's "always ships a test" exception
doesn't independently apply here — the SQL query change (touching `review_log`/`user_card_state`
adjacent read paths) still warrants the table-driven `LeftToStudy` unit test plus the handler test
above per rule 5 (non-obvious edge case: capped-vs-raw count divergence is exactly the silent bug
this issue reports).

## Non-goals / out of scope

- No change to `ListDueCardsForStudy`/`BuildBatch` — the capping logic there is untouched; this
  only adds a read-side dashboard figure that mirrors it.
- No change to `decks.preset` schema or `NewPerDay`/`RevPerDay` semantics.
- Mixed-mode (`new.mix = "mixed"`, #116) needs no special handling here: the cap semantics
  (new capped by `newRemaining`, review capped by `revRemaining`, learning uncapped) are identical
  in `buildMixedBatch` — the dashboard formula doesn't depend on `new.mix`/`rev.order` at all,
  only on the counts and the two remaining-allowance numbers.

## Open questions

None — the formula, query strategy, call sites, and template placement are all resolved above by
reading the existing code (no genuinely ambiguous decision found). If the reviewer wants the
label "Left today" worded differently (e.g. "Left to study", "Remaining"), that's a one-line
template string change, not a structural one.
