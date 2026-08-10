# Enshu

> Multiuser, web-based, Anki-compatible spaced repetition — built for classrooms and teams.

**Status:** pre-alpha, and not yet usable. Phase 1 is largely built — accounts, decks, note
types, template rendering, the reviewer, and `.apkg` reading — but deck import isn't wired end
to end, export doesn't exist, and there are no users. Interfaces and storage may still change
without a migration path.

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

**What this costs us:** offline study on a phone via AnkiDroid. Enshu is a server, and
studying against it needs a connection — see below.

### Offline vs. network-independent

These get conflated. They are different things and we want exactly one of them.

**Network-independent *grading* is a Phase 1 requirement.** Not for offline's sake, but for
latency. Reviewing is a tight repetitive loop — show, grade, next — and a UI that blocks on a
round trip is unusable on any connection worse than perfect. So the client runs `ts-fsrs`
itself, computes the next interval the instant you press a key, and advances. Nothing in that
path waits for the network.

But the client's answer is a *prediction*, not a decision. The grade goes to the server, the
server recomputes it independently, and the server's result is what gets stored:

- **The client never waits** — it schedules locally and moves on.
- **The client is never believed** — it may assert which card, which rating, and when. Every
  number that follows is derived server-side.

That second rule is what makes the classroom layer worth building. An instructor's view of
which students are struggling is a report on stored scheduling state; if a browser could write
that state directly, the report would be self-assessment with extra steps. The server compares
its answer to the client's on every grade, so a stale tab or a version skew surfaces as a
logged divergence instead of quietly wrong intervals.

**Full offline study is not planned.** Pre-caching whole decks and their media, persisting to
IndexedDB, and reconciling multi-device conflicts is most of a sync implementation wearing a
different hat — which is the cost this project already decided not to pay. Local grading is a
latency property, not a first step toward it.

Nothing about that is a one-way door: review history is the source of truth and the server can
already rebuild scheduling state by replaying it. If offline ever earns its keep, it can be
built then, against a foundation that didn't compromise for it.

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
| Scheduling | [`ts-fsrs`](https://github.com/open-spaced-repetition/ts-fsrs) | Runs client-side so grading never blocks on the network, and again server-side, which is where the stored answer comes from. |
| Database | PostgreSQL + Drizzle | Row-level tenancy, real relational integrity across users/decks/progress. |
| Deck I/O | Own `.apkg` reader/writer (`sql.js` / `better-sqlite3`) | Full control over the mapping into our schema. |

**Why TypeScript end to end.** Scheduling runs in the browser for latency, so an FSRS
implementation in JS exists regardless of backend language. It also runs on the server, which
is the copy that decides. Picking Python or Go for the server means two implementations of the
same algorithm kept in agreement by hand — a correctness hazard for the one thing that must
not be wrong. One language, one `ts-fsrs`, one set of semantics.

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

decks           id, owner_id, name
deck_access     deck_id, user_id, role (owner|editor|viewer)
                -- the ONLY way a deck reaches a second user

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
rather than scheduling living on `cards`. Shared decks, co-authoring with separate histories,
and classroom cohorts all fall out of that one choice.

Import maps an Anki collection *into* this shape; export flattens it back out for the
importing user. Both are lossy in one direction only, and both are our code.

### Three things to get right from day one

- **Store Anki's note `guid` on every note.** It is what makes import and re-import
  idempotent. Retrofitting it means every early user's decks duplicate when they re-import.
- **FSRS parameters are per-user, not global.** Optimised parameters are personal. A
  classroom-wide parameter set is wrong for every individual in it. Optionally scope per
  `(user, deck)` — memory behaviour differs by material.
- **The server decides what gets stored.** A client may report which card, which rating, and
  when; scheduling state is derived from that, server-side, every time. This is the one that
  cannot be added later — state written on a client's word stays unverifiable forever, and the
  classroom layer is a report on exactly that state.

---

## Roadmap

**Phase 1 — Single-user core**
Accounts, deck CRUD, note types and templates, the reviewer, local grading against
server-derived state, `.apkg` import/export. Ship this before anything else. It is a complete
product for one user.

**Phase 2 — Multiuser**
Shared decks with `deck_access` roles, so two people can co-author a deck while each keeps a
private review history. Classroom layer: instructor assigns a deck to a cohort, sees
per-student retention, due counts, and lapse hotspots.

**Explicitly not doing**
Each of these is a decision rather than a backlog item:

- **Anki sync protocol** — see above.
- **Full offline study** (deck + media pre-caching, IndexedDB, multi-device conflict
  resolution). Enshu is a server. Local grading is a latency property, not a step toward this.
- **Deck forking** and a **public deck directory**. `deck_access` already covers co-authoring
  and the classroom, and anyone wanting an outside deck can import its `.apkg`. Worth being
  precise: export-then-reimport is *not* forking — reimport creates new cards, and your
  progress is keyed to the old ones, so it doesn't come with you.
- **Native mobile apps** (the web app is the mobile story).
- **Plugin system.**
- **LLM-generated cards**, as a feature that calls a model API on your behalf. A documented
  paste-in text format, filled by whatever model you already use, is fair game — no API key,
  no per-token cost, no third party in your study data.

---

## Licensing

**AGPL-3.0-or-later.**

Because Enshu contains no Anki-derived code, we inherit no licence — Anki is AGPLv3-or-later,
and forking its sync server would have made AGPL permanent and irreversible for this project
(hundreds of contributors, no CLA, nobody able to grant an exception). Dropping sync means
that constraint simply doesn't apply, which is why the choice was ours to make freely rather
than inherited.

We chose AGPLv3-or-later anyway: it's consistent with the ecosystem Enshu sits in, and it
prevents someone running a closed proprietary fork as a hosted service. The trade-off is that
some organisations have blanket policies against AGPL, which can limit institutional
adoption — a real consideration for something aimed at schools, and a cost we accepted
knowingly rather than one we missed.

Deck content is a separate matter from code, and Enshu never redistributes any. Publicly
shared decks carry their own licence terms, and redistributing them without permission is
something Ankitects has publicly objected to — so there is no deck catalogue, no directory,
and no republication. A deck reaches a second person through a `deck_access` row on the
instance it already lives on, or through a file its owner passed along. Should any
deck-sharing surface ever cross instances, per-deck licence metadata becomes a prerequisite,
not an afterthought.

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
