# 系统提示词配置

管理员入口：**AI 与模型 → 提示词编辑**，地址 `/admin/settings/system-prompts`。

服务端统一提供提示词目录、默认内容、适用模型路径、语言、返回格式和必要占位符。每项编辑框只保存自定义覆盖内容；留空、仅输入空白字符或点击清空后保存，都会恢复系统默认值。非空内容中的前后空白会保留，方便编辑拼接片段。

## 配置范围

| 分类 | 可编辑内容 | 主要注册位置 |
| --- | --- | --- |
| 工具与文档路由 | 普通模型系统提示词、决策模型 instructions 与各 choice 选项说明、逐文档判断、工具用途、附件与请求输入模板、搜索查询生成 | `llm/system_prompt_catalog.go`、`llm/system_prompt_guidance.go`、`llm/system_prompt_templates.go`、`rag/system_prompt_catalog.go` |
| 记忆与上下文 | 提取、冲突裁决、去重的普通模型和决策模型指令；压缩系统指令、摘要结构和附加要求 | `llm/system_prompt_catalog.go`、`llm/system_prompt_extras.go` |
| 审查与核验 | 普通审查、自定义类别审查、决策审查、私密对话类别说明、回答核验及输入模板 | `llm/system_prompt_catalog.go`、`llm/system_prompt_guidance.go`、`llm/system_prompt_templates.go` |
| 对话与标题 | 五种语言的身份、日期、风格、个性化、信任边界、工具与技能指引、文件与记忆规则、引用与片段说明；标题生成和标题语言、内联 UI 规则 | `llm/system_prompt_locales.go`、`llm/system_prompt_guidance.go`、`llm/system_prompt_catalog.go` |
| 知识库 | 路由规则、证据充分性、长文档分段提炼及对应用户指令 | `llm/system_prompt_catalog.go`、`rag/system_prompt_catalog.go` |
| 深度研究 | 规划、来源阅读、反思、交叉验证、报告通用写作、标准／比较／趋势模板、无来源写作及各阶段资料说明 | `llm/system_prompt_catalog.go`、`llm/system_prompt_research_inputs.go` |
| 图片 | 转录与描述、证据使用、意图规划、提示词优化、参考图生成、忠实编辑 | `llm/system_prompt_catalog.go`、`llm/system_prompt_guidance.go`、`tools/system_prompt_catalog.go` |
| 工具与内置技能 | 原生工具说明、文本工具调用协议、格式重试、预算与无进展收尾、文档生成技能触发条件及完整说明 | `llm/system_prompt_extras.go`、`llm/system_prompt_guidance.go`、`tools/system_prompt_catalog.go` |

普通模型与决策模型的提示词独立保存。选择哪种模型仍由模型策略页决定；配置提示词不会修改模型、权限、选择键、置信度阈值或失败回退策略。工具范围路由继续只读取决策模型的 `choice`。

模型专属系统提示词、管理员技能（含数据库中预置的技能）、项目指令和用户个性化内容继续在其原有页面维护。工具清单、JSON schema、用户消息、历史记录、审查类别、候选记忆和文档内容仍由运行时生成。目录中的输入模板只控制包装与指令，不保存这些业务数据。

## 返回格式与占位符

页面逐项显示格式约束。标题生成只返回标题文本；普通工具范围路由只返回 `0` 或 `1`；普通审查只返回 `ALLOW` 或 `BLOCK`。需要 JSON 的任务必须保留其字段名、值类型及枚举。决策模型的 `search_only`、`full_tools`、`keep` 等选项键由程序保持，只编辑选项的判断说明。文本工具调用必须保留 `<tool_call>` 协议。

`{{model}}`、`{{user_request}}`、`{{max_tokens}}` 等必要占位符会列在页面上；非空覆盖缺少必要占位符时禁止保存。替换使用字面字符串，不执行模板代码，也不把管理员输入交给 `printf` 解释。

## 存储与生效

`GET /api/admin/system-prompts` 返回目录、当前覆盖和长度上限。`PATCH` 接受 `overrides` 对象，其中仅包含本次修改的 ID 和字符串值。接口验证全部变更后使用设置事务保存；未知 ID、非字符串、超长内容或缺失占位符会拒绝整次更新。两个接口均只允许管理员访问。

覆盖保存在 `system_prompt_overrides` 设置中，并随系统配置备份导出；导入时使用相同校验。压缩附加要求沿用 `context_compaction_prompt`，与上下文与记忆页共用一份数据。单项通常上限为 64 KiB，已有压缩附加要求保持 16 KiB，合并覆盖对象上限为 2 MiB。

保存后使设置缓存失效，后续请求使用新配置。已生成的标题、摘要和记忆不会追溯重写。图片转录的缓存标识包含视觉提示词覆盖摘要，相关提示词修改后会重新获取图片证据。

新增内置提示词时，在所属包初始化阶段注册到 `internal/systemprompts`，并让运行时从同一目录读取默认或覆盖内容。不要在前端复制默认提示词。`admin_system_prompts_test.go` 检查目录、格式说明、管理员边界、清空回退、原子校验和备份；`system_prompts_test.go` 检查普通／决策模型调用、动态上下文、压缩预算和视觉缓存。
