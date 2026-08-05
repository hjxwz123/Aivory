# Aivory 社区推广稿

## 标题备选

1. `[开源] Aivory：一套能跑多轮工具、RAG 和 Python 沙箱的自部署 AI 工作空间`
2. `[自荐开源] 不只是聊天壳：Aivory 把模型、工具、知识库和团队协作放进了一套系统`
3. `[Apache-2.0] Aivory：面向个人与团队的多模型 AI 工作台，支持 Docker 自部署`

## 正文

大家好，向大家介绍一个最近在持续开发的开源项目：**Aivory**。

GitHub：<https://github.com/hjxwz123/Aivory>

![Aivory - Self-hosted AI workspace](https://raw.githubusercontent.com/hjxwz123/Aivory/main/docs/screenshots/aivory-readme-cover.png)

Aivory 是一套面向个人和团队的自部署 AI 工作空间。做这个项目的出发点，是不想只做一个把消息转发给模型的聊天界面，而是希望把日常使用 AI 时经常断开的几个环节真正串起来：**模型、搜索、代码执行、文件、知识库、团队协作，以及后台运营**。

简单说，它既可以作为个人使用的多模型 AI 工作台，也可以作为一套带用户、权限、配额和管理后台的团队服务来部署。

### 它目前能做什么

- **多模型对话**：支持 OpenAI、Anthropic、Gemini 以及任意 OpenAI 兼容端点，也支持图片和嵌入模型。模型、渠道、定价、上下文窗口、工具权限等都可以在后台配置。
- **多轮工具调用**：单轮最多执行 48 次工具调用、跨越 12 个模型循环。搜索、网页抓取、Python、图片生成、记忆和技能可以组合成一条完整工作流。
- **RAG 与知识库**：支持 PDF、DOCX、PPTX、XLSX、文本和图片等文件，包含结构感知分块、查询路由、向量与关键词混合检索、RRF 融合和引用回答。
- **持久 Python 沙箱**：每个对话拥有独立工作区，文件可以跨工具调用和多轮对话复用。沙箱默认断网，生成的图表、表格、PPT 等文件会直接回到对话中。
- **团队工作空间**：可以共享对话、项目、文件和知识库，同时与个人数据隔离；支持邀请链接、成员身份和空间管理。
- **订阅、积分与配额**：支持用户组、模型配额、定时额度、永久积分、兑换码、积分套餐和可选支付渠道。
- **完整管理后台**：覆盖模型渠道、用户、工作空间、知识库、用量、存储、安全、备份、订阅和支付等配置。

### 一个实际工作流

例如，给它一句“检索 2025 年全球 GDP 数据并生成 PowerPoint”，模型可以先加载文档生成技能，再搜索和读取数据源，随后进入隔离的 Python 沙箱清洗数据、绘图并生成 PPT。中间步骤会实时显示，不需要人在搜索、Notebook 和聊天窗口之间来回复制。

![Aivory 多轮工具调用：搜索和读取数据源](https://raw.githubusercontent.com/hjxwz123/Aivory/main/docs/screenshots/tool-calls-1.jpg)

最终产物可以直接以图表和可下载文件的形式回到对话里：

![Aivory 多轮工具调用：生成图表与 PowerPoint](https://raw.githubusercontent.com/hjxwz123/Aivory/main/docs/screenshots/tool-calls-2.jpg)

### 不是把所有逻辑塞进前端

Aivory 的后端使用 Go，负责鉴权、SSE 流式响应、模型编排、工具执行、RAG、存储和用量结算。生产环境使用 PostgreSQL、Redis 和 Qdrant；Python 执行由独立 sidecar 提供，运行器固定关闭网络。

![Aivory 架构：统一连接模型、工具、知识、沙箱和数据服务](https://raw.githubusercontent.com/hjxwz123/Aivory/main/docs/screenshots/aivory-architecture-poster.png)

管理端的大多数运行时配置保存后即可在下一次请求生效，不需要为了增加一个模型或修改工具策略去重新构建前端。

![Aivory 管理后台](https://raw.githubusercontent.com/hjxwz123/Aivory/main/docs/screenshots/admin.jpg)

### Docker 部署

需要 Docker 24+ 和 Compose 插件。生产部署默认包含 `app`、`postgres`、`redis`、`qdrant` 和 `sandbox` 五个容器：

```bash
git clone https://github.com/hjxwz123/Aivory.git
cd Aivory/deploy

cp .env.example .env
# 编辑 .env，至少修改 POSTGRES_PASSWORD、REDIS_PASSWORD 和 JWT_SECRET

docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

启动后访问 `http://localhost`，首次进入会引导创建管理员账号。随后在后台添加 Provider key 并创建模型即可开始使用。

有几点也提前说明：

- Aivory 本身不附带商业模型额度，模型 API key 需要自行准备。
- 完整生产部署不是单文件小工具，默认会启动五个容器；如果只想本地开发，也支持 SQLite 和内存缓存模式。
- Qdrant 用于向量检索；未配置向量后端时，知识库会退回全文上下文方案。
- MinerU OCR、S3/OSS、SearXNG 和支付渠道都是按需配置的可选能力。

### 技术栈与协议

- 前端：React 19、TypeScript、Vite、Tailwind、Radix UI、Zustand
- 后端：Go 1.22、标准 `net/http`
- 数据：PostgreSQL / SQLite、Redis、Qdrant
- 协议：**Apache License 2.0**，允许商用和二次开发，需遵守协议中的版权及变更声明要求

项目还在持续迭代，欢迎试用、提 Issue 或提交 PR。尤其希望听到大家对以下方面的真实反馈：部署流程是否清晰、工具调用链是否稳定、知识库检索效果，以及管理后台是否还缺少关键能力。

GitHub：<https://github.com/hjxwz123/Aivory>

中文文档：<https://github.com/hjxwz123/Aivory/blob/main/README.zh-CN.md>

## 发布建议

- 首图使用 `aivory-readme-cover.png`，它最适合论坛信息流和帖子顶部展示。
- 正文保留“实际工作流”的两张连续截图，这是项目与普通聊天前端区别最直观的证据。
- 偏运维或自部署社区可以保留架构图和五容器说明；偏产品社区可以删掉架构段，缩短正文。
- 不建议在标题中使用“最强”“媲美某某”“生产可用零门槛”等难以验证的表述。
- 发帖后首条回复可以补充当前版本、已知问题和后续路线，正文会更聚焦。
