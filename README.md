# Aurelia

> A multi-model AI chat platform you can self-host in five minutes — Claude, GPT, Gemini, your favourite open-source models, all behind one editorial-feel UI with first-class streaming, tool use, RAG, deep research, and a full admin backend.

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

## What makes Aurelia different

Most self-hosted chat frontends are thin wrappers: they proxy a request to an API and stream back text. Aurelia goes further on the dimensions that matter in production.

**Multi-round interleaved tool calls** — The model's tool loop runs up to 48 calls per turn (12 native provider cycles). Tools from completely different domains can freely chain within a single user message. The canonical example: the model calls `image_generate` to create a chart, then calls `python_execute` to build a PowerPoint slide deck — and the generated image is already sitting at `/workspace/uploads/` when the Python code runs, without any extra user action. Every artifact produced by one tool is automatically staged into the sandbox filesystem for the next tool, turning the model into a multi-step pipeline rather than a one-shot Q&A engine.

**Isolated Python sandbox** — Every conversation gets its own sandbox container. Python code runs with full standard library access, can install packages, read staged files, and write outputs that stream back as artifacts. The sandbox session persists for the lifetime of the conversation (recovering automatically if the container is reaped), so follow-up messages can read files written by earlier ones — a true stateful computing environment alongside the chat.

**Conversation tree, not a linear log** — Edit a past question, regenerate an answer, or retry with a different model. Every revision opens a new branch; you switch between them with `< 2/3 >` controls. The whole tree is stored in the database; only what gets sent to the model is ever compressed.

**Admin backend with zero config** — Channels, models, skills, users, usage reports, and every system setting are managed from a first-class admin UI. Nothing requires a restart. Flip a toggle, save — next request picks it up. Channel API keys live in the database, not in env files, so rotating a key doesn't mean touching the host.

---

## Feature overview

### Tools and pipelines

| Tool | What it does |
|------|-------------|
| `web_search` | Full-text web search via SearXNG (self-hosted) or any Serper-compatible backend. |
| `web_fetch` | Fetch and extract a specific URL, respecting robots.txt and the content safety filter. |
| `fetch_image` | Download a public image into `/workspace/uploads/` so `python_execute` can embed it. |
| `python_execute` | Run arbitrary Python in the isolated sandbox. Full stdlib, pip-installable packages, file I/O. |
| `image_generate` | Call a configured image model (Gemini Imagen, OpenAI DALL-E, etc.) and save the result as an artifact. |
| `search_knowledge_base` | Hybrid dense + BM25 retrieval from any attached knowledge base, with reciprocal-rank fusion. |
| `save_memory` | Persist a fact into the user's memory store for injection in future conversations. |
| `use_skill` | Execute a pre-saved admin-defined skill (prompt + asset bundle). |

All tools stream their progress over SSE so the user sees what's happening in real time, not just the final answer.

### Interleaved tool calls — the full picture

The orchestrator drives an inner loop with up to **48 tool calls per turn** across **12 provider cycles** (6 for prompt-mode models). Between cycles, every tool result — including artifacts produced by `image_generate`, files downloaded by `fetch_image`, data written to `/workspace/` by earlier `python_execute` calls — is available to the next tool call.

The automatic file staging mechanism: before every `python_execute` call, the tool runner stages all of the following into `/workspace/uploads/`:

1. Every file the user uploaded in this conversation (spreadsheets, CSVs, PDFs, images — up to 20 MB each).
2. Every image artifact produced by `image_generate` in this conversation.
3. Every skill asset the user has access to, at `/workspace/skills/<name>/`.

This means a workflow like:

```
User: "Analyse data.csv, generate a bar chart, then produce a slide deck with the chart."

Turn 1
  python_execute: read /workspace/uploads/data.csv → produce analysis
  image_generate: render bar chart → artifact saved
  python_execute: /workspace/uploads/chart.png already there → python-pptx builds slide.pptx
  → artifact: slide.pptx streamed to the browser
```

…works without any intermediate user step. The model drives it end to end.

Per-turn tool budget caps (to bound cost and prevent abuse):

| Tool | Cap per turn | Deep Research |
|------|-------------|---------------|
| `web_search` | 16 | 40 |
| `web_fetch` | 12 | 25 |
| `fetch_image` | 16 | 12 |
| `image_generate` | 8 | 4 |
| `python_execute` | 16 | 8 |
| **Total (all tools)** | **48** | **150** |

### Python sandbox

The sandbox is a sidecar HTTP service (`SANDBOX_BASE_URL`). When configured:

- Each conversation gets a persistent session ID stored in the database. The sandbox container for that session stays alive across multiple turns.
- If the upstream reaper kills an idle container, `python_execute` detects the 404, provisions a fresh session, re-stages all files, and retries — transparently, without surfacing an error to the user.
- Code outputs (stdout, stderr, exceptions) stream back in real time.
- Files written to `/workspace/output/` become artifacts and appear as download cards in the chat.
- Admin sandbox inspector (`/admin/sandbox`) lets you browse and clear a conversation's workspace files.

Without a sandbox URL, `python_execute` runs in a safe-mode that evaluates only simple arithmetic — useful for development and demonstration.

### Conversation tree and branching

Every user edit and every regeneration creates a sibling node in the tree rather than overwriting history. You can:

- Navigate branches with `< N/M >` controls on any assistant message.
- Delete a turn (the round that contains the user question **and** all its assistant answers) safely — the deletion is branch-aware: other branches and all later messages on any branch are untouched.
- Regenerate with a different model — the branch comparison shows both answers side-by-side.

The database stores the full tree. The orchestrator walks only the active path when sending context to the model, compressing distant history into summary blocks at a configurable water-mark (`keep_recent_rounds`).

### RAG and document QA

- **Hierarchical chunking**: small-to-big, ~12% overlap, structure-aware (code blocks / tables / math are never split).
- **Heading breadcrumbs** prefixed to every chunk so the model always knows where a snippet came from.
- **Hybrid retrieval**: Qdrant dense vectors + Postgres BM25 full-text, fused with Reciprocal Rank Fusion.
- **Query routing**: a task LLM classifies each query as `retrieve`, `full_doc`, or `none` before any retrieval, and rewrites the query for multi-vector search.
- **MinerU integration**: scanned PDFs / DOCX / PPTX / XLSX / images run through MinerU's cloud OCR API. Source files land in your own S3 / Aliyun OSS bucket; MinerU only receives a presigned URL.

### Deep Research mode

A separate multi-round engine (`deep_research.go`): the model generates a research plan, fans out up to 40 web searches and 25 page fetches in parallel, verifies claims, and composes a cited final report. The tool budgets are raised specifically for this mode.

### Memory management

After each conversation turn, a background worker uses a task LLM to extract candidate memories. Memories are slotted into a fixed-size store (Tier 0), with ACTIVE vs QUERY_DEPENDENT tags. ACTIVE memories are injected into every system prompt; QUERY_DEPENDENT ones are resolved in-context. Users manage their memory store at `/memory`.

### Knowledge bases

Multiple knowledge bases per account. Each KB supports file upload (text / PDF / DOCX / XLSX / images), status tracking (pending → parsing → embedding → ready), and per-KB document management. KB retrieval is available as a tool inside any conversation that has the KB attached.

### Admin backend

| Section | What you manage |
|---------|----------------|
| Channels | Provider base URL + API key per channel; multiple channels of the same type allowed. |
| Models | Enable/disable, display name, context window, param_controls (per-model UI knobs like "Deep Thinking"), tags, image model fallback chain. |
| Model Tags | Admin-managed labels (e.g. "Fast", "Vision", "Coding"). Assigned to models; rendered as filter chips at the top of the model picker. |
| Skills | Prompt templates + asset bundles callable via the `use_skill` tool. |
| Users | Role, group/tier assignment, real-time ban via JWT token-version bump. |
| Usage | Per-user, per-model, per-purpose (chat / task / image / embedding) usage reports. |
| Settings | Sandbox URL/key, S3/OSS credentials, SearXNG URL, upload allowlist, disabled tools, compaction settings — all live-reloaded. |
| Sandbox Inspector | Browse and clear files in a conversation's sandbox workspace. |

### User groups and tiers

Every user belongs to a group (managed by admins). The group name appears in the sidebar instead of a generic role badge. Groups can carry feature flags that unlock capabilities such as extra tool access or higher context limits.

### Model picker with tag filtering

The model picker shows filter chips for every tag defined in Admin → Model Tags. Selecting a tag narrows the list to only models carrying that tag. Selecting "All" shows every enabled model. Tags are ordered and named by the admin, so you can create meaningful categories like "Fast", "Multimodal", "Reasoning".

### First-run setup

On first launch there are no accounts. Instead of seeding credentials from environment variables (a security anti-pattern), Aurelia shows a setup screen that asks for a name, email, and password. The first account you create becomes the administrator. Subsequent registrations go through the normal flow.

### Internationalization

Full UI in 5 locales: English, Simplified Chinese, Traditional Chinese, Japanese, French. Every page, dialog, toast, and error message is translated — including the admin backend and legal pages.

### File upload safety

- Per-user storage directory (path traversal prevented at multiple layers).
- Last-extension allowlist — `report.pdf.exe` is judged as `.exe`, not `.pdf`.
- NUL and control-character rejection.
- Admin-configurable MIME type allowlist with a safe default that excludes executables, archives, and HTML/SVG.
- Size cap enforced before the file is written to disk.

---

## Quick start (Docker, recommended)

Requires Docker 24+ with the Compose plugin.

```bash
# 1. Clone (you only need the deploy/ folder)
git clone https://github.com/hjxwz123/Aurelia.git
cd Aurelia/deploy

# 2. Fill in secrets
cp .env.example .env
$EDITOR .env             # set POSTGRES_PASSWORD, REDIS_PASSWORD, JWT_SECRET

# 3. Pull prebuilt images + start the stack
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

The app is now on `http://localhost` (or whatever `WEB_PORT` is set to).

**First run**: the setup screen appears — enter a name, email, and password. That account becomes the administrator. Then go to `/admin/channels` to add your first provider key and create a model.

Five containers come up:

| Container  | Image                                   | Role |
| ---------- | --------------------------------------- | ---- |
| `postgres` | `postgres:16-alpine`                    | Users, conversations, KBs, settings, usage. |
| `redis`    | `redis:7-alpine`                        | Cache, rate-limit counters, kill-signal pub/sub. |
| `qdrant`   | `qdrant/qdrant:v1.12.4`                 | Vector search for RAG. |
| `api`      | `ghcr.io/hjxwz123/aurelia-api:latest`   | Go HTTP server (`/api/*`). |
| `web`      | `ghcr.io/hjxwz123/aurelia-web:latest`   | nginx serving the SPA + `/api` reverse proxy. |

**Data persistence**: Postgres / Redis / Qdrant data live in named Docker volumes (`pgdata`, `redisdata`, `qdrantdata`). Uploads and generated artifacts are bind-mounted from a host directory (`DATA_DIR`, default `./data`) — files land directly on the host filesystem, visible and backup-able without entering a container. Back the named volumes and `DATA_DIR` together to keep the database rows, vectors, and on-disk files consistent.

---

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
                  ┌────────────────▼─────────────────────────────┐
                  │      Go API (api container)                  │
                  │  REST + SSE orchestrator                      │
                  │  ┌──────────────────────────────────────┐    │
                  │  │ Provider registry                    │    │
                  │  │  Anthropic · OpenAI · Gemini · Mock  │    │
                  │  └──────────────────────────────────────┘    │
                  │  ┌──────────────────────────────────────┐    │
                  │  │ Tool layer (48 calls/turn)           │    │
                  │  │  web_search · web_fetch · fetch_image│    │
                  │  │  python_execute · image_generate     │    │
                  │  │  search_knowledge_base · save_memory │    │
                  │  │  use_skill                           │    │
                  │  └──────────────────────────────────────┘    │
                  │  ┌────────────────┐  ┌────────────────┐      │
                  │  │ Task LLM       │  │ Memory worker  │      │
                  │  │ (title/router  │  │ (async extract │      │
                  │  │  /compact/etc) │  │  per turn)     │      │
                  │  └────────────────┘  └────────────────┘      │
                  └─┬──────────┬──────────┬──────────┬───────────┘
                    │          │          │          │
            ┌───────▼──┐  ┌────▼────┐  ┌──▼─────┐  ┌▼──────────────┐
            │ Postgres │  │  Redis  │  │ Qdrant │  │ Sandbox sidecar│
            │  schema  │  │  cache  │  │ vectors│  │ (Python, files)│
            │  + audit │  │  + pub  │  │ (RAG)  │  └───────────────┘
            └──────────┘  └─────────┘  └────────┘

  Optional external services (configured from admin UI, live-reload):
   • MinerU cloud (document parsing for scanned PDFs / DOCX / PPTX)
   • S3 / Aliyun OSS (upload bucket for MinerU + workspace archives)
   • SearXNG (self-hosted web search)
   • Real LLM providers (channel keys live in the DB, not in env)
```

---

## Configuration

Most of Aurelia is configured **at runtime from the admin UI**, not from environment variables. Provider keys, MinerU token, S3 credentials, SearXNG URL, upload allowlist, disabled tools — all admin-editable, applying on the next request with no restart.

The env vars in [`deploy/.env.example`](./deploy/.env.example) are only the boot-time essentials:

| Group | Keys | Purpose |
| ----- | ---- | ------- |
| **Image** | `IMAGE_OWNER`, `IMAGE_TAG` | Which GHCR namespace / tag to pull |
| **Network** | `WEB_PORT`, `PUBLIC_ORIGIN` | nginx host port + CORS allow-list |
| **Postgres** | `POSTGRES_USER/PASSWORD/DB` | Database credentials |
| **Redis** | `REDIS_PASSWORD` | Required (used as `requirepass`) |
| **Qdrant** | `QDRANT_API_KEY` | Optional API key |
| **Auth** | `JWT_SECRET` | Required; ≥32 chars, refuses the dev default in production |
| **Data** | `DATA_DIR` | Host directory bind-mounted at `/app/data` for uploads + artifacts (default `./data`) |
| **Boot fallbacks** | `SEARCH_*`, `EMBEDDING_*`, `MINERU_*` | Used only when the matching admin setting is absent |
| **Sandbox** | `SANDBOX_BASE_URL`, `SANDBOX_API_KEY` | Connect to the Python sandbox sidecar (optional; safe-mode without it) |

> No admin account is seeded from environment variables. The first-run setup screen creates the administrator.

---

## Build images yourself

```bash
cd deploy
cp .env.example .env
docker compose -f docker-compose.prod.yml up -d --build
```

Both `api` and `web` services declare both `image:` and `build:`, so Compose uses the prebuilt image when present and falls back to a local build.

---

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

Open `http://localhost:5173`. The first launch shows the setup screen — the first account you create (name + email + password) becomes the administrator. Add a channel + model in `/admin` to start chatting.

---

## GitHub Actions: automatic image builds

| Workflow | Trigger | Output |
| -------- | ------- | ------ |
| **`docker-images.yml`** | push to `main`, `v*.*.*` tags, manual dispatch | `ghcr.io/<owner>/aurelia-api`, `ghcr.io/<owner>/aurelia-web` — multi-arch (amd64 + arm64) |

Tagging strategy:

- Push to `main`    → `:latest` + `:sha-<short>`
- Push tag `v1.2.3` → `:1.2.3` + `:1.2` + `:1` + `:latest`
- Pull request      → built as a smoke test, NOT pushed

The workflow only needs `GITHUB_TOKEN` (auto-provided). After your first successful run, the packages appear under your repo's "Packages" sidebar. If you forked, update `IMAGE_OWNER` in `deploy/.env` to your lowercased GitHub username.

---

## Tech stack

- **Frontend**: React 19, TypeScript 5, Vite 5, Tailwind 4, Radix UI, Zustand, i18next, lucide-react
- **Backend**: Go 1.22, standard `net/http`, hand-rolled sqlc-style queries
- **Storage**: PostgreSQL 16 (production) / SQLite (embedded development fallback)
- **Cache & coordination**: Redis 7
- **Vector search**: Qdrant 1.12 (with brute-force Postgres cosine fallback)
- **Document parsing**: MinerU cloud API (PDF / DOCX / PPTX / images via OCR)
- **Optional**: S3 / Aliyun OSS for source-file hosting, SearXNG for self-hosted web search

---

## Project layout

```
.
├── src/                      React SPA (chat, admin, KB, memory, projects)
├── server/                   Go API
│   ├── cmd/api/              main entrypoint
│   └── internal/
│       ├── api/              HTTP handlers, router, upload safety
│       ├── llm/              Provider adapters + orchestrator + task LLM + memory worker
│       ├── tools/            web_search, web_fetch, fetch_image, python_execute,
│       │                     image_generate, search_knowledge_base, save_memory, use_skill
│       ├── rag/              parse → chunk → embed → query-route → retrieve
│       ├── vector/           Qdrant client (+ PG fallback)
│       ├── store/            Postgres / SQLite schema + queries
│       ├── sandbox/          HTTP client to the Python sandbox sidecar
│       └── storage/          HTTP client for S3 / OSS upload-presign
├── deploy/                   Production stack
│   ├── docker-compose.prod.yml
│   ├── Dockerfile.server     multi-stage Go build → debian-slim
│   ├── Dockerfile.web        Vite build → nginx
│   ├── nginx.conf            SPA + /api reverse proxy (SSE-aware)
│   └── .env.example          template
├── docs/                     specs, design notes
└── .github/workflows/        CI for building images
```

---

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

---

## License

[MIT](./LICENSE) — do whatever you want, including commercial use; just keep the copyright notice.

---

## Acknowledgements

- [Anthropic](https://www.anthropic.com), [OpenAI](https://openai.com), [Google](https://ai.google.dev) for the model APIs.
- [MinerU](https://mineru.net) for the document parsing API.
- [Qdrant](https://qdrant.tech) for the vector database.
- [SearXNG](https://github.com/searxng/searxng) for the self-hostable metasearch engine.
- The Radix UI / shadcn ecosystem for the headless primitive set Aurelia rethemes on top of.
