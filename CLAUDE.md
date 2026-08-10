# CLAUDE.md — Enshu foundation

Working context for agent sessions on this repo. **This file is how to work here** — the rules,
the invariants you must not break, and the process. [docs/architecture.md](docs/architecture.md)
is what the system *is*: state, stack, layout, protocols, roadmap, glossary. Read it when you
need the shape of the thing; read this before you touch it.

[README.md](README.md) is the public rationale ("why these decisions"); [enshu.md](enshu.md) is
the personal-notes digest of it. If a decision here contradicts the README, the README wins on
*rationale* and this file wins on *mechanics*. If you change a decision, update both.

**New to the repo?** Start with [architecture.md §1](docs/architecture.md#1-current-state), then §2 — nothing else will make sense before the
invariants do.

### Section map

Section numbers are a single space split across two files, so a `§N` reference anywhere in the
codebase resolves to exactly one place. Numbers are stable; they do not get renumbered when a
section moves.

| | CLAUDE.md — how to work here | | docs/architecture.md — what this is |
|---|---|---|---|
| **§2** | [Invariants](#2-invariants--do-not-violate-without-an-explicit-decision) | **§1** | [Current state](docs/architecture.md#1-current-state) |
| **§9** | [Conventions](#9-conventions) | **§3** | [Stack](docs/architecture.md#3-stack) |
| **§10** | [Testing](#10-testing) | **§4** | [Repo layout](docs/architecture.md#4-repo-layout) |
| **§13** | [Trademark guardrail](#13-trademark-guardrail) | **§5** | [Data model](docs/architecture.md#5-data-model) |
| **§14** | [Branching, commits, releases](#14-branching-commits-releases) | **§6** | [The review loop](docs/architecture.md#6-the-review-loop) |
| **§15** | [Issue workflow](#15-issue-workflow) | **§7** | [`.apkg` mapping](docs/architecture.md#7-apkg--colpkg-mapping) |
| **§16** | [Environment and tooling](#16-environment-and-tooling) | **§8** | [Note-type rendering](docs/architecture.md#8-note-type-rendering) |
| **§17** | [What NOT to touch](#17-what-not-to-touch) | **§11** | [Build order](docs/architecture.md#11-build-order) |
| **§19** | [Agent memory](#19-agent-memory) | **§12** | [Open questions](docs/architecture.md#12-open-questions) |
| | | **§18** | [Glossary](docs/architecture.md#18-glossary) |
| | | **§20** | [Deviations from Anki](docs/architecture.md#20-deviations-from-anki) |

Deeper reference lives one level further out — **ours** in
[docs/schema.md](docs/schema.md) (full DDL) and
[docs/schema-diagram.md](docs/schema-diagram.md) (ER diagrams); **Anki's** in
[docs/anki-schema.md](docs/anki-schema.md) (their tables and columns),
[docs/anki-schema-diagram.md](docs/anki-schema-diagram.md) (their ER diagrams), and
[docs/apkg-format.md](docs/apkg-format.md) (the container, and the encoding traps that make
reading those columns correctly hard). Rule of thumb: *where a value lives* is the schema docs,
*how to interpret it* is `apkg-format.md`.

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

## 2. Invariants — do not violate without an explicit decision

These are the load-bearing choices. Each one is cheap now and a rewrite later.

1. **Scheduling state never lives on the `cards` row.** It lives in
   `user_card_state`, primary-keyed `(user_id, card_id)`. Every multiuser feature — shared
   decks, co-authoring, classroom cohorts — is a consequence of this one fact.
   Adopting Anki's schema and bolting multiuser on afterwards is a rewrite.
2. **Every note carries Anki's `guid`, unique per owner.** `UNIQUE (owner_id, guid)` is what
   makes import and re-import idempotent. Retrofitting it duplicates every early user's decks
   on their next import. Owner-scoped, not deck-scoped: `guid` is globally unique in Anki, so
   a deck-scoped key would make note identity depend on deck dedup landing first. Decks and
   note types dedup on `UNIQUE (owner_id, name)`, the way Anki's own importer does — never on
   `anki_id`, which is per-collection (deck id 1 is `Default` everywhere) and would silently
   merge unrelated decks. `review_log` is the one exception: `revlog.id` identifies a row
   within its collection, so `UNIQUE (user_id, card_id, anki_id)` is safe and needed.
3. **FSRS parameters are per-user** (optionally per `(user, deck)`), stored as a JSON array
   plus an explicit `fsrs_version` integer — never fixed-width columns. Parameter count
   changes upstream: 17 in FSRS-4.5, 19 in FSRS-5, 21 in FSRS-6.
4. **Never fit one parameter set across a cohort.** Memory behaviour is individual; a
   class-wide fit is wrong for every member of it.
5. **`review_log` is training data, not an audit trail.** Append-only, one row per answer,
   never rolled up into a running total — the same thing Anki's `revlog` is, and for the same
   reason: the optimiser fits against the full history, so a collection years old can still be
   refitted. It is per-user and it cannot be pruned casually. No `DELETE` paths without a
   written decision.
6. **Grading never blocks on the network.** The client computes FSRS locally and advances the
   UI immediately — no `await` between the keypress and the next card. This shapes the entire
   client data flow and is the single most painful thing to retrofit.
7. **The client asserts what the user did; the server derives what follows.** A grade is a
   claim about *which card, which rating, when* — nothing more. The server independently
   recomputes the resulting state and **what the server computes is what gets stored**, in
   both `user_card_state` and `review_log`. The client's own result drives its UI and is
   compared, never trusted. See [architecture.md §6](docs/architecture.md#6-the-review-loop).
   Why this is an invariant and not a preference: trust cannot be retrofitted. State written
   under a client's authority stays unverifiable forever, and Phase 2's instructor dashboard
   is a report on exactly that data. A student who can POST their own `stability` is a
   student whose retention chart means nothing.
8. **No Anki-derived code.** File formats are not copyrightable; the reader/writer is ours.
   Copying Anki source (AGPLv3) would make the licence question permanent and irreversible.
   Read the format spec and other clean-room parsers, never `ankitects/anki` source.
9. **No sync protocol.** Settled and closed. Re-opening it re-imports every cost listed in
   the README.
10. **Follow Anki's model unless multiuser forces a change.** Anki's shapes are twenty years
    of a working product, and we have to read and write its files anyway — so matching it is
    the cheap path *and* the compatible one. Divergence is the thing that needs justifying,
    not conformance.
    The test, applied to any difference: **does it trace to the content/progress seam (§2.1)?**
    Almost every legitimate one does — splitting scheduling off the card row, scoping identity
    per owner, making the server the scheduling authority, sanitising other people's HTML.
    A difference that *doesn't* trace back to multiuser is a wheel being reinvented, and it
    needs its own written reason or it should be changed back. The register of what we do
    differently, and why, is [architecture.md §20](docs/architecture.md#20-deviations-from-anki)
    — add a row when you diverge, and do not diverge silently.

---

## 9. Conventions

- **TypeScript strict**, `noUncheckedIndexedAccess` on. No `any` in `lib/fsrs/` or
  `lib/server/apkg/` — these are the correctness-critical modules.
- **Time is `timestamptz`, always UTC in the DB.** Local-time reasoning happens only at the
  day-boundary calculation (architecture.md §5) and in display formatting.
- **Migrations are generated by `drizzle-kit`, committed, and immutable once merged.** Fix
  forward with a new migration; never edit an applied one.
- **Authorisation is explicit at the query layer.** Every query touching a deck takes a
  `user_id` and joins `deck_access`. Do not rely on route guards alone — a shared deck means
  "readable by some users" is the normal case, not the exception.
- **No cross-user reads without a `deck_access` row. No exceptions.** There is no public-deck
  carve-out and no visibility flag — a deck is reachable by exactly the users with a row, and
  a `deck_access` row never grants read of another user's `user_card_state` regardless of
  role. One authorisation path, so there is only one thing to get right and one thing to test.
- Naming: `snake_case` in SQL, `camelCase` in TS, Drizzle handles the mapping.
- Comment density matches surrounding code. The `apkg` modules earn comments (they encode
  external format facts); route handlers do not.

---

## 10. Testing

Priority order, highest first — this reflects where silent wrongness is most expensive:

1. **The client cannot write scheduling state.** A grade whose `predicted` block claims a
   stability, difficulty, or `due` other than what the server computes must store the
   server's value and raise a divergence — including when `predicted` is hostile rather than
   merely stale. This is invariant §2.7, and it is the test that keeps Phase 2's instructor
   dashboard meaningful. If it ever fails, stop and fix it before anything else.
2. **FSRS wrapper parity.** The same card + rating + timestamp produces byte-identical state
   through the client path and the server path. Property-based over random review sequences.
   Under §2.7 a failure here is a *caught* divergence rather than silent corruption, which is
   why it now sits below the test that does the catching — but a parity break still means
   every user is seeing wrong predictions, so treat it as urgent.
3. **`.apkg` round-trip.** `import(export(import(f))) == import(f)` for fixture files from
   several Anki versions (schema 11 and 18+, with and without FSRS data, with media, with
   cloze, with non-ASCII filenames). **Collect these fixtures early** — they are the hardest
   test asset to produce later and the format is where the unknown-unknowns live.
4. **Send idempotency.** Replaying a batch, reordering it, and interleaving it with a later
   review all converge to the same `user_card_state`.
5. **Access control.** Table-driven: for each (role, resource, operation), assert allow/deny.
   Add a row on every new endpoint.
6. **E2E reviewer** (Playwright): keyboard grading, optimistic advance, events sent, and a
   session survives a transient network failure mid-review.

Fixtures go in `tests/fixtures/apkg/` with a README recording which Anki version produced
each and what it exercises.

---

## 13. Trademark guardrail

`ANKI` is a registered trademark of Ankitects Pty Ltd and is actively enforced. When writing
any user-facing copy, docs, or repo metadata:

The line is **descriptive use versus brand use**, not the presence of the word:

- ✅ Descriptive: "Enshu imports Anki decks." Nominative fair use.
- ⚠️ Never brand anything `Anki<something>`.
- ❌ Never put `AnkiWeb` in anything that *names us* — repo, product, package, domain, GitHub
  topics, headings. It is the official service's name, so wearing it implies affiliation
  outright. `ankiweb-multiuser` was rejected as a project name for exactly this reason.
- ✅ But naming the service in prose is fine, and sometimes required: contrasting it with
  Enshu ("AnkiWeb hosts many separate collections; Enshu targets sharing between them"),
  listing it under prior art, or stating this very rule. That is the same nominative use as
  "imports Anki decks" — and a rule that forbade it would forbid the README's own trademark
  section from naming what it is about.

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
- `ankitects/anki` source — never read it into this codebase (§2.8). Format specs and
  clean-room parsers only.
- The server-side recompute path — it is the live grading path (§2.7), and it is what import
  backfill, client-bug repair, and parameter refits all call in bulk. Never delete it as
  "unused" (architecture.md §6).
- The divergence check and its counter (architecture.md §6). It is the only thing standing between a stale
  client and quietly wrong training data, and it is worthless the moment someone "cleans up"
  the comparison because it never fires. Never firing is the point.
- `lib/fsrs/` must stay pure: no DB, no `fetch`, no browser globals. Both the client's
  prediction and the server's authoritative answer come from it, so it has to run in either
  place — and it must never become the *client's* module with a server copy, or §2.7 quietly
  stops being enforceable.

---

## 19. Agent memory

**Memory for this project lives in `.claude/memory/`, in the repo — not in the global
`~/.claude/projects/…` store.** It is gitignored, so it stays local to this checkout: sessions
on this machine share it, but it is not committed, not reviewed, and not distributed. Anything
that needs to outlive this working copy or reach another contributor belongs in `CLAUDE.md`,
`docs/`, or an issue — not here.

- `.claude/memory/MEMORY.md` is the index: one line per memory, no content of its own.
- One fact per file, kebab-case name, frontmatter carrying `name`, `description`, and
  `metadata.type` (`user` / `feedback` / `project` / `reference`). Cross-link with `[[name]]`.
- Record what the repo doesn't already say. Decisions and their *reasoning* — especially
  things deliberately **not** built and why — belong here. Code structure, past fixes, and
  anything recoverable from git history do not.
- Update the existing file when a fact changes; delete it when it turns out to be wrong.
  A memory reflects what was true when written, so verify anything it names still exists
  before acting on it.
