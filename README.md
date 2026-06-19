# Aurelia

<p align="center">
  <img src="docs/screenshots/hero.png" alt="Aurelia — multi-model AI chat platform" width="100%">
</p>

<p align="center">
  <strong>A self-hosted AI chat platform that actually competes with the hosted products.</strong><br>
  Multi-model · Interleaved tool calls · Persistent Python sandbox · RAG · Deep Research · Full admin backend.
</p>

<p align="center">
  <a href="./README.md"><strong>English</strong></a> ·
  <a href="./README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <a href="https://github.com/hjxwz123/Aurelia/actions/workflows/docker-images.yml"><img alt="Build" src="https://github.com/hjxwz123/Aurelia/actions/workflows/docker-images.yml/badge.svg"></a>
  <a href="https://github.com/hjxwz123/Aurelia/pkgs/container/aurelia-api"><img alt="API image" src="https://img.shields.io/badge/ghcr.io-aurelia--api-blue?logo=docker"></a>
  <a href="https://github.com/hjxwz123/Aurelia/pkgs/container/aurelia-web"><img alt="Web image" src="https://img.shields.io/badge/ghcr.io-aurelia--web-blue?logo=docker"></a>
  <img alt="Go 1.22" src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go">
  <img alt="React 19" src="https://img.shields.io/badge/React-19-61DAFB?logo=react">
  <img alt="TypeScript 5" src="https://img.shields.io/badge/TypeScript-5-3178C6?logo=typescript">
  <a href="./LICENSE"><img alt="Apache 2.0" src="https://img.shields.io/badge/license-Apache%202.0-blue"></a>
</p>

---

## Why Aurelia

Most self-hosted AI frontends are thin proxies — one model, one message, one response. Aurelia is built as a production platform: the same depth you expect from Claude.ai or ChatGPT, with the control of running it yourself.

---

## Tool calls & Python sandbox

This is where Aurelia diverges most from the rest. The orchestrator runs **up to 48 tool calls across 12 provider cycles in a single turn**. Tools chain freely — the output of one call becomes the input for the next, with no manual handoff required.

### Multi-step pipeline in one turn

<p align="center">
  <img src="docs/screenshots/tool-calls-1.jpg" alt="Tool call pipeline: two web searches, Python analysis, web page fetch, Python PPT generation — all in one user message" width="100%">
</p>

The screenshot above shows a single user prompt — *"Retrieve global GDP data for 2025 and generate a PowerPoint presentation"* — triggering a five-step pipeline without any user intervention:

1. `web_search` → IMF World Economic Outlook 2025 nominal GDP by country
2. `web_search` → current IMF GDP projections global total
3. `python_execute` → load and clean the data
4. `web_fetch` → IMF external data page for the top-10 table
5. `python_execute` → build a polished slide deck with python-pptx

<p align="center">
  <img src="docs/screenshots/tool-calls-2.jpg" alt="Result: PowerPoint and CSV artifacts ready to download, bar chart rendered inline, 10 sources cited" width="100%">
</p>

The result: a complete presentation and a data CSV, both available as download cards, with a bar chart rendered inline and 10 sources cited. No intermediate prompts. No "please attach the file." The model drives the whole pipeline.

### How it works

The orchestrator loops through provider cycles until the model stops emitting tool calls or the per-turn budget is reached. Within each cycle, independent tool calls run in parallel; results are fed back as a batch. Files written to `/workspace/` in one call are immediately available to the next:

```
web_search  ─┐
web_search  ─┤→ python_execute (clean data) → web_fetch → python_execute (build .pptx)
             └─ (results merged as one batch)
```

Because the sandbox shares a filesystem session across all calls in the conversation, data from a previous turn is still there when the model comes back minutes later.

### Persistent Python sandbox

Every conversation has its own isolated sandbox container. Files persist between turns — the model can write a CSV in turn 1 and reference it in turn 5. If the sidecar is reaped (container restart, deploy), the runner detects the 404, provisions a fresh session, re-stages all uploaded files, and retries transparently.

- Full Python standard library + pip-installable packages (pandas, matplotlib, python-pptx, …)
- `stdout` / `stderr` stream line-by-line while the code runs — you see progress, not just results
- Exceptions appear inline with the traceback
- Files written to `/workspace/output/` surface as download cards at the end of the message
- Admins can browse and clear any user's sandbox workspace from the inspector panel

### Run code in the browser too

Assistant-generated Python code blocks carry a **Run** button. Click it and Pyodide (CPython compiled to WebAssembly) executes in a Web Worker — the main thread never blocks. `matplotlib` charts render as inline PNGs; the last expression's `repr()` appears below the block.

HTML code blocks open a **live preview panel** alongside the chat as the assistant types — iframe-sandboxed, no same-origin access. Zero backend, zero cost per run.

### Available tools

| Tool | What it does |
|------|--------------|
| `web_search` | Full-text web search via SearXNG (self-hosted) or Serper / Brave |
| `web_fetch` | Fetch and extract a URL — respects robots.txt |
| `fetch_image` | Download a public image into `/workspace/uploads/` for subsequent Python use |
| `python_execute` | Run Python in the persistent sandbox; full stdlib, packages, real file I/O |
| `image_generate` | Call a configured image model and save the result as an artifact |
| `search_knowledge_base` | Hybrid dense + BM25 retrieval with RRF from any attached knowledge base |
| `save_memory` | Persist a user fact for injection in future conversations |
| `use_skill` | Execute an admin-defined skill (prompt + asset bundle) |

Per-turn budget:

| Tool | Standard | Deep Research |
|------|----------|---------------|
| `web_search` | 16 | 40 |
| `web_fetch` | 12 | 25 |
| `fetch_image` | 16 | 12 |
| `image_generate` | 8 | 4 |
| `python_execute` | 16 | 8 |
| **Total calls** | **48** | **150** |

---

## Other features

### Conversation branching

Every edit and every regeneration opens a sibling branch — no history is ever overwritten. Navigate branches with `‹ N/M ›` controls on any message, or fork any branch into a new conversation.

The **Conversation Outline** panel (top-right toolbar) gives a bird's-eye view of the whole thread: numbered user questions, truncated to two lines. Click any item to jump there instantly. The panel floats, is draggable, resizable, and zoomable.

### Deep Research mode

A separate multi-round engine: the model generates a research plan, fans out up to **40 web searches and 25 page reads in parallel**, verifies claims across sources, and composes a cited final report. Progress streams live as the plan unfolds — you see what's being searched, not just the finished answer.

### Document QA and RAG

- **Hierarchical chunking**: small-to-big, ~12% overlap, structure-aware (code / tables / math never split mid-block)
- **Heading breadcrumbs** on every chunk so the model always knows context
- **Hybrid retrieval**: Qdrant dense vectors + PostgreSQL BM25, fused with Reciprocal Rank Fusion
- **Query routing**: a task LLM classifies each query — `retrieve`, `full_doc`, or `none` — before retrieval
- **MinerU integration**: scanned PDFs, DOCX, PPTX, XLSX, and images run through cloud OCR; your files stay in your own S3 / Aliyun OSS bucket

### Memory across conversations

After each turn, a background worker extracts durable facts about the user ("prefers terse answers", "based in Tokyo"). On the next conversation, relevant memories are injected into the system prompt automatically. Users manage their own memory store at `/settings/memory`. Extraction runs async — it never slows the response.

### Thinking / chain-of-thought

Native reasoning support for Claude (extended thinking) and Gemini (chain-of-thought). Thinking blocks stream in a collapsible panel above the response so you can follow the model's reasoning without it cluttering the conversation.

---

## Admin backend

<p align="center">
  <img src="docs/screenshots/admin.jpg" alt="Admin backend: model catalogue with per-model pricing, toggle, and settings" width="100%">
</p>

| Section | What you manage |
|---------|-----------------|
| Channels | Provider URL + API key per channel. Multiple channels of the same provider type are allowed. |
| Models | Enable / disable, display name, context window, per-model knobs, image fallback chain, in/out pricing. |
| Model Tags | Admin-defined labels ("Fast", "Vision", "Coding") shown as filter chips in the model picker. |
| User Groups | Named tiers with feature flags — extra tools, higher context, custom branding, group quotas. |
| Users | Role assignment, real-time ban (JWT token-version bump), conversation drill-down (read-only). |
| Skills | Prompt templates + asset bundles callable via `use_skill`. |
| Usage | Per-user, per-model, per-purpose (chat / task / image / embedding) usage and cost reports. |
| Knowledge Bases | Per-account KBs with document management and embedding status. |
| Settings | Sandbox, S3/OSS, SearXNG, upload allowlist, MinerU, compaction — all live-reloaded, no restart. |
| Backup | Full logical backup (every table as JSONL in a ZIP, optional bundled files). SQLite ↔ PostgreSQL portable. |
| Sandbox Inspector | Browse and clear files in any user's sandbox workspace. |

Every setting takes effect on the next request — no restart, no SSH.

---

## Quick start

Requires Docker 24+ with the Compose plugin.

```bash
# 1. Clone
git clone https://github.com/hjxwz123/Aurelia.git
cd Aurelia/deploy

# 2. Fill in secrets
cp .env.example .env
$EDITOR .env   # set POSTGRES_PASSWORD, REDIS_PASSWORD, JWT_SECRET

# 3. Pull prebuilt images and start
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

Open `http://localhost`. The setup screen appears on first launch — the first account you create becomes the administrator. Go to `/admin/channels` to add a provider key and create a model.

Five containers come up:

| Container | Image | Role |
|-----------|-------|------|
| `postgres` | `postgres:16-alpine` | Users, conversations, KBs, settings, usage |
| `redis` | `redis:7-alpine` | Cache, rate limits, kill-signal pub/sub |
| `qdrant` | `qdrant/qdrant:v1.12.4` | Vector search for RAG |
| `api` | `ghcr.io/hjxwz123/aurelia-api:latest` | Go HTTP + SSE server |
| `web` | `ghcr.io/hjxwz123/aurelia-web:latest` | nginx — SPA + `/api` reverse proxy |

Postgres / Redis / Qdrant use named volumes (`pgdata`, `redisdata`, `qdrantdata`). Uploads and artifacts are bind-mounted from `DATA_DIR` (default `./data`) — files land directly on the host, no container access needed.

---

## Local development

The Go API ships with an embedded SQLite driver and a brute-force cosine embedder, so everything runs without external services for development.

```bash
# Backend
cd server
go run ./cmd/api          # listens on :8787

# Frontend (separate terminal)
cd ..
npm install
npm run dev               # Vite at :5173, proxies /api to :8787
```

Open `http://localhost:5173`. First launch shows the setup screen.

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
                             │
            ┌────────────────▼───────────────────────────┐
            │  Go API — REST + SSE orchestrator           │
            │  ┌──────────────────────────────────────┐   │
            │  │ Provider registry                    │   │
            │  │  Anthropic · OpenAI · Gemini · Mock  │   │
            │  └──────────────────────────────────────┘   │
            │  ┌──────────────────────────────────────┐   │
            │  │ Tool layer  (up to 48 calls / turn)  │   │
            │  │  web_search · web_fetch · fetch_image│   │
            │  │  python_execute · image_generate     │   │
            │  │  search_knowledge_base · save_memory │   │
            │  │  use_skill                           │   │
            │  └──────────────────────────────────────┘   │
            │  ┌────────────────┐  ┌────────────────┐     │
            │  │ Task LLM       │  │ Memory worker  │     │
            │  │ (title/router/ │  │ (async extract │     │
            │  │  compact/etc)  │  │  per turn)     │     │
            │  └────────────────┘  └────────────────┘     │
            └──┬──────────┬──────────┬──────────┬─────────┘
               │          │          │          │
       ┌───────▼──┐  ┌────▼────┐  ┌──▼─────┐  ┌▼──────────────┐
       │ Postgres │  │  Redis  │  │ Qdrant │  │ Sandbox sidecar│
       │ + SQLite │  │  cache  │  │ vectors│  │ (Python, files)│
       └──────────┘  └─────────┘  └────────┘  └───────────────┘

  Optional (admin-configured, live-reload, no restart required):
   MinerU cloud · S3 / Aliyun OSS · SearXNG · Any OpenAI-compatible provider
```

---

## Configuration

Most of Aurelia is configured from the admin UI at runtime — provider keys, MinerU token, S3 credentials, SearXNG URL, upload allowlist, disabled tools, compaction settings. All apply on the next request, no restart needed.

The env file only holds boot-time essentials:

| Group | Keys | Purpose |
|-------|------|---------|
| **Image** | `IMAGE_OWNER`, `IMAGE_TAG` | GHCR namespace / tag to pull |
| **Network** | `WEB_PORT`, `PUBLIC_ORIGIN` | nginx host port + CORS allowlist |
| **Postgres** | `POSTGRES_USER/PASSWORD/DB` | Database credentials |
| **Redis** | `REDIS_PASSWORD` | Cache auth |
| **Auth** | `JWT_SECRET` | Required; ≥ 32 chars |
| **Data** | `DATA_DIR` | Host directory for uploads + artifacts (default `./data`) |
| **Sandbox** | `SANDBOX_BASE_URL`, `SANDBOX_API_KEY` | Python sandbox sidecar (optional) |
| **Boot fallbacks** | `SEARCH_*`, `EMBEDDING_*`, `MINERU_*` | Used when the matching admin setting is absent |

---

## Tech stack

- **Frontend**: React 19, TypeScript 5, Vite 5, Tailwind 4, Radix UI, Zustand, i18next, lucide-react
- **Backend**: Go 1.22, standard `net/http`, hand-rolled typed queries
- **Storage**: PostgreSQL 16 (production) / SQLite (embedded dev fallback)
- **Cache & coordination**: Redis 7
- **Vector search**: Qdrant 1.12 (brute-force PostgreSQL cosine fallback for dev)
- **Document parsing**: MinerU cloud API (PDF / DOCX / PPTX / images via OCR)
- **Internationalization**: 5 locales — English, Simplified Chinese, Traditional Chinese, Japanese, French

---

## Project layout

```
.
├── src/                      React SPA
│   ├── pages/                chat · admin · kb · memory · projects · settings
│   ├── components/           chat primitives, UI system, sidebar
│   ├── store/                Zustand stores (conversations, models, UI, …)
│   └── styles/               Tailwind tokens + global CSS
├── server/                   Go API
│   ├── cmd/api/              main entrypoint
│   └── internal/
│       ├── api/              HTTP handlers, router, upload safety
│       ├── llm/              Provider adapters + orchestrator + task LLM + memory worker
│       ├── tools/            All 8 built-in tools
│       ├── rag/              parse → chunk → embed → query-route → retrieve
│       ├── vector/           Qdrant client (+ PG fallback)
│       ├── store/            Schema + typed queries (SQLite / PostgreSQL)
│       ├── sandbox/          HTTP client for the Python sandbox sidecar
│       └── storage/          S3 / OSS presign client
├── deploy/                   Production Docker stack
│   ├── docker-compose.prod.yml
│   ├── Dockerfile.server
│   ├── Dockerfile.web
│   └── .env.example
└── docs/screenshots/         Screenshots referenced in this README
```

---

## GitHub Actions

| Workflow | Trigger | Output |
|----------|---------|--------|
| `docker-images.yml` | push to `main`, `v*.*.*` tags, manual dispatch | `ghcr.io/<owner>/aurelia-api` and `aurelia-web` — multi-arch (amd64 + arm64) |

- `main` → `:latest` + `:sha-<short>`
- `v1.2.3` → `:1.2.3` + `:1.2` + `:1` + `:latest`

The workflow only needs `GITHUB_TOKEN`.

---

## Contributing

Open an issue first for non-trivial changes. Before submitting a PR:

```bash
# Frontend
npm run lint && npm run typecheck && npm run build

# Backend
cd server && go vet ./... && go build ./...
```

---

## License

[Apache 2.0](./LICENSE) — you may use, modify, and distribute this software, including in proprietary/closed-source products, provided you retain the original copyright notice, include a copy of this license, and note any modifications you make.

---

## Acknowledgements

[Anthropic](https://www.anthropic.com) · [OpenAI](https://openai.com) · [Google](https://ai.google.dev) · [MinerU](https://mineru.net) · [Qdrant](https://qdrant.tech) · [SearXNG](https://github.com/searxng/searxng) · [Radix UI](https://www.radix-ui.com)
