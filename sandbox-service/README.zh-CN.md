# Aivory 本地 Python 沙箱（sidecar）

<p align="center">
  <a href="./README.md">English</a> ·
  <a href="./README.zh-CN.md"><strong>简体中文</strong></a>
</p>

这是一个小型自托管 Python 执行沙箱，主要用于本地开发。它实现了 Go 后端已经使用的 3 个核心 HTTP 端点协议（见 `server/internal/sandbox/sandbox.go`），因此接入只需要配置一个环境变量。

它**不是**从零手写的沙箱：每个 session 都是一个锁定后的 Docker 容器，运行预构建 Python 镜像。`/workspace` 会在同一个 session 的多次 `exec` 调用之间保留（类似 ChatGPT Code Interpreter），并且镜像内置 Noto CJK 字体，所以中文可以正常渲染。

```
┌────────────┐   POST /sessions /exec /files   ┌──────────────┐   docker exec   ┌──────────────────┐
│ Go 后端    │ ──────────────────────────────► │ app.py (本服务)│ ─────────────► │ session 容器      │
└────────────┘   SANDBOX_BASE_URL              └──────────────┘                 │  aivory-sandbox  │
                                                                                 └──────────────────┘
```

## 运行时镜像包含什么

- **数据科学**：numpy、pandas、scipy、scikit-learn、statsmodels、matplotlib、seaborn、plotly（含 kaleido）、pillow、sympy、networkx
- **文档处理（§4.5.1）**：python-pptx、python-docx、openpyxl、xlsxwriter、reportlab、weasyprint（HTML→PDF）、markdown、jinja2
- **工具类**：pypdf、tabulate、requests、lxml、beautifulsoup4、pyyaml
- **字体**：Noto Sans CJK（简中 / 繁中 / 日文 / 韩文）、Noto Color Emoji、DejaVu；matplotlib 已预配置使用这些字体，中文不会再显示成方框（□□□）。

较重的可选组件（用于 HTML 截图的 Playwright/Chromium、用于格式转换的 LibreOffice）保留在 `Dockerfile.runner` 底部注释中。需要时取消注释即可；每项大约会增加 400-500MB。

## 部署：拉取公开镜像并运行（推荐）

本项目已经把可直接使用的镜像发布到 GitHub Container Registry。使用者不需要构建镜像，不需要创建自己的 GitHub 仓库，也不需要登录 GHCR。镜像是公开的：

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

**3. 生成并显示 API key**

```bash
export SANDBOX_API_KEY=$(openssl rand -hex 24)
printf 'SANDBOX_API_KEY=%s\n' "$SANDBOX_API_KEY"
```

请保存输出值。sidecar 会用它校验请求，Go 后端也必须配置同一个 key。

**4. 启动服务**

```bash
docker compose pull
docker compose up -d
docker compose ps
curl -H "Authorization: Bearer $SANDBOX_API_KEY" http://localhost:48217/healthz
```

**5. 让 Go 后端指向它**

```
SANDBOX_BASE_URL=http://<服务器地址>:48217
SANDBOX_API_KEY=<上一步打印出来的同一个值>
```

这样就完成了。使用者直接拉取并运行公开镜像，不需要构建 Docker 镜像。

## 可选：本地构建用于开发或定制

普通用户应使用上面的公开镜像。本节只适合维护者，或需要修改运行时镜像的开发者。你需要一个可用的 Docker 引擎（Docker Desktop 或 Colima）以及 Python 3.10+。

```bash
cd aivory-sandbox

# 1. 构建运行时镜像（一次性，约 5-8 分钟，会下载 wheel 和字体）
docker build -f Dockerfile.runner -t aivory-sandbox:latest .

# 2. 安装 sidecar 依赖，并在 :8000 启动
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt

# 必须启用鉴权。没有 key 时 sidecar 会拒绝启动。可以设置一个 key：
export SANDBOX_API_KEY=$(openssl rand -hex 24)
# 或者，仅在可信的本机开发环境中，显式关闭鉴权：
#   export SANDBOX_ALLOW_NO_AUTH=1
uvicorn app:app --host 127.0.0.1 --port 8000
```

然后为 Go server 设置 `SANDBOX_BASE_URL=http://127.0.0.1:8000`，并使用同一个 `SANDBOX_API_KEY`。

## 冒烟测试（不需要后端）

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

预期结果：`stdout` 为 `"rows 3\n"`，`exit_code` 为 `0`，`files` 中包含一个 `p.png`（base64）。图片里的中文标题应正常显示，而不是方框。

## 配置（环境变量）

runner 网络隔离不是可配置项。即使 sidecar 自身接入 Compose 网络以供 Go 后端访问，每个新建 session 容器仍固定使用 Docker `--network none`。因此用户 Python 不能通过 `requests`、`curl` 或运行时 `pip install` 获取公网内容。需要新增依赖时，应修改 `Dockerfile.runner` 并重新构建 runner 镜像。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `SANDBOX_IMAGE` | `aivory-sandbox:latest` | 运行时镜像 tag |
| `SANDBOX_MEMORY` | `2g` | 单容器内存上限 |
| `SANDBOX_CPUS` | `1` | 单容器 CPU 上限 |
| `SANDBOX_PIDS_LIMIT` | `256` | 防 fork bomb |
| `SANDBOX_API_KEY` | _空_ | **必填。** 客户端必须发送的 Bearer key。为空时 sidecar 会拒绝启动（它暴露 Docker 控制能力，即 RCE）。使用常量时间比较。 |
| `SANDBOX_ALLOW_NO_AUTH` | `0` | 逃生开关：设为 `1` 表示无鉴权启动，仅限可信本机开发环境。 |
| `SANDBOX_MAX_BODY_BYTES` | `58720256` | 在读取 body 前拒绝超过该 `Content-Length` 的请求（HTTP 413）。默认 56 MiB，可容纳 40 MiB 文件的 base64 与 JSON 开销。 |
| `SANDBOX_MAX_CODE_BYTES` | `1048576` | `/exec` 的 `code` 字段最大字节数，超过返回 HTTP 413/422。1 MiB。 |
| `SANDBOX_EXEC_TIMEOUT_CAP_MS` | `120000` | 单次执行硬上限（§4.5） |
| `SANDBOX_IDLE_TTL_SECONDS` | `1800` | 空闲 session 默认 30 分钟后回收（Go 未按会话下发 `idle_ttl_sec` 时的兜底值） |
| `SANDBOX_IDLE_TTL_CAP_SECONDS` | `86400` | 管理员下发的回收窗口（`idle_ttl_sec`）硬上限，24h |
| `SANDBOX_MAX_SESSIONS` | `16` | 最大活跃沙箱容器数 |
| `SANDBOX_MAX_CONCURRENT_EXECS` | `4` | 所有 session 合计最大并发 `/exec` 数 |
| `SANDBOX_MAX_CONCURRENT_CREATES` | `2` | 最大并发 Docker 容器创建数 |
| `SANDBOX_QUEUE_TIMEOUT_SECONDS` | `150` | 请求等待内部执行槽位的最长时间 |
| `SANDBOX_MAX_UPLOAD_BYTES` | `41943040` | 单个 `/files` 上传解码后的最大大小（40 MiB） |
| `SANDBOX_MAX_FILES_PER_EXEC` | `20` | 单次 `/exec` 返回的最大产物数量 |
| `SANDBOX_MAX_TOTAL_ARTIFACT_BYTES` | `52428800` | 单次 `/exec` 返回产物总大小上限 |
| `SANDBOX_READ_ONLY_ROOTFS` | `1` | **默认开启**（F6 磁盘填满防护）：session rootfs 只读，只有 `/tmp`、`$HOME`、`/workspace` 是有大小限制的可写 tmpfs。设为 `0` 可关闭。 |
| `SANDBOX_TMPFS_SIZE` | `256m` | 只读模式下 `/tmp` 和 `/home/sandbox` 的 tmpfs 大小 |
| `SANDBOX_WORKSPACE_SIZE` | `512m` | 只读模式下 `/workspace` tmpfs 大小（仍兼容别名 `SANDBOX_WORKSPACE_TMPFS_SIZE`） |
| `SANDBOX_DISK_SIZE` | _空_ | 可选的每容器 writable-layer 配额，通过 `--storage-opt size=...` 设置。需要 overlay2+prjquota；best-effort 应用，不支持时会不带该选项重试，不会崩溃。 |
| `SANDBOX_SECCOMP_PROFILE` | _空_ | 可选 seccomp profile 路径（sidecar 内可读），通过 `--security-opt seccomp=...` 固定到 session 容器。未设置时使用 Docker 默认 profile。 |
| `SANDBOX_NOFILE_ULIMIT` | `1024:1024` | 单容器 open-file ulimit |
| `SANDBOX_STORAGE_DEFAULT_TTL` | `3600` | `/storage/put` 未传 `expires_in` 时预签名 GET URL 的默认 ttl（秒） |
| `SANDBOX_STORAGE_MAX_TTL` | `86400` | 预签名 GET URL 的硬上限，24 小时 |
| `SANDBOX_MAX_ARCHIVE_BYTES` | `209715200` | reap/delete 时 `/workspace` tar 归档最大大小；超过则跳过归档并记录日志。200 MiB。 |
| `SANDBOX_MAX_STORAGE_BODY_BYTES` | `314572800` | `/storage/put` body 大小上限（RAG 文档上传远大于保护 `/exec`/`/files` 的 F5 上限）。300 MiB。 |
| `SANDBOX_LOCAL_STORAGE_DIR` | _空_ | `local` 归档后端目录（§4.5-F）。设置并挂载为卷后，`local` provider 把 `/workspace` tarball 归档到这里——0 配置持久化，无需 S3/OSS/MinIO。留空则 `local` 不生效（reaped = gone）。**仅单节点**（普通卷不跨副本共享）；MinerU 文档解析仍需对象存储（本机文件无预签名 URL）。仅 operator env，绝不由管理员/请求下发——sidecar 以 root + docker.sock 运行，让调用方选路径即主机写入面。 |

> **存储 provider**：下发的 `storage.provider` 为 `local` / `s3` / `aliyun_oss` / 空。**MinIO / 任意 S3 兼容**：选 `s3` 并填 `storage.s3_endpoint`——自定义 endpoint 会自动切 path-style + SigV4，无需额外开关。

## 安全姿态（开发级）

每个 session 容器都以**非 root**运行，强制使用 `--network none`、`--cap-drop ALL`、`--security-opt no-new-privileges`，并限制 memory/cpu/pids/nofile，单次执行 120 秒超时。默认 rootfs **只读**，只有 `/tmp`、`$HOME` 和 `/workspace` 是有大小限制的 tmpfs，因此 session 不能把宿主机磁盘写满（可设 `SANDBOX_READ_ONLY_ROOTFS=0` 关闭；在支持 overlay2+prjquota 的宿主机上也可设置 `SANDBOX_DISK_SIZE` 限制 writable layer）。可通过 `SANDBOX_SECCOMP_PROFILE` 为 session 容器固定 seccomp profile。

`/exec` 调用在同一个 session 内串行执行，并有全局并发限制。stdout/stderr 在返回前会经过 32KB 上限。生成文件限制为单个 20MB、最多 20 个、总计 50MB，与 §4.5 安全基线一致，同时保持 HTTP 合约不变。

**鉴权是强制的。** 该服务会驱动宿主机 Docker daemon，所以一个可被访问且无鉴权的实例等价于宿主机 RCE。必须设置 `SANDBOX_API_KEY`，否则 sidecar 拒绝启动；`SANDBOX_ALLOW_NO_AUTH=1` 只允许用于可信本机开发。key 使用常量时间比较。请求 body 在读取前会先按大小限制拒绝（`SANDBOX_MAX_BODY_BYTES`，以及 `/exec` `code` 的 1 MiB 上限），防止超大 payload 打爆控制面。`/files` 上传被限制在 `/workspace` 内：目标路径会规范化，且在容器内解析真实路径（跟随任何符号链接父目录）后再次校验，因此符号链接路径不能逃逸 `/workspace`。

**docker.sock 在宿主机上等价于 root。** 架构需要它，但生产环境应在裸 socket 前放一个 [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy)，并限制为 container create/start/exec/kill + image pull（见 `docker-compose.yml` 中的安全说明）。不要把服务端口暴露到公网。

sidecar 启动时还会发现已有的 `aivory.sandbox=1` 容器，因此 sidecar 重启后仍能继续追踪存活 session，并在之后回收过期容器。

这是容器级隔离，适合单机开发环境。它**不是** gVisor/microVM 级别。生产环境可以把 `app.py` 中的 `docker run`/`docker exec` 调用替换成 gVisor、Firecracker 或 E2B 后端；HTTP 合约和 Go 侧代码保持不变，这正是 thin adapter 的意义。

## 端点

| 方法 | 路径 | Body | 返回 |
|---|---|---|---|
| POST | `/sessions` | `{storage?, idle_ttl_sec?, archive_key?}` | `{session_id}` |
| POST | `/exec` | `{session_id, code, timeout_ms?}` | `{stdout, stderr, exit_code, files[]}` |
| POST | `/files` | `{session_id, path, data_base64}` | `{ok}` |
| POST | `/files/get` | `{session_id, path}` | `{data_base64}` |
| POST | `/files/list` | `{session_id}` | `{files[]}` |
| POST | `/files/reset-inputs` | `{session_id}` | `{ok}` |
| DELETE | `/sessions/{id}` | `{storage?, discard?, archive_key?}` | `{ok}` |
| POST | `/storage/put` | `{key, data_base64, content_type, expires_in?, storage}` | `{provider, key, url, expires_in}` |
| POST | `/storage/delete` | `{key, storage}` | `{ok, key}` |
| GET | `/healthz` | - | `{ok, docker, image}` |

`/files/get` 会从 `/workspace` 读回文件，使用和 `/files` 相同的路径限制与符号链接防护。session 或文件不存在时返回 404。

`/storage/*` 是针对管理员配置对象存储桶的操作。Go 后端会在 `storage` 块中转发配置：`provider` 为 `s3` 或 `aliyun_oss`，并带上 `prefix` 与对应 provider 凭据。`boto3` / `oss2` 只有在实际使用对应后端时才会 lazy import。`/storage/put` 上传 base64 字节并返回预签名 GET URL（ttl 为 `expires_in` 或 1 小时，上限 24 小时）；`/storage/delete` 是幂等的，并拒绝删除配置 `prefix` 外的对象。

当 `/sessions` 和 `DELETE /sessions/{id}` 请求携带 `storage` 块时，sidecar 会在 TTL 回收或显式销毁前归档持久工作区状态，并在下次创建 session 时恢复（设计 §4.5）。服务端管理的 `uploads` 和 `skills` 输入目录不会进入归档，旧归档恢复后也会立即清空这两个目录。归档对象键用 `/sessions` 下发的 **`archive_key`（会话 id）**作为文件名 `<prefix>/workspaces/<archive_key>.tgz`——因为每次创建都是新的 session id，用稳定的 `archive_key` 才能**跨回收恢复**工作区（§4.5-C G2）；未下发 `archive_key` 时回退用 session id。`provider` 为 `local`（默认）/ `s3` / `aliyun_oss`。这些操作都是 best-effort：无有效 `storage` 块即 no-op（回收即丢失），归档/恢复失败也不会让请求失败。

`files[]` 产物是本次执行期间 Python 写入 `/workspace/outputs/` 的文件，格式为 `{name, mime_type, data_base64}`，其中可以包含 Python 生成的 PNG。`/files` 只接受符合条件的非图片数据和技能输入，并会按扩展名与文件签名拒绝图片。`/files/reset-inputs` 会清空 `/workspace/uploads` 与 `/workspace/skills`，但不会触碰生成产物或其他工作区状态。
