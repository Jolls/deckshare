# Plan — #101: per-deck daily new-card limit (v1 — enforcement only)

**Issue:** [#101](https://github.com/Jolls/deckshare/issues/101) — "Study goes through all due cards & All new cards."
**Resolved scope (v1):** per-deck "new cards/day" setting stored in `decks.preset`, default 20,
**enforced** in the review batch fetch, editable from the deck edit page. No global/user-level
limit, no review-count cap, no learning-steps config.

**Deferred to [#106](https://github.com/Jolls/deckshare/issues/106):** capping the new-card counts
*displayed* on `/decks` and `/decks/{id}` to match what's actually servable. Those pages will
keep showing the deck's raw unseen-card count through this PR — cosmetic once enforcement is
correct (§9), and split out to keep this PR to the enforcement path. Not milestoned.

## 0. Findings that fix the design

1. **`decks.preset jsonb NOT NULL DEFAULT '{}'` is genuinely unclaimed.** Nothing reads or writes it: `internal/db/models.go:25` and the generated `decks.sql.go` only carry it through as `[]byte`; `docs/plans/54` §"GET/POST /decks/{id}/edit covers name + description only", `docs/plans/56` open question 9 ("caps are a follow-up once preset editing exists in the UI"), `docs/plans/58` §10.5 ("`col.dconf`/`deck_config` stay unread; `decks.preset` keeps its `'{}'` default") all confirm it is reserved for exactly this. **No migration is required** (§2 below).
2. **"Introduced today" must come from `review_log.state_before = 0`, not from `user_card_state`.** A card leaves FSRS state `New` on its first-ever grade and never returns (`internal/review/grade.go` writes `StateBefore: int16(before.State)`, and no code path resets `state` to 0). A lapse writes `state_before = 2`, a relearning step `3`, a same-day learning-step repeat `1` — none of them can be mistaken for an introduction. The `user_card_state`-based proxies are all wrong: `reps = 1` misses a card that took two learning steps today, and `last_review >= study_day_start` counts every review of the day.
3. **Count `DISTINCT card_id`, not `count(*)`.** `review_log` is append-only, and `gradeOutOfOrder` (grade.go:336) recomputes `state_before` from a replay — an out-of-order arrival can therefore leave *two* rows with `state_before = 0` for one card (the first-arrived row keeps the value the server genuinely held). A card is introduced once; `count(DISTINCT rl.card_id)` is what makes that true regardless of arrival order.
4. **A per-request "remaining = limit − introduced" alone overshoots.** Served-but-ungraded cards are not yet in `review_log`. Batch 1 serves 20 new; the refill fires (§6's "<10 unseen left") after 12 grades; `introduced = 12` would allow 8 *more*. The fix is to cap by **ordinal position within the never-seen set**, not by a count of rows returned: cards introduced earlier today have gained a `user_card_state` row and have dropped out of that set, so the ranking restarts at 1 and the served-but-ungraded cards occupy ranks 1..N of the current allowance. The cursor keeps them from being re-sent; the next card's rank exceeds the allowance and is refused. This is stable under every interleaving of grade and refill.
5. **`unseen` is `ucs.user_id IS NULL`** everywhere in `reviews.sql` — no `user_card_state` row at all. Imported *new* Anki cards get no row (`internal/apkg/dbwrite.go:seedCardStates` skips a card with `hasState == false`), so they are genuinely new here and the cap applies to them. This directly answers the issue's "is there an applied limit to new cards on imports being missed?" — there is none today, and this change supplies one.
6. **Suspended/buried filters need no duplication in the cutoff subselect.** A card with no `user_card_state` row cannot be suspended or buried; the only eligibility predicate that applies to a never-seen card is deck membership.
7. **The limit is per-deck (shared), the count is per-(user, deck).** Consequence, and it is the right one under §2.1: a co-owner with `can_edit_settings` changes the limit for everyone studying the deck, but one user's introductions never consume another's allowance.

## 1. Preset JSON shape

Anki's `dconf` shape, per CLAUDE.md §2.10 (conformance is the default; divergence needs a reason):

```json
{"new": {"perDay": 20}}
```

- Key path: `preset -> 'new' -> 'perDay'`, integer.
- Absent / `'{}'` / malformed ⇒ **20** (Anki's classic default).
- Accepted range **0..9999** (Anki's own bounds). `0` means "no new cards from this deck".
- Nesting rather than a flat `new_per_day` leaves the room `docs/plans/56` already anticipates for `new.delays` (learning steps) and `rev.perDay` without a second shape decision later.
- **Parsing happens in Go, never in SQL.** A malformed value must degrade to the default, not turn every study fetch into a 500 — Postgres has no safe cast, so `(preset->'new'->>'perDay')::int` inside the queue query would do exactly that.

No `§20` deviation row: this matches Anki.

## 2. Migration

**None.** `preset` exists, is `NOT NULL DEFAULT '{}'`, and every existing row already reads as "unset ⇒ 20" through the Go accessor. Adding a real column would be a schema change to store one integer the reserved column already covers, and would put us in the position of migrating it again when `new.delays` lands.

**No new index either.** The introduction count is bounded to one day by `review_log_user_id_reviewed_at_idx (user_id, reviewed_at)`; the cutoff subselect is bounded by `cards_deck_id_idx`. (If a deck ever grows large enough for the cutoff's `ORDER BY c2.id` sort to matter, a `cards (deck_id, id)` index is the fix — do **not** add it speculatively in this issue.)

## 3. `internal/db/queries/reviews.sql`

### 3.1 New query — `CountNewIntroducedToday`

Append after `CountQueueForUser`, with this comment (the reasoning in §0.2/§0.3 must survive in the file):

```sql
-- New-card introductions inside the current study day, for one deck (#101). A card is introduced
-- by the one review that takes it out of FSRS state New, so review_log.state_before = 0 is the
-- exact marker: a lapse carries 2, a relearning step 3, a same-day learning-step repeat 1, and
-- none of them can be mistaken for an introduction. count(DISTINCT card_id), not count(*): the
-- out-of-order replay path (architecture.md §6) can leave two state_before = 0 rows for one card,
-- and a card is introduced once.
-- name: CountNewIntroducedToday :one
SELECT count(DISTINCT rl.card_id)::bigint AS introduced_count
FROM review_log rl
JOIN cards c       ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND c.deck_id = sqlc.arg(deck_id)
  AND rl.state_before = 0
  AND rl.reviewed_at >= sqlc.arg(study_day_start)::timestamptz
  AND rl.reviewed_at <  sqlc.arg(study_day_end)::timestamptz;
```

Window bounds are `>= start` / `< end`, matching the half-open `study_day_start`/`study_day_end` pair `GetStudyDayWindow` produces.

### 3.2 `ListDueCardsForStudy` — add the cap

Insert this clause **immediately before** the existing keyset-cursor comparison (`AND (COALESCE(ucs.due, 'infinity'…`), leaving `ORDER BY`, `LIMIT`, and every other filter untouched:

```sql
  -- The per-deck daily new-card cap (#101). new_remaining is the deck's configured limit minus what
  -- has already been introduced today; the caller computes it. The subselect is uncorrelated, so
  -- Postgres runs it once per fetch as an InitPlan: it is the id of the last never-seen card still
  -- inside the allowance, in the same id-ascending order this query serves new cards in.
  -- Capping by POSITION, not by how many rows this fetch returns, is what makes the cap hold
  -- across refills: a card introduced earlier today has a user_card_state row and has left this
  -- set, so the ranking restarts at 1, while a card already served this session but not yet graded
  -- is still in it and still occupies its rank. COALESCE covers "fewer never-seen cards than the
  -- allowance" -- all of them pass. GREATEST keeps OFFSET non-negative: the InitPlan is evaluated
  -- even when the new_remaining > 0 guard is false. Suspended/buried are not re-checked here
  -- because a card with no user_card_state row can be neither.
  AND (ucs.user_id IS NOT NULL
       OR (sqlc.arg(new_remaining)::int > 0
           AND c.id <= COALESCE((
                 SELECT c2.id
                 FROM cards c2
                 LEFT JOIN user_card_state u2
                        ON u2.user_id = sqlc.arg(user_id) AND u2.card_id = c2.id
                 WHERE c2.deck_id = sqlc.arg(deck_id) AND u2.user_id IS NULL
                 ORDER BY c2.id
                 OFFSET GREATEST(sqlc.arg(new_remaining)::int - 1, 0)
                 LIMIT 1
               ), 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid)))
```

Rejected alternative, for the record: a `count(*) FILTER (WHERE unseen) OVER (ORDER BY queue_key, card_id ROWS UNBOUNDED PRECEDING)` window in a CTE. It is correct only if the cursor is applied *outside* the CTE, which forces the whole eligible set (all due cards included) through a window on every refill and throws away the LIMIT pushdown the keyset design exists to keep.

`CountQueueForDeck` and `CountQueueForUser` are **not touched** in this PR — the counts they
report stay the deck's raw unseen count, uncapped, until [#106](https://github.com/Jolls/deckshare/issues/106).

## 4. `internal/db/queries/decks.sql` — `UpdateDeck`

```sql
-- name: UpdateDeck :execrows
UPDATE decks d
SET name = sqlc.arg(name),
    description = sqlc.arg(description),
    -- #101: nested-merge, not jsonb_set. jsonb_set('{}', '{new,perDay}', …, true) is a no-op when
    -- the parent object is missing, which every deck's default '{}' preset is. NULL leaves preset
    -- untouched so a form that doesn't carry the field can't wipe the setting.
    preset = CASE WHEN sqlc.narg(new_per_day)::int IS NULL THEN d.preset
                  ELSE d.preset || jsonb_build_object('new',
                         COALESCE(d.preset -> 'new', '{}'::jsonb)
                         || jsonb_build_object('perDay', sqlc.narg(new_per_day)::int))
             END,
    modified_at = now()
FROM deck_access da
WHERE d.id = sqlc.arg(deck_id) AND da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_settings;
```

Authorisation is unchanged — the same `can_edit_settings` collapse-to-404 shape, so the new setting inherits it for free.

Regenerate with `go generate ./...` (§16); do not hand-edit `internal/db/*.sql.go`. Expect `UpdateDeckParams.NewPerDay pgtype.Int4`, `ListDueCardsForStudyParams.NewRemaining int32`, and `CountNewIntroducedTodayParams{UserID, DeckID, StudyDayStart, StudyDayEnd}`.

## 5. Go changes

### 5.1 New file `internal/review/newlimit.go`

```go
package review

// The per-deck daily new-card limit (#101), stored under decks.preset as Anki's own dconf shape:
// {"new":{"perDay":20}}. Parsed here rather than in SQL so a malformed preset degrades to the
// default instead of failing every study fetch (Postgres has no safe cast).
const (
	DefaultNewPerDay int32 = 20   // Anki's classic default
	MaxNewPerDay     int32 = 9999 // Anki's own upper bound
)

type deckPreset struct {
	New *struct {
		PerDay *int32 `json:"perDay"`
	} `json:"new"`
}

// NewPerDay reads decks.preset. Absent, malformed, or out of range -> DefaultNewPerDay; a value
// inside 0..MaxNewPerDay is returned as written, 0 included (no new cards from this deck).
func NewPerDay(preset []byte) int32

// NewRemaining is the deck's allowance left in the current study day, never negative.
func NewRemaining(perDay int32, introducedToday int64) int32
```

`NewPerDay` clamps rather than errors: a value < 0 or > `MaxNewPerDay` (only reachable by hand-editing the DB — the handler validates) returns `DefaultNewPerDay`.

`CapNewCount` (the displayed-count helper) is **not part of this PR** — see [#106](https://github.com/Jolls/deckshare/issues/106).

### 5.2 `internal/review/batch.go`

`BuildBatch` gains one parameter, `newPerDay int32`, after `window` (so the two call sites cannot forget the introduction count — it is computed inside, not passed in):

```go
func BuildBatch(ctx context.Context, store db.DBTX, p fsrs.Params, userID, deckID pgtype.UUID,
	deckName string, window StudyDay, newPerDay int32, cur Cursor, limit int32, now time.Time) (Batch, error)
```

Immediately before the `ListDueCardsForStudy` call:

```go
introduced, err := q.CountNewIntroducedToday(ctx, db.CountNewIntroducedTodayParams{
	UserID: userID, DeckID: deckID,
	StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
	StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
})
if err != nil {
	return Batch{}, fmt.Errorf("review: count new introduced today: %w", err)
}
```

and `NewRemaining: review.NewRemaining(newPerDay, introduced)` on `ListDueCardsForStudyParams`. Extend the `BuildBatch` doc comment with: *"New (never-seen) cards are capped at the deck's preset new/perDay minus the introductions already logged this study day (#101); due and learning cards are never capped."*

`Exhausted` needs no change — `len(rows) < limit` is exactly right when the cap truncates the fetch.

### 5.3 `internal/http/review.go`

Both call sites — the page handler (line ~61) and `GET /api/reviews/next` (line ~120) — pass `review.NewPerDay(deck.Preset)`. `GetDeckForStudy` selects `d.*`, so `deck.Preset` is already in hand at both; no extra query.

### 5.4 `internal/http/decks.go`

Only the edit page changes — the list (`GET /decks`) and single-deck (`GET /decks/{id}`) handlers are untouched in this PR (their counts stay uncapped, per [#106](https://github.com/Jolls/deckshare/issues/106)):

- `GET /decks/{id}/edit`: add `"NewPerDay": review.NewPerDay(deck.Preset)` to the render map.
- `POST /decks/{id}/edit`: parse the field, then pass it through:

```go
newPerDay := pgtype.Int4{} // absent or empty -> leave preset untouched
if raw := strings.TrimSpace(r.PostForm.Get("new_per_day")); raw != "" {
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 || int32(v) > review.MaxNewPerDay {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	newPerDay = pgtype.Int4{Int32: int32(v), Valid: true}
}
```

  Absent ⇒ untouched is deliberate: it keeps the existing `TestDeckRoutes_GoldenPath` POST (`name=Renamed&description=`) meaningful and stops any future partial form from silently resetting the setting. The 400 shape matches the handler's existing treatment of a bad `name`.

`internal/http` already imports `internal/review`, so no new package edge.

### 5.5 `web/templates/deck_edit.html`

Inside the existing edit form, between the description textarea and the submit button:

```html
    <label for="new_per_day">New cards per day</label>
    <input type="number" id="new_per_day" name="new_per_day" min="0" max="9999" step="1" value="{{.NewPerDay}}" required>
    <small>0 introduces no new cards from this deck. Due and learning cards are never limited.</small>
```

`web/templates/decks.html` and `web/templates/deck.html` need no change in this PR — they keep rendering the raw, uncapped `$counts.New` / `.Queue.New` until [#106](https://github.com/Jolls/deckshare/issues/106).

## 6. Docs to update in the same PR

- `docs/schema.md`: under the `decks` block, document the preset shape — `{"new": {"perDay": 20}}`, absent ⇒ 20, range 0..9999, parsed in Go — and note that it is enforced by `ListDueCardsForStudy`. Note that `CountQueueForDeck`/`CountQueueForUser` still report the raw unseen count (tracked in #106).
- `docs/routes.md` line 58: `GET /decks/{id}/edit` no longer says "`preset` is not yet editable"; it now edits name, description, and new-cards-per-day. Add the same to the `POST /decks/{id}/edit` row.
- `docs/architecture.md` §1: one sentence on the landing, in the existing style.
- **`CHANGELOG.md` — entry needed, but do not write it here.** Per CLAUDE.md §14 the batch's entry is written once by the manager session, at the top under a new `## [0.1.X] - YYYY-MM-DD` with `### Added` and `([#101](https://github.com/Jolls/deckshare/issues/101))`.
- `docs/plans/56-reviewer-batch-grading.md` open question 9 is now partly answered — enforcement lands, but the display-count half of that question is now tracked in #106; no edit to that plan (plans are historical records).

## 7. Tests (CLAUDE.md §10 — this is the item-2 batch-fetch path plus DB queries)

**`internal/review/newlimit_test.go`** (pure, no DB): table-driven over `NewPerDay` — `nil`, `{}`, `{"new":{}}`, `{"new":{"perDay":0}}`, `{"new":{"perDay":5}}`, `{"new":{"perDay":-1}}`, `{"new":{"perDay":10000}}`, `{"new":{"perDay":"20"}}`, `not json`. Plus `NewRemaining` (introduced > limit ⇒ 0).

**`internal/review/batch_test.go`** (new, DB-backed via the existing `beginTx`/`seedFixture` harness in `dbtest_test.go`; add a `seedCards(t, tx, f, n)` helper there that adds N further never-seen cards to the fixture deck). Required cases:

1. **Default cap.** 30 unseen cards, `preset = '{}'` ⇒ across the initial fetch and refills, exactly 20 distinct new cards are ever served.
2. **At the limit, due cards still flow.** 20 `review_log` rows with `state_before = 0` and `reviewed_at` inside the window, plus one card due today ⇒ zero unseen cards served, the due card served. *This is the issue's core assertion.*
3. **Fresh study day resets.** Same data, window advanced by one day ⇒ new cards served again.
4. **`perDay = 0`.** No new cards; due cards unaffected.
5. **Refill does not overshoot.** Serve 20, grade 12 (real `GradeBatch` calls, so `review_log` is written the way production writes it), refill ⇒ zero further new cards.
6. **Lapses do not consume the allowance.** A `review_log` row with `state_before = 2` inside the window leaves the day's new count untouched.
7. **Duplicate introduction rows count once.** Two `state_before = 0` rows for one card (the out-of-order-replay shape, §0.3) consume exactly one of the allowance.

**`internal/http/decks_test.go`**: `POST /decks/{id}/edit` with `new_per_day=5` persists and re-renders in the edit form; `new_per_day=abc`, `-1`, `10000` ⇒ 400 with the deck unchanged; the field absent leaves an existing value intact; access control unchanged (a caller without `can_edit_settings` still 404s).

The displayed-count test (`GET /decks/{id}` renders `New: 0` when the allowance is exhausted) moves to [#106](https://github.com/Jolls/deckshare/issues/106) along with the feature it tests.

## 8. Sequencing

1. `reviews.sql` (§3) + `decks.sql` (§4), then `go generate ./...`.
2. `internal/review/newlimit.go` (§5.1) + its unit test — it has no DB dependency and unblocks everything else.
3. `BuildBatch` (§5.2) and its two call sites (§5.3); `go build ./...` green here.
4. Handlers + template (§5.4, §5.5).
5. Tests (§7), then docs (§6).
6. `go build ./... && go vet ./... && golangci-lint run && go test ./...` (CLAUDE.md §14 pre-commit); DB-backed tests need `DATABASE_URL` and a clean volume (`bash .claude/skills/run-app/reset-db.sh`, §16).

## 9. Accepted consequences (call these out in the PR body)

- **Displayed new counts stay uncapped in this PR.** `/decks` and `/decks/{id}` will keep
  showing the deck's raw unseen-card count, not what's actually servable today — e.g. "50 new"
  with a 20/day limit and none introduced yet. The *behaviour* is correct (only 20 are ever
  served); the *number shown* is not. Tracked in [#106](https://github.com/Jolls/deckshare/issues/106).
- **Importing today an `.apkg` whose revlog contains today's first-ever reviews consumes today's allowance** for those cards. Correct in substance (they were introduced today) and unreachable for a normal import of older history.
- **A shared deck's limit is shared; the count is not.** §0.7.
- **A card that arrives from an import with a `user_card_state` row but no reviews** (only reachable for a *flagged* new Anki card — `seedCardStates`'s `hasState` test) is not "unseen" here, so the cap does not gate it, though grading it does consume the day's allowance. Errs toward fewer new cards, never more. Its pre-existing exclusion from every `CountQueueForDeck` bucket while still being servable is a separate miscount that predates this issue — **not fixed here**; worth a follow-up issue.

## Open questions

None blocking. Two decisions were made rather than deferred, and are reversible if you disagree:

1. **JSON key shape `{"new":{"perDay":N}}`** (Anki's `dconf` nesting) rather than a flat `{"new_per_day":N}`. Chosen on CLAUDE.md §2.10 and to leave room for `new.delays`/`rev.perDay`. Changing it later means a data migration over `preset`, so it is worth a nod before implementation.
2. **Absent `new_per_day` on `POST /decks/{id}/edit` leaves the stored value untouched** rather than resetting to the default. Keeps the existing handler tests honest and makes a partial form non-destructive.

### Critical Files for Implementation
- internal/db/queries/reviews.sql
- internal/review/batch.go
- internal/http/decks.go
- internal/db/queries/decks.sql
- web/templates/deck_edit.html
