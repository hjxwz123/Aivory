package llm

import (
	"database/sql"
	"fmt"
	"strings"

	"aivory/server/internal/systemprompts"
)

func init() {
	systemprompts.Register(systemprompts.Entry{ID: "tools.protocol", Name: "提示词模式：工具调用协议", Group: "tools", Engine: "shared", Default: "\n\n## Available tools\n" +
		"You can call any of the tools below by emitting EXACTLY this format and then STOPPING:\n\n" +
		"<tool_call>{\"name\": \"<tool>\", \"arguments\": <args>}</tool_call>\n\n" +
		"Important rules:\n" +
		"- After emitting `</tool_call>` STOP. Do not write anything else and do not invent the result.\n" +
		"- The orchestrator will execute the tool and reply with a `<tool_result>` block. Continue from there.\n" +
		"- Never repeat a tool call with the same arguments. If the available evidence is sufficient, answer immediately.\n" +
		"- When a tool schema offers a queries or urls array, batch independent requests that are already known.\n" +
		"- If you don't need a tool, just answer the user directly.\n\n" +
		"Tools:\n\n", Format: "必须保留 <tool_call>{\"name\":\"工具名\",\"arguments\":{}}</tool_call> 调用格式；在结束标记后停止。工具清单自动追加。", Notes: "运行时数据由系统自动插入。"})
	systemprompts.Register(systemprompts.Entry{ID: "tools.final.budget", Name: "工具预算耗尽后的回答", Group: "tools", Engine: "shared", Default: toolBudgetFinalInstruction, Format: "最终文本回答；不得再请求工具。", Notes: "运行时数据由系统自动插入。"})
	systemprompts.Register(systemprompts.Entry{ID: "tools.final.no_progress", Name: "工具无进展时收尾", Group: "tools", Engine: "shared", Default: toolNoProgressInstruction, Format: "最终文本回答；不得再请求工具。", Notes: "运行时数据由系统自动插入。"})
	systemprompts.Register(systemprompts.Entry{ID: "tools.final.search", Name: "仅搜索模式收尾", Group: "tools", Engine: "shared", Default: searchOnlyFinalInstruction, Format: "最终文本回答；不得再请求工具。", Notes: "运行时数据由系统自动插入。"})
	systemprompts.Register(systemprompts.Entry{ID: "compaction.summary", Name: "上下文压缩：摘要结构指令", Group: "memory", Engine: "chat", Default: strings.Replace(compactionSummaryInstruction, "%d", "{{max_tokens}}", 1), Format: "结构化纯文本摘要；不得输出 JSON。", Notes: "{{max_tokens}} 自动替换为本次摘要的令牌上限。", Variables: []string{"{{max_tokens}}"}})
	systemprompts.Register(systemprompts.Entry{ID: "compaction.custom", Name: "上下文压缩：附加要求", Group: "memory", Engine: "chat", Default: "", Format: "纯文本附加指令。", Notes: "与「上下文与记忆」页面的压缩提示词共用同一个设置。", SettingKey: "context_compaction_prompt", MaxBytes: 16 * 1024})
}
func compactionInstruction(db *sql.DB) string { return systemprompts.Text(db, "compaction.summary") }
func renderCompactionInstruction(text string, limit int) string {
	if strings.Contains(text, "{{max_tokens}}") {
		return strings.ReplaceAll(text, "{{max_tokens}}", fmt.Sprint(limit))
	}
	return strings.Replace(text, "%d", fmt.Sprint(limit), 1)
}
