# Deploying DeckShare

DeckShare ships as one multi-arch (amd64 + arm64) image, `ghcr.io/jolls/deckshare`, run beside
Postgres with Docker Compose. The image carries its own migrations and applies them at startup, so
there is no separate migration step.

**Requirements:** Docker with Compose v2, and a reverse proxy that terminates TLS (step 4).

## 1. Get the compose file

Copy [`deploy/compose.yaml`](../deploy/compose.yaml) and [`deploy/.env.example`](../deploy/.env.example)
to the server, into one directory. Rename `.env.example` to `.env`.

## 2. Configure `.env`

| Variable | Required | Meaning |
|---|---|---|
| `POSTGRES_PASSWORD` | yes | Database password, e.g. `openssl rand -hex 24`. |
| `ORIGIN` | yes | The public URL users reach the app at, e.g. `https://deckshare.example`. The CSRF check compares each state-changing request's `Origin` against it. |
| `TRUSTED_PROXY` | no | CIDRs of the reverse proxy whose `X-Forwarded-For` the server may trust. Without it every request appears to come from the proxy and the per-IP login/signup limiters collapse into one bucket. |
| `SIGNUP_MODE` | no | `open` (default) lets anyone who can reach the app register; `closed` stops new sign-ups. The image has no tool for creating the first account, so leave it `open` until your accounts exist, then set `closed` and `docker compose up -d`. |
| `LOG_FORMAT` | no | `json` (default in this compose file) or `text`. |
| `DECKSHARE_VERSION` | no | Image tag to run. Pin a release rather than following `latest`. |
| `BIND_ADDR` / `APP_PORT` | no | Where the app port is published on the host. Defaults to `127.0.0.1:3000`. |

## 3. Start

```
docker compose pull
docker compose up -d
```

Migrations run automatically before the app listens. If the database is unreachable or a migration
fails, the app exits non-zero and compose restarts it; see `docker compose logs app`. Postgres is
not published on a host port.

## 4. Put a TLS proxy in front

The app serves plain HTTP on `BIND_ADDR:APP_PORT` and does not terminate TLS. Point your reverse
proxy (Caddy, nginx, Traefik, …) at it, set `ORIGIN` to the public URL, and set `TRUSTED_PROXY` to
the proxy's address range.

## 5. Upgrade

Bump `DECKSHARE_VERSION`, then `docker compose pull && docker compose up -d`. Migrations are
forward-only: to go back to an older image you restore a backup.

## 6. Back up

State lives in two volumes: `pgdata` (Postgres) and `media` (uploaded and imported media).
Back up both. For a logical database backup:

```
docker compose exec db pg_dump -U deckshare deckshare > deckshare.sql
```

## Maintainers

Pushing a `v*` tag runs the `publish` job, which pushes `:<version>`, `:<major>.<minor>` and
`:latest` to GHCR. A package is private the first time it is pushed: set it to public (and linked to
the repository) in the package's settings on GitHub.
