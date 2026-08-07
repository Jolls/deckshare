---
name: evaluate-issue
description: Use when kicking off a GitHub issue evaluation (typically in a cheap Sonnet-low session) to scope the work and recommend which model + reasoning level the *manager* session that implements it should run at, and whether sub-agents help. Triggers on "evaluate issue #NNN", "triage issue", "what model/level for #NNN". Recommends only — it does not implement.
---

# Evaluate issue → recommend the manager session

Your job: scope a GitHub issue and tell the user what model + reasoning level to run the **manager** (implementation) session at, and whether sub-agents would help. **Do not write code, branches, or a plan** — read, assess, recommend. Keep it cheap.

## Steps

1. Read the issue: `gh issue view <NNN> --comments`. Note the labels (`area:*`, `sev:*`) and any linked issues/PRs.
2. Skim what it touches — grep/glob the referenced area(s); check whether it hits SQL/DDL (`SQL/*.sql`), auth/session/CSRF middleware, config/test-mode plumbing, or both `arxlib/` **and** `arx_go/`.
3. Map to the criteria below.
4. Emit the recommendation block. Stop there.

## Output (exact shape)

> **#NNN: <issue title>**
> **Manager session:** `<model>-<level>` · **Sub-agents:** yes/no
> **Why:** <one line tying the choice to what the issue touches>
> **Watch-outs:** <optional — e.g. schema change needs the 4-file update in CLAUDE.md + test-mode seed; integration tests; security review pass>

## Model — Opus if any apply, else Sonnet

- Schema/table changes (new or altered tables — ripples across config, test mode, DDL, seed data, schema docs)
- `area: security` — auth, CSRF, session, crypto
- Cross-cutting architecture, or a refactor touching `arxlib/` **and** `arx_go/` together
- Subtle correctness where a wrong fix ships silently (`sev: critical`/`sev: high` in non-obvious logic)

Sonnet handles the rest: routine handlers, UI/template tweaks, single-file bug fixes, well-scoped features that follow an existing pattern, docs/changelog.

## Level — reasoning effort

- **low** — mechanical or already-diagnosed: doc/label/copy edits, a known one-file fix, pattern-copy of an existing handler.
- **medium** — normal multi-file feature (handler + template + SQL) or a bug needing modest investigation. **Default when unsure.**
- **high** — ambiguous scope, many interacting files, subtle correctness/security, or a design decision with ripple effects. Pair with Opus for schema/auth/architecture work.

## Sub-agents

Recommend **yes** when the work splits into independent parallel parts (broad search across `arxlib/` + `arx_go/`, several self-contained sub-tasks) or the investigation would balloon the manager's context. Recommend **no** for linear, single-threaded changes — the coordination overhead isn't worth it.

## Guardrails

- Recommend; don't implement. No edits, no branch, no commits from this session.
- Base the call on evidence from the issue and code, not the issue title alone.
- When genuinely on the fence between two levels, name both and say which you'd pick.
