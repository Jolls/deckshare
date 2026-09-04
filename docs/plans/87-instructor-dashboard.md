# #87 — Instructor dashboard: per-student retention, due counts, lapse hotspots

Milestone 3 (architecture.md §11), the classroom layer. Sibling issue #86 (cohorts) is
**not** a prerequisite — see §0.2.

## 0. The decision this plan rests on

### 0.1 The rule #87 contradicts

`docs/schema.md` ("Access control at the query layer") and CLAUDE.md §9 both say:

> No cross-user reads without a `deck_access` row, and there are no exceptions. There is no
> visibility flag and no public-deck carve-out: a deck is reachable by exactly the users holding
> a row, and **no combination of that row's permission flags ever grants read of another user's
> `user_card_state`.** One authorisation path means one thing to get right and one thing to test.

An instructor dashboard *is* a cross-user read of `user_card_state` and `review_log`. #87 cannot
be built without amending that sentence, and CLAUDE.md §17 puts that amendment in the
"explicit, recorded decision" bucket rather than the "a handler can just do it" bucket.

**Decision: add a seventh `deck_access` flag, `can_view_progress`.** Alternatives considered and
rejected:

| | Shape | Why not |
|---|---|---|
| Cohort-scoped relation | A `(viewer, deck, cohort)` triple grants progress read of that cohort's members | Creates a *second* authorisation path, which is the thing the rule above exists to prevent. Also hard-blocks #87 on #86 |
| Student-side consent flag | `users.share_progress_with` | No one asked for opt-in; it strands instructors on non-consenting students, and it is a per-user global where the need is per-deck |

The flag keeps the rule's *rationale* intact — one join, `deck_access`, authorised in the query
layer — and changes only its blanket wording. The amended sentence becomes: no permission flag
grants read of another user's `user_card_state` **except `can_view_progress`, which grants
deck-scoped aggregate reads only** (§0.3).

### 0.2 What the flag decouples

Because authority comes from `deck_access` and not from cohort membership, **#87 does not depend
on #86.** An instructor grants `can_view_progress` the same way they grant `can_study` today, one
collaborator at a time on `/decks/{id}/access`. When #86 lands, a cohort becomes a *filter* on
the roster (`/decks/{id}/progress?cohort={id}`) and a bulk way to set the flag — neither is
load-bearing here. Update #87's "Depends on cohorts" line accordingly.

### 0.3 The scope of what the flag grants

Deliberately narrow, and this narrowness is the reason the amendment in §0.1 is defensible:

- **Aggregates only.** Per-student rollups over one deck. Never another user's individual
  `review_log` rows — answer-by-answer rating and timing on a named student is surveillance, and
  granting it is a separate decision nobody has asked for.
- **One deck.** The flag is a `deck_access` column, so it says nothing about the target's other
  decks. There is no cross-deck "student overview" page and no route that would need one.
- **No write.** Nothing on this page mutates `user_card_state` or `review_log`. Resetting a
  student's progress already exists behind `can_manage_access`
  (`POST /decks/{id}/access/{userId}/reset`, #189) and stays there.

### 0.4 Disclosure

The student's own deck page shows "Progress visible to N instructors" when anyone other than
themselves holds `can_view_progress` on the deck. It is ~5 lines and it is what makes the flag
defensible to the person being reported on; it is not optional polish.

## 1. Metrics

### 1.1 "Retention" is two numbers, and both are shown

The issue says "per-student retention (from FSRS state, not just review_log rollups)". That picks
one of two distinct quantities; the page shows both, labelled, because they answer different
questions and their disagreement is the informative part (high pass rate + low recall = the
student has stopped studying).

**Recall now** — mean predicted retrievability across the student's seen cards in this deck,
derived from `user_card_state.stability`, `.state`, and `.last_review`. A pure function of stored
state; no log aggregation. Answers "how much of this deck does this student currently hold?"

**Pass rate 30d** — `count(*) FILTER (WHERE rating > 1) / count(*)` over `review_log` rows for
this student's cards in this deck where `state_before = 2`, within the window. Answers "how are
they doing?" Review-state rows only, matching what `CountReviewedToday` already treats as a
review (`internal/db/queries/reviews.sql`).

**§2.5 is not violated.** The invariant forbids *materialising* rollups that stand in for the
append-only log. A windowed `SELECT … count(*)` is exactly the read the log exists to support —
the same shape `CountReviewedToday` and `CountNewIntroducedToday` already use. No new table, no
stored counter, no `DELETE` path.

### 1.2 Recall is computed in Go, not SQL

`go-fsrs` v4 exposes `(*FSRS).Retrievability(card Card, now time.Time) (float64, error)`
(`fsrs.go:67`), returning 0 for `New` cards and cards with no `LastReview`. It needs the engine,
so it needs that student's effective params for that deck.

The forgetting curve **must not** be reimplemented as a SQL expression. CLAUDE.md §17: the FSRS
package is the only place scheduling maths runs, precisely so no two call sites can disagree. A
SQL copy of the decay/factor arithmetic is a second implementation by any other name.

Consequence: the handler loads each student's `(user_id, card_id, stability, state, last_review)`
rows for the deck and folds them in Go, with one `review.EffectiveParams` lookup per student
(params are per `(user, deck)`; `review.ParamsCache` keys by deck and so is the wrong cache
here — resolve one `fsrs.Params` per student in the roster loop).

**Cost bound, and the mitigation.** This is `students × seen-cards` rows for one page: a
30-student roster on a 500-card deck is 15k narrow rows, which is fine. It is not fine for a
1000-student roster. The students table paginates (§2.2), and the per-card fetch is scoped to the
page of students actually being rendered — not the whole roster.

### 1.3 Due counts have a per-student day boundary

`CountQueueForDeck` takes one scalar `study_day_start` because it serves one caller. Here the
window differs per student: it is derived from `users.timezone` and `users.day_start_hour`
(docs/schema.md, `GetStudyDayWindow`). A roster-wide "due today" therefore needs the window CTE
computed **per student inside the grouped query**, not passed in.

`look_ahead_minutes` stays a scalar — one deck, one preset, read in Go via
`review.DueLookAheadMinutes(deck.Preset)` exactly as `internal/http/decks.go` already does.

### 1.4 Lapse hotspots use `review_log`, not `user_card_state.lapses`

`user_card_state.lapses` is a lifetime counter, seeded by `.apkg` import
(`internal/db/queries/import.sql`, `SeedImportedUserCardState`) and wiped by #189's reset
(`DeleteUserCardStateForDeck`). It does not mean "how often the cohort failed this card".

Use again-rate per `card_id` over the window: `count(*) FILTER (WHERE rating = 1) / count(*)`
across all students on the roster, restricted to `state_before = 2`, with a floor of **≥5 reviews
across ≥3 students** so one student's bad night cannot top the chart. Rank by again-rate desc,
tie-break by review count desc.

**`review_log.card_id` has no foreign key**, by decision (docs/schema.md, "Deletion policy"), so
rows survive their card. The join to `cards` must be INNER, which drops orphans from numerator
and denominator together.

## 2. UX

One deck-scoped, server-rendered page. No JS island — the reviewer is the only one
(docs/routes.md conventions), and nothing here needs one.

### 2.1 Route

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/decks/{id}/progress` | `can_view`, `can_view_progress` | Per-student retention, due counts, and cohort lapse hotspots for one deck |

Sorting is a `?sort=` query param, not client-side JS. `?cohort=` is reserved for #86 and is
ignored until it lands.

Entry points: a `Progress` button in `deck.html`'s `button-nav`, gated on
`{{if .Deck.CanViewProgress}}`, next to `Manage access`; and a link from `access.html`, which
already lists exactly the people this page reports on.

### 2.2 Layout

1. **Header** — deck name, roster size, and an "as of" timestamp. No live updating: the issue
   notes PubSub as a tradeoff of the stack not taken (docs/plans/architecture-reconsidered.md);
   with Go and server-rendered pages the answer is a page refresh, and this closes that thread
   rather than leaving it open.
2. **Students table**, one row per holder of `can_study` on the deck:

   `Student │ Seen │ New │ Learning │ Due │ Recall now │ Pass rate 30d │ Reviews 30d │ Last studied`

   Default sort **Recall asc** — weakest first, which is the instructor's actual question.
   Paginated (§1.2), reusing the `/decks/{id}` notes-list pagination shape (#90) rather than
   inventing a second one.
3. **Lapse hotspots panel** — top N cards by again-rate:

   `Card │ Students │ Reviews │ Again rate`

   Card label is the note's first field (`notes.fields`, a JSONB ordered array — `fields->>0`),
   truncated and escaped by `html/template`, never rendered through `internal/render`. Links to
   the note editor only when the viewer holds `can_edit_content`; plain text otherwise. The ≥5/≥3
   threshold is stated under the table so an empty panel reads as "not enough data yet" rather
   than "no problems".

### 2.3 Empty states

A student with no reviews shows `—` for Recall, Pass rate, and Last studied — **never `0%`**.
The difference between "hasn't started" and "is failing" is the entire value of the page, and a
zero in those columns destroys it.

### 2.4 Out of scope for v1

Each is a decision, not a backlog item:

- No per-student drilldown into individual `review_log` rows (§0.3).
- No live updating (§2.2).
- No cross-deck student overview (§0.3).
- No CSV export — cheap to add later, nothing here forecloses it.
- No charts. The numbers are a table; a time series would need a windowing decision this issue
  has not made.

## 3. Changes

### 3.1 Migration — `migrations/00018_deck_access_can_view_progress.sql`

```sql
-- +goose Up
-- The seventh deck_access permission (#87): read another user's aggregate progress on THIS deck.
-- The one exception to "no permission flag grants read of another user's user_card_state"
-- (docs/schema.md, CLAUDE.md §9) -- deck-scoped, aggregate-only, read-only
-- (docs/plans/87-instructor-dashboard.md §0.3).
-- No last-holder guard: unlike can_manage_access/can_delete, a deck with zero holders of this
-- flag is not stranded, it simply has no instructor view.
ALTER TABLE deck_access ADD COLUMN can_view_progress boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE deck_access DROP COLUMN can_view_progress;
```

`DEFAULT false` is the right backfill: no existing grant should silently start exposing progress.

### 3.2 `internal/db/queries/deck_access.sql` + `deletion.sql`

Every statement that lists the six flags gains a seventh:

- `GrantFullDeckAccess` — creator gets `can_view_progress` true with the rest (a personal deck is
  the trivial case; the owner viewing their own progress is a no-op, not a leak).
- `ListDeckAccessForDeck` — select the column so the access page can render its checkbox.
- `GrantDeckAccess` — new `sqlc.arg(can_view_progress)`.
- `UpdateDeckAccessRow` (deletion.sql) — new `SET` clause.

`CountDeckAccessHolders` is **unchanged** — the guard covers `can_manage_access` and `can_delete`
only, and `can_view_progress` cannot strand a deck.

Regenerate with `go generate ./internal/db/...` and commit the output unedited (CLAUDE.md §16).

### 3.3 `internal/db/queries/progress.sql` — new file, four queries

All authorise on the **viewer's** row, in the query, never in the handler alone (CLAUDE.md §9).
Sketches, not final SQL:

**`GetDeckForProgress :one`** — authorise-and-fetch, same shape and same no-row contract as
`GetDeckForAccessManage`:

```sql
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_view_progress
WHERE d.id = sqlc.arg(deck_id);
```

**`ListStudentProgressForDeck :many`** — the roster with per-student queue counts and the
`review_log` aggregates. Per §1.3 the study-day window is computed per student inside the query,
folding `GetStudyDayWindow`'s `date_trunc`/`make_interval` arithmetic into a CTE joined against
`users` for every roster member rather than taking a scalar arg. Roster = holders of `can_study`
on this deck. `look_ahead_minutes` is a scalar arg. Takes `limit`/`offset` for §2.2.

**`ListStudentCardStateForDeck :many`** — narrow rows for the recall fold in Go:
`(user_id, stability, state, last_review)` for the paged students' `user_card_state` on this
deck's cards. Ordered by `user_id` so the handler folds in one pass.

**`ListLapseHotspotsForDeck :many`** — §1.4. Grouped by `rl.card_id`, INNER JOIN `cards` and
`notes`, `HAVING count(*) >= 5 AND count(DISTINCT rl.user_id) >= 3`, selecting `n.fields->>0` as
the label. Restricted to roster members, so a non-`can_study` collaborator's reviews never leak
in.

### 3.4 `internal/fsrs` — `Retrievability`

```go
// Retrievability is the probability the user still recalls this card at now, on prior's own
// forgetting curve. Zero for a never-seen card and for one with no LastReview, matching the
// library. Pure, like everything in this package (CLAUDE.md §17): the caller maps the
// user_card_state row, this package never sees a database.
func Retrievability(p Params, prior CardState, now time.Time) (float64, error)
```

Thin wrapper over `p.engine().Retrievability(...)`, reusing the existing `toLibCard` mapping so
there is one `CardState → gofsrs.Card` conversion, not two. **Ships with a test** — CLAUDE.md §10
makes the FSRS package an unconditional exception to working rule 5.

### 3.5 `internal/http/progress.go` + `web/templates/progress.html`

New `registerProgressRoutes(mux, pool, pages, time.Now)` in `internal/http/http.go`, alongside
`registerAccessRoutes`. Handler shape follows `internal/http/decks.go`'s `GET /decks/{id}`:
`pathUUID` → authorise-and-fetch → `handleQueryErrPage` → counts → `render`. Register
`"progress"` in `parseTemplates`'s page list with
`pagePartials["progress"] = {"templates/back_to_deck.html"}`.

### 3.6 `internal/http/access.go` + `web/templates/access.html`

`accessFlags` and `flagsFromForm` gain `CanViewProgress` / `can_view_progress`. `access.html`
gains a `View progress` column in the checkbox grid and a checkbox in the grant form.

### 3.7 `web/templates/deck.html`

The `Progress` button (§2.1), and the disclosure line (§0.4). The latter needs a count of *other*
users holding `can_view_progress` on the deck — one small query, or an extra column on the
existing `CountDeckContents` call.

### 3.8 Docs

Several files hardcode "six":

- `docs/schema.md` — the flag table, the "six independent permissions" sentence above it, and the
  **amendment in §0.1** to "Access control at the query layer".
- `CLAUDE.md` §9 — the same "No cross-user reads… no exceptions" bullet.
- `docs/routes.md` — the Conventions bullet listing the six flags, plus a `Progress —
  progress.go (Milestone 3)` section for the new route.
- `migrations/00007_deck_access.sql`'s comment says "Six independent permissions" — it is an
  applied migration and **must not be edited** (CLAUDE.md §9, §17). The new migration's comment
  carries the correction instead.
- `docs/architecture.md` §20, a new row under **Forced by multiuser**:

  | | Anki | DeckShare |
  |---|---|---|
  | Another user's progress | No such concept — one collection is one user, so there is nobody else's progress to read | `deck_access.can_view_progress` grants deck-scoped, aggregate-only, read-only access to other users' `user_card_state`/`review_log` rollups (#87). Traces straight to the §2.1 seam: the instructor view exists only because a second user exists |

- `CHANGELOG.md` — one entry per PR (§14).

## 4. Tests

- **Access control (CLAUDE.md §10.5), the priority here.** Table-driven rows for the new flag,
  and specifically:
  - `can_manage_access` alone does **not** grant progress read.
  - `can_study` alone does not.
  - A viewer without `can_view` gets **404, not 403** — the existence-oracle rule
    (docs/schema.md), which `GetDeckForProgress`'s no-row contract delivers for free.
  - A student on the roster cannot read another student's row.
- **`internal/fsrs`** — `Retrievability` unit test: new card → 0, no `LastReview` → 0, a review
  card at `last_review` → ≈ 1, decaying below `desiredRetention` past the scheduled interval.
- **Metric correctness** — a DB-backed handler test seeding two students with known review
  histories and asserting the rendered Pass rate and Due counts. Per CLAUDE.md §16, scope every
  assertion to the rows the test itself created — no table-wide `count(*)`, no unscoped
  `LIMIT 1` (#134/#119/#108/#141).
- **Day-boundary** — two students in different `users.timezone`s with the same cards must show
  different Due counts at the same instant (§1.3). This is the query's whole reason for the
  per-student CTE and the easiest thing to get silently wrong.
- **Hotspot threshold** — a card with 4 reviews, or with 5 reviews from 2 students, stays out.

## 5. Open questions

1. **Should `can_view_progress` imply anything about `can_study`?** The roster is defined as
   `can_study` holders, so an instructor who holds `can_view_progress` without `can_study` sees a
   roster they are not on. That reads correctly and no enforcement is proposed — flagged only
   because it is the kind of thing a reviewer will ask about.
2. **Window length.** "30d" is asserted throughout above without evidence. It is one constant; if
   a term is 12 weeks, 90d may be the better default. Worth one line of confirmation before
   implementing, not worth blocking on.
3. **Hotspot thresholds (≥5 reviews, ≥3 students)** are a judgement call, not a derived number.
   They should be named constants in `progress.go`, not inlined in SQL, so they can move without
   a migration or a query edit.
