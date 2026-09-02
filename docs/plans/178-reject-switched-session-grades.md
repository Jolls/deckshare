# #178 — Reject review grades sent under a switched-away session

> Issue: [#178](https://github.com/Jolls/enshu/issues/178) — part of
> [#175](https://github.com/Jolls/enshu/issues/175). Design context:
> [docs/plans/175-multi-user-session-switching.md](175-multi-user-session-switching.md) §7.4
> (which exists; #175's issues C/D — session sets and lock — are **not** implemented yet).

Issue B in #175's slate. It lands **before** the switcher (#179), which is what makes the defect
reachable. Nothing in this plan touches `sessions`, `auth`, or the schema.

---

## 1. The defect, restated in terms of this repo's code

`POST /api/reviews/batch` (`internal/http/review.go`) resolves the acting user from the session
cookie alone:

```go
user, ok := auth.UserFromContext(r.Context())
```

The cookie is browser-wide. Once #179 lands, another tab can change which account that cookie names
while a tab sits on `/decks/{id}/review` (or `/study`) with a queue of graded events and a 2-second
flush debounce. Those events are then authorised as the *new* user. `review.GradeBatch`'s
authorisation (`ListStudyableCards`, `grade.go` ~line 243) only asks "can this user study this
card" — for a shared deck where the new user has `can_study`, the answer is yes, and the grades are
written into the wrong user's `user_card_state` and `review_log`. That is silent, unrecoverable
corruption of training data (CLAUDE.md §2.5).

## 2. The fix, in one sentence

The reviewer page renders the acting user id into the `review.js` script tag; `review.js` appends it
to the grade POST as `?u=<uuid>` on **both** the `fetch` and `navigator.sendBeacon` paths; the
handler compares it to the session user and answers **409, writing nothing**, on mismatch. A missing
`u` is tolerated and behaves exactly as today.

Constraints that are already settled and must not be re-litigated (issue text + §7.4):

- **Query parameter, not a body field** — CLAUDE.md §10.1 ("any body field other than
  `{id, cardId, rating, reviewedAt, durationMs}` is ignored") stays literally true; `wireEvent`/
  `wireBatch`/`decodeBatch` are **not** touched.
- **Query string, not a header** — the pagehide path uses `navigator.sendBeacon`, which cannot set
  headers.
- **Rejection-only** — a mismatch can only refuse a write; a match grants nothing the session did not
  already grant. It is a staleness check, not an authorisation input, which is what keeps it clear of
  invariant §2.7.

Scope note: the guard goes on `POST /api/reviews/batch` **only**. `GET /api/reviews/next` is a read
that already 404s on anything the acting user cannot study, and a misattributed *fetch* writes
nothing. Do not add `u` there.

---

## 3. Server — `internal/http/review.go`

### 3.1 New helper, placed immediately after `unauthenticatedJSON`

`unauthenticatedJSON` currently sits at ~line 276, between `noteTypeCSS` and the
`-- Wire parsing [§2.7] --` banner comment. Add directly below it:

```go
// sessionChangedJSON answers a batch whose ?u= names an account other than the session's (#178).
// The session cookie is browser-wide, so the acting account can change under a tab still holding
// graded events; without this, a deck shared with the new account would take those grades into the
// wrong user's user_card_state and review_log -- silent, unrecoverable (CLAUDE.md §2.5). 409 rather
// than 403: nothing about the request is unauthorised, it is stale, and review.js must not retry it.
func sessionChangedJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "session_changed"})
}

// actingUserMatches reports whether the optional ?u= query parameter names the session user.
// Empty/absent is tolerated by design so the server side can land ahead of the client side (#178,
// docs/plans/175-multi-user-session-switching.md §11 issue B step 1). An unparseable value is a
// mismatch, not a 400: this is a rejection-only staleness check and one failure mode is enough.
func actingUserMatches(r *http.Request, userID pgtype.UUID) bool {
	raw := r.URL.Query().Get("u")
	if raw == "" {
		return true
	}
	var claimed pgtype.UUID
	if err := claimed.Scan(raw); err != nil {
		return false
	}
	return claimed.Valid && claimed.Bytes == userID.Bytes
}
```

Comparison is on `Bytes`, not on `String()`, so hyphenation/case differences in the rendered value
can never produce a spurious 409.

### 3.2 The handler

In `registerReviewRoutes`, inside `mux.Handle("POST /api/reviews/batch", ...)` (~line 156), insert
the check **between** the `UserFromContext` block and `parseBatchRequest` — before the body is read,
so a rejected batch parses nothing and starts no transaction:

```go
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			unauthenticatedJSON(w)
			return
		}
		// #178: the session cookie is browser-wide, so a tab can outlive the account it was
		// opened under. ?u= is the client asserting which account it *believes* it is grading
		// as; a mismatch refuses the write and never shapes one (§2.7).
		if !actingUserMatches(r, user.ID) {
			sessionChangedJSON(w)
			return
		}
		events, ok := parseBatchRequest(w, r)
```

No other Go file changes. `pgtype` and `encoding/json` are already imported in this file.

---

## 4. Templates

Both reviewer pages must expose the acting user id to `review.js`. Both already render `"User": user`
in their data map (`review.go` lines 64–67 and 114–118), and `db.User.ID` is a `pgtype.UUID`, which
`html/template` renders via its `String()` method — the same mechanism `data-deck-id="{{.Deck.ID}}"`
already relies on.

**`web/templates/review.html`**, last line before `{{end}}`:

```html
<script src="/static/review.js" defer data-deck-id="{{.Deck.ID}}" data-user-id="{{.User.ID}}"></script>
```

**`web/templates/study.html`**, last line before `{{end}}` (this page has no `data-deck-id` — that is
correct and unchanged, `/study` never refills):

```html
<script src="/static/review.js" defer data-user-id="{{.User.ID}}"></script>
```

Nothing else in either template changes. Do **not** add the id to `review_cards.html` — the fragment
is swapped in by htmx and would carry a stale value on refill after a switch, which is the opposite
of what the guard wants.

---

## 5. Client — `web/static/review.js`

Four edits, all inside the IIFE.

### 5.1 State

In the `state` object literal (~line 19), add after `deckId: null,`:

```js
    userId: null, // acting account at page render; sent as ?u= so a switched-away tab is refused (#178)
```

### 5.2 `init`

In `init()` (~line 36), immediately after `state.deckId = scriptTag.dataset.deckId;`:

```js
    state.userId = scriptTag.dataset.userId || '';
```

### 5.3 The URL builder + both send paths

Add this function immediately above `flush()` (i.e. after `scheduleFlush()` and above `flush()`'s
existing block comment):

```js
  // The acting account is a property of the browser-wide session cookie, not of this tab, so a
  // switch in another tab would otherwise re-attribute these grades to whoever the session now
  // names (#178). ?u= is what the server compares against the session user before writing anything;
  // it is a query parameter and not a header because the pagehide path below uses sendBeacon, which
  // cannot set headers, and not a body field because the batch body is fixed by CLAUDE.md §10.1.
  function batchURL() {
    return state.userId
      ? '/api/reviews/batch?u=' + encodeURIComponent(state.userId)
      : '/api/reviews/batch';
  }
```

In `flush()` (~line 376), replace the literal:

```js
    fetch('/api/reviews/batch', {
```

with:

```js
    fetch(batchURL(), {
```

In `flushOnUnload()` (~line 403), replace:

```js
    if (!navigator.sendBeacon('/api/reviews/batch', blob)) {
```

with:

```js
    if (!navigator.sendBeacon(batchURL(), blob)) {
```

### 5.4 `onBatchSettled` — handle 409 explicitly

409 would already fall into the existing generic `status >= 400 && status < 500 && status !== 401`
drop branch, which is the correct *behaviour* (a retry can never succeed) but the wrong *message*
("Reload the page and try again" is misleading when the account changed). Insert a dedicated branch
**immediately before** that generic branch (~line 440):

```js
    if (status === 409) {
      // The session now belongs to another account (#178). The events cannot be retried -- retrying
      // would either be refused again or, worse, land under the wrong user if the tab reloaded.
      console.error('enshu: batch refused (409, session changed), dropping ' + sent.length + ' event(s)');
      showDeliveryError('This tab is signed in as a different account now, so these grades were not saved. Reload the page to keep studying.');
      return;
    }
```

Nothing else in `review.js` changes; the backoff/retry path, `takePending`, and `applyFreshPreview`
are untouched.

---

## 6. E2E — `tests/e2e/review-grading.spec.ts` (must-fix, not optional)

The existing response predicate matches on an exact URL suffix and **will break** the moment the
client appends the query string:

```ts
    (res) => res.url().endsWith('/api/reviews/batch') && res.request().method() === 'POST',
```

Replace with a pathname match so it is query-string agnostic:

```ts
    (res) => new URL(res.url()).pathname === '/api/reviews/batch' && res.request().method() === 'POST',
```

Then extend the same test, after the existing `expect(result.status).toBe('applied');`, to assert the
guard is actually wired end to end:

```ts
  // #178: the grade POST carries the acting account id, and the server accepted it (200 above).
  const sentURL = new URL(response.url());
  expect(sentURL.searchParams.get('u')).toMatch(/^[0-9a-f-]{36}$/);
```

That covers the `fetch` path. The `sendBeacon` path is not driven by Playwright here (it needs a
page teardown to fire and returns no response to await); it shares `batchURL()` with `fetch`, and its
correctness is asserted by code review of the one-line change rather than by a browser test. Do not
add a beacon e2e scenario for this issue.

---

## 7. Tests — `internal/http/review_test.go`

One new test, placed **immediately after** `TestReviewBatch_ClientCannotWriteSchedulingState` (which
ends at line 221) and before the `-- #142 --` banner, so the §10.1 material stays contiguous. It uses
only existing helpers: `beginTx`, `newTestHandler`, `loginCookie`, `testEmail`, `setupOneCard`,
`newTestEventID`, `doJSON`, `countRows`.

The premise the test must establish is the *dangerous* case, not the already-covered forbidden one:
B has `can_view` **and** `can_study` on A's deck, so `GradeBatch` would happily accept the batch
without the guard.

```go
// -- §10.1 continued (#178): a batch sent under a session that has switched accounts ------------

// The session cookie is browser-wide, so a tab left open as A keeps grading after the browser
// switches to B. With B holding can_study on the shared deck, GradeBatch would authorise and write
// those grades into B's user_card_state and review_log -- plausible-looking, unrecoverable (§2.5).
// The ?u= staleness check must refuse the batch outright; nothing is written for either account.
func TestReviewBatch_RejectsSwitchedAwaySession(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	handler, a := newTestHandler(t, tx, auth.Config{}, func() time.Time { return clock })

	ownerEmail := testEmail()
	ownerCookie := loginCookie(t, tx, a, ownerEmail, "correct-horse-battery")
	deckID, cardID := setupOneCard(t, tx, handler, ownerCookie)

	sharedEmail := testEmail()
	sharedCookie := loginCookie(t, tx, a, sharedEmail, "correct-horse-battery")

	var ownerID, sharedID, deckUUID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, ownerEmail).Scan(&ownerID); err != nil {
		t.Fatalf("lookup owner: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, sharedEmail).Scan(&sharedID); err != nil {
		t.Fatalf("lookup shared user: %v", err)
	}
	if err := deckUUID.Scan(deckID); err != nil {
		t.Fatalf("scan deck id: %v", err)
	}
	// can_study, deliberately: without it the batch would be answered `forbidden` by the existing
	// authorisation and this test would pass for the wrong reason.
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_study) VALUES ($1, $2, true, true)`,
		deckUUID, sharedID); err != nil {
		t.Fatalf("grant can_study: %v", err)
	}

	body := func() string {
		return fmt.Sprintf(`{"events":[{"id":%q,"cardId":%q,"rating":3,"reviewedAt":%q}]}`,
			newTestEventID(), cardID, clock.Format(time.RFC3339Nano))
	}

	t.Run("mismatched u is refused and writes nothing", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch?u="+ownerID.String(), body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE card_id = $1`, cardID); n != 0 {
			t.Errorf("review_log rows for this card = %d, want 0 (for either account)", n)
		}
		if n := countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, cardID); n != 0 {
			t.Errorf("user_card_state rows for this card = %d, want 0", n)
		}
	})

	t.Run("unparseable u is refused", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch?u=not-a-uuid", body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE card_id = $1`, cardID); n != 0 {
			t.Errorf("review_log rows for this card = %d, want 0", n)
		}
	})

	// The two accepting cases: matching u, and no u at all (the server side lands before the client
	// side, so a missing u must behave exactly as it does today).
	t.Run("matching u is applied", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch?u="+sharedID.String(), body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"applied"`) {
			t.Fatalf("status = %d, body = %s, want 200 applied", w.Code, w.Body.String())
		}
	})

	t.Run("absent u is applied", func(t *testing.T) {
		w := doJSON(handler, "POST", "/api/reviews/batch", body(), sharedCookie, "http://example.com")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"applied"`) {
			t.Fatalf("status = %d, body = %s, want 200 applied", w.Code, w.Body.String())
		}
	})
}
```

Notes for the implementing agent:

- Subtest order matters: the two rejecting subtests assert zero rows for the card and must run
  before the two accepting ones write any.
- Every assertion is scoped to `card_id = $1` for the card this test created — no table-wide
  `count(*)`, no unscoped `LIMIT 1` (CLAUDE.md §16).
- `context`, `fmt`, `strings`, `pgtype`, `auth`, `time`, `net/http` are all already imported in
  `review_test.go`; no import changes.
- This is a DB-backed test: `DATABASE_URL` must be exported or it silently skips (CLAUDE.md §16).
  Confirm with `go test ./internal/http/ -run RejectsSwitchedAway -v` and check for `--- SKIP`.

### 7.1 Template-render assertion

Add to the existing `TestReviewPage_HiddenCardShape` (line 729) — it already fetches
`/decks/{id}/review` as `pageResp` — immediately after the existing `class="enshu-card card"`
assertion:

```go
	// #178: review.js reads the acting account id off its own script tag and sends it as ?u=.
	if !strings.Contains(pageResp.Body.String(), `data-user-id="`) {
		t.Error("review page must render the acting user id onto the review.js script tag (#178)")
	}
```

Add the same three lines to `TestStudyAll_MixesAcrossDecks` (line 929) against whatever variable
holds that test's `GET /study` response body, since `study.html` carries the attribute too.

---

## 8. Docs

**`docs/routes.md`** — line 124, the `POST /api/reviews/batch` row. Append to the end of the
description cell, before the closing `|`:

> Optional query param `u` (uuid, #178): the acting account the client believes it is grading as —
> a mismatch against the session user answers **409** and writes nothing, an absent value is
> tolerated. Rejection-only; it can never cause or shape a write (§2.7).

**`docs/architecture.md` §6** — after the paragraph ending "…because the server is the only place
`Repeat` ever runs." (line 479), insert:

> The batch POST also carries the acting account id as a `?u=` query parameter (#178). The session
> cookie is browser-wide, so a tab can outlive the account it was opened under; the server compares
> `u` to the session user and answers 409 without writing when they differ, and tolerates its
> absence. It is a staleness check, not an authorisation input — a mismatch can only refuse a write,
> and a match grants exactly what the session already granted, which is why it does not bend §2.7.
> It is deliberately a query parameter rather than a body field (the batch body is fixed by
> CLAUDE.md §10.1) or a header (`navigator.sendBeacon` cannot set headers).

**`CHANGELOG.md`** — new entry at the top, version `0.2.20`, dated the merge date:

```
## [0.2.20] - YYYY-MM-DD

### Security
- Review grades are now refused when the browser's signed-in account changed after the reviewer
  page was opened: the grade POST carries the acting account id and the server answers 409 and
  writes nothing on a mismatch, instead of silently attributing the reviews to the new account
  ([#178](https://github.com/Jolls/enshu/issues/178)).
```

Tag `v0.2.20` after the version-bump commit (CLAUDE.md §14).

---

## 9. Invariant / CLAUDE.md check

| Rule | Effect |
|---|---|
| §2.5 `review_log` append-only training data | Protected, not bent — this stops rows being written under the wrong user. No `DELETE` path added. |
| §2.7 client asserts, server derives | Preserved. `u` is rejection-only; it is never read into `user_card_state` or `review_log` and never reaches `internal/review`. |
| §10.1 body-field test | Untouched and still literally true — `wireEvent`/`decodeBatch` are not modified. |
| §17 server-side recompute path | Not touched. `internal/review` and `internal/fsrs` see no change at all. |
| CSP (`internal/http/security.go`) | No change. `connect-src 'self'` already covers the same origin with a query string; no test in `security_test.go` needs updating. |

## 10. Build order and verification

1. Server: §3 + the Go test in §7. *Verify:* `go build ./...`, `go vet ./...`, `golangci-lint run`,
   `DATABASE_URL=... go test ./internal/http/ -run 'ReviewBatch' -v` (confirm no `SKIP`).
2. Client + templates: §4, §5, §7.1. *Verify:* `go test ./internal/http/ -run 'HiddenCardShape|StudyAll_MixesAcrossDecks'`.
3. E2E: §6. *Verify:* the Playwright spec, if the user runs it; otherwise code review — note that
   step 6's predicate fix is required whether or not the suite is run in this session.
4. Docs: §8.

Manual verification steps to hand the user (CLAUDE.md §14: do not start the server to check):
open a deck's reviewer, grade a card, and confirm in devtools that the POST goes to
`/api/reviews/batch?u=<your user id>` and returns 200; then close the tab mid-session and confirm the
pagehide beacon goes to the same URL. The 409 path is not manually reachable until #179 lands — the
Go test is the only way to exercise it today, which is exactly why it is written first.

---

## 11. Open questions

None blocking; every decision above is resolved. Two were judgment calls resolved in the plan rather
than left open, recorded here in case the implementing session disagrees:

1. **Unparseable `u` → 409 (chosen) vs 400.** Chosen 409 so the endpoint has exactly one rejection
   mode and the client needs exactly one branch; a garbage `u` certainly does not name the session
   user, so treating it as a mismatch is truthful. Alternative: `badRequest(w)` (400) — also correct,
   and `review.js` would drop the batch either way via its generic 4xx branch, but it adds a second
   failure mode for no gain.
2. **Explicit 409 branch in `onBatchSettled` (chosen) vs relying on the existing generic 4xx drop.**
   Chosen the explicit branch purely for the user-facing message; behaviour is identical either way.
   Alternative: omit §5.4 entirely and accept the "Reload the page and try again" wording — three
   fewer lines, slightly misleading copy.
