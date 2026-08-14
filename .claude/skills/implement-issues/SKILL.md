---
name: implement-issues
description: Use when the user hands you a batch of GitHub issues (or a PR-slate grouping from an epic, e.g. "issue A → [B+C] → D") and wants each one scoped, planned, and implemented sequentially on a single branch, landing in one combined PR to main. Triggers on "evaluate and implement issues #NNN, #NNN", "work through the [epic] slate", "plan then implement these issues", "do the same for issues X and Y". Covers: /evaluate-issue per issue → parallel planning agents → resolve open questions with the user → sequential implementation on one branch left uncommitted (spot-checked with code-review-low per group) → human tests the tip → on approval, commit and open one combined PR to main. Not for a single ad-hoc bug fix (just do it directly) or for planning without implementing (use /evaluate-issue or Plan Mode alone).
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
4. One branch off main; per group: apply edits + build/test + /code-review low — UNCOMMITTED.
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

## 4. Implement sequentially on one branch — uncommitted

The session that ran steps 0-3 continues directly — **no sub-agent**; it already
has the resolved plans and conventions loaded, and implementation is sequential
so there's nothing to parallelize. This session is typically Sonnet-med; if a
plan wants a much higher level (e.g. Opus-high for schema/auth work), ask the
user rather than silently implementing complex work at med effort.

**Unplanned judgment call mid-implementation** — don't guess (effort is fixed
per session):

- **Just needs the user's call:** surface it via `AskUserQuestion`, as in step 3.
- **Needs investigation to even frame the options** (tradeoffs, precedent, what
  breaks): spawn one narrow high-effort Agent scoped to _that question only_,
  then use its output to build the `AskUserQuestion`.

One branch off main for the whole batch:

```
git checkout main
git checkout -b feature/<batch-slug>
```

Then per group in apply order:

- Apply that group's exact plan edits.
- Build/test (`build.bat`, `go test ./...`, etc.). If the group touched SQL,
  handlers, or integration-covered code, also run the live ArxDev integration
  tests (per CLAUDE.md pre-commit) and report pass/fail — on stale-seed failure,
  ask the user to reseed, never do it yourself. DSN command is saved at
  `arx_go/config/LOCAL-IntTesting.txt` (gitignored); source it, don't rebuild it.
- Run `/code-review low` on that group's incremental diff **before the next
  group**; fix what it flags and re-verify.
- **Do not commit** — leave everything in the working tree.

The working tree accumulates the whole batch uncommitted; the "tip" the human
tests is just the working-tree state.

## 5. Human tests the tip, then recommend pre-commit checks

Stop and let the user manually test the working tree (the full cumulative diff).
Before handing off, give the user a bullet-point **manual test-points list**
covering the whole batch (not per-group) so they know what to click through
without re-reading every plan. Base it on the actual diff, not guesswork:

- **Routes/pages touched**, with how to reach them from the UI (nav tab → page,
  or a direct URL like `http://localhost:4568/parts/123`) — one bullet per route.
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
- If any group required TEST_MODE/ArxDev state, note that here so the human
  doesn't test against the wrong DB.

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
file list from its plan):

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
