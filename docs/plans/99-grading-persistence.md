# 99 — Grading persistence (silent review-loss)

Issue: #99 (`sev: high`, `area: security`). Initial pass (below, "What code reading confirms and
rules out" through "Open questions") was static-code-only — no browser/Playwright in this
environment — and shipped a diagnostics-only fix (server-side CSRF logging + a client-side error
banner) without confirming the actual trigger. Real-browser testing after that fix landed
("works slow, fails fast") gave the reproduction the static pass couldn't. **See "Confirmed root
cause (post-diagnostics)" below for what actually happened and the real fix** — the rest of this
file is kept as investigation history.

## Confirmed root cause (post-diagnostics)

Grading 2+ cards within one flush window (i.e. grading faster than the ~2s debounce) always 400'd.
Temporary server-side logging of the raw rejected body (`internal/http/review.go`'s
`parseBatchRequest`, since reverted) captured the actual wire body:

```
events=%22%5Bobject%20Object%5D%22&events=%22%5Bobject%20Object%5D%22
```

— `application/x-www-form-urlencoded`, not JSON at all. Tracing `web/static/htmx.min.js` and
`htmx-ext-json-enc.js` explained why: htmx's base parameter-building step (`Dn`) converts an
array-valued `hx-vals` result (`{events: [...]}`) into **one FormData entry per array element**,
each coerced via `String(value)` → `"[object Object]"`. The `json-enc` extension's
`encodeParameters` re-evaluates `hx-vals` a second time (`api.getExpressionVars`) to recover the
real typed array, then iterates `parameters.forEach` to rebuild the JSON object — but for a
repeated key, its merge branch (`object[key].push(typedValue)`) pushes the *same* recovered array
reference into itself, since `typedValue` doesn't vary per FormData entry. That's a circular
reference; `JSON.stringify(object)` throws inside `encodeParameters`; htmx's extension dispatcher
(`Vt`) catches the throw silently (`try{t(e)}catch(e){H(e)}`, `H` = `console.error`) and falls
back to htmx's default (non-JSON) encoder — which is the malformed body the server correctly
rejected. This reproduces for any batch of 2+ events; a 1-event batch never hits the repeated-key
merge branch, which is why "slow" (one grade per flush) worked and "fast" (multiple grades
batched into one flush) didn't.

This was a **predicted, not new, bug**: `docs/plans/57-csp-reviewer.md` (open question 1, written
during the original CSP work) already flagged that "htmx 2's `detail.parameters` is a `FormData`,
which flattens an array of objects to `"[object Object]"` entries, and the `json-enc` extension
recovers their types only by re-reading `hx-vals`" and prescribed the exact fix: "move
`#review-sender` off htmx entirely to a direct `fetch()` in `review.js`'s `flush()`, reusing the
existing `onAfterRequest` reconciliation and backoff." It was deferred at the time pending a
browser check. This PR is that browser check, and applies that prescribed fix:

- `web/static/review.js`: `flush()` now calls `fetch('/api/reviews/batch', ...)` directly instead
  of dispatching a `flush-events` CustomEvent for htmx to pick up. `onAfterRequest` (an
  `htmx:afterRequest` listener) became `onBatchSettled`, called directly from the fetch
  `.then()`/rejection handler with the same success/4xx/5xx-or-network-error branches and backoff.
  `takePending()` is simplified back to its original (non-reentrant) form — the double-evaluation
  it was guarding against no longer happens once htmx is out of the request path.
- `web/templates/review.html`: `#review-sender` (and its `hx-post`/`hx-ext`/`hx-trigger`/
  `hx-vals`) removed entirely.
- `web/templates/layout.html`, `web/static/htmx-ext-json-enc.js`, `web/static/README.md`: the
  `json-enc` extension is now unused (only `#review-refill`'s scalar `hx-vals` remains on htmx,
  and that never hits the array-merge path) — script tag and vendored file removed, README updated.
- `internal/http/security.go`, `internal/http/security_test.go`: CSP doc comments updated —
  `'unsafe-eval'` is now justified by `#review-refill` alone; `connect-src`'s comment now
  attributes `/api/reviews/batch` to `review.js`'s `fetch()`/`sendBeacon`, not htmx's XHR.

The diagnostics-only fix from the first pass (CSRF-rejection server logging, client-side delivery
error banner) is kept — it's correct regardless of root cause and now gives a visible signal for
any *other* future delivery failure (e.g. real clock-skew rejections), even though it wasn't what
was actually killing every grade here.

No regression test was added for the htmx/json-enc interaction itself (no JS test runner in this
repo, per "Regression test" section below) — this is exactly the gap issue #100 (Playwright E2E
follow-up) exists to close going forward.

## What code reading confirms and rules out

- **CSP is not blocking the request.** `internal/http/security.go:62-69`: `connect-src 'self'`
  covers both htmx's XHR to `/api/reviews/batch` and `review.js`'s `navigator.sendBeacon` to the
  same path (comment at `security.go:50-52` names this explicitly); `script-src 'self'
  'unsafe-eval'` covers htmx's `hx-vals="js:{...}"` evaluation on `#review-sender`
  (`web/templates/review.html:30-35`). No directive here would produce a console CSP violation
  for this path.
- **`checkOrigin` is logically correct and fully covered** (`internal/auth/middleware.go:100-114`,
  `internal/auth/middleware_test.go:32-73`). For the documented default dev config — `ORIGIN` is
  commented out in `.env.example:13` and `.claude/skills/run-app/run.sh` never sets it — `s.origin`
  is `nil` and `checkOrigin` falls back to comparing the `Origin` header's host against `r.Host`
  (`middleware.go:110-113`). Modern browsers attach `Origin` to every same-origin POST/XHR/
  `sendBeacon`, not just cross-origin ones, so this fallback does not reject a genuine same-origin
  request in that setup. `run.sh:1-40` also confirms the dev server binary runs natively on the
  same host as the browser (Postgres is the only containerised piece), so there's no
  reverse-proxy Host-header rewrite in the documented workflow either.
- **The client wiring matches its own accepted plan verbatim.** Every attribute, event name, and
  function in `web/static/review.js` and `web/templates/review.html` was compared line-by-line
  against `docs/plans/56-reviewer-batch-grading.md` §4.3/§5.1/§5.2/§6 (written when this code was
  designed and reviewed in #70) and matches exactly — trigger names (`flush-events`,
  `refill-needed`), element ids (`#review-sender`, `#review-refill`), `hx-vals` shape, debounce
  timing, backoff table. No typo or mismatch found.
- **The server-side grading path is correct and fully tested** — this reconfirms what the issue
  itself already ruled out (`internal/review/grade.go`, `internal/http/review_test.go`), including
  the specific access-control path (`ListStudyableCards`) the issue didn't explicitly name:
  `TestReviewRoutes_AccessControl` (`review_test.go:606-609`) exercises an owner's batch grade
  end-to-end and asserts `"status":"applied"`.

None of this finds a code defect that would make the POST **never fire**. Given that, the two
remaining candidates from the issue's own repro list are still live and mutually exclusive:

1. **`checkOrigin` 403** — only plausible if this particular dev machine has `ORIGIN` set to a
   mismatched value, or a proxy/port-forward sits between the browser and the server and rewrites
   `Host` inconsistently with `Origin` (not the documented `run.sh` setup, but a real possibility if
   the reporter's environment differs from it).
2. **Silent rejection via `clampOrReject`'s clock-skew guard** (`internal/review/grade.go:23-45`) —
   if the server process's clock and the browser's clock disagree by more than 5 minutes, *every*
   graded event in a batch comes back `"status":"rejected"` with HTTP 200. `review.js`'s
   `onAfterRequest` (`web/static/review.js:336-339`) treats this as a terminal, per-event drop:
   `console.error(...)` and nothing else — no retry, no user-visible signal, exactly the "no
   user-visible failure at all" the issue describes. This requires clock skew between the exact
   same machine's browser and server process in the documented dev setup, which is unlikely but not
   impossible (e.g. the server process left running across a host sleep/resume, or a manually
   different setup than `run.sh`).

Both would produce **the same DB signature** the issue reports (zero `review_log` rows, HTTP 200
where relevant) and are indistinguishable from code alone. **Open question 1** below asks the user
to settle this with the trace before deciding if any further code change is needed beyond what
this plan already fixes.

## The one confirmed, code-provable defect

Independent of which of the two candidates above is the actual trigger, this repo has **no
diagnostic trail for either failure mode, on either side of the wire**:

- `internal/auth/middleware.go:31-34`: a `checkOrigin` rejection writes a 403 and returns —
  nothing is logged. If this ever fires against a legitimate browser, there is no server-side
  evidence it happened.
- `web/static/review.js:336-339` and `:346-350`: a dropped batch (4xx, or a `rejected`/`forbidden`
  per-event result) is reported with `console.error` only — nothing reaches the user, and nothing
  a support/debugging flow could see without the reporter having DevTools open at the exact moment
  it happens.

This is what let a full 17-card session go silently unrecorded with nothing to inspect after the
fact except direct DB inspection. Fixing *this* is unconditionally correct regardless of which
candidate above turns out to be the trigger, and is the code change this plan makes.

## Code changes

### 1. `internal/auth/middleware.go` — log CSRF rejections

`log` is already imported (`middleware.go:6`). In `Middleware`, change:

```go
		if isStateChanging(r.Method) && !s.checkOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
```

to:

```go
		if isStateChanging(r.Method) && !s.checkOrigin(r) {
			log.Printf("csrf: rejected %s %s (Origin=%q Host=%q)", r.Method, r.URL.Path, r.Header.Get("Origin"), r.Host)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
```

### 2. `web/templates/review.html` — add an error slot

Insert a hidden error element after the back-to-deck link and before `#review-stage`:

```html
<h1>{{.Deck.Name}}</h1>
<p><a href="/decks/{{.Deck.ID}}">Back to deck</a></p>

<p id="review-error" hidden></p>

<section id="review-stage" hidden>
```

(Plain `<p>`, no new CSS — matches the rest of this template, which carries no custom classes
beyond `enshu-card`/`card`.)

### 3. `web/static/review.js` — surface dropped batches, and clear the slot on recovery

Add one helper near `applyAfter` (both are small reconciliation-only functions):

```js
  function showDeliveryError(msg) {
    var el = document.getElementById('review-error');
    if (el) { el.textContent = msg; el.hidden = false; }
  }

  function clearDeliveryError() {
    var el = document.getElementById('review-error');
    if (el) el.hidden = true;
  }
```

In `onAfterRequest`, call `clearDeliveryError()` at the top of the `evt.detail.successful` branch
(before the per-result loop), and `showDeliveryError(...)` in the two places that currently only
`console.error`:

```js
    if (evt.detail.successful) {
      state.backoffIndex = 0;
      clearDeliveryError();
      var results = [];
      try { results = JSON.parse(xhr.responseText).results || []; } catch (e) { /* malformed body: nothing to reconcile */ }
      for (var i = 0; i < results.length; i++) {
        var r = results[i];
        if (r.status === 'rejected' || r.status === 'forbidden') {
          console.error('enshu: event ' + r.id + ' ' + r.status + ', dropped permanently');
          showDeliveryError('A grade could not be saved (' + r.status + '). Check your device clock or deck access.');
          continue;
        }
        if (r.after) applyAfter(r.cardId, r.after);
      }
      if (state.pending.length > 0) scheduleFlush();
      return;
    }

    if (xhr.status >= 400 && xhr.status < 500) {
      // A malformed batch (400) can never succeed by retrying it unchanged -- drop.
      console.error('enshu: batch rejected (' + xhr.status + '), dropping ' + sent.length + ' event(s)');
      showDeliveryError('Some grades failed to save (status ' + xhr.status + '). Reload the page and try again.');
      return;
    }
```

`clearDeliveryError()` intentionally sits inside the `successful` branch, not gated on "no
rejected results this time" — a batch that arrives with zero rejects is evidence the pipe is
healthy again, which is what should retract a stale warning from an earlier partial failure.

No change to the 5xx/network-error backoff branch — that path already retries silently by design
(transient failures are expected and self-heal); only the two *terminal* drop paths gain a
visible signal.

## Regression test

**Server side (`internal/auth/middleware_test.go`): recommended, cheap to add.** The existing
`TestCheckOrigin`/`TestMiddleware_CSRFBlocksBeforeHandler` cover the boolean outcome fully; what's
new here is purely the log line, so a test would redirect `log`'s output (`log.SetOutput` to a
`bytes.Buffer` around the call, restored after) and assert the rejected-request case produces a
line containing the method, path, and both header values. This is the only server-side diagnostic
for the CSRF-rejection candidate, so a test here is the difference between "we'll know next time"
and "we're back to direct DB inspection next time." Suggesting it per CLAUDE.md working rule 5 —
write it only if you agree.

**Client side (`review.js`'s new banner): no automated test.** This repo has no JS test runner
(`package.json` doesn't exist) and no Playwright suite (`tests/` has no `*.spec.ts` — confirmed,
matching what the issue itself already noted). CLAUDE.md §10 priority 6 names exactly this
surface ("keyboard grading, optimistic advance, events sent") as the right layer to test at, but
building that harness for this one banner is out of scope — see Open question 2. Manual
verification instead:

1. Grade a card with the server stopped (or DevTools "offline") — confirm the banner does **not**
   appear (5xx/network path retries silently by design) but does after the backoff exhausts if the
   server stays down... actually the backoff retries forever on 5xx/network errors, so this step
   only confirms no *premature* banner; skip asserting an eventual banner for this path.
2. Grade a card, then in DevTools set the system/browser clock or intercept the request to send a
   `reviewedAt` more than 5 minutes in the future — confirm the response contains
   `"status":"rejected"` and the banner appears with that card's grade dropped.
3. Grade a second, valid card afterward — confirm the banner clears on the next successful flush.
4. Confirm the banner doesn't block reveal/grade/keyboard interaction with the rest of the page.

## CHANGELOG.md entry

Add under a new version heading (next available per "z increments with every PR" — current head
is `[0.1.20]`):

```
## [0.1.21] - 2026-08-15

### Fixed
- Review grading failures (a rejected/forbidden result, or the batch POST itself failing) are now
  shown to the user in the reviewer instead of only logged to the browser console, and CSRF/Origin
  rejections are now logged server-side — a session's worth of grades could previously be lost
  with no visible failure and no server-side trace to diagnose it from
  ([#99](https://github.com/Jolls/deckshare/issues/99))
```

## Resolved decisions

1. **Trace vs merge:** merge the logging/banner fix now (option b). It's correct regardless of
   which candidate is the actual trigger, and turns any recurrence into an immediately diagnosable
   event (server log line + visible banner) instead of requiring DB spelunking again. No browser
   trace is being run as part of this PR.
2. **Playwright E2E follow-up:** file a new GitHub issue now, scoped to at least "keyboard grading
   → POST fires" as a first spec, referencing #99 as the motivating gap (CLAUDE.md §10 priority 6).
   Do this as part of landing this PR, not deferred.
3. **Regression test:** add it. Write the `middleware_test.go` test described above (redirect
   `log`'s output around a rejected-request case, assert the line contains method, path, Origin,
   and Host).
