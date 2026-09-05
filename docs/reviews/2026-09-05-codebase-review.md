# Codebase review — 2026-09-05

Whole-repo pass at `cb1ee33` (v0.2.30): simplification, missing features, security, and general
correctness. Not tied to an issue and not a plan — a snapshot of what a fresh pass finds, so it
can be triaged into issues.

**Method.** Read every package under `internal/`, `cmd/`, `web/`, all 19 `.sql` query files, all
18 migrations, `CLAUDE.md`, `docs/architecture.md`, `docs/routes.md`. Ran `go build ./...`,
`go vet ./...`, and `go test ./...` against a populated local database. Cross-checked findings
against the open issue list so nothing already tracked is reported as new.

**Headline.** This is unusually disciplined code. The invariants in CLAUDE.md §2 are held in the
code, not just written down — the batch endpoint really does read five fields, the locks really
are sorted, `internal/fsrs` really is pure, the sanitiser really is an allowlist built from
nothing. Zero `TODO`/`FIXME` in the tree. The findings below are mostly *around* that core:
operability, input-validation gaps at the edges, and product surface that isn't built yet.

Nothing here is a violation of §2. One finding (S1) is a live operational defect worth fixing
before anything else.

---

## 1. Security

### S1 — Every 500 is silently swallowed · `sev: high`, `area: security`

`serverError(w)` ([respond.go:16](../../internal/http/respond.go#L16)) takes only the
`ResponseWriter`. It is called **86 times** across `internal/http/` and never receives, logs, or
records the error that caused it. There is also no request logging of any kind — the only
`log.Printf` calls in the whole HTTP path are the CSRF rejection, session-renewal failure, and
template-render failure.

Consequences, in order of how much they hurt:

- A production 500 is undiagnosable. No stack, no message, no request path, nothing.
- An attacker probing for a crash gets identical, quiet 500s and leaves no trace an operator
  could review. There is no record of failed authorisation either — a `forbidden` grade result, a
  404-collapsed access denial, and a 403 CSRF rejection are indistinguishable from normal traffic.
- The `review_log`-corruption reflex CLAUDE.md §15 calls out ("invisible until an optimiser fit
  comes out wrong") has no detection surface: if `GradeBatch` starts erroring, the client retries
  with backoff and the operator learns nothing.

**Fix:** change the signature to `serverError(w, r, err)`, log with `log/slog` at error level with
method/path/user id, and add a small logging middleware beside `securityHeaders` emitting one line
per request (method, path, status, duration, user id). Structured logging also gives the classroom
deployment the "who did what" trail an instructor-facing product will eventually be asked for.

### S2 — Rate limiting is defeated by the reverse proxy the deployment requires · `sev: high`, `area: security`

`clientIP` ([auth.go:114](../../internal/http/auth.go#L114)) reads `r.RemoteAddr` only — no
`X-Forwarded-For` / `X-Real-IP` handling. But `.env.example`'s `ORIGIN` documentation states the
expected deployment is *behind a reverse proxy that terminates TLS*, and it must be: the session
cookie is `__Host-`-prefixed and `Secure`, so plain HTTP cannot work.

Behind that proxy every request's `RemoteAddr` is the proxy. So:

- `loginIPLimit` (20 / 15 min) becomes a **global** budget. Twenty bad logins from anyone locks
  `/login` for every user of the instance for fifteen minutes. Trivially weaponised, and also
  reachable by accident on a busy classroom morning.
- `signupIPLimit` (5 / hour) likewise caps the whole instance at five signups an hour.
- The per-email bucket still works (keyed on email), so credential-stuffing protection survives;
  what breaks is availability.

**Fix:** a `TRUSTED_PROXY` config knob, reading the client IP from the header only when the
immediate peer is trusted. Never read `X-Forwarded-For` unconditionally — that hands the attacker
the ability to forge the limiter key and bypass it entirely. Default (unset) stays `RemoteAddr`.

### S3 — Registration is unconditionally open, with no way to close it · `sev: high`, `area: security`

`POST /signup` is public and there is no config to disable it, no invite mechanism, no admin role,
and no first-user bootstrap. The README/architecture target includes StartOS deployment and Tor
exposure (`ORIGIN` explicitly supports an `.onion` address). An instance reachable outside the LAN
is an open account factory: each new account can create decks, upload up to 550 MB `.apkg`
packages, and consume media storage.

There is also no way for an operator to list, disable, or remove an account after the fact
(#88 tracks the deletion-policy half; the *administration* half is untracked).

**Fix:** a `SIGNUP_MODE` env var (`open` / `invite` / `closed`) is the minimum. `closed` plus a
seeded first account covers the single-classroom case entirely and is a few dozen lines.

### S4 — No timeouts anywhere: HTTP server, DB pool, or request context · `sev: med`, `area: security`

[main.go:69](../../cmd/deckshare/main.go#L69) constructs `&http.Server{Addr, Handler}` with no
`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, or `MaxHeaderBytes`. `gosec`
G112 flags exactly this; it is the textbook Slowloris exposure, and a handful of held-open
connections suffices because there is no connection limit either.

Compounding it:

- [`db.NewPool`](../../internal/db/pool.go#L13) uses pgxpool defaults — `max(4, NumCPU)`
  connections, no `statement_timeout`, no max connection lifetime.
- No handler derives a `context.WithTimeout`. A slow query holds its goroutine, its pool
  connection, and (with no `WriteTimeout`) its socket indefinitely.
- `POST /import` runs the entire package parse and import synchronously **inside one DB
  transaction**, holding a pool connection throughout. Four concurrent imports on a 4-core box
  exhaust the pool and the whole app stops serving.

**Fix:** set the four server timeouts (`ReadHeaderTimeout` at minimum — it is the one that closes
Slowloris), set `statement_timeout` in the DSN, and give `/import` and `/decks/{id}/export` their
own longer, explicit deadline rather than inheriting none.

### S5 — Unbounded memory on the import and export paths · `sev: med`, `area: apkg`

- `/import` accepts a 550 MB body and `apkg.DefaultArchiveLimits` permits 500 MiB of
  *decompressed* member bytes, all held in memory (`media.Store.Put` takes `[]byte`; `apkg.Read`
  buffers the collection). There is no cap on concurrent imports.
- `/decks/{id}/export` buffers the entire generated `.apkg` in a `bytes.Buffer` before writing —
  deliberately, and the reasoning (no truncated download after a 200) is sound, but it means an
  arbitrary-size allocation per concurrent export with no limit on how many can run.

Both are authenticated, so blast radius is bounded by S3's answer. Still: a semaphore
(`chan struct{}` of size 1–2) around the import handler, and a size ceiling on export, are cheap.

### S6 — `data-expected` publishes the answer on the question side for no benefit · `sev: low`, `area: security`

[`TypeAnswerInput`](../../internal/render/typeanswer.go#L18) emits
`<input class="type-answer" data-expected="…">` on the **question** side, and
`web/static/review.js` never reads it — there is no typed-vs-expected comparison anywhere in the
client. The attribute is dead weight that also puts the answer in the question DOM.

(The whole batch's answers are already in the page as hidden nodes by design — §6 — so this leaks
nothing new. Worth fixing as the tail of building the feature properly; see F4.)

### S7 — Access-grant form is a user-enumeration oracle · `sev: low`, `area: security`

`POST /decks/{id}/access` answers "No account with that email address" versus success
([access.go](../../internal/http/access.go)), letting any holder of `can_manage_access` on any
deck test whether an arbitrary email has an account here. Deliberate per the comment (there is no
pending-invite flow, #83), and gated behind holding a deck — but worth recording, because the same
oracle disappears for free the day invites land.

### S8 — `cmd/seed` has no environment guard · `sev: low`, `area: build`

`cmd/seed` creates five accounts with the password `"password"` and needs only `DATABASE_URL`. It
is not in the Docker image (which builds `./cmd/deckshare` only), so this is a footgun, not an
exposure. A `DECKSHARE_ALLOW_SEED=1` check costs three lines and permanently removes the "pointed
it at the wrong DSN" failure.

### Design risk to settle before it ships — #175 / #179 session sets

The plan for credential-free account switching (`docs/plans/175-multi-user-session-switching.md`)
is carefully reasoned and includes a `lock` state. It is still the largest change to the threat
model on the roadmap: it converts a stolen session cookie from "one account compromised" into
"every account in that browser's set compromised." The plan's own Q1 acknowledges this and answers
it with `lock`. Worth one explicit adversarial review pass before it merges rather than after —
specifically, what happens to a set when one member changes their password (today
`ChangePassword` purges *that user's* sessions; sibling rows in the set would survive).

---

## 2. Correctness bugs found

### B1 — Three tests assert table-wide `count(*)` and fail on a populated database · `sev: med`, `area: test`

`go test ./...` against the seeded local DB (`cmd/seed`, #206, merged yesterday) fails:

| Location | Assertion |
|---|---|
| [review_test.go:709](../../internal/http/review_test.go#L709) | `SELECT count(*) FROM review_log` — got 5, want 0 |
| [review_test.go:799](../../internal/http/review_test.go#L799) | `SELECT count(*) FROM review_log` — got 5, want 0 |
| [dbwrite_test.go:565](../../internal/apkg/dbwrite_test.go#L565) | `SELECT count(*) FROM media_blobs` — got 2, want 1 |

This is exactly the anti-pattern CLAUDE.md §16 names as "a bug in the test — scope it to the
row(s)/user the test itself created" (#134/#119/#108/#141). The third is already tracked as
**#199**; the two in `review_test.go` are not.

They pass in CI only because CI migrates a fresh database. That makes green CI conditional on an
empty DB and green local runs conditional on never having seeded — a trap that will keep catching
people. Scope both to the test's own user (`WHERE user_id = $1`) and the media one to the test's
own sha.

### B2 — `POST /settings` and `POST /settings/password` render the retention field as `<no value>` · `sev: low`

`settings.html:61` renders `value="{{.DesiredRetention}}"` on a `required` number input. The
profile and password handlers ([settings.go](../../internal/http/settings.go), every render call
in those two blocks) omit `DesiredRetention` from the data map, so `html/template` emits
`<no value>` into the attribute. After changing your display name or password, the FSRS section
shows an empty, invalid required field.

### B3 — `POST /settings/avatar` renders the version line as `<no value>` · `sev: low`

Same root cause, other direction: all six avatar render calls omit `"Version"`, so
`settings.html:4` renders `Version <no value>` after any avatar upload or avatar error.

### B4 — The displayed app version is stale by ten releases · `sev: low`

[`appVersion = "0.2.20"`](../../internal/http/settings.go#L37); CHANGELOG and git tag are at
`v0.2.30`. `git log -S` confirms it has not been touched since the constant was introduced. A
hand-maintained constant that must be bumped in lockstep with a file it does not live next to will
keep drifting.

**Fix for B2–B4 together:** the shared cause is the untyped `map[string]any` render contract —
nothing checks that a page's required keys are present. Give `/settings` a `settingsView` struct
assembled by one function every branch calls, and derive `appVersion` from
`debug.ReadBuildInfo()` or an `-ldflags -X` value instead of a constant. That kills all three and
their whole class, and is the highest-leverage cleanup in the repo.

---

## 3. Simplification

Ordered by payoff. None are style preferences; each removes a stated fragility or real duplication.

### C1 — `reviews.sql` repeats its scoring expression four times, with a comment saying so

`internal/db/queries/reviews.sql` is 491 lines and carries this, verbatim:

> the `2147483647` (int32 max) sentinel is repeated, not shared — sqlc has no macro mechanism
> across query bodies — at 3 other sites in this file (search for it); change all 4 together or
> the new-card sort order and its own cap boundary desync.

Confirmed at lines 81, 151, 298 and 333. The `rev_order` `CASE` expression is likewise duplicated
across `ListDueCardsForStudy`, its `rev_cutoff` LATERAL, and `ListReviewCardsForStudy`. A comment
saying "change all four together or it silently desyncs" is a defect report, not a mitigation —
this is the highest-risk maintenance surface in the repo, and the failure mode is a silently wrong
study queue.

**Options, cheapest first:**

1. SQL immutable functions in a migration — `deckshare_new_card_key(import_due_position)` and
   `deckshare_rev_key(order, due, scheduled_days, card_id, seed)`. `sqlc` handles function calls
   fine; four copies collapse to four call sites of one definition, and the sentinel lives in
   exactly one place.
2. Failing that, a test that reads the file and asserts the sentinel occurrence count — ugly, but
   it converts a silent desync into a red build.

Option 1 is strictly better and is a normal use of the database.

### C2 — The queue-count block is duplicated between the deck list and the deck page

`GET /decks` and `GET /decks/{id}` ([decks.go](../../internal/http/decks.go)) each independently
compute `NewRemaining` / `RevRemaining` / `LeftToStudy` / `min(NewCount, newRemaining)` from the
same four query results, in different shapes (map-of-decks vs. single deck). Extract
`func deckQueueCounts(row, preset, introduced, reviewed) queueCounts` into `internal/review` and
call it from both: ~30 lines saved and one place for the arithmetic to be right.

### C3 — `GET /decks/{id}` issues eight sequential queries

Deck, contents count, notes, FSRS params, study-day window, queue counts, introduced-today,
reviewed-today, other-progress-viewers — each its own round trip. Several combine trivially
(`CountNewIntroducedToday` and `CountReviewedToday` are the same shape over the same window;
`CountOtherProgressViewers` could ride on `GetDeckForUser`). Not urgent at LAN scale, but this is
the app's most-visited route.

### C4 — AI import inserts notes one at a time

`POST /import/ai` loops `db.CreateNoteWithCards` per note, up to `maxAIImportNotes` = 500 — 500
sequential round trips inside one transaction. `pgx.Batch` or a multi-row insert makes it one or
two. (The same loop shape is already batched in `apkg.Import`; this handler just didn't inherit
it.)

### C5 — `render` / `renderFragment` rely on Go's content sniffing for `Content-Type`

[templates.go:88 and :100](../../internal/http/templates.go#L88) never set `Content-Type`. It works
today because the buffers start with `<!doctype html>` / `<article`, but an empty refill fragment
sniffs as `text/plain`. One `w.Header().Set("Content-Type", "text/html; charset=utf-8")` in each
removes the dependency on a heuristic — and the app already sends `X-Content-Type-Options:
nosniff`, so being explicit is the consistent posture.

### C6 — `sort.Slice` → `slices.SortFunc`

Already tracked as **#139**. Confirmed still present in `internal/review/grade.go`,
`internal/review/lock.go`, and `internal/http/progress.go`.

---

## 4. Missing features

Split into "the schema already expects this" and "product gaps."

### The schema already has the column; nothing writes it

**F1 — Suspend, bury, and flag a card.** `user_card_state.suspended`, `.buried_until` and `.flag`
exist ([migration 00010](../../migrations/00010_user_card_state.sql)), every study query correctly
filters on them, and both the instructor dashboard and the queue counts honour them — but **no
route anywhere sets any of the three.** They are readable dead state. Suspending a card mid-review
is table stakes for anyone arriving from Anki; this is the largest single gap between DeckShare
and the app it means to replace. (#207 asks for flag-with-comment, which is adjacent but not the
same thing.)

**F2 — `sort_field_idx`, and field `sticky` / `is_rtl` / `font` / `size`.** Stored, imported,
exported; never editable and unused in the UI beyond `sort_field_idx` picking the notes-list
column.

### Product gaps

**F3 — No password reset, and no email at all.** No SMTP config, no reset-token table, no
verification. In the target classroom deployment, a student who forgets their password is
permanently locked out and the instructor has no recourse — there is no admin role (S3) and no way
to reset another user's password. This is the most operationally painful omission on the list. A
minimal answer that avoids taking on an SMTP dependency: an operator CLI subcommand
(`deckshare reset-password <email>`) that prints a one-time link.

**F4 — `{{type:Field}}` renders but does nothing.** The input appears on the question side and the
expected answer on the answer side, with no comparison, no diff, no feedback (S6). The plumbing is
complete and correct; only ~20 lines of client comparison and a diff node are missing.
`TypeAnswerExpected`'s own doc comment already anticipates this ("A later reviewer session can
replace this with a typed-vs-expected diff node").

**F5 — No note search or browse.** `ListNotesInDeck` is `ORDER BY modified_at DESC LIMIT 200`
([notes.sql:9](../../internal/db/queries/notes.sql#L9)) with no pagination, no search, no tag
filter and no cross-deck browse. Import a real 5,000-note Anki deck and 4,800 notes are simply
unreachable through the UI. Pagination is tracked as **#90**; search and tag filtering are not.
`notes.tags` is a `text[]` with no GIN index, so tag search needs a migration too.

**F6 — No learner-facing statistics.** The instructor dashboard (#87) is built and good; the
*student* has no equivalent — no review-history graph, no forecast of upcoming workload, no
retention trend, no streak. `review_log` holds everything needed. Anki users expect this, and it
is the natural companion to the dashboard that already exists.

**F7 — Reviewer session UX gaps.** No undo of the last grade, no "edit this note" from the
reviewer, no remaining-count or progress bar (only an "N reviewed this session" counter), no
keyboard help. Undo is the one users will miss immediately after a misclick.

**F8 — No deck hierarchy.** Anki's `Parent::Child` deck tree imports as flat, separately named
decks. Documented as scope in §20's new-card-order row, but a user importing a structured
collection sees a flat list of `Spanish::Verbs::Irregular` names. At minimum, render the `::`
separator as a tree in `/decks`.

**F9 — No `.colpkg` full-collection export** (**#84**) and no bulk/multi-deck export. `/import`
accepts `.colpkg`; the round trip out is per-deck `.apkg` only.

**F10 — No note-type limits.** `POST /note-types`
([notetypes.go:103](../../internal/http/notetypes.go#L103)) caps nothing: not the number of
fields, not the number of templates, not name length, not the CSS blob, not `qfmt` / `afmt`
length. A note type with 5,000 templates multiplies into 5,000 cards per note. This is both a
missing validation and a resource-exhaustion vector, and it stands out precisely because
everything around it is bounded (deck name ≤ 200, field ≤ 64 KiB, batch ≤ 100 events, upload
≤ 550 MB). Parallel gap: `parseTags` caps neither tag count nor tag length.

---

## 5. Operability

Grouped because they share one root: the app has no production-facing instrumentation.

- **No structured logging** (S1). `log.Printf` in five places; `log/slog` unused.
- **No metrics.** `/healthz` pings the DB and that is the whole ops surface. `routes.md` says "add
  when deploy tooling is scaffolded" — the deploy tooling (Dockerfile, multi-arch, StartOS target)
  now exists, so the condition has been met.
- **No `govulncheck` and no `gosec` in CI.** `golangci-lint` runs with `linters.default: standard`,
  a reasonable starting posture, but a project handling auth, file upload and other users' HTML
  should scan its dependency tree. `govulncheck ./...` is one CI line.
- **CI does not run `go test -race`.** Three concurrent subsystems — the auth sweep ticker, the
  media GC ticker, and the mutex-guarded in-memory rate limiters — plus a `sync.WaitGroup`
  -parallelised signup path. `-race` over the existing suite is close to free.
- **`compose.yaml` publishes Postgres on `0.0.0.0:5432`** with `root` / `mysecretpassword`. Fine
  for local dev, but it is the only compose file in the repo and there is no deployment compose to
  contrast it with — someone will run it on a VPS. Bind `127.0.0.1:5432:5432`.
- **No backup/restore guidance.** `review_log` is explicitly irreplaceable training data (§2.5)
  and media bytes live outside the database, so a correct backup is "pg_dump **and** `MEDIA_ROOT`,
  consistently." Nothing documents that pairing.

---

## 6. Testing

Strong: ~36.5k lines of Go, roughly half of it tests, and CLAUDE.md §10's priority list is
genuinely implemented — the client-cannot-write-scheduling-state test exists, the
batch-preview/grade-time consistency property test exists (`internal/fsrs/consistency_test.go`),
the access-control table exists, and the library-behaviour pinning tests
(`TestLibraryClipsOutOfRangeWeightSilently` and friends) are a good idea most projects skip.

Gaps:

1. **B1's three unscoped assertions** — highest priority, because they make the suite's greenness
   conditional on database state.
2. **One `.apkg` fixture.** CLAUDE.md §10.3 asks for "fixture files from several Anki versions
   (schema 11 and 18+, with and without FSRS data, with media, with cloze, with non-ASCII
   filenames)"; `tests/fixtures/apkg/` holds exactly one (`mathematics-schema18.apkg`), with
   `synthetic_test.go` covering schema 11 via hand-built packages. The doc itself calls these "the
   hardest test asset to produce later" — collecting two or three more is cheap now and expensive
   after a format bug ships.
3. **One e2e spec.** `tests/e2e/review-grading.spec.ts` only. Nothing for import, sharing, or the
   note editor.
4. **No `-race` in CI** (above).

---

## 7. What is working well — do not "improve" these

Recorded so a future cleanup pass doesn't mistake deliberate design for accident:

- **The sanitiser** (`internal/render/sanitise.go`) is the best code in the repo. Built from
  `NewPolicy()` rather than `UGCPolicy`, `SkipElementsContent` on the raw-text/foreign-content
  elements that cause mutation XSS, an SVG subset chosen by the principle "no element that scripts
  or references" rather than per-element judgement, and `img src` narrowed to a bare filename.
  Each decision has its attack written next to it.
- **The CSP comment block** (`internal/http/security.go`) explains every source with the concrete
  line of code that forces it, including both concessions and their expiry conditions. This is how
  CSP should be documented.
- **`GradeBatch`'s four concurrency mechanisms** are all present and each is correct — sorted lock
  keys (with the right reason: a hash collision makes tuple order and key order disagree),
  `reviewed_at` ordering, globally-scoped idempotency lookup, and out-of-order replay. The
  re-read-after-upsert ("the guarded upsert's WHERE clause is the authority on what actually
  landed") is exactly right.
- **`decodeBatch`'s five-field struct** — the comment correctly identifies that decoding into a
  narrow struct, *not* `DisallowUnknownFields`, is what makes §2.7 hold.
- **The deletion policy** (`internal/db/deletion.go`, and migration 00011's `card_id`-has-no-FK
  reasoning) is a genuinely hard design solved well.
- **`docs/architecture.md` §20** — a register of every deviation from Anki with its justification,
  plus a stated test for whether a deviation is legitimate. Keep this alive.

---

## 8. Suggested order

| # | Item | Why here |
|---|---|---|
| 1 | S1 — log server errors + request log | Everything else is harder to diagnose without it |
| 2 | B1 — scope the three `count(*)` assertions | Green CI is currently conditional on an empty DB |
| 3 | S4 — HTTP server timeouts | One struct literal; closes Slowloris |
| 4 | S2 — trusted-proxy client IP | Rate limiting is currently inverted into a DoS |
| 5 | S3 — `SIGNUP_MODE` | Gate the instance before it is exposed |
| 6 | B2/B3/B4 — settings view struct + build-info version | Kills three bugs and their whole class |
| 7 | F1 — suspend / bury / flag | Largest user-visible gap; the schema is already there |
| 8 | C1 — collapse `reviews.sql`'s four sentinel copies | Highest-risk maintenance surface |
| 9 | F3 — password reset path | Blocks real classroom use |
| 10 | F10 — note-type input limits | Cheap; closes an exhaustion vector |
| 11 | Ops: `govulncheck`, `-race`, compose bind | One CI file, three lines |

Items 1–5 are roughly a day's work. 6–11 are each their own issue.
