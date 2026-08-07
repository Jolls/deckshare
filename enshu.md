---
tags: [project, software, spaced-repetition, education]
---

# Enshu

Multiuser, web-based, Anki-compatible spaced repetition app for classrooms and teams.

## Status

Design phase — architecture and naming settled, no code written. Repo not yet created.

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
| Stack | SvelteKit + TypeScript + PostgreSQL (Drizzle) |
| Scheduling | FSRS via `ts-fsrs`, client-side — see [[fsrs-spaced-repetition]] |
| Anki compatibility | `.apkg` / `.colpkg` import/export only |
| Sync protocol | **Dropped.** See below |
| Licence | Undecided but unconstrained — see [[open-source-licensing]] |

### The schema decision

The load-bearing one. Do **not** adopt Anki's schema. Split content from per-user scheduling
state, with `user_card_state` keyed on `(user_id, card_id)` rather than scheduling living on
the `cards` row. Shared decks, cohorts, and progress-preserving deck forks all fall out of
that single choice. Adopting Anki's schema now and bolting multiuser on later is a rewrite.

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

Deliberately separated, because they get conflated and have very different costs.

**Network-independent review is Phase 1** — not for offline's sake but for latency. Reviewing
is a tight show/grade/next loop, and a UI that blocks on a round trip is unusable on any
imperfect connection. So FSRS runs client-side, grading updates optimistically, and writes
queue and drain in the background. Cheap, correct anyway, and painful to retrofit because it
dictates the whole client data flow.

**Full offline study is deferred** — deck and media pre-caching, IndexedDB, multi-device
conflict resolution. That's where the real cost is, and devices are connected most of the
time. Worth revisiting for the classroom case (transit, school wifi, capped data), but not
before there are users. Because scheduling is already local, the door stays open cheaply.

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

- **Phase 1 — single-user core.** Accounts, decks, note types, the reviewer, client-side FSRS
  with a queued write path, `.apkg` import/export. Complete product for one user.
- **Phase 2 — multiuser.** Shared decks with roles, classroom cohorts and progress reporting,
  deck forking, public deck directory.

Deferred: full offline study. Explicitly not doing: sync protocol, native mobile apps, plugin
system, LLM-generated cards.

## Open questions

- Licence: AGPLv3 vs MIT/Apache-2.0. AGPL suits the ecosystem and blocks closed hosted forks,
  but institutional AGPL bans could hurt school adoption. Settle before outside contributions.
- Native-speaker check on "Enshu".
- Deck-content licensing for the public directory — AnkiWeb shared decks carry their own terms.

## Links

- [[fsrs-spaced-repetition]] — how the scheduling algorithm works, and what it demands of the schema
- [[open-source-licensing]] — AGPL network copyleft, and trademark descriptive vs brand use
- [ankitects/anki](https://github.com/ankitects/anki) — upstream
- [ts-fsrs](https://github.com/open-spaced-repetition/ts-fsrs) — scheduler implementation
