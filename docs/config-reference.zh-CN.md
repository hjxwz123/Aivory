# Aurelia 高级环境变量参考（全中文版）

> 本文件是 [`config-reference.md`](./config-reference.md) 的**全中文版**：说明文字已译为中文（环境变量名 / 类型 / 默认值 / 代码位置保持原样）。若有出入以英文原表 + 源码为准。
>
> 版本 v2.0 · 2026-07-10 · 由整仓源码生成（对应本次改动：所有此前「硬编码但值得调整」的参数均已改为环境变量可覆盖）
>
> **这 461 个环境变量全部是可选的，未在 `.env.example` 中列出。** 不设置任何一个，Aurelia 的行为与改动前完全一致——每个变量的默认值就是原来的硬编码值。只有当你需要调整某个具体参数时，才在部署环境里添加对应的变量。
> 如果需要调整，把用到的变量抄一份加进你自己的 `.env` 即可（`.env.example` 保持精简，不会自动包含这些高级选项）。
>
> **后端（Go）**：404 个，读取自进程环境变量，改动后**需重启 `aurelia-api` 进程**生效。
> **前端（Vite）**：45 个 `VITE_*` 变量，在**构建时**内联（`npm run build` / `vite build`），必须在构建环境设置，**运行时**改容器环境变量无效，需要重新构建产物。
> **沙盒服务（Python）**：12 个 `SANDBOX_*` 变量（与已有的 `SANDBOX_*` 变量同一命名空间），读取自 `sandbox-service` 进程环境，改动后**需重启该进程**生效。
> 类型：`duration`（Go 时长字符串，如 `90s`/`5m`/`2h`/`500ms`；对 Vite/Python 变量用纯数字，单位见默认值列）、`int`/`int64`、`float`、`bool`（`1/true/yes/on` 与 `0/false/no/off`）、`string`。

## 目录

- [0. 总览](#0-总览)
- [1. LLM 对话 / 编排 / 内部模型调用](#1-llm-对话--编排--内部模型调用)
- [2. RAG 文档解析 / 向量检索](#2-rag-文档解析--向量检索)
- [3. 沙盒代码执行](#3-沙盒代码执行)
- [4. 内置工具（搜索 / Python / 网络安全）](#4-内置工具搜索--python--网络安全)
- [5. 会话 / 消息 / 流式 API](#5-会话--消息--流式-api)
- [6. 认证 / 会话 / 验证码](#6-认证--会话--验证码)
- [7. 上传 / 文件 / 分享](#7-上传--文件--分享)
- [8. 管理后台任务（备份 / 向量维护 / 兑换码）](#8-管理后台任务备份--向量维护--兑换码)
- [9. 数据库 / 缓存 / 后台队列](#9-数据库--缓存--后台队列)
- [10. 邮件 / SMTP](#10-邮件--smtp)
- [11. 服务器启动 / 配置加载](#11-服务器启动--配置加载)
- [12. 前端](#12-前端)

---

## 0. 总览

共 **461** 个环境变量，按子系统分布：

| 子系统 | 变量数 |
| --- | --- |
| 1. LLM 对话 / 编排 / 内部模型调用 | 112 |
| 2. RAG 文档解析 / 向量检索 | 80 |
| 3. 沙盒代码执行 | 15 |
| 4. 内置工具（搜索 / Python / 网络安全） | 25 |
| 5. 会话 / 消息 / 流式 API | 81 |
| 6. 认证 / 会话 / 验证码 | 18 |
| 7. 上传 / 文件 / 分享 | 18 |
| 8. 管理后台任务（备份 / 向量维护 / 兑换码） | 21 |
| 9. 数据库 / 缓存 / 后台队列 | 36 |
| 10. 邮件 / SMTP | 2 |
| 11. 服务器启动 / 配置加载 | 8 |
| 12. 前端 | 45 |
| **合计** | **461** |

---

### 1. LLM 对话 / 编排 / 内部模型调用

主对话流、工具循环、TTFT 看门狗，以及压缩 / 记忆 / 审核 / 校验 / 深度研究 / 文档生成等内部模型调用。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_LLM_APPLY_ANTHROPIC_THINKING_SETTINGS` | `int` | `2048` | `llm/anthropic_provider.go:24` | Anthropic 提供方可调参数（可通过环境变量覆盖；默认值保持原有硬编码行为）。 |
| `AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_ANTHROPIC` | `int` | `240` | `llm/anthropic_provider.go:25` | Anthropic 工具结果摘要的截断长度 |
| `AURELIA_LLM_READ_ANTHROPIC_STREAM_INIT` | `int` | `64*1024` | `llm/anthropic_provider.go:26` | Anthropic 流式响应扫描缓冲区初始大小 |
| `AURELIA_LLM_READ_ANTHROPIC_STREAM_MAX` | `int` | `1024*1024` | `llm/anthropic_provider.go:27` | Anthropic 流式响应读取上限 |
| `AURELIA_LLM_MAX_ITER` | `int` | `20` | `llm/anthropic_provider.go:155` | 最大迭代次数 |
| `AURELIA_LLM_MAX_TOK` | `int` | `4096` | `llm/anthropic_provider.go:164` | Anthropic 请求的默认 max_tokens 值 |
| `AURELIA_LLM_MAX_TOK_2` | `int` | `4096` | `llm/anthropic_provider.go:317` | Prompt-tool 单轮 Anthropic 调用的默认最大输出 token 数 |
| `AURELIA_LLM_INFLIGHT_GRACE` | `duration` | `15*time.Minute` | `llm/compaction.go:46` | assistant 消息行处于 status=streaming 状态时仍受保护、不会被摘要化的最长时间；超过此时长的行视为崩溃遗留。注释声称该值高于 api.maxGenDuration 的“10 分钟”上限，但 maxGenDuration 实际为 90 分钟（messages_handlers.go:26）——该宽限期现已低于生成上限 |
| `AURELIA_LLM_T` | `int` | `4` | `llm/compaction.go:55` | 压缩 token 估算中每条消息附加的固定 token 开销（角色标记/框架文本）。 |
| `AURELIA_LLM_MESSAGE_TOKEN_MEMO_CACHE_BOUND` | `int` | `100000` | `llm/compaction.go:56` | 触发原地重置消息级 token 估算缓存 map 的条目数量上限 |
| `AURELIA_LLM_SUMMARY_TOKENS_CLAMP_FLOOR` | `int` | `256` | `llm/compaction.go:57` | 管理员配置 summary_max_tokens 的钳制下限，低于该值时回退为默认值 2048 |
| `AURELIA_LLM_BIG_TOKEN_OVERFLOW_NUM` | `int` | `5` | `llm/compaction.go:58` | 当实际上下文 Token 数超过 tokenTrigger*5/4 时，本轮内同步摘要而非异步处理；仅在计数为精确的提供方计数时触发 |
| `AURELIA_LLM_BIG_TOKEN_OVERFLOW_DEN` | `int` | `4` | `llm/compaction.go:59` | 逐字保留尾部消息数超过该值时，冷启动积压将同步内联摘要而非异步处理；keepRounds*2 为每轮消息数，*3 为内联判定阈值。 |
| `AURELIA_LLM_INLINE_COMPACTION_BACKLOG_FACTOR` | `int` | `3` | `llm/compaction.go:60` | 单条消息压缩调用的输出 token 数 |
| `AURELIA_LLM_COMPACTION_SUMMARY_GENERATION_TOKENS` | `int` | `envcfg.Int("AURELIA_LLM_MAX_OUTPUT_TOKENS_4", 512` | `llm/compaction.go:61` | TaskCompact 摘要生成调用的最大输出 token 数 |
| `AURELIA_LLM_DETERMINISTIC_SUMMARY_CLIP_BUDGET` | `int` | `300` | `llm/compaction.go:62` | 任务模型摘要为空时，CJK 感知兜底截断的 Token 预算；同时也是提示词中“<300 tokens”指令的依据；与第 637 行提示词及默认系统提示词（task_llm.go:259）中的 300 一致 |
| `AURELIA_LLM_ATTEMPT` | `int` | `4` | `llm/compaction.go:63` | 并发轮次下追加摘要块时的最大 compare-and-swap 尝试次数。 |
| `AURELIA_LLM_ITER` | `int` | `3` | `llm/compaction.go:64` | 重新校验已摘要消息是否仍存在时，每批 `IN(...)` 查询的行数（受驱动占位符数量限制） |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_5` | `int` | `2` | `llm/compaction.go:65` | 单次合并中为使路径摘要控制在预算内允许的最大重复折叠次数 |
| `AURELIA_LLM_CHUNK_SIZE` | `int` | `400` | `llm/compaction.go:788` | 较粗粒度的合并摘要调用的 MaxOutputTokens，为摘要 Token 预算的一半；派生自 summaryMaxTokens（默认 2048 -> 约 1024）；兜底截断也使用 budget/2（第 935 行） |
| `AURELIA_LLM_DR_MAX_ROUNDS` | `int` | `4` | `llm/deep_research.go:47` | 搜索→验证轮次数的硬性上限 |
| `AURELIA_LLM_DR_QUERIES_PER_ROUND` | `int` | `6` | `llm/deep_research.go:48` | 每轮下发的最大搜索数 |
| `AURELIA_LLM_DR_FETCH_PER_ROUND` | `int` | `5` | `llm/deep_research.go:49` | 每轮读取的最大信息源数量。 |
| `AURELIA_LLM_DR_MIN_DEEP_READS` | `int` | `5` | `llm/deep_research.go:50` | 技能第 3 阶段：至少深度阅读的信息源数量 |
| `AURELIA_LLM_DR_SEARCH_TOP_K` | `int` | `8` | `llm/deep_research.go:51` | 每次搜索请求返回的结果数量 |
| `AURELIA_LLM_DR_WALL_CLOCK` | `duration` | `5*time.Minute` | `llm/deep_research.go:52` | 整个引擎的兜底超时 |
| `AURELIA_LLM_DR_CALL_TIMEOUT` | `duration` | `30*time.Second` | `llm/deep_research.go:53` | 单次搜索/抓取调用的超时。 |
| `AURELIA_LLM_DR_MAX_BODY_CHARS` | `int` | `4000` | `llm/deep_research.go:54` | 提供给写作阶段的单个信息源摘录字符数上限 |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_8` | `int` | `1024` | `llm/deep_research.go:60` | 深度研究引擎可通过环境变量覆盖的内联调优常量（默认值保持原有行为） |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_9` | `int` | `512` | `llm/deep_research.go:61` | 最大输出 Token 数（9） |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_10` | `int` | `2048` | `llm/deep_research.go:62` | 深度研究流程中该调用的 MaxOutputTokens。 |
| `AURELIA_LLM_DEEP_RESEARCH_VERIFY_EVIDENCE_EXCERPT_CAP` | `int` | `200` | `llm/deep_research.go:63` | 深度研究证据核验摘录字符数上限 |
| `AURELIA_LLM_DEEP_RESEARCH_VALIDATE_TIMEOUT` | `duration` | `75*time.Second` | `llm/deep_research.go:64` | 深度研究验证阶段超时 |
| `AURELIA_LLM_DEEP_RESEARCH_VALIDATE_SOURCE_EXCERPT_CAP` | `int` | `2000` | `llm/deep_research.go:65` | 深度研究来源验证摘录长度上限 |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_RESULT_SUMMARY_CAP` | `int` | `240` | `llm/deep_research.go:66` | 深度研究工具结果摘要的截断长度上限 |
| `AURELIA_LLM_SCORE_A` | `float` | `9` | `llm/deep_research.go:67` | 信源可信度 A 级评分权重 |
| `AURELIA_LLM_SCORE_B` | `float` | `6` | `llm/deep_research.go:68` | 评分参数 B |
| `AURELIA_LLM_SCORE_C` | `float` | `3` | `llm/deep_research.go:69` | 评分参数 C |
| `AURELIA_LLM_SCORE_KW` | `float` | `1` | `llm/deep_research.go:70` | 关键词匹配得分权重 |
| `AURELIA_LLM_SCORE_FRESH_DOMAIN` | `float` | `2` | `llm/deep_research.go:71` | 新域名评分加成（信源多样性奖励） |
| `AURELIA_LLM_MAX_ITER_4` | `int` | `20` | `llm/google_provider.go:68` | 工具调用循环的最大迭代次数 |
| `AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_GEMINI` | `int` | `240` | `llm/google_provider.go:167` | §6.2 规定 `tool_result` 必须携带上游 `tool_use` id，以便前端将结果与进行中的 `tool_call` 卡片配对；对 Gemini 而言该 id 即函数名（同一函数被多次调用的情况较少见）。 |
| `AURELIA_LLM_READ_GEMINI_STREAM_INIT` | `int` | `64*1024` | `llm/google_provider.go:327` | Gemini 流式初始化读取上限 |
| `AURELIA_LLM_READ_GEMINI_STREAM_MAX` | `int` | `1024*1024` | `llm/google_provider.go:327` | 读取 Gemini 流的最大长度 |
| `AURELIA_LLM_PROVIDER_HTTP_CLIENT_TCP_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `llm/httpclient.go:41` | LLM 提供商 HTTP 客户端的 TCP 拨号超时 |
| `AURELIA_LLM_TRANSPORT_TLSHANDSHAKE_TIMEOUT` | `duration` | `10*time.Second` | `llm/httpclient.go:42` | 传输层 TLS 握手超时 |
| `AURELIA_LLM_TRANSPORT_EXPECT_CONTINUE_TIMEOUT` | `duration` | `1*time.Second` | `llm/httpclient.go:43` | 传输层 Expect-Continue 超时 |
| `AURELIA_LLM_TRANSPORT_IDLE_CONN_TIMEOUT` | `duration` | `90*time.Second` | `llm/httpclient.go:44` | 传输层空闲连接超时 |
| `AURELIA_LLM_TRANSPORT_MAX_IDLE_CONNS` | `int` | `50` | `llm/httpclient.go:45` | 传输层最大空闲连接数 |
| `AURELIA_LLM_MEMORY_WORKER_RECENT_MESSAGE_FETCH_LIMIT` | `int` | `30` | `llm/memory_worker.go:36` | 记忆提取器拉取最近对话消息的 SQL LIMIT（按 created_at 倒序） |
| `AURELIA_LLM_MEMORY_CANDIDATES_EXTRACTION_CAP` | `int` | `5` | `llm/memory_worker.go:37` | 提示词中告知任务模型每次会话最多返回 5 条记忆候选项的指令；软上限内嵌于抽取器提示词文本中 |
| `AURELIA_LLM_MEMORY_EXTRACTOR_USER_TURN_CAP` | `int` | `20` | `llm/memory_worker.go:38` | 追加到记忆提取提示词中的最大用户轮次数，超出后停止追加。 |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS` | `int` | `1024` | `llm/memory_worker.go:39` | `TaskMemoryExtract` 内部 LLM 调用的 `MaxOutputTokens` |
| `AURELIA_LLM_CONF` | `float` | `0.7` | `llm/memory_worker.go:40` | 模型返回值 ≤0 或 >1 时赋予候选记忆的置信度 |
| `AURELIA_LLM_EXISTING_SAME_SLOT_MEMORIES_FETCH_LIMIT` | `int` | `10` | `llm/memory_worker.go:41` | 写入时裁决某个槽位时加载的在用记忆的 SQL LIMIT |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_2` | `int` | `256` | `llm/memory_worker.go:42` | TaskMemoryAdjudicate 保留/过期判定调用的 MaxOutputTokens。 |
| `AURELIA_LLM_SEMANTIC_DEDUP_CANDIDATE_MEMORIES_LIMIT` | `int` | `40` | `llm/memory_worker.go:43` | 检查语义重复时提供给模型参考的已保存记忆条目数 SQL `LIMIT` |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_3` | `int` | `64` | `llm/memory_worker.go:44` | findSemanticDuplicate JSON 调用的最大输出 token 数 |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_6` | `int` | `8` | `llm/moderation.go:25` | ALLOW/BLOCK 内容审核模型调用的 `MaxOutputTokens`；模型 ID/系统提示词/分类类别均可由管理员配置，但该 token 上限不可配置 |
| `AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_OPENAI` | `int` | `240` | `llm/openai_provider.go:22` | 可通过环境变量覆盖的默认值（参见配置参考文档）；对应 AURELIA_* 变量未设置时回退为原硬编码值 |
| `AURELIA_LLM_OFFICIAL_TOOL_SPEC` | `string` | `"medium"` | `llm/openai_provider.go:23` | 官方工具规范 |
| `AURELIA_LLM_READ_OPEN_AICHAT_STREAM_INIT` | `int` | `64*1024` | `llm/openai_provider.go:24` | OpenAI Chat 流式响应读取缓冲区的初始大小 |
| `AURELIA_LLM_READ_OPEN_AICHAT_STREAM_MAX` | `int` | `1024*1024` | `llm/openai_provider.go:25` | OpenAI Chat Completions 流式响应扫描缓冲区最大大小 |
| `AURELIA_LLM_READ_OPEN_AIRESPONSES_STREAM_INIT` | `int` | `64*1024` | `llm/openai_provider.go:26` | OpenAI Responses 流式初始化读取上限 |
| `AURELIA_LLM_READ_OPEN_AIRESPONSES_STREAM_MAX` | `int` | `1024*1024` | `llm/openai_provider.go:27` | 读取 OpenAI Responses 流的最大长度 |
| `AURELIA_LLM_MAX_ITER_2` | `int` | `20` | `llm/openai_provider.go:105` | 工具调用循环的最大迭代次数 |
| `AURELIA_LLM_MAX_ITER_3` | `int` | `20` | `llm/openai_provider.go:605` | OpenAI Responses API 工具调用轮次上限（第 3 处） |
| `AURELIA_LLM_INLINE_QUOTE_SOURCE_INJECTION_CAP` | `int` | `8000` | `llm/orchestrator.go:30` | 下方内联字面量可通过环境变量覆盖的调优参数；对应 AURELIA_* 变量未设置时回退为此前的硬编码值 |
| `AURELIA_LLM_IMAGE_MODE_FORCED_GENERATION_SIZE_COUNT_SIZE` | `string` | `"1024x1024"` | `llm/orchestrator.go:31` | 图像模式强制生成的尺寸/数量/大小 |
| `AURELIA_LLM_IMAGE_MODE_FORCED_GENERATION_SIZE_COUNT_COUNT` | `int` | `1` | `llm/orchestrator.go:32` | 图像模式下强制生成的图片数量（n 参数） |
| `AURELIA_LLM_IMAGE_PROMPT_OPTIMIZER_OUTPUT_TOKENS` | `int` | `400` | `llm/orchestrator.go:33` | 图像提示词优化器输出 token 数上限 |
| `AURELIA_LLM_RECENT_HISTORY_STRINGS` | `int` | `6` | `llm/orchestrator.go:34` | 近期历史记录字符串数量 |
| `AURELIA_LLM_RECENT_HISTORY_STRINGS_2` | `int` | `200` | `llm/orchestrator.go:35` | 近期历史字符串（2） |
| `AURELIA_LLM_TITLE_GENERATION_OUTPUT_TOKENS` | `int` | `60` | `llm/orchestrator.go:36` | 会话标题生成调用的输出 Token 数 |
| `AURELIA_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MAX` | `int` | `600` | `llm/orchestrator.go:37` | 沙箱执行超时钳制范围上限 |
| `AURELIA_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MIN` | `int` | `10` | `llm/orchestrator.go:38` | 沙盒执行超时钳制范围下限 |
| `AURELIA_LLM_SANDBOX_EXEC_CTX_SAFETY_MARGIN` | `duration` | `150*time.Second` | `llm/orchestrator.go:39` | 沙箱执行上下文安全余量 |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_WEB_SEARCH` | `int` | `16` | `llm/orchestrator.go:147` | 每轮网页搜索工具调用次数上限 |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_WEB_FETCH` | `int` | `12` | `llm/orchestrator.go:148` | 单轮对话 `web_fetch` 工具调用次数上限 |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_FETCH_IMAGE` | `int` | `16` | `llm/orchestrator.go:149` | 生成幻灯片/文档时单轮可用的图片数量——加以限制以防止单轮批量下载 |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `8` | `llm/orchestrator.go:150` | 每轮工具调用限制：图像生成 |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `16` | `llm/orchestrator.go:151` | §F10：限制每轮沙箱执行次数（每次最长 120 秒），以防止滥用/DoS。 |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_SEARCH` | `int` | `40` | `llm/orchestrator.go:157` | 深度研究模式 `web_search` 工具调用次数上限 |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_FETCH` | `int` | `25` | `llm/orchestrator.go:158` | 深度研究工具网页抓取次数上限 |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_FETCH_IMAGE` | `int` | `12` | `llm/orchestrator.go:159` | 深度研究工具限制：图像抓取 |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `4` | `llm/orchestrator.go:160` | 深度研究场景下每轮图像生成工具调用次数上限 |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `8` | `llm/orchestrator.go:161` | 深度研究模式 `python_execute` 工具调用次数上限 |
| `AURELIA_LLM_MAX_TOOL_CALLS_PER_TURN` | `int` | `48` | `llm/orchestrator.go:169` | 单轮全局工具调用总上限（§B4）：在各工具独立上限之上，限制单条消息跨所有工具的总调用开销——否则原生 provider 循环（maxIter=12）会允许模型每轮无限制地请求工具调用；深度研究场景会刻意大幅放宽此上限 |
| `AURELIA_LLM_MAX_TOOL_CALLS_PER_TURN_DEEP` | `int` | `150` | `llm/orchestrator.go:170` | 深度模式下每轮最大工具调用次数 |
| `AURELIA_LLM_TRUNC_ERR` | `int` | `2000` | `llm/orchestrator.go:416` | 存储在管理后台用量记录中的原始提供商错误信息的截断长度上限 |
| `AURELIA_LLM_TOOL_TIMEOUTS` | `duration` | `10*time.Second` | `llm/orchestrator.go:2326` | 工具调用超时时间 |
| `AURELIA_LLM_TOOL_TIMEOUTS_2` | `duration` | `15*time.Second` | `llm/orchestrator.go:2327` | 工具超时（配置项 2） |
| `AURELIA_LLM_TOOL_TIMEOUTS_3` | `duration` | `600*time.Second` | `llm/orchestrator.go:2329` | 第三方图像网关响应较慢，需要较宽的超时窗口 |
| `AURELIA_LLM_TOOL_TIMEOUT_DEFAULT` | `duration` | `100*time.Second` | `llm/orchestrator.go:2332` | 工具调用的默认超时 |
| `AURELIA_LLM_PROMPT_MAX_ITER` | `int` | `10` | `llm/prompt_tools.go:36` | 文本协议（tool_mode=prompt）工具循环在强制终止并给出“放弃”消息前的最大轮数上限。同文件中的文档注释仍写“最多 6 轮”——已过时，实际值为 10。仅影响 prompt 模式的模型。 |
| `AURELIA_LLM_PROMPT_MAX_RETRY` | `int` | `2` | `llm/prompt_tools.go:37` | 用于重新生成格式错误的 <tool_call> JSON（循环）以及重新执行失败工具（第 222 行的重试循环）的最大重试次数，重试之间无退避/等待。同一常量被两条不同的重试路径（第 185 行与第 222 行）复用。 |
| `AURELIA_LLM_PROMPT_MODE_TOOL_RESULT_SUMMARY_LENGTH` | `int` | `240` | `llm/prompt_tools.go:38` | 截断工具输出以生成 SSE `tool_result` 摘要及存储的 `tool_call` 块时保留的字符数（`truncate(output,240)`，第 242 行同用）；两处调用点（第 235、242 行）共用同一 240 字面量。 |
| `AURELIA_LLM_PROVIDER_REQUEST_BODY_MAX_BYTES` | `int` | `128*1024` | `llm/provider_request_capture.go:20` | 用于调试排查的脱敏 provider 请求体/序列化请求头保留的最大字节数，超出部分以「...[truncated]」截断 |
| `AURELIA_LLM_PROVIDER_REQUEST_VALUE_MAX_BYTES` | `int` | `8*1024` | `llm/provider_request_capture.go:21` | 单个已捕获值（URL、每个请求头值、每个 JSON 字符串）在截断前保留的最大字节数；8*1024。Base64 data: URI 会被替换为长度占位符，而非直接钳制。 |
| `AURELIA_LLM_P` | `int64` | `604800` | `llm/quota.go:30` | 模型分组配额记录 PeriodSeconds ≤0 时使用的兜底配额窗口时长（正常情况下窗口周期取自管理员设置的分组配额记录） |
| `AURELIA_LLM_IMAGE_DOCUMENT_FLAT_TOKEN_ALLOWANCE` | `int` | `1024` | `llm/quota.go:270` | 请求 Token 估算中图像/文档块（base64，非文本分词）的固定单块 Token 估算值；用于积分预检估算 |
| `AURELIA_LLM_OUTPUT_RESERVE` | `int` | `2000` | `llm/quota.go:313` | 预检积分计费轮次可负担性时，附加在预估输入之上的固定输出 token 预留量。预检可通过管理员 credit_preflight_enabled 关闭，但该 2k 预留量不可调整。 |
| `AURELIA_LLM_P_2` | `int64` | `604800` | `llm/quota.go:327` | 当分组的 `CreditPeriodSeconds` ≤ 0 时使用的限时额度兜底窗口长度；分组 `CreditPeriodSeconds` 可覆盖此默认窗口 |
| `AURELIA_LLM_MAX_TOK_3` | `int` | `512` | `llm/task_llm.go:31` | 最大输出 token 数（配置项 3） |
| `AURELIA_LLM_TITLE_GENERATION_WORD_CAP` | `int` | `8` | `llm/task_llm.go:32` | 标题生成字数上限 |
| `AURELIA_LLM_ROUTER_RETRIEVAL_QUERY_CAP` | `int` | `3` | `llm/task_llm.go:33` | 路由建议的检索查询条数上限 |
| `AURELIA_LLM_RESEARCH_CROSS_VALIDATE_FINDING_CAPS_CONFIRMED` | `int` | `8` | `llm/task_llm.go:34` | 研究交叉验证阶段（Phase 4）「已确认」结论数量上限 |
| `AURELIA_LLM_RESEARCH_CROSS_VALIDATE_FINDING_CAPS_DISPUTED` | `int` | `4` | `llm/task_llm.go:35` | 研究交叉验证中存疑结论的数量上限 |
| `AURELIA_LLM_RESEARCH_CROSS_VALIDATE_FINDING_CAPS_UNVERIFIED` | `int` | `6` | `llm/task_llm.go:36` | 研究交叉验证结论数量上限（未验证） |
| `AURELIA_LLM_MAX_CONCURRENT_TOOLS` | `int` | `4` | `llm/tool_exec.go:28` | execToolsConcurrent 中限制并发搜索/抓取工具调用数的信号量大小（make(chan struct{}, maxConcurrentTools)）。定义于 tool_exec.go，由 deep_research.go:738 使用以限制研究任务的并发扇出。 |
| `AURELIA_LLM_VCTX` | `duration` | `45*time.Second` | `llm/verify.go:77` | 限定次级验证/审计模型调用的 context.WithTimeout |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_7` | `int` | `800` | `llm/verify.go:94` | TaskVerify 对抗性事实核查 JSON 调用的 MaxOutputTokens。审核模型 ID 可由管理员配置（verify_model_id），但该 token 上限不可配置。 |


### 2. RAG 文档解析 / 向量检索

文档分块、Embedding 批量与并发、Qdrant 客户端、MinerU OCR 轮询等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_RAG_NET_DIALER_TIMEOUT` | `duration` | `15*time.Second` | `rag/embedder_http.go:34` | 网络拨号超时 |
| `AURELIA_RAG_NET_DIALER_KEEP_ALIVE` | `duration` | `30*time.Second` | `rag/embedder_http.go:35` | 网络拨号器保活（Keep-Alive）间隔 |
| `AURELIA_RAG_HTTP_TRANSPORT_MAX_IDLE_CONNS` | `int` | `50` | `rag/embedder_http.go:36` | HTTP 传输层最大空闲连接数 |
| `AURELIA_RAG_HTTP_TRANSPORT_MAX_IDLE_CONNS_PER_HOST` | `int` | `10` | `rag/embedder_http.go:37` | 每个主机的 HTTP 传输层最大空闲连接数 |
| `AURELIA_RAG_HTTP_TRANSPORT_IDLE_CONN_TIMEOUT` | `duration` | `90*time.Second` | `rag/embedder_http.go:38` | HTTP 传输层空闲连接超时 |
| `AURELIA_RAG_HTTP_TRANSPORT_TLSHANDSHAKE_TIMEOUT` | `duration` | `20*time.Second` | `rag/embedder_http.go:39` | HTTP 传输层 TLS 握手超时 |
| `AURELIA_RAG_HTTP_TRANSPORT_RESPONSE_HEADER_TIMEOUT` | `duration` | `30*time.Second` | `rag/embedder_http.go:40` | HTTP 传输层响应头超时 |
| `AURELIA_RAG_HTTP_TRANSPORT_EXPECT_CONTINUE_TIMEOUT` | `duration` | `1*time.Second` | `rag/embedder_http.go:41` | HTTP 传输层 Expect-Continue 超时 |
| `AURELIA_RAG_HTTP_CLIENT_TIMEOUT` | `duration` | `3*time.Minute` | `rag/embedder_http.go:42` | HTTP 客户端超时 |
| `AURELIA_RAG_TRUNCATE_AT_N` | `int` | `1200` | `rag/embedder_http.go:44` | 诊断预览文本的截断字符数上限 |
| `AURELIA_RAG_TRUNCATE_AT_N_2` | `int` | `16*1024` | `rag/embedder_http.go:45` | 诊断响应体截断长度上限（第 2 处） |
| `AURELIA_RAG_IO_LIMIT_READER` | `int64` | `4096` | `rag/embedder_http.go:47` | IO 读取字节数上限（LimitReader） |
| `AURELIA_RAG_IO_LIMIT_READER_2` | `int64` | `4096` | `rag/embedder_http.go:48` | IO 限制读取器上限（2） |
| `AURELIA_RAG_EMBEDDING_RETRY_DELAY` | `duration` | `time.Second` | `rag/embedder_http.go:50` | 嵌入请求重试延迟 |
| `AURELIA_RAG_EMBEDDING_RETRY_DELAY_2` | `duration` | `30*time.Second` | `rag/embedder_http.go:51` | 嵌入请求重试退避延迟上限 |
| `AURELIA_RAG_EMBEDDING_RETRY_DELAY_3` | `duration` | `1000*time.Millisecond` | `rag/embedder_http.go:52` | 嵌入请求第 3 次重试延迟 |
| `AURELIA_RAG_DASH_SCOPE_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:179` | DashScope 保持并发，但每个文档拆成两批可避免单个 OCR 密集型摄取任务独占工作区；进程级上限也约束了多个 RAG 工作协程同时嵌入时的总量。 |
| `AURELIA_RAG_DASH_SCOPE_GLOBAL_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:180` | DashScope 全局嵌入并发数 |
| `AURELIA_RAG_DASH_SCOPE_EMBED_ATTEMPT_TIMEOUT` | `duration` | `60*time.Second` | `rag/embedder_http.go:188` | DashScope 兼容模式偶尔会接受请求后卡在服务端排队中，长时间不返回响应头。将单次尝试的超时设置在共享客户端 3 分钟兜底上限之内，使索引失败时抛出可重试、可见的错误，而不是看起来卡住数分钟。该变量供测试使用。 |
| `AURELIA_RAG_EMBED_CONCURRENCY` | `int` | `4` | `rag/embedder_http.go:194` | embedConcurrency 限制同时运行的上游嵌入批量请求数。旧代码严格串行执行，500 个分块的文档需付出 50 次串行往返；应保持适中，因为 RAG 队列可同时处理多个文档，且每个文档都有独立的并发配额 |
| `AURELIA_RAG_MAX_ATTEMPTS` | `int` | `2` | `rag/embedder_http.go:445` | 最大重试次数 |
| `AURELIA_RAG_PDF_INSPECTION_TIMEOUT` | `duration` | `8*time.Second` | `rag/parser.go:56` | PDF 检测超时 |
| `AURELIA_RAG_OFFICE_XML_ZIP_ENTRY_READ_CAP` | `int64` | `16*1024*1024` | `rag/parser.go:253` | officeXMLZipEntryReadCap 限制单个 DOCX/PPTX zip 条目的读取大小。 |
| `AURELIA_RAG_PDF_INSPECTION_SAMPLE_LIMIT` | `int` | `3` | `rag/parser.go:333` | PDF 检测采样页数上限 |
| `AURELIA_RAG_PDF_THIN_CHARS_PER_PAGE` | `int` | `200` | `rag/parser.go:334` | PDF 判定「文本稀疏页」（触发 OCR 兜底）的每页字符数阈值 |
| `AURELIA_RAG_CMD_WAIT_DELAY` | `duration` | `500*time.Millisecond` | `rag/parser.go:375` | cmdWaitDelay 是上下文截止期限触发后、PDF 检测子进程被强制终止前的宽限期 |
| `AURELIA_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_IMAGE` | `int` | `5` | `rag/parser.go:438` | strongScan 图像与页面比例：默认 imageCount*5 >= pages*4（约每 80% 的页面对应一张图像）。两个乘数均可独立覆盖。 |
| `AURELIA_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_PAGE` | `int` | `4` | `rag/parser.go:439` | PDF 扫描件强特征判定的页数系数 |
| `AURELIA_RAG_MINERU_SOURCE_OBJECT_CLEANUP_TIMEOUT` | `duration` | `30*time.Second` | `rag/parser.go:603` | `mineruSourceObjectCleanupTimeout` 限定解析完成后尽力删除已上传给 MinerU 的源对象的超时时间，在解析结束后以新的 context 执行。 |
| `AURELIA_RAG_MINERU_POLL_DEADLINE` | `duration` | `20*time.Minute` | `rag/parser.go:606` | mineruPollDeadline 限制 MinerU 提取轮询循环的总时长上限 |
| `AURELIA_RAG_MINERU_SUBMIT_ERROR_BODY_TRUNCATION` | `int` | `256` | `rag/parser.go:695` | mineruSubmitErrorBodyTruncation 限制日志中保留的 MinerU 提交错误响应体大小。 |
| `AURELIA_RAG_MINERU_POLL_ERROR_BODY_TRUNCATION` | `int` | `256` | `rag/parser.go:757` | mineruPollErrorBodyTruncation 用于限制日志中保留的 MinerU 轮询错误响应体长度。 |
| `AURELIA_RAG_MINERUZIPCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:838` | 将 `![alt](images/foo.png)` 改写为规范中间形式 `![alt](mineru://foo.png)`；`runMinerUMarkdown` 会在文本进入分块、嵌入或入库前剥离这些标记。`mineruZipClient` 用于下载 MinerU 返回的结果压缩包；由于压缩包 URL 来自 API 响应（而非管理员配置），下载走 SSRF 安全客户端，在拨号阶段拦截私有/内网 IP（参见 §C6）。 |
| `AURELIA_RAG_FULL_MD_READ_CAP_INSIDE_ZIP` | `int64` | `32*1024*1024` | `rag/parser.go:841` | fullMdReadCapInsideZip 限制压缩包内 full.md 的读取大小上限 |
| `AURELIA_RAG_MAX_ZIP` | `int64` | `500*1024*1024` | `rag/parser.go:862` | 将下载大小限制在 500 MiB——普通文档的压缩包通常小于 100 MiB；超出此值视为异常，宁可报错也不让服务器 OOM。 |
| `AURELIA_RAG_MINERUCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:1002` | mineruClient 是用于云端 API 提交、轮询、下载三个环节的长超时 HTTP 客户端。60 秒连接超时加上通过请求上下文设置的单次调用截止期限，确保每次往返请求可控，而更大范围的轮询循环上限由 minerUPollTask 强制执行。 |
| `AURELIA_RAG_MINERU_SOURCE_TTLSECONDS` | `int` | `60*60` | `rag/parser.go:1008` | `mineruSourceTTLSeconds` 是提交给 MinerU 的文档预签名 URL 有效期，必须长于完整 OCR 窗口（轮询上限 20 分钟 + MinerU 排队时间）；设为 1 小时留有充裕余量，同时不会让对象长期滞留（解析完成后也会被显式删除）。 |
| `AURELIA_RAG_RAG_FAST_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:42` | rag-fast 摄取通道（文本/表格文档）的 asynq 工作协程并发数 |
| `AURELIA_RAG_RAG_SLOW_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:43` | rag（慢速）摄取通道（MinerU OCR 文档）的 asynq 工作协程并发数 |
| `AURELIA_RAG_INGEST_PIPELINE_TIMEOUT` | `duration` | `70*time.Minute` | `rag/rag.go:44` | RAG 摄取流水线（MinerU 解析 + 嵌入）的整体截止期限，通过 context.WithTimeout 应用，分别用于第 301 行（进程内兜底）和第 337 行（asynq 处理器）。 |
| `AURELIA_RAG_INGEST_TASK_TIMEOUT` | `duration` | `75*time.Minute` | `rag/rag.go:45` | `rag.ingest` 任务的 asynq 单任务超时（管道超时之上的外层边界），在第 279 行 `asynq.Timeout()` 中使用。 |
| `AURELIA_RAG_INGEST_UNIQUE_TTL` | `duration` | `80*time.Minute` | `rag/rag.go:46` | asynq 唯一性锁 TTL，用于对重复的摄取入队请求去重 |
| `AURELIA_RAG_INGEST_HEARTBEAT_INTERVAL` | `duration` | `30*time.Second` | `rag/rag.go:47` | 运行中的摄取工作协程更新文档心跳行的间隔；对应 startIngestHeartbeat 中的 ticker（第 417 行） |
| `AURELIA_RAG_INGEST_STALE_AFTER` | `duration` | `4*time.Minute` | `rag/rag.go:48` | 心跳超过该时长后，处理中（解析/嵌入）的文档将被判定为陈旧并被回收，用于第 241 行的 ClaimStaleIncompleteDocuments 调用。 |
| `AURELIA_RAG_INGEST_PENDING_STALE_AFTER` | `duration` | `ingestUniqueTTL` | `rag/rag.go:49` | 仍处于待处理（从未开始）状态的文档被回收前的等待时长，与唯一锁 TTL 复用同一取值，第 240 行使用。 |
| `AURELIA_RAG_INGEST_RECOVERY_INTERVAL` | `duration` | `time.Minute` | `rag/rag.go:50` | 持续性过期摄取恢复循环的定时轮询间隔 |
| `AURELIA_RAG_INGEST_FINALIZE_TIMEOUT` | `duration` | `30*time.Second` | `rag/rag.go:51` | 失败终止流程中 Qdrant 向量清理的截止期限，用于第 446 行 |
| `AURELIA_RAG_INGEST_ASYNQ_LEASE_MAX_RETRIES` | `int` | `1` | `rag/rag.go:52` | 用于租约/进程丢失恢复的 asynq MaxRetry（业务重试在内部处理），用于第 282 行的 asynq.MaxRetry()。 |
| `AURELIA_RAG_INGEST_ASYNQ_RETRY_DELAY` | `duration` | `2*time.Minute` | `rag/rag.go:53` | 租约丢失的任务重试前的固定 asynq 重试延迟，由第 138 行 `RetryDelayFunc` 返回。 |
| `AURELIA_RAG_INGEST_QUEUE_NAME` | `duration` | `2*time.Second` | `rag/rag.go:59` | 将文档分类到快/慢队列时 GetDocument 查询的截止期限 |
| `AURELIA_RAG_RUN_INGEST_WITH_RETRIES` | `int` | `3` | `rag/rag.go:60` | 整个流水线的重试次数（for attempt := 1; attempt <= 3），循环同时在第 391 行以 attempt < 3 作为守卫条件 |
| `AURELIA_RAG_RUN_INGEST_WITH_RETRIES_2` | `duration` | `3*time.Second` | `rag/rag.go:61` | 整条流水线重试之间的线性退避（3 秒 × 尝试次数），第 1 次尝试为 3 秒，第 2 次为 6 秒。 |
| `AURELIA_RAG_START_INGEST_HEARTBEAT` | `duration` | `5*time.Second` | `rag/rag.go:62` | 每次 `TouchDocumentIngest` 心跳写入的截止期限 |
| `AURELIA_RAG_FINALIZE_CHUNK_CLEANUP_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:63` | 摄取失败终止处理中删除分块记录的截止期限 |
| `AURELIA_RAG_FINALIZE_STATUS_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:64` | 清理完成后终态“失败”状态写入数据库的截止期限 |
| `AURELIA_RAG_EXTRACTION_FAILURE_REASON_CAP` | `int` | `500` | `rag/rag.go:65` | 存储在文档状态中的提取失败原因的最大字符数，对应第 595 行 if len(reason) > 500 的限制。 |
| `AURELIA_RAG_EMBEDDING_ERROR_TRUNCATE` | `int` | `4096` | `rag/rag.go:66` | 存入 `usage_logs` 的嵌入错误信息最大字符数 |
| `AURELIA_RAG_RETRIEVE` | `int` | `5` | `rag/rag.go:67` | RAG 查询嵌入向量的进程本地缓存 TTL |
| `AURELIA_RAG_DENSE_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:68` | 查询嵌入缓存被清空/重建前的最大条目数，第 948 行有粗略的上限检查 |
| `AURELIA_RAG_KEYWORD_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:69` | 调用方传入 topK <= 0 时使用的兜底检索片段数量。 |
| `AURELIA_RAG_SNIPPET_OF` | `int` | `240` | `rag/rag.go:70` | 每个检索范围分支从 Qdrant 请求的稠密向量 Top-N 命中数（注释中标注为「30 dense」，见第 1221 行） |
| `AURELIA_RAG_SPLIT_PARAGRAPHS_AND_TABLES` | `int` | `800` | `rag/rag.go:71` | 每个检索范围向 Qdrant 请求的关键词命中 Top-N 数量 |
| `AURELIA_RAG_ROUTER_CALL_TIMEOUT` | `duration` | `12*time.Second` | `rag/rag.go:72` | 用于稠密+关键词融合的倒数排名融合（RRF）常数 k，见 1/(rank+k) |
| `AURELIA_RAG_MAP_REDUCE_SUMMARISE` | `int` | `200` | `rag/rag.go:73` | 传入 max <= 0 时使用的默认片段字符长度。 |
| `AURELIA_RAG_COLLECT_DOC_HINTS` | `int` | `120` | `rag/rag.go:74` | 检索到的分块注入父窗口片段时的字节预算，在第 1117 行 `expandHit` 调用中使用 |
| `AURELIA_RAG_COLLECT_DOC_HINTS_2` | `int` | `12` | `rag/rag.go:75` | 嵌入子分块的目标字符数 |
| `AURELIA_RAG_QUERY_EMBED_TTL` | `duration` | `10*time.Minute` | `rag/rag.go:933` | 父级章节分块的目标/截断大小（字符数） |
| `AURELIA_RAG_QUERY_EMBED_MAX` | `int` | `4096` | `rag/rag.go:934` | 相邻子分块之间的滑动窗口重叠长度（字符数，约 12%）。 |
| `AURELIA_RAG_FUSE_RECIPROCAL_RANK` | `int` | `60` | `rag/rag.go:1427` | 含图片标记的段落仅在字符长度低于此值时才保持原子不拆分（`imageRe.MatchString(p) && len(p) < 800`） |
| `AURELIA_RAG_RETRIEVED_SNIPPET_CHARS` | `int` | `2000` | `rag/rag.go:1520` | 首 token 关键路径上任务模型查询路由 JSON 调用的截止期限 |
| `AURELIA_RAG_CHILD_TARGET_CHARS` | `int` | `2000` | `rag/rag.go:1757` | 送入 map-reduce 摘要器的每个分块组的估算 Token 数 |
| `AURELIA_RAG_PARENT_TARGET_CHARS` | `int` | `4800` | `rag/rag.go:1758` | map-reduce 中摘要分块组的最大数量。 |
| `AURELIA_RAG_CHUNK_OVERLAP_CHARS` | `int` | `250` | `rag/rag.go:1761` | 内嵌于提示词中的 map-reduce 局部摘要长度软上限（≤200 字），该字面量写在中文提示词字符串内 |
| `AURELIA_RAG_MAPREDUCE_GROUPTOKENS` | `int` | `6000` | `rag/rag.go:2334` | 作为路由文档提示展示的文档首段内容最大字符数 |
| `AURELIA_RAG_MAPREDUCE_MAXGROUPS` | `int` | `8` | `rag/rag.go:2335` | 为路由提示词组装的文档提示的最大数量 |
| `AURELIA_RAG_BATCH_SIZE` | `int` | `64` | `rag/vector_admin.go:136` | 管理员补建缺失向量时，每批嵌入+upsert 的分块数 |
| `AURELIA_VECTOR_QDRANT_HTTP_CLIENT_TIMEOUT` | `duration` | `20*time.Second` | `vector/qdrant.go:31` | 所有 Qdrant 请求的 http.Client 总体超时。 |
| `AURELIA_VECTOR_QDRANT_SCROLL_PAGE_SIZE_EXISTINGCHUNKIDS` | `int` | `256` | `vector/qdrant.go:32` | 枚举已存在分块 ID 时的 Qdrant scroll 分页大小 |
| `AURELIA_VECTOR_QDRANT_SCROLL_PAGE_SIZE_VECTORCHUNKSTATUSES` | `int` | `256` | `vector/qdrant.go:33` | 审计逐分块向量存在性时的 scroll 分页大小 |
| `AURELIA_VECTOR_DELETE_CONCURRENCY` | `int` | `4` | `vector/qdrant.go:460` | deleteByField 批量清理中每个集合的最大并发删除请求数 |


### 3. 沙盒代码执行

`python_execute` 沙盒。Go 侧变量需重启 API 进程生效；`SANDBOX_*` 变量作用于 `sandbox-service` 进程，需重启该服务生效。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `SANDBOX_MAX_OUTPUT_BYTES` | `int` | `32768` | `sandbox-service/app.py:120` | stdout/stderr 输出截断字节上限 |
| `SANDBOX_MAX_ARTIFACT_BYTES` | `int` | `20971520` | `sandbox-service/app.py:121` | 单个产出文件大小上限 |
| `SANDBOX_EXEC_READER_LOOP_POLL_S` | `float` | `0.2` | `sandbox-service/app.py:125` | 执行读取循环（_run_exec_bounded）：selector 轮询间隔、单次读取分块大小，以及循环结束后的进程等待宽限期。 |
| `SANDBOX_EXEC_READER_LOOP_CHUNK_BYTES` | `int` | `8192` | `sandbox-service/app.py:126` | 执行输出读取循环每次读取的分块字节数 |
| `SANDBOX_EXEC_READER_LOOP_WAIT_GRACE_S` | `float` | `2` | `sandbox-service/app.py:127` | 执行输出读取循环等待宽限时间（秒） |
| `SANDBOX_ARCHIVE_TAR_READ_POLL_S` | `float` | `5.0` | `sandbox-service/app.py:131` | 归档 tar 流读取循环：selector 轮询节奏、每次读取的分块大小，以及循环结束后进程等待的宽限期 |
| `SANDBOX_ARCHIVE_TAR_READ_CHUNK_BYTES` | `int` | `65536` | `sandbox-service/app.py:132` | 归档 Tar 读取分块字节数 |
| `SANDBOX_ARCHIVE_TAR_READ_WAIT_GRACE_S` | `float` | `10` | `sandbox-service/app.py:133` | 归档 tar 读取等待的宽限时间（秒） |
| `SANDBOX_S3_MAX_ATTEMPTS` | `int` | `3` | `sandbox-service/app.py:137` | 对象存储 SDK 超时/重试配置——为每次 SDK 调用设置边界，防止存储桶响应缓慢或挂起导致回收器、`DELETE` 操作或会话创建被卡死 |
| `SANDBOX_S3_CONNECT_TIMEOUT_S` | `float` | `10` | `sandbox-service/app.py:138` | S3 连接超时 |
| `SANDBOX_S3_READ_TIMEOUT_S` | `float` | `120` | `sandbox-service/app.py:139` | S3 读取超时（秒） |
| `SANDBOX_OSS_CONNECT_TIMEOUT_S` | `float` | `30` | `sandbox-service/app.py:140` | OSS 连接超时（秒） |
| `AURELIA_SANDBOX_MAX_SANDBOX_RESP_BYTES` | `int64` | `256<<20` | `sandbox/sandbox.go:162` | 解码后的沙箱 sidecar JSON 响应大小上限，用于防止异常或被攻破的 sidecar 导致 OOM（`256 << 20`） |
| `AURELIA_SANDBOX_EXEC_CLIENT_OVERHEAD` | `duration` | `120*time.Second` | `sandbox/sandbox.go:171` | 叠加到执行时长上限上以计算 HTTP 客户端超时，确保客户端不会在 sidecar 完成产物收集前提前超时 |
| `AURELIA_SANDBOX_SANDBOX_ERROR_BODY_READ_CAP` | `int64` | `64<<10` | `sandbox/sandbox.go:174` | 非 2xx 状态的 sidecar 错误响应在截断前读取的字节数；io.LimitReader(resp.Body, 64<<10)。 |


### 4. 内置工具（搜索 / Python / 网络安全）

web_search 结果条数与超时、Python 安全模式、SSRF / 网络安全护栏等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_NETSAFE_NETSAFE_SSRF_CLIENT_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `netsafe/netsafe.go:18` | 共享的 SSRF 安全/内网屏蔽 HTTP 客户端的 TCP 连接超时。net.Dialer{Timeout: 10*time.Second}。 |
| `AURELIA_NETSAFE_MAX_IDLE_CONNS` | `int` | `10` | `netsafe/netsafe.go:19` | SSRF 安全 HTTP 客户端的最大空闲长连接数 |
| `AURELIA_NETSAFE_IDLE_CONN_TIMEOUT` | `duration` | `30*time.Second` | `netsafe/netsafe.go:20` | SSRF 防护 HTTP 客户端的空闲长连接超时 |
| `AURELIA_NETSAFE_TLSHANDSHAKE_TIMEOUT` | `duration` | `10*time.Second` | `netsafe/netsafe.go:21` | SSRF 安全 HTTP 客户端的 TLS 握手超时。 |
| `AURELIA_NETSAFE_NETSAFE_REDIRECTS` | `int` | `5` | `netsafe/netsafe.go:22` | SSRF 安全客户端中止前允许的重定向跳数上限（每一跳都会重新校验），对应 if len(via) >= 5 -> too many redirects。 |
| `AURELIA_TOOLS_IN_TOP_K` | `int` | `5` | `tools/builtins.go:37` | 可通过环境变量覆盖的默认值；对应 AURELIA_* 变量未设置时回退为原硬编码值 |
| `AURELIA_TOOLS_WEB_FETCH_RESPONSE_BODY_READ_CAP` | `int64` | `256*1024` | `tools/builtins.go:38` | 网页抓取响应体读取上限 |
| `AURELIA_TOOLS_WEB_FETCH_EXTRACTED_TEXT_CHAR_CAP` | `int` | `32000` | `tools/builtins.go:39` | 网页抓取提取文本的字符数上限 |
| `AURELIA_TOOLS_PYTHON_EXECUTE_UPLOAD_STAGING_FILE_SIZE` | `int64` | `20*1024*1024` | `tools/builtins.go:40` | `python_execute` 工具上传暂存文件大小上限 |
| `AURELIA_TOOLS_PYTHON_EXECUTE_IMAGE_ARTIFACT_STAGING_SIZE` | `int64` | `20*1024*1024` | `tools/builtins.go:41` | Python 执行环境图片产物暂存大小 |
| `AURELIA_TOOLS_PYTHON_EXECUTE_STDOUT_STDERR_TRUNCATION_CAP` | `int` | `32*1024` | `tools/builtins.go:42` | Python 执行 stdout/stderr 截断上限 |
| `AURELIA_TOOLS_IN_N` | `int` | `4` | `tools/builtins.go:43` | 工具调用中结果数量参数 N 的上限 |
| `AURELIA_TOOLS_IN_SIZE` | `string` | `"1024x1024"` | `tools/builtins.go:44` | 图像生成默认尺寸 |
| `AURELIA_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW` | `duration` | `24*time.Hour` | `tools/builtins.go:45` | 每日图片限额重置周期 |
| `AURELIA_TOOLS_P` | `int64` | `604800` | `tools/builtins.go:46` | P（参数） |
| `AURELIA_TOOLS_IMAGE_IMAGE_INPUT_IMAGE_CAP` | `int` | `3` | `tools/builtins.go:47` | 图像工具单次调用可接受的输入图片数量上限 |
| `AURELIA_TOOLS_FETCHREMOTEIMAGE_DOWNLOAD_CAP` | `int64` | `32<<20` | `tools/builtins.go:48` | `fetchRemoteImage` 远程图片下载大小上限 |
| `AURELIA_TOOLS_IN_TOP_K_2` | `int` | `5` | `tools/builtins.go:49` | 检索 Top-K（配置项 2） |
| `AURELIA_TOOLS_CONFIDENCE` | `float` | `0.95` | `tools/builtins.go:50` | 置信度 |
| `AURELIA_TOOLS_MAX_IMG` | `int64` | `15*1024*1024` | `tools/builtins.go:240` | 图片下载数据大小上限 |
| `AURELIA_TOOLS_SSRFSAFECLIENT_OVERALL_TIMEOUT_WEB_FETCH` | `duration` | `25*time.Second` | `tools/net_safety.go:15` | 模型可控的网络请求（`web_fetch`、远程图片抓取）的 HTTP 客户端总超时（`netsafe.SafeClient(25*time.Second)`） |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `tools/net_safety.go:16` | 管理员配置的工具端点（搜索后端、图像网关）TCP 连接超时 |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_TLS_HANDSHAKE_TIMEOUT` | `duration` | `10*time.Second` | `tools/net_safety.go:17` | 管理员配置的工具端点的 TLS 握手超时。TLSHandshakeTimeout。 |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_RESPONSE_HEADER_TIMEOUT` | `duration` | `600*time.Second` | `tools/net_safety.go:18` | 等待工具端点响应头的超时时间（因图像生成较慢而设置较大）。ResponseHeaderTimeout: 600*time.Second；实际边界由各工具自身的 ctx 控制，未设置整体响应体超时。 |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_IDLE_CONN_TIMEOUT` | `duration` | `90*time.Second` | `tools/net_safety.go:19` | 工具 HTTP 客户端的空闲长连接超时（`IdleConnTimeout: 90*time.Second`） |


### 5. 会话 / 消息 / 流式 API

SSE 心跳、流恢复窗口、生成时长上限、分页与搜索上限、消息路径缓存等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_AUTH_USER_CACHE_TTL` | `duration` | `5*time.Minute` | `api/cache_helpers.go:13` | 每次受保护请求使用的已认证用户记录缓存 TTL |
| `AURELIA_API_AUTH_USER_CACHE_TTL_GROUP_ALREADY` | `duration` | `time.Second` | `api/cache_helpers.go:14` | 用户群组权益已过期时应用的兜底缓存 TTL（近乎立即重新检查）。return time.Second；当距群组到期时间小于 5 分钟时，也会将 TTL 钳制为该剩余时间。 |
| `AURELIA_API_LIMIT_2` | `int` | `200` | `api/conversations_handlers.go:26` | GET /conversations 在未指定 ?limit 时的默认每页条数。 |
| `AURELIA_API_LIMIT_3` | `int` | `500` | `api/conversations_handlers.go:32` | `GET /conversations` 的 `?limit` 参数上限 |
| `AURELIA_API_SEARCH_MESSAGE_HIT_LIMIT` | `int` | `40` | `api/conversations_handlers.go:99` | SearchConversations 返回的消息内容命中数上限 |
| `AURELIA_API_IMPORT_MAX_CONVERSATIONS` | `int` | `1000` | `api/conversations_handlers.go:164` | 单次批量导入请求中可调度的最大会话数 |
| `AURELIA_API_IMPORT_MAX_MESSAGES_PER_CONV` | `int` | `10000` | `api/conversations_handlers.go:165` | 批量导入时每个会话的最大消息数。 |
| `AURELIA_API_IMPORT_MAX_CONTENT_BYTES` | `int` | `200*1024` | `api/conversations_handlers.go:166` | 导入时单条消息内容的字节上限，超出部分会被截断 |
| `AURELIA_API_INLINE_THREAD_QUOTE_CAP` | `int` | `4000` | `api/conversations_handlers.go:282` | 作为内联线程系统上下文注入的引用摘录字符数（rune）上限 |
| `AURELIA_API_GETCONVERSATION_ACTIVE_PATH_LIMIT` | `int` | `200` | `api/conversations_handlers.go:345` | GET /conversations/:id 中反向分页活动路径 ?limit 参数的上限钳制 |
| `AURELIA_API_LIMIT_4` | `int` | `30` | `api/conversations_handlers.go:510` | GET /conversations/:id/messages 的默认末尾窗口大小。 |
| `AURELIA_API_LISTMESSAGES_PAGE_LIMIT` | `int` | `200` | `api/conversations_handlers.go:512` | 活跃路径消息分页 `?limit` 参数上限 |
| `AURELIA_API_P` | `duration` | `604800*time.Second` | `api/credits_handlers.go:18` | 群组 CreditPeriodSeconds 小于等于 0 时，积分消耗/刷新窗口的兜底时长（7 天）；同时作为第 39 行的窗口级缓存 TTL。实际取值来自各群组的 CreditPeriodSeconds（管理端逐群组字段，schema 默认 604800）；此字面量仅为安全兜底。相同的 604800 兜底值在 llm/quota.go、store/quotas.go、tools/builtins.go、models_handlers.go 中重复出现。 |
| `AURELIA_API_RATE_LIMIT_USER` | `int` | `20` | `api/kbs_handlers.go:13` | 知识库文档单用户上传频率限制：滚动 1 分钟窗口内 20 次上传（§C4） |
| `AURELIA_API_CONFIDENCE` | `float` | `0.95` | `api/memories_handlers.go:13` | 用户通过 API 手动创建记忆时标记的置信度分数 |
| `AURELIA_API_MAX_GEN_DURATION` | `duration` | `90*time.Minute` | `api/messages_handlers.go:27` | 分离式生成轮次（占用并发槽位）的硬性兜底超时上限 |
| `AURELIA_API_SSE_PING_HEARTBEAT_POST` | `duration` | `15*time.Second` | `api/messages_handlers.go:32` | postMessage 期间保持 SSE 连接畅通的心跳 ping 定时器间隔 |
| `AURELIA_API_SSE_PING_HEARTBEAT_REGENERATE` | `duration` | `15*time.Second` | `api/messages_handlers.go:33` | 15 秒心跳，防止代理关闭 SSE 通道（§6.2） |
| `AURELIA_API_SSE_PING_HEARTBEAT_STREAM` | `duration` | `15*time.Second` | `api/messages_handlers.go:34` | 每次 genstream.Read 刷新读取的最大缓冲 SSE 事件数。 |
| `AURELIA_API_STREAM_STATUS_RECHECK_INTERVAL` | `duration` | `5*time.Second` | `api/messages_handlers.go:35` | `streamMessage` 跟随循环中的心跳 ping 间隔 |
| `AURELIA_API_STREAM_REPLAY_BATCH_SIZE` | `int` | `200` | `api/messages_handlers.go:36` | 重新轮询消息状态的定时器间隔（用于在缺少 pub/sub 事件时捕获终止状态） |
| `AURELIA_API_ONLINE_PRESENCE_TOUCH_THROTTLE` | `duration` | `time.Minute` | `api/middleware.go:23` | 可通过环境变量覆盖的默认值（参见 § 配置参考）；对应 `AURELIA_*` 变量未设置时均回退到原硬编码值。 |
| `AURELIA_API_CONCURRENT_GEN_SLOT_SAFETY_TTL` | `duration` | `30*time.Minute` | `api/middleware.go:24` | 并发生成槽位的安全 TTL |
| `AURELIA_API_REQUEST_SIGNATURE_REPLAY_WINDOW_FUTURE` | `int64` | `300` | `api/middleware.go:25` | 请求签名重放窗口（未来时间容差） |
| `AURELIA_API_REQUEST_SIGNATURE_REPLAY_WINDOW_PAST` | `int64` | `60` | `api/middleware.go:26` | 请求签名重放校验允许的最大历史时间偏差（过去方向）。 |
| `AURELIA_API_CREDIT_MULTIPLIER` | `float` | `5.0` | `api/models_handlers.go:21` | 将模型（输入+输出）价格映射为选择器中显示的积分倍率的除数：合计价格为 $5 对应 x1.0。结果四舍五入保留一位小数（v*10/10）。仅作展示用的积分费率参考，不可覆盖。 |
| `AURELIA_API_P_2` | `int64` | `604800` | `api/models_handlers.go:22` | 当配额记录的 `PeriodSeconds` ≤ 0 时，统计模型每周期免费额度使用量的兜底窗口（7 天）。实际窗口取自按分组/模型配置的配额记录（管理员可配置）；该字面量仅作兜底值。 |
| `AURELIA_API_JSON_REQUEST_BODY_SIZE_CAP` | `int64` | `4<<20` | `api/mux.go:16` | 任意 JSON 请求体可读取的最大字节数（`MaxBytesReader`），用于防止内存耗尽（默认 4<<20）；备份导入在别处另有独立的 2GB 上限。 |
| `AURELIA_API_PROJECT_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `200` | `api/projects_handlers.go:15` | 加载单个项目详情视图时返回的活跃对话数上限（limit 200，offset 0），不向调用方暴露分页 |
| `AURELIA_API_RATE_LIMIT_USER_2` | `int` | `20` | `api/projects_handlers.go:16` | 项目库文档的每用户上传频率上限：每 1 分钟窗口 20 次上传（§C4）。窗口 = time.Minute。与 kbs_handlers.go:132 保持一致；不可覆盖。 |
| `AURELIA_API_RATE_LIMIT_REGISTER_MAX` | `int` | `5` | `api/router.go:51` | 可通过环境变量覆盖的按 IP 频率限制额度（「每 <窗口> 允许 <N> 次」）及 CORS 预检缓存时长。默认值与历史硬编码值一致，可通过成对的 `AURELIA_API_RATE_LIMIT_*_MAX` / `*_WINDOW` 变量调整（见 `docs/config-reference.md`），未设置时不改变原有行为。 |
| `AURELIA_API_RATE_LIMIT_REGISTER_WINDOW` | `duration` | `60*time.Second` | `api/router.go:52` | 注册接口频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_LOGIN_MAX` | `int` | `10` | `api/router.go:54` | 登录请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_LOGIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:55` | 登录接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_LOGIN_2FA_MAX` | `int` | `10` | `api/router.go:57` | 登录二次验证（2FA）请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_LOGIN_2FA_WINDOW` | `duration` | `60*time.Second` | `api/router.go:58` | 登录 2FA 频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_LOGOUT_MAX` | `int` | `30` | `api/router.go:60` | 登出请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_LOGOUT_WINDOW` | `duration` | `60*time.Second` | `api/router.go:61` | 登出接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_REFRESH_MAX` | `int` | `30` | `api/router.go:63` | 令牌刷新请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_REFRESH_WINDOW` | `duration` | `60*time.Second` | `api/router.go:64` | 令牌刷新频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_VERIFY_EMAIL_MAX` | `int` | `10` | `api/router.go:66` | 邮箱验证码请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_VERIFY_EMAIL_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:67` | 邮箱验证接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_SEND_CODE_MAX` | `int` | `3` | `api/router.go:69` | 验证码发送请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_SEND_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:70` | 验证码发送频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_FORGOT_PASSWORD_MAX` | `int` | `5` | `api/router.go:72` | 忘记密码请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_FORGOT_PASSWORD_WINDOW` | `duration` | `15*60*time.Second` | `api/router.go:73` | 忘记密码接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_RESET_PASSWORD_MAX` | `int` | `5` | `api/router.go:75` | 密码重置请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_RESET_PASSWORD_WINDOW` | `duration` | `60*time.Second` | `api/router.go:76` | 重置密码频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_ISSUE_MAX` | `int` | `30` | `api/router.go:78` | 验证码签发请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_ISSUE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:79` | 验证码签发接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_VERIFY_MAX` | `int` | `60` | `api/router.go:81` | 验证码校验请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_VERIFY_WINDOW` | `duration` | `60*time.Second` | `api/router.go:82` | 验证码校验频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_FIRST_RUN_SETUP_MAX` | `int` | `10` | `api/router.go:84` | 首次运行初始化请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_FIRST_RUN_SETUP_WINDOW` | `duration` | `60*time.Second` | `api/router.go:85` | 首次运行初始化接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_MAX` | `int` | `60` | `api/router.go:87` | 公开分享对话访问频率限制上限 |
| `AURELIA_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_WINDOW` | `duration` | `60*time.Second` | `api/router.go:88` | 公开分享对话频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_MAX` | `int` | `240` | `api/router.go:90` | 共享资源/文件/制品访问请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_WINDOW` | `duration` | `60*time.Second` | `api/router.go:91` | 共享资源/文件/产物接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_MAX` | `int` | `20` | `api/router.go:93` | OAuth 发起/回调/交接请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_WINDOW` | `duration` | `60*time.Second` | `api/router.go:94` | OAuth 发起/回调交接频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_PASSWORD_CHANGE_SET_MAX` | `int` | `5` | `api/router.go:96` | 密码修改/设置请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_PASSWORD_CHANGE_SET_WINDOW` | `duration` | `60*time.Second` | `api/router.go:97` | 密码修改/设置接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_IDENTITY_LINK_START_MAX` | `int` | `20` | `api/router.go:99` | 账号绑定发起请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_IDENTITY_LINK_START_WINDOW` | `duration` | `60*time.Second` | `api/router.go:100` | 身份关联发起频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_MAX` | `int` | `10` | `api/router.go:102` | 2FA 设置/启用/禁用请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:103` | 2FA 设置/启用/禁用接口频率限制窗口 |
| `AURELIA_API_RATE_LIMIT_REDEEM_CODE_MAX` | `int` | `10` | `api/router.go:105` | 兑换码兑换请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_REDEEM_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:106` | 兑换码兑换频率限制时间窗口 |
| `AURELIA_API_RATE_LIMIT_WORKSPACE_JOIN_MAX` | `int` | `30` | `api/router.go:108` | 加入工作区请求频率限制上限 |
| `AURELIA_API_RATE_LIMIT_WORKSPACE_JOIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:109` | 工作区加入接口频率限制窗口 |
| `AURELIA_API_CORS_PREFLIGHT_CACHE_AGE` | `duration` | `600*time.Second` | `api/router.go:111` | CORS 预检请求缓存时长 |
| `AURELIA_API_SELF_USAGE_LOOKBACK_WINDOW` | `int` | `30` | `api/user_handlers.go:209` | `/api/me/usage` 汇总用户消息数量所回溯的天数窗口（`days := 30`） |
| `AURELIA_API_LIMIT` | `int` | `200` | `api/workspaces_handlers.go:15` | 管理端“列出全部工作区”接口在未提供 ?limit/?offset 查询参数时的默认 limit（200）/offset（0）。调用方可通过 ?limit 传入任意正整数覆盖（无上限钳制）；默认值 200 为硬编码。 |
| `AURELIA_API_ADMIN_WORKSPACE_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `500` | `api/workspaces_handlers.go:16` | 管理员工作区分诊详情视图加载的最大会话数（limit 500, offset 0），为 store.ListWorkspaceConversations 的固定参数，不可覆盖。 |
| `AURELIA_GENSTREAM_TTL` | `duration` | `2*time.Hour` | `genstream/genstream.go:14` | 单条消息的 SSE 事件流（`gen:<id>`）在缓存中保留多久以支持断线重连/回放，过期后失效。实际上即为流恢复窗口，不支持环境变量/管理员覆盖，每次 `StreamAppend` 都会应用该值。 |
| `AURELIA_MSGCACHE_PATH_TTL` | `duration` | `45*time.Second` | `msgcache/msgcache.go:15` | 会话活动路径消息列表（conv:path:...）的短期缓存 TTL。 |
| `AURELIA_MSGCACHE_MESSAGE_CACHE_VERSION_KEY_TTL` | `duration` | `10*time.Minute` | `msgcache/msgcache.go:16` | `conv:ver` 版本计数器的 TTL，用于在数据发生变更时使已缓存的消息路径失效。通过 `Bump()->Incr(versionKey, 10*time.Minute)` 设置。 |
| `AURELIA_QUEUE_IN_PROCESS_WORKERS` | `int` | `8` | `queue/queue.go:47` | 进程内后台任务池的固定工作协程数 |
| `AURELIA_QUEUE_PROCESS_JOB_BUFFER` | `int` | `256` | `queue/queue.go:48` | 进程内任务通道的缓冲容量 |
| `AURELIA_QUEUE_QUEUE_BACKPRESSURE_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:49` | 背压兜底路径中同步执行任务的上下文超时。 |
| `AURELIA_QUEUE_QUEUE_WORKER_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:50` | worker 循环中单个任务的 context 超时（需长于 MinerU OCR 最长 20 分钟的处理时间） |


### 6. 认证 / 会话 / 验证码

令牌缓存、验证码有效期与尝试次数、TOTP 时窗、OAuth state TTL 等（不含约定俗成的格式常量，如验证码位数）。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_MAX_CODE_ATTEMPTS` | `int` | `5` | `api/auth_handlers.go:24` | 6 位验证/重置码在被作废前允许的错误尝试次数，与 6 位验证码空间配套，无环境变量/管理员覆盖项。 |
| `AURELIA_API_CODE_FAILURE_COUNTER_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:28` | 按邮箱统计的验证码错误尝试计数器（`codefail:`）存活时长，与验证码 TTL 共用同一取值（`10*time.Minute`） |
| `AURELIA_API_MINIMUM_PASSWORD_LENGTH` | `int` | `8` | `api/auth_handlers.go:29` | 注册/初始设置/重置/修改/设置密码时接受的最小密码长度 |
| `AURELIA_API_EMAIL_VERIFICATION_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:30` | 邮件发送的 6 位账户验证码的有效期；另见 auth_handlers.go:294（重新发送） |
| `AURELIA_API_PASSWORD_RESET_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:31` | 邮件发送的 6 位密码重置码的有效期，同见 auth_handlers.go:350（forgotPassword）。 |
| `AURELIA_API_CAP_TOL` | `float` | `0.04` | `api/captcha.go:41` | 提交的拖放比例与真实缺口比例之间可接受的误差（约合 228px 轨道上的 9px），刻意放宽容差，真正的滥用防护由每 IP 每日上限承担 |
| `AURELIA_API_CAPTCHA_CHALLENGE_CACHE_TTL` | `duration` | `5*time.Minute` | `api/captcha.go:44` | 已签发的滑块验证码挑战在过期前保持可解的时长；5*time.Minute |
| `AURELIA_API_CAPTCHA_PASS_TTL` | `duration` | `10*time.Minute` | `api/captcha.go:110` | 证明验证码刚被解答的无状态 HMAC 通行凭证的有效期，用于限制注册时对已解答验证码凭证的重放。 |
| `AURELIA_API_OAUTH_2FA_HANDOFF_COOKIE_TTL` | `duration` | `300*time.Second` | `api/oauth_handlers.go:24` | OAuth 登录时携带 TOTP 凭据的 HttpOnly `aurelia_2fa` Cookie 的 Max-Age（`MaxAge:300`） |
| `AURELIA_API_OAUTH_STATE_CACHE_TTL` | `duration` | `10*time.Minute` | `api/oauth_handlers.go:25` | 暂存的 OAuth state（含 PKCE verifier/来源）用于完成回调的有效期 |
| `AURELIA_API_OAUTH_TOKEN_EXCHANGE_CONTEXT_TIMEOUT` | `duration` | `20*time.Second` | `api/oauth_handlers.go:26` | 回调中覆盖“授权码换令牌”及后续获取用户信息的截止期限；context.WithTimeout 20*time.Second |
| `AURELIA_API_OAUTH_CROSS_DOMAIN_HANDOFF_TOKEN_TTL` | `duration` | `60*time.Second` | `api/oauth_handlers.go:27` | 将已完成登录状态交回源域名的一次性令牌的有效期，60*time.Second，仅可使用一次。 |
| `AURELIA_API_2FA_LOGIN_TICKET_BURN_THRESHOLD` | `int64` | `5` | `api/twofa_handlers.go:24` | 登录票据被销毁前允许的 TOTP 验证码错误次数 |
| `AURELIA_API_ISSUE_TWOFA_TICKET` | `duration` | `5*time.Minute` | `api/twofa_handlers.go:25` | 在提供 TOTP 验证码之前，代表已验证密码状态的短期票据的有效期；5*time.Minute |
| `AURELIA_OAUTH_HTTP_CLIENT` | `duration` | `15*time.Second` | `oauth/oauth.go:64` | 所有第三方 token/用户信息请求共用的 `http.Client` 单次请求超时（`http.Client{Timeout:15*time.Second}`） |
| `AURELIA_OAUTH_OAUTH_PROVIDER_RESPONSE_BODY_CAP` | `int64` | `1<<20` | `oauth/oauth.go:68` | 从 OAuth 提供方 token/userinfo 响应体读取的最大字节数 |
| `AURELIA_OAUTH_APPLE_CLIENT_SECRET_JWT_EXPIRY` | `duration` | `30*time.Minute` | `oauth/oauth.go:71` | 作为 Apple 动态客户端密钥签发的 ES256 JWT 的 exp 有效期；now.Add(30*time.Minute) |
| `AURELIA_OAUTH_SNIPPET` | `int` | `200` | `oauth/oauth.go:74` | 日志/错误信息中保留的第三方提供商错误响应体最大字符数，len(s)>200 时截断。 |


### 7. 上传 / 文件 / 分享

图片处理、存储清理周期、直传分片、分享令牌、下载缓存 TTL 等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_ICON_SERVING_CACHE_AGE` | `duration` | `86400*time.Second` | `api/admin_uploads.go:29` | 管理端模型图标上传的硬性字节上限（multipart 解析上限 + LimitReader 上限）。ParseMultipartForm 使用 maxIconBytes+1024（第 109 行）；LimitReader 使用 maxIconBytes+1（第 132 行）。 |
| `AURELIA_API_ADMIN_ICON_UPLOAD_SIZE` | `int64` | `256*1024` | `api/admin_uploads.go:60` | 模型图标响应的 Cache-Control public max-age —— 24 小时。 |
| `AURELIA_API_AUDIO_TRANSCRIPTION_UPSTREAM_HTTP_TIMEOUT` | `duration` | `120*time.Second` | `api/audio_handlers.go:21` | 调用 OpenAI 兼容端点 /v1/audio/transcriptions 转录接口的 HTTP 客户端超时 |
| `AURELIA_API_AUDIO_TRANSCRIPTION_USER_RATE_LIMIT` | `int` | `20` | `api/audio_handlers.go:26` | 每用户每滚动窗口的最大转写请求数，超出触发 429；窗口 = 1 分钟，作用域键为 “audio”。 |
| `AURELIA_API_TRANSCRIPTION_UPSTREAM_RESPONSE_READ_CAP` | `int64` | `1<<20` | `api/audio_handlers.go:27` | 从转录上游响应体读取字节数的 io.LimitReader 上限。 |
| `AURELIA_API_TRANSCRIPTION_UPSTREAM_ERROR_TRUNCATION_LENGTH` | `int` | `240` | `api/audio_handlers.go:28` | 502 错误信息中回显的上游错误响应体最大字符数 |
| `AURELIA_API_UPLOAD_RATE_LIMIT_MAX` | `int` | `20` | `api/files_handlers.go:25` | 上传请求频率限制上限 |
| `AURELIA_API_UPLOAD_RATE_LIMIT_WINDOW` | `duration` | `time.Minute` | `api/files_handlers.go:26` | 文件上传接口频率限制窗口 |
| `AURELIA_API_ARTIFACT_CACHE_TTL` | `duration` | `31536000*time.Second` | `api/files_handlers.go:27` | Artifact 缓存 TTL |
| `AURELIA_API_UPLOADED_FILE_CACHE_TTL` | `duration` | `86400*time.Second` | `api/files_handlers.go:28` | 已上传文件缓存 TTL |
| `AURELIA_API_OBJECT_STORAGE_DELETE_TIMEOUT_CLEANUP` | `duration` | `30*time.Second` | `api/storage_cleanup.go:17` | 清理无引用存储对象时，每次对象存储 `Delete` 调用的 `context.WithTimeout` 截止期限 |
| `AURELIA_STORAGE_S3_DIRECT_UPLOAD_MIN_CLIENT_TIMEOUT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:28` | 直传 / OSS 相关可调参数（可通过环境变量覆盖；默认值保持原有硬编码行为） |
| `AURELIA_STORAGE_DIRECT_S3_OSS_UPLOAD_HTTP_CLIENT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:29` | S3/OSS 直传 HTTP 客户端超时 |
| `AURELIA_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_CONNECT` | `int64` | `30` | `storage/s3_direct.go:30` | 阿里云 OSS 客户端连接/读取超时中的连接超时 |
| `AURELIA_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_RW` | `int64` | `300` | `storage/s3_direct.go:31` | 阿里云 OSS 客户端读写超时 |
| `AURELIA_STORAGE_PRESIGN_URL_TTL` | `duration` | `3600*time.Second` | `storage/s3_direct.go:32` | 预签名 URL 有效期 |
| `AURELIA_STORAGE_PRESIGN_URL_TTL_CLAMP_CEILING` | `duration` | `86400*time.Second` | `storage/s3_direct.go:33` | 预签名 URL 有效期的钳制上限 |
| `AURELIA_STORAGE_SIDECAR_STORAGE_CLIENT_HTTP_TIMEOUT` | `duration` | `5*time.Minute` | `storage/storage.go:31` | 基于 sidecar 的 /storage/put 与 /storage/delete 往返请求的 http.Client.Timeout（按约 200 MB 的 MinerU PDF 设定）。Sidecar Put 默认 TTL（1 小时）/上限（24 小时）在 sidecar 端强制执行，不在此 Go 代码中。 |


### 8. 管理后台任务（备份 / 向量维护 / 兑换码）

备份大小上限与异步轮询、向量维护批量、兑换码上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_BACKUP_EXPORT_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_backup_async.go:26` | 内存管理器中保留的近期备份导出任务记录的最大数量，超出后淘汰最旧记录，对应 for len(m.order) > 20 { evict }。 |
| `AURELIA_API_BACKUP_EXPORT_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_backup_async.go:27` | 完整异步备份导出任务（数据库读取 + 压缩打包 + Qdrant 导出）的 context 截止期限（`context.WithTimeout(context.Background(), 12*time.Hour)`） |
| `AURELIA_API_CONFIG_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `16<<20` | `api/admin_backup_handlers.go:25` | 可调参数——支持通过 envcfg 覆盖；默认值保持原有行为不变 |
| `AURELIA_API_BACKUP_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `32<<20` | `api/admin_backup_handlers.go:26` | 备份导入 multipart 内存缓冲区大小 |
| `AURELIA_API_MAX_CONFIG_SIZE` | `int64` | `512<<20` | `api/admin_backup_handlers.go:373` | 512 MiB；配置归档文件通常很小。 |
| `AURELIA_API_QDRANT_ARCHIVE_REQUEST_TIMEOUT` | `duration` | `5*time.Minute` | `api/admin_backup_qdrant.go:26` | 每个 Qdrant 备份/恢复请求（list/scroll/upsert/index）的 http.Client.Timeout，常量 qdrantArchiveRequestTimeout = 5 * time.Minute，用于第 58 行。 |
| `AURELIA_API_QDRANT_ERROR_BODY_READ_CAP` | `int64` | `1<<20` | `api/admin_backup_qdrant.go:27` | 读取非 2xx 的 Qdrant 错误响应体用于生成错误信息时的 `LimitReader` 上限（`io.LimitReader(resp.Body, 1<<20)`） |
| `AURELIA_API_QDRANT_EXPORT_SCROLL_PAGE_SIZE` | `int` | `256` | `api/admin_backup_qdrant.go:28` | 将 Qdrant 集合导出至归档时，每页 /points/scroll 拉取的点数量 |
| `AURELIA_API_QDRANT_IMPORT_UPSERT_FLUSH_BATCH_SIZE` | `int` | `128` | `api/admin_backup_qdrant.go:29` | Qdrant 恢复过程中，/points?wait=true upsert 刷新前累积的点数；if len(batch) >= 128 { flush }，批次初始容量在第 315 行也是 128 |
| `AURELIA_API_ADMIN_USER_LIST_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:20` | 管理员用户列表默认每页条数，钳制上限为 200 |
| `AURELIA_API_ADMIN_CREATED_USER_MIN_PASSWORD_LENGTH` | `int` | `8` | `api/admin_handlers.go:21` | 管理员创建新账户时密码的最小字符长度；if len(req.Password) < 8 { reject } |
| `AURELIA_API_ADMIN_PASSWORD_RESET_MIN_LENGTH` | `int` | `8` | `api/admin_handlers.go:22` | 管理员为用户重置密码时的最小长度要求，对应 if len(req.NewPassword) < 8 { reject }。 |
| `AURELIA_API_ADMIN_USER_CONVERSATIONS_LISTING_CAP` | `int` | `500` | `api/admin_handlers.go:23` | 客服/问题排查场景中列出目标用户对话时的硬编码行数上限（`store.ListConversations(..., 500, 0)`） |
| `AURELIA_API_USAGE_REPORT_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:24` | 管理员用量报表默认每页条数，钳制上限为 200 |
| `AURELIA_API_ANALYTICS_WINDOW` | `int` | `30` | `api/admin_handlers.go:25` | 管理端分析仪表盘的默认回溯窗口（天数）；days := 30 |
| `AURELIA_API_ANALYTICS_WINDOW_2` | `int` | `365` | `api/admin_handlers.go:26` | ?days 分析回溯窗口的上限，对应 n > 0 && n <= 365。 |
| `AURELIA_API_ANALYTICS_BREAKDOWN_TOP_N` | `int` | `8` | `api/admin_handlers.go:27` | 分析明细及趋势中包含的热门模型与热门用户数量（`AdminUsageBreakdown(..., 8)`，分别用于第 912 行 `model_id`、第 913 行 `user_id`） |
| `AURELIA_API_BULK_REDEEM_CODE_GENERATION_QUANTITY` | `int` | `1000` | `api/admin_redeem_handlers.go:18` | 单次批量兑换码创建请求生成兑换码数量的上限，对应 if body.Quantity < 0 \|\| body.Quantity > 1000 { reject }。 |
| `AURELIA_API_MAX_SKILL_ASSET_BYTES` | `int64` | `20*1024*1024` | `api/admin_skill_assets.go:24` | 管理员技能资源上传单文件字节数上限（同时决定 multipart 缓冲区 +4096 及 LimitReader +1） |
| `AURELIA_API_VECTOR_MAINTENANCE_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_vectors_handlers.go:18` | 内存中保留的最近向量检查/重建任务记录数上限，超出后淘汰最旧记录 |
| `AURELIA_API_VECTOR_MAINTENANCE_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_vectors_handlers.go:19` | 异步向量审计/重建任务的上下文截止期限；context.WithTimeout(context.Background(), 12*time.Hour) |


### 9. 数据库 / 缓存 / 后台队列

数据库连接池、设置缓存 TTL、Redis 超时与连接池、后台任务队列容量与并发等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_CACHE_MEMORY_PUB_SUB_SUBSCRIBER_CHANNEL_BUFFER` | `int` | `16` | `cache/cache.go:15` | 开发环境内存缓存中每个 `Subscribe` channel 的缓冲容量；非阻塞 `Publish` 在缓冲区满时会丢弃消息 |
| `AURELIA_CACHE_MEMORY_STREAM_EVENT_RETENTION_CAP` | `int` | `50000` | `cache/cache.go:16` | 内存流每个流保留的最大事件数上限；追加时旧事件裁剪至最近 50k 条（淘汰边界） |
| `AURELIA_CACHE_MEMORY_STREAMREAD_PAGE_LIMIT` | `int` | `100` | `cache/cache.go:17` | 调用方传入 limit<=0 时，StreamRead 返回的流事件数量兜底值。与 redis.go:183 保持一致。 |
| `AURELIA_CACHE_REDIS_OPERATION_COMMAND_TIMEOUT` | `duration` | `3*time.Second` | `cache/redis.go:20` | `NewRedis` 中单次 `Ping` 的 context 截止期限，使配置错误或不可达的 Redis 能在启动时快速失败。仅拨号 URL 来自环境变量 `REDIS_URL`（通过 `redis.ParseURL` 解析），该 ping 超时字面量本身不支持环境变量/管理员覆盖。 |
| `AURELIA_CACHE_REDIS_STARTUP_PING_TIMEOUT` | `duration` | `5*time.Second` | `cache/redis.go:35` | 应用于每条 Redis 命令（Get/Set/Delete/Incr/IncrBy/Decr/Publish/XAdd/XRangeN）的上下文超时 |
| `AURELIA_CACHE_REDIS_PUB_SUB_SUBSCRIBER_CHANNEL_BUFFER` | `int` | `16` | `cache/redis.go:128` | 桥接 Redis 发布/订阅的单个订阅者输出通道的缓冲容量；消费过慢时超出此深度的消息将被丢弃。与内存实现（cache.go:198）保持一致。 |
| `AURELIA_CACHE_REDIS_GENERATION_STREAM_MAXLEN_CAP` | `int64` | `50000` | `cache/redis.go:174` | XADD MaxLen（Approx=true）—— 将 Redis 中生成回放流限制在约 5 万条事件以内，与 cache.go:237 中的内存上限保持一致，不可覆盖。 |
| `AURELIA_CACHE_REDIS_STREAMREAD_PAGE_LIMIT` | `int` | `100` | `cache/redis.go:188` | 调用方传入 `limit<=0` 时，`StreamRead` 返回流事件数量的兜底值，与 `cache.go:247` 保持一致 |
| `AURELIA_STORE_LISTCONVERSATIONS_LIMIT` | `int` | `200` | `store/conversations.go:17` | `limit<=0` 时使用的默认分页大小 |
| `AURELIA_STORE_LISTCONVERSATIONS_LIMIT_2` | `int` | `500` | `store/conversations.go:18` | 每页条数的钳制上限 |
| `AURELIA_STORE_LISTWORKSPACECONVERSATIONS_LIMIT` | `int` | `200` | `store/conversations.go:19` | 工作区会话列表的默认分页大小 |
| `AURELIA_STORE_LISTWORKSPACECONVERSATIONS_LIMIT_2` | `int` | `500` | `store/conversations.go:20` | 工作区列表分页大小的上限钳制。 |
| `AURELIA_STORE_M_CONFIDENCE` | `float` | `0.8` | `store/misc.go:18` | 未设置时分配给新记忆的默认置信度。 |
| `AURELIA_STORE_LIST_MEMORIES_ACTIVE` | `int` | `20` | `store/misc.go:19` | 注入系统提示词的 ACTIVE/QUERY_DEPENDENT 记忆条目上限（`LIMIT 20`） |
| `AURELIA_STORE_ADMIN_USAGE_RECORDS_LIMIT` | `int` | `500` | `store/misc.go:20` | 超过该值时分页条数上限将重置为默认值 |
| `AURELIA_STORE_ADMIN_USAGE_RECORDS_LIMIT_2` | `int` | `50` | `store/misc.go:21` | AdminUsageRecords 在 limit 无效或过大时使用的默认分页大小 |
| `AURELIA_STORE_USAGE_TREND_WINDOW` | `int` | `7` | `store/misc.go:22` | AdminUsageTrend 的默认回溯窗口。 |
| `AURELIA_STORE_USAGE_TREND_HOURLY_BUCKET_THRESHOLD` | `int` | `2` | `store/misc.go:23` | 趋势图切换为按小时分桶统计的天数窗口阈值（小于等于该值时启用） |
| `AURELIA_STORE_USAGE_TOTALS_WINDOW` | `int` | `7` | `store/misc.go:24` | AdminUsageTotals 的默认回溯时间窗口 |
| `AURELIA_STORE_USAGE_BREAKDOWN_TOP_N` | `int` | `8` | `store/misc.go:25` | AdminUsageBreakdown 返回的默认 Top N 键数量 |
| `AURELIA_STORE_USAGE_BREAKDOWN_WINDOW` | `int` | `7` | `store/misc.go:26` | AdminUsageBreakdown 的默认回溯窗口。 |
| `AURELIA_STORE_USAGE_SERIES_WINDOW` | `int` | `7` | `store/misc.go:27` | `AdminUsageSeries` 默认回溯窗口 |
| `AURELIA_STORE_PS` | `int` | `604800` | `store/quotas.go:11` | 管理员将 period_seconds 留空（<= 0）时应用的按（模型，分组）配额窗口，默认 7 天。period_seconds 可在每条配额记录中由管理员配置，此值仅在未设置时作为兜底。 |
| `AURELIA_STORE_REDEEM_CODE_UNIQUENESS_RETRIES` | `int` | `5` | `store/redeem_codes.go:26` | 生成不冲突兑换码的重试次数（错误信息见第 144 行） |
| `AURELIA_STORE_SEARCH_SNIPPET_RADIUS` | `int` | `64` | `store/search.go:14` | 一次性 messages.search_text 回填过程中，keyset 分页每页拉取的行数 |
| `AURELIA_STORE_BATCH` | `int` | `500` | `store/search.go:47` | 构建内容搜索结果摘要片段时，匹配项两侧各保留的上下文字符（rune）数。作为 radius 参数传给 buildSnippet；窗口约为两侧各 64 字符。 |
| `AURELIA_STORE_SETTINGS_CACHE_TTL` | `duration` | `15*time.Second` | `store/settings_cache.go:17` | 进程本地 GetSetting 结果（配置/模型/配额读取）在重新查询数据库前的缓存时长；跨实例失效仍通过 Pub/Sub 实现 |
| `AURELIA_STORE_SET_MAX_OPEN_CONNS` | `int` | `20` | `store/store.go:35` | Postgres 连接池最大打开连接数 |
| `AURELIA_STORE_SET_MAX_IDLE_CONNS` | `int` | `10` | `store/store.go:36` | Postgres 连接池的最大空闲连接数 |
| `AURELIA_STORE_SET_CONN_MAX_IDLE_TIME` | `duration` | `5*time.Minute` | `store/store.go:37` | 连接池（Postgres）关闭空闲连接前的存活时长。 |
| `AURELIA_STORE_SET_CONN_MAX_LIFETIME` | `duration` | `time.Hour` | `store/store.go:38` | 连接池中 Postgres 连接的最大生命周期 |
| `AURELIA_STORE_LIMIT_DEFAULT` | `int` | `200` | `store/users.go:23` | 可通过环境变量覆盖的分页默认值/上限（详见 docs/config-reference.md）；对应 AURELIA_* 变量未设置时回退到原有硬编码值。 |
| `AURELIA_STORE_LIMIT_MAX` | `int` | `500` | `store/users.go:24` | 用户列表分页 `limit` 上限 |
| `AURELIA_STORE_LIMIT_2_DEFAULT` | `int` | `50` | `store/users.go:25` | 默认分页条数（配置项 2） |
| `AURELIA_STORE_LIMIT_2_MAX` | `int` | `200` | `store/users.go:26` | 上限（2） |
| `AURELIA_STORE_BATCH_2` | `int` | `500` | `store/users.go:267` | 批量处理的批次大小 |


### 10. 邮件 / SMTP

SMTP 拨号 / 发送超时等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_MAIL_SMTP_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `mail/mail.go:27` | SMTP 服务器的 TCP 连接超时，确保端口错误时能快速失败。net.Dialer{Timeout: 10*time.Second}。 |
| `AURELIA_MAIL_DEADLINE` | `duration` | `25*time.Second` | `mail/mail.go:28` | 覆盖完整 SMTP 握手与发送过程的总连接截止期限（`SetDeadline`），通过 `time.Now().Add(25*time.Second)` 设置，用于防止 TLS 模式不匹配导致的挂起。 |


### 11. 服务器启动 / 配置加载

HTTP server 超时、优雅关闭、启动流程常量等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_CMD_ARCHIVE_GC_BOOT_SETTLE_DELAY` | `duration` | `2*time.Minute` | `cmd/api/main.go:40` | 启动后到首次归档工作区 GC 清理之间的延迟 |
| `AURELIA_CMD_RUN_PRUNE` | `duration` | `5*time.Minute` | `cmd/api/main.go:41` | 单次 PruneArchives 清理对象存储的截止期限 |
| `AURELIA_CMD_ARCHIVE_GC_SWEEP_INTERVAL` | `duration` | `6*time.Hour` | `cmd/api/main.go:42` | 已归档工作区 GC 清理的执行间隔。 |
| `AURELIA_CMD_HTTP_SERVER` | `duration` | `15*time.Second` | `cmd/api/main.go:43` | `http.Server` 的 `ReadHeaderTimeout` |
| `AURELIA_CMD_HTTP_SERVER_2` | `duration` | `90*time.Minute` | `cmd/api/main.go:44` | http.Server 写超时，为长连接 SSE 流设置（注释写 30 分钟，实际字面值为 90 分钟） |
| `AURELIA_CMD_HTTP_SERVER_3` | `duration` | `120*time.Second` | `cmd/api/main.go:45` | http.Server 用于长连接的 IdleTimeout |
| `AURELIA_CMD_GRACEFUL_SHUTDOWN_TIMEOUT` | `duration` | `10*time.Second` | `cmd/api/main.go:46` | 收到 SIGINT/SIGTERM 时 srv.Shutdown 的截止期限。 |
| `AURELIA_CMD_TASK_ROUTER_ADAPTER_RUN_JSON` | `int` | `256` | `cmd/api/main.go:47` | 通过 `TaskLLM` 执行 RAG `TaskRouter` JSON 调用（路由/摘要）的 `MaxOutputTokens` |


### 12. 前端

**编译期**生效（Vite 在 `npm run build` 时内联 `VITE_*`），需要在构建环境设置，运行时改环境变量无效。轮询间隔、分页、重试 / 退避、去抖、超时、客户端大小上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `VITE_AURELIA_MAX_SSE_RETRIES` | `envNum` | `3` | `src/api/client.ts:339` | SSE 最大重试次数 |
| `VITE_AURELIA_DELAY_BASE` | `envNum` | `1000` | `src/api/client.ts:341` | SSE 重连指数退避：delay = SSE_RECONNECT_BACKOFF_BASE_MS * factor^(retryCount - 1)。 |
| `VITE_AURELIA_DELAY_FACTOR` | `envNum` | `2` | `src/api/client.ts:342` | SSE 重连退避倍数 |
| `VITE_AURELIA_IMAGE_API_MY_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:181` | 「我的图片」接口的默认每页条数 |
| `VITE_AURELIA_IMAGE_API_MY_IMAGES_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:182` | 「我的图片」接口分页 offset 默认值 |
| `VITE_AURELIA_WORKSPACES_API_ADMIN_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:289` | 工作区管理员列表接口条数上限 |
| `VITE_AURELIA_WORKSPACES_API_ADMIN_LIST_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:290` | 工作区 API 管理端列表偏移量 |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:318` | 会话列表接口的默认每页条数 |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:319` | 对话列表接口分页 offset 默认值 |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_ARCHIVED_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:326` | 已归档对话列表接口条数上限 |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_ARCHIVED_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:327` | 会话 API 已归档列表偏移量 |
| `VITE_AURELIA_ADMIN_API_USERS_SEARCH` | `envStr` | `''` | `src/api/endpoints.ts:593` | 管理员用户搜索接口的默认查询关键词（Query 参数） |
| `VITE_AURELIA_ADMIN_API_USERS_LIMIT` | `envNum` | `50` | `src/api/endpoints.ts:594` | 管理端用户列表接口每页条数默认值 |
| `VITE_AURELIA_ADMIN_API_USERS_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:595` | 管理员用户接口分页偏移量 |
| `VITE_AURELIA_ADMIN_API_USER_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:626` | 管理端 API 用户图片数量上限 |
| `VITE_AURELIA_ADMIN_API_USER_IMAGES_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:627` | 管理员用户图片列表接口的默认偏移量 |
| `VITE_AURELIA_ADMIN_API_ANALYTICS` | `envNum` | `30` | `src/api/endpoints.ts:674` | 管理端分析接口默认查询天数 |
| `VITE_AURELIA_MAX_BYTES` | `envNum` | `256 * 1024` | `src/components/admin/icon-uploader.tsx:34` | 管理员图标上传的前端最大体积限制（对应后端 maxIconBytes） |
| `VITE_AURELIA_MAX_LEN` | `envNum` | `12_000` | `src/components/chat/composer.tsx:99` | 消息输入框文本长度上限，超出后禁止发送 |
| `VITE_AURELIA_INGEST_POLL_MS` | `envNum` | `1200` | `src/components/chat/composer.tsx:113` | 会话内文档摄取状态的轮询间隔 |
| `VITE_AURELIA_CONVERSATION_OUTLINE_TREE_REFETCH_BACKOFF` | `envNum` | `200` | `src/components/chat/conversation-outline.tsx:32` | 分支树数据不完整时重新拉取前的线性退避（200ms × 重试次数），retriesRef 无明确上限。 |
| `VITE_AURELIA_INTERVAL_MS` | `envNum` | `50` | `src/components/chat/markdown.tsx:53` | token 流式输出期间 Markdown 重新解析的最小时间间隔（叠加于 useDeferredValue 之上） |
| `VITE_AURELIA_INITIAL_WINDOW` | `envNum` | `24` | `src/components/chat/message-list.tsx:26` | 长会话首屏挂载的最新消息轮数 |
| `VITE_AURELIA_BATCH` | `envNum` | `24` | `src/components/chat/message-list.tsx:27` | 向上滚动加载时每步额外展示的对话轮次数。 |
| `VITE_AURELIA_PAGE` | `envNum` | `30` | `src/components/chat/my-gallery.tsx:12` | 用户图片画廊无限滚动每页加载的图片数量 |
| `VITE_AURELIA_RUN_TIMEOUT_MS` | `envNum` | `120_000` | `src/lib/pyodide-runner.ts:39` | 浏览器内 Python 运行被中止前的硬性超时上限（对应沙盒执行超时上限） |
| `VITE_AURELIA_MAX_STREAM_CHARS` | `envNum` | `200_000` | `src/lib/pyodide-runner.ts:41` | 打印输出流式传输的最大字符数，超出即截断，防止失控循环 |
| `VITE_AURELIA_MAX_RESULT_CHARS` | `envNum` | `20_000` | `src/lib/pyodide-runner.ts:43` | 最终表达式 repr() 结果展示的最大字符数 |
| `VITE_AURELIA_DEFAULT_MAX_DIM` | `envNum` | `1280` | `src/lib/resize-image.ts:11` | 图片压缩默认最大边长（像素） |
| `VITE_AURELIA_DEFAULT_MAX_BYTES` | `envNum` | `240 * 1024` | `src/lib/resize-image.ts:12` | 服务端 256 KiB 上限之下预留的余量 |
| `VITE_AURELIA_QUALITY_START` | `envNum` | `0.9` | `src/lib/resize-image.ts:13` | 起始压缩质量 |
| `VITE_AURELIA_QUALITY_FLOOR` | `envNum` | `0.4` | `src/lib/resize-image.ts:14` | 图片压缩质量下限 |
| `VITE_AURELIA_QUALITY_STEP` | `envNum` | `0.12` | `src/lib/resize-image.ts:15` | 图片压缩质量递减步长 |
| `VITE_AURELIA_MAX_SHIKI_CODE_LENGTH` | `envNum` | `200_000` | `src/lib/syntax/shiki-client.ts:42` | 超出此长度的代码不再进行语法高亮（回退为纯文本） |
| `VITE_AURELIA_FINAL_RENDER_TIMEOUT_MS` | `envNum` | `15_000` | `src/lib/syntax/shiki-client.ts:43` | 等待 shiki Web Worker 完成最终（非流式）代码高亮的超时时间。 |
| `VITE_AURELIA_CACHE_LIMIT` | `envNum` | `160` | `src/lib/syntax/shiki-client.ts:44` | 内存中代码高亮结果 LRU 缓存的最大条目数 |
| `VITE_AURELIA_ADMIN_BACKUP_EXPORT_JOB_POLL_INTERVAL` | `envNum` | `2500` | `src/pages/admin/AdminBackup.tsx:50` | 管理后台备份导出任务的轮询间隔 |
| `VITE_AURELIA_PAGE_SIZE_2` | `envNum` | `20` | `src/pages/admin/AdminRedeemCodes.tsx:80` | 管理端兑换码表格每页行数（前端分片） |
| `VITE_AURELIA_PAGE_SIZE` | `envNum` | `50` | `src/pages/admin/AdminUsage.tsx:33` | 管理端用量/日志表格每页行数 |
| `VITE_AURELIA_IMAGES_PAGE` | `envNum` | `60` | `src/pages/admin/AdminUserLibrary.tsx:29` | 管理员用户素材库每页拉取的图片数 |
| `VITE_AURELIA_ONLINE_WINDOW_S` | `envNum` | `300` | `src/pages/admin/AdminUsers.tsx:42` | `last_seen` 距当前时间在此秒数内即视为用户在线 |
| `VITE_AURELIA_PAGE_SIZE_3` | `envNum` | `50` | `src/pages/admin/AdminUsers.tsx:83` | 管理员用户表每页行数 |
| `VITE_AURELIA_KB_DOC_STATUS_POLL_INTERVAL` | `envNum` | `2200` | `src/pages/kb/KnowledgeBaseDetail.tsx:41` | 存在待处理/解析中/嵌入中文档时的 `setInterval` 轮询间隔 |
| `VITE_AURELIA_CONV_PAGE` | `envNum` | `200` | `src/store/conversations.ts:52` | 侧边栏会话列表每页拉取的会话数（首次加载 + 无限滚动加载更多）；注释称与服务端默认值保持一致，但仍是前端硬编码字面量，作为 limit 参数传出 |
| `VITE_AURELIA_MSG_PAGE` | `envNum` | `40` | `src/store/conversations.ts:66` | 打开会话时加载的最新消息条数，更早的消息在向上滚动时分页加载；该值略高于渲染窗口 INITIAL_WINDOW=24。 |

