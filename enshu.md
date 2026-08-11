---
tags: [project, software, spaced-repetition, education]
---

# Enshu

Multiuser, web-based, Anki-compatible spaced repetition app for classrooms and teams.

## Status

Pre-alpha, restarting on a new stack. An earlier TypeScript/SvelteKit implementation reached
most of Phase 1 before a ground-up stack re-evaluation moved the server to Go and dropped
client-side FSRS entirely — see `docs/plans/architecture-reconsidered.md` in the repo. No Go
code written yet.

## The premise

Anki is architecturally single-user: a collection is one SQLite file where each card row
carries both the content pointer and the scheduling state. There is no seam between "this
deck" and "my progress on this deck." That's why teacher/student assignment, co-authored
decks, and shared decks with private progress don't exist in the Anki ecosystem.

AnkiWeb is multiuser only in the sense of hosting many separate collections. Enshu targets
the other meaning — users actually sharing things.

## Decisions made

| Decision | Choice |
|---|---|
| Name | **Enshu** (演習, "seminar / practice drill") — see [[#Naming]] |
| Stack | Go + PostgreSQL (`sqlc`), server-rendered HTML — see [[#Stack reconsidered]] |
| Scheduling | FSRS via `go-fsrs`, server-side only, no client implementation — see [[fsrs-spaced-repetition]] |
| Anki compatibility | `.apkg` / `.colpkg` import/export only |
| Sync protocol | **Dropped.** See below |
| Licence | Undecided but unconstrained — see [[open-source-licensing]] |

### The schema decision

The load-bearing one. Do **not** adopt Anki's schema. Split content from per-user scheduling
state, with `user_card_state` keyed on `(user_id, card_id)` rather than scheduling living on
the `cards` row. Shared decks, co-authoring with separate histories, and classroom cohorts all
fall out of that single choice. Adopting Anki's schema now and bolting multiuser on later is a
rewrite.

### Stack, reconsidered

Originally SvelteKit + TypeScript end to end, on the reasoning "FSRS must run in the browser
for latency, so pick one language for client and server too." That reasoning doesn't hold: a
card's outcome under each of the four ratings is a pure function of its state as of the batch
fetch, so the server can precompute all four up front and hand them to the client as data — no
FSRS implementation needs to run in the browser at all. Once the client doesn't need a
scheduler, the server language reopens on its own merits: Go won on contributor accessibility
(vs. Rust's learning curve), a clean subprocess boundary to `fsrs-rs` for the optimiser later
(vs. Go's `cgo` story), and a good fit for an app that's mostly server-rendered CRUD. Full
writeup and the alternatives considered: `docs/plans/architecture-reconsidered.md`.

### Why sync was dropped

Sync exists to reconcile independent offline copies across devices. Enshu is a server — you
are already connected to it in order to use it. The problem sync solves doesn't exist here.

Implementing it would have meant reverse-engineering an undocumented protocol, forking a
large Rust codebase and tracking it forever, inheriting AGPL across the project, and building
a projection layer to fake an Anki-shaped collection — all to deliver a *degraded* experience,
since classroom assignment and shared decks can't traverse that protocol anyway.

Cutting it removed Rust from the stack entirely and removed all AGPL exposure. The cost is
offline mobile study via AnkiDroid.

### Offline vs. network-independent

Deliberately separated, because they get conflated and we want exactly one of them.

**Network-independent *grading* is Phase 1** — not for offline's sake but for latency.
Reviewing is a tight show/grade/next loop, and a UI that blocks on a round trip is unusable on
any imperfect connection. So the server precomputes every rating's outcome per card up front,
and the client looks one up and advances the instant you press a key — no scheduler runs
client-side. Painful to retrofit, because it dictates the whole client data flow.

**But the client's grade is an assertion, not a decision.** Two rules, and they don't fight:
the client never waits, and the client is never believed. It may assert which card, which
rating, and when; the server recomputes everything downstream of that and stores its own
result. Nothing computed client-side is ever submitted, so there's nothing to compare or
diverge from — one FSRS implementation, server-side, full stop.

That second rule is what makes the classroom layer mean anything — an instructor's view of who
is struggling is a report on stored scheduling state, and if a browser could write that state
the report would be self-assessment with extra steps. It also can't be added later: state
written on a client's word is unverifiable forever after.

**Full offline study is not planned** — deck and media pre-caching, IndexedDB, multi-device
conflict resolution is most of a sync implementation in a different hat, which is the cost
already declined. Local grading is a latency property, not a first step toward it. Not a
one-way door either: `review_log` is the source of truth and the server can already rebuild
state by replaying it.

### Naming

`ANKI` is a registered trademark of Ankitects Pty Ltd and is actively enforced (Anki Pro was
compelled to rebrand to Noji in June 2025). Descriptive use — "imports Anki decks" — is
nominative fair use and fine. A product *branded* `anki-something` is closer to the risky end.
Findability is handled via GitHub description and topics, which are indexed heavily.

Rejected: **Kioku** (taken — a vocabulary app plus Kiokuhub, an edtech firm), **Terakoya**
(taken — Terakoya.AI), **Doki** 同期 (great double meaning of "cohort" and "sync", but the sync
pun died with the feature, and the romanisation reads cutesy), **Fukushu** (homophone with
"revenge"), `ankiweb-multiuser` (appropriates the official service name).

> [!warning]
> The Japanese has not been checked by a native speaker. Worth doing before the name is
> load-bearing.

## Roadmap

- **Phase 1 — single-user core.** Accounts, decks, note types, the reviewer, local grading
  against server-derived state, `.apkg` import/export. Complete product for one user.
- **Phase 2 — multiuser.** Shared decks with per-user permissions, co-authoring with separate
  histories, classroom cohorts and progress reporting.

Explicitly not doing — decisions, not backlog: sync protocol, full offline study, deck forking,
a public deck directory, native mobile apps, a plugin system, and LLM-generated cards as a
feature that calls a model API (a paste-in text format is fine).

Note that export-then-reimport is *not* a substitute for forking: reimport mints new cards and
progress is keyed to the old ones, so it doesn't survive the trip.

## Open questions

- Licence: AGPLv3 vs MIT/Apache-2.0. AGPL suits the ecosystem and blocks closed hosted forks,
  but institutional AGPL bans could hurt school adoption. Settle before outside contributions.
- Native-speaker check on "Enshu".
- ~~Deck-content licensing~~ — closed with the public directory. Enshu never redistributes deck
  content, so there's no catalogue to attach per-deck licence metadata to. Reopen if any
  deck-sharing surface ever crosses instances.

## Links

- [[fsrs-spaced-repetition]] — how the scheduling algorithm works, and what it demands of the schema
- [[open-source-licensing]] — AGPL network copyleft, and trademark descriptive vs brand use
- [ankitects/anki](https://github.com/ankitects/anki) — upstream
- [go-fsrs](https://github.com/open-spaced-repetition/go-fsrs) — scheduler implementation
