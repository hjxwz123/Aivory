# Aivory 高级环境变量参考

> 版本 v2.0 · 2026-07-10 · 由整仓源码生成（对应本次改动：所有此前「硬编码但值得调整」的参数均已改为环境变量可覆盖）
>
> **这 298 个环境变量全部是可选的，未在 `.env.example` 中列出。** 不设置任何一个，Aivory 的行为与改动前完全一致——每个变量的默认值就是原来的硬编码值。只有当你需要调整某个具体参数时，才在部署环境里添加对应的变量。
> 如果需要调整，把用到的变量抄一份加进你自己的 `.env` 即可（`.env.example` 保持精简，不会自动包含这些高级选项）。
>
> **后端（Go）**：269 个，读取自进程环境变量，改动后**需重启 `aivory-api` 进程**生效。
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

共 **298** 个环境变量，按子系统分布：

| 子系统 | 变量数 |
| --- | --- |
| 1. LLM 对话 / 编排 / 内部模型调用 | 65 |
| 2. RAG 文档解析 / 向量检索 | 59 |
| 3. 沙盒代码执行 | 9 |
| 4. 内置工具（搜索 / Python / 网络安全） | 14 |
| 5. 会话 / 消息 / 流式 API | 73 |
| 6. 认证 / 会话 / 验证码 | 15 |
| 7. 上传 / 文件 / 分享 | 17 |
| 8. 管理后台任务（备份 / 向量维护 / 兑换码） | 20 |
| 9. 服务器启动 / 配置加载 | 3 |
| 10. 前端 | 23 |
| **合计** | **298** |

---

### 1. LLM 对话 / 编排 / 内部模型调用

主对话流、工具循环、TTFT 看门狗，以及压缩 / 记忆 / 审核 / 校验 / 深度研究 / 文档生成等内部模型调用。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_LLM_APPLY_ANTHROPIC_THINKING_SETTINGS` | `int` | `2048` | `llm/anthropic_provider.go:24` | Token headroom added above extended-thinking budget_tokens when raising Anthropic max_tokens so output covers thinking plus reply. |
| `AIVORY_LLM_MAX_ITER` | `int` | `20` | `llm/anthropic_provider.go:160` | Hard cap on native tool-use rounds (Messages API calls) in the Anthropic streaming Stream loop. |
| `AIVORY_LLM_MAX_TOK` | `int` | `64000` | `llm/anthropic_provider.go:169` | Default max_tokens sent on each Anthropic Messages request in the streaming tool loop unless the request overrides it. |
| `AIVORY_LLM_MAX_TOK_2` | `int` | `64000` | `llm/anthropic_provider.go:322` | Default max_tokens for the single Anthropic call in prompt-tool mode (promptRunOnce) unless the request overrides it. |
| `AIVORY_LLM_INFLIGHT_GRACE` | `duration` | `15*time.Minute` | `llm/compaction.go:55` | Grace window a status=streaming assistant row stays protected from summarisation before it counts as a crash leftover. |
| `AIVORY_LLM_T` | `int` | `4` | `llm/compaction.go:62` | Per-message structural token overhead (role markers, framing) added to every message's compaction token estimate. |
| `AIVORY_LLM_MESSAGE_TOKEN_MEMO_CACHE_BOUND` | `int` | `100000` | `llm/compaction.go:63` | Max entries in the per-message token-estimate memo map before it is reset in place to bound memory. |
| `AIVORY_LLM_SUMMARY_TOKENS_CLAMP_FLOOR` | `int` | `256` | `llm/compaction.go:64` | Floor for the admin summary_max_tokens setting; a smaller configured value is reset to the default summary budget. |
| `AIVORY_LLM_BIG_TOKEN_OVERFLOW_NUM` | `int` | `5` | `llm/compaction.go:65` | Numerator of the token-trigger multiple (num/den, default 5/4 = 1.25x) above which overflow is summarised inline this turn. |
| `AIVORY_LLM_BIG_TOKEN_OVERFLOW_DEN` | `int` | `4` | `llm/compaction.go:66` | Denominator of the token-trigger multiple (num/den, default 5/4 = 1.25x) above which overflow is summarised inline this turn. |
| `AIVORY_LLM_INLINE_COMPACTION_BACKLOG_FACTOR` | `int` | `3` | `llm/compaction.go:67` | Multiplier on keepRounds*2 for the un-summarised tail length that forces inline (rather than async) compaction this turn. |
| `AIVORY_LLM_SUMMARY_MERGE_BUDGET` | `int` | `2048` | `llm/compaction.go:76` | Total accumulated summary-token threshold that triggers folding old summary blocks into a coarser one. |
| `AIVORY_LLM_DETERMINISTIC_SUMMARY_CLIP_BUDGET` | `int` | `300` | `llm/compaction.go:68` | Token budget for the deterministic fallback clip of older rounds used when the task-model summary comes back empty. |
| `AIVORY_LLM_ATTEMPT` | `int` | `4` | `llm/compaction.go:69` | Max compare-and-swap retry attempts when appending a new summary block to the conversation's summary_blocks. |
| `AIVORY_LLM_ITER` | `int` | `3` | `llm/compaction.go:70` | Max repeated fold iterations used to bring a conversation path's summary tokens back under budget. |
| `AIVORY_LLM_MAX_OUTPUT_TOKENS_5` | `int` | `2` | `llm/compaction.go:71` | Divisor applied to the merge budget to derive the fold call's MaxOutputTokens and deterministic clip length. |
| `AIVORY_LLM_CHUNK_SIZE` | `int` | `400` | `llm/compaction.go:810` | Message-ID batch size per SQL IN(...) query when re-checking that summarised messages still exist (driver placeholder-limit chunking). |
| `AIVORY_LLM_DR_MAX_ROUNDS` | `int` | `4` | `llm/deep_research.go:47` | Hard cap on the number of search-then-verify rounds the deep-research engine runs. |
| `AIVORY_LLM_DR_QUERIES_PER_ROUND` | `int` | `6` | `llm/deep_research.go:48` | Maximum search queries dispatched per deep-research round. |
| `AIVORY_LLM_DR_FETCH_PER_ROUND` | `int` | `5` | `llm/deep_research.go:49` | Maximum new source candidates picked and read per deep-research round. |
| `AIVORY_LLM_DR_MIN_DEEP_READS` | `int` | `5` | `llm/deep_research.go:50` | Minimum deep-read sources required before deep research may settle, even once coverage gaps look sufficient. |
| `AIVORY_LLM_DR_SEARCH_TOP_K` | `int` | `8` | `llm/deep_research.go:51` | Number of results requested per deep-research search call (top_k). |
| `AIVORY_LLM_DR_WALL_CLOCK` | `duration` | `5*time.Minute` | `llm/deep_research.go:52` | Overall wall-clock timeout bounding an entire deep-research engine run. |
| `AIVORY_LLM_DR_CALL_TIMEOUT` | `duration` | `30*time.Second` | `llm/deep_research.go:53` | Per-call timeout for an individual deep-research search or fetch request. |
| `AIVORY_LLM_DEEP_RESEARCH_VALIDATE_TIMEOUT` | `duration` | `75*time.Second` | `llm/deep_research.go:64` | Timeout bounding the deep-research validate pass that scrutinises weak/single-source claims before writing. |
| `AIVORY_LLM_SCORE_A` | `float` | `9` | `llm/deep_research.go:67` | Ranking score added to a candidate source whose URL is credibility grade A (credibility dominates ranking). |
| `AIVORY_LLM_SCORE_B` | `float` | `6` | `llm/deep_research.go:68` | Ranking score added to a candidate source whose URL is credibility grade B. |
| `AIVORY_LLM_SCORE_C` | `float` | `3` | `llm/deep_research.go:69` | Ranking score added to a candidate source whose URL is credibility grade C. |
| `AIVORY_LLM_SCORE_KW` | `float` | `1` | `llm/deep_research.go:70` | Ranking score added per question keyword (over 3 chars) found in a candidate's title or snippet. |
| `AIVORY_LLM_SCORE_FRESH_DOMAIN` | `float` | `2` | `llm/deep_research.go:71` | Ranking bonus added to a candidate from a domain not yet seen in this research run. |
| `AIVORY_LLM_MAX_ITER_4` | `int` | `20` | `llm/google_provider.go:68` | Hard cap on native tool-use rounds (generateContent calls) in the Gemini streaming Stream loop. |
| `AIVORY_LLM_GEMINI_MAX_TOK` | `int` | `64000` | `llm/google_provider.go:78` | Default generationConfig.maxOutputTokens sent on each Gemini streaming request unless the request overrides it. |
| `AIVORY_LLM_GEMINI_MAX_TOK_2` | `int` | `64000` | `llm/google_provider.go:472` | Default generationConfig.maxOutputTokens for the Gemini prompt-tool-mode call unless the request overrides it. |
| `AIVORY_LLM_CONF` | `float` | `0.7` | `llm/memory_worker.go:40` | Fallback confidence assigned to an extracted memory when the extractor returns a value outside (0,1]. |
| `AIVORY_LLM_OFFICIAL_TOOL_SPEC` | `string` | `"medium"` | `llm/openai_provider.go:23` | search_context_size value passed to OpenAI's official built-in web_search tool spec. |
| `AIVORY_LLM_MAX_ITER_2` | `int` | `20` | `llm/openai_provider.go:110` | Hard cap on native tool-use rounds (Chat Completions calls) in the OpenAI streamChat loop. |
| `AIVORY_LLM_MAX_ITER_3` | `int` | `20` | `llm/openai_provider.go:610` | Hard cap on native tool-use rounds (Responses API calls) in the OpenAI streamResponses loop. |
| `AIVORY_LLM_INLINE_QUOTE_SOURCE_INJECTION_CAP` | `int` | `8000` | `llm/orchestrator.go:30` | Max runes of the source message text injected alongside a highlighted excerpt in an inline-quote sub-conversation before truncation. |
| `AIVORY_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MAX` | `int` | `600` | `llm/orchestrator.go:37` | Upper bound (seconds) the admin sandbox_exec_timeout_sec is clamped to when sizing the python_execute call context. |
| `AIVORY_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MIN` | `int` | `10` | `llm/orchestrator.go:38` | Lower bound (seconds) the admin sandbox_exec_timeout_sec is clamped to when sizing the python_execute call context. |
| `AIVORY_LLM_SANDBOX_EXEC_CTX_SAFETY_MARGIN` | `duration` | `150*time.Second` | `llm/orchestrator.go:39` | Margin added to the clamped sandbox exec timeout when sizing the python_execute context so it outlasts the sandbox HTTP client. |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_SEARCH` | `int` | `16` | `llm/orchestrator.go:147` | Max web_search calls allowed per message in normal mode; exceeding it fails the call. |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_FETCH` | `int` | `12` | `llm/orchestrator.go:148` | Max web_fetch calls allowed per message in normal mode; exceeding it fails the call. |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_FETCH_IMAGE` | `int` | `16` | `llm/orchestrator.go:149` | Max fetch_image calls allowed per message in normal mode; exceeding it fails the call. |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `8` | `llm/orchestrator.go:150` | Max image_generate calls allowed per message in normal mode; exceeding it fails the call. |
| `AIVORY_LLM_PER_TURN_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `16` | `llm/orchestrator.go:151` | Max python_execute sandbox runs allowed per message in normal mode; exceeding it fails the call. |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_SEARCH` | `int` | `40` | `llm/orchestrator.go:157` | Max web_search calls allowed per message while Deep Research runs; exceeding it fails the call. |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_FETCH` | `int` | `25` | `llm/orchestrator.go:158` | Max web_fetch calls allowed per message while Deep Research runs; exceeding it fails the call. |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_FETCH_IMAGE` | `int` | `12` | `llm/orchestrator.go:159` | Max fetch_image calls allowed per message while Deep Research runs; exceeding it fails the call. |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `4` | `llm/orchestrator.go:160` | Max image_generate calls allowed per message while Deep Research runs; exceeding it fails the call. |
| `AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `8` | `llm/orchestrator.go:161` | Max python_execute sandbox runs allowed per message while Deep Research runs; exceeding it fails the call. |
| `AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN` | `int` | `48` | `llm/orchestrator.go:169` | Global ceiling on total tool calls across all tools per message in normal mode, on top of the per-tool caps. |
| `AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN_DEEP` | `int` | `150` | `llm/orchestrator.go:170` | Global ceiling on total tool calls across all tools per message while Deep Research runs. |
| `AIVORY_LLM_TOOL_TIMEOUTS` | `duration` | `10*time.Second` | `llm/orchestrator.go:2326` | Per-invocation timeout bounding a single web_search tool call. |
| `AIVORY_LLM_TOOL_TIMEOUTS_2` | `duration` | `15*time.Second` | `llm/orchestrator.go:2327` | Per-invocation timeout bounding a single web_fetch tool call. |
| `AIVORY_LLM_TOOL_TIMEOUTS_3` | `duration` | `600*time.Second` | `llm/orchestrator.go:2329` | Per-invocation timeout bounding a single image_generate tool call (wide for slow third-party image gateways). |
| `AIVORY_LLM_TOOL_TIMEOUT_DEFAULT` | `duration` | `100*time.Second` | `llm/orchestrator.go:2332` | Fallback per-invocation timeout for any tool not listed in the per-type toolTimeouts map. |
| `AIVORY_LLM_PROMPT_MAX_ITER` | `int` | `10` | `llm/prompt_tools.go:36` | Max iterations of the prompt-mode tool loop (each = one model generation plus optional tool call) before the turn ends. |
| `AIVORY_LLM_PROMPT_MAX_RETRY` | `int` | `2` | `llm/prompt_tools.go:37` | Max retries in prompt-mode for a malformed <tool_call> JSON re-emission or a failing tool run. |
| `AIVORY_LLM_PROVIDER_REQUEST_BODY_MAX_BYTES` | `int` | `128*1024` | `llm/provider_request_capture.go:20` | Max length the captured provider-request body/headers JSON snapshot is clamped to for diagnostics. |
| `AIVORY_LLM_PROVIDER_REQUEST_VALUE_MAX_BYTES` | `int` | `8*1024` | `llm/provider_request_capture.go:21` | Max length each individual captured provider-request value (URL, header value) is clamped to. |
| `AIVORY_LLM_IMAGE_DOCUMENT_FLAT_TOKEN_ALLOWANCE` | `int` | `1024` | `llm/quota.go:270` | Flat token count added per image/document block when estimating request tokens, since base64 isn't text-tokenised. |
| `AIVORY_LLM_OUTPUT_RESERVE` | `int` | `2000` | `llm/quota.go:313` | Fixed output-token reserve added to estimated input tokens when computing the pre-flight credit affordability estimate. |
| `AIVORY_LLM_MAX_CONCURRENT_TOOLS` | `int` | `4` | `llm/tool_exec.go:28` | Max tools executed concurrently within a single turn (semaphore bounding the fan-out runner). |
| `AIVORY_LLM_VCTX` | `duration` | `45*time.Second` | `llm/verify.go:77` | Timeout bounding the post-answer verify/audit LLM call so a slow cross-provider auditor can't stall the turn. |





### 2. RAG 文档解析 / 向量检索

文档分块、Embedding 批量与并发、Qdrant 客户端、MinerU OCR 轮询等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_RAG_EMBEDDING_RETRY_DELAY` | `duration` | `time.Second` | `rag/embedder_http.go:50` | Base delay scaled by 2^min(attempt,4) for exponential backoff between embedding-batch retry attempts. |
| `AIVORY_RAG_EMBEDDING_RETRY_DELAY_2` | `duration` | `30*time.Second` | `rag/embedder_http.go:51` | Upper cap on the computed embedding-retry backoff delay. |
| `AIVORY_RAG_EMBEDDING_RETRY_DELAY_3` | `duration` | `1000*time.Millisecond` | `rag/embedder_http.go:52` | Range of random jitter added to each embedding-retry backoff delay to desynchronise concurrent batches. |
| `AIVORY_RAG_DASH_SCOPE_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:179` | Max concurrent upstream embedding batches per document when the provider is DashScope. |
| `AIVORY_RAG_DASH_SCOPE_GLOBAL_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:180` | Process-wide cap on concurrent DashScope embedding requests (size of the shared slot semaphore). |
| `AIVORY_RAG_DASH_SCOPE_EMBED_ATTEMPT_TIMEOUT` | `duration` | `60*time.Second` | `rag/embedder_http.go:188` | Per-attempt request timeout for DashScope embedding calls, set below the client's 3-minute cap so stuck calls fail visibly. |
| `AIVORY_RAG_EMBED_CONCURRENCY` | `int` | `4` | `rag/embedder_http.go:194` | Max concurrent upstream embedding batches per document for non-DashScope providers. |
| `AIVORY_RAG_MAX_ATTEMPTS` | `int` | `2` | `rag/embedder_http.go:445` | Max attempts for a single embedding-batch POST before giving up (retried with backoff on transient failures). |
| `AIVORY_RAG_PDF_INSPECTION_TIMEOUT` | `duration` | `8*time.Second` | `rag/parser.go:56` | Deadline the parent enforces on the child-process PDF text-layer inspection probe, killing it on expiry. |
| `AIVORY_RAG_OFFICE_XML_ZIP_ENTRY_READ_CAP` | `int64` | `16*1024*1024` | `rag/parser.go:253` | Byte ceiling on each DOCX/PPTX zip entry (document/header/footer/slide XML) read during Office plain-text extraction. |
| `AIVORY_RAG_PDF_INSPECTION_SAMPLE_LIMIT` | `int` | `3` | `rag/parser.go:333` | Maximum number of evenly-spaced PDF pages sampled when probing whether a PDF is a scan or born-digital text. |
| `AIVORY_RAG_PDF_THIN_CHARS_PER_PAGE` | `int` | `200` | `rag/parser.go:334` | Per-sampled-page character floor below which an image-bearing PDF is flagged 'thin' and routed to OCR. |
| `AIVORY_RAG_CMD_WAIT_DELAY` | `duration` | `500*time.Millisecond` | `rag/parser.go:375` | Grace period (cmd.WaitDelay) before the PDF-inspection child process is force-killed after its context deadline fires. |
| `AIVORY_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_IMAGE` | `int` | `5` | `rag/parser.go:438` | Image-count multiplier in the raw-bytes 'strong scan' test imageCount*N >= pages*M that flags a PDF as image-only. |
| `AIVORY_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_PAGE` | `int` | `4` | `rag/parser.go:439` | Page-count multiplier in the raw-bytes 'strong scan' test imageCount*N >= pages*M that flags a PDF as image-only. |
| `AIVORY_RAG_MINERU_SOURCE_OBJECT_CLEANUP_TIMEOUT` | `duration` | `30*time.Second` | `rag/parser.go:603` | Deadline for the best-effort delete of the bucket object uploaded as the MinerU OCR source, run after the parse. |
| `AIVORY_RAG_MINERU_POLL_DEADLINE` | `duration` | `20*time.Minute` | `rag/parser.go:606` | Overall ceiling on the MinerU extract-task poll loop that waits for state done/failed. |
| `AIVORY_RAG_MINERUZIPCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:838` | HTTP client timeout for the SSRF-guarded download of the MinerU result zip. |
| `AIVORY_RAG_FULL_MD_READ_CAP_INSIDE_ZIP` | `int64` | `32*1024*1024` | `rag/parser.go:841` | Byte ceiling on reading the full.md markdown body extracted from inside the MinerU result zip. |
| `AIVORY_RAG_MAX_ZIP` | `int64` | `500*1024*1024` | `rag/parser.go:862` | Byte ceiling on the downloaded MinerU result zip; a larger download is rejected instead of buffered into memory. |
| `AIVORY_RAG_MINERUCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:1002` | http.Client timeout for the MinerU cloud API submit, poll, and download round-trips. |
| `AIVORY_RAG_MINERU_SOURCE_TTLSECONDS` | `int` | `60*60` | `rag/parser.go:1008` | Lifetime in seconds of the presigned GET URL for the source document handed to MinerU. |
| `AIVORY_RAG_RAG_FAST_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:42` | Asynq worker concurrency for the 'rag-fast' ingest lane serving text/spreadsheet documents. |
| `AIVORY_RAG_RAG_SLOW_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:43` | Asynq worker concurrency for the 'rag' slow ingest lane serving OCR/heavy documents. |
| `AIVORY_RAG_INGEST_PIPELINE_TIMEOUT` | `duration` | `70*time.Minute` | `rag/rag.go:44` | Context deadline the full parse/embed ingest pipeline (runIngestWithRetries) runs under per document. |
| `AIVORY_RAG_INGEST_TASK_TIMEOUT` | `duration` | `75*time.Minute` | `rag/rag.go:45` | Asynq per-task processing timeout (asynq.Timeout) applied to a rag.ingest task. |
| `AIVORY_RAG_INGEST_UNIQUE_TTL` | `duration` | `80*time.Minute` | `rag/rag.go:46` | Asynq uniqueness-lock TTL (asynq.Unique) that suppresses duplicate enqueues of the same document. |
| `AIVORY_RAG_INGEST_HEARTBEAT_INTERVAL` | `duration` | `30*time.Second` | `rag/rag.go:47` | Interval between ingest heartbeat writes (TouchDocumentIngest) that keep a running document from looking stale. |
| `AIVORY_RAG_INGEST_STALE_AFTER` | `duration` | `4*time.Minute` | `rag/rag.go:48` | Heartbeat age after which a parsing/embedding document is deemed abandoned and reclaimed for requeue. |
| `AIVORY_RAG_INGEST_PENDING_STALE_AFTER` | `duration` | `ingestUniqueTTL` | `rag/rag.go:49` | Age after which a still-'pending' document is deemed stuck and reclaimed for requeue. |
| `AIVORY_RAG_INGEST_RECOVERY_INTERVAL` | `duration` | `time.Minute` | `rag/rag.go:50` | Interval between sweeps of the recovery loop that reclaims stale abandoned ingest documents. |
| `AIVORY_RAG_INGEST_FINALIZE_TIMEOUT` | `duration` | `30*time.Second` | `rag/rag.go:51` | Deadline for the vector-store DeleteByDocument cleanup when finalizing a failed ingest. |
| `AIVORY_RAG_INGEST_ASYNQ_LEASE_MAX_RETRIES` | `int` | `1` | `rag/rag.go:52` | Asynq MaxRetry count reserved for lease/process loss; handler failures are retried separately in-pipeline. |
| `AIVORY_RAG_INGEST_ASYNQ_RETRY_DELAY` | `duration` | `2*time.Minute` | `rag/rag.go:53` | Delay asynq waits (RetryDelayFunc) before re-running an ingest task whose lease was lost. |
| `AIVORY_RAG_INGEST_QUEUE_NAME` | `duration` | `2*time.Second` | `rag/rag.go:59` | Deadline for the GetDocument lookup that classifies a document into the fast or slow ingest queue. |
| `AIVORY_RAG_RUN_INGEST_WITH_RETRIES` | `int` | `3` | `rag/rag.go:60` | Maximum whole-pipeline ingest attempts before the document is finalized as failed. |
| `AIVORY_RAG_RUN_INGEST_WITH_RETRIES_2` | `duration` | `3*time.Second` | `rag/rag.go:61` | Base backoff between whole-pipeline ingest retries, multiplied by the attempt number. |
| `AIVORY_RAG_START_INGEST_HEARTBEAT` | `duration` | `5*time.Second` | `rag/rag.go:62` | Deadline for each individual ingest heartbeat write (TouchDocumentIngest). |
| `AIVORY_RAG_FINALIZE_CHUNK_CLEANUP_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:63` | Deadline for the DeleteChunksByDocument cleanup when finalizing a failed ingest. |
| `AIVORY_RAG_FINALIZE_STATUS_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:64` | Deadline for the terminal UpdateDocumentStatus write that marks a failed document 'failed'. |
| `AIVORY_RAG_DENSE_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:68` | Top-N hit count requested from the dense/vector leg (vec.Search) of hybrid retrieval. |
| `AIVORY_RAG_KEYWORD_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:69` | Top-N hit count requested from the keyword leg (vec.SearchKeyword) of hybrid retrieval. |
| `AIVORY_RAG_SNIPPET_OF` | `int` | `240` | `rag/rag.go:70` | Default byte-length cap (rune-safe cut) for a result snippet when the caller supplies no explicit max. |
| `AIVORY_RAG_SPLIT_PARAGRAPHS_AND_TABLES` | `int` | `800` | `rag/rag.go:71` | Byte-length threshold below which an image-markdown paragraph is kept as one atomic chunk instead of being sub-split. |
| `AIVORY_RAG_ROUTER_CALL_TIMEOUT` | `duration` | `12*time.Second` | `rag/rag.go:72` | Deadline for the task-router JSON LLM call (task.router) on the first-token hot path before falling back to plain retrieval. |
| `AIVORY_RAG_MAP_REDUCE_SUMMARISE` | `int` | `200` | `rag/rag.go:73` | Chinese-character cap (<=N zi) requested in the map-reduce per-group summarization prompt sent to the task model. |
| `AIVORY_RAG_COLLECT_DOC_HINTS` | `int` | `120` | `rag/rag.go:74` | Leading bytes of each document's content kept in the router's doc-hint line for reference resolution. |
| `AIVORY_RAG_COLLECT_DOC_HINTS_2` | `int` | `12` | `rag/rag.go:75` | Maximum number of per-document hint lines collected for the retrieval router. |
| `AIVORY_RAG_FUSE_RECIPROCAL_RANK` | `int` | `60` | `rag/rag.go:1427` | Rank constant k in the 1/(rank+k) reciprocal-rank fusion blending the vector and keyword retrieval legs. |
| `AIVORY_RAG_RETRIEVED_SNIPPET_CHARS` | `int` | `2000` | `rag/rag.go:1520` | Byte budget for the context window expandHit builds around each retrieved chunk before injection. |
| `AIVORY_RAG_CHILD_TARGET_CHARS` | `int` | `2000` | `rag/rag.go:1757` | Target byte length each embedded child chunk is grown toward when merging split atoms. |
| `AIVORY_RAG_PARENT_TARGET_CHARS` | `int` | `4800` | `rag/rag.go:1758` | Byte length at which a parent section body is truncated for retrieval-time context. |
| `AIVORY_RAG_CHUNK_OVERLAP_CHARS` | `int` | `250` | `rag/rag.go:1761` | Trailing bytes of the previous child chunk prepended to the next as sliding-window overlap. |
| `AIVORY_RAG_MAPREDUCE_GROUPTOKENS` | `int` | `6000` | `rag/rag.go:2334` | Estimated-token budget per chunk group in map-reduce summarisation of an over-budget corpus. |
| `AIVORY_RAG_MAPREDUCE_MAXGROUPS` | `int` | `8` | `rag/rag.go:2335` | Maximum number of chunk groups the map-reduce summariser processes for one query. |
| `AIVORY_RAG_BATCH_SIZE` | `int` | `64` | `rag/vector_admin.go:136` | Number of chunks embedded per Embed call when the admin rebuilds vectors. |
| `AIVORY_VECTOR_QDRANT_SCROLL_PAGE_SIZE_EXISTINGCHUNKIDS` | `int` | `256` | `vector/qdrant.go:32` | Points fetched per Qdrant scroll page when listing chunk ids already present in the index. |
| `AIVORY_VECTOR_QDRANT_SCROLL_PAGE_SIZE_VECTORCHUNKSTATUSES` | `int` | `256` | `vector/qdrant.go:33` | Points fetched per Qdrant scroll page when scanning payloads+vectors for the admin vector-status audit. |
| `AIVORY_VECTOR_DELETE_CONCURRENCY` | `int` | `4` | `vector/qdrant.go:460` | Maximum concurrent per-collection delete requests when sweeping a document's points across all dimensions. |





### 3. 沙盒代码执行

`python_execute` 沙盒。Go 侧变量需重启 API 进程生效；`SANDBOX_*` 变量作用于 `sandbox-service` 进程，需重启该服务生效。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `SANDBOX_MAX_OUTPUT_BYTES` | `int` | `32768` | `sandbox-service/app.py:120` | Byte limit at which the sandbox truncates captured exec stdout/stderr streams. |
| `SANDBOX_MAX_ARTIFACT_BYTES` | `int` | `20971520` | `sandbox-service/app.py:121` | Maximum byte size of a single produced artifact file the sandbox will collect. |
| `SANDBOX_S3_MAX_ATTEMPTS` | `int` | `3` | `sandbox-service/app.py:137` | Botocore max_attempts retry count bounding every S3 storage SDK call. |
| `SANDBOX_S3_CONNECT_TIMEOUT_S` | `float` | `10` | `sandbox-service/app.py:138` | Connect-timeout in seconds applied to every S3 storage SDK call. |
| `SANDBOX_S3_READ_TIMEOUT_S` | `float` | `120` | `sandbox-service/app.py:139` | Read-timeout in seconds applied to every S3 storage SDK call. |
| `SANDBOX_OSS_CONNECT_TIMEOUT_S` | `float` | `30` | `sandbox-service/app.py:140` | Connect-timeout in seconds for the Aliyun OSS bucket client. |
| `AIVORY_SANDBOX_MAX_SANDBOX_RESP_BYTES` | `int64` | `256<<20` | `sandbox/sandbox.go:162` | Byte cap on the sidecar HTTP response body decoded per exec call, guarding the API process against OOM. |
| `AIVORY_SANDBOX_EXEC_CLIENT_OVERHEAD` | `duration` | `120*time.Second` | `sandbox/sandbox.go:171` | Duration added to the per-exec cap to size the sandbox HTTP client timeout, covering the sidecar's post-deadline work. |
| `AIVORY_SANDBOX_SANDBOX_ERROR_BODY_READ_CAP` | `int64` | `64<<10` | `sandbox/sandbox.go:174` | Byte cap on how much of a 4xx/5xx sidecar error response body is read for the error message. |





### 4. 内置工具（搜索 / Python / 网络安全）

web_search 结果条数与超时、Python 安全模式、SSRF / 网络安全护栏等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_TOOLS_IN_TOP_K` | `int` | `5` | `tools/builtins.go:37` | Default result count for the web_search tool when the model's call omits top_k. |
| `AIVORY_TOOLS_WEB_FETCH_RESPONSE_BODY_READ_CAP` | `int64` | `256*1024` | `tools/builtins.go:38` | Byte cap on the HTTP response body web_fetch reads before stripping HTML. |
| `AIVORY_TOOLS_WEB_FETCH_EXTRACTED_TEXT_CHAR_CAP` | `int` | `32000` | `tools/builtins.go:39` | Character cap on web_fetch's HTML-stripped text before it is truncated with an ellipsis marker. |
| `AIVORY_TOOLS_PYTHON_EXECUTE_UPLOAD_STAGING_FILE_SIZE` | `int64` | `20*1024*1024` | `tools/builtins.go:40` | Per-file size ceiling above which a conversation upload is skipped when staging files into the python sandbox. |
| `AIVORY_TOOLS_PYTHON_EXECUTE_IMAGE_ARTIFACT_STAGING_SIZE` | `int64` | `20*1024*1024` | `tools/builtins.go:41` | Per-file size ceiling above which a generated image artifact is skipped when staging it into the python sandbox. |
| `AIVORY_TOOLS_PYTHON_EXECUTE_STDOUT_STDERR_TRUNCATION_CAP` | `int` | `32*1024` | `tools/builtins.go:42` | Character cap for truncating python_execute stdout/stderr before it is surfaced to the model. |
| `AIVORY_TOOLS_IN_N` | `int` | `4` | `tools/builtins.go:43` | Maximum number of images a single image_generate call may request. |
| `AIVORY_TOOLS_IN_SIZE` | `string` | `"1024x1024"` | `tools/builtins.go:44` | Default WxH image dimensions used when an image_generate call omits size. |
| `AIVORY_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW` | `duration` | `24*time.Hour` | `tools/builtins.go:45` | Window Now() is truncated to for the day-start boundary of the per-user image-generation quota ledger. |
| `AIVORY_TOOLS_IMAGE_IMAGE_INPUT_IMAGE_CAP` | `int` | `3` | `tools/builtins.go:47` | Maximum number of reference input images loaded for an image-to-image generation call. |
| `AIVORY_TOOLS_FETCHREMOTEIMAGE_DOWNLOAD_CAP` | `int64` | `32<<20` | `tools/builtins.go:48` | Byte cap on downloading an image URL returned in an image-API response via the SSRF-safe client. |
| `AIVORY_TOOLS_IN_TOP_K_2` | `int` | `5` | `tools/builtins.go:49` | Default snippet count for the search_knowledge_base tool when the call omits top_k. |
| `AIVORY_TOOLS_CONFIDENCE` | `float` | `0.95` | `tools/builtins.go:50` | Confidence score stored on each memory record the save_memory tool creates. |
| `AIVORY_TOOLS_MAX_IMG` | `int64` | `15*1024*1024` | `tools/builtins.go:240` | Byte cap on an image the fetch_image tool downloads; a larger response is rejected. |





### 5. 会话 / 消息 / 流式 API

SSE 心跳、流恢复窗口、生成时长上限、分页与搜索上限、消息路径缓存等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_LIMIT_2` | `int` | `200` | `api/conversations_handlers.go:26` | Default page size for the list-conversations endpoint when the request omits a limit param. |
| `AIVORY_API_LIMIT_3` | `int` | `500` | `api/conversations_handlers.go:32` | Hard upper bound clamped onto the ?limit query param of the list-conversations endpoint. |
| `AIVORY_API_SEARCH_MESSAGE_HIT_LIMIT` | `int` | `40` | `api/conversations_handlers.go:99` | Max number of message-content matches returned by the conversation search endpoint (title matches capped separately). |
| `AIVORY_API_IMPORT_MAX_CONVERSATIONS` | `int` | `1000` | `api/conversations_handlers.go:164` | Max conversations a single import request may create; exceeding it rejects the import. |
| `AIVORY_API_IMPORT_MAX_MESSAGES_PER_CONV` | `int` | `10000` | `api/conversations_handlers.go:165` | Max messages allowed in one conversation of an import request; exceeding it rejects the import. |
| `AIVORY_API_IMPORT_MAX_CONTENT_BYTES` | `int` | `200*1024` | `api/conversations_handlers.go:166` | Per-message content byte cap during import; longer message bodies are truncated to this many bytes. |
| `AIVORY_API_INLINE_THREAD_QUOTE_CAP` | `int` | `4000` | `api/conversations_handlers.go:282` | Max rune length of the quoted text when creating an inline thread; longer selections are truncated. |
| `AIVORY_API_GETCONVERSATION_ACTIVE_PATH_LIMIT` | `int` | `200` | `api/conversations_handlers.go:345` | Upper bound on the ?limit param for get-conversation active-path pagination; larger values return the full path. |
| `AIVORY_API_LIMIT_4` | `int` | `30` | `api/conversations_handlers.go:510` | Default trailing-window page size for the list-messages endpoint when no ?limit query param is supplied. |
| `AIVORY_API_LISTMESSAGES_PAGE_LIMIT` | `int` | `200` | `api/conversations_handlers.go:512` | Max accepted value for the ?limit param on the list-messages endpoint; larger values keep the default page size. |
| `AIVORY_API_RATE_LIMIT_USER` | `int` | `20` | `api/kbs_handlers.go:13` | Per-user rate-limit budget: max knowledge-base document uploads allowed per minute before 429. |
| `AIVORY_API_CONFIDENCE` | `float` | `0.95` | `api/memories_handlers.go:13` | Confidence score stored on a user-created memory record (0-1 scale). |
| `AIVORY_API_MAX_GEN_DURATION` | `duration` | `90*time.Minute` | `api/messages_handlers.go:27` | Wall-clock timeout capping a single detached generation turn before its context is cancelled. |
| `AIVORY_API_SSE_PING_HEARTBEAT_POST` | `duration` | `15*time.Second` | `api/messages_handlers.go:32` | Interval between SSE keep-alive ping ticks on the post-message streaming endpoint. |
| `AIVORY_API_SSE_PING_HEARTBEAT_REGENERATE` | `duration` | `15*time.Second` | `api/messages_handlers.go:33` | Interval between SSE keep-alive ping ticks on the regenerate-message streaming endpoint. |
| `AIVORY_API_SSE_PING_HEARTBEAT_STREAM` | `duration` | `15*time.Second` | `api/messages_handlers.go:34` | Interval between SSE keep-alive ping ticks on the stream-attach (reconnect) endpoint. |
| `AIVORY_API_STREAM_STATUS_RECHECK_INTERVAL` | `duration` | `5*time.Second` | `api/messages_handlers.go:35` | How often the stream-attach handler re-polls the message's generation status to detect a terminal state. |
| `AIVORY_API_STREAM_REPLAY_BATCH_SIZE` | `int` | `200` | `api/messages_handlers.go:36` | Number of buffered stream events read per batch when replaying/catching up a reconnecting SSE stream. |
| `AIVORY_API_ONLINE_PRESENCE_TOUCH_THROTTLE` | `duration` | `time.Minute` | `api/middleware.go:23` | Throttle interval between online-presence 'seen' touches for a user; the cache TTL of the seen marker. |
| `AIVORY_API_CONCURRENT_GEN_SLOT_SAFETY_TTL` | `duration` | `30*time.Minute` | `api/middleware.go:24` | Safety TTL on the per-user concurrent-generation slot counter so a dead slot self-expires. |
| `AIVORY_API_REQUEST_SIGNATURE_REPLAY_WINDOW_FUTURE` | `int64` | `300` | `api/middleware.go:25` | Signed-request replay guard: max seconds X-Req-Ts may lag behind server time before the signature is rejected as expired. |
| `AIVORY_API_REQUEST_SIGNATURE_REPLAY_WINDOW_PAST` | `int64` | `60` | `api/middleware.go:26` | Signed-request replay guard: max seconds X-Req-Ts may run ahead of server time (clock-skew tolerance) before rejection. |
| `AIVORY_API_CREDIT_MULTIPLIER` | `float` | `5.0` | `api/models_handlers.go:21` | Divisor turning a model's combined input+output price into the relative credit multiplier shown in the picker. |
| `AIVORY_API_JSON_REQUEST_BODY_SIZE_CAP` | `int64` | `4<<20` | `api/mux.go:16` | Max byte size accepted for a JSON request body; larger bodies are rejected by MaxBytesReader. |
| `AIVORY_API_PROJECT_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `200` | `api/projects_handlers.go:15` | Number of active conversations loaded into the project-detail response (fetch limit, offset 0). |
| `AIVORY_API_RATE_LIMIT_USER_2` | `int` | `20` | `api/projects_handlers.go:16` | Per-user rate-limit budget: max project document uploads allowed per minute before 429. |
| `AIVORY_API_RATE_LIMIT_REGISTER_MAX` | `int` | `5` | `api/router.go:51` | Per-IP rate-limit budget for POST /api/auth/register: max register attempts allowed per window before 429. |
| `AIVORY_API_RATE_LIMIT_REGISTER_WINDOW` | `duration` | `60*time.Second` | `api/router.go:52` | Rolling window duration for the per-IP POST /api/auth/register rate-limit budget. |
| `AIVORY_API_RATE_LIMIT_LOGIN_MAX` | `int` | `10` | `api/router.go:54` | Per-IP rate-limit budget for POST /api/auth/login: max login attempts allowed per window before 429. |
| `AIVORY_API_RATE_LIMIT_LOGIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:55` | Rolling window duration for the per-IP POST /api/auth/login rate-limit budget. |
| `AIVORY_API_RATE_LIMIT_LOGIN_2FA_MAX` | `int` | `10` | `api/router.go:57` | Per-IP rate-limit budget for POST /api/auth/login/2fa: max 2FA-verify attempts allowed per window before 429. |
| `AIVORY_API_RATE_LIMIT_LOGIN_2FA_WINDOW` | `duration` | `60*time.Second` | `api/router.go:58` | Rolling window duration for the per-IP POST /api/auth/login/2fa rate-limit budget. |
| `AIVORY_API_RATE_LIMIT_LOGOUT_MAX` | `int` | `30` | `api/router.go:60` | Per-IP rate-limit budget for POST /api/auth/logout: max logout requests allowed per window before 429. |
| `AIVORY_API_RATE_LIMIT_LOGOUT_WINDOW` | `duration` | `60*time.Second` | `api/router.go:61` | Rolling window duration for the per-IP POST /api/auth/logout rate-limit budget. |
| `AIVORY_API_RATE_LIMIT_REFRESH_MAX` | `int` | `30` | `api/router.go:63` | Per-IP rate-limit budget for POST /api/auth/refresh: max token-refresh requests allowed per window before 429. |
| `AIVORY_API_RATE_LIMIT_REFRESH_WINDOW` | `duration` | `60*time.Second` | `api/router.go:64` | Rolling window duration for the per-IP POST /api/auth/refresh rate-limit budget. |
| `AIVORY_API_RATE_LIMIT_VERIFY_EMAIL_MAX` | `int` | `10` | `api/router.go:66` | Per-IP rate-limit budget for POST /api/auth/verify-email: max verification attempts allowed per window before 429. |
| `AIVORY_API_RATE_LIMIT_VERIFY_EMAIL_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:67` | Rolling window duration for the per-IP POST /api/auth/verify-email rate-limit budget. |
| `AIVORY_API_RATE_LIMIT_SEND_CODE_MAX` | `int` | `3` | `api/router.go:69` | Per-IP request ceiling within the window for POST /api/auth/send-code, which emails a login/verification code. |
| `AIVORY_API_RATE_LIMIT_SEND_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:70` | Fixed window over which per-IP requests to the /api/auth/send-code code sender are counted. |
| `AIVORY_API_RATE_LIMIT_FORGOT_PASSWORD_MAX` | `int` | `5` | `api/router.go:72` | Per-IP request ceiling within the window for POST /api/auth/forgot-password, which triggers a reset email. |
| `AIVORY_API_RATE_LIMIT_FORGOT_PASSWORD_WINDOW` | `duration` | `15*60*time.Second` | `api/router.go:73` | Fixed window over which per-IP requests to /api/auth/forgot-password are counted. |
| `AIVORY_API_RATE_LIMIT_RESET_PASSWORD_MAX` | `int` | `5` | `api/router.go:75` | Per-IP request ceiling within the window for POST /api/auth/reset-password, which redeems a reset code. |
| `AIVORY_API_RATE_LIMIT_RESET_PASSWORD_WINDOW` | `duration` | `60*time.Second` | `api/router.go:76` | Fixed window over which per-IP requests to /api/auth/reset-password are counted. |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_ISSUE_MAX` | `int` | `30` | `api/router.go:78` | Per-IP request ceiling within the window for GET /api/public/captcha, which issues a slider-puzzle challenge. |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_ISSUE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:79` | Fixed window over which per-IP requests to the GET /api/public/captcha challenge issuer are counted. |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_VERIFY_MAX` | `int` | `60` | `api/router.go:81` | Per-IP request ceiling within the window for POST /api/public/captcha/verify, which checks a captcha solution. |
| `AIVORY_API_RATE_LIMIT_CAPTCHA_VERIFY_WINDOW` | `duration` | `60*time.Second` | `api/router.go:82` | Fixed window over which per-IP requests to the /api/public/captcha/verify solution checker are counted. |
| `AIVORY_API_RATE_LIMIT_FIRST_RUN_SETUP_MAX` | `int` | `10` | `api/router.go:84` | Per-IP request ceiling within the window for POST /api/setup, the first-run create-first-admin endpoint. |
| `AIVORY_API_RATE_LIMIT_FIRST_RUN_SETUP_WINDOW` | `duration` | `60*time.Second` | `api/router.go:85` | Fixed window over which per-IP requests to the POST /api/setup first-run bootstrap are counted. |
| `AIVORY_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_MAX` | `int` | `60` | `api/router.go:87` | Per-IP request ceiling within the window for GET /api/public/shared/:token, the no-auth shared-conversation view. |
| `AIVORY_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_WINDOW` | `duration` | `60*time.Second` | `api/router.go:88` | Fixed window over which per-IP requests to the /api/public/shared/:token viewer are counted. |
| `AIVORY_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_MAX` | `int` | `240` | `api/router.go:90` | Per-IP request ceiling within the window for the shared-conversation file and artifact asset routes. |
| `AIVORY_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_WINDOW` | `duration` | `60*time.Second` | `api/router.go:91` | Fixed window over which per-IP requests to the shared-conversation file/artifact asset routes are counted. |
| `AIVORY_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_MAX` | `int` | `20` | `api/router.go:93` | Per-IP request ceiling within the window for the OAuth start, callback, and cross-domain handoff routes. |
| `AIVORY_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_WINDOW` | `duration` | `60*time.Second` | `api/router.go:94` | Fixed window over which per-IP requests to the OAuth start/callback/handoff routes are counted. |
| `AIVORY_API_RATE_LIMIT_PASSWORD_CHANGE_SET_MAX` | `int` | `5` | `api/router.go:96` | Per-IP request ceiling within the window for the /api/me/password change and /api/me/password/set endpoints. |
| `AIVORY_API_RATE_LIMIT_PASSWORD_CHANGE_SET_WINDOW` | `duration` | `60*time.Second` | `api/router.go:97` | Fixed window over which per-IP requests to the /api/me/password change/set endpoints are counted. |
| `AIVORY_API_RATE_LIMIT_IDENTITY_LINK_START_MAX` | `int` | `20` | `api/router.go:99` | Per-IP request ceiling within the window for POST /api/me/identities/:id/link, which starts OAuth account linking. |
| `AIVORY_API_RATE_LIMIT_IDENTITY_LINK_START_WINDOW` | `duration` | `60*time.Second` | `api/router.go:100` | Fixed window over which per-IP requests to the OAuth identity-link start endpoint are counted. |
| `AIVORY_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_MAX` | `int` | `10` | `api/router.go:102` | Per-IP request ceiling within the window for each of the /api/me/2fa setup, enable, and disable endpoints. |
| `AIVORY_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:103` | Fixed window over which per-IP requests to the 2FA setup/enable/disable endpoints are counted. |
| `AIVORY_API_RATE_LIMIT_REDEEM_CODE_MAX` | `int` | `10` | `api/router.go:105` | Per-IP request ceiling within the window for POST /api/me/redeem, the redemption-code redeem endpoint. |
| `AIVORY_API_RATE_LIMIT_REDEEM_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:106` | Fixed window over which per-IP requests to the /api/me/redeem redemption endpoint are counted. |
| `AIVORY_API_RATE_LIMIT_WORKSPACE_JOIN_MAX` | `int` | `30` | `api/router.go:108` | Per-IP request ceiling within the window for the /api/workspaces/join/:token invite-info and join endpoints. |
| `AIVORY_API_RATE_LIMIT_WORKSPACE_JOIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:109` | Fixed window over which per-IP requests to the /api/workspaces/join/:token endpoints are counted. |
| `AIVORY_API_SELF_USAGE_LOOKBACK_WINDOW` | `int` | `30` | `api/user_handlers.go:209` | Number of days back over which GET /api/me/usage sums the caller's own message count. |
| `AIVORY_API_LIMIT` | `int` | `200` | `api/workspaces_handlers.go:15` | Default max workspaces returned by the admin workspace-list endpoint when the request supplies no ?limit override. |
| `AIVORY_API_ADMIN_WORKSPACE_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `500` | `api/workspaces_handlers.go:16` | Number of conversations loaded for the admin workspace-detail triage view. |
| `AIVORY_QUEUE_IN_PROCESS_WORKERS` | `int` | `8` | `queue/queue.go:47` | Number of concurrent worker goroutines in the in-process background-job pool. |
| `AIVORY_QUEUE_PROCESS_JOB_BUFFER` | `int` | `256` | `queue/queue.go:48` | Buffered slot count of the in-process job channel; when full, Enqueue diverts new jobs to the backpressure fallback. |
| `AIVORY_QUEUE_QUEUE_BACKPRESSURE_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:49` | Context deadline for a job run via the backpressure fallback goroutine when the in-process job buffer is full. |
| `AIVORY_QUEUE_QUEUE_WORKER_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:50` | Context deadline applied to each job executed by an in-process queue worker. |





### 6. 认证 / 会话 / 验证码

令牌缓存、验证码有效期与尝试次数、TOTP 时窗、OAuth state TTL 等（不含约定俗成的格式常量，如验证码位数）。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_MAX_CODE_ATTEMPTS` | `int` | `5` | `api/auth_handlers.go:24` | Number of wrong guesses allowed against an emailed verify/reset code before that code is burned. |
| `AIVORY_API_CODE_FAILURE_COUNTER_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:28` | TTL of the per-email wrong-guess counter tracking failed verify/reset code attempts. |
| `AIVORY_API_MINIMUM_PASSWORD_LENGTH` | `int` | `8` | `api/auth_handlers.go:29` | Minimum character length required for a user-chosen account password at registration and password change. |
| `AIVORY_API_EMAIL_VERIFICATION_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:30` | Lifetime of the emailed account email-verification code. |
| `AIVORY_API_PASSWORD_RESET_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:31` | Lifetime of the emailed password-reset code. |
| `AIVORY_API_CAP_TOL` | `float` | `0.04` | `api/captcha.go:41` | Accepted error between the submitted slider fraction and the true gap fraction for a captcha to pass. |
| `AIVORY_API_CAPTCHA_CHALLENGE_CACHE_TTL` | `duration` | `5*time.Minute` | `api/captcha.go:44` | How long an unsolved slider-puzzle captcha challenge remains valid in cache. |
| `AIVORY_API_CAPTCHA_PASS_TTL` | `duration` | `10*time.Minute` | `api/captcha.go:110` | Lifetime of the signed pass token proving a captcha was recently solved. |
| `AIVORY_API_OAUTH_2FA_HANDOFF_COOKIE_TTL` | `duration` | `300*time.Second` | `api/oauth_handlers.go:24` | Max-Age of the short-lived HttpOnly cookie handing a 2FA login ticket to the SPA after OAuth sign-in. |
| `AIVORY_API_OAUTH_STATE_CACHE_TTL` | `duration` | `10*time.Minute` | `api/oauth_handlers.go:25` | Lifetime of the cached OAuth authorization-flow state entry used as the CSRF guard. |
| `AIVORY_API_OAUTH_TOKEN_EXCHANGE_CONTEXT_TIMEOUT` | `duration` | `20*time.Second` | `api/oauth_handlers.go:26` | Timeout bounding the OAuth callback's code-to-token exchange plus the userinfo fetch. |
| `AIVORY_API_OAUTH_CROSS_DOMAIN_HANDOFF_TOKEN_TTL` | `duration` | `60*time.Second` | `api/oauth_handlers.go:27` | Lifetime of the one-time cross-domain OAuth handoff token the SPA exchanges for a session. |
| `AIVORY_API_2FA_LOGIN_TICKET_BURN_THRESHOLD` | `int64` | `5` | `api/twofa_handlers.go:24` | Number of wrong TOTP-code attempts against a 2FA login ticket before that ticket is burned. |
| `AIVORY_API_ISSUE_TWOFA_TICKET` | `duration` | `5*time.Minute` | `api/twofa_handlers.go:25` | Lifetime of the 2FA login ticket issued after a correct password while the TOTP code is pending. |
| `AIVORY_OAUTH_APPLE_CLIENT_SECRET_JWT_EXPIRY` | `duration` | `30*time.Minute` | `oauth/oauth.go:71` | Lifetime of the generated Apple OAuth client-secret JWT. |





### 7. 上传 / 文件 / 分享

图片处理、存储清理周期、直传分片、分享令牌、下载缓存 TTL 等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_ADMIN_ICON_UPLOAD_SIZE` | `int64` | `256*1024` | `api/admin_uploads.go:59` | Maximum byte size accepted for an admin-uploaded model icon image. |
| `AIVORY_API_AUDIO_TRANSCRIPTION_UPSTREAM_HTTP_TIMEOUT` | `duration` | `120*time.Second` | `api/audio_handlers.go:21` | Timeout for the HTTP call forwarding an audio blob to the upstream transcription API. |
| `AIVORY_API_AUDIO_TRANSCRIPTION_USER_RATE_LIMIT` | `int` | `20` | `api/audio_handlers.go:26` | Maximum audio-transcription requests one user may make per minute. |
| `AIVORY_API_TRANSCRIPTION_UPSTREAM_RESPONSE_READ_CAP` | `int64` | `1<<20` | `api/audio_handlers.go:27` | Byte ceiling on how much of the upstream transcription response is read into memory. |
| `AIVORY_API_AUDIO_STREAM_USER_RATE_LIMIT` | `int` | `30` | `api/audio_stream_handler.go:30` | Maximum live voice-streaming (WebSocket) sessions one user may start per minute. |
| `AIVORY_API_AUDIO_STREAM_MAX_BYTES` | `int64` | `24*1024*1024` | `api/audio_stream_handler.go:33` | Byte ceiling on audio relayed through one live voice-streaming session before the connection is cut. |
| `AIVORY_API_AUDIO_STREAM_MAX_SESSION` | `duration` | `15*time.Minute` | `api/audio_stream_handler.go:34` | Hard wall-clock cap on one live voice-streaming session (mic → Volcano ASR relay). |
| `AIVORY_ASR_DEBUG` | `bool` | `false` | `api/audio_stream_handler.go:39` | Log every decoded Volcano ASR frame (message code, last-package flag, transcript length, raw JSON payload) to diagnose live-transcription issues. Off by default — raw payloads contain the user's speech, so enabling it is an explicit opt-in. |
| `AIVORY_API_UPLOAD_RATE_LIMIT_MAX` | `int` | `20` | `api/files_handlers.go:25` | Maximum file uploads one user may make within the upload rate-limit window. |
| `AIVORY_API_UPLOAD_RATE_LIMIT_WINDOW` | `duration` | `time.Minute` | `api/files_handlers.go:26` | Time window over which a user's file uploads are counted for rate limiting. |
| `AIVORY_API_OBJECT_STORAGE_DELETE_TIMEOUT_CLEANUP` | `duration` | `30*time.Second` | `api/storage_cleanup.go:17` | Per-object timeout for deleting an orphaned file from object storage during storage cleanup. |
| `AIVORY_STORAGE_S3_DIRECT_UPLOAD_MIN_CLIENT_TIMEOUT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:28` | Minimum HTTP-client timeout for direct S3/OSS uploads; a reused client below this is replaced with a fresh one. |
| `AIVORY_STORAGE_DIRECT_S3_OSS_UPLOAD_HTTP_CLIENT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:29` | Timeout of the HTTP client freshly built for direct S3/OSS uploads when the reused one is too short. |
| `AIVORY_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_CONNECT` | `int64` | `30` | `storage/s3_direct.go:30` | Connection timeout, in seconds, for the Aliyun OSS client used by direct uploads. |
| `AIVORY_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_RW` | `int64` | `300` | `storage/s3_direct.go:31` | Read/write timeout, in seconds, for the Aliyun OSS client used by direct uploads. |
| `AIVORY_STORAGE_PRESIGN_URL_TTL` | `duration` | `3600*time.Second` | `storage/s3_direct.go:32` | Expiry applied to a direct-upload presigned GET URL when the caller requests no explicit TTL. |
| `AIVORY_STORAGE_PRESIGN_URL_TTL_CLAMP_CEILING` | `duration` | `86400*time.Second` | `storage/s3_direct.go:33` | Upper bound clamping a caller-requested TTL for a direct-upload presigned GET URL. |
| `AIVORY_STORAGE_SIDECAR_STORAGE_CLIENT_HTTP_TIMEOUT` | `duration` | `5*time.Minute` | `storage/storage.go:31` | Timeout for the HTTP round-trip to the sandbox sidecar's object-storage put/delete endpoints. |





### 8. 管理后台任务（备份 / 向量维护 / 兑换码）

备份大小上限与异步轮询、向量维护批量、兑换码上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_API_BACKUP_EXPORT_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_backup_async.go:26` | Number of past backup-export job records kept in the in-memory history before the oldest are pruned. |
| `AIVORY_API_BACKUP_EXPORT_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_backup_async.go:27` | Maximum wall-clock runtime allowed for a background backup-export job before it is cancelled. |
| `AIVORY_API_CONFIG_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `16<<20` | `api/admin_backup_handlers.go:25` | In-memory buffer for parsing the multipart admin-config import upload; larger parts spill to temp files. |
| `AIVORY_API_BACKUP_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `32<<20` | `api/admin_backup_handlers.go:26` | In-memory buffer for parsing the multipart full-backup import upload; larger parts spill to temp files. |
| `AIVORY_API_MAX_CONFIG_SIZE` | `int64` | `512<<20` | `api/admin_backup_handlers.go:373` | Maximum request-body size accepted when importing an admin-configuration archive. |
| `AIVORY_API_QDRANT_ARCHIVE_REQUEST_TIMEOUT` | `duration` | `5*time.Minute` | `api/admin_backup_qdrant.go:26` | Per-request HTTP timeout for Qdrant calls while exporting or importing a backup archive. |
| `AIVORY_API_QDRANT_EXPORT_SCROLL_PAGE_SIZE` | `int` | `256` | `api/admin_backup_qdrant.go:28` | Points fetched per /points/scroll page when exporting a Qdrant collection to the archive. |
| `AIVORY_API_QDRANT_IMPORT_UPSERT_FLUSH_BATCH_SIZE` | `int` | `128` | `api/admin_backup_qdrant.go:29` | Points accumulated per batch before an upsert is flushed while importing a Qdrant collection. |
| `AIVORY_API_ADMIN_USER_LIST_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:20` | Default result count for the admin user-list endpoint when the request supplies no explicit limit. |
| `AIVORY_API_ADMIN_CREATED_USER_MIN_PASSWORD_LENGTH` | `int` | `8` | `api/admin_handlers.go:21` | Minimum password length enforced when an admin creates a user account. |
| `AIVORY_API_ADMIN_PASSWORD_RESET_MIN_LENGTH` | `int` | `8` | `api/admin_handlers.go:22` | Minimum character length a new password must meet in the admin set-user-password endpoint. |
| `AIVORY_API_ADMIN_USER_CONVERSATIONS_LISTING_CAP` | `int` | `500` | `api/admin_handlers.go:23` | Maximum conversations returned when an admin lists one user's conversations for support/abuse triage. |
| `AIVORY_API_USAGE_REPORT_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:24` | Fallback page size for the admin usage-records report when the requested page_size is missing or outside 1-200. |
| `AIVORY_API_ANALYTICS_WINDOW` | `int` | `30` | `api/admin_handlers.go:25` | Default look-back window (days) for the admin analytics dashboard when no days query param is supplied. |
| `AIVORY_API_ANALYTICS_WINDOW_2` | `int` | `365` | `api/admin_handlers.go:26` | Upper bound (days) accepted for the admin analytics window query param; out-of-range values are ignored. |
| `AIVORY_API_ANALYTICS_BREAKDOWN_TOP_N` | `int` | `8` | `api/admin_handlers.go:27` | Number of top keys (per-model and per-user) kept in the admin analytics breakdown and its time series. |
| `AIVORY_API_BULK_REDEEM_CODE_GENERATION_QUANTITY` | `int` | `1000` | `api/admin_redeem_handlers.go:18` | Maximum number of redeem codes a single bulk-generation request may mint. |
| `AIVORY_API_MAX_SKILL_ASSET_BYTES` | `int64` | `20*1024*1024` | `api/admin_skill_assets.go:24` | Maximum byte size for a single uploaded skill asset file (templates/scripts/small data). |
| `AIVORY_API_VECTOR_MAINTENANCE_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_vectors_handlers.go:18` | Number of most-recent vector-maintenance jobs kept in the in-memory job history; older ones are dropped. |
| `AIVORY_API_VECTOR_MAINTENANCE_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_vectors_handlers.go:19` | Context timeout for one vector-maintenance job run (index audit or rebuild) before it is cancelled. |





### 9. 服务器启动 / 配置加载

HTTP server 超时、优雅关闭、启动流程常量等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AIVORY_CMD_ARCHIVE_GC_BOOT_SETTLE_DELAY` | `duration` | `2*time.Minute` | `cmd/api/main.go:40` | Delay after server boot before the first archived-workspace GC sweep, so a cold start isn't swept immediately. |
| `AIVORY_CMD_RUN_PRUNE` | `duration` | `5*time.Minute` | `cmd/api/main.go:41` | Context timeout for one archived-workspace GC prune run against object storage. |
| `AIVORY_CMD_ARCHIVE_GC_SWEEP_INTERVAL` | `duration` | `6*time.Hour` | `cmd/api/main.go:42` | Interval between archived-workspace GC sweeps that delete stale /workspace tarballs from object storage. |





### 10. 前端

**编译期**生效（Vite 在 `npm run build` 时内联 `VITE_*`），需要在构建环境设置，运行时改环境变量无效。轮询间隔、分页、重试 / 退避、去抖、超时、客户端大小上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `VITE_AIVORY_IMAGE_API_MY_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:181` | Default limit (page size) for fetching the signed-in user's own generated-image gallery. |
| `VITE_AIVORY_WORKSPACES_API_ADMIN_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:289` | Default limit (page size) for the admin workspaces listing request. |
| `VITE_AIVORY_CONVERSATIONS_API_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:318` | Default limit (page size) for the conversations listing request. |
| `VITE_AIVORY_CONVERSATIONS_API_LIST_ARCHIVED_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:326` | Default limit (page size) for the archived-conversations listing request. |
| `VITE_AIVORY_ADMIN_API_USERS_LIMIT` | `envNum` | `50` | `src/api/endpoints.ts:594` | Default limit (page size) for the admin users listing request. |
| `VITE_AIVORY_ADMIN_API_USER_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:626` | Default limit (page size) for the admin drill-down into a user's generated-image gallery. |
| `VITE_AIVORY_ADMIN_API_ANALYTICS` | `envNum` | `30` | `src/api/endpoints.ts:674` | Default look-back window (days) sent with the admin analytics dashboard request. |
| `VITE_AIVORY_MAX_BYTES` | `envNum` | `256 * 1024` | `src/components/admin/icon-uploader.tsx:34` | Client-side maximum byte size for an admin icon upload; larger files are rejected before the request (mirrors backend cap). |
| `VITE_AIVORY_MAX_LEN` | `envNum` | `12_000` | `src/components/chat/composer.tsx:99` | Maximum character length of a chat composer message; longer text is blocked from sending. |
| `VITE_AIVORY_INGEST_POLL_MS` | `envNum` | `1200` | `src/components/chat/composer.tsx:113` | Polling interval (ms) for an attachment's ingest (parse/embed) status while the composer's send stays blocked. |
| `VITE_AIVORY_PAGE` | `envNum` | `30` | `src/components/chat/my-gallery.tsx:12` | Images fetched per page in the user's generated-image gallery infinite-scroll (initial load + loadMore). |
| `VITE_AIVORY_RUN_TIMEOUT_MS` | `envNum` | `120_000` | `src/lib/pyodide-runner.ts:39` | Hard wall-clock cap (ms) per in-browser Pyodide Python run; on timeout the worker is killed. |
| `VITE_AIVORY_MAX_STREAM_CHARS` | `envNum` | `200_000` | `src/lib/pyodide-runner.ts:41` | Maximum total streamed stdout/stderr characters from a Pyodide run before output is cut to stop runaway prints. |
| `VITE_AIVORY_MAX_RESULT_CHARS` | `envNum` | `20_000` | `src/lib/pyodide-runner.ts:43` | Maximum character length of the repr() of a Pyodide run's final expression result before truncation. |
| `VITE_AIVORY_ADMIN_BACKUP_EXPORT_JOB_POLL_INTERVAL` | `envNum` | `2500` | `src/pages/admin/AdminBackup.tsx:50` | Polling interval (ms) for refreshing a running backup-export job's status on the admin backup page. |
| `VITE_AIVORY_PAGE_SIZE_2` | `envNum` | `20` | `src/pages/admin/AdminRedeemCodes.tsx:80` | Rows per page for the admin redeem-codes table (client-side pagination of already-fetched rows). |
| `VITE_AIVORY_PAGE_SIZE` | `envNum` | `50` | `src/pages/admin/AdminUsage.tsx:33` | Rows per page (server-side pageSize) requested for the admin usage-records table. |
| `VITE_AIVORY_IMAGES_PAGE` | `envNum` | `60` | `src/pages/admin/AdminUserLibrary.tsx:29` | Images fetched per page in the admin user-library gallery drill-down (initial load + loadMore). |
| `VITE_AIVORY_ONLINE_WINDOW_S` | `envNum` | `300` | `src/pages/admin/AdminUsers.tsx:42` | Recency window (seconds) within which a user's last activity marks them online in the admin users table. |
| `VITE_AIVORY_PAGE_SIZE_3` | `envNum` | `50` | `src/pages/admin/AdminUsers.tsx:83` | Rows per page (server-side limit) requested for the admin users list table. |
| `VITE_AIVORY_KB_DOC_STATUS_POLL_INTERVAL` | `envNum` | `2200` | `src/pages/kb/KnowledgeBaseDetail.tsx:41` | Polling interval (ms) for refreshing knowledge-base document status while any document is still parsing/embedding. |
| `VITE_AIVORY_CONV_PAGE` | `envNum` | `200` | `src/store/conversations.ts:52` | Conversations fetched per sidebar list page (initial load + infinite-scroll loadMore). |
| `VITE_AIVORY_MSG_PAGE` | `envNum` | `40` | `src/store/conversations.ts:66` | Messages fetched per page when opening a conversation (first page + scroll-up loadOlderMessages). |

