---
name: evaluate-issue
description: Use when kicking off a GitHub issue evaluation (typically in a cheap Sonnet-low session) to scope the work and recommend which model + reasoning level the *manager* session that implements it should run at, and whether sub-agents help. Triggers on "evaluate issue #NNN", "triage issue", "what model/level for #NNN". Recommends only — it does not implement.
---

# Evaluate issue → recommend the manager session

> Source: [Jolls/claude-skills](https://github.com/Jolls/claude-skills) (`skills/evaluate-issue`)
> — specialize downstream copies per-project; sync generic fixes both ways.

Your job: scope a GitHub issue and tell the user what model + reasoning level to run the **manager** (implementation) session at, and whether sub-agents would help. **Do not write code, branches, or a plan** — read, assess, recommend. Keep it cheap.

## Steps

1. Read the issue: `gh issue view <NNN> --comments`. Note any labels (severity/area/type) and linked issues/PRs.
2. Skim what it touches — grep/glob the referenced area(s); check whether it hits schema/migrations, auth/session/security-sensitive code, config/build plumbing, or spans multiple packages/modules.
3. Map to the criteria below.
4. Emit the recommendation block. Stop there.

## Output (exact shape)

> **#NNN: <issue title>**
> **Manager session:** `<model>-<level>` · **Sub-agents:** yes/no
> **Why:** <one line tying the choice to what the issue touches>
> **Watch-outs:** <optional — e.g. schema change ripples across multiple files; needs integration tests; needs a security review pass>

## Model — higher-tier model if any apply, else the default/cheaper model

- Schema/database changes (new or altered tables/columns — ripples across config, migrations, seed data, docs)
- Security-sensitive work — auth, CSRF, session, crypto
- Cross-cutting architecture, or a refactor touching multiple packages/modules together
- Subtle correctness where a wrong fix ships silently (high-severity bug in non-obvious logic)

The default/cheaper model handles the rest: routine handlers, UI/template tweaks, single-file bug fixes, well-scoped features that follow an existing pattern, docs/changelog.

## Level — reasoning effort

- **low** — mechanical or already-diagnosed: doc/label/copy edits, a known one-file fix, pattern-copy of an existing handler.
- **medium** — normal multi-file feature (e.g. handler + template + schema) or a bug needing modest investigation. **Default when unsure.**
- **high** — ambiguous scope, many interacting files, subtle correctness/security, or a design decision with ripple effects. Pair with the higher-tier model for schema/auth/architecture work.

## Sub-agents

Recommend **yes** when the work splits into independent parallel parts (broad search across multiple packages/areas, several self-contained sub-tasks) or the investigation would balloon the manager's context. Recommend **no** for linear, single-threaded changes — the coordination overhead isn't worth it.

## Guardrails

- This step runs directly in the current session — never delegate the read/evaluation to a sub-agent.
- Recommend; don't implement. No edits, no branch, no commits from this session.
- Base the call on evidence from the issue and code, not the issue title alone.
- When genuinely on the fence between two levels, name both and say which you'd pick.

## Adapting this skill to your project

This skill is generic on purpose. For best results, tell it (in your project's CLAUDE.md or similar) what counts as "schema/database," "security-sensitive," and "spans multiple packages" for your codebase — e.g. specific directories, label names, or file globs — so step 2 has concrete things to check.
