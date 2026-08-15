#!/usr/bin/env bash
# Mechanical start/stop for local enshu dev stack. See SKILL.md for the one
# judgment call this doesn't automate (port-3000 conflict on start).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

PORT=3000
DB_URL="postgres://root:mysecretpassword@localhost:5432/local"
PIDFILE=".claude/skills/run-app/.server.pid"
BIN=".claude/skills/run-app/.enshu-server.exe"
LOG=".claude/skills/run-app/.server.log"
MEDIA_ROOT=".claude/skills/run-app/.media"

port_pid() {
  netstat -ano 2>/dev/null | grep ":${PORT} " | grep LISTENING | awk '{print $NF}' | head -1
}

start() {
  existing="$(port_pid || true)"
  if [ -n "$existing" ]; then
    echo "PORT_IN_USE pid=$existing — inspect with PowerShell Get-Process before deciding to kill or reuse it."
    exit 2
  fi

  docker compose up -d db
  cid="$(docker compose ps -q db)"
  for i in $(seq 1 10); do
    docker exec "$cid" pg_isready -U root -d local >/dev/null 2>&1 && break
    sleep 1
  done

  DATABASE_URL="$DB_URL" goose -dir migrations postgres "$DB_URL" up
  go build -o "$BIN" ./cmd/enshu
  mkdir -p "$MEDIA_ROOT"

  ADDR=":${PORT}" DATABASE_URL="$DB_URL" MEDIA_ROOT="$MEDIA_ROOT" "$BIN" >"$LOG" 2>&1 &
  echo $! >"$PIDFILE"
  sleep 1

  code="$(curl -s -o /dev/null -w '%{http_code}' -I "http://localhost:${PORT}/")"
  echo "started pid=$(cat "$PIDFILE") http_status=$code (expect 303 -> /login)"
}

stop() {
  if [ -f "$PIDFILE" ]; then
    kill "$(cat "$PIDFILE")" 2>/dev/null || true
    rm -f "$PIDFILE"
  fi
  remaining="$(port_pid || true)"
  if [ -n "$remaining" ]; then
    echo "STILL_LISTENING pid=$remaining — not killed automatically, verify before Stop-Process."
  fi
  docker compose down
  echo "stopped"
}

status() {
  pid="$(port_pid || true)"
  if [ -n "$pid" ]; then echo "port $PORT listening, pid=$pid"; else echo "port $PORT free"; fi
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  status) status ;;
  *) echo "usage: run.sh {start|stop|status}" >&2; exit 1 ;;
esac
