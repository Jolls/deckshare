# Handoff — Phase 1 build

State as of 2026-08-08. Zero background agents running — safe to compact or start fresh from
here. See `docs/plans/02-phase-1-tracks.md` for the full track plan and `CLAUDE.md` for the
build spec; both are current.

## Merged to `main`

- Schema (#4)
- `lib/fsrs/` isomorphic wrapper (#5)
- `lib/render/` template engine (#12)
- `lib/render/` HTML sanitisation (#6)
- Auth + accounts (#7)
- Deck/note/card CRUD (#8)
- `.apkg` import reader (#9) — schema-11 path verified against a real export; schema-18/
  protobuf field numbers still unverified, tracked in #25

## In flight — needs attention next

**Reviewer + write queue (#13)** — implemented, uncommitted, on branch
`feature/13-reviewer-write-queue` in worktree `.claude/worktrees/agent-a6d7167362bb6ea36`.
271/271 tests passing (svelte-check/lint clean). Not yet reviewed, committed, or PR'd.

Next steps, following the pattern used for every prior track:
1. Run the Opus-high review pass (`area: fsrs` / `area: ux`, cross-cutting write-queue work
   per CLAUDE.md §7's model-selection guidance).
2. Dispatch any fixes back to the implementing agent.
3. Commit, push, open the PR, close #13.

Open questions the implementing agent flagged for the review pass:
- Media URLs deliberately omitted from the session payload — `media_refs` is only populated
  by `.apkg` import (not yet wired to a live deck) and the storage backend is still an open
  question (CLAUDE.md §12). Needs a decision once media actually needs serving.
- Note-type CSS omitted — shipping it raw would reopen what #6's sanitiser closed. Needs a
  scoped-CSS decision.
- `review_log.elapsed_days_before` is derived from `stateAfter` based on an internal
  `ts-fsrs` 5.4.1 invariant (`log.elapsed_days === card.elapsed_days` after `next()`) rather
  than sent separately by the client. Worth a second pair of eyes.
- Requeue rule (a graded card returns to the tail iff its new `due` falls before the
  study-day end) isn't specified in CLAUDE.md §6 — this was the implementer's own call to
  keep client/server agreement on "due today."

## Not started

- **Auth UI (#28)** and **Deck/note-type/note management UI (#29)** — newly filed. Neither
  auth nor CRUD has any browser-facing page, only JSON APIs — there is currently no way for a
  human to sign up, create a deck, or add notes without `curl`/manual SQL, which means the
  reviewer (#13) has nothing to study without hand-inserted rows. Both are thin wrappers over
  already-reviewed API layers (Sonnet-medium, no new logic) and are the actual blocker on a
  human-testable app, ahead of anything else in this list.
- **`.apkg` writer (#10)** — unblocked, reader (#9) is merged. Sonnet-medium once the reader's
  IR is the reference.
- **Schema-18 protobuf field-number verification (#25)** — still open. Needs a self-made
  two-note test deck exported with Anki's "support older Anki versions" turned off, to avoid
  the licensing issue that ruled out the real commercial deck used for the schema-11
  verification.
- **Parameter optimisation (#11)** — last in Phase 1. Resolve CLAUDE.md §12's optimiser
  question first (`fsrs-rs-nodejs` native binding vs. pure-TS vs. out-of-process job) before
  implementing.

## Loose ends

- **CHANGELOG version drift.** Several merged PRs (#5/#6/#7/#8/#12) skipped `CHANGELOG.md`
  entries. `package.json` still says `0.1.0` while the changelog has an `[0.1.1]` section and
  the #13 branch added an `[Unreleased]` entry. Needs reconciling — not urgent, but will get
  more annoying the longer it's deferred.
