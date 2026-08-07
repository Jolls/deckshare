# Enshu

> Multiuser, web-based, Anki-compatible spaced repetition — built for classrooms and teams.

**Status:** pre-alpha. Design doc only; no code yet.

Enshu is a self-hostable spaced repetition server and web reviewer. It imports and exports
Anki deck files, uses the same FSRS scheduling algorithm, and — unlike Anki — is multiuser
from the ground up: many independent accounts, shared decks with per-user progress, and a
teacher/student layer on top.

The name is 演習 *enshū*, Japanese for a seminar or practice drill — the small-group class
where you work through exercises together. It's a sibling of 暗記 *anki* ("memorisation"),
built on 習 *shū*, "to learn through repeated practice."

> [!note]
> Enshu is an independent project. It is not affiliated with, endorsed by, or derived from
> Ankitects Pty Ltd. "Anki" is a registered trademark of Ankitects Pty Ltd and is used here
> only to describe file-format compatibility.

---

## Why this exists

Anki is excellent and its scheduling algorithm is the best available. But it is
*architecturally single-user*. A collection is one SQLite file where each card row carries
both the content pointer and the scheduling state. There is no seam between "this deck" and
"my progress on this deck."

That single design fact is why the following don't exist in the Anki ecosystem:

- A teacher assigning a deck to thirty students and seeing who is falling behind
- Two people co-authoring a deck while each keeps a private review history
- A shared deck that receives corrections upstream without blowing away your scheduling

AnkiWeb is web-based and multiuser in the sense of *hosting many separate collections*. It is
not multiuser in the sense of *users sharing anything*. Enshu targets the second meaning.

### Non-goals

- **Replacing Anki.** Anki desktop remains better for power users and add-ons.
- **Beating FSRS.** We use FSRS as-is. Scheduling research is not our contribution.
- **A better single-user desktop app.** That exists.
- **Sync-protocol compatibility.** See below — this is a deliberate, load-bearing decision.

---

## Compatibility model

**Enshu speaks Anki's file format, not Anki's sync protocol.**

You import `.apkg` and `.colpkg` files, including scheduling state, so an existing collection
arrives intact with review history preserved. You can export back out at any time, so nobody
is locked in. That's the whole compatibility surface.

`.apkg` is a zip containing a SQLite collection plus media files. File formats are not
copyrightable, so this involves no Anki code and no licensing entanglement — we write our own
reader/writer.

### Why not implement sync

Sync exists to reconcile independent offline copies of a collection across devices. Enshu is
a server. You are already connected to it in order to use it, and the browser holds the only
client-side state there is. **The problem sync solves does not exist here.** Attempting it
would mean:

- Reverse-engineering an undocumented protocol with edge cases discovered only in production
- Forking a large Rust codebase and tracking its changes forever
- Inheriting AGPL obligations across the project
- Building a per-user projection layer to fake an Anki-shaped collection out of our schema
- ...to deliver a *degraded* experience, since none of Enshu's actual features — classroom
  assignment, shared decks, progress reporting — can traverse that protocol anyway

The migration case that sync would have served is covered by one-way import. Someone brings
their collection in once and is done.

**What this costs us:** offline study on a phone via AnkiDroid. Devices are connected most of
the time, so full offline study is a nice-to-have, not a launch blocker — see below.

### Offline vs. network-independent

These get conflated. They have very different costs.

**Network-independent review is a Phase 1 requirement.** Not because of offline, but because
of latency. Reviewing is a tight repetitive loop — show, grade, next — and a UI that blocks
on a round trip is unusable on any connection worse than perfect. So:

- FSRS runs client-side (`ts-fsrs`), computing the next interval locally
- Grading updates the UI optimistically and enqueues the write
- The queue drains in the background, batched, with retry

That is just correct architecture for this kind of app, it's cheap, and it happens to make
brief connection drops invisible. It is also the piece that is **painful to retrofit** — it
determines the shape of the entire client data flow.

**Full offline study is deferred.** Pre-caching whole decks and their media, persisting to
IndexedDB, and resolving multi-device conflicts is where the real cost lives, and it buys
comparatively little. Worth revisiting for the classroom case specifically — students on
transit, patchy school wifi, capped data plans — but not before there are users.

Because scheduling is client-side from day one, the door stays open at near-zero cost.

---

## Scheduling: FSRS

Enshu schedules with **FSRS** (Free Spaced Repetition Scheduler), the open-source algorithm
developed by Jarrett Ye and the [open-spaced-repetition](https://github.com/open-spaced-repetition)
group and integrated into Anki since 23.10. We use it as-is via
[`ts-fsrs`](https://github.com/open-spaced-repetition/ts-fsrs).

FSRS models each card with three variables (the DSR model):

- **Retrievability** — probability you would recall the card right now. Decays over time.
- **Stability** — days until retrievability falls to 90%. Your memory's half-life for that card.
- **Difficulty** — how resistant this card is to gaining stability.

You set a **desired retention** (e.g. 0.9) and FSRS schedules each card for the day its
predicted retrievability crosses that threshold. Retention becomes an explicit dial, and
review workload moves predictably against it.

This replaces SM-2 (1987), which uses a per-card "ease factor" multiplier and hand-tuned
constants with no model of the individual's forgetting curve. FSRS instead *fits* its
parameters to a user's actual review history, typically achieving the same retention for
meaningfully fewer reviews.

Two consequences for this codebase:

- **`review_log` is training data, not an audit trail.** The optimiser fits parameters from
  it. It is per-user, and it cannot be pruned casually.
- **Cold start is a real problem for classrooms.** A new student has no history to fit. Ship
  sensible defaults and require a minimum review count (~1,000) before switching a user onto
  their own optimised parameters. Never fit a single parameter set across a cohort — memory
  behaviour is individual, and a class-wide fit is wrong for every member of it.

---

## Architecture

### Stack

| Layer | Choice | Why |
|---|---|---|
| Web app | SvelteKit + TypeScript | The reviewer is the product. Keyboard-driven, sub-100ms card flips. Leaves a clean path to a full PWA later without committing to one now. |
| Scheduling | [`ts-fsrs`](https://github.com/open-spaced-repetition/ts-fsrs) | Runs client-side so grading never blocks on the network; writes are queued and batched. |
| Database | PostgreSQL + Drizzle | Row-level tenancy, real relational integrity across users/decks/progress. |
| Deck I/O | Own `.apkg` reader/writer (`sql.js` / `better-sqlite3`) | Full control over the mapping into our schema. |

**Why TypeScript end to end.** Scheduling has to run in the browser for latency and offline.
That means an FSRS implementation in JS regardless of backend language. Picking Python or Go
for the server means maintaining two FSRS implementations and keeping them in agreement — a
correctness hazard for the one thing that must not be wrong.

Dropping sync also removes the only justification for Rust anywhere in the stack. The whole
system is now one language, one deployable, one database.

### The schema decision

This is the load-bearing decision of the project. **Do not adopt Anki's schema as the
primary store.** Split content from per-user scheduling state:

```
note_types      id, name, field defs, css
templates       id, note_type_id, qfmt, afmt
notes           id, guid, note_type_id, deck_id, fields[], tags[], created, modified
cards           id, note_id, template_id, ordinal
                -- content only; NO scheduling state on this row

decks           id, owner_id, name, visibility, forked_from_deck_id
deck_access     deck_id, user_id, role (owner|editor|viewer)

user_card_state user_id, card_id, due, stability, difficulty,
                state, reps, lapses, elapsed_days, scheduled_days
                -- PRIMARY KEY (user_id, card_id)
review_log      user_id, card_id, rating, review_time, duration_ms,
                stability_before, difficulty_before
                -- NOT bookkeeping: this is the optimiser's training data
user_fsrs_params user_id, deck_id NULL, fsrs_version, params JSONB,
                desired_retention, optimised_at, review_count_at_fit
```

> [!warning]
> Store FSRS parameters as a **JSON array plus an explicit version column**, never a
> fixed-width field. The parameter count changes between algorithm versions — 17 in FSRS-4.5,
> 19 in FSRS-5, 21 in FSRS-6 — so a fixed column buys a schema migration every time upstream
> ships. The version column also lets old fitted parameters stay readable after an upgrade.

Everything multiuser follows from `user_card_state` being keyed on `(user_id, card_id)`
rather than scheduling living on `cards`. Shared decks, classroom cohorts, and deck forking
that preserves your own progress all fall out of that one choice.

Import maps an Anki collection *into* this shape; export flattens it back out for the
importing user. Both are lossy in one direction only, and both are our code.

### Two things to get right from day one

- **Store Anki's note `guid` on every note.** It is what makes import and re-import
  idempotent. Retrofitting it means every early user's decks duplicate when they re-import.
- **FSRS parameters are per-user, not global.** Optimised parameters are personal. A
  classroom-wide parameter set is wrong for every individual in it. Optionally scope per
  `(user, deck)` — memory behaviour differs by material.

---

## Roadmap

**Phase 1 — Single-user core**
Accounts, deck CRUD, note types and templates, the reviewer, client-side FSRS with a queued
write path, `.apkg` import/export. Ship this before anything else. It is a complete product
for one user.

**Phase 2 — Multiuser**
Shared decks with `deck_access` roles. Classroom layer: instructor assigns a deck to a
cohort, sees per-student retention, due counts, and lapse hotspots. Deck forking that
preserves the fork's `user_card_state`. Public deck directory.

**Deferred, revisit with users**
Full offline study (deck + media pre-caching, IndexedDB, multi-device conflict resolution).
Most relevant to the classroom case; cheap to add later because scheduling is already local.

**Explicitly not doing**
Anki sync protocol. Native mobile apps (the web app is the mobile story). Plugin system.
LLM-generated cards.

---

## Licensing

**Undecided, and — importantly — freely decidable.**

Because Enshu contains no Anki-derived code, we inherit no licence. Anki is AGPLv3-or-later,
and forking its sync server would have made AGPL permanent and irreversible for this project
(hundreds of contributors, no CLA, nobody able to grant an exception). Dropping sync means
that constraint simply doesn't apply. We can pick anything, and we can change our minds later.

Given the project is FOSS regardless, the live options:

- **AGPLv3** — consistent with the ecosystem Enshu sits in, and prevents someone running a
  closed proprietary fork as a hosted service. The trade-off is that some organisations have
  blanket policies against AGPL, which can limit institutional adoption — a real
  consideration for something aimed at schools.
- **MIT / Apache-2.0** — maximum adoption, no friction for institutional users. Apache-2.0
  adds an explicit patent grant.

Worth deciding before the first outside contribution arrives, since relicensing afterwards
requires tracking down every contributor.

Deck content is a separate matter from code. Shared decks on AnkiWeb carry their own licence
terms, and redistributing them without permission is something Ankitects has publicly
objected to. The public deck directory must record and display a licence per deck.

*Not legal advice.*

---

## Naming and trademark

`ANKI` is a **registered trademark of Ankitects Pty Ltd**, and it is actively enforced —
Anki Pro was compelled to rebrand to Noji in June 2025, and Ankitects maintains a public
["Anki knockoffs"](https://faqs.ankiweb.net/anki-knockoffs.html) page naming apps it
considers to be trading on the brand.

The distinction that matters is **descriptive use versus brand use**:

- ✅ *"Enshu imports Anki decks"* — nominative fair use. Standard, low risk.
- ⚠️ *A product branded* `AnkiMultiuser` *with a domain and marketing* — this is what got Anki
  Pro renamed. Being FOSS reduces the commercial-confusion argument but does not eliminate it.
- ❌ Anything containing `AnkiWeb` — the name of the official service. Direct implication of
  affiliation. Avoid entirely.

Community repos using an `anki-` prefix as a plain descriptor (`anki-sync-server-rs`,
`anki-connect`, the `ankicommunity` org) have not been targeted, because they are obviously
tools rather than competing products. Enshu is a competing product, which puts it closer to
the risky end. Hence a distinct name, with compatibility carried in the description and
topics — GitHub indexes both heavily, and the `anki` topic page is where the target audience
browses. Findability does not require the name.

```
Repo:        enshu
Description: Multiuser, web-based spaced repetition for classrooms and teams.
             FSRS scheduling, Anki deck import/export.
Topics:      anki, spaced-repetition, fsrs, flashcards, srs, self-hosted,
             education, classroom, sveltekit
```

### Why Enshu, and what was rejected

演習 *enshū* means a seminar or practice drill — group study plus repetition, which is the
product in one word. It shares the 習 root with 復習 *fukushū* (review), 学習 *gakushū*
(learning), and 自習 *jishū* (self-study), and sits alongside 暗記 *anki* without borrowing
from it.

| Candidate | Outcome |
|---|---|
| **enshu** 演習 | **Chosen.** No software or edtech collision found. |
| kioku 記憶 | **Rejected — taken.** A vocabulary-memorisation app on both app stores, plus Kiokuhub, an edtech company serving "learners, teachers, and institutions." Direct collision. |
| terakoya 寺子屋 | **Rejected — taken.** Terakoya.AI ships an education app. |
| doki 同期 | **Fallback.** Real word meaning both "cohort" and "synchronisation" — though the sync half of the pun is moot now. Rejected on romanisation: "Doki" reads cutesy in English (ドキドキ), "Douki" is worse. |
| fukushu 復習 | Rejected. Homophone with 復讐, "revenge." |
| rinko 輪講 | Rejected. Perfect meaning (a study group taking turns presenting) but obscure even in Japan, and a common given name. |
| juku 塾 | Rejected. Short and memorable, but connotes rote cram-school grinding. |
| `anki-multiuser` | Rejected. Findable, but reads as an Anki add-on rather than a product, and carries the trademark question. |
| `ankiweb-multiuser` | Rejected outright. Appropriates the official service name. |

**Known adjacencies, accepted:**

- [Renshuu](https://renshuu.org) — an established Japanese-learning SRS site. 練習 *renshū*
  ("practice") is one letter and one kanji away from 演習 *enshū*, and it's in the adjacent
  space. Different scope (Japanese-language-specific vs general-purpose), but worth knowing.
- Enshu Limited (TSE 6218) — a Japanese CNC machine-tool manufacturer since 1920. Different
  trademark class entirely; no realistic conflict with software.
- Kobori Enshū (1579–1647) — tea master and garden designer. Harmless, arguably a nice
  association.

> [!warning]
> The Japanese here has not been checked by a native speaker. Connotation is exactly where
> non-native judgement is least reliable — get it sanity-checked before the name is
> load-bearing. Renaming a GitHub repo is cheap (redirects are automatic); renaming a product
> with users, a domain, and inbound links is not.

---

## Prior art

- [ankitects/anki](https://github.com/ankitects/anki) — the real thing. Rust core, Python/TS bindings.
- [AnkiWeb](https://ankiweb.net) — official hosting. Many collections, no sharing between them.
- [open-spaced-repetition](https://github.com/open-spaced-repetition) — FSRS reference implementations.
- [awesome-fsrs](https://github.com/open-spaced-repetition/awesome-fsrs) — implementation index.
- [anki-apkg-parser](https://github.com/74Genesis/anki-apkg-parser),
  [anki-reader](https://github.com/ewei068/anki-reader) — prior art for `.apkg` parsing in Node.

---

## Contributing

Nothing to contribute to yet. Licence will be settled before the first outside contribution.
