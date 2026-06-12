# Aurelia

> 一个 5 分钟自部署、厂商中立的多模型 AI 对话平台。Claude、GPT、Gemini、各种开源模型都接得进来，统一在一套有编辑感的 UI 下：流式输出、工具调用、RAG、管理后台,都是标配。

<p align="center">
  <a href="./README.md">English</a> ·
  <a href="./README.zh-CN.md"><strong>简体中文</strong></a>
</p>

<p align="center">
  <a href="https://github.com/hjxwz123/Aurelia/actions/workflows/docker-images.yml"><img alt="构建状态" src="https://github.com/hjxwz123/Aurelia/actions/workflows/docker-images.yml/badge.svg"></a>
  <a href="https://github.com/hjxwz123/Aurelia/pkgs/container/aurelia-api"><img alt="镜像" src="https://img.shields.io/badge/ghcr.io-aurelia--api-blue?logo=docker"></a>
  <a href="https://github.com/hjxwz123/Aurelia/pkgs/container/aurelia-web"><img alt="镜像" src="https://img.shields.io/badge/ghcr.io-aurelia--web-blue?logo=docker"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go">
  <img alt="React" src="https://img.shields.io/badge/React-19-61DAFB?logo=react">
  <img alt="TypeScript" src="https://img.shields.io/badge/TypeScript-5-3178C6?logo=typescript">
  <a href="./LICENSE"><img alt="开源协议" src="https://img.shields.io/badge/license-MIT-green"></a>
</p>

---

## 项目简介

Aurelia 是一个自部署、厂商中立的 AI 对话产品。Claude / OpenAI / Gemini 或任何兼容这三家接口格式的模型,接进来后体验完全一致:实时流式、多轮工具调用、文档问答、项目工作区、对话树分支、一套不像 demo 的编辑式 UI。

整套服务一条 `docker compose up` 起来。PostgreSQL、Redis、Qdrant 全在容器里跑,宿主机不用装任何东西。

## 主要特性

- **多模型,体验统一**:Provider 适配层抽象掉 Claude / OpenAI(Chat Completions 或 Responses)/ Gemini 的差异,对话中途可切换,每条回复都标注生成它的模型。
- **实时工具调用**:web_search、web_fetch、Python 沙箱、图像生成、知识库检索,全部以 SSE 事件流形式推进度。原生 function calling + 提示词拼接回退,任何模型都能用上同一套工具。
- **不毁文档的 RAG**:层级化 small-to-big 切块、~12% 重叠、结构感知(代码 / 表格 / 公式整块保留)、标题路径前缀。Qdrant 密集向量 + Postgres BM25 关键词检索,RRF 融合。
- **MinerU 啃硬文档**:扫描件 PDF / DOCX / PPTX / XLSX / 图片全部走 MinerU 云 API。原文件落在你自己的 S3 或阿里云 OSS 桶,MinerU 只拿到预签名 URL,凭据不出域。
- **对话树,不是线性日志**:编辑历史问题、重试回复都开新分支,通过 `< 2/3 >` 切换。数据库保留完整树,压缩只影响发给模型的上下文。
- **管理后台**:渠道、模型、技能、用户(基于 token 版本号的实时封禁)、用量报表、全局设置(沙箱 / 对象存储 / 搜索 / MinerU / 上传白名单)。**所有配置实时生效,改完保存下一次请求就换**,无需重启。
- **文件上传安全基线**:按用户独立子目录隔离 / 取最后一个扩展名白名单判定(防 `report.pdf.exe`)/ 拒 NUL 与控制字符 / 字节大小上限 / 用 `mime.ParseMediaType` 严格解析。默认安全集刻意排除可执行文件、归档、HTML/SVG。
- **生产就绪镜像**:GitHub Actions 在每次 push 时构建多架构(amd64 + arm64)镜像,发布到 `ghcr.io/hjxwz123/aurelia-{api,web}`。

## 快速开始(推荐:Docker)

需要 Docker 24+ 与 Compose 插件。

```bash
# 1. 克隆(只需要 deploy/ 子目录)
git clone https://github.com/hjxwz123/Aurelia.git
cd Aurelia/deploy

# 2. 填密钥
cp .env.example .env
$EDITOR .env             # 至少改 POSTGRES_PASSWORD、REDIS_PASSWORD、
                         #     JWT_SECRET、SEED_ADMIN_PASSWORD

# 3. 拉镜像 + 启动
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

完成后访问 `http://localhost`(或者你设置的 `WEB_PORT`)。用 `SEED_ADMIN_EMAIL` / `SEED_ADMIN_PASSWORD` 登录,去 `/admin/channels` 配真实的 API key。

五个容器:

| 容器 | 镜像 | 作用 |
| --- | --- | --- |
| `postgres` | `postgres:16-alpine` | 用户、对话、知识库、设置、用量记录 |
| `redis` | `redis:7-alpine` | 缓存、限频计数器、kill-signal pub/sub |
| `qdrant` | `qdrant/qdrant:v1.12.4` | RAG 向量检索 |
| `api` | `ghcr.io/hjxwz123/aurelia-api:latest` | Go HTTP 服务(`/api/*`) |
| `web` | `ghcr.io/hjxwz123/aurelia-web:latest` | nginx 提供前端 + 反代 `/api` |

数据落在命名卷 `pgdata` / `redisdata` / `qdrantdata` / `apidata` —— 备份要整体备,向量、行数据和文件保持一致。

## 架构

```
                     ┌────────────────────────────────┐
                     │  浏览器 (SPA,SSE 流式)         │
                     └─────────────┬──────────────────┘
                                   │ HTTPS
                       ┌───────────▼────────────┐
                       │ nginx (web 容器)       │
                       │ /        → 前端静态     │
                       │ /api/*   → api:8787    │
                       └───────────┬────────────┘
                                   │ HTTP keep-alive
                  ┌────────────────▼─────────────────┐
                  │      Go API (api 容器)           │
                  │  REST + SSE 编排器                │
                  │  ┌──────────────────────────┐    │
                  │  │ Provider 注册表           │    │
                  │  │  Claude / OpenAI / Gemini│    │
                  │  └──────────────────────────┘    │
                  │  ┌──────────────────────────┐    │
                  │  │ 自建工具层                │    │
                  │  │  web_search · web_fetch  │    │
                  │  │  python_execute · KB ret.│    │
                  │  │  image_generate          │    │
                  │  └──────────────────────────┘    │
                  └─┬──────────┬──────────┬──────────┘
                    │          │          │
            ┌───────▼──┐  ┌────▼────┐  ┌──▼─────────┐
            │ Postgres │  │  Redis  │  │   Qdrant   │
            │  业务库   │  │  缓存   │  │  向量库     │
            │  + 审计   │  │  + pub  │  │  (RAG)     │
            └──────────┘  └─────────┘  └────────────┘

  可选外部服务(管理后台配,实时生效):
   • MinerU 云(文档解析)
   • S3 / 阿里云 OSS(MinerU 拉取桶 + 沙箱归档桶)
   • SearXNG(自部署搜索引擎)
   • 真实 LLM 渠道(API key 在数据库里,不在 env)
```

完整设计见 [`DESIGN.md`](./DESIGN.md)(中文,~2000 行)。

## 配置

Aurelia 的绝大多数配置项**通过管理后台实时改**,不依赖环境变量。模型 key、MinerU token、S3 凭据、SearXNG 地址、上传白名单……这些都在 admin 页面里编辑,下一次请求就生效,无需重启。

[`deploy/.env.example`](./deploy/.env.example) 里只放了少量启动必需的项:

| 分组 | 键 | 用途 |
| --- | --- | --- |
| **镜像** | `IMAGE_OWNER`、`IMAGE_TAG` | 从哪个 GHCR 命名空间拉镜像 / 拉哪个 tag |
| **网络** | `WEB_PORT`、`PUBLIC_ORIGIN` | nginx 暴露端口 + CORS 白名单 |
| **Postgres** | `POSTGRES_USER/PASSWORD/DB` | 数据库凭据 |
| **Redis** | `REDIS_PASSWORD` | 必填,启用 `requirepass` |
| **Qdrant** | `QDRANT_API_KEY` | 可选 API key |
| **鉴权** | `JWT_SECRET` | 必填,≥32 字符;生产环境拒绝使用 dev 默认值启动 |
| **首启管理员** | `SEED_ADMIN_EMAIL/PASSWORD` | 仅首次启动用,之后改不动它 |
| **启动兜底** | `SEARCH_*`、`EMBEDDING_*`、`MINERU_*` | 只在对应 admin 设置项为空时生效 |

## 从源码自行构建镜像

同一个 compose 文件也支持本地构建——开发期或者跑在官方镜像没覆盖的架构上时用。

```bash
cd deploy
cp .env.example .env
docker compose -f docker-compose.prod.yml up -d --build
```

`api` 与 `web` 服务都同时声明了 `image:` 和 `build:`,Compose 优先用已有镜像,缺了就本地构。

## 本地开发(不用 Docker)

Go API 自带 SQLite 驱动 + 哈希袋兜底嵌入,完全可以不装外部服务跑起来。

```bash
# 后端
cd server
go run ./cmd/api                  # :8787

# 前端(另一个终端)
cd ..
npm install
npm run dev                       # Vite :5173,代理 /api → :8787
```

打开 `http://localhost:5173`,用 `admin@aurelia.local` / `aurelia-admin` 登录(`.env.example` 里的默认值)。mock provider 已经种好,不配真实模型也能聊。

## GitHub Actions:自动构建镜像

[`.github/workflows/`](./.github/workflows) 下两个 workflow:

| Workflow | 触发 | 产物 |
| --- | --- | --- |
| **`docker-images.yml`** | push 到 `main`、`v*.*.*` tag、手动 dispatch | `ghcr.io/<owner>/aurelia-api`、`ghcr.io/<owner>/aurelia-web` —— 多架构(amd64 + arm64) |

打 tag 规则:

- push 到 `main`        → `:latest` + `:sha-<short>`
- push tag `v1.2.3`     → `:1.2.3` + `:1.2` + `:1` + `:latest`
- pull request          → 只构不推送(冒烟测试)

workflow 只需要 `GITHUB_TOKEN`(GitHub 自动注入),不用额外配 secret。首次成功跑完后,你的 repo 在 Packages 侧栏会出现 `aurelia-api` 和 `aurelia-web`,想给匿名用户拉就在 Package 设置里设为 public。

Fork 之后记得把 `deploy/.env` 里的 `IMAGE_OWNER` 改成你的小写 GitHub 用户名。

## 技术栈

- **前端**:React 19、TypeScript 5、Vite 5、Tailwind 4、Radix UI、Zustand、i18next、lucide-react
- **后端**:Go 1.22、标准 `net/http`、手写 sqlc 风格查询
- **存储**:PostgreSQL 16(生产)/ SQLite(嵌入式兜底)
- **缓存与协调**:Redis 7
- **向量检索**:Qdrant 1.12(无 Qdrant 时降级为 Postgres brute-force 余弦)
- **文档解析**:MinerU 云 API(PDF / DOCX / PPTX / 图片 OCR)
- **可选**:S3 / 阿里云 OSS 作源文件桶,SearXNG 作自部署搜索

## 项目结构

```
.
├── src/                      React 前端(对话 / 后台 / KB / 项目)
├── server/                   Go 后端
│   ├── cmd/api/              main 入口
│   └── internal/
│       ├── api/              HTTP handler、路由、上传安全
│       ├── llm/              Provider 适配 + 编排器(SSE)
│       ├── tools/            web_search / web_fetch / python_execute …
│       ├── rag/              解析 → 切块 → 嵌入 → 检索
│       ├── vector/           Qdrant 客户端
│       ├── store/            Postgres / SQLite 表结构与查询
│       ├── sandbox/          可选沙箱 sidecar 的 HTTP 客户端
│       └── storage/          S3 / OSS 上传 + 预签名 HTTP 客户端
├── deploy/                   生产部署
│   ├── docker-compose.prod.yml
│   ├── Dockerfile.server     多阶段 Go 构建 → debian-slim
│   ├── Dockerfile.web        Vite 构建 → nginx
│   ├── nginx.conf            前端 + /api 反代(SSE 友好)
│   └── .env.example          环境变量模板
├── docs/                     设计笔记、规约
├── DESIGN.md                 完整设计文档(中文)
└── .github/workflows/        镜像构建 CI
```

## 参与贡献

欢迎 PR。改动较大请先开 issue 讨论形态,避免做完再返工。

push 前本地自测:

```bash
# 前端
npm run lint
npm run typecheck
npm run build

# 后端
cd server
go vet ./...
go build ./...
```

CI 跑的就是这一套加 `docker buildx build`。

## 开源协议

[MIT](./LICENSE) —— 商用、修改、再发布随意,保留版权声明即可。

## 致谢

- [Anthropic](https://www.anthropic.com)、[OpenAI](https://openai.com)、[Google](https://ai.google.dev) 提供模型 API。
- [MinerU](https://mineru.net) 提供文档解析云服务。
- [Qdrant](https://qdrant.tech) 提供向量数据库。
- [SearXNG](https://github.com/searxng/searxng) 提供自部署元搜索。
- Radix UI / shadcn 的无头组件生态,Aurelia 在它们之上重新主题化。
