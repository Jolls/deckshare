---
name: run-app
description: Use when explicitly asked to run, start, or test the enshu app locally (or to shut it down afterward). Brings up Postgres via compose, applies goose migrations, builds and runs the Go server, smoke-tests it, and tears everything back down. Triggers on "run the app", "start the server", "test this locally", "shut it all down". Per CLAUDE.md §16, only start the dev server when explicitly asked — never as a verification step for routine changes (use build/vet/lint/test for that).
---

Run `bash .claude/skills/run-app/run.sh {start|stop|status}`.

- `start` fails with `PORT_IN_USE pid=<N>` if port 3000 is already bound. Check
  that PID with `Get-Process -Id <N>` before deciding to reuse or kill it —
  don't assume it's stale — then rerun `start`.
- `stop` kills the tracked server PID and runs `docker compose down` (keeps
  the `pgdata` volume; only add `-v` if the user wants the DB wiped too).
- `.env.example` documents `DATABASE_URL` / `ORIGIN` for non-default setups.

If DB-backed tests fail on stale state (leftover rows from a prior `run-app`
session — see issue #95), run
`bash .claude/skills/run-app/reset-db.sh` to wipe the `pgdata` volume, bring
Postgres back up, reapply goose migrations, and seed a test user
(`test@enshu.local` / `testpassword123`) with two empty decks. Never
improvise `docker compose down -v` by hand.
