# Aivory 高级环境变量参考（全中文版）

> 本文件是 [`config-reference.md`](./config-reference.md) 的**全中文版**：说明文字已译为中文（环境变量名 / 类型 / 默认值 / 代码位置保持原样）。若有出入以英文原表 + 源码为准。
>
> 版本 v2.0 · 2026-07-15 · 由整仓源码生成（对应本次改动：所有此前「硬编码但值得调整」的参数均已改为环境变量可覆盖）
>
> **这 300 个环境变量全部是可选的，未在 `.env.example` 中列出。** 不设置任何一个，Aivory 使用下表所列的默认值。只有当你需要调整某个具体参数时，才在部署环境里添加对应的变量。
> 如果需要调整，把用到的变量抄一份加进你自己的 `.env` 即可（`.env.example` 保持精简，不会自动包含这些高级选项）。
>
> **后端（Go）**：271 个，读取自进程环境变量，改动后**需重启 `aivory-api` 进程**生效。
> **前端（Vite）**：23 个 `VITE_*` 变量，在**构建时**内联（`npm run build` / `vite build`），必须在构建环境设置，**运行时**改容器环境变量无效，需要重新构建产物。
> **沙盒服务（Python）**：6 个 `SANDBOX_*` 变量（与已有的 `SANDBOX_*` 变量同一命名空间），读取自 `sandbox-service` 进程环境，改动后**需重启该进程**生效。
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
- [9. 服务器启动 / 配置加载](#9-服务器启动--配置加载)
- [10. 前端](#10-前端)

---

## 0. 总览

共 **300** 个环境变量，按子系统分布：

| 子系统 | 变量数 |
| --- | --- |
| 1. LLM 对话 / 编排 / 内部模型调用 | 65 |
| 2. RAG 文档解析 / 向量检索 | 60 |
| 3. 沙盒代码执行 | 9 |
| 4. 内置工具（搜索 / Python / 网络安全） | 14 |
| 5. 会话 / 消息 / 流式 API | 73 |
| 6. 认证 / 会话 / 验证码 | 15 |
| 7. 上传 / 文件 / 分享 | 18 |
| 8. 管理后台任务（备份 / 向量维护 / 兑换码） | 20 |
| 9. 服务器启动 / 配置加载 | 3 |
| 10. 前端 | 23 |
| **合计** | **300** |

---

### 1. LLM 对话 / 编排 / 内部模型调用

主对话流、工具循环、TTFT 看门狗，以及压缩 / 记忆 / 审核 / 校验 / 深度研究 / 文档生成等内部模型调用。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_LLM_APPLY_ANTHROPIC_THINKING_SETTINGS` | `int` | `2048` | `llm/anthropic_provider.go:24` | 在抬高 Anthropic max_tokens 时，于扩展思考 budget_tokens 之上追加的 token 余量，使输出能容纳思考加回复。 |
| `AIVORY_LLM_MAX_ITER` | `int` | `20` | `llm/anthropic_provider.go:160` | Anthropic 流式 Stream 循环中原生工具调用轮次（Messages API 调用次数）的硬性上限。 |
| `AIVORY_LLM_MAX_TOK` | `int` | `64000` | `llm/anthropic_provider.go:169` | 流式工具循环中每次 Anthropic Messages 请求发送的默认 max_tokens（除非请求自身覆盖）。 |
| `AIVORY_LLM_MAX_TOK_2` | `int` | `64000` | `llm/anthropic_provider.go:322` | 提示词工具模式（promptRunOnce）下单次 Anthropic 调用的默认 max_tokens（除非请求覆盖）。 |
| `AIVORY_LLM_INFLIGHT_GRACE` | `duration` | `15*time.Minute` | `llm/compaction.go:55` | status=streaming 的助手消息行在被视为崩溃残留之前免于被摘要处理的宽限时长。 |
| `AIVORY_LLM_T` | `int` | `4` | `llm/compaction.go:62` | 在压缩 token 估算中为每条消息追加的结构性 token 开销（角色标记、外框）。 |
| `AIVORY_LLM_MESSAGE_TOKEN_MEMO_CACHE_BOUND` | `int` | `100000` | `llm/compaction.go:63` | 每消息 token 估算记忆缓存 map 在被原地重置以限制内存前允许的最大条目数。 |
| `AIVORY_LLM_SUMMARY_TOKENS_CLAMP_FLOOR` | `int` | `256` | `llm/compaction.go:64` | 管理端 summary_max_tokens 设置的下限；配置值小于该下限时重置为默认摘要预算。 |
| `AIVORY_LLM_BIG_TOKEN_OVERFLOW_NUM` | `int` | `5` | `llm/compaction.go:65` | 本轮触发内联摘要的 token 触发阈值倍数（num/den，默认 5/4 = 1.25 倍）的分子。 |
| `AIVORY_LLM_BIG_TOKEN_OVERFLOW_DEN` | `int` | `4` | `llm/compaction.go:66` | 本轮触发内联摘要的 token 触发阈值倍数（num/den，默认 5/4 = 1.25 倍）的分母。 |
| `AIVORY_LLM_INLINE_COMPACTION_BACKLOG_FACTOR` | `int` | `3` | `llm/compaction.go:67` | 未摘要尾部长度相对 keepRounds*2 的倍数，超过则本轮强制内联（而非异步）压缩。 |
| `AIVORY_LLM_SUMMARY_MERGE_BUDGET` | `int` | `2048` | `llm/compaction.go:76` | 触发把旧摘要块折叠为更粗块的累计摘要 token 阈值。 |
| `AIVORY_LLM_DETERMINISTIC_SUMMARY_CLIP_BUDGET` | `int` | `300` | `llm/compaction.go:68` | 当任务模型摘要返回为空时，对较旧轮次做确定性回退裁剪所用的 token 预算。 |
| `AIVORY_LLM_ATTEMPT` | `int` | `4` | `llm/compaction.go:69` | 向会话 summary_blocks 追加新摘要块时的最大比较并交换（CAS）重试次数。 |
| `AIVORY_LLM_ITER` | `int` | `3` | `llm/compaction.go:70` | 为把某会话路径的摘要 token 压回预算内所执行的最大重复折叠迭代次数。 |
| `AIVORY_LLM_MAX_OUTPUT_TOKENS_5` | `int` | `2` | `llm/compaction.go:71` | 作用于合并预算以推导折叠调用的 MaxOutputTokens 及确定性裁剪长度的除数。 |
| `AIVORY_LLM_CHUNK_SIZE` | `int` | `400` | `llm/compaction.go:810` | 重新校验已摘要消息是否仍存在时每条 SQL IN(...) 查询的消息 ID 批大小（用于规避驱动占位符上限分块）。 |
| `AIVORY_LLM_DR_MAX_ROUNDS` | `int` | `4` | `llm/deep_research.go:47` | 深度研究引擎运行的 搜索再验证 轮次数量的硬性上限。 |
| `AIVORY_LLM_DR_QUERIES_PER_ROUND` | `int` | `6` | `llm/deep_research.go:48` | 每个深度研究轮次派发的最大搜索查询数。 |
| `AIVORY_LLM_DR_FETCH_PER_ROUND` | `int` | `5` | `llm/deep_research.go:49` | 每个深度研究轮次挑选并读取的最大新来源候选数。 |
| `AIVORY_LLM_DR_MIN_DEEP_READS` | `int` | `5` | `llm/deep_research.go:50` | 深度研究在允许收尾前必须达到的最小深读来源数（即便覆盖缺口已看似充足）。 |
| `AIVORY_LLM_DR_SEARCH_TOP_K` | `int` | `8` | `llm/deep_research.go:51` | 每次深度研究搜索调用请求的结果数（top_k）。 |
| `AIVORY_LLM_DR_WALL_CLOCK` | `duration` | `5*time.Minute` | `llm/deep_research.go:52` | 限定整个深度研究引擎运行的总墙钟超时。 |
| `AIVORY_LLM_DR_CALL_TIMEOUT` | `duration` | `30*time.Second` | `llm/deep_research.go:53` | 单次深度研究搜索或抓取请求的每调用超时。 |
| `AIVORY_LLM_DEEP_RESEARCH_VALIDATE_TIMEOUT` | `duration` | `75*time.Second` | `llm/deep_research.go:64` | 限定深度研究在撰写前审查薄弱/单来源论断的 validate 阶段的超时。 |
| `AIVORY_LLM_SCORE_A` | `float` | `9` | `llm/deep_research.go:67` | 为 URL 信誉等级为 A 的候选来源所加的排序分（信誉在排序中占主导）。 |
| `AIVORY_LLM_SCORE_B` | `float` | `6` | `llm/deep_research.go:68` | 为 URL 信誉等级为 B 的候选来源所加的排序分。 |
| `AIVORY_LLM_SCORE_C` | `float` | `3` | `llm/deep_research.go:69` | 为 URL 信誉等级为 C 的候选来源所加的排序分。 |
| `AIVORY_LLM_SCORE_KW` | `float` | `1` | `llm/deep_research.go:70` | 候选来源标题或摘要中每命中一个问题关键词（长度大于 3）所加的排序分。 |
| `AIVORY_LLM_SCORE_FRESH_DOMAIN` | `float` | `2` | `llm/deep_research.go:71` | 为来自本次研究尚未见过域名的候选来源所加的排序加分。 |
| `AIVORY_LLM_MAX_ITER_4` | `int` | `20` | `llm/google_provider.go:68` | Gemini 流式 Stream 循环中原生工具调用轮次（generateContent 调用次数）的硬性上限。 |
| `AIVORY_LLM_GEMINI_MAX_TOK` | `int` | `64000` | `llm/google_provider.go:78` | Gemini 流式循环中每次请求 generationConfig.maxOutputTokens 的默认值（除非请求覆盖）。 |
| `AIVORY_LLM_GEMINI_MAX_TOK_2` | `int` | `64000` | `llm/google_provider.go:472` | Gemini 提示词工具模式调用中 generationConfig.maxOutputTokens 的默认值（除非请求覆盖）。 |
| `AIVORY_LLM_CONF` | `float` | `0.7` | `llm/memory_worker.go:40` | 当提取器返回的置信度不在 (0,1] 范围内时，为抽取记忆赋予的回退置信度。 |
| `AIVORY_LLM_OFFICIAL_TOOL_SPEC` | `string` | `"medium"` | `store/official_tools.go` | 新建 OpenAI Responses 默认 `web_search` 定义或迁移旧字符串配置时使用的 `search_context_size`；此后管理员显式填写的请求 JSON 优先。 |
| `AIVORY_LLM_MAX_ITER_2` | `int` | `20` | `llm/openai_provider.go:110` | OpenAI streamChat（Chat Completions）循环中原生工具调用轮次的硬性上限。 |
| `AIVORY_LLM_MAX_ITER_3` | `int` | `20` | `llm/openai_provider.go:610` | OpenAI streamResponses（Responses API）循环中原生工具调用轮次的硬性上限。 |
| `AIVORY_LLM_INLINE_QUOTE_SOURCE_INJECTION_CAP` | `int` | `8000` | `llm/orchestrator.go:30` | 内联引用子对话中，随高亮摘录一并注入的来源消息文本在截断前的最大字符（rune）数。 |
| `AIVORY_LLM_ATTACHMENT_IMAGE_INLINE_BYTES` | `int64` | `20*1024*1024` | `llm/orchestrator.go` | 单个已验证图片附件在以 base64 写入渠道请求前的独立硬上限；读取过程同样受限，旧的错误大小元数据无法绕过。 |
| `AIVORY_LLM_TOOL_ROUTE_TIMEOUT` | `duration` | `12*time.Second` | `llm/orchestrator.go:38` | 自动工具模式等待后台任务模型给出路由判断的最长时间；超时后按 fail-open 规则开启工具。 |
| `AIVORY_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MAX` | `int` | `600` | `llm/orchestrator.go:39` | 为 python_execute 调用上下文计时时，管理员配置的 sandbox_exec_timeout_sec 被钳制的上限（秒）。 |
| `AIVORY_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MIN` | `int` | `10` | `llm/orchestrator.go:40` | 为 python_execute 调用上下文计时时，管理员配置的 sandbox_exec_timeout_sec 被钳制的下限（秒）。 |
| `AIVORY_LLM_SANDBOX_EXEC_CTX_SAFETY_MARGIN` | `duration` | `150*time.Second` | `llm/orchestrator.go:41` | 为 python_execute 上下文计时时，在钳制后的沙箱执行超时之上额外增加的余量，使上下文比沙箱 HTTP 客户端更晚超时。 |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_SEARCH` | `int` | `16` | `llm/orchestrator.go:147` | 普通模式下每条消息允许的 web_search 调用次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_FETCH` | `int` | `12` | `llm/orchestrator.go:148` | 普通模式下每条消息允许的 web_fetch 调用次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `8` | `llm/orchestrator.go:150` | 普通模式下每条消息允许的 image_generate 调用次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `16` | `llm/orchestrator.go:151` | 普通模式下每条消息允许的 python_execute 沙箱执行次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_SEARCH` | `int` | `40` | `llm/orchestrator.go:157` | 深度研究运行时每条消息允许的 web_search 调用次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_FETCH` | `int` | `25` | `llm/orchestrator.go:158` | 深度研究运行时每条消息允许的 web_fetch 调用次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `4` | `llm/orchestrator.go:160` | 深度研究运行时每条消息允许的 image_generate 调用次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `8` | `llm/orchestrator.go:161` | 深度研究运行时每条消息允许的 python_execute 沙箱执行次数上限；超出即令该调用失败。 |
| `AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN` | `int` | `48` | `llm/orchestrator.go:169` | 普通模式下每条消息跨所有工具的工具调用总数全局上限，叠加在各工具单独上限之上。 |
| `AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN_DEEP` | `int` | `150` | `llm/orchestrator.go:170` | 深度研究运行时每条消息跨所有工具的工具调用总数全局上限。 |
| `AIVORY_LLM_TOOL_TIMEOUTS` | `duration` | `10*time.Second` | `llm/orchestrator.go:2326` | 单次 web_search 工具调用的每次调用超时上限。 |
| `AIVORY_LLM_TOOL_TIMEOUTS_2` | `duration` | `15*time.Second` | `llm/orchestrator.go:2327` | 单次 web_fetch 工具调用的每次调用超时上限。 |
| `AIVORY_LLM_TOOL_TIMEOUTS_3` | `duration` | `600*time.Second` | `llm/orchestrator.go:2329` | 单次 image_generate 工具调用的每次调用超时上限（为迟缓的第三方图像网关留出较宽窗口）。 |
| `AIVORY_LLM_TOOL_TIMEOUT_DEFAULT` | `duration` | `100*time.Second` | `llm/orchestrator.go:2332` | 未在按类型配置的 toolTimeouts 映射中列出的工具所用的每次调用回退超时。 |
| `AIVORY_LLM_PROMPT_MAX_ITER` | `int` | `10` | `llm/prompt_tools.go:36` | 提示模式工具循环在本轮结束前的最大迭代次数（每次迭代 = 一次模型生成加可选的工具调用）。 |
| `AIVORY_LLM_PROMPT_MAX_RETRY` | `int` | `2` | `llm/prompt_tools.go:37` | 提示模式下重发格式错误的 <tool_call> JSON 或重试失败工具执行的最大重试次数。 |
| `AIVORY_LLM_PROVIDER_REQUEST_BODY_MAX_BYTES` | `int` | `128*1024` | `llm/provider_request_capture.go:20` | 用于诊断的已捕获供应商请求体/请求头 JSON 快照被钳制到的最大长度。 |
| `AIVORY_LLM_PROVIDER_REQUEST_VALUE_MAX_BYTES` | `int` | `8*1024` | `llm/provider_request_capture.go:21` | 每个单独捕获的供应商请求值（URL、请求头值）被钳制到的最大长度。 |
| `AIVORY_LLM_IMAGE_DOCUMENT_FLAT_TOKEN_ALLOWANCE` | `int` | `1024` | `llm/quota.go:270` | 估算请求 token 时为每个 image/document 块计入的固定 token 数，因为 base64 无法按文本分词。 |
| `AIVORY_LLM_OUTPUT_RESERVE` | `int` | `2000` | `llm/quota.go:313` | 计算预检积分可负担性估算时，在估算输入 token 之上加入的固定输出 token 预留量。 |
| `AIVORY_LLM_MAX_CONCURRENT_TOOLS` | `int` | `4` | `llm/tool_exec.go:28` | 单轮内并发执行的工具数上限（限制并发执行器扇出的信号量容量）。 |
| `AIVORY_LLM_VCTX` | `duration` | `45*time.Second` | `llm/verify.go:77` | 限制答案生成后校验/审计 LLM 调用的超时，避免迟缓的跨供应商审计拖住本轮。 |





### 2. RAG 文档解析 / 向量检索

文档分块、Embedding 批量与并发、Qdrant 客户端、MinerU OCR 轮询等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_RAG_EMBEDDING_RETRY_DELAY` | `duration` | `time.Second` | `rag/embedder_http.go:50` | 嵌入批次重试之间指数退避的基础延迟，按 2^min(尝试次数,4) 放大。 |
| `AIVORY_RAG_EMBEDDING_RETRY_DELAY_2` | `duration` | `30*time.Second` | `rag/embedder_http.go:51` | 计算出的嵌入重试退避延迟的上限。 |
| `AIVORY_RAG_EMBEDDING_RETRY_DELAY_3` | `duration` | `1000*time.Millisecond` | `rag/embedder_http.go:52` | 为每次嵌入重试退避延迟添加的随机抖动范围，用于打散并发批次的同步性。 |
| `AIVORY_RAG_DASH_SCOPE_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:179` | 当供应商为 DashScope 时，每个文档并发的上游嵌入批次数上限。 |
| `AIVORY_RAG_DASH_SCOPE_GLOBAL_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:180` | 进程级并发 DashScope 嵌入请求数上限（共享槽位信号量的容量）。 |
| `AIVORY_RAG_DASH_SCOPE_EMBED_ATTEMPT_TIMEOUT` | `duration` | `60*time.Second` | `rag/embedder_http.go:188` | DashScope 嵌入调用的单次尝试请求超时，设在客户端 3 分钟上限之下，使卡住的调用可见地失败。 |
| `AIVORY_RAG_EMBED_CONCURRENCY` | `int` | `4` | `rag/embedder_http.go:194` | 非 DashScope 供应商每个文档并发的上游嵌入批次数上限。 |
| `AIVORY_RAG_MAX_ATTEMPTS` | `int` | `2` | `rag/embedder_http.go:445` | 单个嵌入批次 POST 在放弃前的最大尝试次数（瞬时失败时带退避重试）。 |
| `AIVORY_RAG_PDF_INSPECTION_TIMEOUT` | `duration` | `8*time.Second` | `rag/parser.go:56` | 父进程对子进程 PDF 文本层检测探针强制施加的截止时间，到期即将其杀掉。 |
| `AIVORY_RAG_OFFICE_XML_ZIP_ENTRY_READ_CAP` | `int64` | `16*1024*1024` | `rag/parser.go:253` | Office 纯文本提取时，读取每个 DOCX/PPTX zip 条目（正文/页眉/页脚/幻灯片 XML）的字节上限。 |
| `AIVORY_RAG_PDF_INSPECTION_SAMPLE_LIMIT` | `int` | `3` | `rag/parser.go:333` | 探测 PDF 是扫描件还是原生数字文本时，均匀抽样的最大页数。 |
| `AIVORY_RAG_PDF_THIN_CHARS_PER_PAGE` | `int` | `200` | `rag/parser.go:334` | 低于此每抽样页字符数阈值时，含图 PDF 被标记为“文字稀疏”并转交 OCR。 |
| `AIVORY_RAG_CMD_WAIT_DELAY` | `duration` | `500*time.Millisecond` | `rag/parser.go:375` | PDF 检测子进程上下文超时后，强制杀死前的宽限期（cmd.WaitDelay）。 |
| `AIVORY_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_IMAGE` | `int` | `5` | `rag/parser.go:438` | 原始字节“强扫描件”判定 imageCount*N >= pages*M 中的图像数乘子，用于判定 PDF 为纯图像件。 |
| `AIVORY_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_PAGE` | `int` | `4` | `rag/parser.go:439` | 原始字节“强扫描件”判定 imageCount*N >= pages*M 中的页数乘子，用于判定 PDF 为纯图像件。 |
| `AIVORY_RAG_MINERU_SOURCE_OBJECT_CLEANUP_TIMEOUT` | `duration` | `30*time.Second` | `rag/parser.go:603` | 解析后尽力删除为 MinerU OCR 上传的桶内源对象时的超时时限。 |
| `AIVORY_RAG_MINERU_POLL_DEADLINE` | `duration` | `20*time.Minute` | `rag/parser.go:606` | 轮询 MinerU 抽取任务等待 done/failed 状态整个循环的总时限上限。 |
| `AIVORY_RAG_MINERUZIPCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:838` | 经 SSRF 防护下载 MinerU 结果 zip 的 HTTP 客户端超时。 |
| `AIVORY_RAG_FULL_MD_READ_CAP_INSIDE_ZIP` | `int64` | `32*1024*1024` | `rag/parser.go:841` | 从 MinerU 结果 zip 内读取 full.md markdown 正文的字节上限。 |
| `AIVORY_RAG_MAX_ZIP` | `int64` | `500*1024*1024` | `rag/parser.go:862` | 下载 MinerU 结果 zip 的字节上限；超出则拒绝而不缓冲进内存。 |
| `AIVORY_RAG_MINERUCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:1002` | 调用 MinerU 云 API 提交、轮询、下载各次往返的 http.Client 超时。 |
| `AIVORY_RAG_MINERU_SOURCE_TTLSECONDS` | `int` | `60*60` | `rag/parser.go:1008` | 交给 MinerU 的源文档预签名 GET URL 的有效期（秒）。 |
| `AIVORY_RAG_SPREADSHEET_PREVIEW_MAX_FILE_BYTES` | `int64` | `30*1024*1024` | `rag/spreadsheet.go:40` | `python_execute` 未向模型开放时，进程内解析表格并生成有限内联预览所接受的最大文件大小；更大的文件会被跳过。 |
| `AIVORY_RAG_RAG_FAST_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:42` | 服务文本/表格文档的“rag-fast”摄取通道的 asynq 工作并发数。 |
| `AIVORY_RAG_RAG_SLOW_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:43` | 服务 OCR/重型文档的“rag”慢速摄取通道的 asynq 工作并发数。 |
| `AIVORY_RAG_INGEST_PIPELINE_TIMEOUT` | `duration` | `70*time.Minute` | `rag/rag.go:44` | 每个文档的完整解析/嵌入摄取流水线（runIngestWithRetries）运行的上下文时限。 |
| `AIVORY_RAG_INGEST_TASK_TIMEOUT` | `duration` | `75*time.Minute` | `rag/rag.go:45` | rag.ingest 任务的 asynq 单任务处理超时（asynq.Timeout）。 |
| `AIVORY_RAG_INGEST_UNIQUE_TTL` | `duration` | `80*time.Minute` | `rag/rag.go:46` | asynq 唯一性锁 TTL（asynq.Unique），抑制同一文档的重复入队。 |
| `AIVORY_RAG_INGEST_HEARTBEAT_INTERVAL` | `duration` | `30*time.Second` | `rag/rag.go:47` | 摄取心跳写入（TouchDocumentIngest）的间隔，使运行中的文档不被判为过期。 |
| `AIVORY_RAG_INGEST_STALE_AFTER` | `duration` | `4*time.Minute` | `rag/rag.go:48` | 解析/嵌入中的文档心跳超过此时长即视为被遗弃并回收重新入队。 |
| `AIVORY_RAG_INGEST_PENDING_STALE_AFTER` | `duration` | `ingestUniqueTTL` | `rag/rag.go:49` | 仍处于“pending”的文档超过此时长即视为卡住并回收重新入队。 |
| `AIVORY_RAG_INGEST_RECOVERY_INTERVAL` | `duration` | `time.Minute` | `rag/rag.go:50` | 回收过期被遗弃摄取文档的恢复循环的扫描间隔。 |
| `AIVORY_RAG_INGEST_FINALIZE_TIMEOUT` | `duration` | `30*time.Second` | `rag/rag.go:51` | 终结失败摄取时，向量库 DeleteByDocument 清理的超时时限。 |
| `AIVORY_RAG_INGEST_ASYNQ_LEASE_MAX_RETRIES` | `int` | `1` | `rag/rag.go:52` | 为租约/进程丢失预留的 asynq MaxRetry 次数；处理器失败在流水线内单独重试。 |
| `AIVORY_RAG_INGEST_ASYNQ_RETRY_DELAY` | `duration` | `2*time.Minute` | `rag/rag.go:53` | asynq 在重跑租约丢失的摄取任务前等待的延迟（RetryDelayFunc）。 |
| `AIVORY_RAG_INGEST_QUEUE_NAME` | `duration` | `2*time.Second` | `rag/rag.go:59` | 用于将文档分入快/慢摄取队列的 GetDocument 查询超时时限。 |
| `AIVORY_RAG_RUN_INGEST_WITH_RETRIES` | `int` | `3` | `rag/rag.go:60` | 文档被终结为失败前，整条流水线摄取的最大尝试次数。 |
| `AIVORY_RAG_RUN_INGEST_WITH_RETRIES_2` | `duration` | `3*time.Second` | `rag/rag.go:61` | 整条流水线摄取重试之间的基础退避，按尝试次数递乘。 |
| `AIVORY_RAG_START_INGEST_HEARTBEAT` | `duration` | `5*time.Second` | `rag/rag.go:62` | 每次摄取心跳写入（TouchDocumentIngest）的超时时限。 |
| `AIVORY_RAG_FINALIZE_CHUNK_CLEANUP_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:63` | 终结失败摄取时，DeleteChunksByDocument 清理的超时时限。 |
| `AIVORY_RAG_FINALIZE_STATUS_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:64` | 将失败文档标记为“failed”的终态 UpdateDocumentStatus 写入的超时时限。 |
| `AIVORY_RAG_DENSE_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:68` | 混合检索中稠密/向量分支（vec.Search）请求的 Top-N 命中数。 |
| `AIVORY_RAG_KEYWORD_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:69` | 混合检索中关键词分支（vec.SearchKeyword）请求的 Top-N 命中数。 |
| `AIVORY_RAG_SNIPPET_OF` | `int` | `240` | `rag/rag.go:70` | 调用方未指定上限时，结果摘要的默认字节长度上限（按 rune 安全截断）。 |
| `AIVORY_RAG_SPLIT_PARAGRAPHS_AND_TABLES` | `int` | `800` | `rag/rag.go:71` | 低于此字节长度的图像 markdown 段落被作为单个原子块保留而不再拆分。 |
| `AIVORY_RAG_ROUTER_CALL_TIMEOUT` | `duration` | `12*time.Second` | `rag/rag.go:72` | 首 token 热路径上任务路由 JSON LLM 调用（task.router）在回退到普通检索前的超时时限。 |
| `AIVORY_RAG_MAP_REDUCE_SUMMARISE` | `int` | `200` | `rag/rag.go:73` | map-reduce 分组摘要提示中向任务模型要求的中文字数上限（≤N 字）。 |
| `AIVORY_RAG_COLLECT_DOC_HINTS` | `int` | `120` | `rag/rag.go:74` | 路由器文档提示行中为消解指代而保留的每个文档正文的前导字节数。 |
| `AIVORY_RAG_COLLECT_DOC_HINTS_2` | `int` | `12` | `rag/rag.go:75` | 为检索路由器收集的每文档提示行的最大数量。 |
| `AIVORY_RAG_FUSE_RECIPROCAL_RANK` | `int` | `60` | `rag/rag.go:1427` | 融合向量与关键词两路检索的倒数排名融合公式 1/(rank+k) 中的排名常数 k。 |
| `AIVORY_RAG_RETRIEVED_SNIPPET_CHARS` | `int` | `2000` | `rag/rag.go:1520` | expandHit 在注入前围绕每个检索命中块构建的上下文窗口字节预算。 |
| `AIVORY_RAG_CHILD_TARGET_CHARS` | `int` | `2000` | `rag/rag.go:1757` | 合并切分原子单元时每个嵌入子块所趋向的目标字节长度。 |
| `AIVORY_RAG_PARENT_TARGET_CHARS` | `int` | `4800` | `rag/rag.go:1758` | 父级章节正文为检索时上下文而被截断的字节长度。 |
| `AIVORY_RAG_CHUNK_OVERLAP_CHARS` | `int` | `250` | `rag/rag.go:1761` | 作为滑动窗口重叠而加到下一个子块前面的上一子块尾部字节数。 |
| `AIVORY_RAG_MAPREDUCE_GROUPTOKENS` | `int` | `6000` | `rag/rag.go:2334` | 对超预算语料做 map-reduce 摘要时每个块分组的估算 token 预算。 |
| `AIVORY_RAG_MAPREDUCE_MAXGROUPS` | `int` | `8` | `rag/rag.go:2335` | map-reduce 摘要器对单次查询处理的块分组最大数量。 |
| `AIVORY_RAG_BATCH_SIZE` | `int` | `64` | `rag/vector_admin.go:136` | 管理端重建向量时每次 Embed 调用嵌入的块数量。 |
| `AIVORY_VECTOR_QDRANT_SCROLL_PAGE_SIZE_EXISTINGCHUNKIDS` | `int` | `256` | `vector/qdrant.go:32` | 列出索引中已存在的块 ID 时每个 Qdrant scroll 页获取的点数量。 |
| `AIVORY_VECTOR_QDRANT_SCROLL_PAGE_SIZE_VECTORCHUNKSTATUSES` | `int` | `256` | `vector/qdrant.go:33` | 为管理端向量状态审计扫描载荷与向量时每个 Qdrant scroll 页获取的点数量。 |
| `AIVORY_VECTOR_DELETE_CONCURRENCY` | `int` | `4` | `vector/qdrant.go:460` | 跨所有维度清除某文档的点时按集合并发删除请求的最大数量。 |





### 3. 沙盒代码执行

`python_execute` 沙盒。Go 侧变量需重启 API 进程生效；`SANDBOX_*` 变量作用于 `sandbox-service` 进程，需重启该服务生效。Runner 网络不是可配置项：每个 session 都固定使用 Docker `--network none`。所需 Python 依赖必须预装进 runner 镜像，不提供 `SANDBOX_NETWORK` 作为运行时下载的逃生开关。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `SANDBOX_MAX_OUTPUT_BYTES` | `int` | `32768` | `sandbox-service/app.py:120` | 沙箱截断所捕获的 exec stdout/stderr 流的字节上限。 |
| `SANDBOX_MAX_ARTIFACT_BYTES` | `int` | `20971520` | `sandbox-service/app.py:121` | 沙箱收集单个产出文件的最大字节大小。 |
| `SANDBOX_S3_MAX_ATTEMPTS` | `int` | `3` | `sandbox-service/app.py:137` | 限定每次 S3 存储 SDK 调用的 botocore max_attempts 重试次数。 |
| `SANDBOX_S3_CONNECT_TIMEOUT_S` | `float` | `10` | `sandbox-service/app.py:138` | 应用于每次 S3 存储 SDK 调用的连接超时（秒）。 |
| `SANDBOX_S3_READ_TIMEOUT_S` | `float` | `120` | `sandbox-service/app.py:139` | 应用于每次 S3 存储 SDK 调用的读取超时（秒）。 |
| `SANDBOX_OSS_CONNECT_TIMEOUT_S` | `float` | `30` | `sandbox-service/app.py:140` | 阿里云 OSS bucket 客户端的连接超时（秒）。 |
| `AIVORY_SANDBOX_MAX_SANDBOX_RESP_BYTES` | `int64` | `256<<20` | `sandbox/sandbox.go:162` | 每次 exec 调用解码的 sidecar HTTP 响应体字节上限，防止 API 进程 OOM。 |
| `AIVORY_SANDBOX_EXEC_CLIENT_OVERHEAD` | `duration` | `120*time.Second` | `sandbox/sandbox.go:171` | 在每次 exec 上限之上追加的时长，用于设定沙箱 HTTP 客户端超时，以覆盖 sidecar 截止后的收尾工作。 |
| `AIVORY_SANDBOX_SANDBOX_ERROR_BODY_READ_CAP` | `int64` | `64<<10` | `sandbox/sandbox.go:174` | 读取 4xx/5xx sidecar 错误响应体用于错误信息的字节上限。 |





### 4. 内置工具（搜索 / Python / 网络安全）

web_search 结果条数与超时、Python 安全模式、SSRF / 网络安全护栏等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_TOOLS_IN_TOP_K` | `int` | `5` | `tools/builtins.go:37` | 模型调用未指定 top_k 时 web_search 工具的默认结果数量。 |
| `AIVORY_TOOLS_WEB_FETCH_RESPONSE_BODY_READ_CAP` | `int64` | `256*1024` | `tools/builtins.go:38` | web_fetch 在剥离 HTML 前读取的 HTTP 响应体字节上限。 |
| `AIVORY_TOOLS_WEB_FETCH_EXTRACTED_TEXT_CHAR_CAP` | `int` | `32000` | `tools/builtins.go:39` | web_fetch 剥离 HTML 后的文本在以省略标记截断前的字符上限。 |
| `AIVORY_TOOLS_PYTHON_EXECUTE_UPLOAD_STAGING_FILE_SIZE` | `int64` | `40*1024*1024` | `tools/builtins.go:40` | 向 python 沙箱暂存文件时，超过该值则跳过某个会话上传文件的单文件大小上限。调高时还需确保 sidecar 的 `SANDBOX_MAX_UPLOAD_BYTES` 与 `SANDBOX_MAX_BODY_BYTES` 足够大。 |
| `AIVORY_TOOLS_PYTHON_EXECUTE_STDOUT_STDERR_TRUNCATION_CAP` | `int` | `32*1024` | `tools/builtins.go:42` | python_execute 的 stdout/stderr 在呈现给模型前截断的字符上限。 |
| `AIVORY_TOOLS_IN_N` | `int` | `4` | `tools/builtins.go:43` | 单次 image_generate 调用可请求的最大图像数量。 |
| `AIVORY_TOOLS_IN_SIZE` | `string` | `""` | `tools/builtins.go:51` | image_generate 未指定尺寸时使用的可选覆盖值。留空则由供应商自动决定；GPT 图片编辑会在可解析时推断最接近的受支持比例。 |
| `AIVORY_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW` | `duration` | `24*time.Hour` | `tools/builtins.go:45` | 计算每用户图像生成配额账本起始边界时对 Now() 取整的时间窗口。 |
| `AIVORY_TOOLS_IMAGE_IMAGE_INPUT_IMAGE_CAP` | `int` | `0` | `tools/builtins.go:56` | 图生图参考图片数量的全局覆盖值。`0` 表示按模型选择上限（DALL-E 1 张、OpenAI 16 张、Gemini 3 系列 14 张、其他 Gemini 3 张）。 |
| `AIVORY_TOOLS_FETCHREMOTEIMAGE_DOWNLOAD_CAP` | `int64` | `32<<20` | `tools/builtins.go:48` | 通过 SSRF 安全客户端下载图像 API 响应中返回的图像 URL 的字节上限。 |
| `AIVORY_TOOLS_IN_TOP_K_2` | `int` | `5` | `tools/builtins.go:49` | 调用未指定 top_k 时 search_knowledge_base 工具的默认片段数量。 |
| `AIVORY_TOOLS_CONFIDENCE` | `float` | `0.95` | `tools/builtins.go:50` | save_memory 工具创建的每条记忆记录上存储的置信度分数。 |





### 5. 会话 / 消息 / 流式 API

SSE 心跳、流恢复窗口、生成时长上限、分页与搜索上限、消息路径缓存等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_LIMIT_2` | `int` | `200` | `api/conversations_handlers.go:26` | list-conversations 接口在请求未提供 limit 参数时的默认分页大小。 |
| `AIVORY_API_LIMIT_3` | `int` | `500` | `api/conversations_handlers.go:32` | 会话列表接口 ?limit 查询参数的硬性上限，超出即钳制到此值。 |
| `AIVORY_API_SEARCH_MESSAGE_HIT_LIMIT` | `int` | `40` | `api/conversations_handlers.go:99` | 会话搜索接口返回的消息正文命中数上限（标题命中另有单独上限）。 |
| `AIVORY_API_IMPORT_MAX_CONVERSATIONS` | `int` | `1000` | `api/conversations_handlers.go:164` | 单次导入请求可创建的会话数上限，超出则拒绝导入。 |
| `AIVORY_API_IMPORT_MAX_MESSAGES_PER_CONV` | `int` | `10000` | `api/conversations_handlers.go:165` | 导入请求中单个会话允许的消息数上限，超出则拒绝导入。 |
| `AIVORY_API_IMPORT_MAX_CONTENT_BYTES` | `int` | `200*1024` | `api/conversations_handlers.go:166` | 导入时单条消息正文的字节上限，超出部分按此字节数截断。 |
| `AIVORY_API_INLINE_THREAD_QUOTE_CAP` | `int` | `4000` | `api/conversations_handlers.go:282` | 创建行内子话题时引用文本的最大字符（rune）长度，超出则截断。 |
| `AIVORY_API_GETCONVERSATION_ACTIVE_PATH_LIMIT` | `int` | `200` | `api/conversations_handlers.go:345` | 获取会话时活动路径分页 ?limit 参数的上限；超过则返回完整路径。 |
| `AIVORY_API_LIMIT_4` | `int` | `30` | `api/conversations_handlers.go:510` | 消息列表接口未提供 ?limit 时的默认尾部窗口分页大小。 |
| `AIVORY_API_LISTMESSAGES_PAGE_LIMIT` | `int` | `200` | `api/conversations_handlers.go:512` | 消息列表接口 ?limit 参数可接受的最大值；超出则沿用默认分页大小。 |
| `AIVORY_API_RATE_LIMIT_USER` | `int` | `20` | `api/kbs_handlers.go:13` | 每用户限流额度：每分钟允许的知识库文档上传次数上限，超出返回 429。 |
| `AIVORY_API_CONFIDENCE` | `float` | `0.95` | `api/memories_handlers.go:13` | 用户手动创建的记忆条目所存储的置信度分值（0-1 区间）。 |
| `AIVORY_API_MAX_GEN_DURATION` | `duration` | `90*time.Minute` | `api/messages_handlers.go:27` | 单次分离式生成回合的挂钟超时上限，超时后取消其上下文。 |
| `AIVORY_API_SSE_PING_HEARTBEAT_POST` | `duration` | `15*time.Second` | `api/messages_handlers.go:32` | 发送消息流式接口上 SSE 保活 ping 的发送间隔。 |
| `AIVORY_API_SSE_PING_HEARTBEAT_REGENERATE` | `duration` | `15*time.Second` | `api/messages_handlers.go:33` | 重新生成消息流式接口上 SSE 保活 ping 的发送间隔。 |
| `AIVORY_API_SSE_PING_HEARTBEAT_STREAM` | `duration` | `15*time.Second` | `api/messages_handlers.go:34` | 流重连（attach）接口上 SSE 保活 ping 的发送间隔。 |
| `AIVORY_API_STREAM_STATUS_RECHECK_INTERVAL` | `duration` | `5*time.Second` | `api/messages_handlers.go:35` | 流重连处理器重新轮询消息生成状态以检测终态的间隔。 |
| `AIVORY_API_STREAM_REPLAY_BATCH_SIZE` | `int` | `200` | `api/messages_handlers.go:36` | 重连 SSE 流回放/追赶时每批读取的缓冲流事件数量。 |
| `AIVORY_API_ONLINE_PRESENCE_TOUCH_THROTTLE` | `duration` | `time.Minute` | `api/middleware.go:23` | 用户在线状态 seen 触达之间的节流间隔，即 seen 缓存标记的 TTL。 |
| `AIVORY_API_CONCURRENT_GEN_SLOT_SAFETY_TTL` | `duration` | `30*time.Minute` | `api/middleware.go:24` | 每用户并发生成槽计数器的安全 TTL，使失效的占用槽自动过期。 |
| `AIVORY_API_REQUEST_SIGNATURE_REPLAY_WINDOW_FUTURE` | `int64` | `300` | `api/middleware.go:25` | 请求签名防重放：X-Req-Ts 时间戳落后服务器时间的最大秒数，超出则视为过期拒绝。 |
| `AIVORY_API_REQUEST_SIGNATURE_REPLAY_WINDOW_PAST` | `int64` | `60` | `api/middleware.go:26` | 请求签名防重放：X-Req-Ts 时间戳领先服务器时间的最大秒数（时钟偏移容忍），超出则拒绝。 |
| `AIVORY_API_CREDIT_MULTIPLIER` | `float` | `5.0` | `api/models_handlers.go:21` | 将模型输入+输出合计价格换算为选择器中相对积分倍率的除数。 |
| `AIVORY_API_JSON_REQUEST_BODY_SIZE_CAP` | `int64` | `4<<20` | `api/mux.go:16` | JSON 请求体可接受的最大字节数，超出由 MaxBytesReader 拒绝。 |
| `AIVORY_API_PROJECT_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `200` | `api/projects_handlers.go:15` | 项目详情响应中加载的活动会话数量（获取上限，偏移为 0）。 |
| `AIVORY_API_RATE_LIMIT_USER_2` | `int` | `20` | `api/projects_handlers.go:16` | 每用户限流额度：每分钟允许的项目文档上传次数上限，超出返回 429。 |
| `AIVORY_API_RATE_LIMIT_REGISTER_MAX` | `int` | `5` | `api/router.go:51` | POST /api/auth/register 的每 IP 限流额度：每个窗口内允许的最大注册尝试次数，超出返回 429。 |
| `AIVORY_API_RATE_LIMIT_REGISTER_WINDOW` | `duration` | `60*time.Second` | `api/router.go:52` | POST /api/auth/register 每 IP 限流的滚动窗口时长。 |
| `AIVORY_API_RATE_LIMIT_LOGIN_MAX` | `int` | `10` | `api/router.go:54` | POST /api/auth/login 的每 IP 限流额度：每个窗口内允许的最大登录尝试次数，超出返回 429。 |
| `AIVORY_API_RATE_LIMIT_LOGIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:55` | POST /api/auth/login 每 IP 限流的滚动窗口时长。 |
| `AIVORY_API_RATE_LIMIT_LOGIN_2FA_MAX` | `int` | `10` | `api/router.go:57` | POST /api/auth/login/2fa 的每 IP 限流额度：每个窗口内允许的最大两步验证尝试次数，超出返回 429。 |
| `AIVORY_API_RATE_LIMIT_LOGIN_2FA_WINDOW` | `duration` | `60*time.Second` | `api/router.go:58` | POST /api/auth/login/2fa 每 IP 限流的滚动窗口时长。 |
| `AIVORY_API_RATE_LIMIT_LOGOUT_MAX` | `int` | `30` | `api/router.go:60` | POST /api/auth/logout 的每 IP 限流额度：每个窗口内允许的最大登出请求次数，超出返回 429。 |
| `AIVORY_API_RATE_LIMIT_LOGOUT_WINDOW` | `duration` | `60*time.Second` | `api/router.go:61` | POST /api/auth/logout 每 IP 限流的滚动窗口时长。 |
| `AIVORY_API_RATE_LIMIT_REFRESH_MAX` | `int` | `30` | `api/router.go:63` | POST /api/auth/refresh 的每 IP 限流额度：每个窗口内允许的最大令牌刷新请求次数，超出返回 429。 |
| `AIVORY_API_RATE_LIMIT_REFRESH_WINDOW` | `duration` | `60*time.Second` | `api/router.go:64` | POST /api/auth/refresh 每 IP 限流的滚动窗口时长。 |
| `AIVORY_API_RATE_LIMIT_VERIFY_EMAIL_MAX` | `int` | `10` | `api/router.go:66` | POST /api/auth/verify-email 的每 IP 限流额度：每个窗口内允许的最大邮箱验证尝试次数，超出返回 429。 |
| `AIVORY_API_RATE_LIMIT_VERIFY_EMAIL_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:67` | POST /api/auth/verify-email 每 IP 限流的滚动窗口时长。 |
| `AIVORY_API_RATE_LIMIT_SEND_CODE_MAX` | `int` | `3` | `api/router.go:69` | 每 IP 在窗口内对 POST /api/auth/send-code（发送登录/验证码邮件）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_SEND_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:70` | 统计每 IP 对 /api/auth/send-code 发码接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_FORGOT_PASSWORD_MAX` | `int` | `5` | `api/router.go:72` | 每 IP 在窗口内对 POST /api/auth/forgot-password（触发密码重置邮件）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_FORGOT_PASSWORD_WINDOW` | `duration` | `15*60*time.Second` | `api/router.go:73` | 统计每 IP 对 /api/auth/forgot-password 接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_RESET_PASSWORD_MAX` | `int` | `5` | `api/router.go:75` | 每 IP 在窗口内对 POST /api/auth/reset-password（凭码重置密码）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_RESET_PASSWORD_WINDOW` | `duration` | `60*time.Second` | `api/router.go:76` | 统计每 IP 对 /api/auth/reset-password 接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_ISSUE_MAX` | `int` | `30` | `api/router.go:78` | 每 IP 在窗口内对 GET /api/public/captcha（签发滑块验证码挑战）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_ISSUE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:79` | 统计每 IP 对 GET /api/public/captcha 挑战签发接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_VERIFY_MAX` | `int` | `60` | `api/router.go:81` | 每 IP 在窗口内对 POST /api/public/captcha/verify（校验验证码答案）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_VERIFY_WINDOW` | `duration` | `60*time.Second` | `api/router.go:82` | 统计每 IP 对 /api/public/captcha/verify 答案校验接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_FIRST_RUN_SETUP_MAX` | `int` | `10` | `api/router.go:84` | 每 IP 在窗口内对 POST /api/setup（首次运行创建首个管理员）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_FIRST_RUN_SETUP_WINDOW` | `duration` | `60*time.Second` | `api/router.go:85` | 统计每 IP 对 POST /api/setup 首次运行引导接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_MAX` | `int` | `60` | `api/router.go:87` | 每 IP 在窗口内对 GET /api/public/shared/:token（免登录查看分享会话）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_WINDOW` | `duration` | `60*time.Second` | `api/router.go:88` | 统计每 IP 对 /api/public/shared/:token 分享查看接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_MAX` | `int` | `240` | `api/router.go:90` | 每 IP 在窗口内对分享会话的文件与产物（artifact）资源路由的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_WINDOW` | `duration` | `60*time.Second` | `api/router.go:91` | 统计每 IP 对分享会话文件/产物资源路由请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_MAX` | `int` | `20` | `api/router.go:93` | 每 IP 在窗口内对 OAuth 发起、回调与跨域交接路由的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_WINDOW` | `duration` | `60*time.Second` | `api/router.go:94` | 统计每 IP 对 OAuth 发起/回调/交接路由请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_PASSWORD_CHANGE_SET_MAX` | `int` | `5` | `api/router.go:96` | 每 IP 在窗口内对 /api/me/password 改密与 /api/me/password/set 设密接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_PASSWORD_CHANGE_SET_WINDOW` | `duration` | `60*time.Second` | `api/router.go:97` | 统计每 IP 对 /api/me/password 改密/设密接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_IDENTITY_LINK_START_MAX` | `int` | `20` | `api/router.go:99` | 每 IP 在窗口内对 POST /api/me/identities/:id/link（发起 OAuth 账号关联）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_IDENTITY_LINK_START_WINDOW` | `duration` | `60*time.Second` | `api/router.go:100` | 统计每 IP 对 OAuth 身份关联发起接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_MAX` | `int` | `10` | `api/router.go:102` | 每 IP 在窗口内对 /api/me/2fa 的 setup、enable、disable 各接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:103` | 统计每 IP 对 2FA setup/enable/disable 接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_REDEEM_CODE_MAX` | `int` | `10` | `api/router.go:105` | 每 IP 在窗口内对 POST /api/me/redeem（兑换码兑换）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_REDEEM_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:106` | 统计每 IP 对 /api/me/redeem 兑换接口请求数的固定时间窗。 |
| `AIVORY_API_RATE_LIMIT_WORKSPACE_JOIN_MAX` | `int` | `30` | `api/router.go:108` | 每 IP 在窗口内对 /api/workspaces/join/:token（邀请信息与加入）接口的最大请求数。 |
| `AIVORY_API_RATE_LIMIT_WORKSPACE_JOIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:109` | 统计每 IP 对 /api/workspaces/join/:token 接口请求数的固定时间窗。 |
| `AIVORY_API_SELF_USAGE_LOOKBACK_WINDOW` | `int` | `30` | `api/user_handlers.go:209` | GET /api/me/usage 统计调用者自身消息数时回溯的天数。 |
| `AIVORY_API_LIMIT` | `int` | `200` | `api/workspaces_handlers.go:15` | 管理端工作区列表接口在请求未带 ?limit 时返回的默认最大工作区数。 |
| `AIVORY_API_ADMIN_WORKSPACE_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `500` | `api/workspaces_handlers.go:16` | 管理端工作区详情（triage）视图加载的会话数量上限。 |
| `AIVORY_QUEUE_IN_PROCESS_WORKERS` | `int` | `8` | `queue/queue.go:47` | 进程内后台任务池的并发 worker 协程数量。 |
| `AIVORY_QUEUE_PROCESS_JOB_BUFFER` | `int` | `256` | `queue/queue.go:48` | 进程内任务通道的缓冲槽位数；缓冲满时 Enqueue 将新任务转入退避回退路径执行。 |
| `AIVORY_QUEUE_QUEUE_BACKPRESSURE_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:49` | 进程内任务缓冲满时经退避回退协程执行的任务的上下文超时。 |
| `AIVORY_QUEUE_QUEUE_WORKER_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:50` | 进程内队列 worker 执行每个任务时施加的上下文超时。 |





### 6. 认证 / 会话 / 验证码

令牌缓存、验证码有效期与尝试次数、TOTP 时窗、OAuth state TTL 等（不含约定俗成的格式常量，如验证码位数）。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_MAX_CODE_ATTEMPTS` | `int` | `5` | `api/auth_handlers.go:24` | 对邮寄的验证/重置验证码允许的错误尝试次数，达到后即作废该码。 |
| `AIVORY_API_CODE_FAILURE_COUNTER_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:28` | 按邮箱记录验证/重置码错误尝试次数的计数器的存活时长（TTL）。 |
| `AIVORY_API_MINIMUM_PASSWORD_LENGTH` | `int` | `8` | `api/auth_handlers.go:29` | 注册及修改密码时用户自选账户密码所需的最小字符长度。 |
| `AIVORY_API_EMAIL_VERIFICATION_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:30` | 邮件发送的账户邮箱验证码的有效期。 |
| `AIVORY_API_PASSWORD_RESET_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:31` | 邮件发送的密码重置验证码的有效期。 |
| `AIVORY_API_CAP_TOL` | `float` | `0.04` | `api/captcha.go:41` | 滑块验证码通过时，提交的滑动比例与真实缺口比例之间允许的误差。 |
| `AIVORY_API_CAPTCHA_CHALLENGE_CACHE_TTL` | `duration` | `5*time.Minute` | `api/captcha.go:44` | 未解答的滑块拼图验证码挑战在缓存中保持有效的时长。 |
| `AIVORY_API_CAPTCHA_PASS_TTL` | `duration` | `10*time.Minute` | `api/captcha.go:110` | 证明近期已通过验证码的签名通行令牌的有效期。 |
| `AIVORY_API_OAUTH_2FA_HANDOFF_COOKIE_TTL` | `duration` | `300*time.Second` | `api/oauth_handlers.go:24` | OAuth 登录后将两步验证登录票据传给前端的短时 HttpOnly Cookie 的 Max-Age。 |
| `AIVORY_API_OAUTH_STATE_CACHE_TTL` | `duration` | `10*time.Minute` | `api/oauth_handlers.go:25` | 作为 CSRF 防护的 OAuth 授权流程 state 缓存条目的有效期。 |
| `AIVORY_API_OAUTH_TOKEN_EXCHANGE_CONTEXT_TIMEOUT` | `duration` | `20*time.Second` | `api/oauth_handlers.go:26` | OAuth 回调中授权码换取令牌及拉取用户信息的超时上限。 |
| `AIVORY_API_OAUTH_CROSS_DOMAIN_HANDOFF_TOKEN_TTL` | `duration` | `60*time.Second` | `api/oauth_handlers.go:27` | 前端用以换取会话的一次性跨域 OAuth 交接令牌的有效期。 |
| `AIVORY_API_2FA_LOGIN_TICKET_BURN_THRESHOLD` | `int64` | `5` | `api/twofa_handlers.go:24` | 针对某个两步验证登录票据允许的错误 TOTP 验证码次数，达到即作废该票据。 |
| `AIVORY_API_ISSUE_TWOFA_TICKET` | `duration` | `5*time.Minute` | `api/twofa_handlers.go:25` | 密码正确后、待输入 TOTP 验证码期间所签发的两步验证登录票据的有效期。 |
| `AIVORY_OAUTH_APPLE_CLIENT_SECRET_JWT_EXPIRY` | `duration` | `30*time.Minute` | `oauth/oauth.go:71` | 生成的 Apple OAuth 客户端密钥 JWT 的有效期。 |





### 7. 上传 / 文件 / 分享

图片处理、存储清理周期、直传分片、分享令牌、下载缓存 TTL 等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_ADMIN_ICON_UPLOAD_SIZE` | `int64` | `256*1024` | `api/admin_uploads.go:59` | 管理员上传的模型图标图片所接受的最大字节数。 |
| `AIVORY_API_AUDIO_TRANSCRIPTION_UPSTREAM_HTTP_TIMEOUT` | `duration` | `120*time.Second` | `api/audio_handlers.go:21` | 将音频转发至上游转写 API 的 HTTP 请求的超时时间。 |
| `AIVORY_API_AUDIO_TRANSCRIPTION_USER_RATE_LIMIT` | `int` | `20` | `api/audio_handlers.go:26` | 单个用户每分钟可发起的音频转写请求数上限。 |
| `AIVORY_API_TRANSCRIPTION_UPSTREAM_RESPONSE_READ_CAP` | `int64` | `1<<20` | `api/audio_handlers.go:27` | 读取上游转写响应到内存的字节上限。 |
| `AIVORY_API_AUDIO_STREAM_USER_RATE_LIMIT` | `int` | `30` | `api/audio_stream_handler.go:30` | 单个用户每分钟可发起的实时语音流式（WebSocket）会话数上限。 |
| `AIVORY_API_AUDIO_STREAM_MAX_BYTES` | `int64` | `24*1024*1024` | `api/audio_stream_handler.go:33` | 单次实时语音流式会话允许中继的音频字节数上限，超出即断开连接。 |
| `AIVORY_API_AUDIO_STREAM_MAX_SESSION` | `duration` | `15*time.Minute` | `api/audio_stream_handler.go:34` | 单次实时语音流式会话（麦克风 → 火山 ASR 中继）的硬性时长上限。 |
| `AIVORY_ASR_DEBUG` | `bool` | `false` | `api/audio_stream_handler.go:39` | 记录每个解码后的火山 ASR 返回帧（消息码、末包标记、转写文本长度、原始 JSON 负载），用于排查实时转写问题。默认关闭——原始负载含用户语音内容，需显式开启。 |
| `AIVORY_API_UPLOAD_RATE_LIMIT_MAX` | `int` | `20` | `api/files_handlers.go:25` | 上传限流窗口内单个用户可进行的文件上传次数上限。 |
| `AIVORY_API_UPLOAD_RATE_LIMIT_WINDOW` | `duration` | `time.Minute` | `api/files_handlers.go:26` | 对用户文件上传进行限流计数的时间窗口。 |
| `AIVORY_API_OBJECT_STORAGE_DELETE_TIMEOUT_CLEANUP` | `duration` | `30*time.Second` | `api/storage_cleanup.go:17` | 存储清理时从对象存储删除单个孤立文件的超时时间。 |
| `AIVORY_STORAGE_S3_DIRECT_UPLOAD_MIN_CLIENT_TIMEOUT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:28` | 直传 S3/OSS 上传所需的最小 HTTP 客户端超时；复用的客户端低于此值时会新建一个。 |
| `AIVORY_STORAGE_DIRECT_S3_OSS_UPLOAD_HTTP_CLIENT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:29` | 复用客户端超时过短时，为直传 S3/OSS 上传新建的 HTTP 客户端的超时时间。 |
| `AIVORY_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_CONNECT` | `int64` | `30` | `storage/s3_direct.go:30` | 直传所用阿里云 OSS 客户端的连接超时（秒）。 |
| `AIVORY_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_RW` | `int64` | `300` | `storage/s3_direct.go:31` | 直传所用阿里云 OSS 客户端的读写超时（秒）。 |
| `AIVORY_STORAGE_PRESIGN_URL_TTL` | `duration` | `3600*time.Second` | `storage/s3_direct.go:32` | 调用方未指定有效期时，为直传预签名 GET URL 应用的过期时间。 |
| `AIVORY_STORAGE_PRESIGN_URL_TTL_CLAMP_CEILING` | `duration` | `86400*time.Second` | `storage/s3_direct.go:33` | 钳制调用方请求的直传预签名 GET URL 有效期的上限。 |
| `AIVORY_STORAGE_SIDECAR_STORAGE_CLIENT_HTTP_TIMEOUT` | `duration` | `5*time.Minute` | `storage/storage.go:31` | 调用沙箱 sidecar 对象存储 put/delete 接口的 HTTP 往返超时。 |





### 8. 管理后台任务（备份 / 向量维护 / 兑换码）

备份大小上限与异步轮询、向量维护批量、兑换码上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_BACKUP_EXPORT_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_backup_async.go:26` | 内存历史中保留的过往备份导出任务记录数量，超出后修剪最旧的记录。 |
| `AIVORY_API_BACKUP_EXPORT_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_backup_async.go:27` | 后台备份导出任务被取消前允许的最长实际运行时间。 |
| `AIVORY_API_CONFIG_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `16<<20` | `api/admin_backup_handlers.go:25` | 解析管理员配置导入 multipart 上传的内存缓冲区；超出部分溢写到临时文件。 |
| `AIVORY_API_BACKUP_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `32<<20` | `api/admin_backup_handlers.go:26` | 解析完整备份导入 multipart 上传的内存缓冲区；超出部分溢写到临时文件。 |
| `AIVORY_API_MAX_CONFIG_SIZE` | `int64` | `512<<20` | `api/admin_backup_handlers.go:373` | 导入管理员配置归档时接受的最大请求体大小。 |
| `AIVORY_API_QDRANT_ARCHIVE_REQUEST_TIMEOUT` | `duration` | `5*time.Minute` | `api/admin_backup_qdrant.go:26` | 导出或导入备份归档时调用 Qdrant 的单次 HTTP 请求超时。 |
| `AIVORY_API_QDRANT_EXPORT_SCROLL_PAGE_SIZE` | `int` | `256` | `api/admin_backup_qdrant.go:28` | 导出 Qdrant 集合到归档时每个 /points/scroll 页拉取的点数。 |
| `AIVORY_API_QDRANT_IMPORT_UPSERT_FLUSH_BATCH_SIZE` | `int` | `128` | `api/admin_backup_qdrant.go:29` | 导入 Qdrant 集合时，每批累积到该点数即刷写一次 upsert。 |
| `AIVORY_API_ADMIN_USER_LIST_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:20` | 管理员用户列表接口在请求未显式指定 limit 时的默认返回条数。 |
| `AIVORY_API_ADMIN_CREATED_USER_MIN_PASSWORD_LENGTH` | `int` | `8` | `api/admin_handlers.go:21` | 管理员创建用户账户时强制的最小密码长度。 |
| `AIVORY_API_ADMIN_PASSWORD_RESET_MIN_LENGTH` | `int` | `8` | `api/admin_handlers.go:22` | 管理员重置用户密码接口中新密码所需的最小字符长度。 |
| `AIVORY_API_ADMIN_USER_CONVERSATIONS_LISTING_CAP` | `int` | `500` | `api/admin_handlers.go:23` | 管理员查看单个用户会话（用于支持/滥用排查）时返回的最大会话数。 |
| `AIVORY_API_USAGE_REPORT_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:24` | 管理员用量记录报表在请求的 page_size 缺失或超出 1-200 范围时采用的默认每页条数。 |
| `AIVORY_API_ANALYTICS_WINDOW` | `int` | `30` | `api/admin_handlers.go:25` | 管理员分析看板在未提供 days 查询参数时的默认回溯窗口（天）。 |
| `AIVORY_API_ANALYTICS_WINDOW_2` | `int` | `365` | `api/admin_handlers.go:26` | 管理员分析窗口 days 查询参数可接受的天数上限，超出范围的值将被忽略。 |
| `AIVORY_API_ANALYTICS_BREAKDOWN_TOP_N` | `int` | `8` | `api/admin_handlers.go:27` | 管理员分析中按模型和按用户细分及其时间序列所保留的头部键数量（Top-N）。 |
| `AIVORY_API_BULK_REDEEM_CODE_GENERATION_QUANTITY` | `int` | `1000` | `api/admin_redeem_handlers.go:18` | 单次批量生成请求可铸造的最大兑换码数量。 |
| `AIVORY_API_MAX_SKILL_ASSET_BYTES` | `int64` | `20*1024*1024` | `api/admin_skill_assets.go:24` | 单个技能资源文件（模板/脚本/小型数据）上传的最大字节数。 |
| `AIVORY_API_VECTOR_MAINTENANCE_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_vectors_handlers.go:18` | 向量维护任务历史在内存中保留的最近任务数，更早的会被丢弃。 |
| `AIVORY_API_VECTOR_MAINTENANCE_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_vectors_handlers.go:19` | 单次向量维护任务运行（索引审计或重建）在被取消前的上下文超时时间。 |





### 9. 服务器启动 / 配置加载

HTTP server 超时、优雅关闭、启动流程常量等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_CMD_ARCHIVE_GC_BOOT_SETTLE_DELAY` | `duration` | `2*time.Minute` | `cmd/api/main.go:40` | 服务器启动后到首次归档工作区 GC 清扫之间的延迟，避免冷启动时立即清扫。 |
| `AIVORY_CMD_RUN_PRUNE` | `duration` | `5*time.Minute` | `cmd/api/main.go:41` | 单次归档工作区 GC 清理（针对对象存储）运行的上下文超时时间。 |
| `AIVORY_CMD_ARCHIVE_GC_SWEEP_INTERVAL` | `duration` | `6*time.Hour` | `cmd/api/main.go:42` | 归档工作区 GC 清扫之间的间隔，用于从对象存储删除过期的 /workspace 归档包。 |





### 10. 前端

**编译期**生效（Vite 在 `npm run build` 时内联 `VITE_*`），需要在构建环境设置，运行时改环境变量无效。轮询间隔、分页、重试 / 退避、去抖、超时、客户端大小上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `VITE_AIVORY_IMAGE_API_MY_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:181` | 拉取当前登录用户自己的生成图库时的默认每页数量（limit）。 |
| `VITE_AIVORY_WORKSPACES_API_ADMIN_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:289` | 管理员工作区列表请求的默认每页数量（limit）。 |
| `VITE_AIVORY_CONVERSATIONS_API_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:318` | 会话列表请求的默认每页数量（limit）。 |
| `VITE_AIVORY_CONVERSATIONS_API_LIST_ARCHIVED_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:326` | 已归档会话列表请求的默认每页数量（limit）。 |
| `VITE_AIVORY_ADMIN_API_USERS_LIMIT` | `envNum` | `50` | `src/api/endpoints.ts:594` | 管理员用户列表请求的默认每页数量（limit）。 |
| `VITE_AIVORY_ADMIN_API_USER_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:626` | 管理员下钻查看某用户生成图库时的默认每页数量（limit）。 |
| `VITE_AIVORY_ADMIN_API_ANALYTICS` | `envNum` | `30` | `src/api/endpoints.ts:674` | 管理员分析看板请求发送的默认回溯窗口（天）。 |
| `VITE_AIVORY_MAX_BYTES` | `envNum` | `256 * 1024` | `src/components/admin/icon-uploader.tsx:34` | 管理员图标上传的客户端最大字节数，超出则在请求前拒绝（与后端上限一致）。 |
| `VITE_AIVORY_MAX_LEN` | `envNum` | `12_000` | `src/components/chat/composer.tsx:99` | 聊天输入框消息的最大字符长度，超出则禁止发送。 |
| `VITE_AIVORY_INGEST_POLL_MS` | `envNum` | `1200` | `src/components/chat/composer.tsx:113` | 在发送按钮保持锁定期间，轮询附件摄取（解析/嵌入）状态的间隔（毫秒）。 |
| `VITE_AIVORY_PAGE` | `envNum` | `30` | `src/components/chat/my-gallery.tsx:12` | 用户生成图库无限滚动每页拉取的图片数（首屏加载与加载更多）。 |
| `VITE_AIVORY_RUN_TIMEOUT_MS` | `envNum` | `120_000` | `src/lib/pyodide-runner.ts:39` | 浏览器内 Pyodide Python 单次运行的硬性挂钟上限（毫秒），超时则终止 worker。 |
| `VITE_AIVORY_MAX_STREAM_CHARS` | `envNum` | `200_000` | `src/lib/pyodide-runner.ts:41` | Pyodide 运行中流式 stdout/stderr 的最大字符总数，超出则截断以防止失控打印。 |
| `VITE_AIVORY_MAX_RESULT_CHARS` | `envNum` | `20_000` | `src/lib/pyodide-runner.ts:43` | Pyodide 运行最终表达式结果 repr() 在截断前的最大字符长度。 |
| `VITE_AIVORY_ADMIN_BACKUP_EXPORT_JOB_POLL_INTERVAL` | `envNum` | `2500` | `src/pages/admin/AdminBackup.tsx:50` | 管理员备份页面轮询刷新正在运行的备份导出任务状态的间隔（毫秒）。 |
| `VITE_AIVORY_PAGE_SIZE_2` | `envNum` | `20` | `src/pages/admin/AdminRedeemCodes.tsx:80` | 管理员兑换码表格每页行数（对已获取行的客户端分页）。 |
| `VITE_AIVORY_PAGE_SIZE` | `envNum` | `50` | `src/pages/admin/AdminUsage.tsx:33` | 管理员用量记录表格请求的每页行数（服务端 pageSize）。 |
| `VITE_AIVORY_IMAGES_PAGE` | `envNum` | `60` | `src/pages/admin/AdminUserLibrary.tsx:29` | 管理员用户图库下钻视图每页拉取的图片数（首屏加载与加载更多）。 |
| `VITE_AIVORY_ONLINE_WINDOW_S` | `envNum` | `300` | `src/pages/admin/AdminUsers.tsx:42` | 管理员用户表格中，用户最后活动落在该时间窗口（秒）内即视为在线。 |
| `VITE_AIVORY_PAGE_SIZE_3` | `envNum` | `50` | `src/pages/admin/AdminUsers.tsx:83` | 管理员用户列表表格请求的每页行数（服务端 limit）。 |
| `VITE_AIVORY_KB_DOC_STATUS_POLL_INTERVAL` | `envNum` | `2200` | `src/pages/kb/KnowledgeBaseDetail.tsx:41` | 当仍有文档处于解析/嵌入阶段时，轮询刷新知识库文档状态的间隔（毫秒）。 |
| `VITE_AIVORY_CONV_PAGE` | `envNum` | `200` | `src/store/conversations.ts:52` | 侧边栏会话列表每页拉取的会话数（首屏加载与滚动加载）。 |
| `VITE_AIVORY_MSG_PAGE` | `envNum` | `40` | `src/store/conversations.ts:66` | 打开会话时每页拉取的消息数（首页与向上滚动加载更早消息）。 |
