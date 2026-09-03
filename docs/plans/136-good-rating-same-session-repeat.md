# Issue #136 — New card: Good repeats in the same study session

## Root cause (confirmed by reading the code, not the issue text)

This is **not** a SQL "due bucketing" problem and **not** an FSRS miscomputation. It is a
deliberate, documented client-side feature working exactly as designed, and the design is what's
wrong for this case.

1. `internal/fsrs/params.go`'s `engine()` builds the go-fsrs scheduler with `EnableShortTerm:
   true`, `LearningSteps: gofsrs.DefaultLearningSteps()` (`{1, 10}` minutes — Anki's own classic
   default) and `RelearningSteps: gofsrs.DefaultRelearningSteps()` (`{10}` minutes). This is
   go-fsrs's own reimplementation of Anki's short learning-step queue, opted into deliberately.
2. For a New card (`internal/fsrs`'s `CardState{State: New}`), go-fsrs's `scheduler_basic.go`
   `newState()`:
   - `Good` → `goodDelayMinutes(steps, len(steps))` → index `1` into `{1, 10}` → **State =
     Learning, Due = now + 10 minutes, RemainingSteps = 1**.
   - `Easy` → always `graduateToReview()` — jumps straight to Review state with a full
     multi-day FSRS interval (the reported ~8 days).
   - `Again`/`Hard` also land in Learning with a short delay (1 and ~6 minutes respectively).
   This is correct, intended FSRS/Anki-compatible behaviour — not a bug in `internal/fsrs`.
3. `internal/db/queries/reviews.sql`'s `ListDueCardsForStudy`/`ListReviewCardsForStudy` already
   hard-exclude any card graded today: `AND (scored.last_review IS NULL OR scored.last_review <
   sqlc.arg(study_day_start)::timestamptz)`. Once a card is graded, the SQL refill path
   (`GET /api/reviews/next`) can **never** re-serve it for the rest of the study day. This is the
   mechanism the reporter's comment ("change due date to due time... exclude it") guessed at —
   but it's already correct and is not where the repeat comes from.
4. The actual repeat is `web/static/review.js`'s `maybeRequeue()` (lines 227–255), a documented,
   intentional feature ("Learning-steps requeue heuristic (cosmetic, never written anywhere)",
   `docs/plans/56-reviewer-batch-grading.md` Resolved Decision 11, `docs/architecture.md` §5.2 and
   §6 lines ~329–333). It is **cosmetic client-only queue reordering — never sent to the server,
   never affects `user_card_state`/`review_log`** (§2.6/§2.7 are not implicated by any fix here).
   Current rule: after grading, if the graded branch's resulting `state` is `1` (Learning) or `3`
   (Relearning) *and* its `due` is within `REQUEUE_WINDOW_MS` (20 minutes) of now, splice the card
   back into the local in-session queue `max(3, cardsDueBefore)` slots ahead.
   Since step 2 puts a New card's `Good` outcome at Learning/+10min, it always satisfies this
   condition → same-session repeat → the only way to avoid it today is `Easy`, which forces the
   oversized ~8-day interval the reporter describes.

**Conclusion:** the fix belongs entirely in `web/static/review.js`'s `maybeRequeue()`. No change
to `internal/fsrs` (pure, must stay pure — §17), no SQL/query change, no server-side change of any
kind. This keeps grading exactly as authoritative as before (§2.7) — nothing about what gets
computed or stored changes, only in-session display ordering.

### Why "Good never requeues in-session" and not "New cards never requeue"

- Scoping the fix to "prior state was New" would require `maybeRequeue` (or its caller) to know
  the card's *prior* state, which it doesn't currently track (it only has the *post-grade*
  `branch`). Scoping to "the rating was Good" needs no new state and is already available at the
  call site.
- Under the hardcoded 2-step `{1, 10}` / 1-step `{10}` step lists, `Good` can only ever land a
  card back in Learning in two cases: (a) a New card's first `Good` (this issue), or (b) a second
  `Good` after a prior `Again` put it in Learning with `RemainingSteps = 2`. In case (b) the user
  already got it wrong once this session — deferring the confirmation to "next session too" is a
  reasonable, not surprising, generalization of the same "Good means stop bugging me this
  session" rule, and it needs no special-casing to get right.
- `Again`/`Hard` are left untouched: they are the "I don't know this yet" signals, and Anki's
  near-term reinforcement loop for them is not what was reported and should not be removed.
- `Easy` already never requeues today (its branch `state` is always `2`/Review, never `1`/`3`),
  so no change is needed for it.

This is a genuine, deliberate divergence from Anki's own same-session learning-queue behaviour
for the `Good` rating specifically (real Anki *would* re-show a New card that graduated only one
learning step). Per CLAUDE.md §10 it needs a written reason and a row in
`docs/architecture.md` §20 — see "Docs to update" below.

## Code changes

### 1. `web/static/review.js`

Current (lines 199–255):

```js
  // -- Grading, synchronously [§2.6] --------------------------------------

  function grade(rating) {
    var stage = document.getElementById('review-stage');
    if (state.current === null || !stage || stage.dataset.revealed !== 'true') return;
    var card = state.queue[state.current];
    var branch = card.branches[rating];
    if (!branch) return;

    var durationMs = state.revealStart ? Math.max(1, Date.now() - state.revealStart) : null;
    card.done = true;

    var ev = {
      id: uuidv7(),
      cardId: card.cardId,
      rating: rating,
      reviewedAt: new Date().toISOString(),
      durationMs: durationMs,
    };
    state.pending.push(ev);

    maybeRequeue(card, branch);

    document.body.dispatchEvent(new CustomEvent('card-graded', { detail: ev }));
    scheduleFlush();
    showNext();
  }

  // Cosmetic, never written anywhere (architecture.md §6): requeue a card in-session iff its
  // graded branch lands it back in Learning/Relearning within 20 minutes, inserted ahead by
  // max(3, cardsDueBefore) upcoming slots (plan resolved decision 11).
  function maybeRequeue(card, branch) {
    if (branch.state !== 1 && branch.state !== 3) return;
    var dueMs = Date.parse(branch.due);
    if (isNaN(dueMs) || dueMs - Date.now() > REQUEUE_WINDOW_MS) return;

    var cardsDueBefore = 0;
    for (var i = 0; i < state.queue.length; i++) {
      var c = state.queue[i];
      if (c.done) continue;
      var d = Date.parse(c.branches[3].due); // Good branch, representative of queue order
      if (!isNaN(d) && d <= dueMs) cardsDueBefore++;
    }
    var ahead = Math.max(3, cardsDueBefore);

    var insertAt = state.queue.length;
    var remaining = ahead;
    for (var j = state.current + 1; j < state.queue.length; j++) {
      if (!state.queue[j].done) {
        remaining--;
        if (remaining <= 0) { insertAt = j; break; }
      }
    }
    state.queue.splice(insertAt, 0, {
      cardId: card.cardId, el: card.el, branches: card.branches, done: false, repeat: true,
    });
  }
```

Change to:

1. Add a `RATING_GOOD` constant next to the existing tuning constants (lines 13–16):

```js
  var BACKOFF_SECONDS = [1, 2, 4, 8, 16, 30];
  var FLUSH_DEBOUNCE_MS = 2000;
  var REQUEUE_WINDOW_MS = 20 * 60 * 1000;
  var REFILL_THRESHOLD = 10;
  var RATING_GOOD = 3; // fsrs.Good (internal/fsrs/schedule.go) -- wire-identical rating ordinal
```

2. Pass `rating` through to `maybeRequeue` at the call site:

```js
    maybeRequeue(card, branch, rating);
```

3. Update `maybeRequeue` to gate on rating, via an extracted, pure, testable predicate
   `shouldRequeue`:

```js
  // Cosmetic, never written anywhere (architecture.md §6): a card requeues in-session iff its
  // graded branch lands it back in Learning/Relearning within 20 minutes AND the rating that
  // produced it was Again or Hard. Good never requeues in-session (#136) -- go-fsrs's default
  // {1,10}-minute learning steps mean a New card's first Good otherwise satisfies the
  // Learning-state/20-minute test too, forcing users toward Easy (an oversized ~8-day interval)
  // just to avoid an immediate repeat. Easy already never requeues (its branch state is always
  // Review), so this only changes Good's behaviour. This is a deliberate divergence from Anki's
  // own same-session learning queue for Good specifically -- docs/architecture.md §20.
  function shouldRequeue(rating, branchState, dueMs, nowMs) {
    if (branchState !== 1 && branchState !== 3) return false;
    if (rating === RATING_GOOD) return false;
    if (isNaN(dueMs) || dueMs - nowMs > REQUEUE_WINDOW_MS) return false;
    return true;
  }

  function maybeRequeue(card, branch, rating) {
    var dueMs = Date.parse(branch.due);
    if (!shouldRequeue(rating, branch.state, dueMs, Date.now())) return;

    var cardsDueBefore = 0;
    for (var i = 0; i < state.queue.length; i++) {
      var c = state.queue[i];
      if (c.done) continue;
      var d = Date.parse(c.branches[3].due); // Good branch, representative of queue order
      if (!isNaN(d) && d <= dueMs) cardsDueBefore++;
    }
    var ahead = Math.max(3, cardsDueBefore);

    var insertAt = state.queue.length;
    var remaining = ahead;
    for (var j = state.current + 1; j < state.queue.length; j++) {
      if (!state.queue[j].done) {
        remaining--;
        if (remaining <= 0) { insertAt = j; break; }
      }
    }
    state.queue.splice(insertAt, 0, {
      cardId: card.cardId, el: card.el, branches: card.branches, done: false, repeat: true,
    });
  }
```

No other function in `review.js` changes. `indexBatch`'s dedupe-by-`cardId` and the SQL-level
`last_review < study_day_start` exclusion (reviews.sql, unchanged) are unrelated safety nets for a
different race (a refill landing before a grade's POST has committed) and need no changes.

### 2. `docs/architecture.md`

- §5.2 (`web/templates/review_cards.html` / client spec section, the paragraph beginning
  "**Learning-steps heuristic (cosmetic, never written anywhere).**" — currently:

  > Requeue the card in-session iff the branch state is `1` (Learning) or `3` (Relearning) **and**
  > the branch's `due` is within 20 minutes of now; insert it `max(3, cardsDueBefore(branchDue))`
  > positions ahead. Otherwise drop it from the session.

  Append: "**and** the graded rating was Again or Hard — a Good (or Easy) outcome never requeues
  in-session, so a card the user marks as known always waits for the next study session (#136),
  even when go-fsrs's short-term learning steps would otherwise land it back within the window."

- §20 "Deviations from Anki" → "Unforced — resolved": add a new prose entry (matching the
  existing "One note, one deck" entry's style) documenting this divergence: Anki resurfaces a
  card within the same session whenever a learning/relearning step's delay is short enough,
  regardless of rating; Enshu's client-side heuristic (`web/static/review.js`'s `maybeRequeue`,
  cosmetic only, never written to `user_card_state`/`review_log`) additionally never requeues on
  a `Good` rating, so a card the user says they know is always deferred to the next study session
  rather than cycling back minutes later — [#136](https://github.com/Jolls/deckshare/issues/136).
  State plainly that this does not trace to the content/progress seam (§2.1) — it is a UX choice,
  not a multiuser-forced one — which is why it needs this row per the §20 test.

Do not edit `docs/plans/56-reviewer-batch-grading.md` — it is the historical record of the
original (now superseded) decision; the plan you're reading now is the new one.

## Regression tests

### Required: `internal/fsrs/schedule_test.go` — pin the go-fsrs behaviour this fix depends on

This is the one FSRS-adjacent test CLAUDE.md §5's "anything touching FSRS scheduling semantics
always ships a test" exception is really about here: not because `internal/fsrs` changes (it
doesn't), but because the JS fix's correctness *depends on* go-fsrs's default learning-step
behaviour continuing to put a New card's `Good` in Learning state with a same-day `Due`, and
`Easy` always graduating straight to Review. If a future go-fsrs upgrade changes that (e.g.
different default steps, or `Good` graduating New cards directly), the client-side rule silently
stops doing anything useful and nobody would notice. Pin it.

Extend the existing `TestNewCardOutcomes` (currently lines 189–231) — insert after the existing
`if preview.Easy.State != Review { ... }` block (around line 204):

```go
	if preview.Good.State != Learning {
		t.Errorf("Good.State = %v, want Learning -- go-fsrs's default {1,10}-minute learning "+
			"steps mean a New card's first Good does not graduate straight to Review; the "+
			"client-side same-session requeue suppression (#136) assumes this", preview.Good.State)
	}
	if d := preview.Good.Due.Sub(now); d < 0 || d >= 24*time.Hour {
		t.Errorf("Good.Due = %v (%.1f hours after now), want a same-day short-term interval "+
			"(#136 depends on this being short, not multi-day)", preview.Good.Due, d.Hours())
	}
	if preview.Easy.ScheduledDays < 1 {
		t.Errorf("Easy.ScheduledDays = %d, want >= 1 -- Easy always graduates a New card "+
			"straight to Review with a multi-day interval, which is why it was the only "+
			"same-session workaround before #136", preview.Easy.ScheduledDays)
	}
```

`time` is already imported in this file (used by `out.Due.Location()` checks). No new test file
needed; this is a targeted addition to existing, already-passing infra.

### Resolved: no JS-side regression coverage added

**Decision: Option B.** No JS test runner or test file is added for this fix. Coverage is the
required `internal/fsrs` pinning test above (covers the go-fsrs assumption this fix depends on)
plus the manual QA checklist below. Justification: this is a UI-ordering-only, cosmetic change
(never written to the DB, §2.6/§2.7 untouched) — within CLAUDE.md §5's "skip for trivial/UI-only"
carve-out — and introducing a first JS-test-runner precedent for the repo is a separate, larger
decision than this one-line behavioural gate warrants. `shouldRequeue` is *not* extracted with a
dual browser/Node export guard (that scaffolding is only needed if a JS test consumes it); the
`maybeRequeue`/`shouldRequeue` split in the code change above stays purely for in-file readability.

## Manual QA (always do this regardless of the JS-test decision)

1. Reset a test deck to have at least one never-seen card and one review-state card whose
   `Again`/`Hard` outcome is short-term (any due card recently lapsed works, or a fresh New card
   rated Again first).
2. Study the deck. Rate the never-seen card `Good`. Confirm it does **not** reappear later in the
   same session (keep reviewing past its old ~10-minute mark if the session runs that long, or
   just confirm it's simply gone from the visible queue/count).
3. Rate a different card `Again` (or `Hard`). Confirm it **does** still reappear in-session per
   the existing 20-minute/requeue-ahead behaviour (unchanged).
4. Confirm `Easy` still behaves as before (graduates to Review, no requeue) — unaffected by this
   change, but worth a sanity check since it shares the same code path.
5. Reload the reviewer at the start of a new study day and confirm the `Good`-graded card is now
   offered again (it was only deferred, not silently dropped) — sanity-checks that nothing here
   touched `user_card_state`/`due` itself.

## Open questions

None — resolved above (no JS test infra added; see "Resolved: no JS-side regression coverage
added").
