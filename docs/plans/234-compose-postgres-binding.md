# 234 — compose.yaml publishes Postgres on 0.0.0.0 with a default password

## Problem

`compose.yaml` maps `5432:5432`, which binds Postgres to all interfaces on the host, using a
default `POSTGRES_USER`/`POSTGRES_PASSWORD`. It's the only compose file in the repo (no
separate deployment compose to contrast it against), and the project targets self-hosting
(Docker / StartOS) — someone will eventually run this on a machine with a public interface.

## Verification of no-op-ness for local dev

`.env.example`:

```
DATABASE_URL="postgres://root:mysecretpassword@localhost:5432/local"
```

The app connects via `localhost`, not a docker-compose service name. Binding the container's
published port to `127.0.0.1` instead of `0.0.0.0` has no effect on this connection — `localhost`
resolves to `127.0.0.1` (or `::1`) and reaches the loopback-bound published port exactly as it
reaches the current all-interfaces one. No other service in the repo connects to `db` via a
compose network alias (there's only the one service). Confirmed no-op.

## Change

`compose.yaml`, current content:

```yaml
services:
  db:
    image: postgres:18
    restart: always
    ports:
      - 5432:5432
    environment:
      POSTGRES_USER: root
      POSTGRES_PASSWORD: mysecretpassword
      POSTGRES_DB: local
    volumes:
      - pgdata:/var/lib/postgresql
volumes:
  pgdata:
```

Diff:

```diff
 services:
   db:
     image: postgres:18
     restart: always
+    # Development only. Loopback-only binding + default credentials are fine here because
+    # nothing outside this machine should ever reach this port; do not reuse this file as-is
+    # for a deployment.
     ports:
-      - 5432:5432
+      - 127.0.0.1:5432:5432
     environment:
       POSTGRES_USER: root
       POSTGRES_PASSWORD: mysecretpassword
       POSTGRES_DB: local
     volumes:
       - pgdata:/var/lib/postgresql
 volumes:
   pgdata:
```

Comment placement: directly above `ports:`, since that's the mapping the comment explains: the
loopback restriction, plus a pointer that this whole file is dev-only so a future deployment
compose file doesn't get created by copying this one's credentials forward.

## Scope

Single-file change: `compose.yaml` only. No other file references the `db` service's port
mapping or credentials in a way that would need updating (`.env.example`'s `DATABASE_URL`
already uses `localhost`, confirmed above).

## Verification

- Visual diff review — no `go build`/`go test` surface touched.
- Manual: `docker compose up db`, confirm the app still connects locally via the existing
  `DATABASE_URL`, and confirm `5432` is no longer reachable from another host/interface (e.g.
  `docker port` shows `127.0.0.1:5432` rather than `0.0.0.0:5432`).
