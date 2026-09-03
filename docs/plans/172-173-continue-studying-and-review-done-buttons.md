# #172 + #173 — Continue studying past the daily limit, and the review-done button row

Two issues bundled because both land in the same place: `#review-done` in
`web/templates/review.html`, the section `web/static/review.js` unhides when the queue is
exhausted for the study day.

- **#172** — "add a method to continue studying new or due cards even if you've maxed out your
  new/due normal deck limits"
- **#173** — "Add a 'back to home' button, next to 'back to deck' hyperlink and make back to deck
  a white button. 'back to home' should be a blue button."

Branch: `feature/172-173-continue-studying-and-review-done-buttons`.

---

## Resolved decisions

All three questions below were put to the human and resolved. Nothing left as a judgment call.

### Q1 — How does "continue studying" lift the daily cap? → resolved: re-grant a full preset round, not unlimited

**Human's answer (verbatim intent):** "continue studying" should grab *another full day's
allowance*, sized to the deck's own preset — if `rev.perDay` is 5, one click makes 5 more due cards
available; if it's 20, one click makes 20 more available. Not an unlimited/infinite bypass. Clicking
again grants another round on top (cumulative, repeatable — "lets you study again").

This is a **different, smaller mechanism** than the MaxInt32-sentinel design originally drafted
below (now superseded) — no query change needed at all, since it reuses the exact same
`NewRemaining`/`RevRemaining` arithmetic `BuildBatch` already does, just with bigger preset inputs.

**Mechanism:** `BuildBatch` gains a trailing `extraRounds int32` parameter (0 = normal day). Before
computing `newRemaining`/`totalRemaining`, the deck's presets are inflated by that many extra
rounds:

```go
effectiveNewPerDay := newPerDay + extraRounds*newPerDay   // newPerDay * (1 + extraRounds)
effectiveRevPerDay := revPerDay + extraRounds*revPerDay   // revPerDay * (1 + extraRounds)
```

then `newRemaining := NewRemaining(effectiveNewPerDay, introduced)` and
`totalRemaining := RevRemaining(effectiveRevPerDay, introduced+reviewed)` — everything else in
`BuildBatch` (the `if totalRemaining <= 0 { ... } else { effectiveLimit = min(limit, totalRemaining) }`
clamp, `buildSingleBatch`, `buildMixedBatch`) is unchanged, because it already just consumes
whatever `newRemaining`/`totalRemaining` it's handed. **No SQL change, no `go generate`, no
migration** — this is the same code path as any ordinary fetch, with larger preset values.

**Overflow/abuse guard (client-controlled input):** `extraRounds` arrives on an HTTP query param, so
it must be validated at the boundary (CLAUDE.md "validate at system boundaries"). The HTTP handler
parses it and clamps to `[0, maxExtraRounds]` before calling `BuildBatch`; define
`const maxExtraRounds int32 = 20` in `internal/http/review.go`. This bounds
`newPerDay*(1+extraRounds)` well clear of int32 overflow for any realistic preset value and caps how
much a manually-crafted query string can inflate the effective daily allowance. `BuildBatch` itself
does not re-validate — it trusts its caller the same way it already trusts `newPerDay`/`revPerDay`.

**Session behaviour:** the client (not the server) accumulates the round count for the rest of the
page session — clicking "Keep studying" increments a counter and every subsequent refill (manual
click or automatic) resends the current count, so the expanded cap keeps applying until the user
reloads the page or the deck is genuinely out of cards. A reload resets to 0 (extra rounds are not
persisted — see the "Left today" note under Q3).

### Q2 — Does "continue studying" apply to the mixed `/study` (Study All) session? → resolved: no, single-deck only

Confirmed: **single-deck `/decks/{id}/review` only.** `study.html` and the `GET /study` mixed-session
handler are untouched — `GET /study` (#169) is one-shot (`internal/http/review.go` hard-codes
`Exhausted: true`, no `#review-refill`), and `/api/reviews/next` takes exactly one `deck` param, so
there's no multi-deck refill path to extend. #172's text ("your new/due normal deck limits") reads as
per-deck. A `/study`-side follow-up can be filed separately if wanted later.

### Q3 — Should lifting the cap change deck-priority/order behaviour, or be recorded anywhere? → resolved: no, confirmed

- Order/priority: keep the deck's `rev.order` and `priority` exactly as configured. The extra-rounds
  mechanism only inflates the two *quantity* ceilings (`newPerDay`, `revPerDay`) that feed
  `NewRemaining`/`RevRemaining`; `PriorityAllocate` is not on `BuildBatch`'s fetch path at all (only
  `LeftToStudy` calls it), so nothing there needs touching.
- Recording: nothing new is written. The caps are a fetch-time filter, never persisted, so no
  `review_log` or `user_card_state` semantics change, and CLAUDE.md §2.5 / §15's "silently corrupts
  training data" reflex is not engaged. Grades from extra cards are ordinary grades, and they do
  count against the *next* day's arithmetic through `CountNewIntroducedToday` /
  `CountReviewedToday` the same as any other — correct, since `extraRounds` resets to 0 on the next
  page load / next study day regardless.
- Confirmed consequence: the deck list's "Left today" and the Study All count (`LeftToStudy`,
  `internal/review/preset.go`) will still read 0/normal while the user is mid-extra-round, since
  `extraRounds` is a page-session-only counter, never persisted. Acceptable per the human's answer.
- Docs to update: `docs/architecture.md` §20's "Filtered / custom-study decks — Not built" row (a
  narrow slice of Anki's Custom Study now exists), and `BuildBatch`'s doc comment, which currently
  states the cap rules without exception.

---

## Settled decisions (#173) — no judgment left

### Where "home" is

`/decks`. `internal/http/auth.go`'s `GET /{$}` handler redirects an authenticated user to `/decks`
and an anonymous one to `/login`; `/decks` is also what the existing `backToDecks` partial
(`web/templates/back_to_decks.html`) and every "Back to decks" button in `deck.html` /
`decks.html` target. Link `/decks` directly, not `/`, to avoid a pointless 303 hop.

### Scope: `#review-done` in `review.html` only

`web/templates/back_to_deck.html` is `{{define "backToDeck"}}<p><a href="/decks/{{.ID}}">Back to
deck</a></p>{{end}}` and is used by `review.html` (twice — line 5 page header and line 38 inside
`#review-done`), `study.html`'s sibling `backToDecks`, plus `deck_edit.html`, `access.html`,
`deck_new.html`, `import_ai.html`, `import.html`.

**Decision:** change only the `#review-done` occurrence in `review.html`. Do **not** edit
`back_to_deck.html` itself, and do **not** touch the page-header occurrence at `review.html` line 5
or any of the six other templates. Rationale: #173 was filed immediately after review-session work
and describes the completion screen's link; restyling the shared partial would silently restyle six
unrelated pages, violating CLAUDE.md working rule 3 (surgical changes). Because the partial stays
untouched, the two buttons are written inline in `#review-done`.

`study.html`'s own `#review-done` is also left alone: its only link is `{{template "backToDecks"}}`,
which already points at home, and it has no deck to go "back to".

### Exact markup

Button convention in this repo (Pico CSS 2, `web/templates/layout.html`; custom rules in
`web/static/app.css`):

- Solid blue = `role="button"` with **no** `outline` class — this is exactly what commit `70b9056`
  ("Make the deck list's Study button solid blue, not outline", PR #171) did: it deleted `outline`
  from the class list, nothing else. No custom CSS was added and none is needed here.
- White = `role="button" class="outline"`.
- `btn-sm` (`app.css:72`) is the compact size used by every page-level action row.
- `.button-nav` (`app.css:10`) is the flex/wrap row wrapper used by `deck.html:9` and
  `decks.html:3`.

Replace `web/templates/review.html` lines 36–39 with:

```html
<section id="review-done" hidden>
  <p>No more cards due. Nice work.</p>
  <nav class="button-nav">
    <a href="/decks/{{.Deck.ID}}" role="button" class="outline btn-sm">Back to deck</a>
    <a href="/decks" role="button" class="btn-sm">Back to home</a>
  </nav>
</section>
```

Ordering: "Back to deck" keeps its existing position; "Back to home" is added to its right, which is
what "next to" in the issue describes. (#172's button, once Q1 is resolved, goes above this row —
see below.)

No `app.css` change is required for #173.

---

## Implementation spec (#172)

### 1. `internal/review/batch.go`

- `BuildBatch` signature gains a trailing `extraRounds int32` parameter (after `lookAheadMinutes`).
- Inside `BuildBatch`, before the two count queries, compute the effective presets:
  ```go
  effectiveNewPerDay := newPerDay + extraRounds*newPerDay
  effectiveRevPerDay := revPerDay + extraRounds*revPerDay
  ```
  then call `CountNewIntroducedToday`/`CountReviewedToday` exactly as today, and derive
  `newRemaining := NewRemaining(effectiveNewPerDay, introduced)`,
  `totalRemaining := RevRemaining(effectiveRevPerDay, introduced+reviewed)`. When `extraRounds == 0`
  this is byte-for-byte the existing arithmetic (`effectiveNewPerDay == newPerDay`, etc.).
- Nothing else in `BuildBatch` changes: the `if totalRemaining <= 0 { newRemaining = 0 } else {
  effectiveLimit = min(limit, totalRemaining) }` clamp block, `buildSingleBatch`, `buildMixedBatch`
  are untouched — they already just consume whatever `newRemaining`/`totalRemaining` they're given.
- Extend `BuildBatch`'s doc comment with the exception: a caller may request `extraRounds` additional
  full presets for this fetch (#172, "continue studying past today's cap"); learning/relearning
  behaviour, suspended/buried filtering, the study-day `last_review` exclusion, `rev.order`,
  `priority` and `lookAheadMinutes` are all unaffected, and `extraRounds` never changes what is
  *written* — it only inflates the two selection ceilings for this fetch.

No change to `internal/db/queries/reviews.sql`, so no `go generate` / sqlc regeneration and no
migration — `newRemaining`/`totalRemaining` are plain `int32` values already threaded through the
existing queries; this only changes what values `BuildBatch` computes for them.

### 2. `internal/http/review.go`

- Add `const maxExtraRounds int32 = 20` near the top of the file (or beside `BuildBatch`'s other
  call-site constants) — the boundary-validation clamp for the client-controlled query param.
- `buildStudyBatchInWindow` gains a trailing `extraRounds int32`, forwarded to `review.BuildBatch`.
- `buildStudyBatch` gains the same parameter, forwarded to `buildStudyBatchInWindow`.
- `GET /decks/{id}/review` (initial page load) passes `0`. The page load must not honour an
  `extraRounds` query param — extra study starts from the completion screen, so the flag has exactly
  one entry point (`GET /api/reviews/next`).
- `GET /study` (the mixed/per-deck loop) passes `0`.
- `GET /api/reviews/next` parses `r.URL.Query().Get("extraRounds")` with `strconv.Atoi` (or
  `ParseInt`, base 10, bitSize 32); on parse failure, empty, or a negative value, treat as `0` — no
  400, matching how `cursor=""` is treated as "start". On success, clamp:
  `if n > maxExtraRounds { n = maxExtraRounds }`, cast to `int32`, and pass to `buildStudyBatch`.
- Authorisation is untouched: `GetDeckForStudy` (`can_view AND can_study`) still runs first, and
  every list query joins `deck_access`. `extraRounds` cannot widen what a user can see, only how many
  of their own already-authorised cards a fetch returns, and only up to `maxExtraRounds` rounds.
- `decodeBatch` / `wireEvent` / `POST /api/reviews/batch` are **not** touched. The grade body still
  accepts exactly `{id, cardId, rating, reviewedAt, durationMs}` (CLAUDE.md §10.1, §2.7).

### 3. `web/templates/review.html`

Add the trigger button inside `#review-done`, above the nav row from #173:

```html
<section id="review-done" hidden>
  <p>No more cards due. Nice work.</p>
  <p><button type="button" id="study-more">Keep studying</button></p>
  <nav class="button-nav">
    <a href="/decks/{{.Deck.ID}}" role="button" class="outline btn-sm">Back to deck</a>
    <a href="/decks" role="button" class="btn-sm">Back to home</a>
  </nav>
</section>
```

Extend `#review-refill`'s `hx-vals` to carry the current round count:

```
hx-vals='js:{deck: enshuReview.deckId(), cursor: enshuReview.cursor(), extraRounds: enshuReview.extraRounds()}'
```

`enshuReview.extraRounds()` returns the current count as a string (e.g. `'0'`, `'1'`, `'2'`); the
handler's `strconv.Atoi` parses it directly.

### 4. `web/static/review.js`

- Add `extraRounds: 0` to `state`.
- Export `extraRounds: function () { return String(state.extraRounds); }` on `window.enshuReview`,
  alongside the existing `deckId` / `cursor`.
- In `init()`, wire a click listener on `#study-more` that:
  1. increments `state.extraRounds` by 1 (cumulative across clicks in the same page session);
  2. sets `state.exhausted = false` (so `maybeRequestRefill`'s guard opens);
  3. resets `state.cursor = ''`;
  4. hides `#review-done`;
  5. dispatches `refill-needed` on `document.body` (setting `state.refillInFlight = true` first,
     mirroring `maybeRequestRefill`).
- **Why the cursor resets to `''`:** the cards the cap excluded sort *before* the position the
  limited fetch's cursor holds, so carrying that cursor forward would skip exactly the cards the
  button exists to reach. Restarting is safe because `indexBatch`'s `known[cardId]` map already
  drops any card already in the queue or already graded this session, and the server still excludes
  anything with `last_review >= study_day_start`.
- `maybeShowDone` needs no change: after the refill swap, `onAfterSwap` → `indexBatch` re-reads
  `data-exhausted` from the newest `.review-batch`, so `#review-done` (with its "Keep studying"
  button) reappears on its own once the expanded round is itself exhausted — the button stays
  clickable for another round each time.
- Nothing in `grade()`, `flush()`, `takePending()`, or the branch/preview code changes.

### 5. Docs

- `docs/routes.md` — the `GET /api/reviews/next` row: add the `extraRounds` query param (int,
  default 0, clamped server-side to `maxExtraRounds`) and one sentence on what it does (grants that
  many additional full daily presets for this fetch; never changes what is stored).
- `docs/architecture.md` §20 — amend the "Filtered / custom-study decks" row to record that a narrow
  slice (re-grant today's preset allowance for the rest of this study session, #172) now exists,
  while filtered decks proper still do not.

---

## Tests (CLAUDE.md §10, working rule 5)

§10's priority order puts the client-can't-write-scheduling-state test first; this change does not
touch that path, but two of the new tests exist to *prove* that.

### Must add

**`internal/review/batch_test.go`** (DB-backed; `beginTx` + `seedFixture` + `seedCards` helpers
already there, `testStudyDay` for a deterministic window):

- `TestBuildBatch_ExtraRoundServesOnePresetMore` — the `TestBuildBatch_AtLimitDueCardsStillFlow`
  fixture (introductions/reviews logged up to the deck's preset via real `gradeCards`, further
  unseen/due cards beyond it) with `extraRounds=1` serves up to one more preset's worth (not more)
  of the previously-capped cards — assert the count served matches `min(available, preset)`, not
  "all of them", to prove this is a bounded re-grant and not the old unlimited design.
  `extraRounds=2` on the same fixture (with enough further cards seeded) serves up to two presets'
  worth — proves the multiplier is `(1+extraRounds)`, not a flat bonus.
- `TestBuildBatch_ExtraRoundServesPastCombinedTotal` — the `TestBuildBatch_TotalAtLimitBlocksNewToo`
  fixture with `extraRounds=1` serves both new and review-state cards past the original `rev.perDay`,
  bounded by the inflated `rev.perDay*(1+extraRounds)`.
- `TestBuildBatch_ExtraRoundsZeroIsIdentical` — `extraRounds=0` produces byte-identical
  `newRemaining`/`totalRemaining`/results to calling the pre-#172 arithmetic directly, confirming
  every existing (unflagged) call path is unaffected.
- `TestBuildBatch_ExtraRoundStillExcludesReviewedToday` — a card graded earlier in the same study day
  stays excluded even under a large `extraRounds`. This is the one that keeps the re-grant from
  turning into "re-serve everything forever": the `last_review < study_day_start` predicate is a
  correctness filter, not a limit, and `extraRounds` only touches the limit.
- `TestBuildBatch_ExtraRoundStillExcludesSuspendedAndBuried` — same point for the suspended/buried
  predicates.
- `TestBuildBatch_PriorityMixed_ExtraRound` — the mixed path (`buildMixedBatch`) inflates both sides
  and still interleaves; guards against the `newRemaining`/`totalRemaining` values only reaching one
  of the two sub-queries.
- Mechanical: every existing `BuildBatch(...)` call in this file (~25 across
  `TestBuildBatch_*`) gains the new trailing argument (`0` for all pre-existing cases).
  `internal/review/preset_test.go`, `interleave_test.go`, `order_test.go`, `replay_test.go` do not
  call `BuildBatch` and need no change.

**`internal/http/review_test.go`** (DB-backed, `newTestHandler` + `doRequest` + `loginCookie`):

- `TestReviewNext_ExtraRoundsGrantsOnePreset` — deck at its cap: `/api/reviews/next?deck=…&cursor=`
  returns no cards and `data-exhausted="true"`; the same request with `&extraRounds=1` returns up to
  one more preset's worth of cards. Build on `TestReviewNext_KeysetAndExhaustion`'s
  `UPDATE decks SET preset = …` pattern to set a small `new.perDay`/`rev.perDay` rather than seeding
  200 cards.
- `TestReviewNext_ExtraRoundsClampedToMax` — `&extraRounds=999999` behaves identically to
  `&extraRounds=maxExtraRounds` (20) and does not error, overflow, or panic — the boundary-validation
  test for the client-controlled param.
- `TestReviewNext_ExtraRoundsNegativeOrGarbageIsZero` — `&extraRounds=-5` and `&extraRounds=abc`
  both behave identically to omitting the param.
- `TestReviewRoutes_AccessControl` — add a row for `/api/reviews/next?...&extraRounds=1` against a
  view-only and a no-access deck: still 404/403 as the un-flagged request is. §10.5 ("add a row on
  every new endpoint") applies to a new parameter that widens a result set just as much as to a new
  route.
- `TestReviewNext_ExtraRoundsIsSelectionOnly` (or an assertion folded into the first test) — after an
  extra-round fetch, the deck's `user_card_state` and `review_log` row counts for the test's own user
  are unchanged before any grading happens. Cheap, and it is the property Q3 rests on. Scope the
  counts to the user/deck the test created (CLAUDE.md §16 — no table-wide `count(*)`).
- Confirm no change is needed to `TestReviewBatch_ClientCannotWriteSchedulingState`: `extraRounds`
  never appears in the grade body. Worth a one-line comment in the new test saying so rather than a
  new assertion.

Run these with `DATABASE_URL` exported — a green `go test ./...` with it unset proves nothing here
(CLAUDE.md §16); confirm with `go test ./internal/review/... ./internal/http/... -v | Select-String
-Pattern skip`.

### Should not add

- No test for #173. It is a template-only change with no logic (working rule 5: skip for UI-only).
  If a cheap guard is wanted, the existing review-page test can assert the rendered `#review-done`
  contains both `href="/decks"` and `href="/decks/{id}"` — optional.
- `tests/e2e/review-grading.spec.ts` covers keyboard grading and the optimistic advance; extending
  it to drive the "Keep studying" button is reasonable but not required by §10.6, which asks for
  grading/optimistic-advance/network-failure coverage. Propose it, don't write it unprompted.

---

## Pre-commit (CLAUDE.md §14)

`go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` with `DATABASE_URL` set. No
migration (no schema change). Recommended review pass: `/code-review medium` — the diff is small
but touches the batch-fetch path, which §14 flags as cross-cutting.

## CHANGELOG

**Both issues need an entry, in one version bump** (§14: once per PR, all branch changes under one
version entry). Next version is `## [0.2.17]` — do not write it until the work is done and
approved.

- `### Added` — #172, one line, linking
  `https://github.com/Jolls/deckshare/issues/172`.
- `### Changed` — #173, one line, linking
  `https://github.com/Jolls/deckshare/issues/173`.

PR description must repeat the close keyword per issue:

```
Closes #172
Closes #173
```
