# Step 0 — Repo setup

**Model:** Sonnet · **Effort:** low · **Prerequisites:** none · **Blocks:** everything

Mechanical, but three of these get materially more expensive once code exists. Do it as one
PR before any scaffolding.

Rename this file to `docs/plans/<issue-id>-repo-setup.md` once the issue exists (CLAUDE.md
§14).

---

## 1. Land the foundation docs

`feature/foundation-docs` is committed but unpushed.

```
git push -u origin feature/foundation-docs
gh pr create --base main --title "Add foundation docs" --body "..."
```

Merge before starting anything else — CLAUDE.md, `docs/schema.md`, and
`docs/apkg-format.md` are the input to every later step.

**Verify:** `main` contains all three files; `gh pr view --json state` reports `MERGED`.

---

## 2. `.gitattributes`

CLAUDE.md §16 says first commit, and git already warned about CRLF on the docs commit.
Retrofitting this later rewrites every file in one noisy diff.

```gitattributes
* text=auto eol=lf
*.png binary
*.jpg binary
*.apkg binary
*.colpkg binary
```

The `.apkg`/`.colpkg` lines matter — test fixtures are zip archives and must never be
line-ending-normalised.

**Verify:** `git add --renormalize .` produces no changes on a fresh clone.

---

## 3. `LICENSE` — AGPL-3.0-or-later

Decided: **AGPLv3-or-later**. Rationale and alternatives in `docs/plans/licence-notes.md`
(or inline in the PR description if that file isn't kept).

- `LICENSE` — verbatim text from <https://www.gnu.org/licenses/agpl-3.0.txt>. Do not
  reformat, retype, or wrap it.
- `package.json` → `"license": "AGPL-3.0-or-later"` (SPDX id) when Step 1 creates it.
- Rewrite README's **Licensing** section: it currently presents the choice as open. Keep the
  paragraph explaining that Enshu contains no Anki-derived code and therefore inherited no
  licence — that's still true and it's *why* the choice was free. State the decision and the
  reasoning (no proprietary hosted fork), and keep the note that institutional AGPL policies
  are a known adoption cost we accepted.
- Update CLAUDE.md §12: strike the licence open question, leave the rest.
- Keep the separate point that **deck content licensing is not the code licence** — the
  public directory must record and display a per-deck licence.

**Verify:** GitHub's repo sidebar shows "AGPL-3.0"; `LICENSE` diffs clean against the
upstream text.

---

## 4. Labels and milestones

Create the CLAUDE.md §15 label set:

```
sev: critical   sev: high   sev: med   sev: low
area: security  area: db    area: fsrs  area: apkg
area: build     area: refactor          area: test
```

Milestones: `v0.1 Phase 1 — single-user core`, `v0.2 Phase 2 — multiuser`, `Deferred`.

```
gh label create "sev: critical" --color b60205 --description "Security hole, data loss, or shipped crash"
gh milestone list   # verify
```

**Verify:** `gh label list` shows all twelve; `gh api repos/:owner/:repo/milestones` shows
three.

---

## 5. File Phase 1 as issues

One issue per step in CLAUDE.md §11, milestoned `v0.1`, so `/evaluate-issue <NNN>` and the
`feature/<issue-id>-<slug>` branch convention work from here on.

Minimum set: scaffold · schema · FSRS wrapper · template rendering · auth · CRUD ·
reviewer + write queue · apkg import · apkg export · parameter optimisation.

Label the schema, FSRS, apkg, and reviewer issues now — they're the ones whose model
selection is non-default.

**Verify:** `gh issue list --milestone "v0.1 Phase 1 — single-user core"` returns the full
set.

---

## 6. `CHANGELOG.md`

Seed it per CLAUDE.md §14 (Keep a Changelog):

```markdown
# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]
```

---

## Done when

`main` has the docs, `.gitattributes`, `LICENSE`, and `CHANGELOG.md`; labels and milestones
exist; Phase 1 issues are filed. Nothing here needs review passes — it's config, not code.
