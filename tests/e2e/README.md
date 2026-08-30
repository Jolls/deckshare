# E2E (Playwright)

Browser-level coverage for paths a Go unit test can't reach: real keypresses, real `fetch()`
calls, real cookies. See CLAUDE.md §10 priority 6 and issue #100.

## Running locally

1. Install deps once: `npm install` (repo root), `npx playwright install --with-deps chromium`.
2. Start a dev server: `go run ./scripts/run-app start` (run-app skill). This also brings up
   Postgres and applies migrations.
3. `npx playwright test`
4. When done: `go run ./scripts/run-app stop`

Each spec signs up its own throwaway user rather than touching the shared seeded
`test@test.com` account (CLAUDE.md §16 — DB-backed tests stay scoped to their own rows and
tolerate a populated database).

Not auto-started via Playwright's `webServer` option: `run-app start` also owns
Postgres/migrations/build, which is more than Playwright's process-per-run model fits, and
CLAUDE.md's dev-server rule is explicit-start-only.
