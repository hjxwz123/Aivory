# Aurelia

> A multi-model AI chat platform you can self-host in five minutes — Claude, GPT, Gemini, your favourite open-source models, all behind one editorial-feel UI with first-class streaming, tool use, RAG, and an admin backend.

<p align="center">
  <a href="./README.md"><strong>English</strong></a> ·
  <a href="./README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <a href="https://github.com/hjxwz123/Aurelia/actions/workflows/docker-images.yml"><img alt="Build status" src="https://github.com/hjxwz123/Aurelia/actions/workflows/docker-images.yml/badge.svg"></a>
  <a href="https://github.com/hjxwz123/Aurelia/pkgs/container/aurelia-api"><img alt="Container" src="https://img.shields.io/badge/ghcr.io-aurelia--api-blue?logo=docker"></a>
  <a href="https://github.com/hjxwz123/Aurelia/pkgs/container/aurelia-web"><img alt="Container" src="https://img.shields.io/badge/ghcr.io-aurelia--web-blue?logo=docker"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go">
  <img alt="React" src="https://img.shields.io/badge/React-19-61DAFB?logo=react">
  <img alt="TypeScript" src="https://img.shields.io/badge/TypeScript-5-3178C6?logo=typescript">
  <a href="./LICENSE"><img alt="License" src="https://img.shields.io/badge/license-MIT-green"></a>
</p>

---

## Overview

Aurelia is a self-hosted, vendor-neutral conversation product. Pick any model — Claude, OpenAI, Gemini, or anything that speaks one of their three wire formats — and get the same first-class experience: real-time streaming, multi-turn tool use, document QA, project workspaces, conversation branching, and a calm editorial UI that doesn't look like a developer demo.

The whole stack runs from a single `docker compose up`. PostgreSQL, Redis, and Qdrant come up alongside the API and the SPA; no installs on the host, no extra processes to babysit.

## Highlights

- **Multi-model, one experience.** A unified Provider layer wraps Claude / OpenAI (Chat Completions or Responses) / Gemini. Switch mid-conversation, models are tagged per message.
- **Real-time tool use.** Web search, web fetch, Python execution, image generation, and KB retrieval all stream their progress over SSE. Works on any model via native function calling, with a prompt-stitching fallback for models that don't speak it.
- **RAG that doesn't smear documents.** Hierarchical small-to-big chunking, ~12% overlap, structure-aware (never splits code/tables/formulas mid-block), heading-breadcrumb prefixing. Hybrid dense + BM25 search with reciprocal-rank fusion.
- **MinerU for hard documents.** Scanned PDFs / DOCX / PPTX / XLSX / images all route through MinerU's cloud API. Source files land in your own S3 or Aliyun OSS bucket, MinerU only sees a presigned URL.
- **Conversation tree, not linear log.** Edit a past question, retry a reply — every revision creates a branch you can flip between with `< 2/3 >` controls. The DB keeps the whole tree; compression only affects what we send to the model.
- **Admin backend.** Channels, models, skills, users (with real-time ban via JWT token-version bump), usage reports, global settings (sandbox / storage / search / MinerU / uploads). Everything live-reloaded — no restart on config change.
- **File upload safety.** Per-user storage directory, last-extension allowlist (so `report.pdf.exe` is judged on `.exe`), NUL / control-char rejection, size cap, MIME parsed with `mime.ParseMediaType`. Admin-configurable extension list with a safe default that excludes executables, archives, and HTML/SVG.
- **Production-ready images.** Multi-arch (amd64 + arm64) images built by GitHub Actions on every push, published to `ghcr.io/hjxwz123/aurelia-{api,web}`.

## Quick start (Docker, recommended)

Requires Docker 24+ with the Compose plugin.

```bash
# 1. Clone (you only need the deploy/ folder)
git clone https://github.com/hjxwz123/Aurelia.git
cd Aurelia/deploy

# 2. Fill in secrets
cp .env.example .env
$EDITOR .env             # set POSTGRES_PASSWORD, REDIS_PASSWORD,
                         #     JWT_SECRET, SEED_ADMIN_PASSWORD

# 3. Pull prebuilt images + start the stack
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

The app is now on `http://localhost` (or whatever you set `WEB_PORT` to). Log in with `SEED_ADMIN_EMAIL` / `SEED_ADMIN_PASSWORD`, head to `/admin/channels` and add your real provider keys.

Six containers come up:

| Container  | Image                                   | Role |
| ---------- | --------------------------------------- | ---- |
| `postgres` | `postgres:16-alpine`                    | Users, conversations, KBs, settings, usage. |
| `redis`    | `redis:7-alpine`                        | Cache, rate-limit counters, kill-signal pub/sub. |
| `qdrant`   | `qdrant/qdrant:v1.12.4`                 | Vector search for RAG. |
| `api`      | `ghcr.io/hjxwz123/aurelia-api:latest`   | Go HTTP server (`/api/*`). |
| `web`      | `ghcr.io/hjxwz123/aurelia-web:latest`   | nginx serving the SPA + `/api` reverse proxy. |

Data lives in named volumes `pgdata`, `redisdata`, `qdrantdata`, `apidata` — back these up together so vectors, rows and files stay consistent.

## Architecture

```
                     ┌────────────────────────────────┐
                     │  Browser (SPA, SSE streaming)  │
                     └─────────────┬──────────────────┘
                                   │ HTTPS
                       ┌───────────▼────────────┐
                       │ nginx (web container)  │
                       │ /        → static SPA  │
                       │ /api/*   → api:8787    │
                       └───────────┬────────────┘
                                   │ HTTP keep-alive
                  ┌────────────────▼─────────────────┐
                  │      Go API (api container)      │
                  │  REST + SSE orchestrator         │
                  │  ┌──────────────────────────┐    │
                  │  │ Provider registry        │    │
                  │  │  Claude / OpenAI / Gemini│    │
                  │  └──────────────────────────┘    │
                  │  ┌──────────────────────────┐    │
                  │  │ Self-built tool layer    │    │
                  │  │  web_search · web_fetch  │    │
                  │  │  python_execute · KB ret.│    │
                  │  │  image_generate          │    │
                  │  └──────────────────────────┘    │
                  └─┬──────────┬──────────┬──────────┘
                    │          │          │
            ┌───────▼──┐  ┌────▼────┐  ┌──▼─────────┐
            │ Postgres │  │  Redis  │  │   Qdrant   │
            │  schema  │  │  cache  │  │  vectors   │
            │  + audit │  │  + pub  │  │ (RAG)      │
            └──────────┘  └─────────┘  └────────────┘

  Optional external services (configured from admin UI, live-reload):
   • MinerU cloud (document parsing)
   • S3 / Aliyun OSS (upload bucket for MinerU + workspace archives)
   • SearXNG (self-hosted web search)
   • Real LLM providers (channel keys live in the DB, not in env)
```

See [`DESIGN.md`](./DESIGN.md) for the full design document (Chinese, ~2000 lines).

## Configuration

Most of Aurelia is configured **at runtime from the admin UI**, not from environment variables. Anything provider-specific (model keys, MinerU token, S3 credentials, SearXNG URL, upload allowlist…) is admin-editable and applies on the next request — no restart.

The handful of env vars in [`deploy/.env.example`](./deploy/.env.example) are:

| Group | Keys | Purpose |
| ----- | ---- | ------- |
| **Image** | `IMAGE_OWNER`, `IMAGE_TAG` | Which GHCR namespace / tag to pull |
| **Network** | `WEB_PORT`, `PUBLIC_ORIGIN` | nginx host port + CORS allow-list |
| **Postgres** | `POSTGRES_USER/PASSWORD/DB` | Database credentials |
| **Redis** | `REDIS_PASSWORD` | Required (used as `requirepass`) |
| **Qdrant** | `QDRANT_API_KEY` | Optional API key |
| **Auth** | `JWT_SECRET` | Required; ≥32 chars, refuses dev default in production |
| **Seed admin** | `SEED_ADMIN_EMAIL/PASSWORD` | First-boot admin only |
| **Boot fallbacks** | `SEARCH_*`, `EMBEDDING_*`, `MINERU_*` | Used only when the matching admin setting is absent |

## Build images yourself

The same compose file builds locally — useful when iterating on the codebase or running on an architecture not covered by the official images.

```bash
cd deploy
cp .env.example .env
docker compose -f docker-compose.prod.yml up -d --build
```

Both `api` and `web` services have `image:` and `build:` set, so Compose picks the prebuilt image when present and falls back to a local build otherwise.

## Local development (no Docker)

The Go API ships with an embedded SQLite driver and a hash-bag fallback embedder, so the whole app runs without any external service for development.

```bash
# Backend
cd server
go run ./cmd/api                  # listens on :8787

# Frontend (separate terminal)
cd ..
npm install
npm run dev                       # Vite at :5173, proxies /api to :8787
```

Open `http://localhost:5173`. Log in with `admin@aurelia.local` / `aurelia-admin` (defaults from `.env.example`). The mock provider is seeded so you can have a chat without configuring any real model.

## GitHub Actions: automatic image builds

Two workflows in [`.github/workflows/`](./.github/workflows):

| Workflow | Trigger | Output |
| -------- | ------- | ------ |
| **`docker-images.yml`** | push to `main`, `v*.*.*` tags, manual dispatch | `ghcr.io/<owner>/aurelia-api`, `ghcr.io/<owner>/aurelia-web` — multi-arch (amd64 + arm64) |

Tagging strategy:

- Push to `main`        → `:latest` + `:sha-<short>`
- Push tag `v1.2.3`     → `:1.2.3` + `:1.2` + `:1` + `:latest`
- Pull request          → built as a smoke test, NOT pushed

The workflow only needs `GITHUB_TOKEN` (auto-provided), no extra secrets to configure. After your first successful run, the packages appear under your repo's "Packages" sidebar — make them public from Package settings if you want anonymous pulls.

If you forked, also update `IMAGE_OWNER` in `deploy/.env` to your lowercased GitHub username.

## Tech stack

- **Frontend**: React 19, TypeScript 5, Vite 5, Tailwind 4, Radix UI, Zustand, i18next, lucide-react
- **Backend**: Go 1.22, standard `net/http`, sqlc-style hand-rolled queries
- **Storage**: PostgreSQL 16 (production) / SQLite (embedded fallback)
- **Cache & coordination**: Redis 7
- **Vector search**: Qdrant 1.12 (with brute-force PG fallback)
- **Document parsing**: MinerU cloud API (PDF / DOCX / PPTX / images via OCR)
- **Optional**: S3 / Aliyun OSS for source file hosting, SearXNG for self-hosted web search

## Project layout

```
.
├── src/                      React SPA (chat, admin, KB, projects)
├── server/                   Go API
│   ├── cmd/api/              main entrypoint
│   └── internal/
│       ├── api/              HTTP handlers, router, upload safety
│       ├── llm/              Provider adapters + orchestrator (SSE)
│       ├── tools/            web_search, web_fetch, python_execute, ...
│       ├── rag/              parse → chunk → embed → retrieve
│       ├── vector/           Qdrant client
│       ├── store/            Postgres / SQLite schema + queries
│       ├── sandbox/          HTTP client to the optional sandbox sidecar
│       └── storage/          HTTP client for S3 / OSS upload-presign
├── deploy/                   Production stack
│   ├── docker-compose.prod.yml
│   ├── Dockerfile.server     multi-stage Go build → debian-slim
│   ├── Dockerfile.web        Vite build → nginx
│   ├── nginx.conf            SPA + /api reverse proxy (SSE-aware)
│   └── .env.example          template
├── docs/                     specs, design notes
├── DESIGN.md                 the full design document (Chinese)
└── .github/workflows/        CI for building images
```

## Contributing

Pull requests welcome — please open an issue first for non-trivial changes so we can agree on shape.

Local checks before pushing:

```bash
# Frontend
npm run lint
npm run typecheck
npm run build

# Backend
cd server
go vet ./...
go build ./...
```

The CI runs the same plus `docker buildx build` for both images.

## License

[MIT](./LICENSE) — do whatever you want, including commercial use; just keep the copyright notice.

## Acknowledgements

- [Anthropic](https://www.anthropic.com), [OpenAI](https://openai.com), [Google](https://ai.google.dev) for the model APIs.
- [MinerU](https://mineru.net) for the document parsing API.
- [Qdrant](https://qdrant.tech) for the vector database.
- [SearXNG](https://github.com/searxng/searxng) for the self-hostable metasearch.
- The Radix UI / shadcn ecosystem for the headless primitive set Aurelia rethemes on top of.
