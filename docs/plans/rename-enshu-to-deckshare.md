# Plan: rename Enshu → DeckShare

Status: **mechanical rename implemented on `feature/rename-enshu-to-deckshare`, GitHub repo
rename still pending.** Scope decisions below were confirmed with the user on 2026-09-03:

- Includes renaming the GitHub repository (`github.com/Jolls/enshu` → `github.com/Jolls/deckshare`),
  which means the Go module path changes too — **but the actual `gh repo rename` was deferred**
  at the user's request, so it hasn't happened yet. Everything in this repo now assumes the new
  name/path; the GitHub repo itself is still `Jolls/enshu` until that command is run.
- Name collision check done (see below) before committing to the name everywhere.
- No tracking issue opened, per the user's choice.

**What landed on the branch:** phases 2–5 below (module path, build tooling, user-facing
strings incl. the CSS scope class and session cookie name, living docs, CHANGELOG entry).
`go build`/`vet`/`golangci-lint`/`go test ./...` all pass. `README.md`'s and `deckshare.md`'s
naming-rationale sections were replaced with a draft placeholder, flagged for the user to
write the real version. Historical `docs/plans/*.md` and past `CHANGELOG.md` entries were left
untouched, as recommended in §4. GitHub issue URLs (`github.com/Jolls/enshu/issues/NNN`) were
deliberately left pointing at the current (unrenamed) repo — they'd break otherwise.

**Still open:** the actual `gh repo rename` (§3 step 1), the real naming-rationale writeup, and
re-running the diligence checks in §1 immediately before that rename.

## 1. Naming collision check (done, informal)

Web search turned up no active trademark or live competing product:

- **"Alfresco DeckShare"** — a 2011-era open-source Alfresco Web Quick Start add-on for
  presentation hosting (`github.com/jpotts/alfresco-deckshare`). Dormant, different domain
  (enterprise CMS plugin, not flashcards/SRS), no indication of an enforced mark.
- No registered USPTO trademark surfaced for "DeckShare" (informal web search only — not a
  full TESS database query).
- No conflicting live app/service found under the DeckShare name in the flashcard/SRS space.

**Before merging the rename**, do the diligence CLAUDE.md §13 gives `ANKI`-style marks, scaled
to what a pre-launch open-source project needs:
1. Direct query at `tmsearch.uspto.gov` for "DeckShare" in software/education classes.
2. Check `github.com/deckshare` (org/user) and a couple of domain registrars for availability.
3. Check npm/PyPI/Docker Hub namespace collisions only if those registries end up used.

None of this blocks drafting the plan; it blocks the final go/no-go before step 3 below.

## 2. What "Enshu" touches today

`Enshu`/`enshu` appears in 122 files. It falls into distinct buckets that should move as
separate, reviewable PRs rather than one mega-diff — a rename this mechanical is exactly the
case where a bad find-replace (e.g. inside a SQL string, a URL, or a historical doc) is easy to
miss in review if everything lands at once.

| Bucket | Examples | Risk |
|---|---|---|
| **Go module path** | `go.mod` (`module github.com/Jolls/enshu`), every `internal/...` and `cmd/enshu` import across ~90 `.go` files | Mechanical but total — breaks the build if partial. Must land atomically. |
| **Binary / build** | `cmd/enshu/main.go` → `cmd/deckshare/main.go`, `Dockerfile` (`go build -o /out/enshu ./cmd/enshu`, `ENTRYPOINT`), `.github/workflows/ci.yml` (`go build -o enshu ./cmd/enshu`), `scripts/run-app/main.go` | Mechanical, low semantic risk, but must match the module rename exactly or CI breaks. |
| **JS/e2e tooling** | `package.json` (`"name": "enshu-e2e"`), `package-lock.json` | Cosmetic, low risk. |
| **User-facing strings** | `web/templates/*.html` (layout, study, review, note_preview, review_cards — page titles/headers), `web/static/review.js` | User-visible; needs a look at rendered pages, not just text replace. |
| **Living docs** | `CLAUDE.md`, `README.md`, `docs/architecture.md`, `docs/routes.md`, `docs/schema.md`, `docs/schema-diagram.md`, `docs/apkg-format.md`, `docs/anki-schema.md`, `docs/anki-schema-diagram.md`, `docs/review-flow.md`, `migrations/README.md`, `tests/fixtures/apkg/README.md`, `.claude/skills/run-app/SKILL.md` | These describe current state — should read "DeckShare" going forward. `enshu.md` (the personal-notes digest, including the `#Naming` section explaining what "Enshu" means) needs a rewrite of its own, not a search-replace — see §4. |
| **Historical/dated docs** | `CHANGELOG.md` (past entries), `docs/plans/*.md` (~35 files, each a dated snapshot of a past implementation) | These are historical records of decisions made *when the project was called Enshu*. Recommend leaving prose untouched — see §4. |
| **Go code comments/tests referencing "Enshu" as a value** | e.g. test fixtures asserting a rendered `<title>Enshu</title>` or similar string | Needs a real per-file look, not blind replace — a test asserting old behavior should still pass after the string changes, not be silently different. |

## 3. Sequencing

The current branch (`feature/140-wire-deck-export-route`) has unrelated in-flight work and
should land or be set aside first — this rename must not be mixed into that PR. Do it from a
fresh branch off `main` once #140 merges: `feature/rename-enshu-to-deckshare`.

Because the module path change is all-or-nothing (Go won't build with mixed import paths),
split the work into phases that are each independently buildable/testable, not into
independent PRs:

1. **External moves first, in order, before touching code:**
   - Rename the GitHub repo in Settings (`Jolls/enshu` → `Jolls/deckshare`). GitHub
     auto-redirects the old URL for git/HTTP, so this is low-risk and reversible short-term,
     but do it deliberately and update the local `origin` remote right after
     (`git remote set-url origin ...`).
   - Re-check step 1's diligence one more time immediately before this, since it's the
     hard-to-reverse step (once code/history reference the new path, unwinding is a second
     rename).
2. **Mechanical rename commit** (one commit, one PR): `go.mod` module path, every Go import,
   `cmd/enshu` → `cmd/deckshare`, `Dockerfile`, `.github/workflows/ci.yml`,
   `scripts/run-app/main.go`, `package.json` name field. Verify with `go build ./...`,
   `go vet ./...`, `golangci-lint run`, `go test ./...`, and a CI run against the renamed repo.
   No behavior or prose changes in this commit — pure path/name mechanics, so it's easy to
   review as "did the tool do a correct global replace."
3. **User-facing strings commit**: `web/templates/*.html`, `web/static/review.js`, any Go
   string literals/tests asserting rendered "Enshu" text. Actually load the changed templates
   (per CLAUDE.md, describe manual verification steps rather than starting the dev server
   unless asked) or use the `run` skill if the user wants it checked live.
4. **Living docs commit**: `CLAUDE.md`, `README.md`, `docs/architecture.md`, `docs/routes.md`,
   the schema/apkg docs, `docs/review-flow.md`, `migrations/README.md`,
   `tests/fixtures/apkg/README.md`, `.claude/skills/run-app/SKILL.md`. Also rewrite
   `enshu.md` → `deckshare.md`: this file has a `#Naming` section explaining *why* "Enshu"
   (演習, "seminar/practice drill") was chosen, which needs a genuine rewrite (what DeckShare
   means/why), not a mechanical substitution — flag for the user to write or approve the new
   rationale text rather than have it invented wholesale.
5. **CHANGELOG entry**: add a new `## [0.1.X]` entry under `### Changed` noting the rename,
   per CLAUDE.md §14 — do not rewrite old entries.
6. **Explicitly out of scope for this repo**: any external DeckShare branding — domain
   registration, hosting/DNS, social handles, PyPI/npm/Docker Hub org names, README badges
   pointing at old URLs elsewhere. List these for the user as follow-ups; none are files in
   this repo.

## 4. Decisions the user should confirm before execution

1. **Historical docs** (`docs/plans/*.md`, old `CHANGELOG.md` entries): leave as "Enshu" —
   they're dated records of what was true when written — or bulk-replace anyway? Recommend
   leaving them; CLAUDE.md's own memory guidance treats past state as a snapshot, not
   something to retcon.
2. **`enshu.md` naming rationale**: who writes the new "why DeckShare" paragraph — the user,
   or should Claude draft one for approval? (Recommend: user drafts or reviews closely, since
   it's a statement of intent, not a mechanical fact.)
3. **Repo rename timing**: confirm doing it before or after #140 merges (recommend: after, so
   the in-flight PR isn't retargeted mid-review).
4. **New Go module path**: confirm `github.com/Jolls/deckshare` (same owner, same casing
   convention) rather than any other org/casing.

## 5. Verification checklist (per phase 2 commit)

- `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` all pass against the
  new module path.
- Fresh `git clone` of the renamed repo builds cleanly (catches any lingering hardcoded old
  path CI/Docker missed).
- `grep -ri enshu` across the repo returns only the intentionally-preserved historical docs
  from §4.1.
