#!/usr/bin/env bash
# One-command reset of the local Postgres dev/test DB: wipe the pgdata volume, bring the
# container back up, wait for readiness, reapply goose migrations, and seed a test user/decks.
# See SKILL.md. Reach for this when DB-backed tests fail on stale state left by a prior run-app
# session (issue #95) rather than improvising `docker compose down -v` by hand.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

DB_URL="postgres://root:mysecretpassword@localhost:5432/local"

docker compose down -v
docker compose up -d db
cid="$(docker compose ps -q db)"
ready=0
for i in $(seq 1 10); do
  docker exec "$cid" pg_isready -U root -d local >/dev/null 2>&1 && { ready=1; break; }
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "Postgres did not become ready in time" >&2
  exit 1
fi

DATABASE_URL="$DB_URL" goose -dir migrations postgres "$DB_URL" up
DATABASE_URL="$DB_URL" go run ./cmd/seed

echo "reset complete: fresh DB, migrations applied, test user/decks seeded"
