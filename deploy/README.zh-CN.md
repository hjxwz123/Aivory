# Aivory — 生产部署

<p align="center">
  <a href="./README.md">English</a> ·
  <a href="./README.zh-CN.md"><strong>简体中文</strong></a>
</p>

这个目录用于通过 Docker Compose 部署完整栈：

| 服务 | 镜像 / 构建 | 作用 |
| --- | --- | --- |
| `postgres` | `postgres:16-alpine` | 关系型存储：用户、对话、知识库、用量等。 |
| `redis` | `redis:7-alpine` | 缓存、限流计数器、跨进程停止流式输出 pub/sub。 |
| `qdrant` | `qdrant/qdrant:v1.12.4` | RAG 向量检索。 |
| `sandbox` | `ghcr.io/hjxwz123/aivory-sandbox-sidecar` | 内置代码执行沙箱，仅供内网访问。 |
| `app` | `ghcr.io/hjxwz123/aivory-app`（也可通过 `Dockerfile.app` 本地构建） | 同一个容器同时提供构建后的 SPA 和 `/api` 后端，二者同源。 |

完整项目介绍见[根目录 README](../README.zh-CN.md)。本文档只作为部署速查。

## 后端选择机制

生产和本地开发使用的是同一个 API 二进制。它会在启动时检查环境 URL，并据此选择后端：

- `DATABASE_URL=postgres://...` 使用 Postgres（通过 `pgcompat` 驱动）；其它值（例如 `*.db` 路径）使用内嵌 SQLite。
- 设置 `REDIS_URL` 时使用 Redis；未设置时使用进程内内存缓存。
- 设置 `QDRANT_URL` 时使用 Qdrant；未设置时关闭向量搜索，RAG 回退为注入当前范围内的完整文档文本。

因此，**本地运行不需要额外安装这些服务**：不设置这些 URL 就会使用 SQLite + 内存缓存 + 全文上下文兜底。这个 compose 文件设置了三者，Docker 部署默认使用 Qdrant。

分块向量只写入 Qdrant。关系型数据库只保存分块文本和检索元数据，检索时会用数据库校验 Qdrant 命中；当 Qdrant 不可用或为空时，RAG 会注入完整上下文兜底。删除文档、知识库或对话时，也会删除 Qdrant 中对应的点。

## 首次部署（预构建镜像）

```bash
cd deploy
cp .env.example .env
# 编辑 .env：至少设置 POSTGRES_PASSWORD、REDIS_PASSWORD 和 JWT_SECRET。
# 不需要设置 domain/CORS/port 环境变量。app 在同一 origin 上同时提供 SPA 和 /api，
# 所以它被哪个 host 访问，哪个 host 就能工作，包括多个域名。
docker compose -f docker-compose.prod.yml pull
# 还要预拉「沙箱运行时镜像」。sidecar 每次代码执行会话都会用 `docker run` 起一个
# 单独的 ~600MB 镜像（SANDBOX_IMAGE），它不是 compose service，所以上面的 `pull`
# 不会拉它。本地没有缓存时，第一次 python_execute 就会卡在冷拉镜像上并超时（sidecar
# 报 500：`docker run … timed out`）。SANDBOX_PULL_ON_START 会在 sidecar 启动时也拉
# 一次，但那是 best-effort（镜像站慢/不稳时可能没拉下来）——这一步让它变确定性。
# source .env 是为了带上你可能覆盖过的 IMAGE_* 变量。
set -a && . ./.env 2>/dev/null; set +a
docker pull "${IMAGE_REGISTRY:-ghcr.io}/${IMAGE_OWNER:-hjxwz123}/aivory-sandbox:${IMAGE_TAG:-latest}"
docker compose -f docker-compose.prod.yml up -d
```

应用随后可通过 `http://<host>` 访问（默认绑定主机 80 端口；如果 80 被占用，修改 `docker-compose.prod.yml` 里的 `"80:8787"` 映射）。首次启动时系统没有任何用户，第一个通过初始化页面创建的账号会成为管理员。之后在 **/admin** 中添加真实 provider channel（API key 存在数据库里）。

`store.Migrate()` 会在启动时自动运行，并在表不存在时创建 Postgres schema（`schema_pg.sql`）。不需要手动执行 SQL。

## 本地构建镜像

当你在代码库上迭代，或使用官方镜像未覆盖的架构时，可以本地构建：

```bash
cd deploy
cp .env.example .env
docker compose -f docker-compose.prod.yml up -d --build
```

compose 文件同时声明了 `image:` 和 `build:`：存在预构建镜像时 Compose 会优先使用镜像；需要时会回退到本地构建。

## Embedding 维度

Qdrant 按 embedding 宽度使用独立 collection（`aivory_c<dim>`）。如果配置真实 embedding 模型，请确保 `EMBEDDING_DIM`（以及管理后台中该模型的 `dim`）与模型输出维度一致。否则会使用本地 256 维 embedder，它的 collection 与 1536 维模型向量不兼容。

## TLS 与域名

`app` 容器在主机 80 端口提供明文 HTTP。公开部署时应在前面放 TLS 终止层，例如 Caddy、Traefik 或云负载均衡。因为 SPA 和 `/api` 共用一个 origin，**不需要按域名配置任何内容**：可以把任意数量的域名指向代理，只要代理转发 `Host` header 即可（常见反向代理默认都会转发）。无需 `PUBLIC_ORIGIN` / `ALLOWED_ORIGINS`。

## 备份

持久化数据在命名卷中：`pgdata`、`redisdata`、`qdrantdata`。上传文件和生成产物绑定挂载到 `DATA_DIR`（默认 `./data`）。备份时请把它们一起备份，确保向量、数据库行和磁盘文件保持一致。

管理员后台的 **备份与迁移** 页面也会异步生成 Docker 部署用的全量迁移 ZIP：包含数据库逻辑备份、可选 uploads/artifacts，以及 Qdrant 向量数据。生成后的文件存放在 `BACKUP_DIR`（默认 `/app/data/backups`，宿主机上对应 `DATA_DIR/backups`）。
备份导入大小上限由 `MAX_BACKUP_BYTES` 控制（默认 20 GiB）。
