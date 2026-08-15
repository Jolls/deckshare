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

**The stack decision changed; the previously-merged code predates it and is superseded.**

An earlier TypeScript/SvelteKit/Postgres/Drizzle implementation reached most of Phase 1: scaffold
(#3), schema (#4), an isomorphic FSRS wrapper (#5), template rendering + sanitisation (#12, #6),
auth (#7), CRUD (#8), an `.apkg` reader (#9), the reviewer (#13), auth and deck UI (#28, #29),
re-import dedup keys (#32), and server-authoritative grading (#39). A ground-up re-evaluation of
the stack (recorded in [docs/plans/architecture-reconsidered.md](plans/architecture-reconsidered.md))
found that the reasoning which picked TypeScript end-to-end — "FSRS must run in the browser for
latency, so the server has to share its language" — doesn't hold once the server precomputes all
four rating outcomes per card at batch-fetch time instead of the client computing them. Once the
client doesn't need a scheduler, the server language reopens on its own merits, and the
conclusion is Go, no client-side FSRS, PostgreSQL retained, parameter optimisation deferred out
of MVP. This file and CLAUDE.md now describe that decision, not the merged TypeScript code.

**Nothing here is a migration.** Zero users, and the TypeScript code predates the decision by
days — there is no reason to carry any of it forward for its own sake.

**The old code is deleted.** `git log` on this repo's history has the full TypeScript
implementation if anything below turns out to need double-checking against it. Before deletion,
this file, `schema.md`, and `apkg-format.md` were re-checked against it for anything load-bearing
that hadn't already made it into the docs — session-cookie hardening, timing-safe login/signup,
the sanitisation allowlist's XSS-defense rationale, the advisory-lock deadlock-avoidance rule,
and a card-regeneration data-loss trap among them — and folded in where found.

**Build order step 1 (§11) is scaffolded** ([#49](https://github.com/Jolls/enshu/issues/49)):
`cmd/enshu/`, the `internal/` package skeleton (§4), `sqlc`/`goose` wiring, `golangci-lint`,
a multi-arch `Dockerfile`, and GitHub Actions CI.

**Build order step 2 (§11) has landed** ([#50](https://github.com/Jolls/enshu/issues/50),
[#51](https://github.com/Jolls/enshu/issues/51)): the full schema as 14 `goose` migrations and
`sqlc`-generated Go, with the terminal deletion policy (§20, docs/schema.md) and the deck-delete
transaction / last-access-holder guard (`internal/db/deletion.go`). Auth, FSRS, and `.apkg` logic
remain their own build-order steps.

**Build order step 3 (§11) has landed** ([#52](https://github.com/Jolls/enshu/issues/52)):
`internal/auth/` — signup/login/logout with a `__Host-`-prefixed session cookie, sliding
expiration, `argon2id` password hashing, an `Origin`-header CSRF check enforced centrally in
`Service.Middleware`, per-key rate limiting on login and signup, and an in-process ticker that
sweeps expired sessions and stale rate-limit buckets hourly. Also includes the `/settings`
profile and password-change routes (docs/routes.md).

**Build order step 5 (§11) has landed** ([#54](https://github.com/Jolls/enshu/issues/54)):
deck/note-type/note/card CRUD (docs/routes.md). `notes.owner_id = decks.owner_id` is now a
database-enforced composite foreign key (migration `00015`), not just a query-layer convention.
Card generation and card-regeneration-on-edit both go through `internal/db/cards.go`'s
`SyncNoteCards`, which diffs old and new (ordinal, template) pairs so an edit's surviving cards
keep their id, and with it their `user_card_state`/`review_log` history (docs/schema.md's
card-regeneration trap).

**Build order step 6 (§11) has landed** ([#55](https://github.com/Jolls/enshu/issues/55)):
`internal/render/` — the §8 template mini-language (`{{Field}}`, sections, filters, cloze
fan-out) and card-content/CSS sanitisation, built on `github.com/microcosm-cc/bluemonday`
(HTML) and `github.com/aymerick/douceur` (the note-type CSS blob). A pure package: no
`internal/db`, no `net/http`, no I/O. `RenderCard` is the sole entry point; `sanitiseCardHTML`
stays unexported, and the `{{type:Field}}` answer-input widget is spliced in *after*
sanitisation via `TypeAnswerInput`/`TypeAnswerExpected`, never through it — see
[docs/plans/55-note-type-rendering-sanitisation.md](plans/55-note-type-rendering-sanitisation.md)
for the full design. No route consumes it yet; the reviewer (step 7) is the first caller.

**Build order step 7 (§11) has landed** ([#56](https://github.com/Jolls/enshu/issues/56)): the
reviewer — `internal/review/` (batch precompute, server-authoritative grading, replay) and
`internal/http/review.go` (docs/routes.md's three routes), implementing §6 in full: the four
concurrency mechanisms (advisory locks acquired in ascending sorted-key order, events applied in
`reviewed_at` order, idempotency via `review_log.id`, out-of-order replay), the reviewedAt
clamp/reject policy, and the client-side queue module. This is also the first htmx/JS in the
repo — `web/static/` vendors htmx and a hand-written queue module (no build step), served from a
new `/static/` route, rather than the CDN load the stack table below describes for Pico CSS; see
`web/static/README.md`. One deliberate deviation from
[docs/plans/56-reviewer-batch-grading.md](plans/56-reviewer-batch-grading.md)'s design: rating
buttons are plain (no per-button `hx-post`) so grading can batch and retry with backoff, which a
literal per-button POST cannot do — a hidden `#review-sender` element, JS-triggered, is the single
network-touching sender.

---

## 3. Stack

Scaffolded as of [#49](https://github.com/Jolls/enshu/issues/49) (§1). This table describes the
target, decided in [docs/plans/architecture-reconsidered.md](plans/architecture-reconsidered.md).

| Concern | Choice | Notes |
|---|---|---|
| Server + HTTP | Go, stdlib `net/http` | No web framework required — most of the app is CRUD over forms and tables. |
| HTML rendering | `html/template` | Server-rendered; auto-escapes by default, which is most of §8's sanitise-on-render requirement for free. Settled over `templ` — see §12: no meaningful runtime/UX difference either way (the reviewer's felt latency is a client-side JS property, not a rendering one — §6), so the deciding factors were dependency count, no codegen step, and contributor accessibility for a project courting outside contributors. |
| CSS / interactivity | Pico CSS + htmx | Pico is classless — ships no JS of its own, so there's nothing to conflict with htmx's attribute-driven behaviour (unlike a component library such as Bootstrap, which ships its own JS for modals/dropdowns and can fight a swap that pulls the rug out from under it). htmx drives the CRUD shell entirely: `hx-*` attributes on plain HTML, handlers return HTML fragments instead of JSON, no client-side templating layer to keep in sync with the server's. The reviewer is the one exception — see §6. |
| Scheduler | `go-fsrs` (targets FSRS v6) | Runs **server-side only.** No client-side FSRS implementation exists — see §6. |
| Database | PostgreSQL | Row-level tenancy via `user_id` columns and explicit query scoping. Unchanged from the original stack decision — this was never a TypeScript-specific choice. |
| Typed SQL | `sqlc` | Generates Go structs/queries from real SQL, checked against the schema at compile time. |
| Migrations | `goose` | Plain SQL up/down files, checked in under `migrations/` — matches CLAUDE.md §9's "committed, generated SQL, immutable once merged, fix forward" convention directly, with no separate declarative-state layer to keep in sync. See §12. |
| `.apkg` read/write | `modernc.org/sqlite` (pure Go, no cgo), stdlib `archive/zip`, `klauspost/compress/zstd` | No native-binary-per-platform concern, and moot either way since deployment is prebuilt Docker images. |
| Auth | Hand-rolled sessions | Session token SHA-256-hashed at rest, `argon2id` for password hashing via `alexedwards/argon2id` (a thin wrapper over `golang.org/x/crypto/argon2` with sensible parameter defaults — see §12), `Origin`-header CSRF check. |
| Tests | Go's `testing` + Playwright | Playwright still applies unchanged — it drives a real browser and doesn't care what rendered the page. See [CLAUDE.md §10](../CLAUDE.md#10-testing). |
| Lint | `golangci-lint` v2, `linters.default: standard` | Start with the standard set, not `all` — add specific linters as a real gap shows up rather than fighting seventy opinions on day one. See §12. |
| CI | GitHub Actions | Single workflow: `go build`, `go vet`, `golangci-lint run`, `go test ./...` on push and PR. See §12. |
| Deploy | Single Go binary + Postgres, Docker / StartOS | Multi-arch Docker builds cross-compile rather than build per-host. Go 1.26 (current stable) — see §12. |

**No FSRS implementation runs in the browser, ever.** A card's outcome under each of the four
possible ratings is a pure function of its state as of the batch fetch, so the server computes
all four branches up front and ships them down as data (§6) — the client only looks one up. That
removed the reasoning that originally picked TypeScript end-to-end ("the client needs a
scheduler too, so pick one language to avoid two implementations kept in sync by hand"), which is
why the server language reopened and landed on Go instead: contributor accessibility for an AGPL
project (Go's learning curve is shallower than Rust's ownership/lifetime model), a clean
subprocess boundary to `fsrs-rs` if parameter optimisation ever needs it (§12) rather than Go's
`cgo` FFI story, and a good fit for what most of the app is — boring CRUD rendered server-side.
Rust end-to-end and Elixir/Phoenix were both seriously considered and are not wrong choices; the
full evaluation, including the direct check of the `go-fsrs` ecosystem's health and its optimizer
gap, is in [docs/plans/architecture-reconsidered.md](plans/architecture-reconsidered.md).

Dropping client-side FSRS also removes the version-skew failure mode the old callout here warned
about: there was never a second implementation to drift out of sync with, so there is nothing to
pin two ways. `user_fsrs_params.fsrs_version` still matters (invariant §2.3) — it's what makes a
historical `review_log` replay-able after `go-fsrs` upgrades — just not for this reason.

**Don't trust an FSRS library's own parameter validation; check explicitly before calling it.**
The TypeScript prototype's scheduler (`ts-fsrs`) coerced rather than rejected a bad weight —
clamping a `NaN` to `0` instead of refusing it, silently substituting a default for a falsy
`request_retention` — which is exactly the "plausible but wrong, forever" failure invariant §2.3
exists to prevent: a corrupt optimiser fit or a hand-edited `user_fsrs_params` row would schedule
happily and wrongly with nothing raised. Verify `go-fsrs`'s validation behaviour before relying
on it, and if it's similarly permissive, reject explicitly before calling it: wrong parameter
count for the declared `fsrs_version`, any non-finite weight, `desired_retention` outside `(0,
1)`.

**Verify `go-fsrs`'s fuzz behaviour and force it off (or deterministic) before relying on
batch-precompute/grade-time parity.** FSRS's optional "fuzz" randomises an interval slightly; if
`go-fsrs` seeds it from wall-clock time or another non-reproducible source rather than
deterministically from the card, the four-branch preview computed at batch-fetch time and the
same `Repeat()` call re-run at grade time would legitimately disagree — which is precisely the
drift CLAUDE.md §10.2's consistency test exists to catch, and a random source would make that
test flaky rather than meaningful. The historical-replay path (`replayReviews`, above) needs the
same determinism for a different reason: replaying one `review_log` twice must produce the same
`user_card_state`, or `recomputeUserCardState` isn't actually idempotent.

---

## 4. Repo layout

**Scaffolded as of [#49](https://github.com/Jolls/enshu/issues/49).** Package names below are
the ones actually in the repo, not placeholders.

```
enshu/
├─ CLAUDE.md              working rules, invariants, process
├─ README.md              public rationale
├─ enshu.md               personal notes digest
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
│  └─ plans/              implementation plans and decision records, <slug>.md
├─ migrations/            generated SQL (committed, immutable once merged)
├─ cmd/enshu/             main package: wiring, config, server startup
├─ internal/
│  ├─ db/                 sqlc-generated queries + a hand-written pool/connection setup
│  ├─ auth/
│  ├─ apkg/
│  │  ├─ read.go           .apkg/.colpkg -> IR
│  │  ├─ write.go          IR -> .apkg
│  │  ├─ ankischema.go     Anki's SQLite shapes, schema 11 and 18
│  │  └─ media.go          media map + content-addressed blob store
│  ├─ fsrs/                pure scheduling package: wraps go-fsrs, no DB/HTTP/IO
│  ├─ review/              batch-preview construction, grading, replay
│  ├─ render/               note-type template rendering ({{Field}}, cloze, conditionals)
│  └─ http/                 handlers, one file per aggregate: decks, notes, review, access
├─ web/
│  └─ templates/           html/template source
├─ tests/
│  └─ fixtures/apkg/       real exports from multiple Anki versions — see CLAUDE.md §10
└─ scripts/
```

**Boundary rules**

- **Unit tests are colocated with source** (`foo.go` + `foo_test.go` in the same package
  directory), which is Go's own convention, not a repo-specific choice. `tests/` holds only
  what genuinely lives outside any one package: apkg fixtures and (once the reviewer exists)
  browser-driven e2e specs.
- `internal/fsrs/` is pure: no DB, no HTTP, no I/O. This is what lets it be called from every
  server-side context that needs scheduling — live grading, batch-preview precompute, and
  history replay — without any of them able to disagree with each other (CLAUDE.md §17).
- **There is no client/server import boundary to enforce.** The reviewer's client-side code is
  a small vanilla-JS island, a different language entirely from the Go server — the language
  barrier itself makes "a client module imports server code" impossible, unlike the old
  SvelteKit setup where that boundary had to be enforced by the framework.
- `internal/apkg/` produces and consumes an **intermediate representation**, never
  `sqlc`-generated rows directly. Import is `apkg -> IR -> db`, export is `db -> IR -> apkg`.
  The IR is where format quirks are normalised, and it is what unit tests assert against.
- HTTP handlers stay thin: parse, authorise, delegate to `internal/db` queries, respond. The
  full planned route surface is [docs/routes.md](routes.md) — update it alongside any handler
  that adds, renames, or removes a route.

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

**There is no client-side FSRS implementation.** The server precomputes all four rating outcomes
for every card in a batch at fetch time and ships them down as data; the client's entire job is
to look up whichever branch matches the pressed rating and advance instantly. This makes the
interface just as instant as a client-side scheduler would, without there being a second
implementation anywhere to keep in agreement with the first — see §3 for why that stopped being
necessary.

Anki's short-term "learning steps" (e.g. show again in 10 minutes) are a separate, small state
machine, not FSRS — even in real Anki. Deciding whether a card resurfaces later in *this*
session needs only the card's already-known state plus the step config, so the client runs a
lightweight local heuristic for that. It is cosmetic, not authoritative: worst case it's wrong
by one card's position in a session the user is still sitting in, and nothing about it is ever
written to `review_log` or `user_card_state`.

### Fetching cards

**Never fetch per card.** Beyond that, the policy below is the whole of it — the numbers are
starting values, but the *shape* is not arbitrary and the reasoning is recorded so anyone
retuning them can tell which way is safe.

**Session start: the first batch rides along with the page load.** The reviewer's page handler
renders the first batch server-side and includes it in the document response. There is no
separate request for the first card, and therefore nothing to make faster by fetching card 1
on its own — a card already in the HTML beats any round trip you could shorten. The payload is
rendered, sanitised card content, each card's `user_card_state`, **the precomputed outcome under
all four ratings** (`go-fsrs`'s `Repeat()`, one call per card), the user's FSRS params, and the
study-day end.

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

**Client-side shape: hidden cards, htmx for the wire.** Every card in a batch — the initial one
inline in the page response, every refill — renders as a hidden HTML node (e.g.
`<article hidden data-card-id>`), each carrying its four precomputed rating branches, not a
client-side template driven by JSON. A small local JS module owns the in-session queue on top
of that: which card is current, the local learning-steps requeue decision, unseen-count
tracking, and toggling `hidden`/revealing the answer. It never touches the network itself.
Instead it dispatches plain DOM events (`refill-needed`, `card-graded`, `flush-events`) at the
right moments, and htmx attributes listen for those: a hidden element with
`hx-trigger="refill-needed from:body" hx-get="/api/reviews/next" hx-swap="beforeend"` appends the
next batch's hidden cards, and a second hidden element with
`hx-trigger="flush-events from:body" hx-post="/api/reviews/batch" hx-swap="none"` sends whatever
graded events the queue module has accumulated. Rating buttons are plain — no per-button
`hx-post` — because sending is batched (§11's "kind to mobile radios") and retried with backoff,
which a literal one-request-per-button-press design cannot do; the module owns the flush cadence
and backoff, htmx just listens for the event ([#56](https://github.com/Jolls/enshu/issues/56)).
htmx owns the two network-touching elements; the queue module owns everything that doesn't touch
the wire, and nothing about it waits on a request completing before the UI advances
(CLAUDE.md §2.6).

**Grading, synchronously and locally:**

1. Look up the precomputed branch for the pressed rating — no computation, it was already
   done server-side at batch-fetch time.
2. Apply it to the in-memory queue; the local learning-steps heuristic above decides whether
   and where the card resurfaces later this session.
3. Advance the UI immediately.
4. Hand a `ReviewEvent` — `{id (client-generated UUIDv7), cardId, rating, reviewedAt,
   durationMs}` — to the sender.
5. Return. No `await` on the network anywhere in this path.

**Sending.** Events go out as they are produced, batched only to be kind to mobile radios —
never to build a durable offline log (§11 rules out offline study). Retry transient failures
with backoff; the endpoint is idempotent, so a retry after an ambiguous failure is always
safe. Losing a handful of unsent events to a hard crash is acceptable; **storing a wrong one
is not** — missing rows weaken an optimiser fit, wrong rows corrupt it (CLAUDE.md §2.5).

**Server contract — recompute and store.** Idempotent: the same batch twice is a no-op.

```
POST /api/reviews/batch
  { events: [{ id, cardId, rating, reviewedAt, durationMs }] }   <- exactly these fields;
                                                                     nothing else is read

for each event:
  authorise (user_id, card_id) via deck_access
  before   := SELECT * FROM user_card_state WHERE user_id=$u AND card_id=$c
  outcomes := go-fsrs.Repeat(before, reviewedAt)   <- the same call the batch preview used
  after    := outcomes[rating]                     <- the authority

  INSERT INTO review_log (...) VALUES (<before>, <after>) ON CONFLICT (id) DO NOTHING
  UPDATE user_card_state SET <after>
    WHERE user_id=$u AND card_id=$c
      AND (last_review IS NULL OR last_review < $reviewedAt)

  -> respond with <after> so the client can reconcile its queue
```

`rating`, `reviewedAt`, `durationMs` and `cardId` are the only fields the client is entitled
to assert, and the only fields the server reads. Everything written to `review_log` and
`user_card_state` is derived server-side from state the server already holds — there is no
`predicted` field on the wire to trust or distrust, because the server is the only place `Repeat`
ever runs.

Being the only fields read doesn't make them trusted as sent. Parsing them strictly (reject a
malformed event rather than coercing it) is necessary but not sufficient — `reviewedAt` in
particular is a believability check against the server's own clock, not a shape check: a client
clock can be wrong, and a review timestamped in the future would sort ahead of everything else in
a replay and corrupt `review_log`'s ordering guarantee for that card. Clamp or reject it; don't
take it on faith just because it parsed.

The `last_review <` guard makes application last-write-wins by *review time*, not arrival
time — the correctness property that makes a retrying sender safe.

**Ordering and concurrency, now that the server threads state forward itself.** Scheduling
server-side turns each grade into a read-modify-write, and four mechanisms carry what the
client's `stateAfter` used to:

- **An advisory lock per `(user, card)`, held to commit.** Two batches for the same card — two
  tabs, a redelivered POST — would otherwise both read the same `before` under READ COMMITTED
  and one review would vanish. Advisory rather than `SELECT … FOR UPDATE`, because a card the
  user has never seen has no row to lock, and two concurrent first grades are exactly that case.
  **Acquire the locks for a batch in a fixed sorted order** (sort the `(user, card)` keys before
  taking any of them), not in whatever order the batch happens to list its cards — two batches
  that share more than one card, locking in different orders, is a textbook deadlock, and it is
  reachable in practice (two tabs open on the same deck, both about to send a batch that
  overlaps).
- **Sort the batch by `reviewed_at` before scheduling.** Two grades of the same card in one
  batch have to be applied in the order they happened, so a shuffled batch still converges.
- **Skip an event whose id is already in `review_log`.** A pure retry must not be rescheduled
  from the row it already advanced — recomputing `Repeat()` against the *new* `before` would
  silently apply the same rating twice. The lookup is *not* scoped to `user_id`:
  `review_log.id` is a global primary key, so an id taken by anyone is taken here, or
  `ON CONFLICT` would drop the row while its state write landed.
- **Replay the card from `review_log` when a batch predates the stored `last_review`.** The
  server cannot derive a truthful `*_before` for a review it is seeing out of order, and
  fabricating one writes permanently wrong training data (§2.5) that no recompute repairs —
  `replayReviews` only rebuilds `user_card_state`. Rebuilding the card's history instead makes
  every arrival order converge. The client's sender is FIFO, so this is a fault path, not a
  normal one; concurrent tabs on one deck are how it is actually reached.

The one thing that cannot be walked back: a row written *before* the gap became visible keeps
the `*_before` the server genuinely held at that moment, because `review_log` is append-only.
Its `rating` and `reviewed_at` are still exact, which is what a replay and an optimiser fit
need.

**There is no divergence to catch.** The old design had the client submit a `predicted` block
and the server compare it against its own answer, logging a mismatch. That machinery doesn't
exist here: the client never computes an FSRS result, so it never asserts one, so there is
nothing for the server to compare against — a stale tab or a version skew simply gets today's
correct answer from the server, the same as a fresh one would. What replaces it as a correctness
check is CLAUDE.md §10.2: the batch-time precompute and the grade-time recompute must agree,
verified by test, because both call `go-fsrs.Repeat` and a drift between them would mean two
implementations exist after all.

**The server-side recompute path is load-bearing.** `replayReviews` replays `review_log`
through `go-fsrs` to rebuild `user_card_state`. It is what the live path above calls one event
at a time, what batch-fetch calls to build the four-branch preview, and what import backfill,
parameter refits, and post-incident repair call in bulk. Never delete it as "unused."

---

## 7. `.apkg` / `.colpkg` mapping

Three documents cover the Anki side, and it is worth knowing which one answers your question
before opening any of them:

| Question | Document |
|---|---|
| *Where does this value live?* — table and column shapes | [anki-schema.md](anki-schema.md), with [anki-schema-diagram.md](anki-schema-diagram.md) as its visual companion |
| *How do I interpret it?* — container layout, which schema ships in which export, and the encoding traps | [apkg-format.md](apkg-format.md) |
| *Where does it go in our schema?* | [schema.md](schema.md) |

Read `apkg-format.md` before touching the `.apkg` reader/writer package (`internal/apkg/` — §4).

The parts you need without opening any of them:

- `.apkg` is a zip: a SQLite collection, a `media` JSON index-to-filename map, and media
  files named by index. Two collection schemas must both be readable — 11 (note types and
  decks as JSON blobs in `col`) and 18+ (real tables).
- Everything goes through an **IR**: `apkg -> IR -> db`, `db -> IR -> apkg`. Never
  `sqlc`-generated rows directly.
- Import is idempotent on `(owner_id, guid)`; `revlog` becomes `review_log`, which is the
  difference between a cold start and a warm one.
- Two traps that silently produce plausible-looking wrong data: **`cards.due` is
  days-since-`col.crt` for review cards but a position integer for new ones**, and
  **`cards.ivl` is days when positive, seconds when negative**.
- That doc's contents are unverified against a real Anki build. Treat it as a map, not a
  contract, and correct it in place as fixtures are inspected.

---

## 8. Note-type rendering

**Built** ([#55](https://github.com/Jolls/enshu/issues/55)) — `internal/render/`; full design in
[docs/plans/55-note-type-rendering-sanitisation.md](plans/55-note-type-rendering-sanitisation.md).

Anki templates are their own small language, and this is more work than it looks:
`{{Field}}`, `{{#Field}}…{{/Field}}` and `{{^Field}}` conditionals, `{{FrontSide}}`,
filters (`{{text:Field}}`, `{{furigana:Field}}`, `{{type:Field}}`), and cloze deletion
(`{{c1::hidden::hint}}`) where one note generates N cards by cloze ordinal.

Keep it in `internal/render/` (§4), a pure `(template, fields) -> html` package with a
golden-file test per construct — rendering only ever happens server-side, so there's no
isomorphism requirement to satisfy.

**Template tags don't nest braces** — a cloze marker lives inside field *content*, not template
text — so a single non-nested tokenising pass over `{{...}}` is enough; sections
(`{{#Field}}`/`{{^Field}}`/`{{/Field}}`) are the one construct that needs a stack, matched by
field name on close. Cloze rendering has a rule easy to get half-right: the active cloze number
(the card's ordinal) blanks to `[...]` or `[hint]` on the front and reveals highlighted on the
back, but *every other* cloze number in the field reveals as plain text on **both** sides —
dropping the "other numbers" case makes a multi-cloze note's non-active clozes vanish instead of
showing as context.

**Sanitise on render** — note content is user-authored HTML and shared decks mean it is *other
users'* HTML. Card content is untrusted input in the multiuser model, unlike in Anki where it is
always your own. The allowlist design that mattered in the TypeScript prototype (`bluemonday` or
equivalent is the Go analogue of `sanitize-html`) is worth carrying forward as a checklist, since
each item closes a specific attack, not a generic one:

- **No element with a non-HTML parsing mode** — no `<svg>`/`<math>` (foreign content), no
  `<style>`/`<script>`/`<template>`/`<textarea>`/`<title>` (raw-text or escapable-raw-text
  elements). A tokenising sanitiser and a browser's HTML parser disagree about *those* elements
  specifically, and that disagreement is what mutation XSS exploits — leave them all out and
  there's no context left where the browser re-parses sanitised text as markup.
- **Scheme allowlist on URL-bearing attributes** (`http`/`https`/`mailto`) that excludes
  `javascript:`, `data:`, `vbscript:`, and `file:` by omission — an allowlist that names what's
  in, not a denylist that has to keep naming what's out.
- **A CSS value grammar that admits no bare `(`** outside the four colour functions
  (`rgb`/`rgba`/`hsl`/`hsla`), so `url(...)`, `expression(...)`, and `image-set(...)` stay out
  even if a URL-accepting property is ever added to the allowed-properties list by mistake later.
- **`{{type:Field}}`'s answer-input widget is not sanitisable card content.** It renders an
  `<input>` carrying the expected answer for the reviewer to grade against — an interactive
  element with no legitimate place in an allowlist built for static card markup. Treat it as a
  separate insertion the reviewer performs after sanitisation, not as HTML that flows through
  `sanitiseCardHtml`; conflating the two either lets an `<input>` sneak into the allowlist (attack
  surface) or the sanitiser silently strips the answer widget (feature that quietly stops
  working, the more likely outcome if this gets missed).

---

## 11. Build order

**Phase 1 — single-user core.** A complete product for one user; ship before anything else.

1. Scaffold: Go, Postgres, `sqlc`, CI running `go vet`/lint + unit tests.
2. Schema §5 in full — *including* `deck_access` and the `user_id` in `user_card_state`,
   even though Phase 1 has one user per deck. The columns are free now and structural later.
3. Auth + accounts.
4. `internal/fsrs/` (`go-fsrs` wrapper) + batch-preview/grade-time consistency tests
   (CLAUDE.md §10.2).
5. Deck / note-type / note / card CRUD.
6. Template rendering (§8).
7. **The reviewer (§6)** — the piece everything else is downstream of.
8. `.apkg` import, then export.
9. Desired-retention setting — works against FSRS's default parameters, no fitting required.
10. Per-user parameter optimisation — **deferred out of MVP** (§12). `review_log` accumulates
    from day one regardless (invariant §2.5), so this is purely additive whenever it's built.

**Phase 2 — multiuser.** `deck_access` permissions enforced; co-authoring a deck while each author
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
- **Parameter optimisation implementation.** Settled for MVP: deferred entirely (§11 step 10)
  — ship FSRS's default parameters. `fsrs-rs` (Rust, built on the `burn` tensor/autodiff
  framework, what production Anki's own FSRS integration is built on) is the only mature
  optimiser implementation in the ecosystem; the Python reference (`fsrs-optimizer`/`py-fsrs`)
  is the only other one — despite the *scheduler* having ten-plus independent ports, nobody has
  finished porting the optimiser. Not a Phase 1 blocker, but still open: which path to take when
  it's time. `fsrs-rs` invoked as a subprocess/CLI call from Go is proven and available today;
  `go-fsrs`'s own PR #34 (pure-Go v6 optimizer, unmerged as of 2026-08) would need zero FFI at
  all if it matures — worth tracking, and possibly worth contributing to, since Enshu has a
  direct stake in it landing. Full evaluation:
  [docs/plans/architecture-reconsidered.md](plans/architecture-reconsidered.md).
- ~~**Auth library.**~~ Settled: hand-rolled sessions. No OAuth/SSO requirement (Phase 1 is
  email/password only, and CLAUDE.md invariant §2.9 rules out any federated-identity-shaped
  sync surface), so a framework's adapter abstraction doesn't earn its weight — full control is
  consistent with this project owning its own schema and `.apkg` codec rather than adopting
  someone else's shape. A `sessions` table keyed by the SHA-256 hash of a random token (the raw
  token lives only in the cookie, so a DB read never discloses a usable session), `argon2id` for
  password hashing (`alexedwards/argon2id`, below), and an explicit `Origin`-header check on
  state-changing requests for CSRF. Kept behind `internal/auth/` (§4) regardless, so it stays
  reversible.

  The superseded TypeScript prototype worked out several mechanics below the "hand-rolled
  sessions" headline that are easy to skip and cheap to get wrong; carry them into the Go
  implementation rather than re-deriving them:

  - **Session cookie name is `__Host-`-prefixed.** That prefix requires `Secure`, `Path=/`, and
    no `Domain` attribute — the browser enforces all three, not just convention — which is what
    stops a sibling subdomain (a future status page, a blog, anything with its own XSS bug) from
    setting a same-named cookie that silently rides along to Enshu.
  - **Sliding expiration, renewed only when it's close to expiring** (e.g. a 30-day lifetime,
    renewed once under 15 days remain), so a `Set-Cookie` header — and the write it costs — only
    goes out on the minority of requests that need one, not every request of an active session.
  - **Login is timing-safe against account enumeration.** Verify a password hash unconditionally,
    even when no account matches the email — against a fixed dummy `argon2id` hash computed once
    at startup — so a wrong email and a wrong password take the same time. Any fast-reject (e.g.
    an oversized input) must be deterministic on the *attacker's own input*, never on whether the
    account exists, or the fast path itself becomes the oracle.
  - **Signup is timing-safe the same way**, plus one more thing to get right: the existence check
    and the password hash both run unconditionally and in parallel, so rejecting a duplicate
    email costs the same wall-clock time as completing a real signup — skipping the hash on a
    known duplicate is the ~tens-of-milliseconds gap that turns into an email-enumeration oracle.
    The existence check is also not atomic with the insert, so a concurrent signup for the same
    address can race past it — the `UNIQUE (lower(email))` index is the actual guarantee, and the
    handler's job is only to turn that constraint violation into a clean 409 instead of a 500.
  - **Login and signup are rate-limited per key** (e.g. per IP or per email) against
    credential-stuffing and signup-flooding — both are otherwise unlimited-rate endpoints with
    nothing else in front of them. An in-memory, per-process limiter is the right amount of
    complexity for Phase 1's single-instance deployment (§3); revisit only if the deployment
    target goes multi-instance.
  - **CSRF and session population are enforced once, centrally**, wrapping every request before
    it reaches a handler — never as a per-handler concern a new route could ship without by
    forgetting to call it. See CLAUDE.md §9.
- ~~**Go argon2id package.**~~ Settled (2026-08-11): `alexedwards/argon2id` — a thin wrapper
  over `golang.org/x/crypto/argon2` (itself the lower-level option, also considered) that
  enforces the argon2id variant and a cryptographically-secure salt, with sensible parameter
  defaults. Less hand-written code around a security-critical primitive than building the same
  wrapper over the raw stdlib-adjacent package would take.
- ~~**`templ` vs. `html/template`.**~~ Settled (2026-08-11): `html/template`. Both auto-escape
  by default, which is the property that matters for §8, and neither affects the reviewer's
  felt latency — that property is entirely a client-side JS/DOM concern (§6), fully decoupled
  from server rendering by design, so the templating engine only ever touches a one-time
  initial-batch render and htmx refill fragments, both negligible next to DB/FSRS-compute and
  network cost either way. The deciding factors were developer-facing, not user-facing: no
  codegen step (`templ generate`) on top of the `sqlc` one already in the workflow, zero extra
  dependency, and lower onboarding friction for outside contributors on an AGPL project —
  against `templ`'s real advantages of compile-time-checked template calls and cleaner
  component-style reuse of a card-rendering fragment across the initial batch and htmx refills
  (§6). That reuse point is a genuine defect-surface argument for `templ` (a markup drift
  between the two render paths is a user-visible bug — refilled cards missing a `data-*`
  attribute the client's queue module reads), but it's closable with a golden-file test
  (CLAUDE.md §10-style) regardless of engine, so it didn't tip the decision.
- ~~**Migration tool.**~~ Settled (2026-08-11): `goose`. `sqlc` generates queries, not
  migrations — something has to apply the committed SQL in `migrations/` (§4). Chosen over
  `golang-migrate` (comparable, but goose's own migration files can carry Go functions
  alongside SQL, which `schema.md`'s data-migration cases may eventually want) and `atlas`
  (its declarative diff-and-plan model fights CLAUDE.md §9's "committed, generated SQL,
  immutable once merged, fix forward" convention more than it serves it — Enshu wants
  hand-authored, reviewable migration files, not an auto-planned diff).
- ~~**Exact Go package layout.**~~ Settled (2026-08-11): §4's tree, as written, is the target —
  `internal/db`, `internal/auth`, `internal/apkg`, `internal/fsrs`, `internal/review`,
  `internal/render`, `internal/http`. Nothing about it was contested; adjust only if the
  scaffold session hits a concrete reason to.
- ~~**Go version, lint config, CI platform.**~~ Settled (2026-08-11): Go 1.26 (current stable as
  of 2026-08, no legacy constraint on a greenfield project), `golangci-lint` v2 with
  `linters.default: standard` as the starting set (add specific linters as a real gap shows up,
  not `all` on day one), GitHub Actions running `go build`/`go vet`/`golangci-lint run`/
  `go test ./...` on push and PR — the natural default given `gh` is already this project's
  tool of choice.
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
  **The decision is settled; the store is not built** — [#60](https://github.com/Jolls/enshu/issues/60)
  is still open, and no Go media code exists yet at all (§1). The superseded TypeScript
  implementation had a media-map reader for import (`src/lib/server/apkg/media.ts`) but no
  blob store; the Go equivalent (`internal/apkg/media.go` — §4) starts from the same gap.

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
| Grading authority | No client/server boundary exists — it is a local app | Server recomputes and decides (§2.7), and is the *only* place FSRS ever runs — there's no client copy to diverge from (§6). Anki never faced this question; multiuser creates it |
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
| Empty cards on cloze-ordinal removal | Kept as "empty cards" until the user runs Tools → Empty Cards | Deleted immediately by the edit that removed the ordinal, cascading that card's `user_card_state` (§54, `internal/db/cards.go`'s `SyncNoteCards`, docs/schema.md's card-regeneration diff rule). No empty-cards concept exists; building one wasn't required by any Phase 1 step |

### Unforced — obsolete, and due for removal

*The specific identifiers below (`IrNote.primaryDeckAnkiId`, `src/lib/server/apkg/ir.ts`) name
the superseded TypeScript importer (§1). The data-model conclusion is language-independent and
is the design the Go importer should follow: file `cards.deck_id` from each card's own home
deck, never flattened to the note's deck.*

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

One consequence, settled in [#51](https://github.com/Jolls/enshu/issues/51): deleting a deck
deletes the cards filed in it, and a note goes only when it has **no cards left anywhere** — so a
note whose cards span decks survives its home deck's deletion and is re-homed to the deck of its
lowest-ordinal surviving card. That is not expressible as a static FK cascade, so `cards.deck_id`
cascades while `notes.deck_id` restricts, and deck deletion runs as an ordered transaction in
`internal/db/deletion.go`. `review_log` keeps every row: its `card_id` is not a foreign key, the
same shape Anki's `revlog.cid` has, which is what lets a studied deck be deletable without any
`DELETE` path over training data (CLAUDE.md §2.5). Full policy: docs/schema.md, Deletion policy;
reasoning: `docs/plans/51-deletion-policy.md`. Filing `cards.deck_id` from `IrCard.deckAnkiId`
remains #33's job — the schema is ready for it.
