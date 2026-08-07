# CLAUDE.md — Enshu foundation

Working context for agent sessions on this repo. [README.md](README.md) is the public
rationale ("why these decisions"); [enshu.md](enshu.md) is the personal-notes digest of it.
**This file is the build spec** — layout, schema, protocols, conventions, and order of work.

If a decision here contradicts the README, the README wins on *rationale* and this file wins
on *mechanics*. If you change a decision, update both.

---

## Working rules

1. **Think Before Coding:** don't assume/hide confusion. State assumptions; if multiple
   interpretations, present don't pick; suggest simpler approach if exists, push back when
   warranted; if unclear, stop and ask.

2. **Simplicity First:** minimum code, nothing speculative. No unrequested
   features/abstractions/flexibility/error-handling for impossible cases. If 200 lines could
   be 50, rewrite. Test: "would a senior engineer call this overcomplicated?"

3. **Surgical Changes:** touch only what's needed. Don't improve/refactor/reformat adjacent
   code; match existing style; mention unrelated dead code, don't delete it. Remove
   imports/vars/funcs YOUR change orphaned; don't remove pre-existing dead code unless asked.
   Every changed line should trace to the request.

4. **Goal-Driven Execution:** define verifiable success criteria, loop until met (e.g. bug
   fix → repro test → make pass; feature → tests for invalid inputs → pass). For multi-step
   tasks state brief plan with verify step per item.

5. **Tests after bug fixes/features:** suggest a regression test when it'd meaningfully catch
   breakage (non-obvious edge cases, silent-break logic) — briefly, and only write if user
   agrees. Skip for trivial/UI-only/well-covered changes. Exception: anything touching
   `lib/fsrs/` or `lib/server/apkg/` always ships a test — see §10.

6. **Token-Efficient Messages:** terse, no preamble/restating/unrequested trailing summary,
   no just-in-case caveats. Alternatives welcome (standard/idiomatic ones) but skip esoteric
   ones unless asked. Prefer short direct statements over headers unless content has
   genuinely distinct parts. Bullet/Outline style communication is preferred.

7. **Model Selection:** default Sonnet. Suggest Opus once (don't repeat if user stays on
   Sonnet) for: schema changes, the FSRS wrapper or scheduling semantics, `.apkg`
   reader/writer work, cross-cutting architecture (auth, DB layer, template rendering, the
   write queue), security review (auth/CSRF/session/input validation/HTML sanitisation),
   refactors spanning `lib/server/` + `lib/fsrs/` + `routes/`. Not for routine feature
   work/bugfixes/UI/pattern-following handlers.

---

## 1. Current state

**Design phase. Zero lines of code.** No `package.json`, no framework scaffold, no CI.

The next session that writes code is doing greenfield setup. Nothing below is legacy —
it is all still cheap to change *except* the items in §2, which are expensive by the time
there are users.

---

## 2. Invariants — do not violate without an explicit decision

These are the load-bearing choices. Each one is cheap now and a rewrite later.

1. **Scheduling state never lives on the `cards` row.** It lives in
   `user_card_state`, primary-keyed `(user_id, card_id)`. Every multiuser feature — shared
   decks, cohorts, forking with preserved progress — is a consequence of this one fact.
   Adopting Anki's schema and bolting multiuser on afterwards is a rewrite.
2. **Every note carries Anki's `guid`.** It is what makes import and re-import idempotent.
   Retrofitting it duplicates every early user's decks on their next import.
3. **FSRS parameters are per-user** (optionally per `(user, deck)`), stored as a JSON array
   plus an explicit `fsrs_version` integer — never fixed-width columns. Parameter count
   changes upstream: 17 in FSRS-4.5, 19 in FSRS-5, 21 in FSRS-6.
4. **Never fit one parameter set across a cohort.** Memory behaviour is individual; a
   class-wide fit is wrong for every member of it.
5. **`review_log` is training data, not an audit trail.** The optimiser fits against it.
   It is per-user and it cannot be pruned casually. No `DELETE` paths without a written
   decision.
6. **Grading never blocks on the network.** FSRS runs client-side; the UI advances
   optimistically; writes queue and drain in the background. This shapes the entire client
   data flow and is the single most painful thing to retrofit.
7. **No Anki-derived code.** File formats are not copyrightable; the reader/writer is ours.
   Copying Anki source (AGPLv3) would make the licence question permanent and irreversible.
   Read the format spec and other clean-room parsers, never `ankitects/anki` source.
8. **No sync protocol.** Settled and closed. Re-opening it re-imports every cost listed in
   the README.

---

## 3. Stack

| Concern | Choice | Pinned version | Notes |
|---|---|---|---|
| App framework | SvelteKit + TypeScript (strict) | `@sveltejs/kit@^2.63.0`, `svelte@^5.56.1`, `typescript@^6.0.3` | SSR for everything except the reviewer; the reviewer is client-driven. |
| Scheduler | `ts-fsrs` | `5.4.1` (exact) | Runs on **both** client and server. |
| Database | PostgreSQL | `postgres:16` (Docker image) | Row-level tenancy via `user_id` columns and explicit query scoping. |
| ORM / migrations | Drizzle + `drizzle-kit` | `drizzle-orm@^0.45.2`, `drizzle-kit@^0.31.10` | Schema is TypeScript-first; migrations are generated SQL, committed, never edited after merge. |
| `.apkg` read/write | `better-sqlite3` (server) | `better-sqlite3@13.0.3`, `fflate@0.8.3`, `zstd-napi@0.0.13` (all exact) | `fflate` for zip; `zstd-napi` for zstd-compressed schema 18+ exports — native binding, chosen over `@bokuweb/zstd-wasm` for active maintenance (see git history for the comparison). |
| Tests | Vitest + Playwright | `vitest@^4.1.8`, `@playwright/test@^1.60.0` | See §10. |
| Auth | Lucia-style sessions or Auth.js | **Undecided** — see §12. Not blocking; keep it behind `src/lib/server/auth/`. | |

Versions pinned at scaffold time (`feature/3-scaffold`, 2026-08-07). Non-critical-path
devDependencies (SvelteKit, Drizzle, Vitest, Playwright, ESLint, Prettier) use caret ranges,
per `package.json`; `ts-fsrs` and the `.apkg` codec deps are pinned exact since a silent
version drift there is the correctness hazard this file keeps warning about.

**One language, end to end.** Scheduling must run in the browser, so an FSRS implementation
in JS exists regardless of backend language. A Python or Go server would mean two FSRS
implementations kept in agreement — a correctness hazard for the one thing that must not
be wrong. Hence TypeScript everywhere.

> **`ts-fsrs` must be the exact same version on client and server.** Record it in
> `user_fsrs_params.fsrs_version`. A client and server disagreeing about intervals is the
> worst failure mode this system has, because it is silent.

---

## 4. Repo layout

```
enshu/
├─ CLAUDE.md              this file
├─ README.md              public rationale
├─ enshu.md               personal notes digest
├─ docs/
│  ├─ schema.md           full DDL + rationale (§5 lives here)
│  ├─ apkg-format.md      Anki format reference (§7 lives here)
│  └─ plans/              implementation plans, <issue-id>-<slug>.md
├─ drizzle/               generated migrations (committed, immutable once merged)
├─ src/
│  ├─ lib/
│  │  ├─ server/          NEVER imported by client code — SvelteKit enforces this
│  │  │  ├─ db/
│  │  │  │  ├─ schema.ts      Drizzle table definitions (source of truth)
│  │  │  │  ├─ index.ts       connection/pool
│  │  │  │  └─ queries/       one module per aggregate: decks, notes, review, access
│  │  │  ├─ auth/
│  │  │  ├─ apkg/
│  │  │  │  ├─ read.ts        .apkg/.colpkg -> IR
│  │  │  │  ├─ write.ts       IR -> .apkg
│  │  │  │  ├─ anki-schema.ts Anki's SQLite shapes, schema 11 and 18
│  │  │  │  └─ media.ts       media map + content-addressed blob store
│  │  │  └─ fsrs/             server-side scheduling: import backfill, recompute, optimise
│  │  ├─ fsrs/               ISOMORPHIC scheduling wrappers — used by client and server
│  │  ├─ review/             review queue, write queue, session state (client)
│  │  ├─ render/             note-type template rendering ({{Field}}, cloze, conditionals)
│  │  └─ components/
│  ├─ routes/
│  │  ├─ (app)/             authenticated shell
│  │  │  ├─ decks/
│  │  │  ├─ study/[deckId]/ the reviewer
│  │  │  └─ manage/
│  │  ├─ (auth)/
│  │  └─ api/               JSON endpoints for the write queue and batch fetches
│  └─ app.d.ts
├─ tests/
│  ├─ unit/
│  ├─ fixtures/apkg/      real exports from multiple Anki versions — see §10
│  └─ e2e/
└─ scripts/
```

**Boundary rules**

- `src/lib/fsrs/` is isomorphic: pure functions, no DB, no `fetch`, no browser globals. This
  is what guarantees client and server schedule identically.
- `src/lib/server/**` is server-only. SvelteKit fails the build if a client module imports it.
- `src/lib/server/apkg/` produces and consumes an **intermediate representation**, never
  Drizzle rows directly. Import is `apkg -> IR -> db`, export is `db -> IR -> apkg`. The IR
  is where format quirks are normalised, and it is what unit tests assert against.
- Route handlers stay thin: parse, authorise, delegate to `lib/server/db/queries/`, respond.

---

## 5. Data model

**Full DDL, rationale, and the migration checklist: [docs/schema.md](docs/schema.md).**
Read it before any schema change or query crossing the content/per-user-state boundary.

The parts you need without opening it:

- Content tables (`note_types`, `fields`, `templates`, `notes`, `cards`, `decks`,
  `deck_access`) hold **no scheduling state**. Per-user state is `user_card_state`,
  PK `(user_id, card_id)`.
- **UUIDv7 ids, client-generated where possible.** `review_log` rows are created on the
  client, so a client-generated id makes retry idempotent for free. Anki's numeric ids are
  kept as `anki_id` columns for export fidelity, never as keys.
- `notes.guid` + `UNIQUE (deck_id, guid)` is what makes re-import idempotent.
- `review_log` is append-only training data. `user_fsrs_params.params` is a JSON array plus
  an explicit `fsrs_version`.
- **The day boundary is not midnight UTC.** It's a per-user rollover hour (default 04:00
  local, `users.timezone` + `users.day_start_hour`), and it's computed in the query, not the
  client.

---

## 6. The review loop and write queue

This is the part that dictates the client architecture (invariant §2.6). Build it first, and
build it correctly, because everything else in the client is downstream of it.

**Session start.** One request fetches a batch of due cards — rendered content, media URLs,
and current `user_card_state` — plus the user's FSRS params. Prefetch the next N cards while
the current one is displayed. Never fetch per card.

**Grading, synchronously and locally:**

1. `ts-fsrs` computes the next state from the current card state + rating + `now`.
2. Apply the new state to the in-memory queue and advance the UI immediately.
3. Append a `ReviewEvent` (client-generated UUIDv7) to a durable local queue.
4. Return. No `await` on the network anywhere in this path.

**Draining.** A background worker POSTs batches to `/api/reviews/batch` with exponential
backoff. On success, drop the entries. The queue persists to `localStorage` (or IndexedDB
once volume warrants) so a tab close mid-session doesn't lose reviews.

**Server contract — must be idempotent.** Same batch twice is a no-op:

```
POST /api/reviews/batch
  { events: [{ id, cardId, rating, reviewedAt, durationMs,
               stateBefore: {...}, stateAfter: {...} }] }

for each event:
  INSERT INTO review_log (...) ON CONFLICT (id) DO NOTHING
  UPDATE user_card_state SET <stateAfter>
    WHERE user_id=$u AND card_id=$c
      AND (last_review IS NULL OR last_review < $reviewedAt)
```

The `last_review <` guard makes application order-independent and last-write-wins by *review
time*, not arrival time — the correctness property that makes a retrying queue safe.

**The server is not the scheduler authority in Phase 1, but it must be able to be.** Keep a
server-side recompute path that replays `review_log` through `ts-fsrs` to rebuild
`user_card_state`. It is needed for import backfill, for repairing a client bug, and for
parameter refits. Never delete it as "unused."

---

## 7. `.apkg` / `.colpkg` mapping

**Container layout, both collection schemas, field-level gotchas, and the fixture plan:
[docs/apkg-format.md](docs/apkg-format.md).** Read it before touching
`src/lib/server/apkg/`.

The parts you need without opening it:

- `.apkg` is a zip: a SQLite collection, a `media` JSON index-to-filename map, and media
  files named by index. Two collection schemas must both be readable — 11 (note types and
  decks as JSON blobs in `col`) and 18+ (real tables).
- Everything goes through an **IR**: `apkg -> IR -> db`, `db -> IR -> apkg`. Never Drizzle
  rows directly.
- Import is idempotent on `(deck_id, guid)`; `revlog` becomes `review_log`, which is the
  difference between a cold start and a warm one.
- Two traps that silently produce plausible-looking wrong data: **`cards.due` is
  days-since-`col.crt` for review cards but a position integer for new ones**, and
  **`cards.ivl` is days when positive, seconds when negative**.
- That doc's contents are unverified against a real Anki build. Treat it as a map, not a
  contract, and correct it in place as fixtures are inspected.

---

## 8. Note-type rendering

Anki templates are their own small language, and this is more work than it looks:
`{{Field}}`, `{{#Field}}…{{/Field}}` and `{{^Field}}` conditionals, `{{FrontSide}}`,
filters (`{{text:Field}}`, `{{furigana:Field}}`, `{{type:Field}}`), and cloze deletion
(`{{c1::hidden::hint}}`) where one note generates N cards by cloze ordinal.

Keep it in `src/lib/render/`, isomorphic, pure `(template, fields) -> html`, with a golden-
file test per construct. **Sanitise on render** — note content is user-authored HTML and
shared decks mean it is *other users'* HTML. Card content is untrusted input in the
multiuser model, unlike in Anki where it is always your own.

---

## 9. Conventions

- **TypeScript strict**, `noUncheckedIndexedAccess` on. No `any` in `lib/fsrs/` or
  `lib/server/apkg/` — these are the correctness-critical modules.
- **Time is `timestamptz`, always UTC in the DB.** Local-time reasoning happens only at the
  day-boundary calculation (§5) and in display formatting.
- **Migrations are generated by `drizzle-kit`, committed, and immutable once merged.** Fix
  forward with a new migration; never edit an applied one.
- **Authorisation is explicit at the query layer.** Every query touching a deck takes a
  `user_id` and joins `deck_access`. Do not rely on route guards alone — a shared deck means
  "readable by some users" is the normal case, not the exception.
- **No cross-user reads without a `deck_access` row.** The only exception is `visibility =
  'public'`, and that grants read of *content*, never of another user's `user_card_state`.
- Naming: `snake_case` in SQL, `camelCase` in TS, Drizzle handles the mapping.
- Comment density matches surrounding code. The `apkg` modules earn comments (they encode
  external format facts); route handlers do not.

---

## 10. Testing

Priority order, highest first — this reflects where silent wrongness is most expensive:

1. **FSRS wrapper parity.** The same card + rating + timestamp produces byte-identical state
   through the client path and the server recompute path. Property-based over random review
   sequences. If this ever fails, stop and fix it before anything else.
2. **`.apkg` round-trip.** `import(export(import(f))) == import(f)` for fixture files from
   several Anki versions (schema 11 and 18+, with and without FSRS data, with media, with
   cloze, with non-ASCII filenames). **Collect these fixtures early** — they are the hardest
   test asset to produce later and the format is where the unknown-unknowns live.
3. **Write-queue idempotency.** Replaying a batch, reordering it, and interleaving it with a
   later review all converge to the same `user_card_state`.
4. **Access control.** Table-driven: for each (role, resource, operation), assert allow/deny.
   Add a row on every new endpoint.
5. **E2E reviewer** (Playwright): keyboard grading, optimistic advance, queue drains, and a
   session survives the network being cut mid-review.

Fixtures go in `tests/fixtures/apkg/` with a README recording which Anki version produced
each and what it exercises.

---

## 11. Build order

**Phase 1 — single-user core.** A complete product for one user; ship before anything else.

1. Scaffold: SvelteKit, TS strict, Postgres, Drizzle, CI running lint + unit tests.
2. Schema §5 in full — *including* `deck_access` and the `user_id` in `user_card_state`,
   even though Phase 1 has one user per deck. The columns are free now and structural later.
3. Auth + accounts.
4. `lib/fsrs/` isomorphic wrapper + parity tests (§10.1).
5. Deck / note-type / note / card CRUD.
6. Template rendering (§8).
7. **The reviewer and the write queue (§6)** — the piece everything else is downstream of.
8. `.apkg` import, then export.
9. Per-user parameter optimisation + desired-retention setting.

**Phase 2 — multiuser.** `deck_access` roles enforced; deck forking that preserves the
fork's `user_card_state`; classroom cohorts with per-student retention, due counts, and
lapse hotspots; public deck directory with per-deck licence display.

**Deferred:** full offline study — deck and media pre-caching, IndexedDB, multi-device
conflict resolution. Cheap to add later precisely because scheduling is already local.
Revisit when there are classroom users, not before.

**Explicitly not doing:** Anki sync protocol. Native mobile apps. Plugin system.
LLM-generated cards.

---

## 12. Open questions

Unresolved. Do not silently pick one — surface it.

- ~~**Licence.**~~ Settled: AGPL-3.0-or-later. See `LICENSE` and the README's Licensing
  section.
- **Parameter optimisation implementation.** `ts-fsrs` schedules but the reference optimiser
  is Rust (`fsrs-rs`, exposed via `fsrs-rs-nodejs`). Using it reintroduces a native
  dependency — as a *prebuilt binary*, not a forked codebase, so it costs deployment
  complexity rather than the maintenance burden that sync would have. Slightly qualifies the
  README's "no Rust anywhere." Alternatives: pure-TS optimiser (slow, and a correctness
  surface we said we wouldn't own) or an out-of-process optimiser job. **Decide before step
  9 of Phase 1.**
- **Auth library.** Lucia-style hand-rolled sessions vs Auth.js. Keep it behind
  `lib/server/auth/` so the choice stays reversible.
- **Native-speaker check on "Enshu."** Connotation is where non-native judgement is least
  reliable. Renaming a repo is cheap; renaming a product with users and inbound links is not.
- **Deck-content licensing** for the public directory. Shared decks carry their own terms and
  Ankitects has objected publicly to redistribution. The directory must record and display a
  per-deck licence.
- **Media storage backend** for self-hosters: filesystem vs S3-compatible. Content addressing
  (§5) makes this swappable, so it can wait.

---

## 13. Trademark guardrail

`ANKI` is a registered trademark of Ankitects Pty Ltd and is actively enforced. When writing
any user-facing copy, docs, or repo metadata:

- ✅ Descriptive: "Enshu imports Anki decks." Nominative fair use.
- ⚠️ Never brand anything `Anki<something>`.
- ❌ Never use `AnkiWeb` — it names the official service and implies affiliation.

Keep the non-affiliation notice in the README. Findability comes from the GitHub description
and topics (`anki`, `spaced-repetition`, `fsrs`, `flashcards`, `srs`, `self-hosted`,
`education`, `classroom`, `sveltekit`), not from the name.

---

## 14. Branching, commits, releases

**Branching.** Never commit to main. Before the first commit in a session, check the current
branch; if on main, create the branch yourself using the standard below — don't ask for a
name. Naming: `feature/<issue-id>-<short-slug>` when the work maps to a GitHub issue, else
`feature/<short-description>`. Push branch + open PR, never push main directly.

**Pre-commit sequence:**

1. Typecheck, lint, and unit tests pass (`svelte-check`, ESLint, `vitest run`).
2. If the schema changed, the generated migration is committed and applies cleanly to a fresh
   database.
3. Present a multi-select question for review passes to run (recommend based on diff risk,
   mark "(Recommended)"), then run chosen ones in order and summarize findings:
   - `/code-review low` — cheap high-confidence pass, no agent spawn. Default for small/
     low-risk diffs.
   - `/code-review medium` — broader pass. Default for normal feature branches. Spawn agents
     only if token-efficient to do so.
   - `/code-review high` / Opus-High single agent — deep review. Default for large/risky/
     cross-cutting work (schema, auth, FSRS, `.apkg`).
   - `/simplify` — quality-only cleanup (reuse/simplification/efficiency/altitude), no
     bug-hunting. Combine with a code-review pass or standalone for cleanup diffs.
   - Skip review.
4. Pause for user manual testing.
5. On approval, commit. **Never commit without the user's explicit go-ahead** — including
   follow-up fixes on an already-open PR, not just the first commit.

**Don't run the app to verify.** Use typecheck / lint / unit tests, and describe the manual
verification steps for the user instead. Start a dev server only when explicitly asked.

**Changelog.** Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Update `CHANGELOG.md` once per PR/merge (not per commit); all branch changes under one
version entry, grouped under `### Added` / `### Changed` / `### Fixed` / `### Removed` /
`### Security` / `### Deprecated` (only the subheadings you need):

```
## [0.1.X] - YYYY-MM-DD
### Added
- <one-line summary> ([#NNN](https://github.com/<owner>/enshu/issues/NNN))
```

Link is a pointer to the issue; omit only if no issue. No comparison links in the footer.
After committing a version bump, tag it: `git tag vX.Y.Z` (push with the branch/PR, never
force). While major `x` is 0, `z` increments with every PR; `y` bumps only for a deliberate
milestone release.

**Plans.** Save implementation plans (Plan Mode, issue-tied) to
`docs/plans/<issue-id>-<description-stem>.md`.

---

## 15. Issue workflow

**Sizing before implementing.** `/evaluate-issue <NNN>` scopes the work and recommends model
+ reasoning level for the implementing session, and whether sub-agents help. It recommends
only, it doesn't implement.

**Agents.** Prefer agents only when token-efficient or when a different model/effort level is
genuinely needed. Don't use agents to save wall-clock time.

**Labels.**
Severity (any defect): `sev: critical` security hole / data loss / shipped crash;
`sev: high` user-visible correctness bug; `sev: med` latent bug, low practical risk;
`sev: low` cosmetic / log noise / style.
Area (all issues): `area: security` auth/CSRF/session/sanitisation; `area: db`
queries/tx/pool/schema; `area: fsrs` scheduling, parameters, optimiser; `area: apkg`
import/export/media; `area: build` build config/deps/toolchain; `area: refactor` cleanup, no
behaviour change; `area: test` coverage.

> A `sev: critical` bucket that deserves its own reflex: **anything that silently corrupts
> `review_log` or `user_card_state`.** It is unrecoverable training data (§2.5), and the
> failure is invisible until an optimiser fit comes out wrong.

**Closing issues.** Prefer `closes #NNN` / `fixes #NNN` in the PR description for auto-close.
Multi-issue PRs: the keyword must precede EVERY issue number (bare numbers after a comma
don't auto-close) — repeat the keyword per line:

```
Closes #451
Closes #450
```

Add a one-sentence resolution comment before closing.

---

## 16. Environment and tooling

Windows 11, PowerShell primary (Bash tool also available — each takes its own syntax).

- **No `jq` installed.** Use `gh ... --json <fields> --template '{{...}}'` or PowerShell
  `ConvertFrom-Json`.
- **`gh issue view` / `gh pr view` in plain-text mode silently return empty** in both the
  Bash and PowerShell tools — the pager swallows output and exits 0. Always pass
  `--json title,body,labels,comments`.
- **Run `.cmd` / `.bat` shims via the PowerShell tool**, not Bash (Bash only captures the cmd
  banner).
- Line endings: set `.gitattributes` (`* text=auto eol=lf`) at scaffold time so this repo
  never develops the mixed CRLF/LF problem. Do it in the first commit — retrofitting it
  rewrites every file.

---

## 17. What NOT to touch

- The invariants in §2 — not without an explicit, recorded decision.
- Applied migrations in `drizzle/` — immutable once merged. Fix forward.
- `review_log` — append-only. No `DELETE` path without a written decision (§2.5).
- `ankitects/anki` source — never read it into this codebase (§2.7). Format specs and
  clean-room parsers only.
- The server-side recompute path — needed for import backfill, client-bug repair, and
  parameter refits. Never delete it as "unused" (§6).
- `lib/fsrs/` must stay isomorphic: no DB, no `fetch`, no browser globals. It is what
  guarantees client and server schedule identically.

---

## 18. Glossary

| Term | Meaning |
|---|---|
| **Note** | The content unit a user authors. Has typed fields. |
| **Card** | One question/answer view generated from a note by a template. A note makes N cards. |
| **Note type** | Field definitions + templates + CSS. Anki calls this a "model" (`mid`). |
| **Cloze** | A note type where `{{c1::…}}` markers generate one card per ordinal. |
| **DSR** | FSRS's memory model: Difficulty, Stability, Retrievability. |
| **Stability** | Days until recall probability falls to 90%. The memory half-life for a card. |
| **Retrievability** | Probability of recall right now. Decays with elapsed time. |
| **Desired retention** | User-set target (e.g. 0.9). The dial that trades workload against recall. |
| **Lapse** | A review of a `review`-state card rated Again. |
| **`guid`** | Anki's stable per-note identifier. The idempotency key for import. |
| **`.apkg` / `.colpkg`** | Anki's deck / full-collection export. Zip of SQLite + media. |
| **IR** | The intermediate representation between `.apkg` and our schema (§4). |
