# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.1.25] - 2026-08-16

### Added
- Per-deck daily new-card limit: the reviewer's queue now caps never-seen cards at the deck's
  `new cards/day` setting (default 20, Anki's own `new.perDay`), editable from the deck edit
  page, instead of serving every new card in the deck at once
  ([#101](https://github.com/Jolls/enshu/issues/101))

## [0.1.23] - 2026-08-15

### Added
- One-command local test-DB reset: `.claude/skills/run-app/reset-db.sh` wipes the `pgdata`
  volume, brings Postgres back up, reapplies goose migrations, and seeds a test user with two
  empty decks (`cmd/seed`), replacing the manual `docker compose down -v` reset agents were
  improvising when DB-backed tests failed on stale state
  ([#95](https://github.com/Jolls/enshu/issues/95))

## [0.1.22] - 2026-08-15

### Added
- New users are seeded with stock **Basic** and **Cloze** note types on signup, matching Anki's
  own out-of-the-box set, so a new user can create a note immediately instead of hand-building a
  note type (and its `qfmt`/`afmt`) from scratch. Best-effort: a seeding failure is logged, never
  blocks or fails signup itself ([#97](https://github.com/Jolls/enshu/issues/97))
## [0.1.21] - 2026-08-15

### Fixed
- Grading a card in the reviewer never persisted: the batch POST was sent through htmx's
  `hx-vals`/`json-enc` mechanism, which re-evaluates the vals expression a second time to recover
  typed values and, for a 2+ event batch, merges repeated array entries by pushing the array into
  itself — a circular reference that made `JSON.stringify` throw inside htmx's extension, silently
  caught, and fell back to a malformed non-JSON body the server correctly rejected. The batch send
  is now a direct `fetch()` in `review.js`, bypassing htmx's parameter pipeline entirely; the
  now-unused `json-enc` vendored extension was removed. Also: review grading failures (a
  rejected/forbidden result, or the batch POST itself failing) are now shown to the user in the
  reviewer instead of only logged to the browser console, and CSRF/Origin rejections are now
  logged server-side — a session's worth of grades could previously be lost with no visible
  failure and no server-side trace to diagnose it from
  ([#99](https://github.com/Jolls/enshu/issues/99))

## [0.1.20] - 2026-08-15

### Added
- `GET`/`POST /import/ai`: paste-in import for AI-generated cards — pick a deck + note type,
  copy a generated prompt (with a cloze-depth dial for how much text a cloze card blanks) into
  whatever AI you already use, and paste the reply back in. Parses NDJSON via a streaming JSON
  decoder rather than newline-splitting, so it's insensitive to how the reply is
  separated/wrapped; all-or-nothing per paste, using the same note/card-authoring pipeline as
  manual note creation — no model API call from Enshu itself
  ([#85](https://github.com/Jolls/enshu/issues/85))

## [0.1.19] - 2026-08-15

### Added
- `/decks` and `/decks/{id}` show a New/Learning/Due queue summary, using the same eligibility
  rules and `can_study` access check as the review queue itself
  ([#80](https://github.com/Jolls/enshu/issues/80))

## [0.1.18] - 2026-08-15

### Fixed
- Import page's file picker now accepts `.colpkg` as well as `.apkg` — the reader and handler
  already supported both containers, but `accept=".apkg"` hid `.colpkg` files from the OS file
  dialog ([#78](https://github.com/Jolls/enshu/issues/78))

## [0.1.17] - 2026-08-15

### Added
- `GET`/`POST /settings/fsrs` and `POST /decks/{id}/settings/fsrs`: set the desired-retention
  target, globally or per deck, against FSRS's default parameters (no fitting required). The
  per-deck route is authorised in the upsert query itself — a caller without `can_study` on the
  deck matches no rows and gets a 404 ([#63](https://github.com/Jolls/enshu/issues/63))

## [0.1.16] - 2026-08-15

### Added
- `GET`/`POST /import`: upload a `.apkg`, synchronous `read -> IR -> db`, redirect to the deck
  the package's cards actually landed in. `ImportResult` now reports a per-deck card tally so the
  handler can pick that deck over an untouched "Default" deck the package might also carry.
  Verified end to end against the real fixture via the actual route
  ([#62](https://github.com/Jolls/enshu/issues/62))

## [0.1.15] - 2026-08-15

### Added
- `tests/fixtures/apkg/mathematics-schema18.apkg`: the first committed `.apkg` fixture, a real
  schema-18 export used to verify `internal/apkg`'s protobuf field numbers
  ([#61](https://github.com/Jolls/enshu/issues/61))

### Fixed
- `.apkg` schema-18 import now reads real notetypes/fields/templates/decks/media instead of
  unconditionally failing: `ankischema.go`'s protobuf field numbers are verified against a real
  export, two of them (field font/size) corrected from earlier wrong guesses, `read.go` now
  registers the `unicase` collation the schema's tables require to open at all under
  `modernc.org/sqlite`, and a filtered deck is now detected correctly. RTL/sticky field flags,
  browser-format template overrides, and a deck description remain undecoded (default to zero
  value) pending a fixture that exercises them
  ([#61](https://github.com/Jolls/enshu/issues/61))

## [0.1.14] - 2026-08-14

### Added
- `internal/media`: filesystem-backed, content-addressed blob store
  (`${MEDIA_ROOT}/<sha[0:2]>/<sha>`), written via temp-file-plus-rename so a concurrent reader
  never observes a partial write ([#60](https://github.com/Jolls/enshu/issues/60))
- `.apkg` import now writes media into the blob store and records `media_blobs`/`media_refs`
  rows, refing every deck the import touches (the package format doesn't attribute individual
  files to individual decks) ([#60](https://github.com/Jolls/enshu/issues/60))
- `GET /media/{sha256}`: serves a blob to any user with `can_view` on a deck that references it,
  with long-lived private cache headers ([#60](https://github.com/Jolls/enshu/issues/60))

### Changed
- `MEDIA_ROOT` is now a required environment variable alongside `DATABASE_URL`
  ([#60](https://github.com/Jolls/enshu/issues/60))

## [0.1.13] - 2026-08-14

### Added
- `internal/apkg/`: a database → IR → `.apkg` writer (`Export` + `Write`/`WriteFile`), completing
  architecture.md §11's build-order step 8. Always writes schema 11 (the only verified format —
  schema 18 stays reader-only and gated behind [#61](https://github.com/Jolls/enshu/issues/61)),
  reusing each row's original `anki_id` when one was imported and synthesising one otherwise.
  Reconstructs the Anki `type`/`queue`/`due` triple from `user_card_state` + FSRS `State`, and
  synthesises `col.crt` from the earliest exported review-state due date since Enshu persists no
  package-wide creation instant ([#59](https://github.com/Jolls/enshu/issues/59))

## [0.1.12] - 2026-08-14

### Added
- `internal/apkg/`: `.apkg`/`.colpkg` reader (legacy zip and modern zstd containers, schema-11
  fully working, schema-18 gated behind unverified protobuf field numbers pending
  [#61](https://github.com/Jolls/enshu/issues/61)) and an IR → database writer, idempotent on
  `(owner_id, guid)`, filing each card's `deck_id` from that card's own home deck rather than the
  note's (architecture.md §20) ([#58](https://github.com/Jolls/enshu/issues/58))
- `review.LockCards`, an exported per-`(user, card)` advisory-lock helper shared by the importer
  and `GradeBatch` ([#58](https://github.com/Jolls/enshu/issues/58))
- Synthetic in-memory `.apkg` fixture builders (`internal/apkg/synthetic_test.go`) covering
  schema 11/18, the zstd container, out-of-order `ord` arrays, a filtered-deck card, and archive
  ceiling violations — no committed binary fixtures, per `tests/fixtures/apkg/README.md`
  ([#58](https://github.com/Jolls/enshu/issues/58))

### Security
- `.apkg` reading enforces archive ceilings (member count, per-member and total decompressed
  size) and checks a zstd frame's declared size before decompressing, so an uploaded package
  cannot exhaust memory ([#58](https://github.com/Jolls/enshu/issues/58))

## [0.1.11] - 2026-08-14

### Security
- A global `Content-Security-Policy` (`internal/http/security.go`), set by a middleware wrapping
  outside the auth middleware so it covers CSRF rejections and error responses too. Defence in
  depth behind `internal/render`'s sanitisation for card content that, under a shared deck,
  belongs to another user: `script-src` refuses inline and remote script, `img-src 'self'`
  refuses remote card images (a tracking beacon in the multiuser model — architecture.md §20),
  `frame-ancestors 'none'` refuses framing, and `base-uri 'none'` refuses a `<base>` rewrite of
  relative card media ([#57](https://github.com/Jolls/enshu/issues/57))

## [0.1.10] - 2026-08-14

### Added
- The reviewer: `internal/review/` (batch precompute over `internal/fsrs`, server-authoritative
  grading, `review_log` replay) and `internal/http/review.go`'s three routes —
  `GET /decks/{id}/review`, `GET /api/reviews/next`, `POST /api/reviews/batch` — implementing
  architecture.md §6 in full: advisory locks acquired in ascending sorted-key order, events
  applied in `reviewed_at` order, idempotency via `review_log.id`, and out-of-order replay
  ([#56](https://github.com/Jolls/enshu/issues/56))
- `POST /api/reviews/batch`'s route-level test asserting the client cannot write scheduling
  state — extra fields (`stability`, `due`, …) are silently ignored, never stored — the highest
  test-priority item in the repo (CLAUDE.md §10.1) ([#56](https://github.com/Jolls/enshu/issues/56))
- The reviewer's client-side queue module (`web/static/review.js`, vanilla JS, no build step):
  synchronous grading with no network wait, a local learning-steps requeue heuristic, batched
  send with backoff retry, and htmx-driven refill/send wiring. First JS in the repo — vendored
  htmx + the json-enc extension under `web/static/`, served from a new `GET /static/{path...}`
  route, rather than a CDN ([#56](https://github.com/Jolls/enshu/issues/56))

## [0.1.9] - 2026-08-14

### Added
- Deck / note-type / note / card CRUD: `internal/http/{decks,notetypes,notes}.go` handlers and
  server-rendered forms/list pages, following docs/routes.md's Decks/Note types/Notes tables
  ([#54](https://github.com/Jolls/enshu/issues/54))
- Card generation on note create and diff-based card regeneration on note edit
  (`internal/db/cards.go`'s `SyncNoteCards`): one card per template for non-cloze note types, one
  per distinct cloze ordinal for cloze note types; a surviving ordinal's card keeps its id, and
  with it its `user_card_state`/`review_log` history, instead of the drop-and-recreate trap
  docs/schema.md warns against ([#54](https://github.com/Jolls/enshu/issues/54))
- `internal/render.ClozeOrdinals`, a pure cloze-marker scanner shared by card generation and the
  future rendering engine (#55) ([#54](https://github.com/Jolls/enshu/issues/54))
- DB-backed regression test for the card-regeneration trap (edit a 3-cloze note; assert a
  surviving card's id and `user_card_state` are untouched) and a composite-FK regression test
  for `notes.owner_id = decks.owner_id` ([#54](https://github.com/Jolls/enshu/issues/54))
- `internal/render/`: note-type template rendering (§8) — `{{Field}}`, `{{#Field}}`/`{{^Field}}`
  sections, `{{FrontSide}}`, filters (`text`, `furigana`/`kanji`/`kana`, `hint`, `cloze`,
  `type`), special tags (`{{Tags}}`, `{{Type}}`, `{{Deck}}`, `{{Subdeck}}`, `{{Card}}`), and
  cloze fan-out with nested-marker support. `RenderCard(tmpl, note, ordinal, isCloze)` is the
  sole entry point ([#55](https://github.com/Jolls/enshu/issues/55))
- Card-content HTML sanitisation on render, via a `bluemonday` allowlist policy: no
  non-HTML-parsing-mode elements (`svg`/`math`/`style`/`script`/`textarea`/`title`/…), a URL
  scheme allowlist (`http`/`https`/`mailto`), and a shared CSS value grammar banning any bare
  `(` outside the four colour functions, applied to both inline `style=""` and the note-type CSS
  blob ([#55](https://github.com/Jolls/enshu/issues/55))
- `internal/render.SanitiseCSS`: note-type CSS-blob sanitisation via `douceur`'s parser —
  property/selector allowlists, the same value grammar as inline styles, all at-rules dropped,
  selectors scoped to `.enshu-card` ([#55](https://github.com/Jolls/enshu/issues/55))
- `{{type:Field}}` answer-input boundary: rendering leaves an unguessable placeholder (never
  HTML) in `Rendered.HTML`; `TypeAnswerInput`/`TypeAnswerExpected` splice the real widget in
  *after* sanitisation, so the widget can never be sanitised as card content and never needs a
  hole in the allowlist ([#55](https://github.com/Jolls/enshu/issues/55))

### Changed
- `notes.owner_id = decks.owner_id` is now enforced at the database level: `decks` carries
  `UNIQUE (id, owner_id)` and `notes` a composite `FOREIGN KEY (deck_id, owner_id)` (migration
  `00015`), replacing the query-layer-only convention
  ([#54](https://github.com/Jolls/enshu/issues/54))
- `GET /` now redirects an authed user straight to `/decks` instead of rendering a placeholder
  home page; `web/templates/home.html` removed ([#54](https://github.com/Jolls/enshu/issues/54))
- `internal/render.ClozeOrdinals`'s cloze-marker scanner now tracks brace depth, so a nested
  `{{c2::…}}` inside `{{c1::…}}` is recognised correctly instead of truncating at the inner
  marker's close; its exported signature and behaviour on every existing test case are unchanged
  ([#55](https://github.com/Jolls/enshu/issues/55))

### Security
- Card-content and note-type-CSS sanitisation (see Added, above) is the first place untrusted
  HTML from a note reaches a page — required before any shared-deck rendering path exists, since
  a card's content is *other users'* HTML under the multiuser model
  ([#55](https://github.com/Jolls/enshu/issues/55))

## [0.1.8] - 2026-08-13

### Added
- `internal/auth/`: signup, login, and logout with a `__Host-`-prefixed session cookie, sliding
  expiration, `argon2id` password hashing, an `Origin`-header CSRF check enforced centrally in
  the auth middleware, per-key rate limiting on login (IP + email) and signup (IP), and an
  in-process ticker that sweeps expired sessions and stale rate-limit buckets hourly
  ([#52](https://github.com/Jolls/enshu/issues/52))
- `/settings` account routes: profile (display name, timezone, day-start hour) and password
  change ([#52](https://github.com/Jolls/enshu/issues/52))
- First server-rendered HTML templates (`web/templates/`): login, signup, home, and settings
  pages over a shared layout, Pico CSS from CDN ([#52](https://github.com/Jolls/enshu/issues/52))
- `internal/fsrs/`: pure wrapper over `go-fsrs` v4 (FSRS-6) with `Schedule` (grade-time recompute)
  and `PreviewAll` (batch-preview precompute over all four ratings), explicit `fsrs_version`/weight
  count/finite-weight/`desired_retention` validation ahead of the library, fuzz forced off for
  batch-preview/grade-time determinism, and an `ElapsedDays` helper matching go-fsrs's own
  calendar-day-boundary semantics ([#53](https://github.com/Jolls/enshu/issues/53))
- Property-based batch-preview/grade-time consistency test (CLAUDE.md §10.2, this repo's
  second-highest testing priority) over 500 random review sequences, plus determinism tests for
  `Schedule` and full-sequence replay ([#53](https://github.com/Jolls/enshu/issues/53))

### Security
- Timing-safe login (a fixed dummy `argon2id` hash for unknown accounts, compared unconditionally)
  and timing-safe signup (existence check and password hash run unconditionally and in parallel)
  ([#52](https://github.com/Jolls/enshu/issues/52))
- Rate limiting on `POST /settings/password`, keyed per-user, closing a brute-force gap on
  `current_password` that the initial pass missed ([#52](https://github.com/Jolls/enshu/issues/52))
- Login's per-email rate limit now keys on the lower-cased address, matching the case-insensitive
  account lookup -- previously an attacker could rotate email casing for a fresh 5-attempt budget
  per variant ([#52](https://github.com/Jolls/enshu/issues/52))

## [0.1.7] - 2026-08-11

### Added
- Full schema as 14 `goose` migrations, one per table, and `sqlc`-generated models/queries
  ([#50](https://github.com/Jolls/enshu/issues/50))
- CI Postgres service (`postgres:18`) that applies the migrations to a fresh database and
  verifies `sqlc generate` matches the committed `internal/db/` output on every push and PR
  ([#50](https://github.com/Jolls/enshu/issues/50))
- Deletion policy: deck delete removes the deck's cards and any note left with no cards anywhere,
  re-homing notes whose cards survive in other decks; `internal/db/deletion.go` runs it as an
  ordered transaction ([#51](https://github.com/Jolls/enshu/issues/51))
- Last-`can_manage_access`/`can_delete`-holder guard on `deck_access`, enforced in the query layer
  under a deck row lock ([#51](https://github.com/Jolls/enshu/issues/51))
- DB-backed tests for the cascade graph, the deck-delete procedure, and the access guard
  ([#51](https://github.com/Jolls/enshu/issues/51))

### Changed
- `compose.yaml`'s dev database bumped `postgres:16` → `postgres:18`, enabling native
  `uuidv7()` as a `DEFAULT` safety net on every `uuid` primary key alongside the
  application-generated id ([#50](https://github.com/Jolls/enshu/issues/50))
- `internal/db/db.go` renamed to `internal/db/pool.go` so `sqlc generate` (which writes its own
  `db.go`) no longer overwrites the hand-written connection pool code
  ([#50](https://github.com/Jolls/enshu/issues/50))
- Foreign keys carry their terminal `ON DELETE` behaviour instead of the blanket `RESTRICT`:
  sessions, fields, templates, `deck_access.deck_id`, `cards.note_id`/`deck_id`,
  `user_card_state.card_id`, per-deck `user_fsrs_params`, and `media_refs.deck_id` cascade;
  everything user-owned restricts ([#51](https://github.com/Jolls/enshu/issues/51))
- `review_log.card_id` is no longer a foreign key — the column, its index, and the re-import dedup
  key are unchanged, and no `review_log` row is deletable by any path. This is what makes a studied
  deck deletable without a `DELETE` over training data, and it matches Anki's own `revlog.cid`
  ([#51](https://github.com/Jolls/enshu/issues/51))

### Removed
- User deletion as a reachable operation: blocked at the FK level pending an account-closure
  design ([#51](https://github.com/Jolls/enshu/issues/51))

## [0.1.6] - 2026-08-11

### Added
- Go module scaffold: `cmd/enshu/` entrypoint (config from env, DB pool, graceful shutdown) and
  the `internal/` package skeleton from architecture.md §4 ([#49](https://github.com/Jolls/enshu/issues/49))
- `sqlc` wired to generate from `migrations/` + `internal/db/queries/`; `goose` wired to
  `migrations/` for schema migrations
- `golangci-lint` v2 config (`standard` linter set)
- Multi-arch `Dockerfile` (`GOOS`/`GOARCH` cross-compile, distroless runtime image)
- GitHub Actions CI: `go build`, `go vet`, `golangci-lint run`, `go test ./...` on push and PR

## [0.1.5] - 2026-08-10

Documentation only. Reconciles the docs with a ground-up stack re-evaluation
(`docs/plans/architecture-reconsidered.md`) that found FSRS never needs to run in the browser —
a card's outcome under each rating is a pure function of its state at batch-fetch time, so the
server can precompute all four and ship them as data — which removed the reasoning that
originally picked TypeScript end to end. No Go code has been written; the previously-merged
TypeScript/SvelteKit implementation is superseded, not migrated.

### Changed
- Server stack: TypeScript/SvelteKit/Drizzle → Go/`sqlc`, server-rendered HTML. PostgreSQL is
  unchanged — it was never a TypeScript-specific choice
- Scheduling: `ts-fsrs` (client + server) → `go-fsrs` (server-side only, via `Repeat()` for
  batch-preview and grading alike)
- CLAUDE.md invariants §2.6/§2.7 reworded: the client looks up a precomputed rating branch
  instead of computing FSRS locally, and asserts only `{id, cardId, rating, reviewedAt,
  durationMs}` — there is no `predicted` field to compare
- `docs/architecture.md` §6 rewritten around the precompute-once/look-up-and-grade contract;
  §1, §3, §4, §7, §8, §11, §12, §20 updated to match
- README's stack table and "why TypeScript" section replaced with the Go rationale; `enshu.md`
  follows
- CLAUDE.md §9/§10/§14/§16/§17 updated for Go tooling and the new testing surface
  (batch-preview/grade-time consistency replaces client/server FSRS parity)
- Deeper-reference docs (`docs/schema.md`, `docs/apkg-format.md`, `docs/anki-schema.md`) no
  longer cite `drizzle-kit`, `better-sqlite3`, or TypeScript file paths — pointed at the Go
  repo layout instead, and the `better-sqlite3` collation caveat is marked unverified under
  the new `modernc.org/sqlite` driver rather than restated as fact
- architecture.md §12's remaining scaffold-blocking open questions settled: `html/template`
  over `templ` (no runtime/UX difference — the reviewer's felt latency is a client-side JS
  property, not a rendering one; decided on dependency count and contributor accessibility
  instead), `goose` for migrations (plain SQL files fit CLAUDE.md §9's "committed, immutable,
  fix forward" convention more directly than `atlas`'s declarative-diff model), and
  `alexedwards/argon2id` for password hashing. Also newly recorded: Go 1.26, `golangci-lint`
  v2 with the `standard` linter set, and GitHub Actions for CI. §4's package layout confirmed
  as the scaffold target rather than illustrative
- Filed 15 Go-track issues (#49–#63) against architecture.md §11's Phase 1 build order, plus a
  tracking issue (#64), now that all TS-era issues are closed as superseded. `architecture.md`,
  `routes.md`, and `schema.md` repointed from the closed #15/#34 to their live replacements
  (#51, #60)
- `tests/fixtures/apkg/README.md`: replaced the vague "collect fixtures early" reminder with a
  concrete four-export plan covering every dimension `apkg-format.md`'s Fixtures section asks
  for from one small hand-built Anki collection, and reframed the synthetic-fixture builder as
  Go test-helper code belonging to #58, not a standalone script producing committed binaries —
  a synthetic schema-18 fixture built ahead of a real one would just bake today's unverified
  spec guesses into something that looks authoritative

### Removed
- The `predicted`-field / divergence-counter design (architecture.md §6, CLAUDE.md §17) — with
  a single server-side FSRS implementation there is nothing to diverge from

## [0.1.4] - 2026-08-10

### Changed
- Grading is server-authoritative in the code, not only in the docs (CLAUDE.md §2.7). A client now asserts `{ id, cardId, rating, reviewedAt, durationMs }` and nothing else; `applyReviewBatch` reads the stored `user_card_state`, schedules the grade itself through `$lib/fsrs`, and writes *its own* numbers to `review_log` and `user_card_state` ([#39](https://github.com/Jolls/enshu/issues/39))
- The client's local FSRS result travels as `predicted` — compared against the server's answer, never stored, never authoritative. §2.6 is unchanged: grading is still synchronous and local, and the UI never awaits the network ([#39](https://github.com/Jolls/enshu/issues/39))
- `/api/reviews/batch` responds with the server's post-review state per event, so a client can reconcile its queue ([#39](https://github.com/Jolls/enshu/issues/39))
- `review_log`'s FSRS columns come off `ts-fsrs`' own review log rather than being projected from the client's `stateBefore`/`stateAfter` ([#39](https://github.com/Jolls/enshu/issues/39))
- Write-queue batches are sorted by review time before scheduling, and an event already present in `review_log` is skipped outright — a retry no longer reschedules from the row it advanced ([#39](https://github.com/Jolls/enshu/issues/39))
- A batch whose reviews predate the stored `last_review` is replayed from `review_log` rather than folded onto the stored row, so out-of-order arrival converges instead of recording a `*_before` the card was never in ([#39](https://github.com/Jolls/enshu/issues/39))

### Security
- `applyReviewBatch` takes a per-`(user, card)` advisory lock for the transaction. Scheduling server-side makes each grade a read-modify-write, and two concurrent batches for one card (two tabs, a redelivered POST) would otherwise both schedule from the same state and silently drop a review ([#39](https://github.com/Jolls/enshu/issues/39))
- `/api/reviews/batch` refuses a `reviewedAt` more than five minutes ahead of server time. Unbounded it both poisons the server's schedule and writes a `last_review` far enough ahead to freeze that card behind the §6 guard permanently ([#39](https://github.com/Jolls/enshu/issues/39))
- The retry-skip lookup is global rather than scoped to `user_id`, matching `review_log.id`'s primary key. Scoped, a colliding id would have been dropped by `ON CONFLICT` while its `user_card_state` write still landed — state with no backing log row ([#39](https://github.com/Jolls/enshu/issues/39))

### Added
- `src/lib/server/fsrs/divergence.ts`: the §6 comparison (`state`/`reps`/`lapses` exact, `stability`/`difficulty` within `1e-6`, `due` to the second), a structured log line naming both `fsrs_version`s, and a process-lifetime counter. No table — the expected rate is zero, so it is an alert condition ([#39](https://github.com/Jolls/enshu/issues/39))
- CLAUDE.md §10.1 coverage, the repo's top testing priority: a grade whose `predicted` block is hostile rather than merely stale stores the server's value, keeps `review_log` clean, and raises one divergence per event ([#39](https://github.com/Jolls/enshu/issues/39))

### Removed
- The write queue's `localStorage` durability. It existed to serve offline study, which architecture.md §11 rules out; the idempotent endpoint and client-generated event ids stay ([#39](https://github.com/Jolls/enshu/issues/39))

## [0.1.3] - 2026-08-09

Documentation only. A fundamentals pass ahead of the code that implements it — see
`docs/architecture.md` §1 for what the code still contradicts and why that is deliberate.

### Changed
- Grading is server-authoritative. New invariant CLAUDE.md §2.7: a client asserts which card, which rating and when, and the server derives everything downstream of that, storing its own result and comparing the client's for divergence. §2.6 is unchanged — the client still schedules locally and the UI never waits on the network
- `docs/architecture.md` §6 rewritten around that contract: the wire payload, the recompute-compare-store server path, and a divergence policy (structured log plus counter, no table, since the expected rate is zero)
- CLAUDE.md §10 testing priorities reordered — "the client cannot write scheduling state" is now first, above FSRS wrapper parity, because a parity break is now a *caught* divergence rather than silent corruption
- No cross-user read is possible without a `deck_access` row, with no exceptions (CLAUDE.md §9). Replaces the `visibility = 'public'` carve-out with a single authorisation path
- README, `enshu.md`, `docs/schema.md`, and `docs/schema-diagram.md` follow the above; the stale "design doc only, no code yet" status line is replaced with the real one
- CLAUDE.md split in two: it keeps the working rules, the invariants, and the process, while the descriptive half — state, stack, layout, protocols, roadmap, glossary — moves to `docs/architecture.md`. Section numbers are one space shared across both files rather than restarted, so every existing `§N` reference still resolves; CLAUDE.md's preamble carries the map
- New invariant CLAUDE.md §2.10: follow Anki's model unless multiuser forces a change. Divergence is what needs justifying, not conformance, and the test is whether a difference traces back to the content/progress seam (§2.1). `docs/architecture.md` §20 is the register of where we differ and why — including one entry that does *not* pass the test, the importer flattening a note's cards to a single deck
- Recorded in architecture.md §20 that the importer's one-note-one-deck flattening has an expired justification: it was required by `UNIQUE (deck_id, guid)`, which #32 replaced with `UNIQUE (owner_id, guid)`. `IrCard.deckAnkiId` already carries per-card decks, so the resolution belongs to #33 and needs no migration
- §2.5 states explicitly that `review_log` mirrors Anki's `revlog`: append-only, one row per answer, never rolled up into a running total, which is what lets a years-old collection still be refitted
- `docs/anki-schema.md` and `docs/anki-schema-diagram.md` wired into the doc map — CLAUDE.md's reference list, architecture.md's §4 layout tree, and a routing table at the head of §7 that says which of the three Anki-facing docs answers which question
- §13's trademark rule redrawn along descriptive-versus-brand use rather than banning the word `AnkiWeb` outright, which as written forbade the README's own trademark section from naming its subject
- `docs/schema.md` no longer calls the media storage backend undecided; it is filesystem, content-addressed, and matches architecture.md §12
- architecture.md §12 no longer claims the media store is implemented in `src/lib/server/media/store.ts` — that path does not exist and [#34](https://github.com/Jolls/enshu/issues/34) is open

### Removed
- Full offline study, as a roadmap item. Not deferred — not intended. Local grading is a latency property, not a step toward offline, and `review_log` plus the replay path keep the option open if it ever earns its keep
- Deck forking and the public deck directory. Sharing through `deck_access` covers co-authoring and the classroom. Recorded alongside them: export-then-reimport is *not* a substitute for forking, because reimport mints new `cards` rows and `user_card_state` is keyed by `card_id`
- `decks.visibility` and `decks.forked_from_deck_id` from the schema documentation (the columns themselves still need a migration)
- The deck-content licensing open question, closed by dropping the directory — Enshu never redistributes deck content, so there is no catalogue to attach per-deck licence metadata to

## [0.1.2] - 2026-08-08
### Added
- Isomorphic `lib/fsrs/` wrapper over `ts-fsrs` — pure functions, no DB, no `fetch`, no browser globals, so client and server schedule identically ([#5](https://github.com/Jolls/enshu/issues/5))
- `lib/render/` note-type template engine: `{{Field}}`, `{{#Field}}`/`{{^Field}}` conditionals, `{{FrontSide}}`, filters, and cloze fan-out by ordinal ([#12](https://github.com/Jolls/enshu/issues/12))
- Auth + accounts: `argon2id` password hashing, sessions keyed by the SHA-256 hash of the cookie token, `Origin`-header CSRF checks, and login rate limiting ([#7](https://github.com/Jolls/enshu/issues/7))
- Deck / note-type / note / card CRUD query layer and JSON API routes, with `deck_access` authorisation enforced at the query layer ([#8](https://github.com/Jolls/enshu/issues/8))
- `.apkg` / `.colpkg` reader: package → intermediate representation, covering collection schemas 11 and 18+, the legacy and zstd containers, and content-addressed media ([#9](https://github.com/Jolls/enshu/issues/9))
- Synthetic `.apkg` fixtures built to `docs/apkg-format.md`, pending real Anki exports ([#9](https://github.com/Jolls/enshu/issues/9))
- Collection-member preference regression test: a package carrying both `collection.anki21` and a one-note `collection.anki2` downgrade stub must read the former ([#9](https://github.com/Jolls/enshu/issues/9))
- The reviewer and the write queue: one-request session start, synchronous local grading, a durable `localStorage` queue draining to an idempotent `POST /api/reviews/batch`, and the server-side `review_log` recompute path ([#13](https://github.com/Jolls/enshu/issues/13))
- `defaultFsrsParams()` — the `ts-fsrs` defaults a user schedules with before their first optimiser fit ([#13](https://github.com/Jolls/enshu/issues/13))
- Browser-facing signup, login, and logout pages wrapping the existing `/(auth)/*` JSON endpoints, with client-side validation mirroring the server's email/password rules ([#28](https://github.com/Jolls/enshu/issues/28))
- Deck list/create, an auto-seeded default "Basic" note type, add-note form, and note list/delete pages under `src/routes/(app)/decks/` — the minimum UI to create study material without `curl` ([#29](https://github.com/Jolls/enshu/issues/29))
- Re-import dedup keys: `UNIQUE (owner_id, name)` on `decks` and `note_types`, `notes.owner_id` with `UNIQUE (owner_id, guid)`, and `review_log.anki_id` with `UNIQUE (user_id, card_id, anki_id)` ([#32](https://github.com/Jolls/enshu/issues/32))
- `decks.anki_id`, for `.apkg` round-trip export fidelity ([#32](https://github.com/Jolls/enshu/issues/32))
- `DuplicateNameError` → HTTP 409 on the deck and note-type create/rename endpoints, now that a name is a uniqueness key ([#32](https://github.com/Jolls/enshu/issues/32))

### Changed
- Note identity is per owner, not per deck: `notes (deck_id, guid)` → `notes (owner_id, guid)`, with a plain `notes (deck_id)` index kept for deck-scoped queries. Amends the CLAUDE.md §2.2 invariant — `guid` is globally unique in Anki, so a deck-scoped key made note idempotency depend on deck dedup landing first ([#32](https://github.com/Jolls/enshu/issues/32))
- Deck and note-type re-import dedup on name rather than `anki_id`: Anki ids are per-collection (deck id 1 is `Default` in every collection), so an `anki_id` key would silently merge two unrelated imported decks and two different note types ([#32](https://github.com/Jolls/enshu/issues/32))

### Fixed
- `docs/apkg-format.md`: `cards.due` has three meanings discriminated by `queue`, not two by `type`, and `cards.odue` shadows it entirely for cards in a filtered deck; the negative-`ivl` seconds encoding also applies to `revlog`; schema-18 configs are protobuf; deck-name separators and field ordering differ per schema; the modern media index is protobuf ([#9](https://github.com/Jolls/enshu/issues/9))

### Security
- Card content is sanitised on render — note HTML is user-authored and, under shared decks, *other users'* HTML ([#6](https://github.com/Jolls/enshu/issues/6))
- `.apkg` reading enforces archive ceilings (member count, per-member and total decompressed size) and checks a zstd frame's declared size before decompressing, so an uploaded package cannot exhaust memory ([#9](https://github.com/Jolls/enshu/issues/9))

## [0.1.1] - 2026-08-07
### Added
- Full data model in Drizzle: content tables, `deck_access`, `user_card_state`, append-only `review_log`, `user_fsrs_params`, and content-addressed media ([#4](https://github.com/Jolls/enshu/issues/4))
- `studyDayStart()` / `studyDayEnd()` — per-user 04:00-local study-day boundary computed in SQL ([#4](https://github.com/Jolls/enshu/issues/4))
- Isomorphic `uuidv7()` for client-generated, time-ordered ids ([#4](https://github.com/Jolls/enshu/issues/4))

### Removed
- The scaffold's placeholder `task` table ([#4](https://github.com/Jolls/enshu/issues/4))

## [0.1.0] - 2026-08-07
### Added
- SvelteKit + TypeScript (strict) scaffold: ESLint, Prettier, Vitest, Playwright, Drizzle + PostgreSQL, CI ([#3](https://github.com/Jolls/enshu/issues/3))
