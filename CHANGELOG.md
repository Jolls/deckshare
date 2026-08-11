# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
