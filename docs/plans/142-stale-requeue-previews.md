# Issue #142 — Requeued cards show stale interval labels (audit finding J2)

`web/static/review.js`'s `maybeRequeue` reinserts a card into the in-session queue carrying
`card.branches` — the four rating outcomes the server precomputed for the card's *pre-first-grade*
state (CLAUDE.md §2.6). On the card's second appearance the `data-interval-for` labels therefore
describe intervals that the just-applied grade already invalidated. Cosmetic (nothing scheduling
relevant reads those labels, architecture.md §6's learning-steps carve-out) but wrong.

**Decision: neither of the issue's two suggested directions.** A third option dominates both, and
it is a strict subset of option (A)'s "reuse of existing preview logic" with the round trip
removed: **`POST /api/reviews/batch` already returns a per-event `after` state, and its response is
already parsed by the client. Ship the four fresh branches alongside it.** See
[§1](#1-why-not-a-b-or-a-forced-flush) for why the alternatives lose.

Scope: server computes one extra `fsrs.PreviewAll` per graded event, the grade response grows a
`preview` object per result, and the client swaps it into any queue slot still holding that card.
No new endpoint, no new query, no schema change, no migration, no client-side FSRS.

---

## 1. Why not A, B, or a forced flush

| Option | Verdict |
|---|---|
| **(A) New preview-only endpoint**, called on requeue | Rejected. It adds an HTTP route, its own `deck_access` authorisation path, its own test row in the §10.5 access-control table, and a round trip — to fetch data the grade response could have carried for free. The grade POST for that very card is already in flight or about to be. Two requests where one suffices. |
| **(B) Second-order branches at batch-fetch** (4×4=16 per card) | Rejected on both accuracy and size. Accuracy: a second-order branch is `PreviewAll(Schedule(prior, R1, t_fetch), t_fetch)`, but the real first grade happens at `t_grade` (minutes later) and the real second grade later still — so the shipped value is stale *by construction*, which is the exact defect being fixed. Size: 16 branches × 3 attributes = 48 extra `data-*` attributes per card, on every card in a 20-card batch, when at most the Again/Hard sub-branches of Learning/Relearning cards are ever read. Also a much larger wire-contract change than (C). |
| **(C) Fresh preview on the grade response** — **chosen** | The server already computes and returns the authoritative post-grade state (`Result.After`); previewing from it is one pure `fsrs.PreviewAll` call on state already in hand, at the same `now` the batch is being graded at. No new route, no new round trip, no extra latency in any path, and the value is computed from the state the grade *actually stored* rather than a guess made minutes earlier. |
| **Forcing an immediate flush on requeue** (considered, rejected) | Would tighten the arrival window but turn every Again/Hard grade of a learning card into its own POST, against architecture.md §6's deliberately batched sender ("kind to mobile radios"). Not needed — see the timing analysis below. |

**§2.6 compliance (grading never blocks on the network).** Nothing in this change sits between a
keypress and the next card. `grade()` still reads a branch already in memory and calls `showNext()`
synchronously; the fresh preview arrives later, on the response to a POST that was already being
sent, and is applied by the same `onBatchSettled` callback that already runs there. No `await` is
added to any path. Note the requeue itself is *not* on the hot path either — but this design does
not put a network call there regardless, so that question does not need to be relitigated.

**Timing: is the preview there in time?** The requeued slot is inserted at least 3 pending cards
ahead (`ahead = Math.max(3, cardsDueBefore)`). The flush debounce is 2 s, single-request-in-flight,
so the response typically lands within ~2–3 s of the grade, while three more cards take a human
well longer. If it does arrive late — or never, offline — the slot keeps the branches it has today
and `applyFreshPreview` repaints the labels in place if that card is on screen when the response
lands. Strictly better than current behaviour in every case, never worse.

**§2.7 compliance.** `preview` is display-only, server-computed, and travels server→client only.
The request wire struct (`wireEvent`, five fields) is untouched, so there is still nothing
client-supplied that could reach a scheduling field. CLAUDE.md §10.1's test is unaffected.

---

## 2. Server changes

### 2.1 `internal/review/types.go` — `Result` gains a preview

```go
// Result is one event's outcome from GradeBatch.
type Result struct {
	ID      pgtype.UUID
	CardID  pgtype.UUID
	Status  Status
	After   *CardStateDTO // nil for forbidden/rejected
	Preview *fsrs.Preview // the four branches from After, at the batch's now; nil whenever After is
}
```

Add a sentence to the `Result` doc block:

```go
// Result is one event's outcome from GradeBatch. Preview is the four rating outcomes the *stored*
// After state produces, so the client can relabel a card its in-session learning-steps heuristic
// requeued without the branches it was shipped at batch-fetch time (#142). Display-only, like
// every other branch preview: nothing the client sends back is ever read from it (§2.7).
```

`fsrs` is already imported in this file.

### 2.2 `internal/review/grade.go` — thread `fsrs.CardState`, not the DTO

The DTO conversion currently happens in four places deep in the grade path, which leaves
`GradeBatch` holding a `*CardStateDTO` it cannot preview from without an inverse converter (a
second copy of the field list — exactly what `Outcome.CardStateAt`'s doc comment warns against).
Convert once, at the top, and preview from the same value.

**a. `currentStateDTO` → `currentState`** (line ~79):

```go
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
```

**b. `duplicateOf`** (line ~127) — same body, new return type:

```go
func duplicateOf(ctx context.Context, q *db.Queries, userID, cardID pgtype.UUID) (*fsrs.CardState, Status, error) {
	after, err := currentState(ctx, q, userID, cardID)
	if err != nil {
		return nil, "", err
	}
	return after, StatusDuplicate, nil
}
```

**c. `gradeEvent`, `gradeInOrder`, `gradeOutOfOrder`** — change the first return from
`*CardStateDTO` to `*fsrs.CardState`. Only two statement-level edits inside them:

- `gradeInOrder`'s tail (line ~361), keeping the existing "re-read rather than trust outcome" comment verbatim:
  ```go
  	after, err := currentState(ctx, q, userID, ev.CardID)
  	if err != nil {
  		return nil, "", err
  	}
  	return after, StatusApplied, nil
  ```
- `gradeOutOfOrder`'s tail (line ~399):
  ```go
  	return &final, StatusApplied, nil
  ```
  (`final` is `replayStates`' returned `fsrs.CardState`; `&final` is a local copy's address, safe.)

The `return duplicateOf(...)` statements in both functions need no change.

**d. New `resultFor` helper**, placed immediately above `GradeBatch`:

```go
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
```

This is an explicit error check, not a swallow (CLAUDE.md §9): the error is inspected and the
degraded path is the documented behaviour, not an ignored value.

**e. `GradeBatch` — two call sites, and the params cache moves up.**

Move `paramsCache := NewParamsCache()` (currently line ~280, just above the `fresh` loop) to just
above the duplicate-filter loop (currently line ~259, `fresh := make([]Event, 0, len(toGrade))`),
so the early-duplicate path can resolve params too. It is a per-deck memo, so this costs one
lookup for a batch that is entirely duplicates and nothing otherwise.

In the duplicate-filter loop, replace:

```go
		dto, status, err := duplicateOf(ctx, q, userID, ev.CardID)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = Result{ID: ev.ID, CardID: ev.CardID, Status: status, After: dto}
```

with:

```go
		p, err := paramsCache.Get(ctx, q, userID, deckOf[ev.CardID])
		if err != nil {
			return nil, err
		}
		after, status, err := duplicateOf(ctx, q, userID, ev.CardID)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = resultFor(p, ev, status, after, now)
```

A retry gets the same preview the original response carried — the retry exists precisely because
that first response may never have arrived, so withholding it there would leave the requeued card
stale in the one case the fix is most needed.

In the main grading loop, replace:

```go
		after, status, err := gradeEvent(ctx, q, p, userID, ev)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = Result{ID: ev.ID, CardID: ev.CardID, Status: status, After: after}
```

with:

```go
		after, status, err := gradeEvent(ctx, q, p, userID, ev)
		if err != nil {
			return nil, err
		}
		results[ev.ID] = resultFor(p, ev, status, after, now)
```

`now` here is `GradeBatch`'s parameter, already microsecond-truncated at the top of the function.
`forbidden` and `rejected` results are constructed earlier and are untouched — both have
`After == nil`, so neither gets a preview.

**Do not** add a state/due filter that only previews requeue-eligible cards. The 20-minute
Learning/Relearning window is the *client's* cosmetic heuristic (architecture.md §6); replicating
it server-side would create a second copy of it to keep in step, for an arithmetic call whose cost
is a rounding error next to the round trip already being made.

### 2.3 `internal/http/review.go` — the wire shape

`branchView` gains JSON tags (it is currently template-only; the tags do not affect template field
access, so the HTML `data-*` attributes and the JSON branch come from one formatter and can never
disagree about format):

```go
type branchView struct {
	Due           string `json:"due"`
	State         int    `json:"state"`
	ScheduledDays int32  `json:"scheduledDays"`
}
```

Add, next to `toBranchView`:

```go
// previewView is fsrs.Preview on the wire -- the same three fields per branch that the hidden
// card node carries as data-* attributes, so the client parses one branch shape, not two.
type previewView struct {
	Again branchView `json:"again"`
	Hard  branchView `json:"hard"`
	Good  branchView `json:"good"`
	Easy  branchView `json:"easy"`
}

func toPreviewView(p fsrs.Preview) previewView {
	return previewView{
		Again: toBranchView(p.Again),
		Hard:  toBranchView(p.Hard),
		Good:  toBranchView(p.Good),
		Easy:  toBranchView(p.Easy),
	}
}
```

`resultView` and `toResultViews`:

```go
type resultView struct {
	ID      string       `json:"id"`
	CardID  string       `json:"cardId"`
	Status  string       `json:"status"`
	After   any          `json:"after,omitempty"`
	Preview *previewView `json:"preview,omitempty"`
}

func toResultViews(results []review.Result) []resultView {
	views := make([]resultView, len(results))
	for i, r := range results {
		v := resultView{ID: r.ID.String(), CardID: r.CardID.String(), Status: string(r.Status)}
		if r.After != nil {
			v.After = r.After
		}
		if r.Preview != nil {
			pv := toPreviewView(*r.Preview)
			v.Preview = &pv
		}
		views[i] = v
	}
	return views
}
```

**Resulting response contract** (additive; every existing field and its meaning unchanged):

```json
{"results":[{
  "id":"…","cardId":"…","status":"applied",
  "after":{"due":"…","state":2,"stability":…,"difficulty":…,"reps":1,"lapses":0,
           "scheduledDays":15,"learningSteps":0,"lastReview":"…"},
  "preview":{"again":{"due":"2026-03-01T12:10:00Z","state":3,"scheduledDays":0},
             "hard":{…},"good":{…},"easy":{…}}
}]}
```

`preview` is present on `applied` and `duplicate` results, absent on `forbidden`/`rejected` (and
absent on the vanishingly rare `duplicate` for a card with no `user_card_state` row). Size: ~200
bytes per event, ≤ 20 KB at the 100-event cap, response-only.

No `sqlc`/DB-layer change: no query is added or altered, so no `go generate`, no migration.

---

## 3. Client changes — `web/static/review.js`

### 3.1 Apply the fresh preview when the grade response lands

In the success branch of `onBatchSettled`, extend the existing results loop (do not add a second
loop):

```js
      for (var i = 0; i < results.length; i++) {
        var r = results[i];
        applyFreshPreview(r.cardId, r.preview);
        if (r.status === 'rejected' || r.status === 'forbidden') {
```

(the rest of the loop body is unchanged).

Add, immediately after `onBatchSettled`:

```js
  // The branches on a hidden card node were precomputed for its state *before* this session's
  // first grade (§2.6), so a card the learning-steps heuristic requeued would show its pre-grade
  // intervals on its second appearance (#142). The grade response carries the four branches the
  // server recomputed from the state it actually stored; swap them into every slot still holding
  // that card, and repaint the labels if it is the one on screen. Cosmetic, exactly like the
  // branches it replaces -- nothing here is ever sent back.
  function applyFreshPreview(cardId, preview) {
    var fresh = previewBranches(preview);
    if (!fresh) return;
    for (var i = 0; i < state.queue.length; i++) {
      var slot = state.queue[i];
      if (slot.cardId !== cardId || slot.done) continue;
      slot.branches = fresh;
      if (i === state.current) updateIntervalLabels(slot);
    }
  }

  // All four branches or none: a partial swap would leave grade() with a missing branch, which it
  // answers by silently ignoring the keypress.
  function previewBranches(p) {
    if (!p || !p.again || !p.hard || !p.good || !p.easy) return null;
    return { 1: wireBranch(p.again), 2: wireBranch(p.hard), 3: wireBranch(p.good), 4: wireBranch(p.easy) };
  }

  // The JSON counterpart of branch(): same three fields, already typed, no parseInt.
  function wireBranch(b) {
    return { due: b.due, state: b.state, scheduledDays: b.scheduledDays };
  }
```

Assignment, not mutation: the graded slot is `done` and keeps its old object, so nothing reads a
half-updated branch set. Only non-`done` slots are touched, which are the requeued slot and (in the
refill-race case that `indexBatch` dedupes) nothing else.

### 3.2 `maybeRequeue` — say where the fresh branches come from

The splice keeps `card.branches` (it is the only value that exists at that instant, and a stale
label beats a blank one). Add the pointer so the next reader does not re-file this finding:

```js
    // card.branches is the pre-grade preview -- the best value available at this instant.
    // onBatchSettled replaces it with the server's post-grade preview when this grade's POST
    // comes back, which is normally several cards before this slot is shown (#142).
    state.queue.splice(insertAt, 0, {
      cardId: card.cardId, el: card.el, branches: card.branches, done: false, repeat: true,
    });
```

### 3.3 File header comment

The module header (lines 4–7) currently ends "it looks up a precomputed branch already present on
the hidden card node (CLAUDE.md §2.6, §2.7)". Extend the sentence:

```js
// value -- it looks up a precomputed branch already present on the hidden card node, or, for a
// card this session requeued, the fresh one the grade response brought back (CLAUDE.md §2.6, §2.7).
```

No change to `web/templates/review.html`, `web/templates/review_cards.html`, the CSP, or any htmx
attribute. The `data-*` attributes on the hidden `<article>` are deliberately left alone: nothing
re-reads a node once `indexBatch` has slotted it.

---

## 4. Tests

### 4.1 The FSRS package is not touched

No file in `internal/fsrs` changes — `PreviewAll` is called from one more place, not modified. The
CLAUDE.md §10 "anything touching the FSRS scheduling package always ships a test" rule therefore
does not fire, and §10.2's property (`TestPreviewMatchesRecompute`,
`internal/fsrs/consistency_test.go`) already covers the new call exactly: it folds each chosen
outcome forward with `CardStateAt` and re-previews from it every step, which *is* previewing from a
post-grade state. **Do not duplicate that property test.** State this reasoning in the PR
description so the review pass does not ask for it.

### 4.2 New: `internal/http/review_test.go` — the contract test

Add after `TestReviewBatch_ClientCannotWriteSchedulingState`, in the same independent-recompute
style (DB-backed; `beginTx` skips without `DATABASE_URL`, so run it with the env var set — CLAUDE.md
§16):

```go
// -- #142: the grade response's preview is computed from the state the grade stored ------------

func TestReviewBatch_PreviewIsRecomputedFromStoredState(t *testing.T) {
```

Body, in order:

1. `tx := beginTx(t)`; `clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)`; `handler, a :=
   newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })`; login cookie;
   `_, cardID := setupOneCard(t, tx, handler, cookie)`.
2. POST one event, **rating 4 (Easy)**, `reviewedAt` = `clock`. Rating choice is load-bearing and
   must not be changed casually: Easy graduates a New card straight to `Review` with a multi-day
   interval, so the post-grade preview differs from the pre-grade one in nine of the twelve wire
   fields (Again becomes `Relearning`/10 min, Hard and Good become `Review` with `scheduledDays`
   ≥ 15). Grading `Again` instead would leave the card in `Learning` with the same remaining steps,
   and go-fsrs's Again/Hard/Good branches would then be wire-identical before and after
   (`scheduler_basic.go`'s `newState` vs `learningState` reach the same `applyStep` delays) —
   a test that could not fail.
3. Decode the body into a local struct mirroring §2.3's shape (`results[].status`,
   `results[].preview.{again,hard,good,easy}.{due,state,scheduledDays}`). Assert HTTP 200, one
   result, `status == "applied"`, `preview != nil`.
4. Independently recompute:
   ```go
   params, err := fsrs.NewDefaultParams(review.DefaultDesiredRetention)
   row := getUserCardStateByCardID(t, tx, cardID)
   stored := fsrs.CardState{
       Due: row.Due.Time, Stability: row.Stability, Difficulty: row.Difficulty,
       State: fsrs.State(row.State), Reps: row.Reps, Lapses: row.Lapses,
       ScheduledDays: row.ScheduledDays, LearningSteps: row.LearningSteps,
       LastReview: row.LastReview.Time,
   }
   want, err := fsrs.PreviewAll(params, stored, clock)
   ```
   Assert all four branches match field for field, with `due` compared as
   `o.Due.UTC().Format(time.RFC3339Nano)`. Building `stored` from the **database row** rather than
   from the response's `after` is deliberate: it pins the preview to the state that was actually
   persisted, and catches a preview taken from an in-memory `Outcome` that the guarded upsert
   rejected.
5. Meaningfulness guard, so the assertion above cannot pass vacuously:
   ```go
   stale, err := fsrs.PreviewAll(params, fsrs.CardState{}, clock)
   ```
   `t.Fatal` if `stale` and `want` agree on all twelve wire fields (`due`/`state`/`scheduledDays`
   per branch) — that would mean the fixture no longer separates the pre- and post-grade previews
   and the test needs a different rating or a deeper fixture, not a green tick.

### 4.3 New: the duplicate path carries a preview too

In `TestReviewBatch_Idempotency`'s existing `"same batch sent twice"` subtest, add one assertion to
the existing `w2` checks: the replayed response body contains `"preview":`. One line; it pins the
`paramsCache` move in §2.2e, whose whole purpose is that path.

### 4.4 No JS test

There is no JS test harness in this repo (no Playwright, no node test runner — CLAUDE.md §10.6's
E2E reviewer suite is not built yet). Client verification is manual, §6 below.

### 4.5 Regression check on existing tests

None of `internal/http/review_test.go`'s existing assertions compare whole response bodies for
equality (they use `strings.Contains` on `"status":"…"` and compare DB rows), so the additive
`preview` field breaks nothing. Confirm by running the package.

---

## 5. Documentation

1. **`docs/routes.md`**, the `POST /api/reviews/batch` row — change
   `returns {results:[{id,cardId,status,after}]} per event` to:
   `returns {results:[{id,cardId,status,after,preview}]} per event (`preview` = the four rating
   branches recomputed from `after`, display-only, so a card the client requeued in-session can be
   relabelled — absent on forbidden/rejected)`.
2. **`docs/architecture.md` §6**, the server-contract pseudo-block — change the last line
   ```
     -> respond with <after> so the client can reconcile its queue
   ```
   to
   ```
     -> respond with <after>, and Repeat(after) so the client can relabel a requeued card
   ```
   and add one sentence to the end of the "Anki's short-term learning steps…" paragraph (the one
   ending "…even when go-fsrs's short-term learning steps would otherwise land it back within the
   window."):

   > A requeued card's interval labels are refreshed from the grade response's own four-branch
   > preview, since the ones it was shipped with at batch-fetch time describe its pre-grade state
   > ([#142](https://github.com/Jolls/enshu/issues/142)); the requeue decision itself still runs
   > locally and still writes nothing.
3. **`docs/architecture.md` §20 — no new row.** Checked against §2.10's test: this is not a
   divergence from Anki in either direction. Anki is a local app that recomputes a requeued card's
   intervals as a matter of course; refreshing our labels moves *toward* its behaviour. The one
   registered deviation in this area — Good never requeuing in-session (#136) — is unchanged.
4. **`CLAUDE.md` — no change.** §10's priority list, §2's invariants, and §17's untouchables are
   all unaffected; the server-side recompute path is extended, not bypassed.
5. **`.claude/memory/` — no entry.** Nothing here is a decision that outlives the diff; the
   rejected alternatives are recorded in §1 of this plan.

---

## 6. Manual verification (CLAUDE.md §14 step 4)

Per CLAUDE.md §14, do not start the app as a build check — this list is for the user's own pass,
after `go build ./...` / `go vet ./...` / `golangci-lint run` / `go test ./...` are green.

1. `go run ./scripts/run-app start`, open a deck with a few due cards.
2. On a card whose interval labels show days (a Review-state card), press **1 (Again)** — it
   requeues.
3. In DevTools → Network, the `POST /api/reviews/batch` response now carries a `preview` object per
   result.
4. Keep grading until the requeued card comes back. Its four labels must show relearning-scale
   values (minutes/hours for Again, and the post-lapse values for Hard/Good/Easy) — **not** the
   day-scale labels it showed on its first appearance.
5. Offline check: with DevTools set to offline, grade and requeue a card. The labels stay at the
   pre-grade values (today's behaviour, no crash, no blank labels), and the delivery-error banner
   behaves exactly as before. Going back online and letting the retry succeed updates the labels.

---

## 7. Landing

- Branch: `feature/142-stale-requeue-previews`.
- Recommended review pass: **`/code-review medium`**. The diff is small and local, but it does
  touch `internal/review/grade.go`'s return-type plumbing and a wire contract — a `low` pass would
  under-serve the `*CardStateDTO` → `*fsrs.CardState` refactor, and `high` is not warranted since
  no scheduling arithmetic, no query, and no authorisation path changes.
- **`CHANGELOG.md`** — new `## [0.1.39] - <merge date>` at the top. Subheading is **`### Fixed`**:
  the issue is labelled `area: refactor`/`sev: low`, but the change fixes a user-visible display
  inaccuracy and adds a response field, so this is a behaviour change, not a pure refactor.

  ```
  ## [0.1.39] - YYYY-MM-DD

  ### Fixed
  - Fixed a card requeued within a study session showing the interval labels computed for its
    state *before* it was graded: `POST /api/reviews/batch` now returns a `preview` object per
    result — the four rating outcomes recomputed server-side from the state the grade actually
    stored — and the reviewer swaps those into the requeued card's slot
    ([#142](https://github.com/Jolls/enshu/issues/142)).
  ```
- Tag `v0.1.39` after the version-bump commit.
- PR description: `Closes #142`, and carry §4.1's note on why no new `internal/fsrs` test ships.
