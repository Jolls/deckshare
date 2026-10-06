# #261 — Learner stats page: own progress overall and per deck

`GET /stats`, any logged-in user, own data only, read-only, no new flag/JS/dependency. Metric
definitions follow docs/plans/87-instructor-dashboard.md §1.1 (Recall now / Pass rate 30d /
Reviews 30d) plus Due.

## 1. Files

### 1.1 `internal/db/queries/stats.sql` (new), then `go generate ./internal/db/...`

All queries take `user_id`, join `deck_access` on `can_view AND can_study`, and filter
`user_id = sqlc.arg(user_id)` on `user_card_state` / `review_log`. Header comment cites #261 and
that these are the caller's own rows only.

- `ListStatsCardStateForUser :many` — `c.deck_id, ucs.stability, ucs.difficulty, ucs.state,
  ucs.last_review` from `user_card_state ucs JOIN cards c JOIN deck_access da`. Same columns as
  `ListStudentCardStateForDeck` (progress.sql), but across all of the caller's can_study decks.
- `ListStatsReviewCountsForUser :many` — per `c.deck_id`: `pass_count` (`rating > 1`),
  `review_count`. Filters `rl.state_before = 2` and `rl.reviewed_at >= now - make_interval(days =>
  window_days)`, i.e. the same predicates as `reviewstats` in `ListStudentProgressForDeck`.
  Args: `user_id`, `now`, `window_days`.
- `ListStatsDailyReviewsForUser :many` — overall (all can_study decks), grouped by the caller's
  local study day: `((rl.reviewed_at AT TIME ZONE u.timezone) - make_interval(hours =>
  u.day_start_hour::int))::date AS day`, with `review_count` and `pass_count`. Same
  `state_before = 2` and window predicate as above. Joins `users u` on `u.id = user_id`. Args:
  `user_id`, `now`, `window_days`. Ordered by `day`.

Reused unchanged (no new SQL): `ListStudyableDecksForUser` (decks.sql) for the deck list;
`CountQueueForUser` (reviews.sql) for Due, so the number equals the `/decks` Due column;
`GetStudyDayWindow` via `studyDayWindow` (review.go) for the user's own day boundary.

### 1.2 `internal/http/stats.go` (new)

`registerStatsRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template,
now func() time.Time)`; one route `GET /stats` wrapped in `auth.RequireUser`. Handler order:

1. `ListStudyableDecksForUser`.
2. `studyDayWindow`, then `CountQueueForUser` built exactly as in `GET /decks` (decks.go lines
   47-64: `DueLookAheadMinutes`, `ReleaseGateDay`); take `DueCount` per deck, sum for overall.
3. `ListStatsCardStateForUser`; params via `review.EffectiveParams(ctx, q, user.ID, deckID)` once
   per deck; fold into one `recallAccumulator` per deck plus one overall using
   `fsrs.Retrievability` (same call shape as `foldStudentProgress`, progress.go 160-166).
4. `ListStatsReviewCountsForUser`, window `progressWindowDays`.
5. `ListStatsDailyReviewsForUser`, window `progressWindowDays`; zero-fill missing days in Go so the
   chart has exactly `progressWindowDays` points.
6. Display strings via existing `formatPercent` and `noDataDisplay` (progress.go). A nil recall /
   zero reviews renders `noDataDisplay`, never `0%`.
7. Build two inline SVG strings as `template.HTML` in a helper in this file (polyline/bars, fixed
   viewBox, no JS, no external resource; values are numbers only, nothing user-controlled is
   interpolated). Include `<title>` per chart and a text summary for accessibility.
8. `render(w, pages["stats"], http.StatusOK, map[string]any{"User": user, "AsOf": n, "Overall":
   ..., "Decks": ..., "ReviewsChart": ..., "PassRateChart": ...})`.

Row types: `statsRow{Name, DeckID, RecallDisplay, PassRateDisplay, Reviews, Due}` (overall uses the
same type).

### 1.3 `internal/http/http.go`

Add `registerStatsRoutes(mux, pool, pages, time.Now)` after `registerProgressRoutes` (line 43).

### 1.4 `internal/http/templates.go`

Add `"stats"` to the page list in `parseTemplates`; add `pagePartials["stats"] =
{"templates/back_to_decks.html"}`.

### 1.5 `web/templates/stats.html` (new)

`{{define "content"}}`: `<h1>Stats</h1>`, as-of line, Overall summary (four metrics), per-deck
table (`Deck | Recall now | Pass rate 30d | Reviews 30d | Due`, deck name links `/decks/{id}`,
empty state "No decks you can study yet."), two `<section>`s each with the chart and an `<h2>`,
`{{template "backToDecks" .}}` (confirm the define name in back_to_decks.html before use). Follow
progress.html markup conventions (`<table>`, `.table-scroll` wrapper as in decks.html).

### 1.6 Entry point

Per open question 1.

### 1.7 CSS

Only if the SVG needs it: minimal rules in `web/static/app.css` (SVG colours via `currentColor`
and Pico variables so dark mode works). No new file.

### 1.8 Docs

- `docs/routes.md`: new section `## Stats — stats.go ([#261](...))` after Progress (line ~190)
  with the `GET /stats` row (permission: session; own data only; lists the metrics and the
  can_study scoping), and a one-line note that it adds no permission flag and no write route.
- `docs/architecture.md`: only if its repo-layout (§4) lists http files individually; add
  `stats.go`. Check at implementation time.
- No schema.md / CLAUDE.md invariant change (own-rows read, no cross-user path). No §20 row (no
  divergence from Anki's model).
- CHANGELOG: handled at commit time.

## 2. Test plan

DB-backed tests use `beginTx`, create their own users via `testEmail()`/`loginCookie`, and scope
every assertion to rows the test created (CLAUDE.md §16). No table-wide `count(*)`, no unscoped
`LIMIT 1`. Confirm `DATABASE_URL` is set so none skip.

### (1) Coverage audit — existing tests on touched/reused code

No existing function is modified (all new code is additive; only `http.go` and `templates.go`
gain one line each). Reused functions and their current coverage:

- `fsrs.Retrievability` — `internal/fsrs/schedule_test.go`: `TestRetrievability`.
- `CountQueueForUser` — `internal/review/batch_test.go`: `TestCountQueueForUser_PerDeckLookAhead`;
  `internal/review/release_gate_test.go`: `TestCountQueueForUser_GatesPerDeck`.
- `studyDayWindow` / `GetStudyDayWindow` — `internal/http/cards_state_test.go` (bury test, line
  ~90); `GET /decks` path in `internal/http/decks_test.go`.
- `ListStudyableDecksForUser` — only exercised indirectly via
  `internal/http/review_test.go: TestStudyAll_MixesAcrossDecks`.
- `review.EffectiveParams` — no direct test found by name in this audit; exercised via the
  review/grade handler tests.
- Pass-rate predicate (`state_before = 2`, `rating > 1`, window) —
  `internal/http/progress_test.go: TestProgressRoute_RosterAndPassRate`.
- `formatPercent` / `noDataDisplay` — `TestProgressTemplateRenders`,
  `TestProgressRoute_RosterAndPassRate`.
- Gap: the Recall fold in `foldStudentProgress` has no test (`progress_test.go` never asserts a
  Recall value).

### (2) Characterization tests (pass on unchanged code)

- `TestListStudyableDecksForUser_CanStudyOnly` (new, `internal/http/stats_test.go`): a deck where
  the user has `can_view` only is absent; `can_view`+`can_study` is present. Pins the filter the
  stats deck list depends on.
- `TestFoldStudentProgress_RecallIsMeanRetrievability` (new, `internal/http/progress_test.go`):
  seed one student with two `user_card_state` rows (known stability/difficulty/last_review),
  call `foldStudentProgress` with a pinned `now`, assert `Recall` equals the mean of two direct
  `fsrs.Retrievability` calls with the same params. Pins the definition the stats page must
  match.

### (3) Red tests (fail today: no `/stats` route -> 404, no queries, no template)

All in `internal/http/stats_test.go` unless noted.

- `TestStatsRoute_AccessControl` (§10.5 row): table — no session -> 303 to login; logged-in user
  with no decks -> 200; logged-in user with decks -> 200. Fails today with 404 for logged-in
  cases.
- `TestStatsRoute_OnlyOwnNumbers`: user A and user B both hold `can_study` on a shared deck, both
  have `review_log` + `user_card_state` rows with different values; A's page shows A's
  review count/pass rate and does not contain B's. Fails today (404).
- `TestStatsRoute_ExcludesDecksWithoutCanStudy`: deck with `can_view` only is absent from the
  table and from every overall number; a can_study deck is present. Fails today (404).
- `TestStatsRoute_MetricsMatchSeededData`: pinned `now` (via handler clock injection as other
  handler tests do), known seed -> asserts Recall now (equals direct `fsrs.Retrievability`
  mean), Pass rate (1 Again + 1 Good = 50%), Reviews 30d, and Due equal to `CountQueueForUser`'s
  `DueCount` for the same inputs. Fails today (404).
- `TestStatsRoute_NoDataShowsDash`: can_study deck with no reviews/state renders `noDataDisplay`
  for Recall and Pass rate, never `0%`. Fails today.
- `TestStatsRoute_ReviewsOutsideWindowExcluded`: a review 31 days old is not counted in Reviews
  30d or pass rate or the charts; one 29 days old is. Fails today.
- `TestStatsRoute_DueUsesOwnDayBoundary`: two users, different `timezone`, identical card state;
  a card reviewed between the two rollovers counts as Due for one and not the other (mirror of
  `TestListStudentProgressForDeck_DayBoundaryIsPerStudent`, using `setUserTimezone`). Fails
  today.
- `TestListStatsDailyReviewsForUser_BucketsByOwnStudyDay`: query-level; a review at a time
  straddling the user's `day_start_hour` lands on the expected local day. Fails today (query
  absent).
- `TestStatsCharts_InlineSVGNoScript` (+ `TestStatsTemplateRenders` for the template alone,
  modelled on `TestProgressTemplateRenders`, so it runs without a DB): rendered body contains two
  `<svg` elements, exactly `progressWindowDays` points each, and no `<script` introduced by the
  page. Fails today (no template).
- `TestStatsRoute_ReadOnly`: a `POST /stats` returns 405/404 (no write route), and a GET leaves
  `user_card_state` / `review_log` row counts for the test's own user unchanged. Fails today only
  for the GET-200 precondition.

### (4) Manual-only items

- Charts look right in light and dark mode and at phone width.
- Nav entry point is visible and the link works from `/decks`.
- SVG is readable with a screen reader (title/summary text).
- Page against a real populated account is plausible (Recall vs `/decks/{id}/progress` for the
  same user and deck).

## 3. Verification (no app run, §14)

`go generate ./internal/db/...`, `go build ./...`, `go vet ./...`, `golangci-lint run`,
`go test ./...` with `DATABASE_URL` exported; confirm no skips. No migration (no schema change).

## 4. Open questions

1. Where should the entry point to `/stats` live: a `Stats` button in `decks.html`'s `button-nav`,
   a link in `header.html` (all pages), or both?
2. Overall Recall now: card-weighted mean (all seen cards across can_study decks, one
   accumulator) or the mean of the per-deck means?
3. Daily pass-rate chart: how should a day with zero reviews appear — a gap in the line, or
   omitted from the line (connecting adjacent days)? (Reviews-per-day chart plots 0.)
4. Chart day buckets: the caller's own study day (`timezone` + `day_start_hour`, as planned), or
   plain UTC/calendar days?
5. Due definition: reuse `CountQueueForUser` (matches the `/decks` Due column, includes
   release-gate and "not already reviewed today" rules) as planned, rather than the
   `ListStudentProgressForDeck` due definition from #87. Confirm.
6. Should "Recall now" count only cards in `state` != New (as `fsrs.Retrievability` already
   returns 0 for New), meaning never-seen cards (no `user_card_state` row) are excluded from the
   mean, as in #87? Confirm same behaviour.

## Resolved decisions

1. **Entry point:** a `Stats` button in `decks.html`'s `button-nav`. No header link.
2. **Overall Recall now:** card-weighted mean over all seen cards across `can_study` decks (not a mean of per-deck means).
3. **Pass-rate chart, zero-review days:** break the line (gap). The reviews-per-day chart plots 0.
4. **Day buckets:** the caller's study day (`timezone` + `day_start_hour`).
5. **Due:** reuse `CountQueueForUser`, so it matches the `/decks` Due column.
6. **Recall now, never-seen cards:** excluded (no `user_card_state` row), as in #87.
