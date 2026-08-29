# Aivory — deployment profiles

<p align="center">
  <a href="./README.md"><strong>English</strong></a> ·
  <a href="./README.zh-CN.md">简体中文</a>
</p>

This folder contains two independent Docker Compose profiles:

- `docker-compose.personal.yml`: one app container with SQLite business data,
  embedded SQLite vectors, and in-process cache/queue; no bundled sandbox.
- `docker-compose.prod.yml`: the existing full Postgres + Redis + Qdrant stack.

The full stack contains:

| Service    | Image / build              | Role                                    |
| ---------- | -------------------------- | --------------------------------------- |
| `postgres` | `postgres:16-alpine`       | Relational store (users, conversations, KBs, usage). |
| `redis`    | `redis:7-alpine`           | Cache, rate-limit counters, cross-process stop-stream pub/sub. |
| `qdrant`   | `qdrant/qdrant:v1.12.4`    | Vector search for RAG.                  |
| `sandbox`  | `ghcr.io/hjxwz123/aivory-sandbox-sidecar` | Bundled code-execution sandbox (internal-only). |
| `app`      | `ghcr.io/hjxwz123/aivory-app` | One container serving BOTH the built SPA and the `/api` backend on the same origin. |

See the [official documentation](https://docs.aivorygo.com) for guided deployment
and configuration references, or the [root README](../README.md) for the full
project overview. This file remains the versioned deployment cheat-sheet.

## How backend selection works

The API binary is the **same** in both profiles. It resolves backends at boot:

- `DATABASE_URL=postgres://…` → Postgres (via the `pgcompat` driver); anything
  else (e.g. a `*.db` path) → embedded SQLite.
- `REDIS_URL` set → Redis; unset → in-process memory cache.
- `VECTOR_BACKEND=auto` (default) preserves the old rule: a configured
  `QDRANT_URL` selects Qdrant, otherwise vectors are disabled and RAG falls back
  to full in-scope document text.
- `VECTOR_BACKEND=qdrant` requires `QDRANT_URL`.
- `VECTOR_BACKEND=sqlite` requires a SQLite `DATABASE_URL` and enables embedded
  exact cosine vector search. The personal profile pins this setting.
- `VECTOR_BACKEND=disabled` explicitly disables vector retrieval.

The full Compose file still pins Postgres, Redis, and Qdrant exactly as before.
The personal Compose file pins SQLite, clears the remote service URLs, and
selects embedded vectors; copied environment values cannot silently re-enable
Redis or Qdrant.

Full-stack vectors remain in Qdrant. Personal vectors are normalized and stored
as binary rows in the same SQLite database, searched exactly in-process, and
included in logical backups. Both backends apply the same scope checks and
delete vectors when their document, KB, or conversation is removed.

## Personal deployment

```bash
cd deploy
cp .env.personal.example .env.personal
# Set JWT_SECRET. Configure embeddings here or later in admin when required.
$EDITOR .env.personal
docker compose --env-file .env.personal -f docker-compose.personal.yml pull
docker compose --env-file .env.personal -f docker-compose.personal.yml up -d
```

Open `http://<host>`. By default this profile starts only `app`; it does not
mount the Docker socket or pull either sandbox image. Python execution is
unavailable until a sandbox is configured.

For an external sandbox, configure its URL and Bearer key under **Admin →
Tools**, or set `SANDBOX_BASE_URL` and `SANDBOX_API_KEY` in `.env.personal`
before startup. To deploy the bundled sandbox locally instead, set these
matching values in `.env.personal`:

```dotenv
SANDBOX_BASE_URL=http://sandbox:8000
SANDBOX_API_KEY=aivory-personal-sandbox
```

Then add the optional profile to both commands:

```bash
docker compose --env-file .env.personal -f docker-compose.personal.yml --profile sandbox pull
docker compose --env-file .env.personal -f docker-compose.personal.yml --profile sandbox up -d
```

The profile starts `sandbox` and `sandbox-image-keepalive`. The sidecar uses the
host Docker socket to create locked-down session containers, so enable it only
on a host where that privilege is acceptable. The socket is never mounted in
the default one-container profile.

`DATA_DIR` defaults to `./data-personal`. Its `aivory.db` contains both business
rows and vectors; the same directory also holds uploads, artifacts, local
objects, and admin backup archives. Back up the whole directory. This profile is
single-instance only: do not scale `app` or place the SQLite file on NFS.

The bundled local hash embedder works without configuration but is a basic
fallback. For production-quality semantic retrieval, configure an
OpenAI-compatible embedding endpoint and ensure `EMBEDDING_DIM` matches its
actual output. Rebuild vectors in admin after changing model or dimension.

## Full-stack deployment

```bash
cd deploy
cp .env.example .env
# edit .env: set POSTGRES_PASSWORD, REDIS_PASSWORD, and JWT_SECRET at minimum.
# For a domain or HTTPS reverse proxy, also set the exact ALLOWED_ORIGINS value.
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

### CPU architecture compatibility

The three Aivory images (`app`, sandbox runtime, and sandbox sidecar) publish a
single multi-platform tag for both `linux/amd64` (`uname -m` reports `x86_64`)
and `linux/arm64` (`uname -m` reports `aarch64` or `arm64`). Compose selects the
host architecture automatically; no `platform:` override is required. ARM64
servers must run a 64-bit Linux OS; 32-bit ARM/`armv7l` is not supported.

Existing x86_64 deployments require no migration or configuration change. Keep
the existing `.env`, Compose file, image tags, volumes, and data, then use the
same `pull` and `up -d --no-build` commands documented below. New ARM64 servers
use the same quick-deploy commands shown above.

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

Qdrant uses one collection per embedding width (`aivory_c<dim>`); embedded
SQLite stores the dimension beside each vector. If you configure a real
embedding model, set `EMBEDDING_DIM` (and/or the model's `dim` in admin) to match.
The local 256-dim vectors stay isolated from a 1536-dim external model in either
backend.

## TLS & domains

The `app` container serves plain HTTP on host port 80. For public deployments put
a TLS terminator (Caddy, Traefik, or a cloud LB) in front of it. Set
`ALLOWED_ORIGINS` to the exact browser origin, for example
`ALLOWED_ORIGINS=https://chat.example.com`. For multiple origins, use
`ALLOWED_ORIGINS=https://chat.example.com,https://admin.example.com:8443`; do not
include paths or trailing slashes. Same-origin IP/HTTP testing may leave it unset.

## Backups

For the personal profile, business rows and vectors are both inside
`DATA_DIR/aivory.db`; the admin logical backup includes `vector_points`. Back up
the whole `DATA_DIR` as well so uploads and artifacts stay consistent with it.

For the full profile, the existing behavior is unchanged:

Persisted in named volumes: `pgdata`, `redisdata`, `qdrantdata`. Uploads and
artifacts are bind-mounted from `DATA_DIR` (default `./data`). Back these up
together so vectors, rows and files stay consistent.

The admin **Backup & Migration** page also creates async full migration archives
for Docker deployments. Those ZIPs include the logical database dump, optional
uploads/artifacts, and Qdrant vectors, and are stored under `BACKUP_DIR`
(default `/app/data/backups`, visible on the host as `DATA_DIR/backups`).
Backup import accepts up to `MAX_BACKUP_BYTES` bytes (default 20 GiB).
