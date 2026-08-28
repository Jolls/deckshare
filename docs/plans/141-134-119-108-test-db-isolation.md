# Plan: fix DB-seed-induced test failures (#134, #119, #108, #141)

## 0. Summary of the root cause

`cmd/seed/main.go` (invoked unconditionally at the end of `.claude/skills/run-app/reset-db.sh`) commits, to the shared `local` Postgres database: 1 user (`test@test.com`), 1 session (a side effect of calling `authSvc.Signup` directly), 2 decks, 5 notes, and 5 cards (3 Basic + 2 Cloze, one card each). Every DB-backed Go test runs inside its own `pgx.Tx` that is rolled back at the end (`beginTx` in `internal/auth/session_test.go`, `internal/http/auth_test.go`), so tests are isolated from *each other*, but under Postgres's default READ COMMITTED isolation each fresh statement in that tx still sees rows already **committed** by the separate seeding process. Two distinct bug shapes result:

1. **Absolute/table-wide assertions that assume the table starts empty** (`SELECT count(*) FROM sessions` asserting `0`, etc.) — issues #134, #119.
2. **Unscoped `SELECT ... LIMIT 1` with no `WHERE`/`ORDER BY`** on a shared table, which can return a seeded row instead of the row the test just created — issue #108, and (per the sweep below) present in more places than #108 named.

`#141`'s six "forbidden instead of applied" failures are a *symptom* of bug shape 2, not a production bug — confirmed below by reading `GradeBatch`, `ListStudyableCards`, and the `deck_access` migration.

## 1. Root-cause findings, confirmed/refuted per test

### 1.1 Production code is clean (no fix needed, not touched)

- `internal/db/decks.go`'s `CreateDeckWithAccess` always calls `GrantFullDeckAccess` for the creating owner in the same tx as `CreateDeck` — every deck a test creates via the HTTP routes gets a full-access `deck_access` row for its owner, atomically.
- `internal/db/queries/reviews.sql`'s `ListStudyableCards` (lines 387–392) correctly joins `cards` to `deck_access` requiring `can_view AND can_study` for the grading user — this matches `migrations/00007_deck_access.sql`'s column semantics exactly.
- `internal/review/grade.go`'s `GradeBatch` (line 181 on) marks a card `forbidden` only when it's absent from `ListStudyableCards`'s result — correct given the above.
- **Conclusion: a card the test's own deck owner created is never actually inaccessible to that owner.** The only way `GradeBatch` returns `forbidden` for "the owner's own card" is if the `cardID` the test passes in doesn't actually belong to a deck the owner has access to — i.e. the test fixture resolved the wrong card. This rules out the "bug is in the query" possibility #141 raised. **No production code changes in this plan.**

### 1.2 `internal/http/review_test.go`'s `setupOneCard` (#108)

Confirmed at current line 53 (not line 211 as the stale issue text says — that was `session_test.go`'s old line number, unrelated file):
```go
if err := tx.QueryRow(context.Background(), `SELECT id FROM cards LIMIT 1`).Scan(&cardID); err != nil {
```
No `WHERE`, no `ORDER BY`. `deckID` is already in scope. Apply #108's own suggested fix as-is.

### 1.3 A second, previously-unidentified sibling bug: `setupSecondCard` (new finding, same file)

`setupSecondCard` (line 387) is used by two of #141's named tests to create a *second* card for the same owner. It has an **unscoped deck lookup** that #108 didn't call out:
```go
var deckID, noteTypeID string
if err := tx.QueryRow(ctx, `SELECT id FROM decks LIMIT 1`).Scan(&deckID); err != nil {
```
Worse than `setupOneCard`'s bug: the function isn't even given the caller's deck — it blindly grabs *some* deck from the whole table. Under a seeded DB this can resolve to a deck owned by `test@test.com`, not the test's own fresh owner, so the subsequent `POST {deckPath}/notes` 403/404s for a *different* reason than "wrong card" — "wrong deck entirely." The `cards` lookup at line 406 (`SELECT id FROM cards ORDER BY id DESC LIMIT 1`) is fine as written: cards use UUIDv7 primary keys (time-ordered), so `ORDER BY id DESC LIMIT 1` deterministically returns the row this call just inserted regardless of what else is in the table — no fix needed there.

### 1.4 Per-test verdict for #141's six named tests

All six call `setupOneCard`; two of them additionally call `setupSecondCard`. All six are the same root cause — no test needs its own distinct fix.

| Test | Calls `setupOneCard` | Calls `setupSecondCard` | Verdict |
|---|---|---|---|
| `TestReviewBatch_Idempotency` (all 3 subtests) | yes (all 3) | yes (subtest 2, "two-event batch...") | **Confirmed**: §1.2 (all 3) + §1.3 (subtest 2) |
| `TestReviewBatch_OutOfOrderReplay` | yes | yes | **Confirmed**: §1.2 + §1.3 |
| `TestReviewBatch_ReviewedAtClampAndReject` | yes | no | **Confirmed**: §1.2 only |
| `TestReviewRoutes_AccessControl` | yes | no | **Confirmed**: §1.2 only — the owner's final grade at the end of the test is the one that spuriously comes back `forbidden`; the stranger/view-only `forbidden` assertions earlier in the same test are the test's actual intent, not a bug |
| `TestReviewPage_HiddenCardShape` | yes | no | **Confirmed**: §1.2 only (already established by #108 itself) |
| `TestReviewNext_ExcludesCardsReviewedThisStudyDay` | yes | no | **Confirmed**: §1.2 only (already established by #108 itself) |

**Answer to the overlap question: (a) — all six of #141's tests share the #108/#134 root cause.** No test in this set needs a fix beyond §1.2 and §1.3. `#141` is fully resolved by fixing `setupOneCard` and `setupSecondCard`; it identifies no distinct production or fixture bug of its own.

**Bonus finding, not in #141's list but same cause:** `TestReviewBatch_ClientCannotWriteSchedulingState` (CLAUDE.md §10.1's highest-priority test) and `TestReviewBatch_MalformedIs400` also call `setupOneCard` and are exposed to the identical bug; they just weren't hit in whoever ran #141's repro (this depends on Postgres's physical row layout, which is why the issue calls the failure "intermittent"). They'll become reliably green as a side effect of this fix — worth confirming in the PR's test run, no separate action needed.

### 1.5 Additional unscoped-`LIMIT 1` instances found by the requested grep sweep (#108's ask)

Grepped all `*_test.go` for `LIMIT 1` and bare `count(*)`. Beyond `review_test.go`, found the identical unscoped-`decks`/`notes`-`LIMIT 1` pattern in two more files, all in `internal/http`. In every case the fix is the same mechanical shape as #108's own fix (and in most cases *cheaper*: the deck path is already sitting in a discarded return value or an unused `Location` header, so the query can be deleted entirely, not just scoped):

- `internal/http/aiimport_test.go`: 5 occurrences of `SELECT id FROM decks LIMIT 1` (lines 21, 78, 106, 134, 174) — 4 of them immediately follow a call to `setupDeckAndNoteType(t, handler, cookie)` whose return value (`deckPath`) is thrown away; the 5th follows a raw `POST /decks` whose response (`w`) is also discarded before the Location header is read.
- `internal/http/notes_test.go`: `SELECT id FROM decks LIMIT 1` (line 229) — same discarded-`deckPath` shape; `SELECT id FROM notes LIMIT 1` (lines 47, 113, 263) — `deckPath`/`deckID` is available in every case, just needs threading through as a `WHERE deck_id = $1` scope.

These are equally mechanical and low-risk (same idiom already proven at `setupOneCard`'s existing `deckPath`-derivation line), so this plan fixes them too, not just notes them.

**Checked and ruled safe (no change needed):**
- `internal/apkg/dbwrite_test.go:318` and `internal/http/notetypes_test.go:68` — both already carry a `WHERE ... ORDER BY ... LIMIT 1`, properly scoped.
- All `count(*)` calls in `internal/db/*_test.go` and `internal/http/decks_test.go`/`settings_test.go`/`notetypes_test.go` — scoped by a specific id or a distinctive literal (e.g. `display_name = 'Changed'`) that the seeded row can never match.
- `internal/db/deletion_test.go:464,470` (`review_log` before/after) — a relative diff over the same tx; immune to any static pre-existing row count regardless of seeding.

### 1.6 Correction to issue #119's own text

Reading `internal/auth/session_test.go` and `internal/http/auth_test.go` directly (rather than trusting the issue's test list) gives:

- **`TestLogin_UnknownEmail`** (`session_test.go:201-213`): genuinely broken. Bare `SELECT count(*) FROM sessions` asserting `n == 0`, no baseline, no scoping. **Confirmed.**
- **`TestLogout_ValidSessionDeletesRow`** (`auth_test.go:235-250`): genuinely broken, same shape. **Confirmed.**
- **`TestLogin_WrongPassword`** (`session_test.go:181-199`): the issue names this as broken, but reading the code shows it already uses a **before/after relative diff** (`before := countRows(...)` right after `Signup`, then asserts `after == before`), which is immune to any static pre-existing seed rows — whatever the seeded baseline is, it cancels out of the diff. `git blame` confirms this code is unchanged since the original commit (`9067008`), so this isn't a stale reference to an already-fixed bug either — the issue's list is simply imprecise here. **Refuted — no fix applied.** (`TestChangePassword_InvalidatesOtherSessions` at line 466 is likewise already correctly scoped by `WHERE user_id = $1` and needs no change.)

This gives exactly 2 required fixes for #119, not the larger list the issue implies.

## 2. Resolved decision (was: open question)

**Which of #134's candidate fixes to apply.**

Options considered: split seeding behind a `--seed` flag (option 1); a dedicated always-empty `enshu_test` database (option 2, no evidence this was ever actually built despite the issue's claim); scope every affected test's own assertions so tests never depend on the DB being empty (option 3); a loud skip when `DATABASE_URL` is unset (option 4, independent of the others).

**Decision: option 3, applied fully — no `reset-db.sh` or `cmd/seed` change.** User's framing: the tests should work correctly against a seeded DB, not require an empty one. This is also simply *more* of what #3.1–3.5 already do for #108/#119 regardless of which option was picked (those fixes scope test lookups/counts to the rows the test itself created) — so extending that same idiom to the 6 remaining bare-count assertions in §5 finishes the job rather than adding a second mechanism. `reset-db.sh` keeps seeding unconditionally, exactly as it does today; §3.6/§3.7 (the `--seed` flag split) are **dropped from this plan**. §3.8's CLAUDE.md update is kept but reworded: instead of documenting a new flag, it records that DB-backed tests are now robust to a seeded DB, so the "circular trap" #134 named (reset-db.sh's advice causing the very failures it's meant to fix) is resolved by removing the tests' fragile assumption, not by giving reset-db.sh a mode that avoids triggering it. The "loud skip on unset `DATABASE_URL`" docs addition (option 4) is orthogonal and kept as-is.

## 3. Per-file changes, in application order

### 3.1 `internal/http/review_test.go` — fix `setupOneCard` (#108, exactly as spelled out)

```go
	if err := tx.QueryRow(context.Background(), `SELECT id FROM cards LIMIT 1`).Scan(&cardID); err != nil {
```
→
```go
	if err := tx.QueryRow(context.Background(),
		`SELECT id FROM cards WHERE deck_id = $1 ORDER BY id DESC LIMIT 1`, deckID,
	).Scan(&cardID); err != nil {
```

### 3.2 `internal/http/review_test.go` — fix `setupSecondCard` (new finding, §1.3)

Thread the caller's `deckID` in instead of guessing:
```go
func setupSecondCard(t *testing.T, tx pgx.Tx, handler http.Handler, cookie *http.Cookie) (deckPath, cardID string) {
	t.Helper()
	ctx := context.Background()
	var deckID, noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM decks LIMIT 1`).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	deckPath = "/decks/" + deckID
```
→
```go
func setupSecondCard(t *testing.T, tx pgx.Tx, handler http.Handler, cookie *http.Cookie, deckID string) (deckPath, cardID string) {
	t.Helper()
	ctx := context.Background()
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	deckPath = "/decks/" + deckID
```
(Rest of the function body unchanged — the `cards ORDER BY id DESC LIMIT 1` lookup at the end is already safe, see §1.3.)

Update both call sites to capture and pass the deck id they were already discarding:

In `TestReviewBatch_Idempotency`, subtest `"two-event batch sent shuffled vs sorted"` (around line 261 and 274):
```go
		_, cardA := setupOneCard(t, tx, handler, cookie)
		...
		_, cardB := setupSecondCard(t, tx, handler, cookie)
```
→
```go
		deckA, cardA := setupOneCard(t, tx, handler, cookie)
		...
		_, cardB := setupSecondCard(t, tx, handler, cookie, deckA)
```

In `TestReviewBatch_OutOfOrderReplay` (around line 331 and 371):
```go
	_, cardID := setupOneCard(t, tx, handler, cookie)
	...
	_, controlCard := setupSecondCard(t, tx, handler, cookie)
```
→
```go
	deckID, cardID := setupOneCard(t, tx, handler, cookie)
	...
	_, controlCard := setupSecondCard(t, tx, handler, cookie, deckID)
```

### 3.3 `internal/http/aiimport_test.go` — fix the 5 unscoped `decks LIMIT 1` lookups (§1.5)

In `TestAIImportRoutes_GoldenPath`, `TestAIImportRoutes_BadLine_AllOrNothing`, `TestAIImportRoutes_FieldCountMismatch_400` — identical shape in each:
```go
	setupDeckAndNoteType(t, handler, cookie)
	var deckID, noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM decks LIMIT 1`).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck: %v", err)
	}
```
→
```go
	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
```
(`strings` is already imported in this file.)

In `TestAIImportRoutes_AccessControl`, same shape but with `ownerCookie`:
```go
	setupDeckAndNoteType(t, handler, ownerCookie)
	var deckID, noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM decks LIMIT 1`).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck: %v", err)
	}
```
→
```go
	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
```

In `TestAIImportRoutes_ClozeWithoutMarkers_400` (doesn't call `setupDeckAndNoteType`, does its own raw `POST /decks`):
```go
	doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	var deckID string
	if err := tx.QueryRow(ctx, `SELECT id FROM decks LIMIT 1`).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck: %v", err)
	}

	ntBody := url.Values{}
	...
	w := doRequest(handler, "POST", "/note-types", ntBody.Encode(), cookie, "http://example.com")
```
→
```go
	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckID := strings.TrimPrefix(w.Header().Get("Location"), "/decks/")

	ntBody := url.Values{}
	...
	w = doRequest(handler, "POST", "/note-types", ntBody.Encode(), cookie, "http://example.com")
```
(Note the later `w :=` becomes `w =` — `w` is already declared.)

### 3.4 `internal/http/notes_test.go` — fix the unscoped `decks`/`notes LIMIT 1` lookups (§1.5)

Add `"strings"` to the import block (alphabetically, after `"net/url"`).

`TestNoteRoutes_GoldenPath`:
```go
	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID string
```
→
```go
	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
```
and
```go
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes LIMIT 1`).Scan(&noteID); err != nil {
```
→
```go
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE deck_id = $1 ORDER BY id DESC LIMIT 1`, deckID).Scan(&noteID); err != nil {
```

`TestNoteRoutes_ClozeGeneratesCardsByOrdinal`: same two edits — add `deckID := strings.TrimPrefix(deckPath, "/decks/")` right after `deckPath := w.Header().Get("Location")`, then scope the `notes LIMIT 1` query the same way.

`TestNoteRoutes_AccessControl`: same two edits — add the `deckID` derivation right after `deckPath := setupDeckAndNoteType(t, handler, ownerCookie)`, then scope its `notes LIMIT 1` query the same way.

`TestNoteRoutes_ViewOnlyCollaborator_CannotAddNote`:
```go
	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	var deckID, viewerID string
	if err := tx.QueryRow(ctx, `SELECT id FROM decks LIMIT 1`).Scan(&deckID); err != nil {
		t.Fatalf("lookup deck: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, viewerEmail).Scan(&viewerID); err != nil {
```
→
```go
	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var viewerID string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, viewerEmail).Scan(&viewerID); err != nil {
```
(eliminates the query entirely, deckID was already fully determined by `deckPath`).

### 3.5 `#119` fixes

`internal/auth/session_test.go`, `TestLogin_UnknownEmail`:
```go
func TestLogin_UnknownEmail(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	_, _, err := s.Login(ctx, "1.2.3.4", testEmail(), "any-password-at-all")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM sessions`); n != 0 {
		t.Errorf("session row count = %d, want 0", n)
	}
}
```
→ (matches the already-correct before/after idiom used two tests above it, `TestLogin_WrongPassword`):
```go
func TestLogin_UnknownEmail(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()
	before := countRows(t, tx, `SELECT count(*) FROM sessions`)

	_, _, err := s.Login(ctx, "1.2.3.4", testEmail(), "any-password-at-all")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
	if after := countRows(t, tx, `SELECT count(*) FROM sessions`); after != before {
		t.Errorf("session row count = %d, want unchanged (%d)", after, before)
	}
}
```
(A before/after diff rather than a `WHERE user_id = $1` scope, because this test never creates a user to scope by — that's the point of the test.)

`internal/http/auth_test.go`, `TestLogout_ValidSessionDeletesRow`:
```go
func TestLogout_ValidSessionDeletesRow(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/logout", "", cookie, "http://example.com")
	if w.Code != 303 {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM sessions`); n != 0 {
		t.Errorf("session row count = %d, want 0", n)
	}
}
```
→ (issue #119 explicitly prefers scoping to the test's own rows):
```go
func TestLogout_ValidSessionDeletesRow(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/logout", "", cookie, "http://example.com")
	if w.Code != 303 {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	var userID string
	if err := tx.QueryRow(context.Background(), `SELECT id FROM users WHERE email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	if n := countRows(t, tx, `SELECT count(*) FROM sessions WHERE user_id = $1`, userID); n != 0 {
		t.Errorf("session row count = %d, want 0", n)
	}
}
```

### 3.6 `internal/http/aiimport_test.go` and `internal/http/notes_test.go` — scope the 6 remaining bare-count assertions (§5, resolved decision §2)

Six assertions count an entire table and compare to an exact literal (`== 2`, `== 0`, etc.), which only holds if the DB happens to be empty of unrelated rows:

- `internal/http/aiimport_test.go` lines 62, 65, 93, 121, 200 (bare `count(*)` on `notes` and/or `cards`)
- `internal/http/notes_test.go` line 142 (bare `count(*)` on `notes` or `cards`)

For each: read the surrounding test function, identify the `deckID` (or equivalent scoping id) already available in that test — every one of these tests creates its own deck via `setupDeckAndNoteType` or an explicit `POST /decks`, and by this point in the plan (§3.3/§3.4) that deck id is already being captured via `strings.TrimPrefix(deckPath, "/decks/")` in most of these same test functions. Rewrite each bare `SELECT count(*) FROM notes` / `SELECT count(*) FROM cards` to `SELECT count(*) FROM notes WHERE deck_id = $1` / `... FROM cards WHERE deck_id = $1` (join through `notes` to `cards` via `note_id`/`deck_id` as the existing schema requires, matching whatever join shape `internal/db/queries` already uses elsewhere for deck-scoped card counts) passing that test's own `deckID`, and update the expected literal if the deck-scoped count differs from the old table-wide expectation (it shouldn't, since these tests already expect their own rows — the literal was only ever coincidentally validated against an empty table).

Do not touch `internal/apkg/dbwrite_test.go:526` or `internal/http/review_test.go:516,602` (the `media_blobs`/`review_log` counts noted in §5) — those are not currently broken (nothing in this batch's scope writes to those tables), and scoping them without a concrete failure to fix would be speculative churn outside CLAUDE.md's "Surgical Changes" rule. Leave them as a documented latent risk (§5), same as before.

### 3.7 `CLAUDE.md` §16

```
- **Stale local Postgres data breaks DB-backed tests.** The `compose.yaml` `pgdata` volume
  persists across `run-app` sessions; leftover rows from manual testing make `go test ./...`
  fail in ways that look like code bugs. Run
  `bash .claude/skills/run-app/reset-db.sh` to wipe the volume, reapply migrations, and reseed
  a test user/decks — don't improvise `docker compose down -v` by hand.
```
→
```
- **DB-backed tests are scoped to their own rows, not the whole table.** They tolerate a
  seeded/populated database — `bash .claude/skills/run-app/reset-db.sh` (wipe the volume,
  reapply migrations, reseed a test user/decks) is safe to run before a test session; you do
  not need an empty database for `go test ./...` to pass. If a new DB-backed test asserts a
  table-wide `count(*)` or picks a row via unscoped `LIMIT 1`, that's a bug in the test — scope
  it to the row(s)/user the test itself created (see #134/#119/#108/#141).
- **A green `go test ./...` is not proof DB-backed tests ran.** With `DATABASE_URL` unset, every
  DB-backed test calls `t.Skip` and the suite still reports `ok`. Export `DATABASE_URL` (see
  `.env.example`) before trusting a DB-backed result, and if in doubt run
  `go test ./... -v | grep -i skip` (PowerShell: `... | Select-String -Pattern skip`) to confirm
  nothing silently skipped.
```

`reset-db.sh` and `SKILL.md` are **not modified** by this plan — seeding stays unconditional, exactly as today.

## 4. Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run` (CLAUDE.md §14 pre-commit sequence).
- `bash .claude/skills/run-app/reset-db.sh` (seeds as it always has), then `DATABASE_URL=... go test ./...` — should be fully green. This is the strongest check: it's both #134's circular-trap repro (the documented remedy no longer breaks the suite) and #141's exact repro command from the issue (`go test ./internal/http/... -run 'TestReviewBatch_...|...'`), run against a seeded DB.
- `DATABASE_URL= go test ./... -v | grep -i skip` — sanity-check the new §16 guidance actually surfaces skips.

## 5. Latent risks noted, not fixed (currently non-failing — nothing in this batch's scope writes to these tables, so there's no concrete failure to fix)

- `internal/apkg/dbwrite_test.go:526` (`SELECT count(*) FROM media_blobs`, asserts `== 1`) — bare table-wide count, but `cmd/seed` never writes to `media_blobs`, so this isn't currently broken. Fragile if seed ever gains a media fixture.
- `internal/http/review_test.go:516,602` (`SELECT count(*) FROM review_log`, asserts `== 0`) — same shape, same reasoning: `cmd/seed` never grades a card, so not currently broken.
### Critical Files for Implementation
- internal/http/review_test.go
- internal/http/aiimport_test.go
- internal/http/notes_test.go
- internal/auth/session_test.go
- internal/http/auth_test.go
- CLAUDE.md
