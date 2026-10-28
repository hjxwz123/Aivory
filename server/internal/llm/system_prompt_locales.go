package llm

import (
	"database/sql"
	"strings"

	"aivory/server/internal/systemprompts"
)

type localePromptField struct {
	id, name  string
	text      *string
	variables []string
}

func localePromptFields(p *promptL10n) []localePromptField {
	return []localePromptField{
		{"identity", "助手身份", &p.identity, []string{"{{model}}", "{{model}}"}},
		{"defaultStyle", "默认回答风格", &p.defaultStyle, []string{}},
		{"dateGrounding", "当前日期", &p.dateGrounding, []string{"{{date}}"}},
		{"personaHeader", "个性化标题", &p.personaHeader, []string{}},
		{"personaTone", "个性化语气", &p.personaTone, []string{"{{tone}}"}},
		{"personaAddress", "用户称呼", &p.personaAddress, []string{"{{nickname}}"}},
		{"trustHeader", "信任边界标题", &p.trustHeader, []string{}},
		{"trustBody", "信任边界", &p.trustBody, []string{}},
		{"toolHeader", "工具指引标题", &p.toolHeader, []string{}},
		{"toolWebSearch", "联网搜索指引", &p.toolWebSearch, []string{}},
		{"toolSearchFilters", "搜索筛选指引", &p.toolSearchFilters, []string{}},
		{"toolSearchImages", "搜索配图指引", &p.toolSearchImages, []string{}},
		{"toolFetchImage", "图片下载指引", &p.toolFetchImage, []string{}},
		{"toolPython", "Python 指引", &p.toolPython, []string{}},
		{"toolImage", "图片生成指引", &p.toolImage, []string{}},
		{"toolSaveMemory", "保存记忆指引", &p.toolSaveMemory, []string{}},
		{"toolMultiRound", "多轮工具指引", &p.toolMultiRound, []string{}},
		{"toolSearchOnly", "仅搜索模式", &p.toolSearchOnly, []string{}},
		{"toolSearchRequired", "强制搜索", &p.toolSearchRequired, []string{}},
		{"toolCite", "来源引用指引", &p.toolCite, []string{}},
		{"sandboxHeader", "沙箱文件标题", &p.sandboxHeader, []string{}},
		{"sandboxBody", "沙箱文件指引", &p.sandboxBody, []string{}},
		{"skillsAvailHeader", "可用技能标题", &p.skillsAvailHeader, []string{}},
		{"skillsAvailBody", "技能按需加载", &p.skillsAvailBody, []string{}},
		{"skillsInlineHeader", "内联技能标题", &p.skillsInlineHeader, []string{}},
		{"skillsInlineBody", "内联技能指引", &p.skillsInlineBody, []string{}},
		{"presentationHeader", "回答展示标题", &p.presentationHeader, []string{}},
		{"presentationBody", "回答展示指引", &p.presentationBody, []string{}},
		{"projectHeader", "项目标题", &p.projectHeader, []string{"{{project}}"}},
		{"memoryHeader", "用户记忆标题", &p.memoryHeader, []string{}},
		{"memoryRules", "用户记忆使用规则", &p.memoryRules, []string{}},
		{"documentsHeader", "文档列表标题", &p.documentsHeader, []string{}},
		{"excerptHeader", "选中片段标题", &p.excerptHeader, []string{}},
		{"excerptBody", "选中片段指引", &p.excerptBody, []string{}},
		{"ragIntro", "知识库引用指引", &p.ragIntro, []string{}},
	}
}
func init() {
	for locale, defaults := range promptL10nTable {
		for _, f := range localePromptFields(&defaults) {
			required := []string{}
			for _, v := range f.variables {
				if len(required) == 0 || required[len(required)-1] != v {
					required = append(required, v)
				}
			}
			systemprompts.Register(systemprompts.Entry{ID: "conversation." + f.id + "." + locale, Name: f.name, Group: "conversation", Engine: "shared", Locale: locale, Default: replacePromptFormat(*f.text, f.variables), Format: "对话系统提示词片段 / Conversation system fragment", Notes: "按用户界面语言选择；仅覆盖当前语言版本。模型专属、项目和技能提示词仍在各自配置中维护。", Variables: required})
		}
	}
}
func replacePromptFormat(text string, variables []string) string {
	for _, v := range variables {
		text = strings.Replace(text, "%s", v, 1)
	}
	return text
}
func configuredPromptL10n(db *sql.DB, locale string) promptL10n {
	p := promptL10nFor(locale)
	overrides := systemprompts.Load(db)
	key := promptLocaleKey(locale)
	for _, f := range localePromptFields(&p) {
		*f.text = overrides.Text("conversation." + f.id + "." + key)
	}
	return p
}
