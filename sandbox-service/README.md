# Aivory local Python sandbox (sidecar)

<p align="center">
  <a href="./README.md"><strong>English</strong></a> ·
  <a href="./README.zh-CN.md">简体中文</a>
</p>

A tiny, self-hosted Python execution sandbox for local development. It implements
the exact 3-endpoint HTTP protocol the Go backend already speaks
(`server/internal/sandbox/sandbox.go`), so wiring it up is just an env var.

It is **not** a from-scratch sandbox: each session is a locked-down Docker
container running a pre-baked Python image. `/workspace` persists across `exec`
calls within a session (like ChatGPT Code Interpreter), and Chinese renders
correctly because the image ships Noto CJK fonts.

```
┌────────────┐   POST /sessions /exec /files   ┌──────────────┐   docker exec   ┌──────────────────┐
│ Go backend │ ──────────────────────────────► │ app.py (this)│ ──────────────► │ session container │
└────────────┘   SANDBOX_BASE_URL              └──────────────┘                 │  aivory-sandbox  │
                                                                                 └──────────────────┘
```

## What's in the runtime image

- **Data science**: numpy, pandas, scipy, scikit-learn, statsmodels,
  matplotlib, seaborn, plotly (+ kaleido), pillow, sympy, networkx
- **Documents (§4.5.1)**: python-pptx, python-docx, openpyxl, xlsxwriter,
  reportlab, weasyprint (HTML→PDF), markdown, jinja2
- **Utilities**: pypdf, tabulate, requests, lxml, beautifulsoup4, pyyaml
- **Fonts**: Noto Sans CJK (SC/TC/JP/KR), Noto Color Emoji, DejaVu — matplotlib
  is pre-configured to use them, so no more tofu boxes (□□□) for Chinese.

Heavy extras (Playwright/Chromium for HTML screenshots, LibreOffice for format
conversion) are left commented at the bottom of `Dockerfile.runner` — uncomment
if you need them; they add ~400–500MB each.

## Deploy: pull the public images and run (recommended)

This project publishes ready-to-use images to GitHub Container Registry. Users
do **not** need to build images, create their own GitHub repo, or log in to
GHCR. The images are public:

```
ghcr.io/hjxwz123/aivory-sandbox:latest          # Python runtime
ghcr.io/hjxwz123/aivory-sandbox-sidecar:latest  # control service
```

**1. Clone the public repo on your server**

```bash
git clone https://github.com/hjxwz123/aivory-sandbox.git
cd aivory-sandbox
```

**2. Pull the public images**

```bash
export OWNER=hjxwz123

docker pull ghcr.io/$OWNER/aivory-sandbox:latest
docker pull ghcr.io/$OWNER/aivory-sandbox-sidecar:latest
docker images "ghcr.io/$OWNER/aivory-sandbox*"
```

**3. Generate and display the API key**

```bash
export SANDBOX_API_KEY=$(openssl rand -hex 24)
printf 'SANDBOX_API_KEY=%s\n' "$SANDBOX_API_KEY"
```

Keep this value. The sidecar requires it for requests, and the Go backend must
use the same key.

**4. Start the service**

```bash
docker compose pull
docker compose up -d
docker compose ps
curl -H "Authorization: Bearer $SANDBOX_API_KEY" http://localhost:48217/healthz
```

**5. Point the Go backend at it:**

```
SANDBOX_BASE_URL=http://<server-host>:48217
SANDBOX_API_KEY=<same value printed above>
```

That's the whole loop. Users pull and run the public images; they do not build
Docker images.

---

## 部署：拉取公开镜像并运行（推荐）

本项目已经把可直接使用的镜像发布到 GitHub Container Registry。使用者
不需要构建镜像，不需要创建自己的 GitHub 仓库，也不需要登录 GHCR。镜像
是公开的：

```
ghcr.io/hjxwz123/aivory-sandbox:latest          # Python 运行时镜像
ghcr.io/hjxwz123/aivory-sandbox-sidecar:latest  # 控制服务镜像
```

**1. 在服务器上克隆公开仓库**

```bash
git clone https://github.com/hjxwz123/aivory-sandbox.git
cd aivory-sandbox
```

**2. 拉取公开镜像**

```bash
export OWNER=hjxwz123

docker pull ghcr.io/$OWNER/aivory-sandbox:latest
docker pull ghcr.io/$OWNER/aivory-sandbox-sidecar:latest
docker images "ghcr.io/$OWNER/aivory-sandbox*"
```

**3. 生成并显示密钥**

```bash
export SANDBOX_API_KEY=$(openssl rand -hex 24)
printf 'SANDBOX_API_KEY=%s\n' "$SANDBOX_API_KEY"
```

请保存输出的值。sidecar 会用它校验请求，Go 后端也必须配置同一个密钥。

**4. 启动服务**

```bash
docker compose pull
docker compose up -d
docker compose ps
curl -H "Authorization: Bearer $SANDBOX_API_KEY" http://localhost:48217/healthz
```

**5. 配置 Go 后端**

```
SANDBOX_BASE_URL=http://<服务器地址>:48217
SANDBOX_API_KEY=<上一步打印出来的同一个值>
```

这样就完成了。使用者直接拉取并运行公开镜像，不需要构建 Docker 镜像。

---

## Optional: build locally for development or customization

Regular users should use the public images above. This section is only for
maintainers or developers who want to change the runtime image. It requires a
running Docker engine (Docker Desktop or Colima) and Python 3.10+.

```bash
cd aivory-sandbox

# 1. Build the runtime image (one-time, ~5–8 min, downloads the wheels + fonts)
docker build -f Dockerfile.runner -t aivory-sandbox:latest .

# 2. Install the sidecar deps and run it on :8000
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt

# Auth is mandatory — the sidecar refuses to start without a key. Either set one:
export SANDBOX_API_KEY=$(openssl rand -hex 24)
# …or, for a trusted localhost-only dev box, explicitly opt out of auth:
#   export SANDBOX_ALLOW_NO_AUTH=1
uvicorn app:app --host 127.0.0.1 --port 8000
```

Then set `SANDBOX_BASE_URL=http://127.0.0.1:8000` (and the same
`SANDBOX_API_KEY`) for the Go server.

## Smoke test (no backend needed)

```bash
export SANDBOX_URL=${SANDBOX_URL:-http://localhost:48217}

SID=$(curl -s -XPOST "$SANDBOX_URL/sessions" \
  -H "Authorization: Bearer $SANDBOX_API_KEY" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["session_id"])')

curl -s -XPOST "$SANDBOX_URL/exec" \
  -H "Authorization: Bearer $SANDBOX_API_KEY" \
  -H 'content-type: application/json' \
  -d "{
  \"session_id\": \"$SID\",
  \"code\": \"import matplotlib.pyplot as plt; plt.plot([1,2,3]); plt.title('中文标题'); plt.savefig('/workspace/outputs/p.png'); print('rows', 3)\"
}" | python3 -m json.tool
```

You should get `stdout: "rows 3\n"`, `exit_code: 0`, and one file `p.png`
(base64) in `files` — with the Chinese title rendered, not boxes.

## Configuration (env vars)

| Var | Default | Notes |
|---|---|---|
| `SANDBOX_IMAGE` | `aivory-sandbox:latest` | runtime image tag |
| `SANDBOX_NETWORK` | `none` | set `bridge` to allow `pip install` at runtime |
| `SANDBOX_MEMORY` | `2g` | per-container memory cap |
| `SANDBOX_CPUS` | `1` | per-container CPU cap |
| `SANDBOX_PIDS_LIMIT` | `256` | fork-bomb guard |
| `SANDBOX_API_KEY` | _(empty)_ | **Required.** Bearer key clients must send. The sidecar **refuses to start** if this is empty/blank (it exposes host Docker control — RCE). Constant-time compared. |
| `SANDBOX_ALLOW_NO_AUTH` | `0` | escape hatch: set `1` to start with **no auth** (trusted localhost-only dev only). |
| `SANDBOX_MAX_BODY_BYTES` | `58720256` | reject requests whose `Content-Length` exceeds this (HTTP 413) before reading the body. 56 MiB, enough for a 40 MiB upload after base64 and JSON overhead. |
| `SANDBOX_MAX_CODE_BYTES` | `1048576` | max bytes for the `/exec` `code` field (HTTP 413/422 over). 1 MiB. |
| `SANDBOX_EXEC_TIMEOUT_CAP_MS` | `120000` | hard ceiling per exec (§4.5) |
| `SANDBOX_IDLE_TTL_SECONDS` | `1800` | idle sessions reaped after 30 min (fallback when Go forwards no per-session `idle_ttl_sec`) |
| `SANDBOX_IDLE_TTL_CAP_SECONDS` | `86400` | hard ceiling for the admin-forwarded idle TTL (`idle_ttl_sec` clamped to this). 24h. |
| `SANDBOX_MAX_SESSIONS` | `16` | max active sandbox containers |
| `SANDBOX_MAX_CONCURRENT_EXECS` | `4` | max concurrent `/exec` calls across sessions |
| `SANDBOX_MAX_CONCURRENT_CREATES` | `2` | max concurrent Docker container creates |
| `SANDBOX_QUEUE_TIMEOUT_SECONDS` | `150` | how long a request waits for an internal slot |
| `SANDBOX_MAX_UPLOAD_BYTES` | `41943040` | max decoded size for one `/files` upload (40 MiB) |
| `SANDBOX_MAX_FILES_PER_EXEC` | `20` | max returned artifacts per `/exec` |
| `SANDBOX_MAX_TOTAL_ARTIFACT_BYTES` | `52428800` | max total returned artifact bytes per `/exec` |
| `SANDBOX_READ_ONLY_ROOTFS` | `1` | **on by default** (F6 disk-fill guard): session rootfs is read-only; only `/tmp`, `$HOME` and `/workspace` are writable size-bounded tmpfs. Set `0` to disable. |
| `SANDBOX_TMPFS_SIZE` | `256m` | tmpfs size for `/tmp` and `/home/sandbox` when read-only mode is on |
| `SANDBOX_WORKSPACE_SIZE` | `512m` | size cap for the writable `/workspace` tmpfs when read-only mode is on (alias `SANDBOX_WORKSPACE_TMPFS_SIZE` still honoured) |
| `SANDBOX_DISK_SIZE` | _(empty)_ | opt-in per-container writable-layer quota via `--storage-opt size=…`. Needs overlay2+prjquota; applied best-effort (retried without it if unsupported, never crashes). |
| `SANDBOX_SECCOMP_PROFILE` | _(empty)_ | opt-in path (readable inside the sidecar) to a seccomp profile pinned on session containers via `--security-opt seccomp=…`. Unset = docker's default profile. |
| `SANDBOX_NOFILE_ULIMIT` | `1024:1024` | per-container open-file ulimit |
| `SANDBOX_STORAGE_DEFAULT_TTL` | `3600` | default presigned-GET ttl (seconds) for `/storage/put` when `expires_in` is omitted |
| `SANDBOX_STORAGE_MAX_TTL` | `86400` | hard cap on the presigned-GET ttl (24h) |
| `SANDBOX_MAX_ARCHIVE_BYTES` | `209715200` | max `/workspace` tar size archived on reap/delete; larger workspaces skip archive (logged). 200 MiB. |
| `SANDBOX_MAX_STORAGE_BODY_BYTES` | `314572800` | body-size ceiling for `/storage/put` (RAG document uploads dwarf the F5 cap that guards `/exec`/`/files`). 300 MiB. |
| `SANDBOX_LOCAL_STORAGE_DIR` | _(empty)_ | directory for the `local` archive backend (§4.5-F). When set (mount it as a volume), the `local` storage provider archives `/workspace` tarballs here — zero-config persistence, no S3/OSS/MinIO needed. Empty = `local` is inert (reaped = gone). **Single-node only** (a plain volume isn't shared across replicas); MinerU document parsing still needs an object store (no presigned URL for local files). Operator env, never admin/forwarded — the sidecar is root+docker.sock, so a caller-chosen path would be a host-write vector. |

> **Storage providers.** The forwarded `storage.provider` is one of `local` (the default — archives to `SANDBOX_LOCAL_STORAGE_DIR`, no external store), `s3`, `aliyun_oss`, or empty. Workspace tarballs are keyed by the forwarded `archive_key` (the conversation id), so a workspace **survives session recycle** — each session gets a fresh id, but archive/restore use the stable key. **MinIO / any S3-compatible store**: pick `s3` and set `storage.s3_endpoint` — a custom endpoint auto-selects path-style addressing + SigV4, so it works with no extra flags.

## Security posture (dev-grade)

Each session container runs **non-root**, `--network none`, `--cap-drop ALL`,
`--security-opt no-new-privileges`, with memory/cpu/pids/nofile limits and a
120s exec timeout. By default the rootfs is **read-only** and only `/tmp`,
`$HOME` and `/workspace` are writable size-bounded tmpfs mounts, so a session
can't fill the host disk (set `SANDBOX_READ_ONLY_ROOTFS=0` to opt out; set
`SANDBOX_DISK_SIZE` on an overlay2+prjquota host for a writable-layer quota too).
You can pin a seccomp profile on session containers with
`SANDBOX_SECCOMP_PROFILE`. `/exec` calls are serialized per session, globally
rate-limited, and stdout/stderr are streamed through a 32KB cap before
returning. Produced files are capped at 20MB each, 20 files, and 50MB total per
exec — matching the §4.5 安全基线 while keeping the HTTP contract unchanged.

**Auth is mandatory.** This service drives the host Docker daemon, so an
unauthenticated reachable instance is host RCE. `SANDBOX_API_KEY` must be set or
the sidecar refuses to start (`SANDBOX_ALLOW_NO_AUTH=1` only for a trusted
localhost-only dev box). The key is compared in constant time. Request bodies
are size-capped before they're read (`SANDBOX_MAX_BODY_BYTES`, plus a 1 MiB cap
on `/exec` `code`) so a giant payload can't OOM the control plane. `/files`
uploads are confined to `/workspace`: the destination is normalised **and** its
real path is resolved inside the container (following any symlinked parent) and
re-checked, so a symlinked path can't escape `/workspace`.

**docker.sock is root-equivalent on the host.** The architecture needs it, but
in production put a [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy)
in front of the raw socket restricted to container create/start/exec/kill +
image pull (see the security note in `docker-compose.yml`), and never expose the
service port to the public internet.

On startup the sidecar also discovers existing `aivory.sandbox=1` containers,
so a sidecar restart can keep tracking live sessions and reap stale ones later.

This is container-level isolation, fine for a single-host dev box. It is **not**
gVisor/microVM-grade. For production, replace the `docker run`/`docker exec`
calls in `app.py` with a gVisor, Firecracker, or E2B backend — the HTTP contract
and the Go side stay identical (that's the whole point of the thin adapter).

## Endpoints

| Method | Path | Body | Returns |
|---|---|---|---|
| POST | `/sessions` | `{storage?, idle_ttl_sec?, archive_key?}` | `{session_id}` |
| POST | `/exec` | `{session_id, code, timeout_ms?}` | `{stdout, stderr, exit_code, files[]}` |
| POST | `/files` | `{session_id, path, data_base64}` | `{ok}` |
| POST | `/files/get` | `{session_id, path}` | `{data_base64}` |
| DELETE | `/sessions/{id}` | `{storage?, discard?, archive_key?}` | `{ok}` |
| POST | `/storage/put` | `{key, data_base64, content_type, expires_in?, storage}` | `{provider, key, url, expires_in}` |
| POST | `/storage/delete` | `{key, storage}` | `{ok, key}` |
| GET | `/healthz` | — | `{ok, docker, image}` |

`/files/get` reads a file back out of `/workspace` (same path-confinement and
symlink guard as `/files`). 404 when the session or file is missing.

`/storage/*` are object-storage operations against the admin-configured bucket
(forwarded in the `storage` block: `provider` ∈ `s3`|`aliyun_oss`, plus `prefix`
and the provider creds). `boto3`/`oss2` are lazy-imported only when a backend is
actually used. `/storage/put` uploads the base64 bytes and returns a presigned
GET URL (ttl `expires_in` or 1h, capped at 24h); `/storage/delete` is idempotent
and refuses to delete outside the configured `prefix`.

When a `storage` block is forwarded on `/sessions` and `DELETE /sessions/{id}`,
the sidecar archives `/workspace` to `<prefix>/workspaces/<session_id>.tgz`
before a TTL/explicit teardown and restores it on the next create with that id
(design §4.5). All best-effort: without an effective `storage` block this is a
no-op (reaped = gone), and an archive/restore failure never fails the request.

Artifacts in `files[]` are whatever the code wrote under `/workspace/outputs/`
during that exec (`{name, mime_type, data_base64}`). User uploads should be
written to `/workspace/uploads/` via `/files` — that's the path the
`python_execute` tool description tells the model about.
