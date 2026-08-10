# Architecture — Enshu

**What this system is and how it is built.** Companion to [CLAUDE.md](../CLAUDE.md), which
holds the working rules, the invariants, and the process an agent follows. This file holds the
descriptive half: state, stack, layout, protocols, roadmap, and vocabulary.

[README.md](../README.md) is the public rationale ("why these decisions"); [enshu.md](../enshu.md)
is the personal-notes digest of it. If a decision here contradicts the README, the README wins
on *rationale* and this file wins on *mechanics*. If you change a decision, update both.

> **Section numbers are shared with CLAUDE.md, not restarted here.** The two files are one
> numbering space split across two documents, so every `§N` reference in the codebase keeps
> resolving to exactly one place. CLAUDE.md's preamble maps every section to its file.

---

## 1. Current state

**Phase 1 is most of the way built, and this document is deliberately ahead of the code.**

Merged: scaffold (#3), schema (#4), `lib/fsrs/` (#5), `lib/render/` + sanitisation (#12, #6),
auth (#7), CRUD (#8), `.apkg` reader (#9), the reviewer (#13), auth and deck UI (#28, #29),
re-import dedup keys (#32). Still open: `.apkg` import-to-database and export (#33, #10),
media (#34), parameter optimisation (#11).

**The gap you need to know about.** [CLAUDE.md §2.7](../CLAUDE.md#2-invariants--do-not-violate-without-an-explicit-decision) and §6 describe a server-authoritative grading path.
The code still implements the earlier client-authoritative one — `src/lib/review/` computes
FSRS locally and POSTs `stateAfter`, and the server writes what it receives. That is a
recorded decision to change the code, not drift to reconcile in the other direction. When the
two disagree, **this file is right and the code is stale.**

Specifically, as of 2026-08-09:

- `src/lib/review/session.ts`, `wire.ts`, `write-queue.ts` and `/api/reviews/batch` implement
  the client-authoritative path. `write-queue.ts`'s `localStorage` durability existed to serve
  offline study, which §11 now rules out; the idempotent endpoint and client-generated event
  ids stay.
- The session loader fetches 100 cards once and never refills — §6 now specifies 20, refilling
  at 10 unseen remaining.
- `src/lib/server/db/schema.ts` still defines `deckVisibility`, `visibility` and
  `forkedFromDeckId`, and `queries/access.ts` still has the `visibility === 'public'` branch
  that CLAUDE.md §9 says must not exist. A `0005` migration is owed.
- `src/lib/server/apkg/ir.ts` justifies `IrNote.primaryDeckAnkiId` with `UNIQUE (deck_id, guid)`,
  a key #32 replaced. See §20 — the fix belongs to #33 and needs no migration.
- Invariant references in code comments are off by one past §2.6 (old §2.7 → §2.8, §2.8 → §2.9),
  and §10's testing priorities gained a new #1, so every `§10.N` shifted. A comment reading
  "CLAUDE.md §6" now names the wrong file, though the number itself is still right.

Zero users, and the project is days old. Everything is cheap to change, so nothing here is
kept because it is already written — least of all the parts that are (see [CLAUDE.md §14](../CLAUDE.md#14-branching-commits-releases)'s review step,
and prefer deleting a wrong abstraction to preserving it).

---

## 3. Stack

| Concern | Choice | Pinned version | Notes |
|---|---|---|---|
| App framework | SvelteKit + TypeScript (strict) | `@sveltejs/kit@^2.63.0`, `svelte@^5.56.1`, `typescript@^6.0.3` | SSR for everything except the reviewer; the reviewer is client-driven. |
| Scheduler | `ts-fsrs` | `5.4.1` (exact) | Runs on **both** client and server. |
| Database | PostgreSQL | `postgres:16` (Docker image) | Row-level tenancy via `user_id` columns and explicit query scoping. |
| ORM / migrations | Drizzle + `drizzle-kit` | `drizzle-orm@^0.45.2`, `drizzle-kit@^0.31.10` | Schema is TypeScript-first; migrations are generated SQL, committed, never edited after merge. |
| `.apkg` read/write | `better-sqlite3` (server) | `better-sqlite3@13.0.3`, `fflate@0.8.3`, `zstd-napi@0.0.13` (all exact) | `fflate` for zip; `zstd-napi` for zstd-compressed schema 18+ exports — native binding, chosen over `@bokuweb/zstd-wasm` for active maintenance (see git history for the comparison). |
| Tests | Vitest + Playwright | `vitest@^4.1.8`, `@playwright/test@^1.60.0` | See [CLAUDE.md §10](../CLAUDE.md#10-testing). |
| Auth | Hand-rolled sessions (Lucia-style) | `@node-rs/argon2@2.0.2` (exact) for password hashing | See §12 for rationale. Lives entirely behind `src/lib/server/auth/`. |

Versions pinned at scaffold time (`feature/3-scaffold`, 2026-08-07). Non-critical-path
devDependencies (SvelteKit, Drizzle, Vitest, Playwright, ESLint, Prettier) use caret ranges,
per `package.json`; `ts-fsrs`, the `.apkg` codec deps, and `@node-rs/argon2` are pinned exact
since a silent version drift there is the correctness/security hazard this file keeps warning
about.

**One language, end to end.** The client schedules locally for its own UI (CLAUDE.md §2.6), so an FSRS
implementation in JS exists regardless of backend language. A Python or Go server would mean
two implementations of the same algorithm kept in agreement by hand. Since the server's answer
is the authoritative one (CLAUDE.md §2.7), that disagreement would be a *scheduling* bug rather than a
display one. Hence TypeScript everywhere — one `ts-fsrs`, one set of semantics.

> **Run the exact same `ts-fsrs` version on client and server**, and record it in
> `user_fsrs_params.fsrs_version`. This used to be the system's worst failure mode because it
> was silent. Under CLAUDE.md §2.7 it isn't silent any more — the server recomputes every grade and a
> mismatch shows up as a divergence (§6) rather than as quietly wrong intervals. Keep the
> versions pinned together anyway: the divergence counter is a smoke alarm, not a reason to
> leave the stove on.

---

## 4. Repo layout

```
enshu/
├─ CLAUDE.md              working rules, invariants, process
├─ README.md              public rationale
├─ enshu.md               personal notes digest
├─ HANDOFF.md             what to pick up next; links every open issue
├─ .claude/
│  ├─ memory/             agent memory — repo-local, gitignored, see CLAUDE.md §19
│  └─ skills/
├─ docs/
│  ├─ architecture.md     this file
│  ├─ schema.md           OUR full DDL + rationale (§5 lives here)
│  ├─ schema-diagram.md   OUR ER diagrams
│  ├─ anki-schema.md      ANKI's tables and columns — where a value lives
│  ├─ anki-schema-diagram.md  ANKI's ER diagrams
│  ├─ apkg-format.md      the .apkg container + encoding traps (§7 lives here)
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
│  ├─ fixtures/apkg/      real exports from multiple Anki versions — see CLAUDE.md §10
│  └─ e2e/
└─ scripts/
```

**Boundary rules**

- **Unit tests are colocated with source** (`foo.ts` + `foo.spec.ts` / `foo.test.ts` in the
  same directory), not under `tests/unit/`. The scaffold's Vitest project and
  `.svelte-kit/tsconfig.json` only glob `src/**`, so colocated is what actually runs and
  typechecks. `tests/` holds only what genuinely lives outside `src/`: apkg fixtures and
  Playwright e2e specs.

- `src/lib/fsrs/` is isomorphic: pure functions, no DB, no `fetch`, no browser globals. This
  is what guarantees client and server schedule identically.
- `src/lib/server/**` is server-only. SvelteKit fails the build if a client module imports it.
- `src/lib/server/apkg/` produces and consumes an **intermediate representation**, never
  Drizzle rows directly. Import is `apkg -> IR -> db`, export is `db -> IR -> apkg`. The IR
  is where format quirks are normalised, and it is what unit tests assert against.
- Route handlers stay thin: parse, authorise, delegate to `lib/server/db/queries/`, respond.

---

## 5. Data model

**Full DDL, rationale, and the migration checklist: [schema.md](schema.md).**
Read it before any schema change or query crossing the content/per-user-state boundary.

The parts you need without opening it:

- Content tables (`note_types`, `fields`, `templates`, `notes`, `cards`, `decks`,
  `deck_access`) hold **no scheduling state**. Per-user state is `user_card_state`,
  PK `(user_id, card_id)`.
- **UUIDv7 ids, client-generated where possible.** `review_log` rows are created on the
  client, so a client-generated id makes retry idempotent for free. Anki's numeric ids are
  kept as `anki_id` columns for export fidelity, never as keys.
- `notes.guid` + `UNIQUE (owner_id, guid)` is what makes re-import idempotent; decks and note
  types dedup on `UNIQUE (owner_id, name)`.
- `review_log` is append-only training data. `user_fsrs_params.params` is a JSON array plus
  an explicit `fsrs_version`.
- **The day boundary is not midnight UTC.** It's a per-user rollover hour (default 04:00
  local, `users.timezone` + `users.day_start_hour`), and it's computed in the query, not the
  client.

---

## 6. The review loop

This is the part that dictates the client architecture (CLAUDE.md invariants §2.6 and §2.7). Build it
first, and build it correctly, because everything else in the client is downstream of it.

Two rules, and they are not in tension:

- **The client never waits.** It computes locally, shows the answer, advances (CLAUDE.md §2.6).
- **The client is never believed.** The server recomputes and stores its own answer (CLAUDE.md §2.7).

The client's copy of `ts-fsrs` exists to make the interface instant. It is a *prediction* of
what the server will conclude, and it is almost always right — which is exactly why the rare
case where it isn't must be caught rather than trusted.

### Fetching cards

**Never fetch per card.** Beyond that, the policy below is the whole of it — the numbers are
starting values, but the *shape* is not arbitrary and the reasoning is recorded so anyone
retuning them can tell which way is safe.

**Session start: the first batch rides along with the page load.** The reviewer is a SvelteKit
route with a `+page.server.ts`, so the batch is part of the document response. There is no
separate request for the first card, and therefore nothing to make faster by fetching card 1
on its own — a card already in the HTML beats any round trip you could shorten. The payload is
rendered, sanitised card content, each card's `user_card_state`, the user's FSRS params, and
the study-day end.

| | Value | Why |
|---|---|---|
| Initial batch | **20 cards** | Rendered HTML runs ~2–4 KB per card, so 20 is ~40–80 KB — one image, and small beside the JS bundle already loading. Bigger batches buy nothing: a user who quits after 15 cards paid for the rest. |
| Refill trigger | **fewer than 10 *unseen* cards left** | At a brisk one grade per second that is ten seconds of headroom against a refill that costs ~300 ms on a bad connection. Size the buffer against the *worst* refetch, not the average. |
| Refill size | **20 cards** | Same reasoning as the initial batch. |
| End of deck | server sets **`exhausted: true`** | Without an explicit terminal signal, the end of a session degrades into a poll loop. |

**The trap: count *unseen* cards, not queue length.** A card in learning steps goes back to the
tail of the queue, so a queue holding 12 cards might be 11 requeued repeats and one card the
user has never seen. Triggering on queue length would skip the refill and then stall on the
next grade. Only cards never graded this session count toward the threshold.

**Refills must not re-send what the client already holds.** Page on a stable keyset over the
queue ordering `(due, card_id)`, and exclude cards the user has already reviewed since the
study day began — those come back from the client's local queue if they are still due, never
from a refill. The client also dedupes by `cardId` on receipt, which is cheap and covers the
case where a graded card's new `due` lands it back inside the window.

**Media is deliberately not part of this yet.** Card content prefetches cheaply; media does
not, and prefetching 20 cards' images is a different problem from prefetching 20 cards' HTML.
Until #34 wires the blob store to live decks, the session payload carries no media URLs. When
it does, prefetch media for the next two or three cards only — not for the batch.

**Grading, synchronously and locally:**

1. `ts-fsrs` computes the next state from the current card state + rating + `now`.
2. Apply the new state to the in-memory queue and advance the UI immediately.
3. Hand a `ReviewEvent` (client-generated UUIDv7) to the sender.
4. Return. No `await` on the network anywhere in this path.

**Sending.** Events go out as they are produced, batched only to be kind to mobile radios —
never to build a durable offline log (§11 rules out offline study). Retry transient failures
with backoff; the endpoint is idempotent, so a retry after an ambiguous failure is always
safe. Losing a handful of unsent events to a hard crash is acceptable; **storing a wrong one
is not** — missing rows weaken an optimiser fit, wrong rows corrupt it (CLAUDE.md §2.5).

**Server contract — recompute, compare, store.** Idempotent: the same batch twice is a no-op.

```
POST /api/reviews/batch
  { events: [{ id, cardId, rating, reviewedAt, durationMs,
               predicted: {...} }] }        <- the client's own result, for comparison only

for each event:
  authorise (user_id, card_id) via deck_access
  before  := SELECT * FROM user_card_state WHERE user_id=$u AND card_id=$c
  after   := ts-fsrs.next(before, rating, reviewedAt)     <- the authority

  if diverges(after, event.predicted): log + count (see below). Never change `after`.

  INSERT INTO review_log (...) VALUES (<before>, <after>) ON CONFLICT (id) DO NOTHING
  UPDATE user_card_state SET <after>
    WHERE user_id=$u AND card_id=$c
      AND (last_review IS NULL OR last_review < $reviewedAt)

  -> respond with <after> so the client can reconcile its queue
```

`rating`, `reviewedAt`, `durationMs` and `cardId` are the only fields the client is entitled
to assert. Everything written to `review_log` and `user_card_state` is derived server-side
from state the server already holds.

The `last_review <` guard makes application order-independent and last-write-wins by *review
time*, not arrival time — the correctness property that makes a retrying sender safe.

**Divergence handling.** `predicted` is compared, never stored and never authoritative:

- Compare `state`, `reps`, `lapses` exactly; `stability` and `difficulty` within `1e-6`
  (`ts-fsrs` rounds to 8 decimal places, which is why those columns are `double precision`);
  `due` to the second.
- On mismatch: emit a structured log line with `user_id`, `card_id`, both values, and both
  `fsrs_version`s, and increment a counter. **Do not add a table for this.** The expected rate
  is zero, so it is an alert condition, not queryable data — a table would imply routine
  divergence is normal.
- A nonzero counter means a stale client bundle, a `ts-fsrs` version skew, parameters that
  never reached the client after a refit, or tampering. All four are bugs to fix, and
  `replayReviews` (below) repairs whatever they touched.

**The server-side recompute path is load-bearing.** `replayReviews` replays `review_log`
through `ts-fsrs` to rebuild `user_card_state`. It is what the live path above calls one event
at a time, *and* what import backfill, parameter refits, and post-incident repair call in bulk.
Never delete it as "unused."

---

## 7. `.apkg` / `.colpkg` mapping

Three documents cover the Anki side, and it is worth knowing which one answers your question
before opening any of them:

| Question | Document |
|---|---|
| *Where does this value live?* — table and column shapes | [anki-schema.md](anki-schema.md), with [anki-schema-diagram.md](anki-schema-diagram.md) as its visual companion |
| *How do I interpret it?* — container layout, which schema ships in which export, and the encoding traps | [apkg-format.md](apkg-format.md) |
| *Where does it go in our schema?* | [schema.md](schema.md) |

Read `apkg-format.md` before touching `src/lib/server/apkg/`.

The parts you need without opening any of them:

- `.apkg` is a zip: a SQLite collection, a `media` JSON index-to-filename map, and media
  files named by index. Two collection schemas must both be readable — 11 (note types and
  decks as JSON blobs in `col`) and 18+ (real tables).
- Everything goes through an **IR**: `apkg -> IR -> db`, `db -> IR -> apkg`. Never Drizzle
  rows directly.
- Import is idempotent on `(owner_id, guid)`; `revlog` becomes `review_log`, which is the
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

## 11. Build order

**Phase 1 — single-user core.** A complete product for one user; ship before anything else.

1. Scaffold: SvelteKit, TS strict, Postgres, Drizzle, CI running lint + unit tests.
2. Schema §5 in full — *including* `deck_access` and the `user_id` in `user_card_state`,
   even though Phase 1 has one user per deck. The columns are free now and structural later.
3. Auth + accounts.
4. `lib/fsrs/` shared wrapper + parity tests (CLAUDE.md §10.2).
5. Deck / note-type / note / card CRUD.
6. Template rendering (§8).
7. **The reviewer (§6)** — the piece everything else is downstream of.
8. `.apkg` import, then export.
9. Per-user parameter optimisation + desired-retention setting.

**Phase 2 — multiuser.** `deck_access` roles enforced; co-authoring a deck while each author
keeps a private review history; classroom cohorts with per-student retention, due counts, and
lapse hotspots. The seam in CLAUDE.md §2.1 is what makes all of it possible, and §2.7 is what makes the
per-student numbers worth showing.

**Explicitly not doing.** Each of these is a decision, not a backlog:

- **Anki sync protocol.** See the README.
- **Full offline study** — deck and media pre-caching, IndexedDB, multi-device conflict
  resolution. Not deferred: not wanted. Enshu is a server, and the reviewer being
  *network-independent for the duration of a grade* (CLAUDE.md §2.6) is a latency property, not a step
  toward offline. Should this ever be reconsidered, nothing here forecloses it: `review_log`
  is the source of truth and `replayReviews` already exists.
- **Deck forking** and a **public deck directory.** Sharing a deck via `deck_access` already
  covers co-authoring and the classroom; users who want an outside deck import its `.apkg`
  themselves. Note that export-then-reimport is *not* a substitute for forking — reimport
  mints new `cards` rows and `user_card_state` is keyed by `card_id`, so progress does not
  survive it. Forking is the feature that would, and it is out of scope until something
  concrete demands it.
- **Native mobile apps.** The web app is the mobile story.
- **Plugin system.**
- **LLM-generated cards** as a feature that calls a model API. The acceptable shape is a
  documented text format the user pastes, produced by whatever model they already use — no
  API key, no per-token cost, no vendor in the dependency tree.

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
  9 of Phase 1** — and step 8 (`.apkg` import) is in flight, so this is the *next* gate, and
  the only open question left that blocks implementation rather than copy or policy.
- ~~**Auth library.**~~ Settled: hand-rolled sessions, Lucia-style. Auth.js's value is its
  OAuth provider ecosystem and adapter abstraction — neither earns its weight here. Phase 1
  is email/password only (CLAUDE.md invariant §2.9 rules out any federated-identity-shaped sync
  surface), and Auth.js's Drizzle adapter is community-maintained and fights our
  already-decided schema conventions (UUIDv7 ids, the `lower(email)` functional unique
  index) rather than matching them. Lucia the *package* is archived upstream in favour of a
  "copy this pattern" guide, so "Lucia-style" means exactly that: a `sessions` table keyed by
  the SHA-256 hash of a random token (the raw token lives only in the cookie, so a DB read
  never discloses a usable session), `argon2id` via `@node-rs/argon2` for password hashing,
  and an explicit `Origin`-header check on state-changing requests for CSRF. Full control,
  consistent with this project owning its own schema and `.apkg` codec rather than adopting
  someone else's shape. Kept behind `src/lib/server/auth/` regardless, so it stays
  reversible.
- **Native-speaker check on "Enshu."** Connotation is where non-native judgement is least
  reliable. Renaming a repo is cheap; renaming a product with users and inbound links is not.
- ~~**Deck-content licensing.**~~ Closed by dropping the public deck directory (§11). Enshu
  never redistributes deck content: a deck reaches another user through a `deck_access` row on
  this instance, or through an `.apkg` the user obtained and imported themselves. There is no
  catalogue to attach per-deck licence metadata to, and no republication for Ankitects' stated
  objection to attach to. Reopen this the moment any deck-sharing surface crosses instances.
- ~~**Media storage backend.**~~ Settled: **filesystem**, content-addressed, for self-hosted
  deployment. Bytes live at `${MEDIA_ROOT}/<sha[0:2]>/<sha>`; the database holds metadata rows
  only (`media_blobs.sha256`/`size_bytes`/`mime`) and never a `bytea` column. This is what Anki
  itself does, one step further: content-addressed rather than filename-addressed, because two
  users' decks can each carry a different `image.jpg`. S3-compatible remains a drop-in later —
  the metadata-row-plus-external-bytes shape is identical, only "external" changes.
  **The decision is settled; the store is not built** — [#34](https://github.com/Jolls/enshu/issues/34)
  is still open, and the only media code that exists today is `src/lib/server/apkg/media.ts`,
  which reads a package's media map during import. There is no `src/lib/server/media/`.

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

---

## 20. Deviations from Anki

CLAUDE.md §2.10 is the rule: follow Anki's model unless multiuser forces a change. This is the
register of where we don't, and why. **Add a row when you diverge.** An empty justification
column is a bug report.

The test for every row: *does this trace back to the content/progress seam (§2.1)?* If yes, it
is forced and settled. If no, it is a choice — and a choice needs a better reason than "ours
seemed tidier."

### Forced by multiuser

| | Anki | Enshu |
|---|---|---|
| Scheduling state | On the `cards` row, alongside the content pointer | `user_card_state`, keyed `(user_id, card_id)` (§2.1) |
| Grading authority | No client/server boundary exists — it is a local app | Server recomputes and decides (§2.7). Anki never faced this question; multiuser creates it |
| Note identity | `guid`, unique within the one collection | `UNIQUE (owner_id, guid)`. A collection *is* one user, so owner-scoping is the same rule restated under multiuser |
| Row ids | Epoch-millis integers, unique per collection | UUIDv7; Anki's ids kept as `anki_id` for export fidelity. Per-collection ids cannot key across users — deck id 1 is `Default` in every collection ever made |
| Card HTML | Trusted: it is always your own content | Sanitised on render (§8). A shared deck is *other users'* HTML |
| Deck-content licensing | AnkiWeb hosts a public deck catalogue | No catalogue; a deck moves by `deck_access` row or by a file its owner passed on (§11) |

### Deliberate scope, not disagreement

| | Anki | Enshu |
|---|---|---|
| Sync protocol | Yes | No, permanently (§2.9) |
| Filtered / custom-study decks | Yes | Not built. The importer reads `odid`/`odue` and files cards under their real home deck, so nothing is lost on the way in |
| Add-ons | Plugin API | No plugin system (§11) |

### Unforced — obsolete, and due for removal

**One note, one deck.** First, the cardinality, because it is easy to misread: **a card belongs
to exactly one deck** — `cards.did` is a single column, and so is our `cards.deck_id`. We match
Anki there and always have. Filtered decks are not an exception: they set `did` to the filtered
deck and keep the real home in `odid`, which is a temporary move with a forwarding address, not
membership in two decks.

What Anki does *not* do is require a note's cards to share a deck. Each card carries its own
`did`, which is why "Deck Override" on a card template works and why a note's reverse card can
live in its own deck. People use this. `IrNote.primaryDeckAnkiId` collapses a note's cards to
one deck — the home deck of the lowest-numbered card — and that is the whole of the deviation.

**It is not a multiuser deviation, and its stated reason has already expired.** The rationale
recorded in `src/lib/server/apkg/ir.ts` is:

> Our schema scopes them to notes — `notes.deck_id` with `UNIQUE (deck_id, guid)` — because
> that unique index is the whole idempotency guarantee, so the reader has to pick one.

That was correct when written. A note keyed on `(deck_id, guid)` must have exactly one deck or
its identity is undefined. But #32 replaced that key with `UNIQUE (owner_id, guid)`, so the
constraint that forced the flattening no longer exists — and the multiuser argument is what
*removed* it, not what created it. The comment simply outlived the schema.

**Nothing is lost yet.** `IrCard.deckAnkiId` already carries each card's own home deck, so the
IR is faithful today; `primaryDeckAnkiId` is a derived convenience with no consumer, because
the database writer is #33 and unbuilt. The fidelity loss only materialises if #33 files cards
by their note's deck instead of their own.

So the resolution is cheap and belongs to #33: **file `cards.deck_id` from
`IrCard.deckAnkiId`**, and keep `notes.deck_id` as the note's home deck — where it was first
filed, where the notes list shows it, and the default for cards generated later. No migration,
no reader change, one decision in the writer. `primaryDeckAnkiId` then either becomes that home
deck or goes away.

One consequence to settle while doing it: `notes.deck_id` and `cards.deck_id` both cascade on
deck delete, so deleting a note's home deck would take cards living in *other* decks with it.
Anki's own answer is that deleting a deck deletes its cards, and notes left with no cards go
too. Deletion policy is already unreachable behind the FK restricts (#15), so settle both at
once.
