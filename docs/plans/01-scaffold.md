# Step 1 — Scaffold

**Model:** Sonnet · **Effort:** medium · **Prerequisites:** Step 0 merged ·
**Blocks:** all three tracks in `02-phase-1-tracks.md`

Pattern-following work, but two parts carry real judgment: the `lib/server` boundary and the
strict-TS configuration. Don't let it drift into writing feature code — this step ends when
an empty app builds, tests, and connects to a database.

Rename to `docs/plans/<issue-id>-scaffold.md` once the issue exists.

---

## The non-obvious requirement

**Install every Phase 1 dependency here, including ones nothing imports yet.**

Tracks A, B, and C run in parallel afterwards. If each adds its own dependencies, all three
edit `package.json` and `package-lock.json` and conflict on every merge — for no reason,
since the dependency set is already known. Front-load it and the tracks touch disjoint
directories.

Then **write the resolved versions into CLAUDE.md §3**, which currently asserts none on
purpose (no network access when it was written). Pin exact versions, no carets, for
`ts-fsrs` at minimum.

> `ts-fsrs` must be the **same exact version** on client and server, recorded in
> `user_fsrs_params.fsrs_version`. A client and server disagreeing about intervals is this
> system's worst failure mode because it is silent. Pin it and add a comment saying why.

---

## Tasks

### 1. SvelteKit + TypeScript

`npm create svelte@latest` — skeleton project, TypeScript, ESLint, Prettier, Vitest,
Playwright.

`tsconfig.json`: `"strict": true`, `"noUncheckedIndexedAccess": true` (CLAUDE.md §9).

**Verify:** `npm run build` and `npm run check` both pass on the skeleton.

### 2. Directory skeleton

Create the CLAUDE.md §4 tree with `.gitkeep` files — the layout is a decision, and making it
physical stops later sessions from inventing their own:

```
src/lib/server/{db/{queries},auth,apkg,fsrs}/
src/lib/{fsrs,review,render,components}/
src/routes/{(app),(auth),api}/
tests/{unit,fixtures/apkg,e2e}/
```

Add `tests/fixtures/apkg/README.md` explaining what each fixture must record (Anki version,
what it exercises). **Start collecting fixtures now** even though nothing reads them —
CLAUDE.md §10 flags them as the hardest test asset to produce later.

### 3. Postgres + Drizzle

`drizzle-orm`, `drizzle-kit`, `postgres` (or `pg`). `drizzle.config.ts` pointed at
`src/lib/server/db/schema.ts`, migrations output to `drizzle/`.

Docker Compose for local Postgres 16, or documented local install. `.env.example` committed,
`.env` gitignored.

Leave `schema.ts` **empty except for one throwaway table** to prove the pipeline. Track A
owns the real schema — do not pre-empt it.

**Verify:** `drizzle-kit generate` produces a migration; it applies to a fresh database; a
trivial query round-trips.

### 4. The server boundary

Add one import-boundary test: a client module importing from `$lib/server/**` must fail the
build. SvelteKit enforces this natively — confirm it actually fires rather than assuming.

**Verify:** temporarily import a server module from a client component, see the build fail,
revert.

### 5. CI

GitHub Actions on push and PR: install, `svelte-check`, ESLint, `vitest run`, `npm run
build`. Postgres service container so DB-touching tests can run.

Skip Playwright in CI for now — no app to drive yet. Add it with the reviewer.

**Verify:** a deliberately failing test fails the workflow.

---

## Done when

`npm run check && npm run lint && npm run test && npm run build` all pass; a migration
applies to a fresh database; CI is green on the PR; CLAUDE.md §3 lists real pinned versions.

## Explicitly not in this step

Real schema (Track A). Any FSRS code (Track B). Any rendering (Track C). Auth. Routes beyond
a placeholder page. If the diff contains a table definition other than the throwaway, scope
has slipped.
