# Aurelia — production deployment

This folder deploys the full stack with Docker Compose:

| Service    | Image / build              | Role                                    |
| ---------- | -------------------------- | --------------------------------------- |
| `postgres` | `postgres:16-alpine`       | Relational store (users, conversations, KBs, usage). |
| `redis`    | `redis:7-alpine`           | Cache, rate-limit counters, cross-process stop-stream pub/sub. |
| `qdrant`   | `qdrant/qdrant:v1.12.4`    | Vector search for RAG.                  |
| `api`      | `ghcr.io/hjxwz123/aurelia-api` *(or local build via `Dockerfile.server`)* | The HTTP API (`/api/*`). |
| `web`      | `ghcr.io/hjxwz123/aurelia-web` *(or local build via `Dockerfile.web`)* | Serves the built SPA, proxies `/api` to `api`. |

See the [root README](../README.md) for the full project overview; this file is
just the deployment cheat-sheet.

## How backend selection works

The API binary is the **same** one used in local dev. It picks each backend by
inspecting an environment URL at boot:

- `DATABASE_URL=postgres://…` → Postgres (via the `pgcompat` driver); anything
  else (e.g. a `*.db` path) → embedded SQLite.
- `REDIS_URL` set → Redis; unset → in-process memory cache.
- `QDRANT_URL` set → Qdrant; unset → vector search disabled, RAG falls back to
  brute-force cosine over the embeddings mirrored in the relational store.

So **nothing needs to be installed locally** to run the app — leave those URLs
unset and it runs on SQLite + memory + brute-force. This compose file sets all
three, giving the production topology.

Embeddings are dual-written: every chunk vector goes to both Postgres (insurance
/ fallback) and Qdrant (search). Deleting a document/KB/conversation removes its
points from Qdrant too.

## First deploy (prebuilt images)

```bash
cd deploy
cp .env.example .env
# edit .env: set POSTGRES_PASSWORD, REDIS_PASSWORD, JWT_SECRET,
# SEED_ADMIN_PASSWORD, and PUBLIC_ORIGIN at minimum.
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

The app is then on `http://<host>:${WEB_PORT}` (default 80). Log in with
`SEED_ADMIN_EMAIL` / `SEED_ADMIN_PASSWORD`, then add real provider channels in
**/admin** (their API keys are stored in the database).

`store.Migrate()` runs automatically on boot and creates the Postgres schema
(`schema_pg.sql`) if the tables don't exist — no manual SQL step.

## Build the images locally

When iterating on the codebase, or on an architecture not covered by the
official images:

```bash
cd deploy
cp .env.example .env
docker compose -f docker-compose.prod.yml up -d --build
```

The compose file declares both `image:` and `build:`, so Compose prefers the
prebuilt image when present and falls back to a local build otherwise.

## Embedding dimension

Qdrant uses one collection per embedding width (`aurelia_c<dim>`). If you
configure a real embedding model, set `EMBEDDING_DIM` (and/or the model's `dim`
in the admin UI) to match — otherwise the local 256-dim embedder is used and
its collection won't match a 1536-dim model's vectors.

## TLS

This compose terminates plain HTTP on `WEB_PORT`. For public deployments put a
TLS terminator (Caddy, Traefik, or a cloud LB) in front of the `web` service and
set `PUBLIC_ORIGIN=https://your-domain`.

## Backups

Persisted in named volumes: `pgdata`, `redisdata`, `qdrantdata`, `apidata`
(uploads + artifacts). Back these up together so vectors, rows and files stay
consistent.
