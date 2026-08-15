# Plan: #56 — The reviewer: batch fetch, refill, synchronous grading, server-authoritative `POST /api/reviews/batch`

Phase 1, build-order step 7 (architecture.md §11) — "the piece everything else is downstream of."
Full contract: architecture.md §6; route table: docs/routes.md "Review". Invariants CLAUDE.md §2.6
(client never waits) and §2.7 (client never believed) constrain nearly every decision below; every
place they do is marked **[§2.6]** / **[§2.7]**.

**Scope.** sqlc queries + regenerated output; the whole of `internal/review/`; `internal/http/review.go`
and its wiring; a static-asset route and the first vendored JS in this repo; `web/templates/review.html`
+ a hidden-card partial; the test set in §9. No new migration (schema is complete as of `00015`).

**Out of scope.** Media in the payload (architecture.md §6: deferred to #34/#60 — the payload carries no
media URLs). Deck due-counts on `GET /decks/{id}` (routes.md defers them to this step, but they are a
separate handler change; file a follow-up rather than growing this PR). Per-deck FSRS overrides UI
(step 9). Anki `preset`-driven daily caps (Open question 9). E2E Playwright (CLAUDE.md §10.6, separate issue).

---

## 0. Resolved decisions

Everything here is decided. Genuinely-open items are in **Open questions** at the end and nowhere else.

### 0.1 The refill trigger is client-computed; the server only reports exhaustion

architecture.md §6 is explicit: the queue module owns "unseen-count tracking" and "dispatches plain DOM
events (`refill-needed`, `card-graded`)"; the server's only sizing responsibility is `exhausted: true`.
So there is **no server-side "count unseen remaining" query**. `exhausted` is derived without an extra
query: `exhausted = len(rows) < requestedLimit`. Batch sizes: 20 initial, 20 per refill, client triggers
at `< 10` unseen.

### 0.2 One transaction per `POST /api/reviews/batch`, all advisory locks taken first, sorted

The handler opens one `store.Begin(ctx)`; `review.GradeBatch` runs inside it and the handler commits.
Order inside `GradeBatch`, exactly:

1. Validate + normalise every event (§0.6). Any malformed event ⇒ **the whole batch is 400 and nothing
   is written** (strict parse, architecture.md §6: "reject a malformed event rather than coercing it").
2. Authorise: `ListStudyableCards` for the batch's distinct card ids ⇒ map `card_id → deck_id`. Cards
   absent from the result are unauthorised or deleted; their events get `status: "forbidden"` and are
   dropped from further processing (§0.7).
3. Compute lock keys for the surviving `(user, card)` pairs, dedupe, **sort ascending**, and acquire each
   with `LockCardForGrade` in that order (architecture.md §6's deadlock rule).
4. `ListExistingReviewLogIDs` for every event id — **not scoped to `user_id`** (§6: `review_log.id` is a
   global PK). Matching events get `status: "duplicate"`; they are not rescheduled.
5. Sort the remaining events by `(reviewed_at, id)` and apply them one at a time (§0.5).
6. Return `[]Result`; handler commits and writes JSON.

Params are resolved per `deck_id`, cached in a `map[deckID]fsrs.Params` for the batch (a batch can span
decks; each deck may have its own override row).

### 0.3 Advisory-lock key: one `bigint`, FNV-1a over `user_id ‖ card_id`

Postgres advisory locks take integers, not UUIDs. Pinned derivation:

```go
// lockKey maps a (user, card) pair onto the single-bigint advisory-lock space. FNV-1a/64 over
// user_id's 16 bytes followed by card_id's 16 bytes, reinterpreted as a signed int64. Do NOT use
// UUID byte prefixes: these are UUIDv7, so the leading 48 bits are a timestamp and every user
// created in the same millisecond-range would share a key. A hash collision costs two unrelated
// pairs a shared lock -- needless serialisation, never a correctness bug. Nothing else in this
// codebase uses the bigint advisory space, so there is no cross-feature collision to reason about.
func lockKey(userID, cardID pgtype.UUID) int64
```

**Sort on the derived key, not on the `(user, card)` tuple.** Deadlock freedom needs a total order over the
*locks actually acquired*; under a (rare) hash collision, tuple order and key order can disagree and a
cycle becomes constructible. Sorting the keys makes it unconstructible by construction:

```go
// lockKeys returns the deduped, ascending lock keys for evs. Ascending *key* order, not (user,card)
// order, is what makes two overlapping batches deadlock-free (architecture.md §6).
func lockKeys(userID pgtype.UUID, evs []Event) []int64
```

`pg_advisory_xact_lock` is re-entrant within a session, so re-acquiring a key is harmless; locks release
at commit/rollback with no explicit unlock path.

### 0.4 `reviewedAt` clamp/reject policy (architecture.md §6 leaves the numbers open — pinned here)

Applied per event, before anything else touches it, against `now` = the handler's injected server clock:

| Condition | Action |
|---|---|
| not RFC 3339 with an offset, or absent | **malformed** ⇒ whole batch 400 |
| `reviewedAt > now + 5m` | event `status: "rejected"`, nothing written, batch still 200 |
| `now < reviewedAt ≤ now + 5m` | **clamp** to `now` (ordinary client-clock skew) |
| `reviewedAt < now - 30d` | event `status: "rejected"`, nothing written |
| otherwise | used as sent |

Reasoning to preserve in a comment: a future-timestamped review sorts ahead of everything else in a replay
and permanently corrupts `review_log`'s ordering guarantee for that card (§6, §2.5). 5 minutes covers real
skew; beyond it the clock is wrong, not skewed. The 30-day floor stops a clock stuck at epoch from injecting
history at the head of the log. `"rejected"` must be treated as **terminal** by the client's sender — it
must not retry a rejected event (§0.7).

**Every timestamp entering review logic is truncated to microseconds** (`t.Truncate(time.Microsecond)`)
before use. Postgres `timestamptz` is microsecond-resolution; without truncation the in-memory value and the
stored value differ in the nanosecond digits and the `last_review < $reviewedAt` guard and the idempotency
tests become non-deterministic. This applies to `reviewedAt`, to the clamped `now`, and to `Outcome.Due`.

### 0.5 Grading one event — the exact sequence

Given the lock is already held and the event is not a duplicate:

```
before := SELECT * FROM user_card_state WHERE user_id=$u AND card_id=$c   -- zero fsrs.CardState if absent
params := paramsFor(deck_of(card))

IN-ORDER  (before.LastReview is zero OR ev.ReviewedAt >= before.LastReview):
    after := fsrs.Schedule(params, before, ev.Rating, ev.ReviewedAt)          -- [§2.7] the authority
    InsertReviewLog(id=ev.ID, ..., state_before=before.State,
                    learning_steps_before=before.LearningSteps,
                    stability_before=before.Stability, difficulty_before=before.Difficulty,
                    elapsed_days_before=fsrs.ElapsedDays(before, ev.ReviewedAt),
                    scheduled_days_after=after.ScheduledDays,
                    fsrs_version=params.Version(), review_kind=reviewKind(before.State))
       -- rowcount 0 here means a concurrent inserter won the race: return "duplicate", write nothing else
    UpsertUserCardStateOnReview(after, last_review=ev.ReviewedAt)             -- guarded, see §0.9
    result.After := after

OUT-OF-ORDER  (ev.ReviewedAt < before.LastReview):
    rows   := ListReviewLogForCard(u, c)                       -- ordered (reviewed_at, id)
    merged := insert ev into rows at its (reviewed_at, id) position
    states := replayStates(params, merged)                     -- pure fold from the zero CardState
    priorForEv := states[indexOf(ev)]                          -- the truthful *_before for ev
    InsertReviewLog(... using priorForEv ...)
    UpsertUserCardStateFromReplay(final(states), last_review = max reviewed_at in merged)  -- unguarded
    result.After := final(states)
```

Load-bearing details:

- **Existing `review_log` rows are never rewritten** (append-only, §2.5; §6: "a row written *before* the gap
  became visible keeps the `*_before` the server genuinely held").
- The replay path uses the **current** params, not each row's stored `fsrs_version` — the stored version is
  what keeps the row *interpretable* later; a rebuild is by definition a rebuild under today's parameters.
- `*_before` numeric columns are always written (0 for a never-seen card). `NULL` in `stability_before` /
  `difficulty_before` / `fsrs_version` is reserved for imported history (migration `00011`).
- `anki_id` is never set by this path (NULL ⇒ no collision on `UNIQUE (user_id, card_id, anki_id)`).
- `review_kind` from `before.State`: New(0)→`0`, Learning(1)→`0`, Review(2)→`1`, Relearning(3)→`2` (mirrors
  Anki's `revlog.type`; see Open question 7).
- `durationMs` is stored as sent (after §0.6 validation); it is never derived.

### 0.6 Wire parsing — exactly five fields, extras ignored **[§2.7]**

```go
type wireEvent struct {
    ID         string  `json:"id"`
    CardID     string  `json:"cardId"`
    Rating     int     `json:"rating"`
    ReviewedAt string  `json:"reviewedAt"`
    DurationMs *int32  `json:"durationMs"`
}
type wireBatch struct{ Events []wireEvent `json:"events"` }
```

- Decode with a plain `json.NewDecoder(io.LimitReader(...)).Decode(&batch)`. **Do not call
  `DisallowUnknownFields`** and **never decode into `map[string]any`**: CLAUDE.md §10.1 says extra fields
  must be *ignored*, not rejected, and the top-priority test asserts a 200 with the extras dropped. A struct
  with exactly these five fields is the mechanism — there is no code path by which `stability` or `due` can
  reach a query argument.
- Validation (any failure ⇒ 400, nothing written): body ≤ 64 KiB (`http.MaxBytesReader`); `1 ≤ len(events) ≤ 100`;
  `id` and `cardId` parse as UUIDs; `rating` ∈ 1..4 (`fsrs.Rating.Valid()`); `reviewedAt` parses per §0.4;
  `durationMs` absent/`null`, or an integer in `[1, 3_600_000]` (schema comment: never 0 as a stand-in for
  unknown).

### 0.7 Response shape: 200 with a per-event status

```json
{"results":[
  {"id":"…","cardId":"…","status":"applied","after":{"due":"2026-08-18T09:12:03.123456Z","state":2,
   "stability":12.34,"difficulty":5.67,"reps":3,"lapses":0,"scheduledDays":4,"learningSteps":0,
   "lastReview":"2026-08-14T09:12:03.123456Z"}},
  {"id":"…","cardId":"…","status":"duplicate","after":{…current stored state…}},
  {"id":"…","cardId":"…","status":"forbidden"},
  {"id":"…","cardId":"…","status":"rejected"}
]}
```

`status` ∈ `applied | duplicate | forbidden | rejected`. Results are returned in **request order**, not
processing order. `duplicate` carries the current stored state so the client can still reconcile. Rationale
for per-event status over a whole-batch 403/404: a card deleted mid-session (note edit regenerates cards)
would otherwise wedge the client's pending queue into an infinite retry loop. See Open question 5.

Status codes: `200` on any parsed, authenticated request; `400` malformed body; `401` no session;
`405`/`404` from the mux.

### 0.8 `/api/reviews/*` returns 401, not `auth.RequireUser`'s 303

`auth.RequireUser` redirects to `/login` because "this is a form-based HTML app". That is wrong for the two
API routes: an expired session mid-review would answer an XHR with an HTML login page and 303, and the
graded events would be silently dropped. The two `/api/` handlers therefore do their own
`auth.UserFromContext` check and return `401` with `{"error":"unauthenticated"}`. `GET /decks/{id}/review`
keeps `auth.RequireUser`. Note this in `docs/routes.md`'s Conventions ("Auth" bullet).

### 0.9 Study-day window is computed by a query, taken once per request

`StudyDayStart`/`StudyDayEnd` do not exist anywhere yet — this issue adds them, as one sqlc query
(`GetStudyDayWindow`), **not** as a migration-level SQL function (sqlc's catalog handling of
`CREATE FUNCTION` is not worth the risk) and **not** in Go (schema.md: "Compute the queue window in the
query, not in the client"). The handler calls it once and passes both timestamps as parameters into the
queue query, so the expression exists in exactly one place.

The reviewer page and refill payload both carry `studyDayEnd` (architecture.md §6's session payload list).

### 0.10 Queue ordering and the keyset cursor

Order is `(COALESCE(ucs.due, 'infinity'), c.id)` — all due review cards by `due`, then never-seen cards by
`card_id`. `infinity` keeps the key total and non-NULL, which a row-comparison keyset requires (a NULL in a
row comparison filters the row out silently — the trap that would make refills lose every new card).

The cursor is **opaque** to the client: `base64url(strconv.FormatInt(unixMicros,10) + ":" + uuid)`, with
`unixMicros = math.MaxInt64` standing for `infinity`. This keeps `infinity` out of the DOM and out of the
query string. `review.EncodeCursor`/`review.DecodeCursor`; an undecodable cursor is a 400.

Filters, all in the queue query: `NOT COALESCE(ucs.suspended,false)`; `ucs.buried_until IS NULL OR
buried_until <= study_day_start::date`; `ucs.due IS NULL OR ucs.due < study_day_end`; and
`ucs.last_review IS NULL OR ucs.last_review < study_day_start` — that last one is the "exclude cards already
reviewed this study day" rule (§6), served from `user_card_state` rather than a `review_log` scan, because
every grade this codebase writes sets `last_review` in the same transaction.

### 0.11 Note-type CSS is emitted once per page, never per batch

`render.SanitiseCSS` runs once per distinct note type backing a card in the deck (`ListNoteTypeCSSForDeck`)
and the page emits one `<style>` block. Refill fragments therefore carry no CSS and cannot introduce a
style that arrives after the card that needs it. Every card wrapper carries `class="enshu-card"`
(`render.ScopeClass`) — mandatory, or the sanitised CSS's scoped selectors match nothing.

### 0.12 The initial batch and the refill fragment render from the same template

`web/templates/review_cards.html` defines `{{define "review_cards"}}` and is parsed into **both** the page
template set and a standalone fragment set. Markup drift between the inline batch and refills is exactly the
defect architecture.md §12 flags as the one real cost of choosing `html/template` over `templ`; sharing the
partial plus the §9.7 shape test is the mitigation that decision assumed.

---

## 1. Files

**New**

| File | Responsibility |
|---|---|
| `internal/db/queries/reviews.sql` | queue, study-day window, card authorisation, advisory lock |
| `internal/review/types.go` | `Event`, `Result`, `Status`, `CardStateDTO`, `Cursor` |
| `internal/review/lock.go` | `lockKey`, `lockKeys` (pure) |
| `internal/review/replay.go` | `replayStates` (pure), `ReplayCard` (DB) |
| `internal/review/batch.go` | `BuildBatch` — queue rows → rendered, previewed `Card`s |
| `internal/review/grade.go` | `GradeBatch`, `gradeEvent`, params cache, `reviewKind` |
| `internal/review/params.go` | `EffectiveParams` (row → `fsrs.Params`, defaults fallback) |
| `internal/http/review.go` | the three routes |
| `internal/http/static.go` | `GET /static/{path...}` |
| `web/static.go` | `//go:embed static` |
| `web/static/htmx.min.js` | vendored, version recorded |
| `web/static/htmx-ext-json-enc.js` | vendored, version recorded |
| `web/static/review.js` | the in-session queue module |
| `web/static/README.md` | vendored versions, upstream URLs, SHA-256, licences |
| `web/templates/review.html` | reviewer page shell |
| `web/templates/review_cards.html` | hidden-card partial (shared, §0.12) |
| `internal/http/review_test.go` | route tests §9.1–§9.9 |
| `internal/review/lock_test.go` | lock ordering (pure, `TestLockKeys` only — §9.11 dropped, resolved decision 12) |
| `internal/review/replay_test.go` | convergence / arrival-order tests |

**Changed**

| File | Change |
|---|---|
| `internal/db/queries/review_log.sql` | + `ListExistingReviewLogIDs`, `InsertReviewLog`, `ListReviewLogForCard` |
| `internal/db/queries/user_card_state.sql` | + `UpsertUserCardStateOnReview`, `UpsertUserCardStateFromReplay` |
| `internal/db/queries/user_fsrs_params.sql` | + `GetEffectiveFsrsParams` |
| `internal/db/queries/fields.sql` | + `ListFieldsForNoteTypes` |
| `internal/db/*.sql.go`, `models.go`, `querier.go` | regenerated — `go generate ./...`, never hand-edited (CLAUDE.md §16) |
| `internal/http/http.go` | wire `registerReviewRoutes`, `registerStaticRoutes`, `parseFragments` |
| `internal/http/templates.go` | page partials map + `parseFragments` + `renderFragment` |
| `internal/http/auth_test.go` | `newTestHandler` registers the new routes (and takes a clock) |
| `internal/review/doc.go` | expand the package doc to name the four concurrency mechanisms |
| `web/templates/layout.html` | `<script src="/static/htmx.min.js" defer>` + json-enc ext |
| `web/templates/deck.html` | "Study" link to `/decks/{id}/review` |
| `docs/routes.md` | mark Review built (#56); fix the refill row (HTML fragment, opaque cursor); add `/static/…`; note §0.8 |
| `docs/architecture.md` | §1 current state; §6 sender-shape amendment if Open question 4 lands on (a) |
| `CHANGELOG.md` | `[0.1.10]`, `### Added` |

---

## 2. sqlc queries — exact SQL

Run `go generate ./...` (i.e. `sqlc generate -f sqlc.yaml`) and **commit the generated output**; CI
re-runs it and fails on a diff.

### 2.1 `internal/db/queries/reviews.sql` (new)

```sql
-- The reviewer's queue (architecture.md §6). Every query here takes user_id and joins deck_access
-- (CLAUDE.md §9) -- review routes require can_view AND can_study.

-- The day boundary (docs/schema.md): a per-user rollover hour in the user's own timezone, not
-- midnight UTC. The arithmetic runs on the LOCAL wall clock, so a DST transition makes the study day
-- 23 or 25 hours long instead of silently shifting the rollover. `now` is a parameter, not now(), so
-- handler tests can pin the clock.
-- name: GetStudyDayWindow :one
WITH l AS (
    SELECT u.timezone AS tz,
           u.day_start_hour::int AS h,
           (sqlc.arg(now)::timestamptz AT TIME ZONE u.timezone) AS local_now
    FROM users u
    WHERE u.id = sqlc.arg(user_id)
), s AS (
    SELECT tz,
           date_trunc('day', local_now - make_interval(hours => h)) + make_interval(hours => h) AS start_local
    FROM l
)
SELECT (start_local AT TIME ZONE tz)::timestamptz                        AS study_day_start,
       ((start_local + interval '1 day') AT TIME ZONE tz)::timestamptz   AS study_day_end
FROM s;

-- name: GetDeckForStudy :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE d.id = sqlc.arg(deck_id);

-- The queue. Keyset over (COALESCE(due,'infinity'), card_id): due reviews first by due date, then
-- never-seen cards by id. 'infinity' (not NULL) keeps the sort key total -- a NULL inside the row
-- comparison below would silently drop every new card from every refill. Never-seen cards have no
-- user_card_state row at all, hence the LEFT JOIN and the COALESCEd columns.
-- name: ListDueCardsForStudy :many
SELECT c.id                                        AS card_id,
       c.ordinal                                   AS card_ordinal,
       COALESCE(ucs.due, 'infinity'::timestamptz)  AS queue_key,
       (ucs.user_id IS NULL)                       AS unseen,
       COALESCE(ucs.due, now())                    AS due,
       COALESCE(ucs.stability, 0)::double precision  AS stability,
       COALESCE(ucs.difficulty, 0)::double precision AS difficulty,
       COALESCE(ucs.state, 0)::smallint            AS state,
       COALESCE(ucs.reps, 0)::int                  AS reps,
       COALESCE(ucs.lapses, 0)::int                AS lapses,
       COALESCE(ucs.scheduled_days, 0)::int        AS scheduled_days,
       COALESCE(ucs.learning_steps, 0)::smallint   AS learning_steps,
       ucs.last_review                             AS last_review,
       n.fields                                    AS note_fields,
       n.tags                                      AS note_tags,
       nt.id                                       AS note_type_id,
       nt.name                                     AS note_type_name,
       nt.is_cloze                                 AS is_cloze,
       t.name                                      AS template_name,
       t.qfmt                                      AS qfmt,
       t.afmt                                      AS afmt
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
JOIN notes n       ON n.id = c.note_id
JOIN note_types nt ON nt.id = n.note_type_id
JOIN templates t   ON t.id = c.template_id
LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
WHERE c.deck_id = sqlc.arg(deck_id)
  AND NOT COALESCE(ucs.suspended, false)
  AND (ucs.buried_until IS NULL OR ucs.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
  AND (ucs.due IS NULL OR ucs.due < sqlc.arg(study_day_end)::timestamptz)
  AND (ucs.last_review IS NULL OR ucs.last_review < sqlc.arg(study_day_start)::timestamptz)
  AND (COALESCE(ucs.due, 'infinity'::timestamptz), c.id)
      > (sqlc.arg(cursor_due)::timestamptz, sqlc.arg(cursor_card_id)::uuid)
ORDER BY queue_key, c.id
LIMIT sqlc.arg(batch_size);

-- Note-type CSS for every card in the deck: sanitised once per page, never per card (#55's doc
-- comment), so a refilled card can never arrive before its styles.
-- name: ListNoteTypeCSSForDeck :many
SELECT DISTINCT nt.id, nt.css
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
JOIN notes n       ON n.id = c.note_id
JOIN note_types nt ON nt.id = n.note_type_id
WHERE c.deck_id = sqlc.arg(deck_id);

-- Per-card authorisation for a grade batch, which may span decks. A card missing from the result is
-- absent, invisible, or not studyable -- deliberately indistinguishable (docs/schema.md).
-- name: ListStudyableCards :many
SELECT c.id AS card_id, c.deck_id
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE c.id = ANY(sqlc.arg(card_ids)::uuid[]);

-- The per-(user,card) advisory lock, held to commit (architecture.md §6). Advisory rather than
-- SELECT ... FOR UPDATE because a never-seen card has no user_card_state row to lock, and two
-- concurrent first grades are exactly that case. The key is derived in Go -- see internal/review/
-- lock.go for the derivation and for why a batch's keys are acquired in ascending key order.
-- name: LockCardForGrade :exec
SELECT pg_advisory_xact_lock(sqlc.arg(key)::bigint);
```

### 2.2 `review_log.sql` (append)

```sql
-- The idempotency check (architecture.md §6). Deliberately NOT scoped to user_id: review_log.id is a
-- global primary key, so an id taken by anyone is taken here -- scoping it would let ON CONFLICT drop
-- the row while the state write landed.
-- name: ListExistingReviewLogIDs :many
SELECT id FROM review_log WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- Every column except id is computed server-side (CLAUDE.md §2.7). anki_id stays NULL for rows this
-- reviewer writes. execrows, not exec: 0 rows means the id was already taken and this is a pure retry,
-- which must NOT be rescheduled from the row it already advanced.
-- name: InsertReviewLog :execrows
INSERT INTO review_log (
    id, user_id, card_id, rating, reviewed_at, duration_ms,
    state_before, learning_steps_before, stability_before, difficulty_before,
    elapsed_days_before, scheduled_days_after, fsrs_version, review_kind
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(rating),
    sqlc.arg(reviewed_at), sqlc.narg(duration_ms),
    sqlc.arg(state_before), sqlc.arg(learning_steps_before),
    sqlc.arg(stability_before), sqlc.arg(difficulty_before),
    sqlc.arg(elapsed_days_before), sqlc.arg(scheduled_days_after),
    sqlc.arg(fsrs_version), sqlc.arg(review_kind)
)
ON CONFLICT (id) DO NOTHING;

-- The replay path (architecture.md §6); backed by review_log_card_id_user_id_reviewed_at_idx. Only
-- rating and reviewed_at are read: a replay re-derives every *_before itself.
-- name: ListReviewLogForCard :many
SELECT id, rating, reviewed_at
FROM review_log
WHERE user_id = sqlc.arg(user_id) AND card_id = sqlc.arg(card_id)
ORDER BY reviewed_at, id;
```

### 2.3 `user_card_state.sql` (append)

```sql
-- Last-write-wins by REVIEW time, not arrival time -- the property that makes a retrying sender safe
-- (architecture.md §6). suspended / buried_until / flag are user settings, not scheduling output, and
-- are never touched here.
-- name: UpsertUserCardStateOnReview :execrows
INSERT INTO user_card_state (user_id, card_id, due, stability, difficulty, state, reps, lapses,
                             elapsed_days, scheduled_days, learning_steps, last_review)
VALUES (sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(due), sqlc.arg(stability),
        sqlc.arg(difficulty), sqlc.arg(state), sqlc.arg(reps), sqlc.arg(lapses),
        sqlc.arg(elapsed_days), sqlc.arg(scheduled_days), sqlc.arg(learning_steps),
        sqlc.arg(last_review))
ON CONFLICT (user_id, card_id) DO UPDATE SET
    due = EXCLUDED.due, stability = EXCLUDED.stability, difficulty = EXCLUDED.difficulty,
    state = EXCLUDED.state, reps = EXCLUDED.reps, lapses = EXCLUDED.lapses,
    elapsed_days = EXCLUDED.elapsed_days, scheduled_days = EXCLUDED.scheduled_days,
    learning_steps = EXCLUDED.learning_steps, last_review = EXCLUDED.last_review
WHERE user_card_state.last_review IS NULL OR user_card_state.last_review < EXCLUDED.last_review;

-- The replay writer: unguarded, because a rebuild from review_log IS the newest truth for this card by
-- construction (architecture.md §6). Only internal/review.ReplayCard may call this.
-- name: UpsertUserCardStateFromReplay :exec
INSERT INTO user_card_state (...same columns...)
VALUES (...)
ON CONFLICT (user_id, card_id) DO UPDATE SET ...same assignments, no WHERE...;
```

### 2.4 `user_fsrs_params.sql` / `fields.sql` (append)

```sql
-- The per-(user,deck) override if there is one, else the user's global row. deck_id NULLS LAST puts
-- the override first (CLAUDE.md §2.3, §2.4: never one parameter set across a cohort).
-- name: GetEffectiveFsrsParams :one
SELECT fsrs_version, params, desired_retention
FROM user_fsrs_params
WHERE user_id = sqlc.arg(user_id) AND (deck_id = sqlc.arg(deck_id) OR deck_id IS NULL)
ORDER BY deck_id NULLS LAST
LIMIT 1;

-- name: ListFieldsForNoteTypes :many
SELECT note_type_id, ordinal, name FROM fields
WHERE note_type_id = ANY(sqlc.arg(note_type_ids)::uuid[])
ORDER BY note_type_id, ordinal;
```

---

## 3. `internal/review` — exact Go surface

`internal/review` is the only package allowed to map DB rows into `fsrs.CardState` and `fsrs.Outcome`
back into columns. `internal/fsrs` stays pure (CLAUDE.md §17): no import of `internal/db` is ever added
to it.

### 3.1 `types.go`

```go
type Status string
const (
    StatusApplied   Status = "applied"
    StatusDuplicate Status = "duplicate"
    StatusForbidden Status = "forbidden"
    StatusRejected  Status = "rejected"
)

// Event is exactly what the client is entitled to assert (CLAUDE.md §2.7, architecture.md §6):
// which card, which rating, when, and how long it took. Nothing else exists on this type, so no
// client-supplied scheduling value has anywhere to be stored.
type Event struct {
    ID         pgtype.UUID
    CardID     pgtype.UUID
    Rating     fsrs.Rating
    ReviewedAt time.Time // UTC, microsecond-truncated, already clamped (§0.4)
    DurationMs *int32
}

type Result struct {
    ID     pgtype.UUID
    CardID pgtype.UUID
    Status Status
    After  *CardStateDTO // nil for forbidden/rejected
}

// CardStateDTO is the JSON shape of <after>, and the source of the four data-* branch attribute
// sets on a hidden card. One type, so the preview the client applies and the state the server
// returns can never describe a card differently.
type CardStateDTO struct {
    Due           time.Time  `json:"due"`
    State         uint8      `json:"state"`
    Stability     float64    `json:"stability"`
    Difficulty    float64    `json:"difficulty"`
    Reps          int32      `json:"reps"`
    Lapses        int32      `json:"lapses"`
    ScheduledDays int32      `json:"scheduledDays"`
    LearningSteps int16      `json:"learningSteps"`
    LastReview    *time.Time `json:"lastReview,omitempty"`
}

type Cursor struct {
    Due    time.Time // zero means -infinity (start of queue); MaxCursorDue means +infinity
    CardID pgtype.UUID
}
func EncodeCursor(c Cursor) string
func DecodeCursor(s string) (Cursor, error) // "" => start of queue
```

### 3.2 `lock.go`

`lockKey`, `lockKeys` as in §0.3, plus:

```go
// acquireLocks takes every lock the batch needs, in ascending key order, before touching any row.
func acquireLocks(ctx context.Context, q *db.Queries, keys []int64) error
```

### 3.3 `replay.go`

```go
// LoggedReview is one review_log row reduced to the two columns a replay reads.
type LoggedReview struct {
    ID         pgtype.UUID
    Rating     fsrs.Rating
    ReviewedAt time.Time
}

// replayStates folds rows (which MUST be sorted by (reviewed_at, id)) through fsrs.Schedule from the
// zero CardState. priors[i] is the state the server would have held immediately before rows[i] --
// which is the only truthful source of a *_before for a review arriving out of order (architecture.md
// §6: fabricating one writes permanently wrong training data that no recompute repairs). final is the
// state after the last row. Pure: no DB, no clock.
func replayStates(p fsrs.Params, rows []LoggedReview) (priors []fsrs.CardState, final fsrs.CardState, err error)

// ReplayCard rebuilds user_card_state for (user, card) from the card's full review_log history and
// writes it. This is the server-side recompute path CLAUDE.md §17 forbids deleting as unused: live
// grading's out-of-order branch calls it, and import backfill (#8), parameter refits (§11 step 10),
// and post-incident repair are its other callers. Must run inside a transaction it does not own,
// under the (user, card) advisory lock; the caller commits.
func ReplayCard(ctx context.Context, tx pgx.Tx, p fsrs.Params, userID, cardID pgtype.UUID) (fsrs.CardState, error)
```

### 3.4 `batch.go`

```go
type Card struct {
    CardID   pgtype.UUID
    Unseen   bool
    Question template.HTML // sanitised by render, {{type:Field}} widget already spliced
    Answer   template.HTML
    Preview  fsrs.Preview  // all four branches [§2.6] -- the whole reason the client never waits
    Prior    CardStateDTO
}

type Batch struct {
    Cards       []Card
    Cursor      string // opaque; "" when Exhausted
    Exhausted   bool
    StudyDayEnd time.Time
}

// BuildBatch fetches up to limit due cards after cursor and precomputes all four rating outcomes for
// each (CLAUDE.md §2.6: no FSRS ever runs in the browser). now is the fetch instant; the client's
// grade will be recomputed server-side at its own reviewedAt, so a branch going stale as wall-clock
// advances is expected (architecture.md §6), not a bug.
func BuildBatch(ctx context.Context, q db.DBTX, p fsrs.Params, userID, deckID pgtype.UUID,
                window StudyDay, cur Cursor, limit int32, now time.Time) (Batch, error)
```

Body, in order: `ListDueCardsForStudy` → collect distinct `note_type_id`s → `ListFieldsForNoteTypes`
→ per row: unmarshal `note_fields` jsonb into `[]string`, zip with field names by ordinal into
`render.Note{Fields, Tags, NoteType, Deck, Subdeck}` (Subdeck = last `::` component of the deck name)
→ `render.RenderCard(tmpl, note, ordinal, isCloze)` → `render.TypeAnswerInput(card.Question)` /
`render.TypeAnswerExpected(card.Answer)` → `fsrs.PreviewAll(p, prior, now)`. A render error on one card
fails the batch (a card that cannot render cannot be graded honestly).

### 3.5 `grade.go`

```go
// GradeBatch is the whole of POST /api/reviews/batch's server side (architecture.md §6). It must run
// inside a transaction it does not own -- the advisory locks it takes are held to that transaction's
// commit, which is what makes a concurrent grade of the same card wait rather than read a stale
// `before`. The caller commits.
//
// The four concurrency mechanisms, all here: locks acquired in sorted key order (deadlock avoidance),
// events applied in reviewed_at order, events already in review_log skipped, and a card whose stored
// last_review postdates the event replayed from review_log instead of scheduled forward.
func GradeBatch(ctx context.Context, tx pgx.Tx, userID pgtype.UUID, now time.Time, evs []Event) ([]Result, error)

// reviewKind maps the prior FSRS state onto review_log.review_kind (Anki revlog.type: 0 learning,
// 1 review, 2 relearning, 3 cram, 4 manual). Our reviewer never writes 3 or 4.
func reviewKind(s fsrs.State) int16
```

### 3.6 `params.go`

```go
// DefaultDesiredRetention matches go-fsrs's own default request retention; it is what a user with no
// user_fsrs_params row gets (architecture.md §11 step 10 defers fitting entirely).
const DefaultDesiredRetention = 0.9

// EffectiveParams resolves the (user, deck) override, else the user's global row, else library
// defaults. An empty params array means "use the library defaults" (migration 00012).
func EffectiveParams(ctx context.Context, q db.DBTX, userID, deckID pgtype.UUID) (fsrs.Params, error)
```

---

## 4. `internal/http/review.go`

```go
func registerReviewRoutes(mux *http.ServeMux, store db.Beginner,
                          pages, fragments map[string]*template.Template, now func() time.Time)
```

`NewHandler` passes `time.Now`; tests pass a fixed clock (this is what makes §9.1 and §9.4 deterministic).

### 4.1 `GET /decks/{id}/review` — `can_view` + `can_study`

`auth.RequireUser` → `pathUUID(r,"id")` → `GetDeckForStudy` (`pgx.ErrNoRows` ⇒ `notFound`, never 403) →
`GetStudyDayWindow` → `EffectiveParams` → `BuildBatch(limit 20, cursor "")` → `ListNoteTypeCSSForDeck`
+ `render.SanitiseCSS` per note type → `render(w, pages["review"], 200, data)`.

Template data: `User`, `Deck`, `CSS []template.CSS`, `Batch` (cards, cursor, exhausted),
`StudyDayEnd`, `DesiredRetention`, `FsrsVersion`.

### 4.2 `GET /api/reviews/next?deck={uuid}&cursor={opaque}` — `can_view` + `can_study`

No `RequireUser` (§0.8). Answers an **HTML fragment**, not JSON: `renderFragment(w, fragments["review_cards"], 200, data)`
— the same partial the page uses, so htmx's `hx-swap="beforeend"` appends identical markup (§0.12).
Bad/absent `deck` or undecodable `cursor` ⇒ 400. No access ⇒ 404. Limit is fixed at 20 server-side; the
client does not choose it.

### 4.3 `POST /api/reviews/batch` — `can_view` + `can_study`

No `RequireUser` (§0.8). CSRF: the central `auth.Middleware` Origin check already covers it — htmx uses
XHR, and browsers set `Origin` on every non-safe-method request, so no per-handler work and no token.

Parse per §0.6 → `store.Begin` → `review.GradeBatch(ctx, tx, user.ID, now(), events)` → `tx.Commit` →
`w.Header().Set("Content-Type","application/json")` → encode `{"results":[…]}` per §0.7. Any DB error
after parse ⇒ 500 with rollback (deferred `tx.Rollback`), so a client retry is safe and lands nothing twice.

### 4.4 `internal/http/static.go` + `web/static.go`

```go
// GET /static/{path...} -- vendored htmx and the reviewer's queue module. Public (no session): these
// are the same bytes for everyone, and the login page loads htmx too.
mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))
```

`fs.Sub(web.Static, "static")` + `http.FileServerFS`, wrapped to set
`Cache-Control: public, max-age=3600`. No directory listing concern (the embed contains only the three
files + README).

`web/static.go`:
```go
//go:embed static
var Static embed.FS
```
(New var in the existing `web` package; `web/embed.go` keeps `Templates` unchanged.)

### 4.5 `internal/http/templates.go`

- `pagePartials = map[string][]string{"review": {"templates/review_cards.html"}}`; the parse loop appends
  these to the `ParseFS` file list.
- `parseFragments()` returns `map[string]*template.Template` with `"review_cards"` parsed standalone.
- `renderFragment(w http.ResponseWriter, t *template.Template, status int, name string, data any)` —
  buffer, `ExecuteTemplate(name)`, then status + write (same failure posture as `render`).

---

## 5. Templates

### 5.1 `web/templates/review_cards.html` — the shared partial

```
{{define "review_cards"}}
<div class="review-batch" data-cursor="{{.Batch.Cursor}}" data-exhausted="{{.Batch.Exhausted}}">
  {{range .Batch.Cards}}
  <article hidden class="enshu-card card" data-card-id="{{.CardID}}" data-unseen="{{.Unseen}}"
           data-again-due="…" data-again-state="…" data-again-scheduled-days="…"
           data-hard-due="…"  data-hard-state="…"  data-hard-scheduled-days="…"
           data-good-due="…"  data-good-state="…"  data-good-scheduled-days="…"
           data-easy-due="…"  data-easy-state="…"  data-easy-scheduled-days="…">
    <div class="card-question">{{.Question}}</div>
    <div class="card-answer" hidden>{{.Answer}}</div>
  </article>
  {{end}}
</div>
{{end}}
```

Exactly three attributes per branch — `due` (RFC 3339), `state` (0–3), `scheduled-days` — which is
everything the client needs for the interval label and the requeue decision, and nothing it could
mistake for something to submit. **No `predicted` block, and nothing here is ever sent back**
(architecture.md §6: there is no `predicted` field on the wire) **[§2.7]**. Dates are formatted in Go
(`time.RFC3339Nano`), not by the template.

`class="enshu-card"` is mandatory (`render.ScopeClass`). `.Question`/`.Answer` are `template.HTML` and
emit unescaped — that is the entire contract of `render.Rendered` (#55).

### 5.2 `web/templates/review.html` — the page shell

Blocks, in order:

1. `<style>` per sanitised note-type CSS blob.
2. `<section id="review-stage">` — the visible current card (question/answer are copied in from the
   hidden node by the queue module), a reveal button, and four rating buttons
   `<button data-rating="1..4">` with a `<small data-interval-for="1..4">` for the branch's interval
   label. Buttons are plain — they carry no `hx-post` (§0.13 / Open question 4).
3. `<div id="review-queue">{{template "review_cards" .}}</div>` — the initial 20, inline in the page
   response. No separate request for card 1 (§6) **[§2.6]**.
4. `<div id="review-refill" hidden hx-get="/api/reviews/next" hx-trigger="refill-needed from:body"
   hx-vals='js:{deck: enshuReview.deckId(), cursor: enshuReview.cursor()}'
   hx-target="#review-queue" hx-swap="beforeend"></div>`
5. `<div id="review-sender" hidden hx-post="/api/reviews/batch" hx-ext="json-enc"
   hx-trigger="flush-events from:body" hx-vals='js:{events: enshuReview.takePending()}'
   hx-swap="none"></div>`
6. `<section id="review-done" hidden>` — end-of-session panel, shown when the queue empties and the
   newest batch says `data-exhausted="true"`.
7. `<script src="/static/review.js" defer data-deck-id="{{.Deck.ID}}" data-study-day-end="…"></script>`

htmx owns exactly the two network-touching elements (4) and (5); the queue module never touches the
network itself (§6).

---

## 6. `web/static/review.js` — the queue module

Vanilla ES2020, no build step, no imports, one IIFE exposing `window.enshuReview`. Responsibilities,
each traceable to architecture.md §6:

- **Queue ownership.** On load and on `htmx:afterSwap` for `#review-queue`, index every
  `article[data-card-id]` into an in-memory array; dedupe by `cardId` (a graded card whose new `due`
  lands it back inside the refill window must not appear twice).
- **Advance.** Show the current card's question into `#review-stage`; reveal on `Space`/click; grade on
  keys `1`–`4` or the buttons.
- **Grading, synchronously [§2.6].** Read `data-{again|hard|good|easy}-*` for the pressed rating; apply
  to the in-memory queue; advance the UI. No `await`, no network call, and no FSRS arithmetic anywhere
  in this path.
- **Learning-steps heuristic (cosmetic, never written anywhere).** Requeue the card in-session iff the
  branch state is `1` (Learning) or `3` (Relearning) **and** the branch's `due` is within 20 minutes of
  now; insert it `max(3, cardsDueBefore(branchDue))` positions ahead. Otherwise drop it from the session.
- **Unseen tracking + refill.** Maintain a count of queued cards never graded this session (`data-unseen`
  plus a local `graded` set). After each grade, if `unseen < 10` and the newest batch is not exhausted
  and no refill is in flight, `document.body.dispatchEvent(new CustomEvent('refill-needed'))`.
- **Event production.** Build `{id: uuidv7(), cardId, rating, reviewedAt: new Date().toISOString(),
  durationMs}` — `durationMs` measured from reveal-to-grade — push onto `pending`, dispatch
  `card-graded` (the documented DOM event), then schedule a flush: immediately if none has gone out in
  the last 2 s, else a 2 s timer. Flush = `document.body.dispatchEvent(new CustomEvent('flush-events'))`.
  Also flush on `pagehide` and on `visibilitychange → hidden`.
- **`uuidv7()`** — ~12 lines: 48-bit big-endian `Date.now()`, 4-bit version `7`, 2-bit variant `0b10`,
  rest from `crypto.getRandomValues`. `crypto.randomUUID()` is v4 and is *not* a substitute (schema.md
  specifies UUIDv7 ids). Monotonicity within a millisecond is not required.
- **`takePending()`** — returns and clears `pending`, stashing the taken array in `inFlight` so a failure
  can put it back. Called synchronously by htmx's `hx-vals js:`.
- **Retry with backoff.** On `htmx:afterRequest` for `#review-sender`: success ⇒ apply each result's
  `after` to the in-memory card (cosmetic reconciliation), drop `inFlight`, reset backoff; network error
  or status ≥ 500 ⇒ unshift `inFlight` back onto `pending` and re-dispatch `flush-events` after
  1/2/4/8/16/30 s; status 400 or a per-event `"rejected"`/`"forbidden"` ⇒ **drop those events
  permanently** and `console.error` (they can never succeed, and retrying forever would wedge the queue).
- **Exhaustion.** Queue empty + newest `.review-batch[data-exhausted="true"]` ⇒ show `#review-done`.

Nothing in this file computes an FSRS value, and nothing it sends contains one **[§2.7]**.

`web/static/README.md` records, per vendored file: upstream URL, exact version, SHA-256, and licence
(htmx is 0BSD — the notice must be preserved in-repo alongside this AGPL project's own `LICENSE`).

---

## 7. Wiring

`internal/http/http.go`:

```go
fragments, err := parseFragments()
…
registerStaticRoutes(mux)
registerReviewRoutes(mux, pool, pages, fragments, time.Now)
```

`internal/http/auth_test.go`'s `newTestHandler` gains the same two registrations and takes an optional
clock (default `time.Now`), so existing tests are unaffected.

---

## 8. Implementation order

1. `internal/db/queries/*.sql` → `go generate ./...` → commit generated `.sql.go`, `models.go`,
   `querier.go`. Verify with `goose -dir migrations postgres "$DATABASE_URL" up` on a fresh DB.
2. `internal/review/types.go`, `lock.go`, `replay.go` (pure parts) + their unit tests — no HTTP yet.
3. `internal/review/params.go`, `batch.go`, `grade.go`.
4. `internal/http/review.go`, `static.go`, `templates.go` changes, `http.go` wiring.
5. `web/templates/review*.html`, `web/static/*` (vendor htmx, then write `review.js`).
6. Tests §9.
7. `docs/routes.md`, `docs/architecture.md` §1, `CHANGELOG.md` `[0.1.10]`, tag.

---

## 9. Tests

### 9.1 `TestReviewBatch_ClientCannotWriteSchedulingState` — CLAUDE.md §10.1, the highest-priority test in the repo

Fixture: user, deck, note type, one note ⇒ one card. Fixed clock. POST a body containing the five legal
fields **plus** `"stability": 9999, "difficulty": 1, "due": "2099-01-01T00:00:00Z", "state": 2,
"reps": 500, "lapses": 7, "scheduledDays": 9999, "elapsedDays": 5, "learningSteps": 3,
"fsrsVersion": 1, "ankiId": 42, "after": {"stability": 9999}`.

Asserts:
- status 200 (extras are *ignored*, not rejected — that is the §10.1 wording);
- the response `after` equals `fsrs.Schedule(params, fsrs.CardState{}, rating, clock)` computed
  independently in the test;
- `user_card_state` matches that `Outcome` field-for-field and contains **no** injected value;
- `review_log` has `state_before = 0`, `stability_before = 0`, `difficulty_before = 0`,
  `elapsed_days_before = 0`, `scheduled_days_after` = the computed value, `fsrs_version = 6`,
  `review_kind = 0`, `anki_id IS NULL`, `duration_ms` = the sent value;
- a belt-and-braces scan: `SELECT count(*) FROM user_card_state WHERE stability = 9999 OR reps = 500` = 0,
  same for `review_log`.

If this ever fails, per CLAUDE.md §10.1: stop and fix before anything else.

### 9.2 `TestReviewBatch_Idempotency` — CLAUDE.md §10.4

Three sub-cases, each asserting identical final `user_card_state` and exactly one `review_log` row per id:
(a) the same batch sent twice; (b) a two-event batch sent shuffled vs. sorted; (c) `[e1]`, then `[e2]`,
then `[e1]` replayed after `e2` already landed.

### 9.3 `TestReviewBatch_OutOfOrderReplay`

Grade `e2` (t+10m) first, then `e1` (t+0) in a second request. Asserts: both rows in `review_log`;
`e2`'s `state_before` is **unchanged** by the later replay (append-only); `e1`'s `state_before` is the
replayed prior (`New`); final `user_card_state` equals an independent `replayStates` fold over
`[e1, e2]`; and equals the state produced by a control card graded `e1` then `e2` in order.

### 9.4 `TestReviewBatch_ReviewedAtClampAndReject`

Table: `now+30s` ⇒ applied with `reviewed_at == now`; `now+4m` ⇒ applied, clamped; `now+1h` ⇒
`"rejected"`, zero rows written; `now-40d` ⇒ `"rejected"`, zero rows written.

### 9.5 `TestReviewBatch_MalformedIs400`

Table: `rating: 0`, `rating: 5`, missing `cardId`, `reviewedAt: "yesterday"`, `durationMs: -1`,
`durationMs: 99999999`, `events: []`, 101 events, 100 KiB body. Each ⇒ 400 and
`count(review_log) == 0` — including when the bad event is the *second* of two (nothing partially applied).

### 9.6 `TestReviewRoutes_AccessControl` — CLAUDE.md §10.5

Table over the three routes × {no session, stranger, `can_view` but not `can_study` (flip the flag with
raw SQL on the test tx), owner}. Expected: `GET /decks/{id}/review` ⇒ 303 / 404 / 404 / 200;
`GET /api/reviews/next` ⇒ 401 / 404 / 404 / 200; `POST /api/reviews/batch` ⇒ 401 / 200-with-`forbidden`
/ 200-with-`forbidden` / 200-with-`applied`, plus `count(review_log) == 0` for the two forbidden cases.
Add the same rows to the repo's running access-control table.

### 9.7 `TestReviewPage_HiddenCardShape`

Asserts the page body contains an `<article hidden … class="…enshu-card…" data-card-id="…">` carrying
all twelve `data-*` branch attributes, and asserts the refill fragment for the same card is
byte-identical in its attribute set (the drift guard architecture.md §12 asked for).

### 9.8 `TestReviewNext_KeysetAndExhaustion`

25 cards: page 1 returns 20 with `data-exhausted="false"`, page 2 returns the remaining 5 with
`data-exhausted="true"`, and the two sets are disjoint. Includes a mix of never-seen and due cards to
exercise the `infinity` sort key.

### 9.9 `TestReviewNext_ExcludesCardsReviewedThisStudyDay`

Grade a card, refill ⇒ absent. Advance the injected clock past the user's rollover hour ⇒ present again.
Also a case with `day_start_hour = 4` and a "23:30 local" review, asserting it counts as the *previous*
study day.

### 9.10 `TestLockKeys` (pure, `internal/review`)

Table: unsorted + duplicated events ⇒ ascending, deduped keys; two overlapping batches listed in
opposite order ⇒ identical key order for their shared subset (the deadlock-freedom property, asserted
without a database).

### 9.11 Cut from scope

`TestGradeBatch_ConcurrentOverlappingBatches` (a real two-connection DB test of the advisory-lock
deadlock-avoidance path) is **not implemented** — resolved decision 12 above. `TestLockKeys` (§9.10) is
the only coverage for the lock-ordering logic; the actual blocking/re-entrant behaviour of
`pg_advisory_xact_lock` is trusted, not tested, for this issue.

### 9.12 `TestReplayCard_ArrivalOrderConverges`

Insert the same five `review_log` rows in several permutations across separate cards, call `ReplayCard`
on each, assert identical `user_card_state` — the pure-DB complement to §9.3.

CLAUDE.md §10.2 is already covered at the pure-function level by `internal/fsrs/consistency_test.go`;
§9.1's "response `after` == independently computed `fsrs.Schedule`" is the route-level echo of it and
is all this issue needs to add.

---

## 10. Anticipated traps

1. **Microsecond truncation** (§0.4). Skip it and `last_review < $reviewedAt` behaves differently in
   memory and in the DB, and §9.2 flakes.
2. **NULL in a keyset row comparison** silently drops rows — hence `COALESCE(…, 'infinity')` (§0.10).
3. **sqlc + `LEFT JOIN` + `sqlc.embed`**: don't. Embedding a nullable side generates non-nullable fields
   that fail to scan. Explicit `COALESCE`d columns plus an `unseen` boolean (§2.1).
4. **`ON CONFLICT (id) DO NOTHING` needs `:execrows`**, not `:exec` — the rowcount *is* the idempotency
   signal (§2.2).
5. **Advisory locks inside the test harness's savepoint** are held until the outer test transaction ends,
   not until the savepoint releases. Harmless (they are re-entrant per session), but it means the
   single-connection harness can never observe blocking — which is why §9.11 exists separately.
6. **`durationMs` must never be coerced to 0** to mean "unknown" (migration `00011` comment).
7. **`render.ScopeClass`** on the card wrapper, or every sanitised CSS rule silently matches nothing.
8. **`{{type:Field}}`** must be spliced with `TypeAnswerInput`/`TypeAnswerExpected` in `BuildBatch`,
   after sanitisation, never by the template.
9. **`hx-vals='js:…'` runs synchronously per request** — `takePending()` must be side-effect-safe if
   htmx re-evaluates it (guard with the `inFlight` slot).
10. **`hx-trigger` `delay:` resets on every event**, so continuous grading would never flush. The JS owns
    the flush cadence and htmx just listens for `flush-events` (§6 of this plan / §5.1 item 5).

---

## 11. Verification

`go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` with `DATABASE_URL` set;
`goose -dir migrations postgres "$DATABASE_URL" up` on a fresh DB; `sqlc generate` produces no diff.
Manual steps for the user (CLAUDE.md §14: don't run the app to verify): create a deck with ~25 notes,
open `/decks/{id}/review`, grade with the keyboard through the refill boundary with DevTools throttled,
confirm the UI never blocks on a request, kill the network for 10 s mid-session and confirm events land
after it returns, and confirm `review_log` row count equals grades made.

---

## Resolved decisions (formerly open questions)

All resolved with the user before implementation; zero judgment calls remain.

1. **`reviewedAt` clamp/reject tolerances — RESOLVED: plan default.** Clamp when `≤ now+5m` (down to
   `now`), reject (nothing written, event `status: "rejected"`) when `> now+5m` or `< now−30d`, use as
   sent otherwise. The 30-day floor is a backstop against a grossly-wrong clock (e.g. stuck at epoch)
   injecting bogus history at the head of `review_log`, not a limit expected to trigger in normal use.
2. **Advisory-lock integer-key derivation — RESOLVED: plan default.** FNV-1a/64 over
   `user_id ‖ card_id` ⇒ one `bigint`, as specified in §0.3.
3. **Refill response format — RESOLVED: plan default.** HTML fragment (`renderFragment` on the shared
   `review_cards` partial), matching architecture.md §6's htmx wiring. Correct routes.md's "JSON" wording
   in this PR (already reflected in §1's file table).
4. **Rating-button wire shape — RESOLVED: plan default.** Plain buttons, no per-button `hx-post`; one
   hidden batched sender element owns flush cadence + backoff retry, per §0's design and §6 of this plan.
5. **Per-event failure policy — RESOLVED: plan default.** Always `200`, per-event `status` ∈
   `applied|duplicate|forbidden|rejected`, as specified in §0.7.
6. **Vendoring posture vs. the existing CDN — RESOLVED: plan default.** Vendor htmx (+ json-enc ext)
   only, under `web/static/`. Leave Pico CSS loading from jsDelivr as-is; the inconsistency is not
   addressed in this PR.
7. **`review_log.review_kind` mapping — RESOLVED: plan default.** Derive from `state_before`
   (New/Learning→0, Review→1, Relearning→2), per §0.5 and `reviewKind` in §3.5. Best-effort given
   `docs/anki-schema.md`'s unverified `revlog.type` ordering; affects export/analysis fidelity only, not
   scheduling correctness (FSRS never reads this column).
8. **New-card ordering in the queue — RESOLVED: plan default.** All due reviews first (by `due`), then
   never-seen cards by `card_id`, per §0.10's keyset design (`COALESCE(due,'infinity')`). No
   `decks.preset` interleaving in this issue.
9. **Daily new-card / review caps — RESOLVED: plan default.** No caps in this issue. `decks.preset`
   stays unread; caps are a follow-up once preset editing exists in the UI.
10. **`/api/reviews/*` auth failure shape — RESOLVED: plan default.** `401` JSON for the two `/api/`
    routes (per §0.8); `GET /decks/{id}/review` keeps `auth.RequireUser`'s `303`-to-login.
11. **Learning-steps requeue heuristic constants — RESOLVED: plan default.** 20-minute window, insert
    `max(3, cardsDueBefore(due))` positions ahead, per §0's queue-module description and §6 of this plan.
    Cosmetic only (never written to the DB) — low-stakes to retune later.
12. **Concurrency-test harness — RESOLVED: dropped, NOT the plan default.** The user chose to **drop the
    DB-level two-connection deadlock test** and rely solely on the pure `TestLockKeys` test (§9.10, no
    database — verifies the key-sort/dedupe logic only). Consequence: **§9.11
    (`TestGradeBatch_ConcurrentOverlappingBatches`) is cut from scope** — do not write it. The actual
    blocking/re-entrant behaviour of `pg_advisory_xact_lock` is trusted, not verified by test, for this
    issue. `internal/review/lock_test.go` therefore contains only the pure lock-ordering test; drop the
    "+ the two-connection deadlock test" responsibility from its row in §1's file table.

---

### Critical Files for Implementation
- `C:\Users\JohnJolly\Local\git\enshu\internal\db\queries\reviews.sql` (new — queue, study-day window, card authorisation, advisory lock)
- `C:\Users\JohnJolly\Local\git\enshu\internal\review\grade.go` (new — `GradeBatch`, the four concurrency mechanisms, §2.7's authority)
- `C:\Users\JohnJolly\Local\git\enshu\internal\http\review.go` (new — the three routes, strict wire parsing)
- `C:\Users\JohnJolly\Local\git\enshu\internal\fsrs\schedule.go` (existing, unchanged — `Schedule`/`PreviewAll`/`ElapsedDays` are the only scheduling entry points)
- `C:\Users\JohnJolly\Local\git\enshu\web\static\review.js` (new — the in-session queue module; owns everything that doesn't touch the wire)