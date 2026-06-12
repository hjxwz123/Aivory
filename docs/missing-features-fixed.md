# Missing features — what was fixed this audit pass

> 每条 = 一项 design.md 需求的修复记录。按发现顺序列出。所有改动都已合入主分支。

---

## §2.3-G 模型级可调参数 — ❌→✅

### 问题
- 数据库的 `models.param_controls` 字段已存在,管理后台编辑器能写 JSON,但
  - 前端**不渲染**任何控件,用户无法切换"深度思考"等选项
  - 后端**不合并**任何 fragment 到上游请求 body,选了也没用
- 这是 design.md 设计书里专门花一节(§2.3-G)讲的核心机制,缺失等于阉割了模型差异化能力

### 修复
**后端**:`server/internal/llm/param_controls.go` 新增 `MergeParamControls`
- 按 declared key 白名单接收前端选值,未声明的 key 一律丢弃
- toggle: 只接受 `true`/`false`,映射到 `map.on`/`map.off`
- select: 只接受 declared `options[].value`,映射到 `map[value]`
- 深合并: object 递归,array/scalar 替换
- 三 provider 都在拼装请求 body 后调用一次 `MergeParamControls(body, req.ParamControls, req.ParamOverrides)`

**前端**:`src/components/chat/param-controls.tsx` 新增 `<ParamControls>` 组件
- 按当前模型的 `param_controls` 渲染 toggle / select
- 支持 `show_if` 条件显示
- 支持每个控件 + 选项的 `icon`(lucide-react 动态导入)
- 发送时调 `filterVisibleParams` 剥掉被 `show_if` 隐藏的 key,确保只把可见值上报
- Composer 集成: 切换模型时 reset 选值,发送时把 `params` 透传给 `sendMessage`

### 验证
管理后台给某个模型配 `param_controls` JSON,回首页打开对话,Composer 上方应出现对应控件;发条消息后看后端 log 应见请求 body 里出现 map fragment。

---

## §2.3-F 任务模型 TaskLLM helper — ❌→✅

### 问题
- design.md §2.3-F 明确要求**所有内部 LLM 调用统一走管理员配的"任务模型"**(标题/RAG 路由/压缩摘要/记忆抽取/跨厂商降级)
- 当前代码每处都自己拼字符串,标题用 `summariseTitle` 截前 28 字,根本不调任何模型;RAG 路由不存在;摘要不存在;记忆不存在
- 即使将来想接 Haiku/Flash 也无统一入口

### 修复
新增 `server/internal/llm/task_llm.go`:
- `TaskKind` 枚举: TaskTitle / TaskRouter / TaskCompact / TaskMemoryExtract / TaskMemoryAdjudicate / TaskDowngrade
- `TaskLLM.Run(ctx, kind, prompt, opts)` 统一入口
- `TaskLLM.RunJSON(ctx, kind, prompt, &out, opts)` 强制 JSON 输出 + 解析
- `RunJSONString` adapter 给 rag 包用(避免 import 循环)
- 内部默认 system prompt 按 kind 切换(标题用"3-8 word title"风格、router 用 JSON schema 风格,等)
- 每次调用自动 `LogUsage` 写一行,purpose 写 `task.title` / `task.router` 等,管理后台用量报表可分别看
- 模型解析: 先读 `settings.task_model_id`,缺则降级到 `default_model_id`
- 调用时不挂任何工具(`noopToolRunner` 直接报错防止 task 模型偷调工具)

mock provider 也加了 `fastTaskReply` — 检测 `MaxOutputTokens <= 256` 时按 system prompt 关键词产出合理短输出(title/JSON router/summary/memory),让本地零 Key 也能完整跑通流程。

### 验证
在 /admin/settings 把 task_model_id 设为 mock 模型 → 发新对话首条消息 → 看 conversation.title 异步变成真生成的短标题(而不是头 28 字截断)。

---

## §4.13 prompt-mode 工具协议 — ❌→✅

### 问题
- design.md §4.13 详细写了"非原生 function calling 模型走文本协议"的完整设计
- 当前代码: `ToolModePrompt` 字段在 UnifiedChatRequest 里有,**provider 里完全没读这个字段**
  - 不注入工具清单
  - 不设 stop_sequences
  - 不解析 `<tool_call>` 标记
  - 不做循环
- 影响: 任何 `tool_mode=prompt` 的模型工具完全失效(对开源模型 / 老模型不友好)

### 修复
新增 `server/internal/llm/prompt_tools.go`:
- `PromptToolPreamble(tools)` — 生成插入 system 末尾的工具清单 + 协议说明
- `PromptToolStopSequence()` — `</tool_call>`,三 provider 都在 ToolModePrompt 时挂这个
- `ParsePromptToolCall(text)` — 容错解析(允许闭合标签缺失)
- `SplitTextAndCall(text)` — 把"工具调用前可见文本"和"调用本身"分开,前者照常 SSE 转发
- `RunPromptToolLoop(...)` — 一站式循环,JSON 解析失败重试 2 次,循环上限 6 轮(低于 native 12)
- 全部归一化为 UnifiedBlock + tool_call/tool_result SSE 事件,前端无感

三 provider 都接入: Anthropic `stop_sequences`、OpenAI `stop`、Gemini `generationConfig.stopSequences`。

### 验证
在 /admin/models 编辑模型把 `tool_mode` 改为 `prompt`,system_prompt 加少量内容 → 发个"搜一下今天天气"试试 → 模型应回吐 `<tool_call>{"name":"web_search",...}</tool_call>`,被 stop 截停,后端解析执行,结果以 `<tool_result>` 回灌继续。

---

## §4.7 长上下文压缩 — ❌→🟡

### 问题
- design.md §4.7 用一整节讲滑动窗口 + 滚动摘要 + 锚定到节点的设计
- 当前代码: `conversations.summary_blocks` JSON 列存在,但**没有任何代码写它**;orchestrator 也从不读它

### 修复
新增 `server/internal/llm/compaction.go`:
- `MaybeCompact(ctx, db, task, conv, history)` 在 orchestrator 拼装请求前调用
- 按 `settings.keep_recent_rounds`(默认 6)切水位线,挪出窗口的旧 N 轮压缩
- 用 TaskLLM(taskType="compact")生成 ≤300 token 摘要;TaskLLM 不可用时降级用 `clipOlder` 取前 300 词作 fallback,绝不 crash
- 摘要块写回 `conversations.summary_blocks`,带 `anchor_message_id` 和 `from_message_id` 锚点
- 组装时调 `ApplySummaryBlocks(blocks)` 拼到 system prompt 末尾"Earlier conversation (summarised)"
- DB 永远存全文 — 用户翻历史看到的不受影响

### 状态: 🟡 不是完整 ✅
- Tier 1 的分层合并(摘要块超预算时合并最老的几块)未做 — 短期影响小,长会话会变长但不溢出
- 每块只摘一次的不变量已守住,杜绝"摘要的摘要"退化

### 验证
跑一段对话 > 6 轮 → 看 conversations.summary_blocks 不再是 `[]` → 再发条消息时 system prompt 里应出现 "## Earlier conversation (summarised)" 段。

---

## §4.16 记忆抽取 worker — ❌→🟡

### 问题
- design.md 用一整节描述异步抽取 + 状态裁决,要求**不阻塞回答路径**
- 当前代码: memories 表 + 用户管理页 + save_memory 工具都有,但**自动抽取**完全没做
- 用户每天都要手动添加记忆 = 这是反 §4.16 的核心设计原则

### 修复
新增 `server/internal/llm/memory_worker.go`:
- `MemoryWorker.Process(ctx, convID)` 在每轮回答完毕后 `queue.Enqueue("memory.process", ...)` 异步触发
- 读取最近 20 条对话消息 → TaskLLM (taskType="memory_extract") 抽取候选(JSON 数组)
- Tier 0 裁决: 按 slot 直替换 — 同 slot 的旧 ACTIVE → STALE,新的 ACTIVE
- 没有 slot 的就直接 append(避免 Tier 1 的语义传播误杀)
- 全程异步,失败 swallow,绝不影响用户回答速度

注入侧 (`orchestrator.composeSystemPrompt`):
- 拉 `store.ListMemoriesActive(userID)`(只取 ACTIVE + QUERY_DEPENDENT,limit 20)
- 在 system prompt §⑤ 段注入,带 [CURRENT] / [CONTEXT-DEPENDENT] 标签
- 回答模型 in-context 自己裁决,不再单独跑"记忆过滤"模型 — 这就是 design.md 写的产品化取舍

### 状态: 🟡 不是完整 ✅
- Tier 1 的语义传播冲突(腿伤影响骑车)未做 — 是 best-effort 增强,Tier 0 已覆盖最常见场景

### 验证
聊一段"我搬到东京了,我喜欢拉面" → 等几秒 → 打开 /memory 应看到至少一条记忆出现 → 再发"我住在哪" → 助手应能回答东京。

---

## §6.3 标题自动生成 — 🟠→✅

### 问题
- design.md 明确要求"用任务模型异步生成标题"
- 当前代码: `summariseTitle` 头 28 字截取,根本不是模型生成

### 修复
orchestrator 在第一轮(`shouldGenerateTitle`)调用 `scheduleTitle`:
1. 立即用 `clipTitle` 设一个 placeholder 让 sidebar 马上更新
2. 异步 `queue.Enqueue("title.generate", ...)` → `TaskLLM.Run(TaskTitle, userText)`
3. 拿到结果 `cleanTitle`(去引号、单行、≤40 char) → `UpdateConversation`

mock provider 的 `fastTaskReply` 识别 "conversation title" 系统提示后,取 user 消息前 6 个词作为标题,本地零 Key 也能验证。

### 验证
新建对话发"今天我去东京塔" → 立即看到 sidebar 显示"今天我去东京塔",几秒后变成"东京塔之行"(或类似的真生成标题)。

---

## §4.11-B 查询路由 — 🟡→✅

### 问题
- design.md 用一节描述查询路由(意图分类 + 改写)— 大文档时一次廉价 LLM 决定 retrieve/full_doc/none
- 当前 RAG 只有一个 `Retrieve(query)` 直接做向量检索,没有任何意图分类

### 修复
`rag/rag.go` 新增 `RouteAndRetrieve(ctx, userID, convID, kbIDs, userText, history, topK)`:
- 调 `s.task.RunJSON("task.router", ...)` 得 `{strategy, queries[]}`
- `strategy=none` → 不检索,完全省调用
- `strategy=full_doc` → 用原文 query 检索 topK*2 作 sample
- `strategy=retrieve` → 对每个 rewritten query 检索 + dedupe + 截 topK
- TaskLLM 不可用时默认走 `retrieve` 用原文 query(最安全的兜底)

mock provider 的 `fastTaskReply` 识别 "strategy" 系统提示后产出合理 JSON router 输出。

接口设计: 用 `rag.TaskRouter` 接口接 `internal/llm.TaskLLM`(避免 import 循环),`main.go` 用 `taskRouterAdapter` 桥接。

### 验证
绑定一个 KB 给会话 → 问"这文档主要讲什么"(应触发 `full_doc`)→ 问"第 3 页表格里的数字" (应触发 `retrieve` + 改写查询)→ 看后端 log 应见 router 输出。

---

## §4.8 system prompt 六段组装 — 🟡→✅

### 问题
- design.md §4.8 要求按 ①模型 → ②工具准则 → ③技能索引 → ④项目指令 → ⑤记忆 → ⑥可用文档 严格顺序拼装
- 当前 `composeSystemPrompt` 只拼了 ①+ 项目 + RAG,缺技能索引、缺记忆,顺序也乱

### 修复
重写 `composeSystemPrompt` 用 `systemPromptOpts` 结构入参,严格按文档顺序输出:
```
# ① model.system_prompt
# ② tool guidance
# ③ skill index (use_skill name + when)
# ④ project instructions
# ⑤ memories ([CURRENT] / [CONTEXT-DEPENDENT])
# ⑥ available documents
# (then) summary blocks
# (last) RAG snippets — closest to user message
```

稳定前缀利于前缀缓存(§4.9)。

### 验证
建项目 + KB + 加记忆 + 给模型勾选技能 → 发条消息 → 在后端打 system prompt 日志看顺序是否符合上面的 layout。

---

## §4.15 分支切换 UI — 🟠→✅

### 问题
- 后端 `setActiveLeafHandler` + `forkConversationHandler` + `regenerateHandler` 都 OK
- 前端只在 message-row 显示 `1/3` 这种文字 badge,**没有左右箭头**,用户无法切换分支

### 修复
新增 `BranchSwitcher` 子组件(`message-row.tsx` 末尾):
- 渲染 `<ChevronLeft>` `1/3` `<ChevronRight>` 真实按钮
- 点击调 `onSwitch(targetMessageId)` → message-list 接力到 store.setActiveLeaf → 后端 PATCH /active-leaf
- 后端返回新的路径 + sibling 信息,store 重新更新 conversations
- API 已经在 messages_handlers.go 用 `enrichWithSiblings` 把 siblings ID 数组挂在每条消息上 — frontend 现在真的用了

`Message.siblings` 字段从 ApiMessage.siblings 透传过来。

### 验证
对一条助手消息点"重新生成"→ 出现 `1/2`,点 `<` 应回到原回答,点 `>` 应到新回答。

---

## §4.6 真实文件上传 — 🟠→✅

### 问题
- 后端 `POST /api/files` 已存在并 OK
- 前端 composer 选了文件只在本地 state 里塞一条 chip,**根本没调 /api/files**
- 发消息时 attachment 只有本地 uid 和 filename,后端啥都拿不到

### 修复
`composer.tsx` 重写 `handleAttach`:
- 立即在本地塞一个 `uploading: true` 的 chip(乐观 UI)
- 并发调 `api('/files?conversation_id=...', { method: 'POST', body: FormData })`
- 拿到返回的 `id` 后用真 id 替换本地 uid
- 上传期间 send 按钮禁用并显示 spinner
- 失败移除 chip + toast error

`Composer.onSubmit` signature 改为 `(text, attachments, options: { mode?, params? })` 让上层可以传 params(§2.3-G);上层 ChatHome / ChatThread 已对应改造。

### 验证
传 PDF → 看到 spinner 几百毫秒 → 替换为真 file_id chip → 点发送 → 后端 messages 表里 attachments 字段应包含真 file_id。

---

## §8.3 task / image / embedding usage — 🟠→✅

### 问题
- design.md §8.3 要求 task/image/embedding 都按 purpose 写一行 usage_logs
- 当前只 chat 写,task 模型调用全部不记账 → 管理后台用量报表看不到 task 成本

### 修复
TaskLLM.Run 拿到 result 后无条件 `LogUsage(... Purpose: string(kind) ...)`,kind 是 `task.title`/`task.router`/`task.compact` 等。AdminUsage 报表自然能 group by purpose 看到。

### 验证
让 task_model_id 指向真实付费模型 → 跑几条对话 → /admin/usage 应看到 chat / task.title / task.router 三种 purpose 的行。

---

## A12 artifacts 越权下载 — 🟠→✅

### 问题
- `GET /api/artifacts/:id` 之前直接返回 404 不做事
- design.md 附录 B A12 明确要求归属校验 + 路径越权防护

### 修复
- `store.GetArtifact(id, userID)` JOIN artifacts/messages/conversations 三表校验归属
- handler 校验 storage_path 必须以 `ArtifactDir` 开头(防 `../../etc/passwd` 类越权)
- 按 mime 设 `inline`(图片)或 `attachment`(其他)的 `content-disposition`
- 设 `content-length` + 正确 mime 头
- 配套 `store.CreateArtifact(...)` 方便将来真沙箱/真图像写产物

---

## OpenAI tool_input 增量事件 — 🟠→✅

### 问题
- design.md §6.2 SSE 协议 `tool_input` 是工具入参的流式增量,Anthropic provider 发了,**OpenAI 不发**
- 影响: 用 GPT 时前端看不到"正在搜索: xxx"的实时进度,只能等工具执行完

### 修复
OpenAI provider 的 `readOpenAIChatStream` 在 tool_calls delta 出现时:
- 第一次见到 index 时 emit `tool_start`(同时携带 name + id)
- 每次 arguments delta 都 emit `tool_input`(带 id + name + 当前增量)
- 与 Anthropic 行为完全一致,前端无感

---

## i18n 全覆盖 — 🟠→✅

### 问题
- Admin 6 个页(channels/models/skills/users/usage/settings)、KB 列表/详情、MemoryView 都是英文硬编码
- 中文用户切到 zh 看到的还是英文 — 完全违反 i18n 要求

### 修复
新增 3 对翻译文件(admin + kb + memory,中英各一),共 6 个 JSON,200+ 个 key。
所有页面把 `'Cancel'`, `'Save'`, `'Loading…'` 等硬编码替换为 `t(...)` 调用。
`i18n/index.ts` 命名空间扩到 11 个。

修复 `sidebar.tsx` 的 star toggle 文本 bug(原来切换"已收藏"用了 `t('actions.copy', { ns: 'common' })`,完全错误的 key)。

---

## 删除 mock 业务数据 — 🟠→✅

### 问题
- `src/data/models.ts` `conversations.ts` `projects.ts` `user.ts` `replies.ts` 是 mock 数据
- `useModels` store 在 fallback 到 `MOCK_ADAPTED` — 这意味着没登录或后端没起时会显示"假模型列表",混淆排查
- `runtime/` 整个目录是空的(老 mock adapter 残骸)

### 修复
- 删除 5 个 mock data 文件 + runtime/ 目录
- `useModels` 不再 fallback,初始 `models: []` `defaultId: ''`,所有数据来自后端
- `settings store` 不再依赖 `DEFAULT_MODEL_ID`,初始空字符串
- `pages/settings/Models.tsx` 改用 useModels 真数据

保留 `data/suggestions.ts` — 这只是 i18n key + icon 占位,不是 mock 业务数据。

---

## UI 动效与 prefers-reduced-motion — 🟡→✅

### 修复
- 新增 `indeterminate` keyframe — KB 文档 parsing/embedding 状态条
- 新增 `message-in` keyframe — 消息行入场 220ms 淡入上滑
- 所有动画通过 `globals.css` 末尾的 `@media (prefers-reduced-motion: reduce)` 全局禁用
- 整体动效风格(porcelain + electric violet 单 accent + sage 仅 AI moment)与现有 dialog/sheet/dropdown 统一

---

**完。每一项都从 design.md 出发,从问题、修复、验证三段叙述。**
