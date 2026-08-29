# Aivory — 部署配置

<p align="center">
  <a href="./README.md">English</a> ·
  <a href="./README.zh-CN.md"><strong>简体中文</strong></a>
</p>

这个目录提供两套彼此独立的 Docker Compose 配置：

- `docker-compose.personal.yml`：只启动一个 app 容器，使用 SQLite 业务库、SQLite
  内嵌向量和进程内缓存/队列，不内置沙箱。
- `docker-compose.prod.yml`：原有 PostgreSQL + Redis + Qdrant 完整栈。

完整栈包含：

| 服务 | 镜像 / 构建 | 作用 |
| --- | --- | --- |
| `postgres` | `postgres:16-alpine` | 关系型存储：用户、对话、知识库、用量等。 |
| `redis` | `redis:7-alpine` | 缓存、限流计数器、跨进程停止流式输出 pub/sub。 |
| `qdrant` | `qdrant/qdrant:v1.12.4` | RAG 向量检索。 |
| `sandbox` | `ghcr.io/hjxwz123/aivory-sandbox-sidecar` | 内置代码执行沙箱，仅供内网访问。 |
| `app` | `ghcr.io/hjxwz123/aivory-app` | 同一个容器同时提供构建后的 SPA 和 `/api` 后端，二者同源。 |

部署与配置指南见[官方文档](https://docs.aivorygo.com)，完整项目介绍见
[根目录 README](../README.zh-CN.md)。本文档继续作为随代码版本维护的部署速查。

## 后端选择机制

个人版、完整版和本地开发使用同一个 API 二进制，启动时按配置选择后端：

- `DATABASE_URL=postgres://...` 使用 Postgres（通过 `pgcompat` 驱动）；其它值（例如 `*.db` 路径）使用内嵌 SQLite。
- 设置 `REDIS_URL` 时使用 Redis；未设置时使用进程内内存缓存。
- `VECTOR_BACKEND=auto`（默认）保持原有规则：有 `QDRANT_URL` 就使用 Qdrant，
  否则关闭向量并回退为注入当前范围内的完整文档文本。
- `VECTOR_BACKEND=qdrant` 强制使用 Qdrant，并要求设置 `QDRANT_URL`。
- `VECTOR_BACKEND=sqlite` 要求 SQLite `DATABASE_URL`，启用内嵌精确余弦检索；
  个人版 Compose 固定使用该值。
- `VECTOR_BACKEND=disabled` 显式关闭向量检索。

完整版 Compose 继续像以前一样固定使用 PostgreSQL、Redis、Qdrant。个人版 Compose
固定使用 SQLite、清空远程服务 URL 并启用 SQLite 向量，即使环境文件误留了 Redis/Qdrant
地址也不会偷偷连回外部服务。

完整版向量仍只写入 Qdrant。个人版向量会归一化并以二进制形式写入同一个 SQLite
数据库，由应用进程执行精确检索，并随逻辑备份一起导出。两种后端使用相同的作用域
校验，删除文档、知识库或对话时也会删除对应向量。

## 个人版部署

```bash
cd deploy
cp .env.personal.example .env.personal
# 设置 JWT_SECRET；需要高质量语义检索时，也可在这里或后台配置 embedding。
$EDITOR .env.personal
docker compose --env-file .env.personal -f docker-compose.personal.yml pull
docker compose --env-file .env.personal -f docker-compose.personal.yml up -d
```

访问 `http://<host>`。个人版默认只启动 `app`，不会挂载 Docker socket，也不会拉取两张
沙箱镜像。未配置沙箱前 Python 执行不可用。

使用外部沙箱时，可在「管理后台 → 工具」填写 URL 和 Bearer 密钥，或在启动前通过
`.env.personal` 设置 `SANDBOX_BASE_URL` 和 `SANDBOX_API_KEY`。需要在个人版机器本地
部署内置沙箱时，在 `.env.personal` 中设置下面一对值：

```dotenv
SANDBOX_BASE_URL=http://sandbox:8000
SANDBOX_API_KEY=aivory-personal-sandbox
```

然后在两条命令中加入可选 profile：

```bash
docker compose --env-file .env.personal -f docker-compose.personal.yml --profile sandbox pull
docker compose --env-file .env.personal -f docker-compose.personal.yml --profile sandbox up -d
```

该 profile 会启动 `sandbox` 和 `sandbox-image-keepalive`。sidecar 需要通过宿主机
Docker socket 创建受限的会话容器，因此只应在可接受这一权限的主机启用；默认单容器
个人版绝不会挂载该 socket。

`DATA_DIR` 默认是 `./data-personal`。其中的 `aivory.db` 同时包含业务数据和向量；上传
文件、生成产物、本地对象与后台备份也在该目录下。请整体备份这个目录。个人版只支持
单个 app 实例，不要扩容 app，也不要把 SQLite 文件放在 NFS 上。

零配置时仍可使用内置的本地 hash embedding，但它只适合作为基础兜底。需要可靠的语义
检索时，请配置 OpenAI 兼容的 embedding 接口，并确保 `EMBEDDING_DIM` 与模型实际输出
一致。更换 embedding 模型或维度后，需要在管理后台重建已有向量。

## 完整版部署

```bash
cd deploy
cp .env.example .env
# 编辑 .env：至少设置 POSTGRES_PASSWORD、REDIS_PASSWORD 和 JWT_SECRET。
# 使用域名或 HTTPS 反向代理时，还必须设置准确的 ALLOWED_ORIGINS。
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

> **沙箱运行时镜像**：代码执行（`python_execute`）跑在一个**单独的 ~600MB 镜像**里，
> 由 sidecar 每次会话用 `docker run` 起。`sandbox-image-keepalive` 这个 service 会把
> 这个镜像**钉在本地**——`pull` 会拉它,而且宿主机的 `docker image prune -a` / 清理
> cron **删不掉它**。没有这个钉子,镜像在会话之间无人引用(会话容器是临时的),就会被
> prune 清掉,下次 `python_execute` 冷拉镜像并超时(sidecar 报 500：`docker run … timed out`)。
> 如果你自己跑镜像清理,建议用 `docker image prune -f`(不带 `-a`,有 tag 的镜像会保留)。

应用随后可通过 `http://<host>` 访问（默认绑定主机 80 端口；如果 80 被占用，修改 `docker-compose.prod.yml` 里的 `"80:8787"` 映射）。首次启动时系统没有任何用户，第一个通过初始化页面创建的账号会成为管理员。之后在 **/admin** 中添加真实 provider channel（API key 存在数据库里）。

### CPU 架构兼容性

三张 Aivory 镜像（应用、沙箱运行时和沙箱 sidecar）的同一标签都会同时发布
`linux/amd64`（`uname -m` 输出 `x86_64`）与 `linux/arm64`（输出 `aarch64`
或 `arm64`）。Compose 会自动选择宿主机架构，不需要设置 `platform:`。ARM64
服务器必须运行 64 位 Linux；不支持 32 位 ARM/`armv7l`。

已有 x86_64 部署不需要迁移，也不需要修改 `.env`、Compose、镜像标签、数据卷或
现有数据，继续使用下文原有的 `pull` 和 `up -d --no-build` 命令即可。全新 ARM64
服务器直接使用上面的快速部署命令，不需要 ARM 专用配置。

`store.Migrate()` 会在启动时自动运行，并在表不存在时创建 Postgres schema（`schema_pg.sql`）。不需要手动执行 SQL。

## 按版本号部署或回滚

`IMAGE_TAG` 会同时选择应用、沙箱运行时和沙箱 sidecar 的发布版本。发布 tag 时三张镜像都会生成相同的语义版本标签，因此以后即使更新了沙箱，`3.0.0` 这类版本也只需填写一个值。

编辑实际部署使用的 `deploy/.env`（不是已经复制过的 `.env.example`）：

```dotenv
# Git tag v3.0.0 对应的镜像 tag 是 3.0.0，不能写 v3.0.0。
IMAGE_TAG=3.0.0
```

拉取前先检查 Compose 最终解析出的镜像，确认三张 Aivory 镜像都带上目标版本：

```bash
cd deploy
docker compose --env-file .env -f docker-compose.prod.yml config --images
docker compose --env-file .env -f docker-compose.prod.yml pull
docker compose --env-file .env -f docker-compose.prod.yml up -d --no-build
docker compose --env-file .env -f docker-compose.prod.yml images
```

预期至少包含：

```text
ghcr.io/hjxwz123/aivory-app:3.0.0
ghcr.io/hjxwz123/aivory-sandbox:3.0.0
ghcr.io/hjxwz123/aivory-sandbox-sidecar:3.0.0
```

请在三张发布镜像全部生成后再部署。如果某次发布未完成，`pull` 会直接失败，不会静默混用不同版本。回滚方式相同：选择一个完整发布过的旧版本，再重新执行 `pull` 和 `up`。

`2.2.6` 等历史版本早于沙箱语义版本镜像。部署这类旧版本时，使用可选兼容覆盖：

```dotenv
IMAGE_TAG=2.2.6
SANDBOX_IMAGE_TAG=latest
```

如果改了标签仍没有变化，先执行 `config --images`。它能直接发现两类常见问题：修改的是 `.env.example` 而实际 `.env` 仍为 `latest`，或者从仓库根目录运行导致没有读取预期 env 文件。显式传入 `--env-file .env` 可以避免后者。

## Embedding 维度

Qdrant 按 embedding 宽度使用独立 collection（`aivory_c<dim>`）；SQLite 内嵌后端会在
每条向量旁记录维度。如果配置真实 embedding 模型，请确保 `EMBEDDING_DIM`（以及管理
后台中该模型的 `dim`）与模型输出维度一致。本地 256 维向量与外部模型的 1536 维向量
在两种后端中都会保持隔离。

## TLS 与域名

`app` 容器在主机 80 端口提供明文 HTTP。公开部署时应在前面放 TLS 终止层，例如 Caddy、Traefik 或云负载均衡，并将 `ALLOWED_ORIGINS` 设置为浏览器实际访问的 Origin，例如 `ALLOWED_ORIGINS=https://chat.example.com`。多个地址可写成 `ALLOWED_ORIGINS=https://chat.example.com,https://admin.example.com:8443`；不要填写路径或末尾 `/`，纯 IP 同源 HTTP 测试可留空。

## 备份

个人版的业务数据和向量都在 `DATA_DIR/aivory.db`，管理员逻辑备份会包含
`vector_points`。同时仍应整体备份 `DATA_DIR`，确保上传文件与生成产物保持一致。

完整版保持原有备份行为不变：

持久化数据在命名卷中：`pgdata`、`redisdata`、`qdrantdata`。上传文件和生成产物绑定挂载到 `DATA_DIR`（默认 `./data`）。备份时请把它们一起备份，确保向量、数据库行和磁盘文件保持一致。

管理员后台的 **备份与迁移** 页面也会异步生成 Docker 部署用的全量迁移 ZIP：包含数据库逻辑备份、可选 uploads/artifacts，以及 Qdrant 向量数据。生成后的文件存放在 `BACKUP_DIR`（默认 `/app/data/backups`，宿主机上对应 `DATA_DIR/backups`）。
备份导入大小上限由 `MAX_BACKUP_BYTES` 控制（默认 20 GiB）。
