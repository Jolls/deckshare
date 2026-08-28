---
name: implement-issues
description: Use when the user hands you a batch of GitHub issues (or a PR-slate grouping from an epic, e.g. "issue A → [B+C] → D") and wants each one scoped, planned, and implemented sequentially on a single branch, landing in one combined PR to main. Triggers on "evaluate and implement issues #NNN, #NNN", "work through the [epic] slate", "plan then implement these issues", "do the same for issues X and Y". Covers: /evaluate-issue per issue → parallel planning agents → resolve open questions with the user → sequential Sonnet-high implementation agents (one per group, minimal handback) on one branch left uncommitted (spot-checked with code-review-low per group) → human tests the tip → on approval, commit and open one combined PR to main. Not for a single ad-hoc bug fix (just do it directly) or for planning without implementing (use /evaluate-issue or Plan Mode alone).
---

# Implement a batch of issues on one branch, one combined PR

The user gives a list of issues, optionally pre-grouped into PR units. Each
**group** gets its own plan. Planning is the expensive part; implementation is
sequential in **one working tree on one branch** — apply group 1's edits, then
group 2 on top, etc., all left **uncommitted** until the human tests the
cumulative tip and approves. Git choreography happens once, at land time.

## Flow at a glance

```
0. Confirm groups + apply order (dependency first).
1. /evaluate-issue every issue (REQUIRED) → model/level for each planning agent.
2. Parallel planning agents, one per group → plan files with zero judgment calls.
3. Resolve every "Open question" with the user; write the decision into the plan.
4. One branch off main; per group: a sequential Sonnet-high implementation agent
   applies edits + build/test + /code-review low — UNCOMMITTED. Agents report back
   minimal status only; open questions get relayed to the human immediately.
5. Human tests the tip (working tree); recommend pre-commit checks; get go-ahead.
6. Commit (disjoint-guarded), push, one combined PR to main.
7. /done after merge.
```

## 0. Confirm groups and order

Respect any order the user gave (e.g. "752 → [753+755] → 754"). Otherwise
propose one (dependency first, else ascending risk/issue number) and confirm
before spending tokens on planning.

## 1. Evaluate each group

Run `/evaluate-issue <NNN>` (Skill tool) for every issue — **required**; it sets
the model/level for each group's planning agent. For a bundled group, evaluate
each member, then give one combined recommendation: model = Opus if _any_ member
trips Opus; level = max of members' (or name both and pick if on the fence).

## 2. Plan each group with a dedicated agent — in parallel

Launch one Agent per group in a single message (so they run in parallel) at the
model/level `/evaluate-issue` recommended. Prompt each with:

- The issue body/locations verbatim — don't make it re-fetch from `gh`.
- This instruction: **"provide a plan with specific file changes to implement
  the plan. The plan should only provide enough context needed to implement. No
  judgement calls in the plan. If unsure, ask."**
- Plan path: `docs/plans/<issue-id(s)>-<short-slug>.md`.
- Read the actual current code at every referenced location before writing —
  issue text describing line numbers goes stale.
- **Return only the plan path + open questions** — not the plan body, code
  excerpts, or investigation notes. The plan lives in the file; the manager
  reads it from disk in step 4. Path + open questions is all step 3 needs.

## 3. Resolve open questions

For each open question across all plans:

- Use `AskUserQuestion`, one per item, options as concrete choices (not yes/no),
  recommended option first when you have one.
- **Write the resolution into the plan file** as a "Resolved decision" with the
  exact change spec, so every plan is fully actionable with zero judgment calls
  left before you touch code.

## 4. Implement each group with a dedicated agent — sequential, minimal handback

Token-saving design: the manager (this session) does **not** read plan bodies,
diffs, or investigation notes for implementation — it only relays plan paths in
and short status reports out. Most groups' plans are self-contained (step 2
already forced "no judgment calls left"), so most groups don't need to know
anything about any other group's plan.

One branch off main for the whole batch, created by the manager:

```
git checkout main
git checkout -b feature/<batch-slug>
```

Then per group, **in apply order**, spawn one Agent (`subagent_type: claude`,
`model: sonnet`, high reasoning effort) scoped to that group only:

- **Default — no cross-group context.** Prompt: the resolved plan file path
  (the agent reads it off disk — it's already fully resolved, zero judgment
  calls per step 3) plus instruction to implement it exactly as written, run
  build/test, run `/code-review low` on the incremental diff and fix what it
  flags, and leave everything **uncommitted**.
- **Exception — shared context.** Only include another group's plan/decisions
  in the prompt when this group's own plan explicitly depends on it (shared
  files, an interface the earlier group introduced, a resolved decision that
  constrains this group too). Don't default to bundling context "just in
  case" — that's the token cost this design exists to avoid. If two groups are
  coupled tightly enough that splitting them would just make the second agent
  re-derive what the first one did, implement them as one agent call for both
  plans rather than forcing an artificial split.
- **Run agents one at a time, in the foreground** (`run_in_background: false`),
  waiting for each to finish before launching the next — they share the same
  working tree and each group applies on top of the previous group's
  uncommitted edits, so they cannot run in parallel like the planning agents did.
- Implementation agents must **not** call `AskUserQuestion` themselves. If a
  plan turns out to have a judgment call it didn't anticipate, the agent stops
  and returns the question with concrete options instead of guessing or asking
  the user directly.
- **Return only:** pass/fail on build/test (and integration tests where run),
  pass/fail + fixes-applied on `/code-review low`, the list of files touched,
  and any open question/blocker (with concrete options) — not the diff, not
  code excerpts, not a narration of what it did. The manager and the human can
  read the working tree directly if they need more.

Prompt each agent to run the project's standard build/test commands. If the
group touched schema, handlers, or other integration-covered code, also run
the project's integration test suite where one exists (per the repo's
CLAUDE.md pre-commit guidance) — on a stale-fixture/environment failure, the
agent should report it rather than try to fix the environment itself.

**When an agent returns an open question:** relay it to the user via
`AskUserQuestion` immediately (same pattern as step 3), write the resolution
wherever it needs to live (plan file and/or code comment), and only then move
to the next group — don't let an unresolved judgment call ride into later
groups.

If a group's plan calls for materially higher effort than routine
implementation (e.g. Opus-level schema/auth/FSRS work per CLAUDE.md §7), ask
the user before spawning that group's agent at a higher model/effort — don't
silently downgrade complex work to fit the Sonnet-high default.

The working tree accumulates the whole batch uncommitted; the "tip" the human
tests is just the working-tree state.

## 5. Human tests the tip, then recommend pre-commit checks

Stop and let the user manually test the working tree (the full cumulative diff).
Before handing off, give the user a bullet-point **manual test-points list**
covering the whole batch (not per-group) so they know what to click through
without re-reading every plan. Base it on the actual diff, not guesswork:

- **Routes/pages/entry points touched**, with how to reach them (nav path, CLI
  command, direct URL, etc.) — one bullet per entry point.
- **New/changed UI elements** to interact with: buttons, form fields, modals,
  filters — what to click and what result to expect.
- **Golden path** per feature: the normal, expected-to-work flow end to end.
- **Edge cases / invalid inputs** worth poking: empty fields, duplicate values,
  bad file types, permission boundaries — anything the plan's success criteria
  called out.
- **Cross-feature regressions**: existing features that touch the same
  tables/handlers/templates and could break silently (e.g. a shared partial,
  a modified helper used elsewhere).
- **Data-visible checks**: what to look for in the DB/UI after the action
  (row inserted, field updated, audit record written) if not obvious from the UI alone.
- If any group required a special test mode or environment/seed state, note
  that here so the human doesn't test against the wrong data.

Before any commit, recommend the checks the diff warrants (per CLAUDE.md
pre-commit): a deeper `/code-review medium|high` over the whole combined diff if
it's large or touches schema/auth/cross-cutting (the per-group `low` passes were
spot checks), plus integration tests if not already run, plus `/simplify` if the
batch introduced any reuse/simplification/efficiency cleanup opportunity worth a
quality-only pass (it doesn't hunt bugs, so it's additive to code-review, not a
substitute). Run the chosen passes, summarize, fix. **Commit only on the user's
go-ahead — never before.**

## 6. On approval: commit (disjoint-guarded), push, one combined PR

Check whether any file is touched by more than one group (you know each group's
file list from its implementation agent's return, cross-checked against its plan):

- **All groups file-disjoint (normal):** commit per group, staging just that
  group's files, so each issue gets a clean commit:
  ```
  git add <group 1 files>; git commit -F <msg citing issue 1>
  git add <group 2 files>; git commit -F <msg citing issue 2>
  ```
- **Any file shared by ≥2 groups:** don't split it (whole-file staging can't
  attribute lines; `git add -p` is interactive/blocked). Make **one commit for
  the whole batch** — the correct move, not a workaround.

Commit hygiene:

- `CHANGELOG.md`: **one** version entry (next sequential patch), all groups'
  changes grouped under `### Added`/`### Fixed`/etc.
- `git add` the plan files too — useful PR context.
- One tag on the tip commit (`git tag vX.Y.Z`).

Push and open one PR (body file — here-strings garble):

```
git push -u origin feature/<batch-slug>   # then: git push --tags
gh pr create --title "<summarize the batch>" --body-file <path>
```

PR body: one bullet per issue's change, and `Closes #NNN` on its own line per
issue (bare numbers after a comma don't auto-close). Test-plan checklist
reflects what was verified: build/test + `/code-review low` per group, any
deeper pass, and the human's manual test.

## 7. Clean up

Once the user confirms the PR is merged, run `/done`.

## Notes

- Apply order must put a dependency before the group that needs it (step 0) —
  you build it up in one tree.
- Keep each plan file scoped to its own group.
- If sequential implementation becomes the bottleneck (many groups, long
  builds), reconsider — but default to single-branch unless the user asks.
