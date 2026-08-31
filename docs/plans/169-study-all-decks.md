# #169 — Study All: a single mixed-deck study session

## Context

Every study entry point today (`GET /decks/{id}/review`) is scoped to one deck. Issue #169 asks
for one "big study button" that draws a slice from *every* deck the user can study — each deck
contributing according to its own rules (priority, order, daily caps) — merges the slices, shuffles
them, and runs one ordinary review session over the mix.

An opus-high research pass (this session) found that the batch engine (`BuildBatch`,
`internal/review/batch.go`) is deck-scoped at six independent points — daily-limit counting,
the three keyset queries, and per-deck media/FSRS-params resolution (invariant §2.3: FSRS
params are per `(user, deck)`) — so a single cross-deck SQL query would silently ship previews
computed under the wrong deck's params, or resolve media filenames against the wrong deck. The
only safe merge point is *after* per-deck `Card` rendering: call the existing per-deck batch
path once per deck, concatenate the results.

Three design questions the research flagged as genuinely open were resolved with the user before
writing this plan:

1. **Session shape: one-shot**, not paged. Each deck's slice is pulled once at session start; no
   composite cursor, no changes to `review.Cursor`/refill.
2. **Pull size: reuse `rev.perDay`** as each deck's contribution ceiling (already clamped to
   what's actually left today by `BuildBatch`'s existing `effectiveLimit` logic) — no new
   `decks.preset` field, no migration.
3. **Participation: automatic** — every deck the user has study access (`can_view AND
   can_study`) to contributes, no new opt-in flag.

## Design

One new top-level route, `GET /study`, beside `GET /decks/{id}/review` in
`internal/http/review.go`'s `registerReviewRoutes`. Handler:

1. Look up the user's studyable decks via a new query (below).
2. For each deck, call the **existing, unchanged** `buildStudyBatch` helper
   ([review.go:140-159](internal/http/review.go#L140-L159)) with `cur = review.Cursor{AtStart:
   true}` and `limit = review.RevPerDay(deck.Preset)`. This is the reuse that keeps everything
   correct for free: `buildStudyBatch` already resolves per-deck FSRS params
   (`review.EffectiveParams`), the study-day window, and calls `BuildBatch` with that deck's own
   `NewPerDay`/`RevPerDay`/`ParseRevOrder`/`ParsePriority`/`DueLookAheadMinutes` — nothing new to
   get right per deck.
3. Concatenate every deck's `Batch.Cards`, shuffle once (`math/rand/v2` — no crypto need, this
   isn't the FSRS random-order seed), wrap as `Batch{Cards: merged, Cursor: "", Exhausted: true}`.
   One-shot per decision #1: no refill will ever fire (`review.js`'s `state.exhausted` gate at
   review.js:287 already suppresses the `refill-needed` dispatch when `Exhausted` is true), so no
   `Cursor`/`EncodeCursor` changes are needed anywhere.
4. CSS: call the existing `noteTypeCSS` helper ([review.go:163-174](internal/http/review.go#L163-L174))
   once per deck and concatenate the slices. Duplicate `<style>` blocks across decks are harmless
   (browser re-applies identical rules) — no dedup logic needed.
5. Render a new `study` page template with the merged batch + CSS.

The `review_cards` fragment (used for the actual card queue markup) is already deck-free and
needs no changes.

### New template: `web/templates/study.html`

Modeled on `review.html` but without single-deck assumptions:
- No `{{.Deck.Name}}` heading / `{{template "backToDeck" .Deck}}` — use the existing
  `backToDecks` partial (`web/templates/back_to_decks.html`) instead, both at the top and in the
  `#review-done` section.
- `<script src="/static/review.js" defer>` with no `data-deck-id` — confirmed harmless:
  `state.deckId` is only read to build the refill request (review.js:35, review.js:477), which
  never fires in a one-shot session.
- Same `#review-stage`, `#review-queue` (`{{template "review_cards" .Batch}}`), `#review-refill`
  (left wired identically for structural consistency, though it's inert since `Exhausted` is
  always true) as `review.html`.

### UI entry point

Add a "Study All" button to `web/templates/decks.html`'s existing `button-nav`
(decks.html:3-7, alongside "New deck"/"Note types"/"Import" — same `outline btn-sm` style),
linking to `/study`. Only show it when there's something to study: sum `LeftToStudy` across
decks the same way the per-row `Left` column already does
([decks.go:89-98](internal/http/decks.go#L89-L98)) and pass a `TotalLeft` value to the template.

## Files to touch

1. **`internal/db/queries/decks.sql`** — add `ListStudyableDecksForUser`, modeled on
   `GetDeckForSettingsEdit` (decks.sql:13-18) but joining `can_view AND can_study` and returning
   all decks (no `WHERE id = ...`):
   ```sql
   -- name: ListStudyableDecksForUser :many
   SELECT d.*
   FROM decks d
   JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                      AND da.can_view AND da.can_study
   ORDER BY d.name;
   ```
   Run `go generate ./...` and commit the regenerated `internal/db` output (CLAUDE.md §16 — never
   hand-edit generated files).

2. **`internal/http/review.go`** — new `mux.Handle("GET /study", ...)` inside
   `registerReviewRoutes`, implementing the Design section above. Small local shuffle helper
   (`shuffleCards` or inline) next to the handler.

3. **`internal/http/templates.go`** — add `"study"` to the page-name list (templates.go:32-37)
   and a `pagePartials["study"]` entry: `{"templates/review_cards.html",
   "templates/back_to_decks.html"}` (mirrors the existing `"review"` entry at templates.go:18,
   swapping `back_to_deck` for `back_to_decks`).

4. **`web/templates/study.html`** (new) — per Design section above.

5. **`web/templates/decks.html`** — add the "Study All" button; `internal/http/decks.go`'s
   `GET /decks` handler passes `TotalLeft` alongside the existing `Counts` map.

6. **`docs/routes.md`** — add the `GET /study` row to the Review routes table.

7. **`docs/architecture.md` §20 (deviations from Anki)** — add a row: this reverses two
   previously-written scope statements (architecture.md:859 "filtered/custom-study decks: not
   built", architecture.md:862 "cross-day interleaving stays unbuilt", and routes.md:202 "no
   filtered/custom-study deck routes"). Not an invariant violation — it doesn't touch scheduling
   state, identity, or auth — but CLAUDE.md invariant §2.10 requires not diverging silently from a
   recorded design statement, so it needs its own line explaining the "Study All" button is a
   fixed one-shot mix, not Anki-style filtered decks or persistent cross-deck queues.

8. **`CHANGELOG.md`** — one `### Added` entry under the current unreleased version, linking
   issue #169.

## Tests

New cases in `internal/http/review_test.go`, following existing helpers
(`setupOneCard`/`addNotes`/`gradeCard`, review_test.go:37-91):

- **Mixed pull correctness**: two decks with different `priority`/`rev.perDay` presets both
  contribute to one `GET /study` response, each capped at its own `rev.perDay` (or less, if
  partially consumed today).
- **Access control** (CLAUDE.md §10.5, mandatory for a new endpoint): a deck where the user has
  `can_view` but not `can_study` contributes zero cards.
- **Exhausted budget**: a deck with today's daily budget already spent contributes zero cards to
  the mix without erroring — exercises the existing `effectiveLimit` clamp this feature leans on.
- **Grading parity**: grading a card pulled via `/study` behaves identically to grading it from
  that deck's own `/decks/{id}/review` page (confirms the deck-agnostic grading path noted in
  research — `review_log`/`user_card_state` are keyed `(user_id, card_id)` with no deck column,
  invariant §2.1 — so no new risk, but worth one assertion since it's the invariant §2.7 path).

No changes needed to `internal/review` package tests (`batch_test.go`, `preset_test.go`) —
`BuildBatch` itself is untouched.

## Explicitly out of scope (per resolved decisions)

- No new `decks.preset` field, no migration.
- No composite `Cursor` / no refill paging for the mixed session.
- No opt-in "include in quick study" flag — participation is automatic.

## Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` — with `DATABASE_URL`
  set (CLAUDE.md §16: an unset `DATABASE_URL` silently skips every DB-backed test and the suite
  still reports `ok`).
- Manual (describe steps for the user, per CLAUDE.md — don't run the app to verify): create 2-3
  decks with distinct `priority`/`rev.perDay` settings and some due/new cards in each, click
  "Study All" from `/decks`, confirm the session mixes cards from all of them in shuffled order,
  grade a few, then revisit one contributing deck's own review page and confirm its daily
  remaining count reflects what was just graded from the mixed session.
