package tools

import "aivory/server/internal/systemprompts"

func init() {
	systemprompts.Register(systemprompts.Entry{ID: "image.reference", Name: "参考图生成约束", Group: "image", Engine: "shared", Default: `Generate a new image using the supplied images as visual references according to the instruction below.
Follow the user's requested relationship to the references. When asked to imitate or match a reference, closely follow its layout, composition, colors, borders, and typography while applying the requested content changes. When a reference is only for a subject or a particular detail, use it for that purpose. Preserve the requested literal text. Do not invent a different visual style that conflicts with the requested reference.

Exact user instruction:
`, Format: "图片生成模型的原生提示词；系统在末尾追加用户指令。", Notes: "运行时数据由系统自动插入。"})
	systemprompts.Register(systemprompts.Entry{ID: "image.edit", Name: "忠实编辑约束", Group: "image", Engine: "shared", Default: `Faithfully edit the supplied source image according to the instruction below.
Treat the first supplied image as the authoritative base canvas. Any later supplied images are references for the requested changes, not replacement canvases, unless the instruction explicitly selects a different base. Change only what the instruction explicitly requests. Preserve every other detail as closely as possible, especially the canvas and crop, composition, layout, colors, background, lighting, texture, text content, language, typography, spacing, and alignment. Do not translate, paraphrase, retype, add, remove, or restyle anything unless the instruction explicitly requires it.

Exact user edit instruction:
`, Format: "图片生成模型的原生提示词；系统在末尾追加用户指令。", Notes: "运行时数据由系统自动插入。"})
}

func init() {
	systemprompts.Register(systemprompts.Entry{ID: "tools.description.aivory_web_search", Name: "工具说明：联网搜索", Group: "tools", Engine: "shared", Locale: "", Default: (&webSearchTool{}).Description(), Format: "提供给模型的工具 description；工具名称、输入 schema 和权限由系统控制。", Notes: "运行时数据由系统自动插入。", Variables: []string{}})
	systemprompts.Register(systemprompts.Entry{ID: "tools.description.web_fetch", Name: "工具说明：网页读取", Group: "tools", Engine: "shared", Locale: "", Default: (&webFetchTool{}).Description(), Format: "提供给模型的工具 description；工具名称、输入 schema 和权限由系统控制。", Notes: "运行时数据由系统自动插入。", Variables: []string{}})
	systemprompts.Register(systemprompts.Entry{ID: "tools.description.fetch_image", Name: "工具说明：图片下载", Group: "tools", Engine: "shared", Locale: "", Default: (&fetchImageTool{}).Description(), Format: "提供给模型的工具 description；工具名称、输入 schema 和权限由系统控制。", Notes: "运行时数据由系统自动插入。", Variables: []string{}})
	systemprompts.Register(systemprompts.Entry{ID: "tools.description.python_execute", Name: "工具说明：Python 执行", Group: "tools", Engine: "shared", Locale: "", Default: (&pythonExecuteTool{}).Description(), Format: "提供给模型的工具 description；工具名称、输入 schema 和权限由系统控制。", Notes: "运行时数据由系统自动插入。", Variables: []string{}})
	systemprompts.Register(systemprompts.Entry{ID: "tools.description.image_generate", Name: "工具说明：图片生成", Group: "tools", Engine: "shared", Locale: "", Default: (&imageGenerateTool{}).Description(), Format: "提供给模型的工具 description；工具名称、输入 schema 和权限由系统控制。", Notes: "运行时数据由系统自动插入。", Variables: []string{}})
	systemprompts.Register(systemprompts.Entry{ID: "tools.description.use_skill", Name: "工具说明：技能加载", Group: "tools", Engine: "shared", Locale: "", Default: (&useSkillTool{}).Description(), Format: "提供给模型的工具 description；工具名称、输入 schema 和权限由系统控制。", Notes: "运行时数据由系统自动插入。", Variables: []string{}})
	systemprompts.Register(systemprompts.Entry{ID: "tools.description.save_memory", Name: "工具说明：记忆保存", Group: "tools", Engine: "shared", Locale: "", Default: (&saveMemoryTool{}).Description(), Format: "提供给模型的工具 description；工具名称、输入 schema 和权限由系统控制。", Notes: "运行时数据由系统自动插入。", Variables: []string{}})
}
