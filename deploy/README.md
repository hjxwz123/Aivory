# Aivory — production deployment

<p align="center">
  <a href="./README.md"><strong>English</strong></a> ·
  <a href="./README.zh-CN.md">简体中文</a>
</p>

This folder deploys the full stack with Docker Compose:

| Service    | Image / build              | Role                                    |
| ---------- | -------------------------- | --------------------------------------- |
| `postgres` | `postgres:16-alpine`       | Relational store (users, conversations, KBs, usage). |
| `redis`    | `redis:7-alpine`           | Cache, rate-limit counters, cross-process stop-stream pub/sub. |
| `qdrant`   | `qdrant/qdrant:v1.12.4`    | Vector search for RAG.                  |
| `sandbox`  | `ghcr.io/hjxwz123/aivory-sandbox-sidecar` | Bundled code-execution sandbox (internal-only). |
| `app`      | `ghcr.io/hjxwz123/aivory-app` | One container serving BOTH the built SPA and the `/api` backend on the same origin. |

See the [root README](../README.md) for the full project overview; this file is
just the deployment cheat-sheet.

## How backend selection works

The API binary is the **same** one used in local dev. It picks each backend by
inspecting an environment URL at boot:

- `DATABASE_URL=postgres://…` → Postgres (via the `pgcompat` driver); anything
  else (e.g. a `*.db` path) → embedded SQLite.
- `REDIS_URL` set → Redis; unset → in-process memory cache.
- `QDRANT_URL` set → Qdrant; unset → vector search disabled, RAG injects the
  full in-scope document text as a fallback.

So **nothing needs to be installed locally** to run the app — leave those URLs
unset and it runs on SQLite + memory + full-context RAG fallback. This compose
file sets all three, and Docker deployments use Qdrant by default.

Chunk vectors are stored only in Qdrant. The relational database stores chunk
text and retrieval metadata, which lets the retriever validate Qdrant hits and
fall back to full-context injection if Qdrant is unavailable or empty. Deleting
a document/KB/conversation removes its points from Qdrant too.

## Quick deploy with Docker

```bash
cd deploy
cp .env.example .env
# edit .env: set POSTGRES_PASSWORD, REDIS_PASSWORD, and JWT_SECRET at minimum.
# There is NO domain/CORS/port env to set — the app serves the SPA and /api on
# one origin, so whatever host it's reached on works (multiple domains included).
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

> **Sandbox runtime image.** Code execution (`python_execute`) runs in a SEPARATE
> ~600MB image the sidecar `docker run`s per session. The `sandbox-image-keepalive`
> service pins that image locally so `pull` fetches it AND a host `docker image
> prune -a` / cleanup cron can't evict it. Without that pin the image goes
> unreferenced between sessions (the per-session containers are ephemeral), gets
> pruned, and the next `python_execute` cold-pulls it and times out (sidecar 500:
> "docker run … timed out"). If you run your own image cleanup, prefer
> `docker image prune -f` (no `-a`, which spares tagged images).

The app is then on `http://<host>` (host port 80 by default; change the
`"80:8787"` mapping in `docker-compose.prod.yml` if 80 is taken). On first launch
the deployment has zero users — the FIRST account you create via the setup screen
becomes the administrator. Then add real provider channels in **/admin** (their
API keys are stored in the database).

`store.Migrate()` runs automatically on boot and creates the Postgres schema
(`schema_pg.sql`) if the tables don't exist — no manual SQL step.

## Deploy or roll back by version

`IMAGE_TAG` selects one release for the app, sandbox runtime, and sandbox
sidecar. Release tags publish all three images with the same semver tag, so a
future version such as `3.0.0` needs only one value even when sandbox code has
changed.

Edit the real `deploy/.env` used by Compose, not `.env.example` after it has
already been copied:

```dotenv
# Git tag v3.0.0 produces image tag 3.0.0; do not include the leading v.
IMAGE_TAG=3.0.0
```

Render the image list before pulling so the selected release is explicit:

```bash
cd deploy
docker compose --env-file .env -f docker-compose.prod.yml config --images
docker compose --env-file .env -f docker-compose.prod.yml pull
docker compose --env-file .env -f docker-compose.prod.yml up -d --no-build
docker compose --env-file .env -f docker-compose.prod.yml images
```

The rendered list should include at least:

```text
ghcr.io/hjxwz123/aivory-app:3.0.0
ghcr.io/hjxwz123/aivory-sandbox:3.0.0
ghcr.io/hjxwz123/aivory-sandbox-sidecar:3.0.0
```

Wait until all release images have finished publishing before deploying. A
failed or incomplete release will then fail during `pull` instead of silently
mixing versions. Rollback is the same operation: select an older complete tag,
then repeat `pull` and `up`.

Historical releases such as `2.2.6` predate matching sandbox semver images. To
deploy one, use the optional compatibility override:

```dotenv
IMAGE_TAG=2.2.6
SANDBOX_IMAGE_TAG=latest
```

If changing the tag appears to do nothing, `config --images` exposes the usual
causes immediately: `.env.example` was edited while the existing `.env` still
says `latest`, or Compose was launched from a directory that loaded a different
env file. Passing `--env-file .env` removes the latter ambiguity.

## Embedding dimension

Qdrant uses one collection per embedding width (`aivory_c<dim>`). If you
configure a real embedding model, set `EMBEDDING_DIM` (and/or the model's `dim`
in the admin UI) to match — otherwise the local 256-dim embedder is used and
its collection won't match a 1536-dim model's vectors.

## TLS & domains

The `app` container serves plain HTTP on host port 80. For public deployments put
a TLS terminator (Caddy, Traefik, or a cloud LB) in front of it. Because the SPA
and `/api` share one origin, there is **nothing to configure per domain** — point
as many domains as you like at the proxy and each one works, as long as the proxy
forwards the `Host` header (every reverse proxy does by default). No
`PUBLIC_ORIGIN` / `ALLOWED_ORIGINS`.

## Backups

Persisted in named volumes: `pgdata`, `redisdata`, `qdrantdata`. Uploads and
artifacts are bind-mounted from `DATA_DIR` (default `./data`). Back these up
together so vectors, rows and files stay consistent.

The admin **Backup & Migration** page also creates async full migration archives
for Docker deployments. Those ZIPs include the logical database dump, optional
uploads/artifacts, and Qdrant vectors, and are stored under `BACKUP_DIR`
(default `/app/data/backups`, visible on the host as `DATA_DIR/backups`).
Backup import accepts up to `MAX_BACKUP_BYTES` bytes (default 20 GiB).
