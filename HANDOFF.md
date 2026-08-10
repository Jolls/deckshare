# Handoff

Scratch notes for starting a fresh session — where the work stands and what to pick up next.
**Not project documentation**: nothing references this file, nothing depends on it, and it can
go stale or be deleted without consequence. The reasoning lives in [CLAUDE.md](CLAUDE.md) and
[docs/architecture.md](docs/architecture.md); the detail lives in the issues.

_Last updated 2026-08-10, after [#39](https://github.com/Jolls/enshu/issues/39)._

## Read this first

**The docs are deliberately ahead of the code.** #38 re-decided several fundamentals without
touching the implementation. Where they disagree, the docs are right and the code is stale —
do not reconcile by editing the docs back. `docs/architecture.md` §1 lists what is left.

## Next up

**Bring the code to the docs.** These are the #38 decisions still unimplemented:

| | Issue | Notes |
|---|---|---|
| Batched card fetch with refill | [#40](https://github.com/Jolls/enshu/issues/40) | Loader still fetches 100 once and never refills; §6 specifies 20, refilling at 10 unseen. Touches the files #39 just reshaped |
| Drop `visibility` / `forked_from_deck_id` | [#41](https://github.com/Jolls/enshu/issues/41) | Schema + a `0005` migration + collapsing the access path |
| `notes.owner_id` composite FK | [#42](https://github.com/Jolls/enshu/issues/42) | Could ride along with #41's migration |
| §-references in code comments | [#44](https://github.com/Jolls/enshu/issues/44) | Bookkeeping. Cheap, and comments outliving their schema is how the §20 flattening survived unexamined |

**Then the `.apkg` path**, which is where Phase 1 actually finishes:

- [#33](https://github.com/Jolls/enshu/issues/33) — import: write the IR into the database.
  Carries a recorded decision about per-card decks (architecture.md §20).
- [#34](https://github.com/Jolls/enshu/issues/34) — media: filesystem blob store. Backend is
  settled, nothing is built, and cards with images do not work until it is.
- [#35](https://github.com/Jolls/enshu/issues/35) — deck import UI + real-fixture verification.
- [#10](https://github.com/Jolls/enshu/issues/10) — export. Unblocked; the reader's IR is the
  reference. Until this exists, the round-trip property (§10.3) has never run.
- [#25](https://github.com/Jolls/enshu/issues/25) — verify schema-18 protobuf field numbers.
  Half the import surface is untested against a real export.

**Last in Phase 1:** [#11](https://github.com/Jolls/enshu/issues/11), per-user parameter
optimisation. **Resolve architecture.md §12's optimiser question first** (`fsrs-rs-nodejs` vs
pure-TS vs out-of-process) — it is the only open question left that blocks implementation
rather than copy or policy.

## Open defects

- [#15](https://github.com/Jolls/enshu/issues/15) — deck/user deletion is unreachable behind
  FK restricts. Worth settling with #33's deck-cascade question.
- [#16](https://github.com/Jolls/enshu/issues/16) — CSP + card-content style/media policy.
- [#17](https://github.com/Jolls/enshu/issues/17) — note-type CSS is unsanitised. #6 closed
  card HTML; the `css` blob is still open.
- [#23](https://github.com/Jolls/enshu/issues/23) — no cleanup for expired sessions.
- [#46](https://github.com/Jolls/enshu/issues/46) — `/api/reviews/batch` has no route-level
  test. #39 broke the handler outright (every review rejected) with the suite fully green;
  review caught it, nothing else would have.

## Ideas, not scheduled

- [#43](https://github.com/Jolls/enshu/issues/43) — AI-authored cards via a documented
  paste-in text format. Deliberately not a feature that calls a model API (§11).

## Merged

Scaffold (#3) · Schema (#4) · `lib/fsrs/` (#5) · `lib/render/` engine (#12) and sanitisation
(#6) · Auth (#7) · CRUD (#8) · `.apkg` reader (#9) · Reviewer + write queue (#13) · Auth and
deck UI (#28, #29) · Re-import dedup keys (#32) · Changelog reconciliation (#36) ·
Fundamentals re-evaluation (#38) · Server-authoritative grading (#39).

`.apkg` reader caveat: the schema-11 path is verified against a real export; schema-18
protobuf field numbers are not (#25).
