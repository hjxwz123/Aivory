# Aivory 2.5.0 发布：从对话，到真正交付文件

> Aivory 是一个开源、自部署的 AI 工作空间。2.5.0 正式版已经发布。本文对比的是 **2.4.0 → 2.5.0**，中间的 2.4.x 更新也计算在内。

![Aivory 2.5.0 能力示意：多模型协作、文件进入任务、预览编辑、生成演示文稿](https://raw.githubusercontent.com/hjxwz123/Aivory/v2.5.0/docs/screenshots/aivory-2.5.0-workflow.png)

过去，聊天工具回答完问题，真正整理文件、修改文档和交付结果往往还得切到别处。Aivory 2.5.0 把这些步骤更多地留在同一个工作区：把资料或目录交给 AI，在对话旁查看结果，直接修改文档，或者继续做成演示文稿。

## 相比 2.4.0，这次更新了什么？

| 方向 | 到 2.5.0 新增或改进的体验 |
| --- | --- |
| 对话与任务 | 对话里排队发送后续问题、快速切换分支；停止回复后可以删除或编辑重发。消息操作和生成文件入口更清楚。 |
| 文件工作流 | 上传整个文件夹并保留目录结构；在对话右侧预览 HTML、文档和沙箱产物，桌面端可以调整面板宽度。 |
| 文档编辑 | 在浏览器里编辑文本、代码、HTML、DOCX、XLSX，以及 PPTX 中已有的文字；结果可下载或保存为新副本。 |
| AI PPT | 可选接入文多多 Docmee API，在 Aivory 中编辑大纲、选模板、生成并管理演示文稿，生成和付费编辑计入用量。 |
| 搜索与模型 | 增加 Tavily 和免 Key 的 DuckDuckGo 搜索；可选用 TypeSafe/Jev 做工具、记忆和文档使用决策；文本模型也可借助独立视觉模型理解图片。 |
| 团队与部署 | 增加域名入驻、工作空间公告与权限控制、Passkey 和企业微信登录、管理员应用更新；四张发布镜像均支持 amd64/arm64。 |

### 文件不只是附件，也是任务的一部分

2.5.0 可以把整个目录交给有权限的沙箱任务，并保留子目录路径。生成的 HTML、PDF、Office 文件以及沙箱结果，能够在对话旁的统一面板中查看；可编辑的格式还能在浏览器里修改并另存一份。这样，问问题、检查产物和调整文件可以连续完成。

![Aivory 对话界面示例：模型选择、历史记录与回复](https://raw.githubusercontent.com/hjxwz123/Aivory/v2.5.0/docs/screenshots/hero.png)

*上图为产品对话界面示例；2.5.0 的文件面板和文档编辑能力以发布说明为准。*

### 搜索、执行、产出可以串起来

2.4.0 已经支持多工具协作。此后，搜索增加了 Tavily 与 DuckDuckGo，工具路由也能更准确地区分简单检索和需要完整工具集的任务。对于需要图表、表格或演示文稿的工作，Aivory 可以在沙箱里继续处理资料并返回产物。DuckDuckGo 无需 API Key，但公共端点可能对服务器 IP 限流或弹出验证。

![Aivory 工具任务示例：数据分析图表与可下载的 PowerPoint](https://raw.githubusercontent.com/hjxwz123/Aivory/v2.5.0/docs/screenshots/tool-calls-2.jpg)

*图中展示的是沙箱生成文件的任务示例；AI PPT 创作台是另一个需要管理员接入 Docmee 的可选功能。*

### 让团队使用更可控

从 2.4.0 到 2.5.0，Aivory 加入了邮箱域名入驻、工作空间公告、成员权限和个人数据处理流程，也支持 Passkey 与企业微信登录。管理员可以管理模型、渠道、工具与订阅，并通过系统更新页面安装已发布的稳定版本。

## 体验与部署

- 在线体验：[demo.aivorygo.com](https://demo.aivorygo.com)
- 源码与问题反馈：[GitHub · Aivory](https://github.com/hjxwz123/Aivory)
- 2.5.0 正式版与完整变更：[Release v2.5.0](https://github.com/hjxwz123/Aivory/releases/tag/v2.5.0)
- 部署文档：[docs.aivorygo.com](https://docs.aivorygo.com)

已有部署升级时，请先备份数据库、上传文件和生成产物，保留现有配置。在实际使用的 `deploy/.env` 中将 `IMAGE_TAG` 和 `UPDATER_IMAGE_TAG` 设为 `2.5.0`，确认没有旧版的 `APP_IMAGE_TAG` / `SANDBOX_IMAGE_TAG` 覆盖，再按[正式版说明](https://github.com/hjxwz123/Aivory/releases/tag/v2.5.0)核对镜像并升级。AI PPT、TypeSafe、视觉模型和文件夹沙箱任务都需要相应配置或权限。

欢迎在 GitHub Issues 反馈使用中的问题，也欢迎提交改进建议和 PR。
