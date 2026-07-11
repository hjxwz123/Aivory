# Aurelia 高级环境变量参考

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
| `AURELIA_LLM_APPLY_ANTHROPIC_THINKING_SETTINGS` | `int` | `2048` | `llm/anthropic_provider.go:24` | Anthropic provider tunables (env-overridable; defaults preserve prior hardcoded behavior). |
| `AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_ANTHROPIC` | `int` | `240` | `llm/anthropic_provider.go:25` | Tool Result Summary Truncation Anthropic |
| `AURELIA_LLM_READ_ANTHROPIC_STREAM_INIT` | `int` | `64*1024` | `llm/anthropic_provider.go:26` | Read Anthropic Stream Init |
| `AURELIA_LLM_READ_ANTHROPIC_STREAM_MAX` | `int` | `1024*1024` | `llm/anthropic_provider.go:27` | Read Anthropic Stream Max |
| `AURELIA_LLM_MAX_ITER` | `int` | `20` | `llm/anthropic_provider.go:155` | Max Iter |
| `AURELIA_LLM_MAX_TOK` | `int` | `4096` | `llm/anthropic_provider.go:164` | Max Tok |
| `AURELIA_LLM_MAX_TOK_2` | `int` | `4096` | `llm/anthropic_provider.go:317` | Max Tok 2 |
| `AURELIA_LLM_INFLIGHT_GRACE` | `duration` | `15*time.Minute` | `llm/compaction.go:46` | How long an assistant row may sit status=streaming and still be protected from being summarized; older rows treated as crash leftovers Comment claims it sits above api.maxGenDuration '10-minute' cap, but maxGenDuration is actually 90m (messages_handlers.go:26) — grace is now BELOW the gen cap |
| `AURELIA_LLM_T` | `int` | `4` | `llm/compaction.go:55` | Flat token surcharge added per message (role markers/framing) in the compaction token estimate |
| `AURELIA_LLM_MESSAGE_TOKEN_MEMO_CACHE_BOUND` | `int` | `100000` | `llm/compaction.go:56` | Entry-count ceiling at which the per-message token-estimate memo map is reset in place |
| `AURELIA_LLM_SUMMARY_TOKENS_CLAMP_FLOOR` | `int` | `256` | `llm/compaction.go:57` | Lower clamp for admin summary_max_tokens; below this it reverts to the 2048 default Clamp applied to admin value |
| `AURELIA_LLM_BIG_TOKEN_OVERFLOW_NUM` | `int` | `5` | `llm/compaction.go:58` | When real ctx tokens exceed tokenTrigger*5/4, summarize inline this turn instead of async Only fires when the count is an exact provider count |
| `AURELIA_LLM_BIG_TOKEN_OVERFLOW_DEN` | `int` | `4` | `llm/compaction.go:59` | Verbatim-tail message count above which a cold-start backlog is summarized inline rather than async keepRounds*2 is messages-per-round; *3 is the inline gate |
| `AURELIA_LLM_INLINE_COMPACTION_BACKLOG_FACTOR` | `int` | `3` | `llm/compaction.go:60` | Output tokens for a per-message compaction call |
| `AURELIA_LLM_COMPACTION_SUMMARY_GENERATION_TOKENS` | `int` | `envcfg.Int("AURELIA_LLM_MAX_OUTPUT_TOKENS_4", 512` | `llm/compaction.go:61` | MaxOutputTokens for the TaskCompact summary-generation call |
| `AURELIA_LLM_DETERMINISTIC_SUMMARY_CLIP_BUDGET` | `int` | `300` | `llm/compaction.go:62` | Token budget for the CJK-aware fallback clip when the task model summary is empty; also the '<300 tokens' prompt instruction Same 300 in prompt at line 637 and default system prompt (task_llm.go:259) |
| `AURELIA_LLM_ATTEMPT` | `int` | `4` | `llm/compaction.go:63` | Max compare-and-swap attempts when appending a summary block under concurrent turns |
| `AURELIA_LLM_ITER` | `int` | `3` | `llm/compaction.go:64` | Rows per IN(...) batch when re-verifying summarized messages still exist (driver placeholder limit) |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_5` | `int` | `2` | `llm/compaction.go:65` | Max repeated folds per merge pass to bring path summary under budget |
| `AURELIA_LLM_CHUNK_SIZE` | `int` | `400` | `llm/compaction.go:788` | MaxOutputTokens for the coarser merged-summary call, half the summary token budget Derived from summaryMaxTokens (default 2048 -> ~1024); fallback clip also uses budget/2 (line 935) |
| `AURELIA_LLM_DR_MAX_ROUNDS` | `int` | `4` | `llm/deep_research.go:47` | hard cap on search→verify rounds |
| `AURELIA_LLM_DR_QUERIES_PER_ROUND` | `int` | `6` | `llm/deep_research.go:48` | max searches dispatched per round |
| `AURELIA_LLM_DR_FETCH_PER_ROUND` | `int` | `5` | `llm/deep_research.go:49` | max sources read per round |
| `AURELIA_LLM_DR_MIN_DEEP_READS` | `int` | `5` | `llm/deep_research.go:50` | skill Phase 3: deep-read at least this many sources |
| `AURELIA_LLM_DR_SEARCH_TOP_K` | `int` | `8` | `llm/deep_research.go:51` | results requested per search |
| `AURELIA_LLM_DR_WALL_CLOCK` | `duration` | `5*time.Minute` | `llm/deep_research.go:52` | backstop for the whole engine |
| `AURELIA_LLM_DR_CALL_TIMEOUT` | `duration` | `30*time.Second` | `llm/deep_research.go:53` | per search/fetch call |
| `AURELIA_LLM_DR_MAX_BODY_CHARS` | `int` | `4000` | `llm/deep_research.go:54` | per-source excerpt fed to the writer |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_8` | `int` | `1024` | `llm/deep_research.go:60` | Overridable inline tuning constants for the deep-research engine (env-backed; defaults preserve original behaviour). |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_9` | `int` | `512` | `llm/deep_research.go:61` | Max Output Tokens 9 |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_10` | `int` | `2048` | `llm/deep_research.go:62` | Max Output Tokens 10 |
| `AURELIA_LLM_DEEP_RESEARCH_VERIFY_EVIDENCE_EXCERPT_CAP` | `int` | `200` | `llm/deep_research.go:63` | Deep Research Verify Evidence Excerpt Cap |
| `AURELIA_LLM_DEEP_RESEARCH_VALIDATE_TIMEOUT` | `duration` | `75*time.Second` | `llm/deep_research.go:64` | Deep Research Validate Timeout |
| `AURELIA_LLM_DEEP_RESEARCH_VALIDATE_SOURCE_EXCERPT_CAP` | `int` | `2000` | `llm/deep_research.go:65` | Deep Research Validate Source Excerpt Cap |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_RESULT_SUMMARY_CAP` | `int` | `240` | `llm/deep_research.go:66` | Deep Research Tool Result Summary Cap |
| `AURELIA_LLM_SCORE_A` | `float` | `9` | `llm/deep_research.go:67` | Score A |
| `AURELIA_LLM_SCORE_B` | `float` | `6` | `llm/deep_research.go:68` | Score B |
| `AURELIA_LLM_SCORE_C` | `float` | `3` | `llm/deep_research.go:69` | Score C |
| `AURELIA_LLM_SCORE_KW` | `float` | `1` | `llm/deep_research.go:70` | Score Kw |
| `AURELIA_LLM_SCORE_FRESH_DOMAIN` | `float` | `2` | `llm/deep_research.go:71` | Score Fresh Domain |
| `AURELIA_LLM_MAX_ITER_4` | `int` | `20` | `llm/google_provider.go:68` | Max Iter 4 |
| `AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_GEMINI` | `int` | `240` | `llm/google_provider.go:167` | §6.2 tool_result MUST include the upstream tool_use id so the UI can pair the result with the in-flight tool_call card. For Gemini the id is the function name (multiple calls to the same fn rare). |
| `AURELIA_LLM_READ_GEMINI_STREAM_INIT` | `int` | `64*1024` | `llm/google_provider.go:327` | Read Gemini Stream Init |
| `AURELIA_LLM_READ_GEMINI_STREAM_MAX` | `int` | `1024*1024` | `llm/google_provider.go:327` | Read Gemini Stream Max |
| `AURELIA_LLM_PROVIDER_HTTP_CLIENT_TCP_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `llm/httpclient.go:41` | Provider HTTP Client Tcp Dial Timeout |
| `AURELIA_LLM_TRANSPORT_TLSHANDSHAKE_TIMEOUT` | `duration` | `10*time.Second` | `llm/httpclient.go:42` | Transport Tlshandshake Timeout |
| `AURELIA_LLM_TRANSPORT_EXPECT_CONTINUE_TIMEOUT` | `duration` | `1*time.Second` | `llm/httpclient.go:43` | Transport Expect Continue Timeout |
| `AURELIA_LLM_TRANSPORT_IDLE_CONN_TIMEOUT` | `duration` | `90*time.Second` | `llm/httpclient.go:44` | Transport Idle Conn Timeout |
| `AURELIA_LLM_TRANSPORT_MAX_IDLE_CONNS` | `int` | `50` | `llm/httpclient.go:45` | Transport Max Idle Conns |
| `AURELIA_LLM_MEMORY_WORKER_RECENT_MESSAGE_FETCH_LIMIT` | `int` | `30` | `llm/memory_worker.go:36` | SQL LIMIT on how many recent conversation messages the memory extractor pulls (DESC by created_at) Only user-role rows are then kept; paired with the turns>=20 cap below |
| `AURELIA_LLM_MEMORY_CANDIDATES_EXTRACTION_CAP` | `int` | `5` | `llm/memory_worker.go:37` | Prompt instruction telling the task model to return at most 5 memory candidate items per conversation Soft cap embedded in the extractor prompt text |
| `AURELIA_LLM_MEMORY_EXTRACTOR_USER_TURN_CAP` | `int` | `20` | `llm/memory_worker.go:38` | Max number of user turns appended into the memory-extraction prompt before breaking |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS` | `int` | `1024` | `llm/memory_worker.go:39` | MaxOutputTokens for the TaskMemoryExtract internal LLM call |
| `AURELIA_LLM_CONF` | `float` | `0.7` | `llm/memory_worker.go:40` | Confidence assigned to a candidate memory when the model's value is <=0 or >1 |
| `AURELIA_LLM_EXISTING_SAME_SLOT_MEMORIES_FETCH_LIMIT` | `int` | `10` | `llm/memory_worker.go:41` | SQL LIMIT on live memories loaded for a slot during write-time adjudication |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_2` | `int` | `256` | `llm/memory_worker.go:42` | MaxOutputTokens for the TaskMemoryAdjudicate keep/stale verdict call |
| `AURELIA_LLM_SEMANTIC_DEDUP_CANDIDATE_MEMORIES_LIMIT` | `int` | `40` | `llm/memory_worker.go:43` | SQL LIMIT on saved memories shown to the model when checking for a semantic duplicate |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_3` | `int` | `64` | `llm/memory_worker.go:44` | MaxOutputTokens for the findSemanticDuplicate JSON call |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_6` | `int` | `8` | `llm/moderation.go:25` | MaxOutputTokens for the ALLOW/BLOCK moderation model call Model id/system prompt/categories are admin-configurable; the token cap is not |
| `AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_OPENAI` | `int` | `240` | `llm/openai_provider.go:22` | Env-overridable defaults (§ config-reference). Each falls back to the original hardcoded value when its AURELIA_* variable is unset. |
| `AURELIA_LLM_OFFICIAL_TOOL_SPEC` | `string` | `"medium"` | `llm/openai_provider.go:23` | Official Tool Spec |
| `AURELIA_LLM_READ_OPEN_AICHAT_STREAM_INIT` | `int` | `64*1024` | `llm/openai_provider.go:24` | Read Open Aichat Stream Init |
| `AURELIA_LLM_READ_OPEN_AICHAT_STREAM_MAX` | `int` | `1024*1024` | `llm/openai_provider.go:25` | Read Open Aichat Stream Max |
| `AURELIA_LLM_READ_OPEN_AIRESPONSES_STREAM_INIT` | `int` | `64*1024` | `llm/openai_provider.go:26` | Read Open Airesponses Stream Init |
| `AURELIA_LLM_READ_OPEN_AIRESPONSES_STREAM_MAX` | `int` | `1024*1024` | `llm/openai_provider.go:27` | Read Open Airesponses Stream Max |
| `AURELIA_LLM_MAX_ITER_2` | `int` | `20` | `llm/openai_provider.go:105` | Max Iter 2 |
| `AURELIA_LLM_MAX_ITER_3` | `int` | `20` | `llm/openai_provider.go:605` | Max Iter 3 |
| `AURELIA_LLM_INLINE_QUOTE_SOURCE_INJECTION_CAP` | `int` | `8000` | `llm/orchestrator.go:30` | Env-overridable tuning knobs for inline literals used below. Each defaults to the previous hardcoded value when its AURELIA_* variable is unset (see docs/config-reference.md). |
| `AURELIA_LLM_IMAGE_MODE_FORCED_GENERATION_SIZE_COUNT_SIZE` | `string` | `"1024x1024"` | `llm/orchestrator.go:31` | Image Mode Forced Generation Size Count Size |
| `AURELIA_LLM_IMAGE_MODE_FORCED_GENERATION_SIZE_COUNT_COUNT` | `int` | `1` | `llm/orchestrator.go:32` | Image Mode Forced Generation Size Count Count |
| `AURELIA_LLM_IMAGE_PROMPT_OPTIMIZER_OUTPUT_TOKENS` | `int` | `400` | `llm/orchestrator.go:33` | Image Prompt Optimizer Output Tokens |
| `AURELIA_LLM_RECENT_HISTORY_STRINGS` | `int` | `6` | `llm/orchestrator.go:34` | Recent History Strings |
| `AURELIA_LLM_RECENT_HISTORY_STRINGS_2` | `int` | `200` | `llm/orchestrator.go:35` | Recent History Strings 2 |
| `AURELIA_LLM_TITLE_GENERATION_OUTPUT_TOKENS` | `int` | `60` | `llm/orchestrator.go:36` | Title Generation Output Tokens |
| `AURELIA_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MAX` | `int` | `600` | `llm/orchestrator.go:37` | Sandbox Exec Timeout Clamp Range Max |
| `AURELIA_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MIN` | `int` | `10` | `llm/orchestrator.go:38` | Sandbox Exec Timeout Clamp Range Min |
| `AURELIA_LLM_SANDBOX_EXEC_CTX_SAFETY_MARGIN` | `duration` | `150*time.Second` | `llm/orchestrator.go:39` | Sandbox Exec Ctx Safety Margin |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_WEB_SEARCH` | `int` | `16` | `llm/orchestrator.go:147` | Per Turn Tool Limits Web Search |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_WEB_FETCH` | `int` | `12` | `llm/orchestrator.go:148` | Per Turn Tool Limits Web Fetch |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_FETCH_IMAGE` | `int` | `16` | `llm/orchestrator.go:149` | images for a deck/doc — bounded so a turn can't mass-download |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `8` | `llm/orchestrator.go:150` | Per Turn Tool Limits Image Generate |
| `AURELIA_LLM_PER_TURN_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `16` | `llm/orchestrator.go:151` | §F10: cap sandbox executions/turn (each up to 120s) to bound abuse/DoS |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_SEARCH` | `int` | `40` | `llm/orchestrator.go:157` | Deep Research Tool Limits Web Search |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_FETCH` | `int` | `25` | `llm/orchestrator.go:158` | Deep Research Tool Limits Web Fetch |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_FETCH_IMAGE` | `int` | `12` | `llm/orchestrator.go:159` | Deep Research Tool Limits Fetch Image |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_IMAGE_GENERATE` | `int` | `4` | `llm/orchestrator.go:160` | Deep Research Tool Limits Image Generate |
| `AURELIA_LLM_DEEP_RESEARCH_TOOL_LIMITS_PYTHON_EXECUTE` | `int` | `8` | `llm/orchestrator.go:161` | Deep Research Tool Limits Python Execute |
| `AURELIA_LLM_MAX_TOOL_CALLS_PER_TURN` | `int` | `48` | `llm/orchestrator.go:169` | per-turn GLOBAL tool-call ceiling (§B4): bounds a single message's total tool-driven cost across ALL tools, on top of the per-tool caps — the native provider loop (maxIter=12) otherwise lets the model request unbounded tools per round. Deep Research deliberately fans out far more. |
| `AURELIA_LLM_MAX_TOOL_CALLS_PER_TURN_DEEP` | `int` | `150` | `llm/orchestrator.go:170` | Max Tool Calls Per Turn Deep |
| `AURELIA_LLM_TRUNC_ERR` | `int` | `2000` | `llm/orchestrator.go:416` | Trunc Err |
| `AURELIA_LLM_TOOL_TIMEOUTS` | `duration` | `10*time.Second` | `llm/orchestrator.go:2326` | Tool Timeouts |
| `AURELIA_LLM_TOOL_TIMEOUTS_2` | `duration` | `15*time.Second` | `llm/orchestrator.go:2327` | Tool Timeouts 2 |
| `AURELIA_LLM_TOOL_TIMEOUTS_3` | `duration` | `600*time.Second` | `llm/orchestrator.go:2329` | slow third-party image gateways need a wide window |
| `AURELIA_LLM_TOOL_TIMEOUT_DEFAULT` | `duration` | `100*time.Second` | `llm/orchestrator.go:2332` | Tool Timeout Default |
| `AURELIA_LLM_PROMPT_MAX_ITER` | `int` | `10` | `llm/prompt_tools.go:36` | Max rounds of the text-protocol (tool_mode=prompt) tool loop before it force-terminates with a give-up message. Doc comments in the same file still say 'up to 6' — stale; actual value is 10. Only affects prompt-mode models. |
| `AURELIA_LLM_PROMPT_MAX_RETRY` | `int` | `2` | `llm/prompt_tools.go:37` | Max retries both for re-emitting a malformed <tool_call> JSON (loop) and for re-running a failing tool (retries loop at line 222). No backoff/sleep between retries. Same constant reused for two distinct retry paths (lines 185 and 222). |
| `AURELIA_LLM_PROMPT_MODE_TOOL_RESULT_SUMMARY_LENGTH` | `int` | `240` | `llm/prompt_tools.go:38` | Chars kept when truncating a tool's output for the SSE tool_result Summary and the stored tool_call block (truncate(output,240)); also at line 242. Two call sites (235, 242) with the same 240 literal. |
| `AURELIA_LLM_PROVIDER_REQUEST_BODY_MAX_BYTES` | `int` | `128*1024` | `llm/provider_request_capture.go:20` | Max bytes retained for a sanitized captured provider request body / serialized headers blob (debug inspection). 128*1024. Excess is clamped with '...[truncated]'. |
| `AURELIA_LLM_PROVIDER_REQUEST_VALUE_MAX_BYTES` | `int` | `8*1024` | `llm/provider_request_capture.go:21` | Max bytes retained per individual captured value (URL, each header value, each JSON string) before truncation. 8*1024. Base64 data: URIs are replaced by a length placeholder instead of clamped. |
| `AURELIA_LLM_P` | `int64` | `604800` | `llm/quota.go:30` | Fallback quota window length when a per-group quota row's PeriodSeconds is <=0 Per-window period normally comes from the per-model-group quota row (admin); this is the fallback |
| `AURELIA_LLM_IMAGE_DOCUMENT_FLAT_TOKEN_ALLOWANCE` | `int` | `1024` | `llm/quota.go:270` | Flat per-block token estimate for image/document blocks (base64 not text-tokenized) in request-token estimation Feeds the credit preflight estimate |
| `AURELIA_LLM_OUTPUT_RESERVE` | `int` | `2000` | `llm/quota.go:313` | Fixed output-token reserve added to estimated input when preflighting affordability of a credit-charged turn Preflight can be disabled via admin credit_preflight_enabled, but the 2k reserve is not tunable |
| `AURELIA_LLM_P_2` | `int64` | `604800` | `llm/quota.go:327` | Fallback timed-credit window length when a group's CreditPeriodSeconds is <=0 Group CreditPeriodSeconds overrides; this is the default window |
| `AURELIA_LLM_MAX_TOK_3` | `int` | `512` | `llm/task_llm.go:31` | Max Tok 3 |
| `AURELIA_LLM_TITLE_GENERATION_WORD_CAP` | `int` | `8` | `llm/task_llm.go:32` | Title Generation Word Cap |
| `AURELIA_LLM_ROUTER_RETRIEVAL_QUERY_CAP` | `int` | `3` | `llm/task_llm.go:33` | Router Retrieval Query Cap |
| `AURELIA_LLM_RESEARCH_CROSS_VALIDATE_FINDING_CAPS_CONFIRMED` | `int` | `8` | `llm/task_llm.go:34` | Research Cross Validate Finding Caps Confirmed |
| `AURELIA_LLM_RESEARCH_CROSS_VALIDATE_FINDING_CAPS_DISPUTED` | `int` | `4` | `llm/task_llm.go:35` | Research Cross Validate Finding Caps Disputed |
| `AURELIA_LLM_RESEARCH_CROSS_VALIDATE_FINDING_CAPS_UNVERIFIED` | `int` | `6` | `llm/task_llm.go:36` | Research Cross Validate Finding Caps Unverified |
| `AURELIA_LLM_MAX_CONCURRENT_TOOLS` | `int` | `4` | `llm/tool_exec.go:28` | Semaphore size limiting concurrent search/fetch tool calls in execToolsConcurrent (make(chan struct{}, maxConcurrentTools)) Defined in tool_exec.go; consumed by deep_research.go:738 to bound research fan-out |
| `AURELIA_LLM_VCTX` | `duration` | `45*time.Second` | `llm/verify.go:77` | context.WithTimeout bounding the secondary verify/auditor model call |
| `AURELIA_LLM_MAX_OUTPUT_TOKENS_7` | `int` | `800` | `llm/verify.go:94` | MaxOutputTokens for the TaskVerify adversarial fact-check JSON call Auditor model id is admin-configurable (verify_model_id); token cap is not |


### 2. RAG 文档解析 / 向量检索

文档分块、Embedding 批量与并发、Qdrant 客户端、MinerU OCR 轮询等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_RAG_NET_DIALER_TIMEOUT` | `duration` | `15*time.Second` | `rag/embedder_http.go:34` | Net Dialer Timeout |
| `AURELIA_RAG_NET_DIALER_KEEP_ALIVE` | `duration` | `30*time.Second` | `rag/embedder_http.go:35` | Net Dialer Keep Alive |
| `AURELIA_RAG_HTTP_TRANSPORT_MAX_IDLE_CONNS` | `int` | `50` | `rag/embedder_http.go:36` | HTTP Transport Max Idle Conns |
| `AURELIA_RAG_HTTP_TRANSPORT_MAX_IDLE_CONNS_PER_HOST` | `int` | `10` | `rag/embedder_http.go:37` | HTTP Transport Max Idle Conns Per Host |
| `AURELIA_RAG_HTTP_TRANSPORT_IDLE_CONN_TIMEOUT` | `duration` | `90*time.Second` | `rag/embedder_http.go:38` | HTTP Transport Idle Conn Timeout |
| `AURELIA_RAG_HTTP_TRANSPORT_TLSHANDSHAKE_TIMEOUT` | `duration` | `20*time.Second` | `rag/embedder_http.go:39` | HTTP Transport Tlshandshake Timeout |
| `AURELIA_RAG_HTTP_TRANSPORT_RESPONSE_HEADER_TIMEOUT` | `duration` | `30*time.Second` | `rag/embedder_http.go:40` | HTTP Transport Response Header Timeout |
| `AURELIA_RAG_HTTP_TRANSPORT_EXPECT_CONTINUE_TIMEOUT` | `duration` | `1*time.Second` | `rag/embedder_http.go:41` | HTTP Transport Expect Continue Timeout |
| `AURELIA_RAG_HTTP_CLIENT_TIMEOUT` | `duration` | `3*time.Minute` | `rag/embedder_http.go:42` | HTTP Client Timeout |
| `AURELIA_RAG_TRUNCATE_AT_N` | `int` | `1200` | `rag/embedder_http.go:44` | Truncate At N |
| `AURELIA_RAG_TRUNCATE_AT_N_2` | `int` | `16*1024` | `rag/embedder_http.go:45` | Truncate At N 2 |
| `AURELIA_RAG_IO_LIMIT_READER` | `int64` | `4096` | `rag/embedder_http.go:47` | Io Limit Reader |
| `AURELIA_RAG_IO_LIMIT_READER_2` | `int64` | `4096` | `rag/embedder_http.go:48` | Io Limit Reader 2 |
| `AURELIA_RAG_EMBEDDING_RETRY_DELAY` | `duration` | `time.Second` | `rag/embedder_http.go:50` | Embedding Retry Delay |
| `AURELIA_RAG_EMBEDDING_RETRY_DELAY_2` | `duration` | `30*time.Second` | `rag/embedder_http.go:51` | Embedding Retry Delay 2 |
| `AURELIA_RAG_EMBEDDING_RETRY_DELAY_3` | `duration` | `1000*time.Millisecond` | `rag/embedder_http.go:52` | Embedding Retry Delay 3 |
| `AURELIA_RAG_DASH_SCOPE_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:179` | DashScope remains concurrent, but two batches per document avoids a single OCR-heavy ingest monopolising the workspace. A process-wide cap also bounds the aggregate when several RAG workers reach embedding at once. |
| `AURELIA_RAG_DASH_SCOPE_GLOBAL_EMBED_CONCURRENCY` | `int` | `2` | `rag/embedder_http.go:180` | Dash Scope Global Embed Concurrency |
| `AURELIA_RAG_DASH_SCOPE_EMBED_ATTEMPT_TIMEOUT` | `duration` | `60*time.Second` | `rag/embedder_http.go:188` | DashScope compatible-mode can occasionally accept the request and then sit behind provider-side queueing without returning headers. Bound each attempt below the shared client's broad 3-minute safety cap so indexing fails with a retryable, visible error instead of looking stuck for many minutes. Variable for tests. |
| `AURELIA_RAG_EMBED_CONCURRENCY` | `int` | `4` | `rag/embedder_http.go:194` | embedConcurrency caps how many upstream embedding batches run at once. The old code did them strictly sequentially, so a 500-chunk doc paid 50 serial round-trips. Keep this moderate because the RAG queue can process multiple documents at once and each document gets its own concurrency allowance. |
| `AURELIA_RAG_MAX_ATTEMPTS` | `int` | `2` | `rag/embedder_http.go:445` | Max Attempts |
| `AURELIA_RAG_PDF_INSPECTION_TIMEOUT` | `duration` | `8*time.Second` | `rag/parser.go:56` | PDF Inspection Timeout |
| `AURELIA_RAG_OFFICE_XML_ZIP_ENTRY_READ_CAP` | `int64` | `16*1024*1024` | `rag/parser.go:253` | officeXMLZipEntryReadCap bounds a single DOCX/PPTX zip-entry read. |
| `AURELIA_RAG_PDF_INSPECTION_SAMPLE_LIMIT` | `int` | `3` | `rag/parser.go:333` | PDF Inspection Sample Limit |
| `AURELIA_RAG_PDF_THIN_CHARS_PER_PAGE` | `int` | `200` | `rag/parser.go:334` | PDF Thin Chars Per Page |
| `AURELIA_RAG_CMD_WAIT_DELAY` | `duration` | `500*time.Millisecond` | `rag/parser.go:375` | cmdWaitDelay is the grace period before the PDF-inspection child process is force-killed after its context deadline fires. |
| `AURELIA_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_IMAGE` | `int` | `5` | `rag/parser.go:438` | strongScan image-to-page ratio: default imageCount*5 >= pages*4 (≈ one image per 80% of pages). Both multipliers are independently overridable. |
| `AURELIA_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_PAGE` | `int` | `4` | `rag/parser.go:439` | PDF Raw Signals Strong Scan Page |
| `AURELIA_RAG_MINERU_SOURCE_OBJECT_CLEANUP_TIMEOUT` | `duration` | `30*time.Second` | `rag/parser.go:603` | mineruSourceObjectCleanupTimeout bounds the best-effort delete of the source object we uploaded for MinerU, run on a fresh context after the parse. |
| `AURELIA_RAG_MINERU_POLL_DEADLINE` | `duration` | `20*time.Minute` | `rag/parser.go:606` | mineruPollDeadline caps the total MinerU extract poll loop. |
| `AURELIA_RAG_MINERU_SUBMIT_ERROR_BODY_TRUNCATION` | `int` | `256` | `rag/parser.go:695` | mineruSubmitErrorBodyTruncation caps the MinerU submit error body kept in logs. |
| `AURELIA_RAG_MINERU_POLL_ERROR_BODY_TRUNCATION` | `int` | `256` | `rag/parser.go:757` | mineruPollErrorBodyTruncation caps the MinerU poll error body kept in logs. |
| `AURELIA_RAG_MINERUZIPCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:838` | We rewrite `![alt](images/foo.png)` → `![alt](mineru://foo.png)` as a canonical intermediate form; runMinerUMarkdown strips those markers before the text reaches chunking, embedding, or database storage. mineruZipClient downloads the MinerU result zip. Because the zip URL comes from the API *response* (not admin config), it goes through an SSRF-safe client that blocks private/internal IPs at dial time (§C6). |
| `AURELIA_RAG_FULL_MD_READ_CAP_INSIDE_ZIP` | `int64` | `32*1024*1024` | `rag/parser.go:841` | fullMdReadCapInsideZip bounds the in-zip full.md read. |
| `AURELIA_RAG_MAX_ZIP` | `int64` | `500*1024*1024` | `rag/parser.go:862` | Cap the download at 500 MiB — zips for normal documents are <100 MiB; anything bigger is a runaway and we'd rather error than OOM the server. |
| `AURELIA_RAG_MINERUCLIENT_TIMEOUT` | `duration` | `5*time.Minute` | `rag/parser.go:1002` | mineruClient is a long-timeout HTTP client used for the submit + poll + download legs of the cloud API. 60s connect + a per-call deadline via the request context keeps individual round-trips honest while the larger poll-loop ceiling is enforced in minerUPollTask. |
| `AURELIA_RAG_MINERU_SOURCE_TTLSECONDS` | `int` | `60*60` | `rag/parser.go:1008` | mineruSourceTTLSeconds is the presigned-URL lifetime for the document we hand MinerU. It must outlast the full OCR window (poll cap 20 min + MinerU queue time); 1 hour gives generous head-room without leaving objects around long (they're also explicitly deleted right after the parse). |
| `AURELIA_RAG_RAG_FAST_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:42` | asynq worker concurrency for the rag-fast ingest lane (text/spreadsheet docs) const block; passed to asynq.Config.Concurrency in UseAsynq |
| `AURELIA_RAG_RAG_SLOW_QUEUE_CONCURRENCY` | `int` | `4` | `rag/rag.go:43` | asynq worker concurrency for the rag (slow) ingest lane (MinerU OCR docs) |
| `AURELIA_RAG_INGEST_PIPELINE_TIMEOUT` | `duration` | `70*time.Minute` | `rag/rag.go:44` | overall RAG ingest pipeline deadline (MinerU parse + embed) applied via context.WithTimeout used at lines 301 (in-process fallback) and 337 (asynq handler) |
| `AURELIA_RAG_INGEST_TASK_TIMEOUT` | `duration` | `75*time.Minute` | `rag/rag.go:45` | asynq per-task Timeout for a rag.ingest task (outer bound above pipeline timeout) used at line 279 asynq.Timeout() |
| `AURELIA_RAG_INGEST_UNIQUE_TTL` | `duration` | `80*time.Minute` | `rag/rag.go:46` | asynq uniqueness-lock TTL to dedupe duplicate ingest enqueues used at line 285 asynq.Unique() |
| `AURELIA_RAG_INGEST_HEARTBEAT_INTERVAL` | `duration` | `30*time.Second` | `rag/rag.go:47` | interval at which a running ingest worker touches the doc heartbeat row ticker in startIngestHeartbeat (line 417) |
| `AURELIA_RAG_INGEST_STALE_AFTER` | `duration` | `4*time.Minute` | `rag/rag.go:48` | heartbeat age after which an in-progress (parsing/embedding) doc is treated as stale and reclaimed used in ClaimStaleIncompleteDocuments call line 241 |
| `AURELIA_RAG_INGEST_PENDING_STALE_AFTER` | `duration` | `ingestUniqueTTL` | `rag/rag.go:49` | age after which a still-pending (never-started) doc is reclaimed; aliased to the unique-lock TTL used at line 240 |
| `AURELIA_RAG_INGEST_RECOVERY_INTERVAL` | `duration` | `time.Minute` | `rag/rag.go:50` | ticker interval for the continuous stale-ingest recovery loop ticker in RunIngestRecovery line 221 |
| `AURELIA_RAG_INGEST_FINALIZE_TIMEOUT` | `duration` | `30*time.Second` | `rag/rag.go:51` | deadline for Qdrant vector cleanup during failure finalization used at line 446 |
| `AURELIA_RAG_INGEST_ASYNQ_LEASE_MAX_RETRIES` | `int` | `1` | `rag/rag.go:52` | asynq MaxRetry for lease/process-loss recovery (business retries handled internally) used at line 282 asynq.MaxRetry() |
| `AURELIA_RAG_INGEST_ASYNQ_RETRY_DELAY` | `duration` | `2*time.Minute` | `rag/rag.go:53` | fixed asynq RetryDelayFunc delay before a lost-lease task is retried returned by RetryDelayFunc line 138 |
| `AURELIA_RAG_INGEST_QUEUE_NAME` | `duration` | `2*time.Second` | `rag/rag.go:59` | deadline for the GetDocument lookup that classifies a doc into fast/slow queue |
| `AURELIA_RAG_RUN_INGEST_WITH_RETRIES` | `int` | `3` | `rag/rag.go:60` | whole-pipeline retry attempt count (for attempt := 1; attempt <= 3) loop also guards attempt < 3 at line 391 |
| `AURELIA_RAG_RUN_INGEST_WITH_RETRIES_2` | `duration` | `3*time.Second` | `rag/rag.go:61` | linear backoff between whole-pipeline retries (3s * attempt number) attempt 1->3s, attempt 2->6s |
| `AURELIA_RAG_START_INGEST_HEARTBEAT` | `duration` | `5*time.Second` | `rag/rag.go:62` | deadline for each TouchDocumentIngest heartbeat write |
| `AURELIA_RAG_FINALIZE_CHUNK_CLEANUP_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:63` | deadline to delete chunk rows during failed-ingest finalization |
| `AURELIA_RAG_FINALIZE_STATUS_TIMEOUT` | `duration` | `10*time.Second` | `rag/rag.go:64` | deadline for the terminal 'failed' status DB transition after cleanup |
| `AURELIA_RAG_EXTRACTION_FAILURE_REASON_CAP` | `int` | `500` | `rag/rag.go:65` | max chars of the extraction-failure reason stored on the doc status guard at line 595 if len(reason) > 500 |
| `AURELIA_RAG_EMBEDDING_ERROR_TRUNCATE` | `int` | `4096` | `rag/rag.go:66` | max chars of the embedding error message stored in usage_logs |
| `AURELIA_RAG_RETRIEVE` | `int` | `5` | `rag/rag.go:67` | process-local TTL for cached RAG query-embedding vectors |
| `AURELIA_RAG_DENSE_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:68` | max entries in the query-embedding cache before it is flushed/rebuilt crude cap check at line 948 |
| `AURELIA_RAG_KEYWORD_SEARCH_LEG_LIMIT` | `int` | `30` | `rag/rag.go:69` | fallback number of retrieved snippets when caller passes topK <= 0 |
| `AURELIA_RAG_SNIPPET_OF` | `int` | `240` | `rag/rag.go:70` | top-N dense vector hits requested from Qdrant per scope leg '30 dense' per comment line 1221 |
| `AURELIA_RAG_SPLIT_PARAGRAPHS_AND_TABLES` | `int` | `800` | `rag/rag.go:71` | top-N keyword hits requested from Qdrant per scope leg |
| `AURELIA_RAG_ROUTER_CALL_TIMEOUT` | `duration` | `12*time.Second` | `rag/rag.go:72` | reciprocal-rank-fusion constant k in 1/(rank+k) for dense+keyword fusion |
| `AURELIA_RAG_MAP_REDUCE_SUMMARISE` | `int` | `200` | `rag/rag.go:73` | default snippet char length when max <= 0 is passed |
| `AURELIA_RAG_COLLECT_DOC_HINTS` | `int` | `120` | `rag/rag.go:74` | byte budget for a retrieved chunk's injected parent-window snippet used in expandHit call line 1117 |
| `AURELIA_RAG_COLLECT_DOC_HINTS_2` | `int` | `12` | `rag/rag.go:75` | target size (chars) of an embedded child chunk |
| `AURELIA_RAG_QUERY_EMBED_TTL` | `duration` | `10*time.Minute` | `rag/rag.go:933` | target/truncation size (chars) of a parent section chunk |
| `AURELIA_RAG_QUERY_EMBED_MAX` | `int` | `4096` | `rag/rag.go:934` | sliding-window overlap (chars, ~12%) between consecutive child chunks |
| `AURELIA_RAG_FUSE_RECIPROCAL_RANK` | `int` | `60` | `rag/rag.go:1427` | a paragraph containing an image marker is kept atomic only if under this char length imageRe.MatchString(p) && len(p) < 800 |
| `AURELIA_RAG_RETRIEVED_SNIPPET_CHARS` | `int` | `2000` | `rag/rag.go:1520` | deadline for the task-model query-router JSON call on the first-token hot path |
| `AURELIA_RAG_CHILD_TARGET_CHARS` | `int` | `2000` | `rag/rag.go:1757` | est-token size of each chunk group fed to the map-reduce summariser |
| `AURELIA_RAG_PARENT_TARGET_CHARS` | `int` | `4800` | `rag/rag.go:1758` | max number of summarised chunk groups in map-reduce |
| `AURELIA_RAG_CHUNK_OVERLAP_CHARS` | `int` | `250` | `rag/rag.go:1761` | prompt-embedded soft cap (≤200字) on each map-reduce partial summary length literal inside Chinese prompt string |
| `AURELIA_RAG_MAPREDUCE_GROUPTOKENS` | `int` | `6000` | `rag/rag.go:2334` | max chars of a document's first content shown as a router doc-hint |
| `AURELIA_RAG_MAPREDUCE_MAXGROUPS` | `int` | `8` | `rag/rag.go:2335` | max number of document hints assembled for the router prompt |
| `AURELIA_RAG_BATCH_SIZE` | `int` | `64` | `rag/vector_admin.go:136` | chunks per embed+upsert batch during admin missing-vector rebuild |
| `AURELIA_VECTOR_QDRANT_HTTP_CLIENT_TIMEOUT` | `duration` | `20*time.Second` | `vector/qdrant.go:31` | http.Client overall timeout for all Qdrant requests |
| `AURELIA_VECTOR_QDRANT_SCROLL_PAGE_SIZE_EXISTINGCHUNKIDS` | `int` | `256` | `vector/qdrant.go:32` | scroll page limit when enumerating existing chunk ids |
| `AURELIA_VECTOR_QDRANT_SCROLL_PAGE_SIZE_VECTORCHUNKSTATUSES` | `int` | `256` | `vector/qdrant.go:33` | scroll page limit when auditing per-chunk vector presence |
| `AURELIA_VECTOR_DELETE_CONCURRENCY` | `int` | `4` | `vector/qdrant.go:460` | max concurrent per-collection delete requests in deleteByField sweep |


### 3. 沙盒代码执行

`python_execute` 沙盒。Go 侧变量需重启 API 进程生效；`SANDBOX_*` 变量作用于 `sandbox-service` 进程，需重启该服务生效。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `SANDBOX_MAX_OUTPUT_BYTES` | `int` | `32768` | `sandbox-service/app.py:120` | stdout/stderr truncation |
| `SANDBOX_MAX_ARTIFACT_BYTES` | `int` | `20971520` | `sandbox-service/app.py:121` | single produced file cap |
| `SANDBOX_EXEC_READER_LOOP_POLL_S` | `float` | `0.2` | `sandbox-service/app.py:125` | Exec reader loop (_run_exec_bounded): selector poll cadence, per-read chunk size, and the post-loop process-wait grace period. |
| `SANDBOX_EXEC_READER_LOOP_CHUNK_BYTES` | `int` | `8192` | `sandbox-service/app.py:126` | Exec Reader Loop Chunk Bytes |
| `SANDBOX_EXEC_READER_LOOP_WAIT_GRACE_S` | `float` | `2` | `sandbox-service/app.py:127` | Exec Reader Loop Wait Grace S |
| `SANDBOX_ARCHIVE_TAR_READ_POLL_S` | `float` | `5.0` | `sandbox-service/app.py:131` | Archive tar-stream reader loop: selector poll cadence, per-read chunk size, and the post-loop process-wait grace period. |
| `SANDBOX_ARCHIVE_TAR_READ_CHUNK_BYTES` | `int` | `65536` | `sandbox-service/app.py:132` | Archive Tar Read Chunk Bytes |
| `SANDBOX_ARCHIVE_TAR_READ_WAIT_GRACE_S` | `float` | `10` | `sandbox-service/app.py:133` | Archive Tar Read Wait Grace S |
| `SANDBOX_S3_MAX_ATTEMPTS` | `int` | `3` | `sandbox-service/app.py:137` | Object-storage SDK timeouts/retries — bound every SDK call so a slow/hung bucket can't freeze the reaper, DELETE, or session creation. |
| `SANDBOX_S3_CONNECT_TIMEOUT_S` | `float` | `10` | `sandbox-service/app.py:138` | S3 Connect Timeout S |
| `SANDBOX_S3_READ_TIMEOUT_S` | `float` | `120` | `sandbox-service/app.py:139` | S3 Read Timeout S |
| `SANDBOX_OSS_CONNECT_TIMEOUT_S` | `float` | `30` | `sandbox-service/app.py:140` | OSS Connect Timeout S |
| `AURELIA_SANDBOX_MAX_SANDBOX_RESP_BYTES` | `int64` | `256<<20` | `sandbox/sandbox.go:162` | Cap on decoded sidecar JSON response to prevent OOM from a buggy/compromised sidecar. 256 << 20. |
| `AURELIA_SANDBOX_EXEC_CLIENT_OVERHEAD` | `duration` | `120*time.Second` | `sandbox/sandbox.go:171` | Added to exec cap to size the HTTP client timeout so client never deadlines before the sidecar finishes artifact collection. client Timeout = exec + execClientOverhead (line 193). |
| `AURELIA_SANDBOX_SANDBOX_ERROR_BODY_READ_CAP` | `int64` | `64<<10` | `sandbox/sandbox.go:174` | Bytes read from a non-2xx sidecar error response before truncation. io.LimitReader(resp.Body, 64<<10). |


### 4. 内置工具（搜索 / Python / 网络安全）

web_search 结果条数与超时、Python 安全模式、SSRF / 网络安全护栏等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_NETSAFE_NETSAFE_SSRF_CLIENT_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `netsafe/netsafe.go:18` | TCP connect timeout for the shared SSRF-safe/private-block HTTP clients. net.Dialer{Timeout: 10*time.Second}. |
| `AURELIA_NETSAFE_MAX_IDLE_CONNS` | `int` | `10` | `netsafe/netsafe.go:19` | Max idle keep-alive connections for the SSRF-safe HTTP clients. |
| `AURELIA_NETSAFE_IDLE_CONN_TIMEOUT` | `duration` | `30*time.Second` | `netsafe/netsafe.go:20` | Idle keep-alive connection timeout for the SSRF-safe HTTP clients. |
| `AURELIA_NETSAFE_TLSHANDSHAKE_TIMEOUT` | `duration` | `10*time.Second` | `netsafe/netsafe.go:21` | TLS handshake timeout for the SSRF-safe HTTP clients. |
| `AURELIA_NETSAFE_NETSAFE_REDIRECTS` | `int` | `5` | `netsafe/netsafe.go:22` | Redirect hop limit before the SSRF-safe client aborts (each hop re-validated). if len(via) >= 5 -> too many redirects. |
| `AURELIA_TOOLS_IN_TOP_K` | `int` | `5` | `tools/builtins.go:37` | Env-overridable defaults (see docs/config-reference.md). Each falls back to the original hardcoded value when its AURELIA_* variable is unset. |
| `AURELIA_TOOLS_WEB_FETCH_RESPONSE_BODY_READ_CAP` | `int64` | `256*1024` | `tools/builtins.go:38` | Web Fetch Response Body Read Cap |
| `AURELIA_TOOLS_WEB_FETCH_EXTRACTED_TEXT_CHAR_CAP` | `int` | `32000` | `tools/builtins.go:39` | Web Fetch Extracted Text Char Cap |
| `AURELIA_TOOLS_PYTHON_EXECUTE_UPLOAD_STAGING_FILE_SIZE` | `int64` | `20*1024*1024` | `tools/builtins.go:40` | Python Execute Upload Staging File Size |
| `AURELIA_TOOLS_PYTHON_EXECUTE_IMAGE_ARTIFACT_STAGING_SIZE` | `int64` | `20*1024*1024` | `tools/builtins.go:41` | Python Execute Image Artifact Staging Size |
| `AURELIA_TOOLS_PYTHON_EXECUTE_STDOUT_STDERR_TRUNCATION_CAP` | `int` | `32*1024` | `tools/builtins.go:42` | Python Execute Stdout Stderr Truncation Cap |
| `AURELIA_TOOLS_IN_N` | `int` | `4` | `tools/builtins.go:43` | In N |
| `AURELIA_TOOLS_IN_SIZE` | `string` | `"1024x1024"` | `tools/builtins.go:44` | In Size |
| `AURELIA_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW` | `duration` | `24*time.Hour` | `tools/builtins.go:45` | Daily Image Limit Reset Window |
| `AURELIA_TOOLS_P` | `int64` | `604800` | `tools/builtins.go:46` | P |
| `AURELIA_TOOLS_IMAGE_IMAGE_INPUT_IMAGE_CAP` | `int` | `3` | `tools/builtins.go:47` | Image Image Input Image Cap |
| `AURELIA_TOOLS_FETCHREMOTEIMAGE_DOWNLOAD_CAP` | `int64` | `32<<20` | `tools/builtins.go:48` | Fetchremoteimage Download Cap |
| `AURELIA_TOOLS_IN_TOP_K_2` | `int` | `5` | `tools/builtins.go:49` | In Top K 2 |
| `AURELIA_TOOLS_CONFIDENCE` | `float` | `0.95` | `tools/builtins.go:50` | Confidence |
| `AURELIA_TOOLS_MAX_IMG` | `int64` | `15*1024*1024` | `tools/builtins.go:240` | Max Img |
| `AURELIA_TOOLS_SSRFSAFECLIENT_OVERALL_TIMEOUT_WEB_FETCH` | `duration` | `25*time.Second` | `tools/net_safety.go:15` | Total HTTP client timeout for model-controlled fetches (web_fetch, remote image fetch). netsafe.SafeClient(25*time.Second). |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `tools/net_safety.go:16` | TCP connect timeout for admin-configured tool endpoints (search backends, image gateways). net.Dialer{Timeout: 10*time.Second}. |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_TLS_HANDSHAKE_TIMEOUT` | `duration` | `10*time.Second` | `tools/net_safety.go:17` | TLS handshake timeout for admin-configured tool endpoints. TLSHandshakeTimeout. |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_RESPONSE_HEADER_TIMEOUT` | `duration` | `600*time.Second` | `tools/net_safety.go:18` | Time to wait for response headers from tool endpoints (large because image gen is slow). ResponseHeaderTimeout: 600*time.Second; per-tool ctx is the real bound. No overall body timeout set. |
| `AURELIA_TOOLS_TOOLHTTPCLIENT_IDLE_CONN_TIMEOUT` | `duration` | `90*time.Second` | `tools/net_safety.go:19` | Idle keep-alive connection timeout for the tool HTTP client. IdleConnTimeout: 90*time.Second. |


### 5. 会话 / 消息 / 流式 API

SSE 心跳、流恢复窗口、生成时长上限、分页与搜索上限、消息路径缓存等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_AUTH_USER_CACHE_TTL` | `duration` | `5*time.Minute` | `api/cache_helpers.go:13` | TTL for the cached authenticated-user record used on every protected request. |
| `AURELIA_API_AUTH_USER_CACHE_TTL_GROUP_ALREADY` | `duration` | `time.Second` | `api/cache_helpers.go:14` | Fallback cache TTL applied when the user's group entitlement has already expired (near-immediate re-check). return time.Second; also clamps TTL down to time-until-group-expiry when sooner than 5m. |
| `AURELIA_API_LIMIT_2` | `int` | `200` | `api/conversations_handlers.go:26` | Default page size for GET /conversations when ?limit is absent |
| `AURELIA_API_LIMIT_3` | `int` | `500` | `api/conversations_handlers.go:32` | Upper clamp on ?limit for GET /conversations |
| `AURELIA_API_SEARCH_MESSAGE_HIT_LIMIT` | `int` | `40` | `api/conversations_handlers.go:99` | Max message-content hits returned by SearchConversations |
| `AURELIA_API_IMPORT_MAX_CONVERSATIONS` | `int` | `1000` | `api/conversations_handlers.go:164` | Max conversations schedulable in one bulk import request |
| `AURELIA_API_IMPORT_MAX_MESSAGES_PER_CONV` | `int` | `10000` | `api/conversations_handlers.go:165` | Max messages per conversation in a bulk import |
| `AURELIA_API_IMPORT_MAX_CONTENT_BYTES` | `int` | `200*1024` | `api/conversations_handlers.go:166` | Per-message content byte cap on import; longer is truncated |
| `AURELIA_API_INLINE_THREAD_QUOTE_CAP` | `int` | `4000` | `api/conversations_handlers.go:282` | Max runes of a quoted excerpt injected as inline-thread system context |
| `AURELIA_API_GETCONVERSATION_ACTIVE_PATH_LIMIT` | `int` | `200` | `api/conversations_handlers.go:345` | Upper clamp on ?limit for reverse-paginated active path in GET /conversations/:id |
| `AURELIA_API_LIMIT_4` | `int` | `30` | `api/conversations_handlers.go:510` | Default trailing window size for GET /conversations/:id/messages |
| `AURELIA_API_LISTMESSAGES_PAGE_LIMIT` | `int` | `200` | `api/conversations_handlers.go:512` | Upper clamp on ?limit for active-path message pagination |
| `AURELIA_API_P` | `duration` | `604800*time.Second` | `api/credits_handlers.go:18` | Fallback length of the credit consumption/refresh window (7 days) used when the group's CreditPeriodSeconds is <=0; also becomes the per-window cache TTL at line 39. Real value comes from per-group CreditPeriodSeconds (admin/per-group field, schema default 604800); this literal is only the safety fallback. Same 604800 fallback repeated in llm/quota.go, store/quotas.go, tools/builtins.go, models_handlers.go. |
| `AURELIA_API_RATE_LIMIT_USER` | `int` | `20` | `api/kbs_handlers.go:13` | Per-user upload rate cap for KB documents: 20 uploads per rolling 1-minute window (§C4). Window = time.Minute (also hardcoded). No env/admin override; rateLimitUser signature is (perWindow int, window time.Duration). |
| `AURELIA_API_CONFIDENCE` | `float` | `0.95` | `api/memories_handlers.go:13` | Confidence score stamped on a memory the user manually creates via the API. No env/admin override. |
| `AURELIA_API_MAX_GEN_DURATION` | `duration` | `90*time.Minute` | `api/messages_handlers.go:27` | Hard backstop cap on a detached generation turn (holds a concurrency slot) |
| `AURELIA_API_SSE_PING_HEARTBEAT_POST` | `duration` | `15*time.Second` | `api/messages_handlers.go:32` | Ping ticker keeping the SSE channel open during postMessage |
| `AURELIA_API_SSE_PING_HEARTBEAT_REGENERATE` | `duration` | `15*time.Second` | `api/messages_handlers.go:33` | 15s heartbeat so proxies don't close the SSE channel (§6.2) |
| `AURELIA_API_SSE_PING_HEARTBEAT_STREAM` | `duration` | `15*time.Second` | `api/messages_handlers.go:34` | Max buffered SSE events read per genstream.Read flush |
| `AURELIA_API_STREAM_STATUS_RECHECK_INTERVAL` | `duration` | `5*time.Second` | `api/messages_handlers.go:35` | Ping ticker in streamMessage follow loop |
| `AURELIA_API_STREAM_REPLAY_BATCH_SIZE` | `int` | `200` | `api/messages_handlers.go:36` | Ticker to re-poll message status (catches terminal state without a pub/sub event) |
| `AURELIA_API_ONLINE_PRESENCE_TOUCH_THROTTLE` | `duration` | `time.Minute` | `api/middleware.go:23` | Env-overridable defaults (§ config-reference); each falls back to the original hardcoded value when its AURELIA_* variable is unset. |
| `AURELIA_API_CONCURRENT_GEN_SLOT_SAFETY_TTL` | `duration` | `30*time.Minute` | `api/middleware.go:24` | Concurrent Gen Slot Safety TTL |
| `AURELIA_API_REQUEST_SIGNATURE_REPLAY_WINDOW_FUTURE` | `int64` | `300` | `api/middleware.go:25` | Request Signature Replay Window Future |
| `AURELIA_API_REQUEST_SIGNATURE_REPLAY_WINDOW_PAST` | `int64` | `60` | `api/middleware.go:26` | Request Signature Replay Window Past |
| `AURELIA_API_CREDIT_MULTIPLIER` | `float` | `5.0` | `api/models_handlers.go:21` | Divisor mapping a model's (input+output) price to the credit multiplier shown in the picker: a $5 combined price = x1.0. Result rounded to one decimal (v*10/10). Purely presentational credit-rate reference; no override. |
| `AURELIA_API_P_2` | `int64` | `604800` | `api/models_handlers.go:22` | Fallback window (7 days) for counting a model's per-cycle free allotment usage when the quota row's PeriodSeconds is <=0. Actual window from the per-group per-model quota row (admin-configurable); literal is the fallback only. |
| `AURELIA_API_JSON_REQUEST_BODY_SIZE_CAP` | `int64` | `4<<20` | `api/mux.go:16` | Max bytes read from any JSON request body (MaxBytesReader) to prevent memory exhaustion. 4<<20; backup import has its own 2GB limit elsewhere. |
| `AURELIA_API_PROJECT_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `200` | `api/projects_handlers.go:15` | Max number of active conversations returned when loading a single project detail view (limit 200, offset 0), no pagination surfaced to caller. Fixed args to store.ListConversations; not overridable. |
| `AURELIA_API_RATE_LIMIT_USER_2` | `int` | `20` | `api/projects_handlers.go:16` | Per-user upload rate cap for project-library documents: 20 uploads per 1-minute window (§C4). Window = time.Minute. Mirrors kbs_handlers.go:132; no override. |
| `AURELIA_API_RATE_LIMIT_REGISTER_MAX` | `int` | `5` | `api/router.go:51` | Env-overridable per-IP rate-limit budgets ("<N> per <window>") and the CORS preflight cache age. Defaults match the historical hardcoded values; each is tunable via the paired AURELIA_API_RATE_LIMIT_*_MAX / *_WINDOW variables (see docs/config-reference.md) without changing behaviour when unset. |
| `AURELIA_API_RATE_LIMIT_REGISTER_WINDOW` | `duration` | `60*time.Second` | `api/router.go:52` | Rate Limit Register Window |
| `AURELIA_API_RATE_LIMIT_LOGIN_MAX` | `int` | `10` | `api/router.go:54` | Rate Limit Login Max |
| `AURELIA_API_RATE_LIMIT_LOGIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:55` | Rate Limit Login Window |
| `AURELIA_API_RATE_LIMIT_LOGIN_2FA_MAX` | `int` | `10` | `api/router.go:57` | Rate Limit Login 2FA Max |
| `AURELIA_API_RATE_LIMIT_LOGIN_2FA_WINDOW` | `duration` | `60*time.Second` | `api/router.go:58` | Rate Limit Login 2FA Window |
| `AURELIA_API_RATE_LIMIT_LOGOUT_MAX` | `int` | `30` | `api/router.go:60` | Rate Limit Logout Max |
| `AURELIA_API_RATE_LIMIT_LOGOUT_WINDOW` | `duration` | `60*time.Second` | `api/router.go:61` | Rate Limit Logout Window |
| `AURELIA_API_RATE_LIMIT_REFRESH_MAX` | `int` | `30` | `api/router.go:63` | Rate Limit Refresh Max |
| `AURELIA_API_RATE_LIMIT_REFRESH_WINDOW` | `duration` | `60*time.Second` | `api/router.go:64` | Rate Limit Refresh Window |
| `AURELIA_API_RATE_LIMIT_VERIFY_EMAIL_MAX` | `int` | `10` | `api/router.go:66` | Rate Limit Verify Email Max |
| `AURELIA_API_RATE_LIMIT_VERIFY_EMAIL_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:67` | Rate Limit Verify Email Window |
| `AURELIA_API_RATE_LIMIT_SEND_CODE_MAX` | `int` | `3` | `api/router.go:69` | Rate Limit Send Code Max |
| `AURELIA_API_RATE_LIMIT_SEND_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:70` | Rate Limit Send Code Window |
| `AURELIA_API_RATE_LIMIT_FORGOT_PASSWORD_MAX` | `int` | `5` | `api/router.go:72` | Rate Limit Forgot Password Max |
| `AURELIA_API_RATE_LIMIT_FORGOT_PASSWORD_WINDOW` | `duration` | `15*60*time.Second` | `api/router.go:73` | Rate Limit Forgot Password Window |
| `AURELIA_API_RATE_LIMIT_RESET_PASSWORD_MAX` | `int` | `5` | `api/router.go:75` | Rate Limit Reset Password Max |
| `AURELIA_API_RATE_LIMIT_RESET_PASSWORD_WINDOW` | `duration` | `60*time.Second` | `api/router.go:76` | Rate Limit Reset Password Window |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_ISSUE_MAX` | `int` | `30` | `api/router.go:78` | Rate Limit Captcha Issue Max |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_ISSUE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:79` | Rate Limit Captcha Issue Window |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_VERIFY_MAX` | `int` | `60` | `api/router.go:81` | Rate Limit Captcha Verify Max |
| `AURELIA_API_RATE_LIMIT_CAPTCHA_VERIFY_WINDOW` | `duration` | `60*time.Second` | `api/router.go:82` | Rate Limit Captcha Verify Window |
| `AURELIA_API_RATE_LIMIT_FIRST_RUN_SETUP_MAX` | `int` | `10` | `api/router.go:84` | Rate Limit First Run Setup Max |
| `AURELIA_API_RATE_LIMIT_FIRST_RUN_SETUP_WINDOW` | `duration` | `60*time.Second` | `api/router.go:85` | Rate Limit First Run Setup Window |
| `AURELIA_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_MAX` | `int` | `60` | `api/router.go:87` | Rate Limit Public Shared Conversation Max |
| `AURELIA_API_RATE_LIMIT_PUBLIC_SHARED_CONVERSATION_WINDOW` | `duration` | `60*time.Second` | `api/router.go:88` | Rate Limit Public Shared Conversation Window |
| `AURELIA_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_MAX` | `int` | `240` | `api/router.go:90` | Rate Limit Shared Assets Files Artifacts Max |
| `AURELIA_API_RATE_LIMIT_SHARED_ASSETS_FILES_ARTIFACTS_WINDOW` | `duration` | `60*time.Second` | `api/router.go:91` | Rate Limit Shared Assets Files Artifacts Window |
| `AURELIA_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_MAX` | `int` | `20` | `api/router.go:93` | Rate Limit Oauth Start Callback Handoff Max |
| `AURELIA_API_RATE_LIMIT_OAUTH_START_CALLBACK_HANDOFF_WINDOW` | `duration` | `60*time.Second` | `api/router.go:94` | Rate Limit Oauth Start Callback Handoff Window |
| `AURELIA_API_RATE_LIMIT_PASSWORD_CHANGE_SET_MAX` | `int` | `5` | `api/router.go:96` | Rate Limit Password Change Set Max |
| `AURELIA_API_RATE_LIMIT_PASSWORD_CHANGE_SET_WINDOW` | `duration` | `60*time.Second` | `api/router.go:97` | Rate Limit Password Change Set Window |
| `AURELIA_API_RATE_LIMIT_IDENTITY_LINK_START_MAX` | `int` | `20` | `api/router.go:99` | Rate Limit Identity Link Start Max |
| `AURELIA_API_RATE_LIMIT_IDENTITY_LINK_START_WINDOW` | `duration` | `60*time.Second` | `api/router.go:100` | Rate Limit Identity Link Start Window |
| `AURELIA_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_MAX` | `int` | `10` | `api/router.go:102` | Rate Limit 2FA Setup Enable Disable Max |
| `AURELIA_API_RATE_LIMIT_2FA_SETUP_ENABLE_DISABLE_WINDOW` | `duration` | `5*60*time.Second` | `api/router.go:103` | Rate Limit 2FA Setup Enable Disable Window |
| `AURELIA_API_RATE_LIMIT_REDEEM_CODE_MAX` | `int` | `10` | `api/router.go:105` | Rate Limit Redeem Code Max |
| `AURELIA_API_RATE_LIMIT_REDEEM_CODE_WINDOW` | `duration` | `60*time.Second` | `api/router.go:106` | Rate Limit Redeem Code Window |
| `AURELIA_API_RATE_LIMIT_WORKSPACE_JOIN_MAX` | `int` | `30` | `api/router.go:108` | Rate Limit Workspace Join Max |
| `AURELIA_API_RATE_LIMIT_WORKSPACE_JOIN_WINDOW` | `duration` | `60*time.Second` | `api/router.go:109` | Rate Limit Workspace Join Window |
| `AURELIA_API_CORS_PREFLIGHT_CACHE_AGE` | `duration` | `600*time.Second` | `api/router.go:111` | CORS Preflight Cache Age |
| `AURELIA_API_SELF_USAGE_LOOKBACK_WINDOW` | `int` | `30` | `api/user_handlers.go:209` | Window (days) over which /api/me/usage sums the user's message count days := 30 |
| `AURELIA_API_LIMIT` | `int` | `200` | `api/workspaces_handlers.go:15` | Default limit (200) / offset (0) for the admin 'list all workspaces' endpoint when no ?limit/?offset query params are supplied. Caller may override limit with any positive int via ?limit (no upper clamp); the 200 default is hardcoded. |
| `AURELIA_API_ADMIN_WORKSPACE_DETAIL_CONVERSATIONS_PAGE_SIZE` | `int` | `500` | `api/workspaces_handlers.go:16` | Max conversations (limit 500, offset 0) loaded for the admin workspace triage detail view. Fixed args to store.ListWorkspaceConversations; not overridable. |
| `AURELIA_GENSTREAM_TTL` | `duration` | `2*time.Hour` | `genstream/genstream.go:14` | How long the per-message SSE event stream (gen:<id>) is retained in cache for reconnect/replay before expiry. Effectively the stream resume window; no env/admin override. Applied on every StreamAppend. |
| `AURELIA_MSGCACHE_PATH_TTL` | `duration` | `45*time.Second` | `msgcache/msgcache.go:15` | Short-lived cache TTL for a conversation's active-path message list (conv:path:...). |
| `AURELIA_MSGCACHE_MESSAGE_CACHE_VERSION_KEY_TTL` | `duration` | `10*time.Minute` | `msgcache/msgcache.go:16` | TTL of the conv:ver version counter used to invalidate cached message paths on mutation. Set via Bump()->Incr(versionKey, 10*time.Minute). |
| `AURELIA_QUEUE_IN_PROCESS_WORKERS` | `int` | `8` | `queue/queue.go:47` | fixed number of workers in the in-process background job pool |
| `AURELIA_QUEUE_PROCESS_JOB_BUFFER` | `int` | `256` | `queue/queue.go:48` | buffered capacity of the in-process job channel |
| `AURELIA_QUEUE_QUEUE_BACKPRESSURE_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:49` | context timeout for jobs run synchronously in the backpressure fallback path |
| `AURELIA_QUEUE_QUEUE_WORKER_JOB_TIMEOUT` | `duration` | `30*time.Minute` | `queue/queue.go:50` | per-job context timeout in worker loop (outlasts MinerU OCR up to 20m) |


### 6. 认证 / 会话 / 验证码

令牌缓存、验证码有效期与尝试次数、TOTP 时窗、OAuth state TTL 等（不含约定俗成的格式常量，如验证码位数）。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_MAX_CODE_ATTEMPTS` | `int` | `5` | `api/auth_handlers.go:24` | Wrong guesses allowed against a 6-digit verify/reset code before it is burned Pairs with the 6-digit code space; no env/admin override |
| `AURELIA_API_CODE_FAILURE_COUNTER_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:28` | How long the per-email wrong-guess counter (codefail:) lives; shares the code TTL 10*time.Minute |
| `AURELIA_API_MINIMUM_PASSWORD_LENGTH` | `int` | `8` | `api/auth_handlers.go:29` | Minimum accepted password length on register/setup/reset/change/set-password Repeated: auth_handlers.go:105,151,371 and user_handlers.go:83,132 |
| `AURELIA_API_EMAIL_VERIFICATION_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:30` | Validity of the emailed 6-digit account-verification code Also auth_handlers.go:294 (resend) |
| `AURELIA_API_PASSWORD_RESET_CODE_TTL` | `duration` | `10*time.Minute` | `api/auth_handlers.go:31` | Validity of the emailed 6-digit password-reset code Also auth_handlers.go:350 (forgotPassword) |
| `AURELIA_API_CAP_TOL` | `float` | `0.04` | `api/captcha.go:41` | Accepted error between submitted drop fraction and true gap fraction (~9px on 228px track) Deliberately forgiving; per-IP daily cap is the real abuse backstop |
| `AURELIA_API_CAPTCHA_CHALLENGE_CACHE_TTL` | `duration` | `5*time.Minute` | `api/captcha.go:44` | How long an issued slider-puzzle challenge stays solvable before expiring 5*time.Minute |
| `AURELIA_API_CAPTCHA_PASS_TTL` | `duration` | `10*time.Minute` | `api/captcha.go:110` | Validity of the stateless HMAC pass proving a captcha was just solved Bounds replay of a solved-captcha pass at register time |
| `AURELIA_API_OAUTH_2FA_HANDOFF_COOKIE_TTL` | `duration` | `300*time.Second` | `api/oauth_handlers.go:24` | Max-Age of the HttpOnly aurelia_2fa cookie carrying the TOTP ticket on OAuth logins MaxAge:300 |
| `AURELIA_API_OAUTH_STATE_CACHE_TTL` | `duration` | `10*time.Minute` | `api/oauth_handlers.go:25` | How long a stashed OAuth state (+PKCE verifier/origin) is valid to complete the callback 10*time.Minute; also link flow oauth_handlers.go:367 |
| `AURELIA_API_OAUTH_TOKEN_EXCHANGE_CONTEXT_TIMEOUT` | `duration` | `20*time.Second` | `api/oauth_handlers.go:26` | Deadline covering the provider code->token exchange plus userinfo fetch in the callback context.WithTimeout 20*time.Second |
| `AURELIA_API_OAUTH_CROSS_DOMAIN_HANDOFF_TOKEN_TTL` | `duration` | `60*time.Second` | `api/oauth_handlers.go:27` | Lifetime of the one-time token that hands a completed login back to the origin domain 60*time.Second, single-use |
| `AURELIA_API_2FA_LOGIN_TICKET_BURN_THRESHOLD` | `int64` | `5` | `api/twofa_handlers.go:24` | Wrong TOTP codes allowed against a login ticket before it is burned d.Cache.Incr(...)>=5 |
| `AURELIA_API_ISSUE_TWOFA_TICKET` | `duration` | `5*time.Minute` | `api/twofa_handlers.go:25` | Lifetime of the short-lived ticket that stands in for a verified password until the TOTP code is supplied 5*time.Minute |
| `AURELIA_OAUTH_HTTP_CLIENT` | `duration` | `15*time.Second` | `oauth/oauth.go:64` | Per-request timeout on the shared http.Client for all provider token/userinfo calls http.Client{Timeout:15*time.Second} |
| `AURELIA_OAUTH_OAUTH_PROVIDER_RESPONSE_BODY_CAP` | `int64` | `1<<20` | `oauth/oauth.go:68` | Max bytes read from a provider token/userinfo response body io.LimitReader(...,1<<20); also oauth.go:253 (userinfo) and :291 (github) |
| `AURELIA_OAUTH_APPLE_CLIENT_SECRET_JWT_EXPIRY` | `duration` | `30*time.Minute` | `oauth/oauth.go:71` | exp lifetime of the ES256 JWT minted as Apple's dynamic client secret now.Add(30*time.Minute) |
| `AURELIA_OAUTH_SNIPPET` | `int` | `200` | `oauth/oauth.go:74` | Max characters of a provider error body kept for log/error messages len(s)>200 truncation |


### 7. 上传 / 文件 / 分享

图片处理、存储清理周期、直传分片、分享令牌、下载缓存 TTL 等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_ICON_SERVING_CACHE_AGE` | `duration` | `86400*time.Second` | `api/admin_uploads.go:29` | Hard byte cap for admin model-icon uploads (multipart parse limit + LimitReader cap) ParseMultipartForm uses maxIconBytes+1024 (line 109); LimitReader uses maxIconBytes+1 (line 132). |
| `AURELIA_API_ADMIN_ICON_UPLOAD_SIZE` | `int64` | `256*1024` | `api/admin_uploads.go:60` | Cache-Control public max-age for served model icons — 24 hours |
| `AURELIA_API_AUDIO_TRANSCRIPTION_UPSTREAM_HTTP_TIMEOUT` | `duration` | `120*time.Second` | `api/audio_handlers.go:21` | http.Client.Timeout for the outbound /v1/audio/transcriptions call to the OpenAI-compatible endpoint |
| `AURELIA_API_AUDIO_TRANSCRIPTION_USER_RATE_LIMIT` | `int` | `20` | `api/audio_handlers.go:26` | Max transcription requests per user per rolling window before 429 Window = 1 minute, scope key "audio". |
| `AURELIA_API_TRANSCRIPTION_UPSTREAM_RESPONSE_READ_CAP` | `int64` | `1<<20` | `api/audio_handlers.go:27` | io.LimitReader cap on bytes read from the transcription upstream response body |
| `AURELIA_API_TRANSCRIPTION_UPSTREAM_ERROR_TRUNCATION_LENGTH` | `int` | `240` | `api/audio_handlers.go:28` | Max characters of upstream error body echoed back in the 502 error message |
| `AURELIA_API_UPLOAD_RATE_LIMIT_MAX` | `int` | `20` | `api/files_handlers.go:25` | Upload Rate Limit Max |
| `AURELIA_API_UPLOAD_RATE_LIMIT_WINDOW` | `duration` | `time.Minute` | `api/files_handlers.go:26` | Upload Rate Limit Window |
| `AURELIA_API_ARTIFACT_CACHE_TTL` | `duration` | `31536000*time.Second` | `api/files_handlers.go:27` | Artifact Cache TTL |
| `AURELIA_API_UPLOADED_FILE_CACHE_TTL` | `duration` | `86400*time.Second` | `api/files_handlers.go:28` | Uploaded File Cache TTL |
| `AURELIA_API_OBJECT_STORAGE_DELETE_TIMEOUT_CLEANUP` | `duration` | `30*time.Second` | `api/storage_cleanup.go:17` | context.WithTimeout deadline for each object-storage Delete during unreferenced-storage cleanup |
| `AURELIA_STORAGE_S3_DIRECT_UPLOAD_MIN_CLIENT_TIMEOUT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:28` | Direct-upload / OSS tunables (env-overridable; defaults preserve prior hardcoded behavior). |
| `AURELIA_STORAGE_DIRECT_S3_OSS_UPLOAD_HTTP_CLIENT` | `duration` | `20*time.Minute` | `storage/s3_direct.go:29` | Direct S3 OSS Upload HTTP Client |
| `AURELIA_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_CONNECT` | `int64` | `30` | `storage/s3_direct.go:30` | Aliyun OSS Client Connect Read Timeouts Connect |
| `AURELIA_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_RW` | `int64` | `300` | `storage/s3_direct.go:31` | Aliyun OSS Client Connect Read Timeouts Rw |
| `AURELIA_STORAGE_PRESIGN_URL_TTL` | `duration` | `3600*time.Second` | `storage/s3_direct.go:32` | Presign URL TTL |
| `AURELIA_STORAGE_PRESIGN_URL_TTL_CLAMP_CEILING` | `duration` | `86400*time.Second` | `storage/s3_direct.go:33` | Presign URL TTL Clamp Ceiling |
| `AURELIA_STORAGE_SIDECAR_STORAGE_CLIENT_HTTP_TIMEOUT` | `duration` | `5*time.Minute` | `storage/storage.go:31` | http.Client.Timeout for sidecar-backed /storage/put and /storage/delete round-trips (sized for ~200 MB MinerU PDFs) Sidecar Put default TTL (1h) / cap (24h) are enforced sidecar-side, not in this Go code. |


### 8. 管理后台任务（备份 / 向量维护 / 兑换码）

备份大小上限与异步轮询、向量维护批量、兑换码上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_API_BACKUP_EXPORT_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_backup_async.go:26` | max number of recent backup-export job records kept in the in-memory manager before oldest are evicted for len(m.order) > 20 { evict } |
| `AURELIA_API_BACKUP_EXPORT_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_backup_async.go:27` | context deadline for a full async backup-export job (DB read + zip + qdrant dump) context.WithTimeout(context.Background(), 12*time.Hour) |
| `AURELIA_API_CONFIG_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `16<<20` | `api/admin_backup_handlers.go:25` | Tunable knobs — envcfg overrides; defaults preserve original behaviour. |
| `AURELIA_API_BACKUP_IMPORT_MULTIPART_MEMORY_BUFFER` | `int64` | `32<<20` | `api/admin_backup_handlers.go:26` | Backup Import Multipart Memory Buffer |
| `AURELIA_API_MAX_CONFIG_SIZE` | `int64` | `512<<20` | `api/admin_backup_handlers.go:373` | 512 MiB; config archives are normally tiny. |
| `AURELIA_API_QDRANT_ARCHIVE_REQUEST_TIMEOUT` | `duration` | `5*time.Minute` | `api/admin_backup_qdrant.go:26` | http.Client.Timeout for every Qdrant backup/restore request (list/scroll/upsert/index) const qdrantArchiveRequestTimeout = 5 * time.Minute; used at line 58 |
| `AURELIA_API_QDRANT_ERROR_BODY_READ_CAP` | `int64` | `1<<20` | `api/admin_backup_qdrant.go:27` | LimitReader cap when reading a non-2xx Qdrant error response body for the error message io.LimitReader(resp.Body, 1<<20) |
| `AURELIA_API_QDRANT_EXPORT_SCROLL_PAGE_SIZE` | `int` | `256` | `api/admin_backup_qdrant.go:28` | points fetched per /points/scroll page when exporting a Qdrant collection to the archive body{"limit": 256} |
| `AURELIA_API_QDRANT_IMPORT_UPSERT_FLUSH_BATCH_SIZE` | `int` | `128` | `api/admin_backup_qdrant.go:29` | points accumulated before flushing a /points?wait=true upsert during Qdrant restore if len(batch) >= 128 { flush }; batch initial cap also 128 at line 315 |
| `AURELIA_API_ADMIN_USER_LIST_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:20` | default page size for the admin users list; clamped to a max of 200 if limit<=0 {limit=50}; if limit>200 {limit=200} (lines 464-469) |
| `AURELIA_API_ADMIN_CREATED_USER_MIN_PASSWORD_LENGTH` | `int` | `8` | `api/admin_handlers.go:21` | minimum password character length when an admin provisions a new account if len(req.Password) < 8 { reject } |
| `AURELIA_API_ADMIN_PASSWORD_RESET_MIN_LENGTH` | `int` | `8` | `api/admin_handlers.go:22` | minimum length for an admin-set user password reset if len(req.NewPassword) < 8 { reject } |
| `AURELIA_API_ADMIN_USER_CONVERSATIONS_LISTING_CAP` | `int` | `500` | `api/admin_handlers.go:23` | hardcoded row limit when listing a target user's conversations for support/triage store.ListConversations(..., 500, 0) |
| `AURELIA_API_USAGE_REPORT_PAGE_SIZE_CAP` | `int` | `50` | `api/admin_handlers.go:24` | default page size for the admin usage report; clamped to a max of 200 if pageSize<=0 \|\| pageSize>200 { pageSize=50 } |
| `AURELIA_API_ANALYTICS_WINDOW` | `int` | `30` | `api/admin_handlers.go:25` | default look-back window (days) for the admin analytics dashboard days := 30 |
| `AURELIA_API_ANALYTICS_WINDOW_2` | `int` | `365` | `api/admin_handlers.go:26` | upper bound on the ?days analytics look-back window n > 0 && n <= 365 |
| `AURELIA_API_ANALYTICS_BREAKDOWN_TOP_N` | `int` | `8` | `api/admin_handlers.go:27` | number of top models and top users included in analytics breakdown + series AdminUsageBreakdown(..., 8) for model_id (line 912) and user_id (line 913) |
| `AURELIA_API_BULK_REDEEM_CODE_GENERATION_QUANTITY` | `int` | `1000` | `api/admin_redeem_handlers.go:18` | upper bound on codes generated in one bulk redeem-code create request if body.Quantity < 0 \|\| body.Quantity > 1000 { reject } |
| `AURELIA_API_MAX_SKILL_ASSET_BYTES` | `int64` | `20*1024*1024` | `api/admin_skill_assets.go:24` | per-file byte cap for an admin skill-asset upload (also drives multipart buffer +4096 and LimitReader +1) const maxSkillAssetBytes = 20 * 1024 * 1024; no env/admin override (separate from admin max_file_upload_mb which does not apply here) |
| `AURELIA_API_VECTOR_MAINTENANCE_JOB_HISTORY_RETENTION` | `int` | `20` | `api/admin_vectors_handlers.go:18` | max recent vector check/rebuild job records kept in memory before oldest evicted for len(m.order) > 20 { evict } |
| `AURELIA_API_VECTOR_MAINTENANCE_JOB_RUNTIME` | `duration` | `12*time.Hour` | `api/admin_vectors_handlers.go:19` | context deadline for an async vector audit/rebuild job context.WithTimeout(context.Background(), 12*time.Hour) |


### 9. 数据库 / 缓存 / 后台队列

数据库连接池、设置缓存 TTL、Redis 超时与连接池、后台任务队列容量与并发等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_CACHE_MEMORY_PUB_SUB_SUBSCRIBER_CHANNEL_BUFFER` | `int` | `16` | `cache/cache.go:15` | Buffered capacity of each Subscribe channel in the dev in-memory cache; non-blocking Publish drops when full. |
| `AURELIA_CACHE_MEMORY_STREAM_EVENT_RETENTION_CAP` | `int` | `50000` | `cache/cache.go:16` | Max events kept per in-memory stream; older events trimmed to the last 50k on append (eviction bound). Same 50000 literal reused at line 238 for the slice trim. Mirrors redis MaxLen (redis.go:169). |
| `AURELIA_CACHE_MEMORY_STREAMREAD_PAGE_LIMIT` | `int` | `100` | `cache/cache.go:17` | Fallback number of stream events returned by StreamRead when caller passes limit<=0. Mirrors redis.go:183. |
| `AURELIA_CACHE_REDIS_OPERATION_COMMAND_TIMEOUT` | `duration` | `3*time.Second` | `cache/redis.go:20` | Context deadline for the single Ping issued at NewRedis so a misconfigured/unreachable Redis fails fast at startup. Only the dial URL comes from REDIS_URL (env, via redis.ParseURL); this ping deadline literal has no env/admin override. |
| `AURELIA_CACHE_REDIS_STARTUP_PING_TIMEOUT` | `duration` | `5*time.Second` | `cache/redis.go:35` | context.WithTimeout applied to every Redis command (Get/Set/Delete/Incr/IncrBy/Decr/Publish/XAdd/XRangeN). Same 3s literal repeated at lines 40,50,56,65,80,98,113,164,185. No env/admin override; go-redis Read/Write/Dial/PoolSize come only from the REDIS_URL query string, none hardcoded here. |
| `AURELIA_CACHE_REDIS_PUB_SUB_SUBSCRIBER_CHANNEL_BUFFER` | `int` | `16` | `cache/redis.go:128` | Buffered capacity of the per-subscriber out channel bridging Redis pub/sub; slow consumers drop messages past this depth. Matches the in-memory impl (cache.go:198). |
| `AURELIA_CACHE_REDIS_GENERATION_STREAM_MAXLEN_CAP` | `int64` | `50000` | `cache/redis.go:174` | XADD MaxLen (Approx=true) — caps a generation replay stream at ~50k events in Redis. Mirrors the in-memory cap in cache.go:237. No override. |
| `AURELIA_CACHE_REDIS_STREAMREAD_PAGE_LIMIT` | `int` | `100` | `cache/redis.go:188` | Fallback number of stream events returned by StreamRead when caller passes limit<=0. Mirrors cache.go:247. |
| `AURELIA_STORE_LISTCONVERSATIONS_LIMIT` | `int` | `200` | `store/conversations.go:17` | Default page size when limit<=0 |
| `AURELIA_STORE_LISTCONVERSATIONS_LIMIT_2` | `int` | `500` | `store/conversations.go:18` | Upper clamp on page size |
| `AURELIA_STORE_LISTWORKSPACECONVERSATIONS_LIMIT` | `int` | `200` | `store/conversations.go:19` | Default page size for workspace conversation listing |
| `AURELIA_STORE_LISTWORKSPACECONVERSATIONS_LIMIT_2` | `int` | `500` | `store/conversations.go:20` | Upper clamp on workspace listing page size |
| `AURELIA_STORE_M_CONFIDENCE` | `float` | `0.8` | `store/misc.go:18` | Default confidence assigned to a new memory when unset |
| `AURELIA_STORE_LIST_MEMORIES_ACTIVE` | `int` | `20` | `store/misc.go:19` | Max ACTIVE/QUERY_DEPENDENT memories injected into the system prompt (LIMIT 20) |
| `AURELIA_STORE_ADMIN_USAGE_RECORDS_LIMIT` | `int` | `500` | `store/misc.go:20` | Upper bound above which page limit resets to default |
| `AURELIA_STORE_ADMIN_USAGE_RECORDS_LIMIT_2` | `int` | `50` | `store/misc.go:21` | Default page size for AdminUsageRecords when limit invalid/too large |
| `AURELIA_STORE_USAGE_TREND_WINDOW` | `int` | `7` | `store/misc.go:22` | Default lookback window for AdminUsageTrend |
| `AURELIA_STORE_USAGE_TREND_HOURLY_BUCKET_THRESHOLD` | `int` | `2` | `store/misc.go:23` | Window (days) at/below which trend switches to hourly buckets |
| `AURELIA_STORE_USAGE_TOTALS_WINDOW` | `int` | `7` | `store/misc.go:24` | Default lookback window for AdminUsageTotals |
| `AURELIA_STORE_USAGE_BREAKDOWN_TOP_N` | `int` | `8` | `store/misc.go:25` | Default number of top keys returned by AdminUsageBreakdown |
| `AURELIA_STORE_USAGE_BREAKDOWN_WINDOW` | `int` | `7` | `store/misc.go:26` | Default lookback window for AdminUsageBreakdown |
| `AURELIA_STORE_USAGE_SERIES_WINDOW` | `int` | `7` | `store/misc.go:27` | Default lookback window for AdminUsageSeries |
| `AURELIA_STORE_PS` | `int` | `604800` | `store/quotas.go:11` | Per-(model,group) quota window applied when an admin leaves period_seconds <= 0 (defaults to 7 days). period_seconds is admin-configurable per quota row; this is only the fallback when unset. |
| `AURELIA_STORE_REDEEM_CODE_UNIQUENESS_RETRIES` | `int` | `5` | `store/redeem_codes.go:26` | Attempts to generate a collision-free redeem code error message at :144 |
| `AURELIA_STORE_SEARCH_SNIPPET_RADIUS` | `int` | `64` | `store/search.go:14` | Rows fetched per keyset page during the one-time messages.search_text backfill. |
| `AURELIA_STORE_BATCH` | `int` | `500` | `store/search.go:47` | Runes of context kept on each side of a content-search match when building the result snippet. Passed as radius arg to buildSnippet; window is ~2x64 runes. |
| `AURELIA_STORE_SETTINGS_CACHE_TTL` | `duration` | `15*time.Second` | `store/settings_cache.go:17` | How long a process-local GetSetting result (config/model/quota reads) is cached before re-hitting the DB; cross-instance invalidation via Pub/Sub still applies. One of the hottest reads in the server; only invalidated on writes or cfg:invalidate. |
| `AURELIA_STORE_SET_MAX_OPEN_CONNS` | `int` | `20` | `store/store.go:35` | Connection pool max open conns for Postgres |
| `AURELIA_STORE_SET_MAX_IDLE_CONNS` | `int` | `10` | `store/store.go:36` | Connection pool max idle conns for Postgres |
| `AURELIA_STORE_SET_CONN_MAX_IDLE_TIME` | `duration` | `5*time.Minute` | `store/store.go:37` | Idle connection lifetime before closing (Postgres pool) |
| `AURELIA_STORE_SET_CONN_MAX_LIFETIME` | `duration` | `time.Hour` | `store/store.go:38` | Max lifetime of a pooled connection (Postgres) |
| `AURELIA_STORE_LIMIT_DEFAULT` | `int` | `200` | `store/users.go:23` | Env-overridable pagination defaults/caps (see docs/config-reference.md); each falls back to the original hardcoded value when its AURELIA_* var is unset. |
| `AURELIA_STORE_LIMIT_MAX` | `int` | `500` | `store/users.go:24` | Limit Max |
| `AURELIA_STORE_LIMIT_2_DEFAULT` | `int` | `50` | `store/users.go:25` | Limit 2 Default |
| `AURELIA_STORE_LIMIT_2_MAX` | `int` | `200` | `store/users.go:26` | Limit 2 Max |
| `AURELIA_STORE_BATCH_2` | `int` | `500` | `store/users.go:267` | Batch 2 |


### 10. 邮件 / SMTP

SMTP 拨号 / 发送超时等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_MAIL_SMTP_DIAL_TIMEOUT` | `duration` | `10*time.Second` | `mail/mail.go:27` | TCP connect timeout for the SMTP server so a wrong port fails fast. net.Dialer{Timeout: 10*time.Second}. |
| `AURELIA_MAIL_DEADLINE` | `duration` | `25*time.Second` | `mail/mail.go:28` | Overall connection deadline covering the full SMTP handshake+send (SetDeadline). time.Now().Add(25*time.Second); guards against TLS-mode mismatch hangs. |


### 11. 服务器启动 / 配置加载

HTTP server 超时、优雅关闭、启动流程常量等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `AURELIA_CMD_ARCHIVE_GC_BOOT_SETTLE_DELAY` | `duration` | `2*time.Minute` | `cmd/api/main.go:40` | delay after boot before the first archived-workspace GC sweep |
| `AURELIA_CMD_RUN_PRUNE` | `duration` | `5*time.Minute` | `cmd/api/main.go:41` | deadline for one PruneArchives sweep against object storage |
| `AURELIA_CMD_ARCHIVE_GC_SWEEP_INTERVAL` | `duration` | `6*time.Hour` | `cmd/api/main.go:42` | interval between archived-workspace GC sweeps |
| `AURELIA_CMD_HTTP_SERVER` | `duration` | `15*time.Second` | `cmd/api/main.go:43` | http.Server ReadHeaderTimeout |
| `AURELIA_CMD_HTTP_SERVER_2` | `duration` | `90*time.Minute` | `cmd/api/main.go:44` | http.Server WriteTimeout, sized for long-lived SSE streams comment says 30 minutes but literal is 90m |
| `AURELIA_CMD_HTTP_SERVER_3` | `duration` | `120*time.Second` | `cmd/api/main.go:45` | http.Server IdleTimeout for keep-alive connections |
| `AURELIA_CMD_GRACEFUL_SHUTDOWN_TIMEOUT` | `duration` | `10*time.Second` | `cmd/api/main.go:46` | deadline for srv.Shutdown on SIGINT/SIGTERM |
| `AURELIA_CMD_TASK_ROUTER_ADAPTER_RUN_JSON` | `int` | `256` | `cmd/api/main.go:47` | MaxOutputTokens for RAG TaskRouter JSON calls (router/summariser) via TaskLLM |


### 12. 前端

**编译期**生效（Vite 在 `npm run build` 时内联 `VITE_*`），需要在构建环境设置，运行时改环境变量无效。轮询间隔、分页、重试 / 退避、去抖、超时、客户端大小上限等。

| 环境变量 | 类型 | 默认值 | 位置 | 说明 |
| --- | --- | --- | --- | --- |
| `VITE_AURELIA_MAX_SSE_RETRIES` | `envNum` | `3` | `src/api/client.ts:339` | Max SSE Retries |
| `VITE_AURELIA_DELAY_BASE` | `envNum` | `1000` | `src/api/client.ts:341` | Exponential SSE reconnect backoff: delay = SSE_RECONNECT_BACKOFF_BASE_MS * factor^(retryCount - 1). |
| `VITE_AURELIA_DELAY_FACTOR` | `envNum` | `2` | `src/api/client.ts:342` | Delay Factor |
| `VITE_AURELIA_IMAGE_API_MY_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:181` | Image API My Images Limit |
| `VITE_AURELIA_IMAGE_API_MY_IMAGES_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:182` | Image API My Images Offset |
| `VITE_AURELIA_WORKSPACES_API_ADMIN_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:289` | Workspaces API Admin List Limit |
| `VITE_AURELIA_WORKSPACES_API_ADMIN_LIST_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:290` | Workspaces API Admin List Offset |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:318` | Conversations API List Limit |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:319` | Conversations API List Offset |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_ARCHIVED_LIMIT` | `envNum` | `200` | `src/api/endpoints.ts:326` | Conversations API List Archived Limit |
| `VITE_AURELIA_CONVERSATIONS_API_LIST_ARCHIVED_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:327` | Conversations API List Archived Offset |
| `VITE_AURELIA_ADMIN_API_USERS_SEARCH` | `envStr` | `''` | `src/api/endpoints.ts:593` | Admin API Users Search |
| `VITE_AURELIA_ADMIN_API_USERS_LIMIT` | `envNum` | `50` | `src/api/endpoints.ts:594` | Admin API Users Limit |
| `VITE_AURELIA_ADMIN_API_USERS_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:595` | Admin API Users Offset |
| `VITE_AURELIA_ADMIN_API_USER_IMAGES_LIMIT` | `envNum` | `60` | `src/api/endpoints.ts:626` | Admin API User Images Limit |
| `VITE_AURELIA_ADMIN_API_USER_IMAGES_OFFSET` | `envNum` | `0` | `src/api/endpoints.ts:627` | Admin API User Images Offset |
| `VITE_AURELIA_ADMIN_API_ANALYTICS` | `envNum` | `30` | `src/api/endpoints.ts:674` | Admin API Analytics |
| `VITE_AURELIA_MAX_BYTES` | `envNum` | `256 * 1024` | `src/components/admin/icon-uploader.tsx:34` | Client-side max size for admin icon uploads (mirrors backend maxIconBytes) Mirrors backend admin_uploads.go; no env/admin key |
| `VITE_AURELIA_MAX_LEN` | `envNum` | `12_000` | `src/components/chat/composer.tsx:99` | Max composer message text length before send is blocked |
| `VITE_AURELIA_INGEST_POLL_MS` | `envNum` | `1200` | `src/components/chat/composer.tsx:113` | Conversation-doc ingest status poll cadence |
| `VITE_AURELIA_CONVERSATION_OUTLINE_TREE_REFETCH_BACKOFF` | `envNum` | `200` | `src/components/chat/conversation-outline.tsx:32` | Linear backoff (200ms * retryCount) before refetching the branch tree when it looks incomplete retriesRef has no visible cap |
| `VITE_AURELIA_INTERVAL_MS` | `envNum` | `50` | `src/components/chat/markdown.tsx:53` | Wall-clock floor between markdown re-parses during token streaming (on top of useDeferredValue) Throttle interval; final value flushed verbatim |
| `VITE_AURELIA_INITIAL_WINDOW` | `envNum` | `24` | `src/components/chat/message-list.tsx:26` | Number of latest turns mounted on first paint of a long transcript |
| `VITE_AURELIA_BATCH` | `envNum` | `24` | `src/components/chat/message-list.tsx:27` | Additional turns revealed per scroll-toward-top step |
| `VITE_AURELIA_PAGE` | `envNum` | `30` | `src/components/chat/my-gallery.tsx:12` | Images fetched per page in the user image gallery infinite scroll |
| `VITE_AURELIA_RUN_TIMEOUT_MS` | `envNum` | `120_000` | `src/lib/pyodide-runner.ts:39` | Hard cap per in-browser Python run before it is aborted (mirrors sandbox exec ceiling) Mirrors §4.5 sandbox exec cap (server SANDBOX_DEFAULT_EXEC_TIMEOUT_MS=120000) but is a separate frontend literal |
| `VITE_AURELIA_MAX_STREAM_CHARS` | `envNum` | `200_000` | `src/lib/pyodide-runner.ts:41` | Max streamed print output chars before truncation to stop runaway loops |
| `VITE_AURELIA_MAX_RESULT_CHARS` | `envNum` | `20_000` | `src/lib/pyodide-runner.ts:43` | Max chars of the final expression repr() shown |
| `VITE_AURELIA_DEFAULT_MAX_DIM` | `envNum` | `1280` | `src/lib/resize-image.ts:11` | Default Max Dim |
| `VITE_AURELIA_DEFAULT_MAX_BYTES` | `envNum` | `240 * 1024` | `src/lib/resize-image.ts:12` | headroom under the server's 256 KiB cap |
| `VITE_AURELIA_QUALITY_START` | `envNum` | `0.9` | `src/lib/resize-image.ts:13` | Quality Start |
| `VITE_AURELIA_QUALITY_FLOOR` | `envNum` | `0.4` | `src/lib/resize-image.ts:14` | Quality Floor |
| `VITE_AURELIA_QUALITY_STEP` | `envNum` | `0.12` | `src/lib/resize-image.ts:15` | Quality Step |
| `VITE_AURELIA_MAX_SHIKI_CODE_LENGTH` | `envNum` | `200_000` | `src/lib/syntax/shiki-client.ts:42` | Code longer than this is not syntax-highlighted (falls back to plain) |
| `VITE_AURELIA_FINAL_RENDER_TIMEOUT_MS` | `envNum` | `15_000` | `src/lib/syntax/shiki-client.ts:43` | Timeout awaiting the shiki web-worker for a final (non-streaming) highlight |
| `VITE_AURELIA_CACHE_LIMIT` | `envNum` | `160` | `src/lib/syntax/shiki-client.ts:44` | Max entries in the in-memory highlight result LRU cache |
| `VITE_AURELIA_ADMIN_BACKUP_EXPORT_JOB_POLL_INTERVAL` | `envNum` | `2500` | `src/pages/admin/AdminBackup.tsx:50` | Admin Backup Export Job Poll Interval |
| `VITE_AURELIA_PAGE_SIZE_2` | `envNum` | `20` | `src/pages/admin/AdminRedeemCodes.tsx:80` | Rows per page in the admin redeem-codes table (client-side slice) |
| `VITE_AURELIA_PAGE_SIZE` | `envNum` | `50` | `src/pages/admin/AdminUsage.tsx:33` | Rows per page in the admin usage/log table |
| `VITE_AURELIA_IMAGES_PAGE` | `envNum` | `60` | `src/pages/admin/AdminUserLibrary.tsx:29` | Images fetched per page in the admin per-user library |
| `VITE_AURELIA_ONLINE_WINDOW_S` | `envNum` | `300` | `src/pages/admin/AdminUsers.tsx:42` | A user counts as online if last_seen is within this many seconds of now |
| `VITE_AURELIA_PAGE_SIZE_3` | `envNum` | `50` | `src/pages/admin/AdminUsers.tsx:83` | Rows per page in the admin users table |
| `VITE_AURELIA_KB_DOC_STATUS_POLL_INTERVAL` | `envNum` | `2200` | `src/pages/kb/KnowledgeBaseDetail.tsx:41` | setInterval poll cadence while any document is pending/parsing/embedding |
| `VITE_AURELIA_CONV_PAGE` | `envNum` | `200` | `src/store/conversations.ts:52` | Conversations fetched per sidebar list page (initial + loadMore infinite scroll) Comment says kept at server default; still a frontend literal passed as the limit param |
| `VITE_AURELIA_MSG_PAGE` | `envNum` | `40` | `src/store/conversations.ts:66` | Latest messages loaded when opening a conversation; older pages fetched on scroll-up Kept a bit above the render window INITIAL_WINDOW=24 |

