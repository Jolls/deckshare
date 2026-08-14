---
name: run-app
description: Use when explicitly asked to run, start, or test the enshu app locally (or to shut it down afterward). Brings up Postgres via compose, applies goose migrations, builds and runs the Go server, smoke-tests it, and tears everything back down. Triggers on "run the app", "start the server", "test this locally", "shut it all down". Per CLAUDE.md §16, only start the dev server when explicitly asked — never as a verification step for routine changes (use build/vet/lint/test for that).
---

# Run enshu locally

## Start / Run

1. **Bring up Postgres:**
   ```bash
   docker compose up -d db
   ```
2. **Wait for it to accept connections:**
   ```bash
   for i in $(seq 1 10); do
     docker exec enshu-db-1 pg_isready -U root -d local && break
     sleep 1
   done
   ```
3. **Apply migrations** (idempotent — safe to run even if already current):
   ```bash
   export DATABASE_URL="postgres://root:mysecretpassword@localhost:5432/local"
   goose -dir migrations postgres "$DATABASE_URL" up
   ```
4. **Build first** to catch compile errors before launching:
   ```bash
   go build ./...
   ```
5. **Check port 3000 isn't already bound** before launching — a server from an
   earlier session may still be listening (`go run` leaves the compiled binary
   running independently of the shell that launched it):
   ```bash
   netstat -ano | grep ':3000' | grep LISTENING
   ```
   If something's there, identify it with PowerShell
   (`Get-Process -Id <pid>`) before deciding whether to reuse it or kill it —
   don't assume it's stale.
6. **Run the server** (background; `ADDR` defaults to `:3000` if unset):
   ```bash
   export DATABASE_URL="postgres://root:mysecretpassword@localhost:5432/local"
   export ADDR=":3000"
   go run ./cmd/enshu
   ```
7. **Smoke-test:**
   ```bash
   curl -s -I http://localhost:3000/
   ```
   Unauthenticated root should `303` redirect to `/login`.

`.env.example` documents `DATABASE_URL` and the optional `ORIGIN` (needed
only behind a reverse proxy that terminates TLS — see the comment in that
file and architecture.md §12 on the CSRF Origin check).

## Shut down / Stop

1. **Kill the server process** — find it by the port, not by assuming the
   backgrounded shell command owns it:
   ```bash
   netstat -ano | grep ':3000' | grep LISTENING
   ```
   Then in PowerShell: `Stop-Process -Id <pid> -Force`.
2. **Tear down Postgres:**
   ```bash
   docker compose down
   ```
   This removes the container and network but keeps the named volume
   (`pgdata`), so data survives a restart. Only add `-v` if the user asks to
   wipe the database too.
3. **Confirm the port is free:**
   ```bash
   netstat -ano | grep ':3000' | grep LISTENING || echo "port 3000 free"
   ```
