# Architecture, reconsidered

> [!note]
> **This is a decision record, not the source of truth.** It was the output of one session's
> ground-up re-evaluation of the stack, done deliberately ignoring the existing codebase and
> docs at the time — the instruction going in was "don't assume we keep what was started,
> evaluate the right choice on its own merits."
>
> Its conclusions have since been reconciled into the canonical docs: **Go server, no
> client-side FSRS (the server precomputes all four rating branches per card at batch-fetch
> time), PostgreSQL retained, per-user parameter optimisation deferred out of MVP.**
> [CLAUDE.md](../../CLAUDE.md) and [architecture.md](../architecture.md) are now current — read
> those for what the system actually is. Read this file for the detailed reasoning and
> evidence below (the go-fsrs ecosystem check, the alternatives considered and rejected) that
> doesn't belong in the day-to-day working docs.
>
> A few sub-choices below were left open on purpose rather than settled here — tracked as open
> questions in [architecture.md §12](../architecture.md#12-open-questions): templ vs.
> `html/template`, the Go argon2 library, the migration tool, and exact package layout.

## Why this exists

The original stack (TypeScript end-to-end, SvelteKit, Postgres, Drizzle) was chosen at scaffold
time partly on the reasoning "FSRS must run in the browser for latency, so the whole stack
should be one language to avoid two scheduler implementations kept in sync by hand." This
session re-derived the stack from the product requirements instead of that starting point, and
found that reasoning doesn't hold up — see below. The replacement conclusion is a different
language for the server entirely, so this is a bigger revision than a dependency swap.

## The requirement that drove the original stack, and why it doesn't hold

Restated from [README.md](../README.md) and [architecture.md §6](architecture.md#6-the-review-loop):
grading must never block on the network — the reviewer shows the next card and advances the
instant a key is pressed, with no `await` in that path. The client's result is a prediction, the
server recomputes independently, and the server's answer is what gets stored.

The original design met the first half of that (no network wait) by running a full client-side
copy of `ts-fsrs`, computing predicted state locally, and comparing it to the server's answer
after the fact (a `predicted` block on the batch payload, a divergence counter). That's a real
solution, but it's solving a bigger problem than the one that exists:

- **A card's outcome under each of the four ratings doesn't depend on anything that happens
  during the session** — it's a pure function of the card's state *as of the batch fetch*. So
  the server can compute all four branches for every card up front, at batch-fetch time, and
  ship them down as plain data. The client never computes anything; it looks up whichever
  branch matches the rating the user pressed.
- **Anki's "learning steps" (short-term re-show-it-in-10-minutes behavior) are not FSRS.**
  They're a separate, small state machine, independent of the stability/difficulty model, even
  in real Anki. Deciding "does this card resurface later in this session" needs the card's
  already-known current state plus the step config — not the scheduler.
- That local resurface decision is also **cosmetic, not authoritative** — worst case it's wrong
  by one card position within a session the user is still sitting in. Nothing about it is ever
  written to `review_log` or `user_card_state`, so it carries none of the correctness weight the
  rest of the system is built to protect.

**Consequence: no FSRS implementation needs to run in the browser, ever.** The client's job
shrinks to: display a pre-rendered card, look up a precomputed preview, apply a lightweight
local (non-authoritative) requeue heuristic, and fire-and-forget `{cardId, rating, reviewedAt,
durationMs}`. This also means the `predicted`-field / divergence-counter design in the current
architecture.md §6 becomes unnecessary — there's only ever one implementation of FSRS, running
server-side, so there's nothing to diverge from. That's a simplification owed to §6 whenever
this gets reconciled.

Once the browser doesn't need a scheduler, **the server language stops being constrained by the
client at all**, and the "one language end-to-end" argument that picked TypeScript no longer
applies. The choice reopens on its own merits.

## What actually differentiates the server-language choice

Performance and concurrency characteristics (GC pauses, tail latency under load, per-process
fault isolation) were seriously considered and then explicitly discarded — this is a
self-hosted tool for a classroom or small team, not a system that will ever be stressed enough
for those properties to be observable. Compile-time / iteration-speed friction was also
explicitly waived as a deciding factor per the user's direction, not because it's not real, but
because it's not the priority here.

What was left, once those were stripped out:

- **Contributor accessibility**, for an AGPL project that wants outside contributors eventually.
  Gating the entire codebase behind a language with a real learning curve (Rust: ownership,
  lifetimes, trait bounds) is a durable cost that's expensive to reverse later (it means
  rewriting the app, not refactoring it) if it turns out to matter.
- **FFI/dependency cleanliness**, for the one piece of this system that has a real
  correctness-critical, non-web-dev-shaped problem: FSRS parameter optimization (gradient-based
  fitting, not a simple formula — see below). Go's FFI story to Rust (`cgo`) is genuinely the
  worst of the options surveyed; a subprocess/CLI boundary avoids it entirely.
- **Ecosystem fit for what the app actually is.** Most of Enshu's UI is boring CRUD (decks,
  note types, class rosters, settings) — forms and tables, not something that needs a SPA
  framework. Go's stdlib `html/template` auto-escapes contextually by default, which lines up
  directly with the existing "sanitise on render" requirement (architecture.md §8) with less to
  get wrong than remembering to do it by hand.

Rust end-to-end and Elixir/Phoenix were both seriously considered and are not wrong choices —
Rust end-to-end has the cleanest possible integration with the scheduler (no FFI boundary at
all, since it'd just be a dependency) and the smallest deployment footprint; Elixir/Phoenix has
a genuinely better fit for Phase 2's live instructor dashboard (Phoenix PubSub gives you
live-updating views essentially for free, where Go or Rust would mean bolting on SSE/WebSockets
by hand) and a strong, idiomatic FFI story to Rust via `rustler`. Go was picked over both
specifically for the combination of contributor accessibility + adequate-to-good fit everywhere
else, not because the alternatives are deficient.

## The FSRS ecosystem, checked directly (2026-08-10)

FSRS has two genuinely different pieces, confirmed against the
[open-spaced-repetition](https://github.com/open-spaced-repetition) org directly rather than
assumed:

- **The scheduler** (forward-step: given state + rating + time, compute the next state) is a
  small, well-defined, published spec — independently ported to *ten-plus* languages already
  (`ts-fsrs`, `go-fsrs`, `rs-fsrs`, C/C++, Java, Clojure, Swift, Elixir, Dart, Ruby). That breadth
  of independent, unrelated ports is itself evidence the formulas are simple and portable enough
  that reimplementation risk is low.
- **The optimizer** (fitting ~19–21 parameters to a user's review history via gradient-based
  numerical optimization) has, as of this check, exactly two working implementations in the
  whole ecosystem: `fsrs-rs` (Rust, uses the `burn` tensor/autodiff framework, and is what
  production Anki's own FSRS integration is built on) and the Python reference (`fsrs-optimizer`
  / `py-fsrs`, PyTorch-based — one binding literally advertises replacing a "2GB pytorch"
  dependency with a "1MB hand-coded gradient" alternative, which tells you how heavy the
  straightforward path is). Despite the same prolific ecosystem porting the scheduler to ten
  languages, **nobody has finished porting the optimizer to anything else** — strong evidence
  it's genuinely hard to get right, not merely unattempted.

**`go-fsrs` specifically** (github.com/open-spaced-repetition/go-fsrs):
- Official org, actively maintained — last push 2026-07-25, 142 stars, not archived.
- Targets **FSRS v6**, current with what Anki ships today.
- Its high-level API already has `Repeat(card, now)` → outcome under all four ratings at once,
  and `Reschedule(card, reviews, opts)` → replay a history and rebuild state. Both map directly
  onto what this design needs: precomputed batch previews, and the `replayReviews` capability
  architecture.md §6 already calls load-bearing.
- **No optimizer exists in the repo tree today** (checked directly — no `optimizer/` directory
  on `main`). But it's a live area: **PR #34**, *"feat: add FSRS v6 parameter optimizer in pure
  Go,"* opened and updated as recently as 2026-07-08, is an active, unmerged attempt at exactly
  this. **Issue #33**, *"Proposal: Add FSRS Optimizer Support via Rust Bindings,"* shows the
  reference org itself considered binding to `fsrs-rs` from Go as a legitimate alternative path
  — the same conclusion this document reaches independently.

No `.apkg`-compatibility argument favors the Rust optimizer over an independent one, or vice
versa: FSRS is a published, versioned spec, and a `.apkg` file just stores plain numbers (a
parameter array tagged with a version, per-card floats) computed by *whatever* correctly
implements that spec. The compatibility bar is "does the version match," true for any
implementation.

## Decision: no Rust in the MVP at all

Per-user parameter optimization is deferred out of MVP scope entirely — ship with FSRS's
default parameters, matching the cold-start design already on record ("require ~1,000 reviews
before switching a user onto their own optimised parameters" — most early users are on defaults
for a long time regardless). This is a low-risk deferral specifically because `review_log` was
already designed as append-only training data from day one (architecture.md invariant, §2.5) —
the data keeps accumulating whether or not an optimizer exists yet, and adding one later is
purely additive, no rearchitecture required.

With optimization deferred, **there is no remaining reason to touch Rust in the MVP.** The
scheduler is `go-fsrs`, in-process, zero FFI.

## MVP stack

| Layer | Choice | Notes |
|---|---|---|
| Server + scheduler | Go, `go-fsrs` in-process | Default FSRS params only; no per-user fitting yet |
| HTML rendering | `html/template` or `templ`, server-rendered | Auto-escaping by default; small vanilla-JS island for the reviewer only |
| Database | PostgreSQL | Multiuser row-level tenancy, advisory locks for the concurrent-grade race, JSONB for versioned FSRS params — a data-shape argument, not a scale one; holds at 5 users or 5,000 |
| Typed SQL | `sqlc` | Real SQL, compile-time-checked against the schema |
| `.apkg` I/O | `modernc.org/sqlite` (pure Go, no cgo), stdlib `archive/zip`, `klauspost/compress/zstd` | No native-binary-per-platform concern; also irrelevant either way since deployment is prebuilt Docker images |
| Auth | Hand-rolled sessions | No SSO/OAuth requirement — confirmed, self-hosters are individuals/small teams, not districts with IT-mandated SSO |
| Deploy | Single Go binary + Postgres, Docker / StartOS | |

The reviewer's client-side JS is intentionally thin: hold the pre-fetched batch, display each
card's precomputed `Repeat()`-style preview, apply the local non-authoritative requeue
heuristic, and fire-and-forget the grade POST. No scheduler, no framework required for this to
work.

## Explicitly deferred

**Per-user FSRS parameter optimization.** Not designed away — scoped out of MVP. Two live paths
when it's time, neither requiring rework of what's built now, since the boundary (a "fit
params" call) is a single, isolated function regardless of which is chosen:

- `fsrs-rs`, invoked as a subprocess/CLI call from Go — proven, production-grade, available
  today.
- `go-fsrs` PR #34 (pure-Go v6 optimizer) — if it matures, it slots in with zero FFI at all.
  Worth actively tracking, and potentially worth contributing effort to directly, since Enshu
  has a direct stake in it landing.

**Everything else already marked as out of scope in the original README/architecture.md**
(sync protocol, full offline study, deck forking, a public deck directory, native mobile,
plugins, LLM-generated cards) — no re-evaluation of those happened in this session; nothing
here contradicts them.

## Reconciliation checklist for the future session

Not done now. When this gets reconciled with the rest of the docs, at minimum:

- **README.md** — the "Architecture / Stack" table and "Why TypeScript end to end" section
  are both superseded by the reasoning above.
- **CLAUDE.md §3 (Stack table)** — full rewrite: language, framework, ORM, deck-I/O deps, test
  tooling all change. `noUncheckedIndexedAccess`/"No `any`" TS-specific conventions in §9 need a
  Go-shaped equivalent (or removal). §16's Windows/PowerShell tooling notes may need Go-specific
  additions (build tags, `go generate` for `sqlc`/`templ`, cross-compilation for multi-arch
  Docker images).
- **architecture.md §3 (Stack)** — full rewrite. §4 (Repo layout) — the `src/lib/server/` /
  `src/lib/fsrs/` boundary enforced by SvelteKit's client/server import rules doesn't exist in
  Go the same way; needs a Go-idiomatic equivalent (package boundaries, not framework-enforced
  ones). §6 (Review loop) — the `predicted`/divergence-counter design should be simplified per
  the "no client-side FSRS at all" finding above. §20 (Deviations from Anki) — may gain a row
  for "grading authority" now that there's genuinely nothing to diverge from server truth.
- **Testing (CLAUDE.md §10)** — item 2, "FSRS wrapper parity... through the client path and the
  server path," is moot if there is no client path anymore. Needs rewording or removal.
- **Decide what, if anything, of the existing TypeScript/SvelteKit code survives** the switch,
  or whether this is a re-scaffold from zero given the project is "days old" with no users
  (architecture.md §1).
