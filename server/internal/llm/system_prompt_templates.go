package llm

import "aivory/server/internal/systemprompts"

func init() {
	systemprompts.Register(systemprompts.Entry{ID: "routing.tool_scope.input", Name: "工具范围：输入模板", Group: "routing", Engine: "shared", Default: "Uploaded images: {{uploaded_images}}\nUploaded files: {{uploaded_files}}\nAttachment types: {{attachment_types}}\nConversation files available: {{conversation_files}}\nUser request: {{user_request}}", Format: "普通模型 user 消息／决策模型 state；附件状态与用户请求自动填充。", Notes: "运行时数据由系统自动插入；保留固定格式及边界标记。", Variables: []string{"{{uploaded_images}}", "{{uploaded_files}}", "{{attachment_types}}", "{{conversation_files}}", "{{user_request}}"}})
	systemprompts.Register(systemprompts.Entry{ID: "verify.input", Name: "回答核验：输入模板", Group: "moderation", Engine: "chat", Default: "<user-question>\n{{question}}\n</user-question>\n\n<assistant-answer>\n{{answer}}\n</assistant-answer>\n\nAudit the assistant-answer against the user-question.", Format: "严格 JSON；与核验系统提示词的 verdict、findings 格式一致。", Notes: "运行时数据由系统自动插入；保留固定格式及边界标记。", Variables: []string{"{{question}}", "{{answer}}"}})
	systemprompts.Register(systemprompts.Entry{ID: "tools.protocol.retry", Name: "提示词模式：工具调用格式重试", Group: "tools", Engine: "shared", Default: "<tool_error>\nYour <tool_call> JSON failed to parse: {{error}}\nRe-emit the tool call as ONE valid JSON object: <tool_call>{\"name\": \"<tool>\", \"arguments\": {...}}</tool_call>\n</tool_error>", Format: "须保留 <tool_call>、name、arguments、</tool_call> 协议。", Notes: "运行时数据由系统自动插入；保留固定格式及边界标记。", Variables: []string{"{{error}}"}})
}
