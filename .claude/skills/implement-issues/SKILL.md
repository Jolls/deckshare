---
name: implement-issues
description: Use when the user hands you a batch of GitHub issues (or a PR-slate grouping from an epic, e.g. "issue A → [B+C] → D") and wants each one scoped, planned, and implemented sequentially on a single branch, landing in one combined PR to main. Triggers on "evaluate and implement issues #NNN, #NNN", "work through the [epic] slate", "plan then implement these issues", "do the same for issues X and Y". Covers: /evaluate-issue per issue → parallel planning agents → resolve open questions with the user → sequential test-first implementation on one branch left uncommitted (characterization tests, then failing tests, then the change; spot-checked with a low-effort code review per group) → human tests the tip → on approval, commit and open one combined PR to main. Not for a single ad-hoc bug fix (just do it directly) or for planning without implementing (use /evaluate-issue or Plan Mode alone).
---

# Implement a batch of issues on one branch, one combined PR

> Source: [Jolls/claude-skills](https://github.com/Jolls/claude-skills) (`skills/implement-issues`)
> — specialize downstream copies per-project; sync generic fixes both ways.

The user gives a list of issues, optionally pre-grouped into PR units. Each
**group** gets its own plan. Planning is the expensive part; implementation is
sequential in **one working tree on one branch** — apply group 1's edits, then
group 2 on top, etc., all left **uncommitted** until the human tests the
cumulative tip and approves. Git choreography happens once, at land time.

Each group is built **test-first**: pin down current behavior the change could
break, write tests that fail before the change and pass after, then make the
change. The point is to shrink what the human has to test by hand — a big
batch is hard to click through, but whatever the tests prove doesn't need
clicking.

## Flow at a glance

```
0. Confirm groups + apply order (dependency first).
1. /evaluate-issue every issue (REQUIRED) → model/level for each planning agent.
   If an issue isn't specific enough to plan without judgment calls, run
   /update-issue on it first, then re-evaluate.
2. Parallel planning agents, one per group → plan files (with a Test plan) and zero judgment calls.
3. Resolve every "Open question" with the user; write the decision into the plan.
4. One branch off main; plans moved into their issues (files deleted); per group: characterization tests (pass) → red tests
   (fail) → change (all pass) → a low-effort code review — UNCOMMITTED.
   Optional per-group human checkpoint.
5. Human tests the tip (manual-only items); recommend pre-commit checks; get go-ahead.
6. Commit (disjoint-guarded), push, one combined PR to main.
7. /done after merge.
```

## 0. Confirm groups and order

Respect any order the user gave (e.g. "752 → [753+755] → 754"). Otherwise
propose one (dependency first, else ascending risk/issue number) and confirm
before spending tokens on planning.

Also ask whether the user wants a **per-group checkpoint** (pause after each
group for a quick manual check, see step 4) or only the end-of-batch test.
Recommend it for large batches or groups with a lot of manual-only surface.

## 1. Evaluate each group

Run `/evaluate-issue <NNN>` (Skill tool) for every issue — **required**; it sets
the model/level for each group's planning agent. For a bundled group, evaluate
each member, then give one combined recommendation: model = higher-tier if *any*
member trips it; level = max of members' (or name both and pick if on the fence).

**If you (the manager) judge an issue isn't specific enough to hand a planning
agent** — ambiguous scope, missing acceptance criteria, conflicting comments,
anything that would force the agent to make a judgment call step 2 says it
shouldn't make — run `/update-issue <NNN>` (Skill tool) on it before planning.
Let that skill's own clarification/confirmation flow run with the user, then
re-run `/evaluate-issue` on the updated issue before proceeding to step 2.

## 2. Plan each group with a dedicated agent — in parallel

Launch one Agent per group in a single message (so they run in parallel) at the
model/level `/evaluate-issue` recommended. Prompt each with:
- The issue body/locations verbatim — don't make it re-fetch from `gh`.
- This instruction: **"provide a plan with specific file changes to implement
  the plan. The plan should only provide enough context needed to implement. No
  judgement calls in the plan. If unsure, ask."**
- Plan path: `docs/plans/<issue-id(s)>-<short-slug>.md` -- a scratch file while planning. Step 4
  moves it into the issue and deletes it, so it is never committed.
- Read the actual current code at every referenced location before writing —
  issue text describing line numbers goes stale.
- A **Test plan** section with four parts:
  1. **Coverage audit** — existing tests that already cover the functions/
     handlers the change touches (file + test name).
  2. **Characterization tests** — new tests pinning *current* behavior the
     change could break that nothing covers yet. Must pass on unchanged code.
     Mark any that knowingly lock in a bug the change will fix.
  3. **Red tests** — the issue's acceptance criteria as tests that fail on
     current code and pass after the change. Name each test, what it asserts,
     and why it fails today.
  4. **Manual-only** — what can't reasonably be automated (visual layout,
     client-side JS, OS dialogs, etc.), as short bullets.
  If a test needs new fixture/seed data, list the exact rows and note the
  human refresh step. If a group needs no tests, write `No tests: <stub
  reason>` (e.g. "copy change only") — a few words, not a paragraph.
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

This is also the change-proposal review: the user's go-ahead on the plans
covers writing the tests they list. `No tests: <reason>` needs no separate
sign-off.

## 4. Implement sequentially on one branch — uncommitted

The session that ran steps 0-3 continues directly — **no sub-agent**; it already
has the resolved plans and conventions loaded, and implementation is sequential
so there's nothing to parallelize. This session is typically low-effort; if a
plan wants a much higher level (e.g. a high-effort pass for a schema/auth-heavy
group), ask the user rather than silently implementing complex work at low effort.

**Unplanned judgment call mid-implementation** — don't guess (effort is fixed
per session):
- **Just needs the user's call:** surface it via `AskUserQuestion`, as in step 3.
- **Needs investigation to even frame the options** (tradeoffs, precedent, what
  breaks): spawn one narrow high-effort Agent scoped to *that question only*,
  then use its output to build the `AskUserQuestion`.

One branch off main for the whole batch:
```
git checkout main
git checkout -b feature/<batch-slug>
```

**Move each plan into its issue** before the first group: post the plan file as a comment on its
issue (`gh issue comment <N> --body-file docs/plans/<file>.md`; for a bundled group, comment on
each member issue), then delete the file. The issue comment is the plan from here on -- if it
changes mid-implementation, edit that comment rather than recreating a file. Plan files are never
committed.

Then per group in apply order, following its Test plan:
1. **Characterization tests** — write them, run them against unchanged code.
   They must **pass**. A failure means an existing bug or a wrong assumption:
   stop and ask the user, don't fix it silently.
2. **Red tests** — write them and run them. They must **fail on an
   assertion**, for the reason the plan gives. A compile error doesn't count;
   stub any new function/type signatures first so the test compiles and fails
   on behavior. If a red test passes already, the plan is wrong — stop and ask.
3. **Change** — apply the plan's edits until every test passes (flip any
   characterization test the plan marked as locking in a bug). Build/test
   using your project's normal commands.
4. If the group touched schema/migrations, or code covered by an
   integration/live-system test suite, run that suite and report pass/fail —
   on a stale-fixture/seed-data failure (including seed rows the plan added),
   ask the user to refresh it, never do that yourself.
5. Run a low-effort code review (e.g. `/code-review low`) on that group's
   incremental diff **before the next group**; fix what it flags and re-verify.
6. If the user chose per-group checkpoints (step 0): pause, give that group's
   manual-only items plus its golden path, and wait for the OK before the next
   group.
7. **Do not commit** — leave everything in the working tree.

`No tests` groups skip 1–2.

The working tree accumulates the whole batch uncommitted; the "tip" the human
tests is just the working-tree state.

## 5. Human tests the tip, then recommend pre-commit checks

Stop and let the user manually test the working tree (the full cumulative diff).
Before handing off, give the user a bullet-point **manual test-points list**
covering the whole batch (not per-group) so they know what to click through
without re-reading every plan. Base it on the actual diff, not guesswork.
Behavior the tests already prove doesn't need a manual step — lead with the
plans' manual-only items, list which tests cover the rest, and skip anything
already checked at a per-group checkpoint unless a later group touched it:
- **Screens/entry points touched**, with how to reach them (nav path, route,
  CLI command, etc.) — one bullet per entry point.
- **New/changed UI or interface elements** to interact with: buttons, form
  fields, modals, filters, flags — what to do and what result to expect.
- **Golden path** per feature: the normal, expected-to-work flow end to end.
- **Edge cases / invalid inputs** worth poking: empty fields, duplicate values,
  bad input types, permission boundaries — anything the plan's success criteria
  called out.
- **Cross-feature regressions**: existing features that touch the same
  data/handlers/shared code and could break silently (e.g. a shared partial,
  a modified helper used elsewhere).
- **Observable-state checks**: what to look for after the action (row
  inserted, field updated, log/audit entry written) if not obvious from the
  UI alone.
- If any group required a special test mode/environment, note that here so
  the human doesn't test against the wrong target.

Before any commit, recommend the checks the diff warrants (per your project's
documented pre-commit conventions, if any): a deeper code review over the whole
combined diff if it's large or touches schema/auth/cross-cutting concerns (the
per-group low-effort passes were spot checks), plus integration tests if not
already run, plus a simplify/cleanup pass if the batch introduced any
reuse/simplification/efficiency opportunity worth a quality-only pass. Run the
chosen passes, summarize, fix. **Commit only on the user's go-ahead — never
before.**

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
- Changelog (if your project keeps one): **one** entry, all groups' changes
  grouped by category.
- Don't commit plan files -- they were moved into the issues in step 4.
- Tag the tip commit if your project uses version tags.

Push and open one PR (body file — here-strings/heredocs can garble multi-line
content, prefer writing the body to a file first):
```
git push -u origin feature/<batch-slug>   # then: git push --tags
gh pr create --title "<summarize the batch>" --body-file <path>
```
PR body: one bullet per issue's change, and `Closes #NNN` on its own line per
issue (bare numbers after a comma don't auto-close). Test-plan checklist
reflects what was verified: tests added per group (characterization + red →
green), build/test + low-effort review per group, any deeper pass, and the
human's manual test.

## 7. Clean up

Once the user confirms the PR is merged, run `/done`.

## Notes

- Apply order must put a dependency before the group that needs it (step 0) —
  you build it up in one tree.
- Keep each plan scoped to its own group.
- If sequential implementation becomes the bottleneck (many groups, long
  builds), reconsider — but default to single-branch unless the user asks.

## Adapting this skill to your project

This skill pairs with `/evaluate-issue` and `/done` from this same repo, and
assumes a `/code-review` (or similar) skill exists for the review passes — swap
in whatever your project actually has. It also assumes GitHub issues/PRs via
`gh`; adjust step 6 if your project uses a different tracker or PR flow.
