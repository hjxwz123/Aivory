# 浏览器内 Word / Excel / PPT / HTML 编辑方案技术选型调研

**调研日期：2026-09-21**（所有版本号与许可证均按此日期核实）
**目标场景**：商业闭源 React 19 + Vite + TypeScript Web 应用（AI 聊天产品），右侧面板需**预览 + 编辑** Word / Excel / PPT / HTML。

> **重要前提**：本次调研发现，2025 年的很多"常识"已经过时。以下是本次调研中最容易踩坑、且网上资料大量过期的地方：
> 1. **Univer 的 xlsx/docx/pptx 导入/导出是 Pro 付费功能**，OSS 版根本没有文件读写能力（详见 A.1）。
> 2. **SuperDoc 已重构为 v2 且改名 `superdoc`（DOCX 原生模型）**，但仍是 AGPL-3.0（详见 B.2）。
> 3. **出现了两个新的 Apache-2.0 浏览器内原生编辑器**：`@docx-editor.dev/*`（Word）和 `pptx-viewer`（PPT）—— 它们才是本题正解级方案（详见 B.1 / C.1）。
> 4. **ONLYOFFICE Docs 9.4（2026-05）已取消 Community Edition 的 20 并发连接限制**，且架构单进程化、移除 RabbitMQ/数据库依赖。**ONLYOFFICE 官方自己的 FAQ 和版本对比页仍写着"仅 20"，那两个页面已过期**（详见 B.3）。
> 5. **服务端方案的关键分野是原生格式**：**ONLYOFFICE 是 OOXML 原生**，**Collabora 是 ODF 原生**（OOXML 走转换过滤器）—— 这直接决定了保真度上限（详见 B.3 / B.4 / E.2）。
> 6. **SheetJS 的 npm 包停在 0.18.5（2022 年），最新 0.20.3 只在官方 CDN**（详见 A.5）。

---

## 0. 结论速览（给决策者的 6 句话）

1. **纯前端 + 商业闭源可用**的组合是存在的，且不需要 AGPL/商业授权：**Excel** 用 AG Grid Community(MIT) 或 RevoGrid(MIT) + SheetJS CE 0.20.3(Apache-2.0)；**Word** 用 `@docx-editor.dev/react`(Apache-2.0)；**PPT** 用 `pptx-viewer`(Apache-2.0)；**HTML** 用 CodeMirror 6(MIT) + TipTap(MIT)。
2. **要求"高保真 docx/pptx 原生 round-trip"的成熟方案，绝大多数是 AGPL 或商业授权**：Univer(Pro)、SuperDoc(AGPL)、ONLYOFFICE(AGPL+§7)、PPTist(AGPL)、`ranuts/document`(AGPL+§7 必须保留 logo)。闭源商用必须买商业授权或开源。
3. **不推荐为了省事上 ONLYOFFICE / Collabora**：两者**都不是 React 组件**，都是 **iframe + 独立服务端 + 强制后端**（ONLYOFFICE 必须有 `callbackUrl` 处理器，Collabora 必须自写 WOPI host），架构复杂度与运维成本远超"右侧面板"这个需求本身。如果确实要走服务端路线，**ONLYOFFICE 是 OOXML 原生**（保真度结构性更优，且 Developer 版专为白标嵌入 SaaS 设计），**Collabora 是 ODF 原生**（OOXML 走转换过滤器，保真度更弱）但**许可证零义务且唯一明码标价（€3/用户/月）**。两者共同短板：**都不执行 MS VBA、都不含 MS 字体、pptx 都是最高风险格式**。
4. **Excel 的最大陷阱是 Handsontable 系**：本体为商业授权（$999/开发者起），且 `hyperformula` 是 **GPL-3.0-only**（不是 MIT），闭源商用必须同时购买 HyperFormula 商业授权。**Excel 的第二个陷阱是"以为 OSS 版 Univer 能读写 xlsx"——它不能。**
5. **Word 的最大陷阱是 mammoth + TipTap + docx.js 链路**：它**不是 round-trip**。mammoth 是单向有损转换，`docx`(docx.js) **完全没有读取 API**，所以"导出"等于**重新生成一份新文档**，而不是保存用户的文件。页眉页脚/分页/文本框/浮动图形/公式/域/目录/修订痕迹全部丢失，且每次保存累积损失。
6. **两个纠正常识的发现**：① **PPTist 确实支持 pptx 导入**（走 MIT 的 `pptxtojson`，作者自评导入 ~85%+/导出 ~95%+），网上说它"导入缺失"是错的；② **你不需要自己写 PPTX 解析器**，`pptxtojson`(MIT) 已经解决了——**贵的是"编辑器"，不是"解析器"**。

---

## A. 电子表格（xlsx / csv）浏览器内编辑

### A.1 Univer（dream-num/univer）

| 项目 | 结论 |
|---|---|
| 最新版本 | OSS：`@univerjs/core` **0.25.2**、`@univerjs/presets` **0.25.1**（2026-09 发布）；另有 `1.0.0-alpha.8` / `1.0.0-beta.2` / `1.0.0-rc.0` 预发布线，**尚未 GA** |
| 许可证 | **Apache-2.0**（OSS 仓库根 LICENSE 已核实） |
| ⚠️ 关键限制 | **xlsx / docx / pptx 导入导出 = Univer Pro 付费功能，不在 OSS 包内** |
| React 兼容 | peerDeps 明确声明 `react ^19.0.0` ✅ |
| 包体积 | `@univerjs/presets` 解包 **7.2 MB**（1724 文件）；`@univerjs/core` 2.9 MB |

**免费版 vs Pro 差别（官方 README 的 "Open Source and Pro" 对照表，已核实）**：

- OSS 版 Sheets 具备：工作簿/工作表/区域、选择、公式、数字格式、筛选、排序、数据校验、条件格式、超链接、批注、查找替换、备注、表格、绘图集成、可扩展 UI。
- **Pro 独占**：实时协作、编辑历史、**导入/导出**、打印、图表、透视表、迷你图、大纲、形状、单元格内图形、数据连接器、服务端计算、性能增强公式引擎。
- OSS 版 Slides 仅有数据模型与 UI 包（**开发中**），Slides 的**模型/UI/导入导出全部是 Pro**。

**Pro 的商业模式与硬性技术约束（这是最容易低估的部分）**：

1. `@univerjs-pro/*` 包**在 npm 上完全没有 `license` 字段**（即默认"保留所有权利"），不是 Apache-2.0。已核实 `@univerjs-pro/exchange-client@0.25.2`、`@univerjs-pro/license@0.25.2` 的 registry 元数据。
2. 必须注册 `UniverLicensePlugin`（主线程 + Web Worker **各一次**）。**无有效 license 时进入 evaluation mode：文档带水印、导入文件大小受限、协作人数受限、部分高级功能受限**（官方 license 指南原文）。
3. **xlsx 导入导出必须自建服务端**：`UniverExchangeClientPlugin` 需要 `uploadFileServerUrl` / `importServerUrl` / `exportServerUrl` / `signUrlServerUrl` 等一整套 `universer-api` 端点，服务端还要放 `license.txt` + `licenseKey.txt`。
4. **官方未公开 Pro 价格**：univer.ai 上是 "Contact Us"（`/pricing` 返回 404）。我**无法核实具体报价**，只能确认"免费试用 30 天"。这本身是选型风险（不可预测的采购成本 + 与服务端绑定的授权校验）。

**采购建议**：Univer 只在你能接受"Pro 商业授权 + 自建转换服务端"时才成立。如果你要的是"纯浏览器、无服务端"，Univer OSS 直接出局——它连 xlsx 都读不了。

来源：[Univer README（OSS/Pro 边界）](https://github.com/dream-num/univer)、[Univer LICENSE (Apache-2.0)](https://raw.githubusercontent.com/dream-num/univer/main/LICENSE)、[Univer 能力矩阵](https://univer.ai/capabilities)、[univer-pro-integrate skill（Exchange 服务端 URL 要求）](https://github.com/dream-num/univer-sdk-skills/blob/main/skills/univer-pro-integrate/SKILL.md)、[license-guide（evaluation 限制原文）](https://raw.githubusercontent.com/dream-num/univer-sdk-skills/main/skills/univer-pro-integrate/references/license-guide.md)、[@univerjs-pro/exchange-client npm](https://registry.npmjs.org/@univerjs-pro/exchange-client/latest)

### A.2 Luckysheet（Univer 前身）— ❌ 已死，不要用

- **许可证 MIT**，但 **仓库 README 顶部明确写着 "Luckysheet is no longer maintained"**，并指引用户迁移到 Univer。
- `CHANGELOG` 最后一次发布是 **v2.1.13 / 2021-01-19**，即**约 5 年半前**。
- README 同时声明：**"For advanced features like import, export, and printing, please use Univer"** —— 即导入导出要另外找 Luckyexcel（`luckyexcel@1.0.1`，MIT，但同样是 2021 年前后的产物）。
- 结论：**闭源商业项目用它等于承担 5 年未修复的安全/兼容风险**（含 jQuery 依赖、IE11 时代代码）。仅在"内部一次性工具"场景可考虑。

来源：[Luckysheet README（弃维护声明）](https://github.com/dream-num/Luckysheet)、[Luckysheet CHANGELOG（末次 2021-01-19）](https://raw.githubusercontent.com/dream-num/Luckysheet/master/CHANGELOG.md)

### A.3 Handsontable / HyperFormula — ⚠️ 商业授权 + GPL 陷阱

| 包 | 版本 | 许可证（registry 原文） |
|---|---|---|
| `handsontable` | 18.1.1 | `SEE LICENSE IN LICENSE.txt`（**非开源**） |
| `hyperformula` | 3.4.0 | **`GPL-3.0-only`** |

**Handsontable 许可证原文要点**（已核实 `LICENSE.txt`）：

- **双重许可，但不是 OSS 双重许可**：仅"严格个人使用或纯评估用途"适用 non-commercial license；**任何商业用途都必须签商业授权协议**。
- 附加禁令：**"In any case, you must not make any such use of this software as to develop software which may be considered competitive with this software."**（不得用于开发与 Handsontable 构成竞争的软件）—— 对一个"AI 聊天产品内嵌表格编辑器"，措辞上有解释空间，法务需要评估。
- **价格（官网 pricing 页，2026-09 核实）**：Standard **from $999/开发者**；Priority **from $1299/开发者**；Enterprise 定制。Hobby license 免费但**不可用于商业**。每个使用 Handsontable 的开发者都需一个 license。

**HyperFormula 是独立产品、独立授权**：GPLv3 **或** 向 Handsontable 购买 proprietary license。**GPL-3.0-only 无法用于闭源商用**（除非把它作为独立进程 + 不构成衍生作品的隔离，实践上几乎不可行）。**因此：如果只用 Handsontable 而不买 HyperFormula 商业授权，你就不能开公式计算能力**；而"表格编辑器没有公式"通常不可接受 → 实际成本 = Handsontable + HyperFormula 两笔授权。

来源：[Handsontable LICENSE.txt](https://raw.githubusercontent.com/handsontable/handsontable/master/LICENSE.txt)、[HyperFormula LICENSE.txt（GPLv3/proprietary 双授权）](https://raw.githubusercontent.com/handsontable/hyperformula/master/LICENSE.txt)、[Handsontable Pricing](https://handsontable.com/pricing)

### A.4 x-spreadsheet / RevoGrid / AG Grid

| 方案 | 版本 | 许可证 | 商业闭源可用性 | 说明 |
|---|---|---|---|---|
| **x-spreadsheet** (`x-data-spreadsheet`) | 1.1.9 | **MIT** | ✅ 可用 | 1.2 MB，极轻。但**功能原始**（无公式引擎规模、无 xlsx 读写），作者维护极低频。适合"只要能改格子"的轻量场景 |
| **RevoGrid** (`@revolist/revogrid`) | **4.28.0** | **MIT** | ✅ 可用 | 7.2 MB / 448 文件。**注意包名是 scoped 的 `@revolist/revogrid`**，无 scope 的 `revogrid` 在 npm 上不存在（404）。它本质是**高性能虚拟数据网格**，不是电子表格：有编辑/导出关键词，但**无公式引擎、无 xlsx round-trip** |
| **AG Grid Community** | **36.2.0** | **MIT**（已核实 LICENSE.txt） | ✅ 可用 | 20.6 MB。同样是**数据网格**而非电子表格：Community 版**无 xlsx 导入导出**、无公式引擎。Enterprise 版才补这些（商业授权）。但若你的需求是"AI 产出表格 → 浏览/微调 → 导出 xlsx"，AG Grid Community + SheetJS 的组合非常干净 |

来源：[x-spreadsheet LICENSE (MIT)](https://raw.githubusercontent.com/myliang/x-spreadsheet/master/LICENSE)、[@revolist/revogrid npm (MIT)](https://registry.npmjs.org/@revolist/revogrid/latest)、[AG Grid LICENSE.txt (MIT)](https://raw.githubusercontent.com/ag-grid/ag-grid/master/LICENSE.txt)

### A.5 SheetJS (xlsx) — ✅ 可商用，但**必须换安装源**

这是本次调研中信息差最大的一个点：

| 问题 | 事实（2026-09 核实） |
|---|---|
| npm 上的 `xlsx` 是什么状态？ | **停在 `0.18.5`（2022-03 签名），已 4 年未更新**。是"registry 长期滞后"，不是项目死了 |
| 最新版本在哪？ | **`0.20.3`，唯一权威源是官方 CDN**：`https://cdn.sheetjs.com/xlsx-0.20.3/xlsx-0.20.3.tgz` |
| 许可证 | **Apache-2.0**（CE 版）。官方文档原文：*"The Apache 2.0 License is a 'permissive' license which allows commercial use but requires attribution."* 并明确 *"You may use SheetJS CE in proprietary applications"* |
| CE vs Pro | CE = 数据解析/生成；**Pro 才提供**：复杂模板样式编辑、图片/图表/透视表、公式求值引擎 |
| 商用合规要求 | 必须在开源声明页放置指定 attribution（保留版权与许可声明、标注修改）。官方建议放在 ToS/EULA 链接的独立页面 |
| 安装方式 | `npm i https://cdn.sheetjs.com/xlsx-0.20.3/xlsx-0.20.3.tgz`，或在 `package.json` 用 `overrides` 强制替换，**强烈建议 vendoring（把 tarball 放进仓库）**以规避供应链风险 |

**结论**：**SheetJS CE 0.20.3 是"xlsx 读写"这一层最省事、最安全的选择**（Apache-2.0 + 成熟 + 纯前端）。但它**只是读写库，不是编辑器 UI**：你需要自己接一个网格（AG Grid / RevoGrid / Handsontable）+ 自己实现编辑体验。若还需要**公式计算**，CE 没有公式引擎 —— 这是必须提前决策的功能边界。

来源：[SheetJS License 文档](https://docs.sheetjs.com/docs/miscellany/license)、[SheetJS 安装文档（0.20.3 CDN 权威源 + npm 滞后说明）](https://docs.sheetjs.com/docs/getting-started/installation/frameworks)、[SheetJS CDN](https://cdn.sheetjs.com/)、[SheetJS README](https://raw.githubusercontent.com/SheetJS/sheetjs/master/README.md)

### A.6 补充：其他 xlsx 读写库

| 方案 | 版本 | 许可证 | 备注 |
|---|---|---|---|
| **ExcelJS** | 4.4.0 | **MIT** | 读写 xlsx 且样式控制能力强于 SheetJS CE。**但最后发布于 2023-10，近 3 年未更新**（21.8 MB 解包，含大量 Node 依赖如 `unzipper`/`archiver`，浏览器打包需注意） |
| **jspreadsheet-ce** | 5.0.4 | registry **无 license 字段**（⚠️ 需向作者确认；CE 项目通常为 MIT） | 0.3 MB 极轻，有 `@jspreadsheet/formula` 依赖。商用前**必须**确认许可证 |
| **xlsx-populate** | — | 未核实 | 本次未验证，不作为推荐 |

来源：[ExcelJS LICENSE (MIT)](https://raw.githubusercontent.com/exceljs/exceljs/master/LICENSE)、[exceljs npm](https://registry.npmjs.org/exceljs/latest)、[jspreadsheet-ce npm](https://registry.npmjs.org/jspreadsheet-ce/latest)

---

## B. Word（docx）浏览器内编辑

### B.1 ⭐ `@docx-editor.dev/*`（EigenPal DOCX Editor）— 本类最佳解

**这是本次调研最重要的发现**，也是题目里 `@eigenpal/docx-js-editor` 的**正确后继**（该旧包已 `deprecated`，链路为 `@eigenpal/docx-js-editor` → `@eigenpal/docx-editor-react` → **`@docx-editor.dev/react`**）。

| 项目 | 结论 |
|---|---|
| 最新版本 | **2.21.0**（`@docx-editor.dev/core` 与 `/react` 同步） |
| 许可证 | **Apache-2.0**（`/core`、`/react`、`/vue`、`/i18n`、`/fonts`）；`/pro` 与 `/editor-api` 为 **EigenPal Pro Evaluation License 1.0（禁止生产使用）** |
| React 兼容 | peerDeps **`react ^18.0.0 \|\| ^19.0.0`** ✅ |
| 是否需要服务端 | **不需要**。官方定位："Canonical OOXML, all in the browser"、"**client-side**" |
| 包体积 | core 解包 6.8 MB；react adapter 1.3 MB；下载量 ~102–135k/月 |
| round-trip | ✅ **原生 OOXML 读写**，官方明确 "**lossless round-trip: untouched content and unsupported OOXML survive the save**" |

**官方定价（pricing 页原文，2026-09 核实）**：

- **Free / Core（Apache 2.0，$0 forever）**：打开与保存 .docx、编辑文本/表格/图片/链接/列表、使用 React 或 Vue 编辑器、**"Ship commercial products"**（明确允许闭源商业产品）。
- **Pro：$500 USD / 月**（单产品）。含**修订痕迹(tracked changes)、批注(comments)、自定义节点、实时协作、`editor-api` 文档自动化**。注意原文：*"One Pro subscription covers one product"* —— 多产品或白标需多份订阅；且**停止付费后必须从产品中移除 Pro 包**。
- **Pro + Priority**：定制报价（私有 Slack + 工程支持）。

**保真度细节（官方 Word fidelity 页面，93 项特性矩阵）**：分别给出 Editing / Rendering / Round-trip 三个维度。摘录关键点：

- ✅ Full（编辑+渲染+round-trip 全通）：粗斜下划线删除线、上下标、字体字号、文字颜色（**主题色按引用 round-trip，不扁平化为 hex**）、高亮/底纹、对齐与两端对齐、行距段距、keepNext/keepLines/widowControl/pageBreakBefore、缩进与悬挂缩进、段落样式（含自定义样式与 w:next 后继样式）、多级项目符号列表。
- ⚠️ Partial：**嵌入字体**（渲染 Full，但只能保留不能新增）、**RTL/双向文本**、**段落边框**（能渲染能 round-trip，**但编辑器 UI 还不能增删改**）、**制表位**、字符方向。
- ⚠️ **能渲染、能 round-trip，但不能编辑**：文本效果（轮廓/阴影/浮雕）、**隐藏文字**、**OMML 数学公式**（原样保存 + 样式化文本回退，**不能编辑公式**）、**首字下沉与文本框(framePr)**、**自动断字（连 round-trip 都只是 Preserved）**。
- ⚠️ **最大风险点：字体度量**。官方明确 "**Word-compatible wrapping requires font bytes**"；不提供字体文件时使用**回退度量，不保证与 Word 一致的分页/断行**。`@docx-editor.dev/fonts` 提供 6 种开源替代字体（5 个默认字族匹配字宽，Century Gothic 误差 <1%），但 "Kerning and glyph differences can still change line breaks"。**中文字体（宋体/黑体/微软雅黑）需自备并合法授权**。
- 安全/稳定性：有 API 稳定性策略；Next.js/SSR 需 dynamic import（依赖 DOM）。

**采购建议**：**如果你的 Word 需求是"编辑正文 + 表格 + 图片 + 列表"，直接用 Free/Apache-2.0 版即可，零授权成本、零服务端**。只有当你需要**修订痕迹/批注/协作**时才升到 $500/月 —— 而这些功能恰好是 AI 产品"让 AI 改稿并让人审阅"的高价值点，需要提前做预算决策。

来源：[DOCX Editor README（包清单 + license 范围）](https://github.com/eigenpal/docx-editor)、[LICENSE（Apache-2.0 + 范围声明）](https://raw.githubusercontent.com/eigenpal/docx-editor/main/LICENSE)、[Pro License（禁止生产使用）](https://raw.githubusercontent.com/eigenpal/docx-editor/main/packages/pro/LICENSE.md)、[Pricing（Free 可商用 / Pro $500/月）](https://www.docx-editor.dev/pricing)、[Word fidelity 矩阵](https://www.docx-editor.dev/docs/2.x/word-fidelity)、[@docx-editor.dev/core npm](https://registry.npmjs.org/@docx-editor.dev/core/latest)、[@docx-editor.dev/react npm](https://registry.npmjs.org/@docx-editor.dev/react/latest)、[@eigenpal/docx-js-editor（已 deprecated）](https://registry.npmjs.org/@eigenpal/docx-js-editor/latest)

### B.2 SuperDoc — ⚠️ AGPL-3.0，能力强但许可证硬

| 项目 | 结论 |
|---|---|
| 最新版本 | **`superdoc@2.16.0`**（另有 `legacy 1.46.3`、`1.0.0-beta.103` 线） |
| 许可证 | **AGPL-3.0**（仓库 LICENSE 与 npm 元数据均确认） |
| 商业授权 | 官方 README 原文：*"AGPLv3 for open source use. A **commercial license** is available for proprietary deployments."* → 闭源商用**必须购买商业授权**（价格未公开，需 [联系](https://www.superdocportal.dev/get-in-touch)） |
| 能力 | **DOCX 原生引擎**（V2 起改为 OOXML-backed 文档模型，不再以 ProseMirror 为权威模型）。官方："**Edits write back to the XML without an HTML conversion step**" |
| round-trip | ✅ 强。分页、节、页眉页脚、表格保留为文档结构 |
| 是否需要服务端 | **不需要**："The editor needs no server of its own" |
| 包体积 | 9.2 MB / 748 文件；peerDeps 含 `yjs`、`@hocuspocus/provider`、可选 `pdfjs-dist`、`@liveblocks/client` |
| 额外优势 | 同一 Document API 在浏览器 / Node SDK / Python SDK / CLI / MCP server 间通用，**对 "AI agent 改文档" 场景很契合** |

**重要历史变更**：V1 用 ProseMirror 作为权威编辑模型（所以很多老资料说它"基于 ProseMirror"）；**V2 已改为 DOCX 原生模型**。如果你看到的评测是 V1 时代，关于保真度的结论可能已过时。另外仓库已从 `Harbour-Enterprises/SuperDoc` 迁到 `superdoc-dev/superdoc`。

**结论**：技术上它是 SuperDoc / docx-editor.dev 里 DOCX 保真度最有野心的一家，但 **AGPL-3.0 让它在闭源产品里只有"买商业授权"一条路**。若你能接受付费，值得与 docx-editor.dev Pro 做同台对比（尤其 SuperDoc 的 agent/MCP 能力对 AI 产品有独特价值）。

来源：[SuperDoc README（AGPL + 商业授权声明、V2 DOCX-native 说明）](https://github.com/Harbour-Enterprises/SuperDoc)、[SuperDoc LICENSE (AGPL-3.0)](https://raw.githubusercontent.com/superdoc-dev/superdoc/main/LICENSE)、[superdoc npm（2.16.0 / AGPL-3.0）](https://registry.npmjs.org/superdoc)

### B.3 ONLYOFFICE Docs / Document Server — ⚠️ AGPL-3.0 + 附加条款 + 重服务端

| 项目 | 结论 |
|---|---|
| 许可证 | **AGPL-3.0**，且 **Ascensio System SIA 依 AGPL 第 7 条追加了额外条款**（已核实 LICENSE 文件，非纯 AGPL） |
| 附加条款要点 | ① 必须保留所有版权/许可/归属声明；② **修改版必须以显著方式标注"已修改"并注明日期、且说明基于 ONLYOFFICE**；③ **交互式界面中必须向用户展示"可识别 ONLYOFFICE 为原始开发者 / 当前可能是修改版 / 可访问许可信息"的显著入口**；④ 不授予商标权；⑤ 非代码内容（插画/图标/文档）为 **CC BY-SA 4.0** |
| 闭源商用结论 | ❌ **默认不可用**。AGPL 第 13 条 = 通过网络提供服务也必须向用户提供完整对应源码。你嵌进商业闭源 AI 产品就必须开源整个产品（或隔离到可争议的边界）。**唯一干净路径是购买 Enterprise/商业授权** |
| 服务端要求 | **必须独立 Docker 服务**。官方系统要求：**CPU 双核 2 GHz+、RAM 4 GB+、HDD ≥ 40 GB、SWAP ≥ 4 GB**、amd64 Linux。启动：`docker run ... onlyoffice/documentserver`，前端通过 `api.js` → `DocsAPI.DocEditor` iframe 指向**独立的 Document Server 主机**，**JWT 默认开启**（`JWT_SECRET`，建议指定固定值，否则重启会重新生成导致集成失效） |
| ⚠️ **后端是强制项** | ONLYOFFICE **只提供编辑器/转换/builder 服务**。你必须自己实现**文档存储服务** + **`callbackUrl` 处理器**（在文档关闭时从 `url` 下载编辑后的文件、落库、返回 `{"error":0}`）。**没有这个后端，编辑无法保存。** |
| 9.4 版重大变更（2026-05-19） | ✅ **Community Edition 取消了 20 并发连接限制**（"Removed the limitation of 20 simultaneously opened documents"）；架构**合并为单进程**；**移除了 RabbitMQ 与数据库依赖**；移除代码压缩以便阅读。⚠️ **ONLYOFFICE 自己的 FAQ/对比表仍写着 "only 20"，那些页面已过期** |
| 商业版/开源版功能差异 | **Enterprise/Developer 才有**：移动端 Web 编辑器、**白标（去品牌）**、Admin Panel、无限并发、集群/HA、Live Viewer、支持与 SLA。**Community 必须保留 ONLYOFFICE 品牌，不能白标** |
| 定价（官方配置器读取，2026-09-21） | **Docs Enterprise "From $1500"**；页面默认配置（50 连接 / 1 年 / 1 年更新 / Basic）显示 **合计 $2100**。**Docs Developer 显示合计 $3500**（未公布 "From" 价）。**均为按连接数计费的配置器报价，仅作指示性参考，不是正式报价** |
| Docker 与部署 | Community：`onlyoffice/documentserver` 单镜像。**Enterprise/Developer 镜像内置 PostgreSQL + RabbitMQ + Redis (+nginx)**。⚠️ **单镜像方案明确不兼容 Kubernetes** |
| docx/xlsx/pptx 编辑 | 三种格式都支持高保真编辑与真实 round-trip。**ONLYOFFICE 是 OOXML 原生**（内部格式就是 .docx/.xlsx/.pptx/.pdf，官方称 "native OOXML editing without any intermediate conversion"）—— 这是它相对 Collabora 的**结构性优势**。9.4 大幅增强 pptx（+25 套母版主题、+20 种切换） |
| ⚠️ **保真度实测级风险** | ① **`.docx` round-trip 并非无损**：官方确认 bug [#3382](https://github.com/ONLYOFFICE/DocumentServer/issues/3382)（v9.0.3）—— 打开时渲染完美，但保存后回 MS Word 打开**页眉页脚区域版式被破坏**（已修，2025-09 关闭）。失败模式是**版式/分页**，不是内容丢失。② **`.doc` 无法另存为 `.doc`**（官方明确 "not available for saving"），`assemblyFormatAsOrigin` 失败时会**弹回退提示并静默改存 OOXML** → **不要把 `.doc/.xls/.ppt` 当字节稳定格式**。③ **PPTX 有多个仍开放的确认 bug**：[#3349](https://github.com/ONLYOFFICE/DocumentServer/issues/3349) 不支持 `duotone` 幻灯片背景 → 在标准 MS 模板上**文字不可读**；[#3218](https://github.com/ONLYOFFICE/DocumentServer/issues/3218) 导出图片丢失/拉伸、**公式不可见**；[#3539](https://github.com/ONLYOFFICE/DocumentServer/issues/3539)、[#3206](https://github.com/ONLYOFFICE/DocumentServer/issues/3206)、[#2654](https://github.com/ONLYOFFICE/DocumentServer/issues/2654) 仍开放。官方 9.0 FAQ 把 "完整 SmartArt 编辑器" 和 "图表编辑" 列为**待实现需求** |
| ⚠️ **宏（VBA）** | **VBA 永不执行**。ONLYOFFICE 宏是 **JavaScript**、沙箱化、无文件/网络/OS 访问权限（7.1 起严格模式）。官方 FAQ：*"Can I use my Microsoft Office (VBA) macros in ONLYOFFICE? **Not directly**"*。且 **VBA 载荷保留无任何官方保证** —— bug [#3466](https://github.com/ONLYOFFICE/DocumentServer/issues/3466) 曾导致保存后 Excel 中**所有 VBA 失效**（已修） |
| ⚠️ **字体** | **默认不含 MS 字体**，缺失时**静默替换为最接近的字体**，官方明说 "the document layout and display might suffer from such substitution"。内置 `core-fonts` 为度量兼容替代品：**Carlito(≡Calibri)、Caladea(≡Cambria)、Liberation(≡Arial/Times/Courier)**、DejaVu、Noto、Open Sans、Ubuntu —— **没有真正的 Arial/Times/Calibri/Cambria**。→ **必须预期行断点变化**，需管理员安装真字体并重跑 `documentserver-generate-allfonts.sh`，且**字体授权是你的责任** |
| ⚠️ **大文件** | 客户端渲染 ⇒ **浏览器内存（而非服务器 RAM）是上限**。无官方大小限制页；第三方实测 ~25 MB 复杂 `.docx` 会让浏览器 OOM 崩溃（"Aw, Snap!"），1 MB docx ≈ 5–20 MB 内存，图片/OLE/修订/嵌套表格会显著恶化。限制项在 `/etc/onlyoffice/documentserver/default.json` 的 `inputLimits`（针对**解压后** zip 大小） |
| ⚠️ **安全隔离** | 服务端解析不受信 OOXML，有持续 CVE 历史（宏沙箱/RCE、XLS/PDF 解析越界、XSS）。**必须把编辑服务隔离在独立主机/网段**；注意 `ALLOW_PRIVATE_IP_ADDRESS` 默认 `false`（服务器默认取不到内网地址）、`ADMINPANEL_ENABLED`/`EXAMPLE_ENABLED` 默认 `false` |

**结论**：ONLYOFFICE 是"要真正 Office 级保真度"时的经典答案，但代价是：**1) 必须付费买商业授权（AGPL 对闭源产品是硬约束）；2) 必须运维独立 Docker 服务 + 文件存储 + 回调后端 + JWT + 字体**。对"右侧面板预览+编辑"的轻量需求属于**架构过度**。若三种格式的 Office 级保真度是硬需求，它是候选，但请连同运维人力一起算 TCO。

来源：[ONLYOFFICE DocumentServer LICENSE（AGPLv3 + Additional Terms）](https://raw.githubusercontent.com/ONLYOFFICE/DocumentServer/master/LICENSE)、[官方 Docker 安装与系统要求](https://helpcenter.onlyoffice.com/docs/installation/docs-community-install-docker.aspx)、[ONLYOFFICE Docs 9.4 发布说明：CE 取消连接限制、单进程化、移除 RabbitMQ/数据库（OSB Alliance，2026-05-19）](https://osb-alliance.de/news/onlyoffice94)

### B.4 Collabora Online — ✅ MPL-2.0（许可证友好），但服务端更重

| 项目 | 结论 |
|---|---|
| 许可证 | **Mozilla Public License 2.0 (MPL-2.0)**（已核实 `COPYING`）；LibreOffice 核心亦为 MPL-2.0 |
| ⚠️ **关键细分** | **MPL-2.0 适用于"源码形式"，但 Collabora 官方交付的"可执行形式"另有 proprietary 附加条件**。官方条款页原文：*"Executable Forms ... are distributed with additional conditions under a proprietary license"*；并规定 *"If You distribute any open source component of the Software, You must remove all Marks [Collabora trademarks]"* |
| 闭源商用结论 | ✅ **相对友好**。MPL-2.0 是**文件级弱 copyleft**，**没有 AGPL 的网络服务条款**。**实践路径：自己从 MPL 源码构建**（只需公开你修改过的 MPL 覆盖文件，**你的应用代码保持闭源**）；或购买 COOL 商业订阅 |
| ⚠️ CODE 不能用于生产 | 官方 FAQ 原文：CODE 是滚动开发版，*"no SLA or long term support"*，*"we don't recommend CODE for business or production environments"*；Collabora 员工（论坛，2025-09）：*"CODE is the community version for testing / home use, COOL is meant to be used for production."* **生产必须买 COOL 订阅** |
| 定价（官方 `/subscriptions`，2026-09-21 核实） | **CODE = 免费**。**Collabora Online for Business：≤99 用户 = €3.00 / £2.60 / $3.40 每用户每月**。100+ 用户 = "Personalised Volume Discounts"，需联系销售；教育/NGO 有特别价；3 年期可谈折扣 |
| 集成方式 | **WOPI 协议，WOPI host 是必须的** —— 你必须自建文件存储 + 认证。`coolwsd` 监听 **9980** 端口，`/hosting/discovery`，前端 iframe + postMessage API。官方 FAQ：*"You need some file storage & authentication solution, since COOL only provides document editing."* |
| 资源测算（公开的 openDesk / ZenDiS 政府基准） | **每 15 活跃用户 1 vCPU；每活跃用户 50 MB RAM；每 10 活跃用户 1 Mbit/s** |
| 当前版本线 | **26.04**（COOL 26.04，最新 26.04.3.3 / 2026-09-14）；Docker 镜像 `collabora/code` 及 COOL 对应镜像 |
| ⚠️ **架构性劣势：ODF 原生，不是 OOXML 原生** | LibreOffice 的默认格式是 ODF，`.docx/.xlsx/.pptx` 走**导入/导出过滤器** → **打开时转换、保存时重新序列化**。厂商自己的证据：26.04.3.3 修复了 *"Saving a PPTX to ODF and back could turn every shape green, and shapes that used a theme colour lost the link to the theme"*。这与 ONLYOFFICE 的 OOXML 原生有**本质差距** |
| ⚠️ **官方转换限制页（最关键的保真度文档）** | [About Converting Microsoft Office Documents](https://help.collaboraoffice.com/latest/en-GB/text/shared/guide/ms_import_export_limitations.html) 原文：*"some layout features and formatting attributes in more complex Microsoft Office documents are handled differently... or are unsupported. As a result, **converted files require some degree of manual reformatting.**"* 明确挑战清单 —— **Word**：AutoShapes、修订标记、OLE 对象、表单控件、索引、表格/框架/多栏、超链接与书签、WordArt；**PowerPoint**：AutoShapes、制表/行/段间距、**母版背景图形**、组合对象、多媒体效果；**Excel**：AutoShapes、OLE、控件、**数据透视表**、新图表类型、条件格式、部分函数。另：*"Collabora Office **cannot run Visual Basic Scripts**"* |
| ⚠️ **宏默认关闭** | 因 **CVE-2025-24796**（宏可远程执行恶意代码），**自 22.04 起宏默认禁用**（`coolwsd.xml` 的 `<enable_macros_execution>` 默认 `false`）。官方：*"**Macro editing is not possible online** and needs to be done in the desktop application."* |
| ⚠️ **字体同样缺失且静默替换** | 默认字体集为度量兼容替代品：`Carlito`/`Caladea`（≡ Calibri/Cambria）、`Liberation Sans/Serif`（≡ Arial/Times New Roman）、Noto、DejaVu、Gentium 等。**真 MS 字体缺失且无法合法再分发**；管理员可安装但**必须更新 systemplate**。静默替换是**实时 UX 问题**：开放 issue [#6351](https://github.com/CollaboraOnline/online/issues/6351) 要求增加缺字体提示 |
| ⚠️ **PPTX 是最弱环节** | 厂商 PowerPoint 挑战清单（含母版背景图形、AutoShapes、组合对象、间距、多媒体）都在"需要人工重新排版"之下。已修缺陷清单本身就是失败类枚举：母版背景未保留、**字号 10pt 误为 18pt**、Chartex 图表导入再导出丢失、嵌入字体失败导致保存崩溃等 |
| ⚠️ **渲染模型 = 服务端瓦片** | 与 ONLYOFFICE 的客户端渲染相反：每个按键都在服务端处理、服务端渲染瓦片下发。**服务器重、客户端轻**；代价是**每并发编辑者的 CPU/RAM 成本**，且两个来源的容量估算**相差约 3 倍**（见下） |
| ⚠️ **容量估算分歧（务必按高值压测）** | ① 德国政府 openDesk/ZenDiS 基准：**每 15 活跃用户 1 vCPU、每活跃用户 50 MB RAM**；② 第三方综合：**每 5 活跃编辑者 ~1 核、每活跃会话 ~300–500 MB**，空闲 ~1.3 GB。**Collabora 官方 sizing 文档在 Anubis 反爬后无法读取**。→ **按高值做容量测试** |
| 源码与集成注意 | **活跃开发在 Gerrit（gerrit.collaboraoffice.com），不在 GitHub**；GitHub 仅 issues + release 产物，镜像在 `CollaboraOnline/online.mirror`。**必须从 mirror/Gerrit 树构建，不要用 issue 仓库**。⚠️ **并非所有文件都是 MPL-2.0**（README：*"primarily under the MPLv2"*）→ 再分发前须审计 `browser/LICENSE`、`THIRDPARTYLICENSES`。仓库生成 **SBOM**（`SBOM.md`），对你的许可证审计有用。文档引擎按文档跑在 **chroot jail（"kit"）**中，Docker 附带 **seccomp profile** |

**第三方来源分歧提示**：`mso-test`（官方 round-trip 完整性测试，~243,000 文档）测的是 **"MS Office 能否无损打开"（完整性），不是视觉/格式保真度**，不要当作保真度评分。另有一项 2026 年独立实测发现一份 30 页合同 .docx 在 ONLYOFFICE 中"与 Word 几乎一致"，而 **Collabora 出现了分页位移**。

**结论**：许可证上是"服务端方案"里最干净的（MPL-2.0 且无网络条款），**且是唯一有公开明码标价的**（€3/用户/月）。**如果你的第一优先级是"许可证零风险"，Collabora 是答案**；**如果第一优先级是 OOXML 保真度与集成工作量，ONLYOFFICE 商业版是答案**。但三件事必须提前认清：① **pptx 是两者共同的最高风险格式**；② **两者都不跑 MS VBA、都不含 MS 字体**；③ **两者都要你自建后端**。

来源：[Collabora Online COPYING (MPL-2.0)](https://github.com/CollaboraOnline/online/blob/main/COPYING)、[Collabora Online 订阅与定价](https://www.collaboraonline.com/subscriptions/)、[Collabora 条款（MPL 源码 vs proprietary 可执行形式）](https://www.collaboraonline.com/terms/collabora-online-mplv2/)、[LibreOffice 许可证](https://www.libreoffice.org/licenses/)、[Collabora FAQs（CODE 不推荐用于生产）](https://www.collaboraonline.com/faqs/)、[CODE 页面](https://www.collaboraonline.com/code/)、[官方转换限制页](https://help.collaboraoffice.com/latest/en-GB/text/shared/guide/ms_import_export_limitations.html)、[mso-test 结果](https://www.collaboraoffice.org/mso-test/)、[openDesk 缩放基准](https://raw.githubusercontent.com/opendesk-edu/opendesk-edu/main/docs/scaling.md)、[26.04 发布说明](https://www.collaboraonline.com/collabora-online-26-04-release-notes/)

### B.5 TipTap 3 路线：mammoth + TipTap + docx.js —— ❌ 保真度不可接受

**这是本题我最强烈反对的方案**，因为它根本不是 round-trip。

| 组件 | 版本 | 许可证 | 本质 |
|---|---|---|---|
| `mammoth` | **1.12.3** | **BSD-2-Clause** | docx → **HTML**（单向、有损、语义化转换） |
| `@tiptap/core` | **3.31.3** | **MIT** | 基于 ProseMirror 的无头富文本编辑器 |
| `docx` (docx.js) | **9.7.1** | **MIT** | **只能"从零生成"新 docx，不解析已有 docx** |

**保真度损失的机制性原因**：这条链路是 `docx → HTML → (编辑) → 重新生成 docx`，**中间必然丢失所有 OOXML 结构语义**：

- **分页/版式**：HTML 没有"页"的概念 → 分页符、节、页边距、纸张尺寸、页眉页脚**全部无法表达**。
- **样式体系**：Word 的样式继承链、`w:next` 后继样式、主题字体/主题色 → 塌缩为行内 CSS 或直接丢失。
- **未建模内容**：文本框、浮动图形/SmartArt/形状、首字下沉、制表位与引导线、脚注尾注、域代码、OMML 公式、修订痕迹与批注 —— 这些在 HTML 里**没有对应表示**，mammoth 会直接跳过。
- **反向生成**：`docx` 库**无法读取**已有 docx，所以它**不可能保留**任何未被你的编辑器建模的内容 —— 每次保存都是"按你的 HTML 重新生成一份文档"，**原文件里所有未建模元素永久消失**。
- **字体度量**：浏览器渲染的断行与你最终生成 docx 里 Word 打开的断行是两套排版引擎，**必然不一致**。

**TipTap 的商业授权边界**（对闭源商用重要）：

- **核心编辑器 = MIT**（已核实 LICENSE.md，Copyright 2025 Tiptap GmbH），**可自由用于闭源商用**。StarterKit 等基础扩展 MIT。付费代码从**私有 registry `registry.tiptap.dev`** 以 `@tiptap-pro/*` 分发（这也是为什么公共 npm 上搜不到）。
- ✅ **2025 年 6 月起，10 个原 Pro 扩展转为 MIT**：Details/DetailsSummary、**Emoji**、**DragHandle**、**FileHandler**、InvisibleCharacters、**Mathematics**、**TableOfContents**、UniqueID。这对闭源产品是利好。
- ❌ **仍然付费**（按官方文档的 plan 标记）：**Comments**（Start 档）；**Import/Export DOCX**（BETA，Start 档起）；**Version/Snapshot**（Start）；**Snapshot Compare**（Team）；**Pages**（分页/页眉页脚，Team）；**Paste Handler**（Team）；**Basic AI Generation**（Start）；**AI Toolkit**（BETA 附加项）；**Tracked Changes**（附加项 **+$249/月**）。**Collaboration 扩展本身是 MIT**（+ Hocuspocus 4.7.0 MIT，可自托管），但**Comments/Version/协作必须使用 Cloud 托管文档**。
- **Tiptap 确实在卖 DOCX 导入导出**：产品名 "Conversion"，**Beta 状态**，**从 $49/月的 Start 档起含**（`@tiptap-pro/extension-convert-kit` + import/export 扩展），可本地部署。旧版 `@tiptap-pro/extension-import` / `-export` 于 **2026 年 sunset**。
- **定价**（2026-09 核实）：Start **$49/月**、Team **$149/月**、Business **$999/月**、Enterprise 定制；年付 −20%；开发者授权 $39/开发者/月起；30 天试用。**没有免费 Platform 档** —— 官方已于 2025 年 6 月**取消 Tiptap Cloud 免费档**。Pro 授权仅面向 B2B，永久有效，但**不可独立再分发，且必须随产品提供 license**。
- ⚠️ **React 19 的两个注意点**：① `@tiptap/react` 3.31.3 的 peerDeps 允许 `^17||^18||^19` 并在 React 19 上做了测试 ✅；② **但 Tiptap 官方 UI Components 文档仍写着"在 React 18 上效果最好……部分组件可能尚未完全兼容"React 19**；③ 有一个高影响的**未修复** bug：[#7543](https://github.com/ueberdosis/tiptap/issues/7543) —— 在 React 19 下，用已有 ID 重新挂载 `useEditor` 且配合 `ReactNodeViewRenderer` 时会触发 `flushSync` 错误（2026-02-26 提交，至 2026-07-16 仍 open）。**AI 聊天产品频繁挂载/卸载编辑器面板，正好踩这个场景，需 POC 验证。**

**结论**：mammoth+TipTap+docx.js **只能用在"AI 生成一份简单 docx 给你下载"的场景**，绝不能用于"用户上传 docx → 在右侧面板编辑 → 存回原文件"。若你的产品承诺"编辑 Word 文档"，这条链路会立刻暴露保真度问题。**替代品：`@docx-editor.dev/react`（Apache-2.0，原生 OOXML round-trip）或 SuperDoc（买商业授权）。**

**即使付费也解决不了的部分（战略要点）**：**TipTap 自己的付费 Conversion 能力矩阵也承认** —— 浮动图片（编辑器 ✕ / 导出 ✕）、文本框 ✕、形状 ✕、目录不导出、节/section ✕、脚注尾注导出 ✕、RTL 不支持；**分页需要 Team 档的 Pages 扩展**。也就是说：**"页级精确的 WYSIWYG round-trip"对富文本架构而言，在任何价位都尚未解决。**

来源：[Tiptap LICENSE.md (MIT)](https://raw.githubusercontent.com/ueberdosis/tiptap/main/LICENSE.md)、[Tiptap README（Pro Extensions 需订阅）](https://github.com/ueberdosis/tiptap)、[Tiptap Pricing（$49/$149/$999/月，Tracked Changes +$249）](https://tiptap.dev/pricing)、[@tiptap/core npm 3.31.3 MIT](https://registry.npmjs.org/@tiptap/core/latest)、[mammoth npm 1.12.3 BSD-2-Clause](https://registry.npmjs.org/mammoth/latest)、[docx npm 9.7.1 MIT](https://registry.npmjs.org/docx/latest)

### B.6 其他 docx 方案

| 方案 | 版本 | 许可证 | 编辑？ | round-trip？ | 结论 |
|---|---|---|---|---|---|
| **docx-preview / docxjs** | — | **Apache-2.0**（已核实 docxjs LICENSE） | ❌ **仅渲染/预览** | ❌ | 适合"只预览不编辑"；配合其他编辑器做纯预览面板可用 |
| `@eigenpal/docx-js-editor` | 0.5.3 | MIT（但已 **deprecated**） | ✅ | — | **不要用**，已改名。走 `@docx-editor.dev/react` |
| `@eigenpal/docx-editor-react` | 1.9.0 | Apache-2.0（但已 **deprecated**） | ✅ | — | 同上，中间过渡包名 |
| `@docx-editor.dev/pro` | 2.21.0 | **EigenPal Pro Evaluation License（禁止生产）** | ✅ | ✅ | **⚠️ 危险**：包在公共 npm 上，但许可证明确 **"You may not use the Software for Production Use"**，违反即侵权。生产必须购买 |

来源：[docxjs LICENSE (Apache-2.0)](https://raw.githubusercontent.com/VolodymyrBaydalka/docxjs/master/LICENSE)、[@docx-editor.dev/pro License](https://raw.githubusercontent.com/eigenpal/docx-editor/main/packages/pro/LICENSE.md)

---

## C. PPT（pptx）浏览器内编辑

**先回答核心问题：是否存在成熟的开源浏览器内 pptx 编辑器（不只是渲染器）？**

**2026 年的答案是：存在，而且不止一个。** 这是与 2024 年以前情况最大的不同 —— 以前确实"只有渲染器"（如 reveal.js、pptx-preview），现在有两类可用方案，但**成熟度差异很大**。

### C.1 ⭐ `pptx-viewer`（ChristopherVR）— Apache-2.0，本类唯一"闭源免费可用"的原生 pptx 编辑器

| 项目 | 结论 |
|---|---|
| 最新版本 | `pptx-viewer-core` **4.0.1**、`pptx-react-viewer` **4.1.1** |
| 许可证 | **Apache-2.0**，LICENSE 原文明确：*"free to use, modify, and distribute, **including in commercial and closed-source projects**"*，并强调专利授权对法务友好 |
| React 兼容 | peerDeps **`react ^18.2.0 \|\| ^19.0.0`** ✅ |
| 能力 | **解析 / 渲染 / 编辑 / 放映 / 转换**，且**保存回合法 .pptx**（round-trip）。支持 `.pptx/.ppsx/.pptm/.potx` 及旧版二进制 `.ppt` |
| round-trip 覆盖 | **16 类元素**、预设形状、图表、SmartArt（含节点增删改+样式+3D 布局）、表格样式、主题、母版、嵌入媒体、EMF/WMF 元文件、OLE 对象、数字墨水、签名、**AES-128/256 加密**、**VBA 宏保留**、Strict/Transitional 命名空间互转（48 组映射） |
| 是否需要服务端 | ❌ **不需要**。客户端渲染（HTML/CSS/SVG 而非 Canvas），核心引擎也可在 Node 跑 |
| 包体积 | ⚠️ **较大**：`pptx-react-viewer` 解包 **40.0 MB**、`pptx-viewer-core` **23.0 MB**。且 peerDeps 很多：`framer-motion`、`lucide-react`、`react-icons`、`i18next`、`react-i18next`、`fast-xml-parser`、`jszip`、`jspdf`、`html2canvas-pro`；`yjs`/`three` 可选 |
| ⚠️ 成熟度风险 | **单一维护者（ChristopherVR）+ 个人 GitHub 仓库**；README **自陈 "Developed with Claude Code (Opus 4.x)"**。虽有 provenance 签名、Playwright/Vitest 测试、CI 徽章，但**无企业背书、无 SLA、社区规模小**。属于"高能力 / 高单点风险" |
| 已知限制（官方列出） | CSS 渲染导致 `backdrop-filter` 近似、路径渐变近似为椭圆径向；**字体必须浏览器已有，缺失则回退系统字体**（嵌入字体可解混淆注入）；动画 266 个预设中仅 42 个有专门播放效果，其余回退淡入淡出；光栅导出经 html2canvas-pro 有保真差异；受浏览器 canvas 尺寸上限（通常 16384²） |

**采购建议**：**如果 PPT 是核心功能且必须闭源免费 → 目前它是唯一现实选择**。但请务必做 POC（用你们真实的 pptx 样本测 round-trip），并把"单维护者风险"纳入评估（例如考虑 fork 后自行维护、或准备商业方案作为 Plan B）。包体积需要 code-splitting（它是右侧面板，可 lazy load）。

来源：[pptx-viewer README（Apache-2.0、能力、限制、React 用法）](https://github.com/ChristopherVR/pptx-viewer)、[pptx-react-viewer npm 4.1.1](https://registry.npmjs.org/pptx-react-viewer/latest)、[pptx-viewer-core npm 4.0.1](https://registry.npmjs.org/pptx-viewer-core/latest)

### C.2 PPTist — AGPL-3.0，能力最强但**禁止闭源商用**

| 项目 | 结论 |
|---|---|
| 许可证 | **AGPL-3.0**（已核实 LICENSE）。商业页原文：**"本项目禁止闭源商用"** |
| 技术栈 | **Vue 3 + TypeScript**（不是 React）。React 项目只能 iframe 嵌入或重写 |
| pptx 导入 | ✅ 支持，官方自评**保真度 ~85%+**（"animations, special charts, deeply nested structures, non-standard elements, and some advanced styles may inevitably have fidelity differences"） |
| pptx 导出 | ✅ 支持，官方自评**保真度 ~95%+** |
| 商业授权 | 可选路径（作者明码标价）：① 使用 2022-05 停维护的 **Apache-2.0 旧版**；② 成为重要贡献者；③ **付费独立商业授权：1 年 ¥2999 / 永久 ¥5699（不含税）**。⚠️ 授权**不含技术支持、不含 API/SDK、不保证未来兼容性、不保证无 bug** |
| 编辑能力 | 非常完整：文本富文本、图片裁剪/滤镜/蒙版、形状自由绘制、线条、图表（8 类）、表格、视频/音频、LaTeX 公式、分组/对齐/磁吸、动画与切换、演讲者视图、移动端基础编辑 |
| 成熟度 | 高（长期活跃、文档与 FAQ 完善、"代码可控无技术债"），但**明确声明不是开箱即用产品，需自行接后端** |

**采购建议**：**¥2999/年 或 ¥5699 永久是一次性低成本**，如果你是 Vue 团队或能接受 iframe 集成，它的"编辑体验成熟度"其实高于 pptx-viewer。但它是**为自建演示产品设计的**，作者明确建议"用它做不同于 Office PPT 的演示产品，而不是 Office 文件的编辑中转站"。若你的诉求恰恰是"高保真编辑 Office pptx"，作者的官方建议是**用真实样本先验证保真度**。

来源：[PPTist README（保真度自评、商业授权条款与价格）](https://github.com/pipipi-pikachu/PPTist)、[PPTist LICENSE (AGPL-3.0)](https://raw.githubusercontent.com/pipipi-pikachu/PPTist/master/LICENSE)

### C.3 pptxgenjs — ⚠️ 只能生成，不能读

| 项目 | 结论 |
|---|---|
| 最新版本 | **4.0.1**（2025-06 发布，维护中） |
| 许可证 | **MIT** ✅ |
| 能力边界 | **纯"从零创建"**：`pptxgenjs` 是生成库，**没有解析已有 .pptx 的能力**。因此它**无法单独实现 round-trip** |
| 包体积 | 2.5 MB，很轻 |

**"用 pptxgenjs + 自建画布编辑器"的可行性与工作量**（题目问的第 3 点）：

**可行性结论：技术上可行，但不建议从零自建。** 关键纠正：**你不必自己写 PPTX 解析器** —— `pptxtojson`（MIT，见 C.4）已经把"读 pptx"这一环解决了。**真正昂贵的是"编辑器"，不是"解析器"。** 但剩下的每一项（三方模型映射、真实保真渲染器、交互层、富文本、图表表格 SmartArt、母版/主题继承、导出保真度打地鼠）依然是巨大的工程量。

**工作量粗估（1 名熟练前端，含测试，不含美术/产品设计）**：

| 范围 | 内容 | 人日 |
|---|---|---|
| 只读画布查看器 | 解析（pptxtojson）+ 渲染 + 缩放平移 + 缩略图 | **30–60** |
| 查看器 + 基础元素编辑 | 上述 + 选中/拖动/缩放/旋转 + ProseMirror 文本引擎 + 撤销重做 | **120–200** |
| + 完整元素与交互 | 形状/图片/表格/图表、图层、分组、吸附对齐、备注 | **300–500** |
| + 母版/版式/主题、媒体、公式、SmartArt、~95% round-trip、移动端 | — | **600–1,000+** |
| **（旧估算，仅列作对照）** | 曾按"含自写解析器"估 125–235 | 已作废 |

**现实锚点**：PPTist 从 **2020-12** 开始做，到 **2026-09-19 仍在提交导入保真度修复** —— 约 **5.7 年**的持续投入。

**必须强调的三方模型映射问题**：PPTist 的内部模型 ≠ `pptxtojson` 输出 ≠ `pptxgenjs` 输入。你需要自己做**三向映射**（单位 pt/px/EMU、旋转翻转、autofit/`fontScale`、文本内边距、z-order），这是纯粹的新增工作量。另外 **PPTist 把 `pptxgenjs` 钉在 `^3.12.0`，而当前是 4.0.1**（⚠️ 是否有意为之未核实，v4 改变了 Node 探测与 Vite/Web Worker 行为）。

**对比**：直接采用 `pptx-viewer`（Apache-2.0）集成 + POC 约 **8–20 人日**；采用 PPTist 商业授权（iframe）约 **10–20 人日**。**自建成本是采用现成方案的 15–50 倍**，且最终保真度大概率更低。**唯一值得自建的理由**：你的 PPT 场景**不需要读现有 pptx**（例如"AI 生成全新演示文稿"），此时 pptxgenjs（MIT）+ 只读预览的成本可降到约 **25–45 人日** —— 此时完全无需 ptxtojson。

> ⚠️ **值得挑战需求本身**：如果这个 PPT 面板的真实需求是"把 AI 生成的演示文稿呈现/微调/导出"，而不是"编辑用户上传的 pptx"，那么 **pptxgenjs(MIT) + 轻量预览**才是成本与合规双优解。请先确认这一点再投入编辑能力。

来源：[pptxgenjs npm 4.0.1 (MIT)](https://registry.npmjs.org/pptxgenjs/latest)

### C.4 `pptxtojson` — MIT 的浏览器端 PPTX 解析器（⭐ 战略价值高）

这是本次调研中"自建路线"评估的关键拼图，纠正了题目的一个前提。

| 项目 | 结论 |
|---|---|
| 最新版本 | **v2.0.1（2026-04-15）**；上一 tag 1.5.0（2025-06-22） |
| 许可证 | **MIT** ✅（Copyright © 2020–present pipipi-pikachu） |
| 能力 | **浏览器端 PPTX → JSON 解析**（纯前端，`parse(arrayBuffer, options)`）。输出 `slides[]`、`themeColors[]`、`size{width,height}`、`usedFonts[]` |
| 元素覆盖 | `text / image / shape / table / chart / video / audio / math / diagram(SmartArt) / group`，以及填充（纯色/图片/渐变/图案）、边框、阴影、旋转、翻转、文本内边距、autofit + `fontScale`、超链接、演讲者备注、页面切换、`layoutElements`（母版/版式） |
| 自评保真度 | 整体 "~80%+"；**用户从零创作的 deck 可达 95%+**；复杂母版/模板、深层嵌套组、渐变、SmartArt 更差 |
| ⚠️ 注意 | 文本以 **HTML 富文本**输出；所有长度单位为 **pt**（0.x 用 px，迁移有坑）；Node 支持为 experimental |

**这直接修正了「用 pptxgenjs 从零生成 + 自建画布编辑器」的工作量评估**：你**不需要自己写 PPTX 解析器** —— `pptxtojson`（MIT）免费补上了这一环。**真正昂贵的是"编辑器"，不是"解析器"。**

来源：[pptxtojson](https://github.com/pipipi-pikachu/pptxtojson)

### C.5 ⚠️ `ranuts/document` — 架构极具参考价值，但**许可证不可用**

| 项目 | 结论 |
|---|---|
| 最新版本 | **v0.0.6（2026-09-02）**，活跃（~1,948★） |
| 本质 | 把 **ONLYOFFICE `sdkjs` + `web-apps` 9.3.0.133 与 `x2t` 转换器编译为 WebAssembly**，**完全客户端运行**，PWA/离线。可编辑 `.docx/.xlsx/.csv/.pptx`，可打开 `.doc/.ppt/.odt/.ods/.odp/.rtf/.txt`，PDF 批注 |
| 与本题的契合度 | **极高**：iframe + 完整 `postMessage` API（`document:open-url` / `document:opened` / `document:saved`）、`embed=1`、`readonly=1`；另有 npm 组件 `@ranui/preview` |
| 部署 | 静态构建或 `ghcr.io/ranuts/document:latest`（**只是静态服务器，无服务端处理**） |
| 体积 | ⚠️ NOTICE 披露 `x2t.wasm` 约 **42 MB**（brotli 压缩后分发），另有内置字体与内置 Monaco → **必须 lazy-load / iframe 隔离** |
| **许可证** | ❌ **AGPL-3.0 + ONLYOFFICE §7 附加条款**。AGPL §7(b) 要求**分发时必须保留原始产品 logo** —— 该项目在页头与"关于"面板展示 ONLYOFFICE logo，并**有测试在 logo 被移除时失败**。§7(e) 不授予商标权。项目明确声明自己是衍生作品、与 Ascensio 无关联 |

**结论：不能直接用于闭源产品（AGPL §13 + 保留 logo 义务），但它是最好的架构参考**（worker + WASM 转换、postMessage embed 契约、File System Access 写回）。若"纯浏览器 + Office 级保真度"是你的硬需求，这是一个值得**与法务一起评估商业授权可能**的方向。

来源：[ranuts/document](https://github.com/ranuts/document)

### C.6 其他已核查并排除的 PPTX 方案

| 方案 | 版本 | 许可证 | 为何排除 |
|---|---|---|---|
| `pptx-preview` | 1.0.7（2025-10） | ISC | 仅预览，不编辑 |
| `pptxjs` | `0.0.0`（358 字节） | ISC | **stub/占位包，已死** |
| `onlyoffice-x2t-wasm` | 2026-04-23 | ⚠️ **无 LICENSE 文件**（上游为 AGPL） | 仅转换、不编辑；许可证状态不明 |
| reveal.js | — | MIT | 仅播放，无编辑 |
| WebODF | — | — | ODF 而非 OOXML |
| Syncfusion PptxEditor | — | 商业 | 免费 Community License 仅限 <$1M 营收且 ≤5 开发者 |
| Aspose.Slides for JS / WPS Web Office SDK / Zoho Office Integrator | — | 商业 | 需采购 |
| Google Slides API | — | 商业 | ⚠️ **Google 原生幻灯片模型，`.pptx` 仅能导入/导出，无法原地编辑 PPTX XML** |

---

## D. HTML 代码编辑器

### D.1 CodeMirror 6 vs Monaco Editor

| 维度 | **CodeMirror 6** | **Monaco Editor** |
|---|---|---|
| 最新版本 | `codemirror` **6.0.2**；`@codemirror/state` **6.7.5**；持续发布 | **0.56.0** |
| 许可证 | **MIT** ✅（已核实 LICENSE） | **MIT** ✅ |
| 实测体积 | `codemirror@6.0.2`（basicSetup）= **373,186 B raw / 118,750 B gzip**（Bundlephobia 实测）。⚠️ 真实 HTML+CSS+JS 组合约 **140–175 kB gzip 是估算值，非实测**。**可裁剪**：不用 `basicSetup` 而手动挑扩展，去掉 `@codemirror/autocomplete`（~75.9 kB raw）、`@codemirror/lint`（~31.6 kB）、`@codemirror/search`（~41.3 kB）可省很多 | **数 MB 级**。`monaco-editor` 解包 93.4 MB；发布产物含单个 chunk **6,612,405 B（gzip ~1.40 MB）** 与 **4,045,954 B（gzip ~1.03 MB）**，另有约 90 个 chunk，CSS 167 kB raw / 25 kB gzip，codicon.ttf 141 kB |
| React 集成 | `@uiw/react-codemirror` **4.25.11**（MIT，peerDeps `react >=17` ✅ React 19 可用）；wrapper 自身仅 **151,015 B raw / 48,779 B gzip** | `@monaco-editor/react` **4.7.0**（MIT，peerDeps 明确含 **`react ^19.0.0`**） |
| Vite 打包 | ✅ 干净，无特殊配置 | ⚠️ **需处理 Web Worker**（`MonacoEnvironment.getWorker` / `?worker`）。**AMD 构建已废弃**。是 Vite 上最常见的坑 |
| 窄面板适用性 | ✅✅ **强烈推荐**。单列流式布局、**无最小宽度**、可隐藏 gutter、`EditorView.lineWrapping`，**200–280px 即可用** | ❌ **不适合**。桌面 IDE 形态的固定 chrome（minimap、overview ruler、固定字形边距），**不会 reflow** |
| 移动端/触控 | ⚠️ **未核实**：未找到厂商明确声明。基于原生 selection/`contenteditable` + `drawSelection` 扩展；社区报告有典型软键盘/IME 边界问题。桌面优先产品可忽略 | ❌ **厂商明确不支持**：README FAQ 原文 *"Is the editor supported in mobile browsers or mobile web app frameworks? **No.**"*（issue microsoft/monaco-editor#246）。另：**VS Code 扩展在 Monaco 中不可用** |
| 特性 | 语法高亮、自动补全、lint、折叠、多光标、搜索 | 全部 + 智能提示、类型检查、diff editor、minimap、命令面板 |

**其他可选项**：**Ace**（`ace-builds` 1.44.0，**BSD-3-Clause**，成熟但触控弱、架构老）；**Prism + textarea overlay / CodeJar**（MIT，几十 kB / ~2 kB，无真正编辑能力）。❌ Sandpack（是打包器+运行器，不是文本编辑器）、❌ StackBlitz WebContainers（商业 WASM Node 运行时，体积巨大）。

**结论**：**右侧窄面板场景选 CodeMirror 6。** Monaco 的优势（类型系统、diff、重构工具）在"编辑一段 HTML"上基本用不到，却要付出 **10 倍以上传输体积**、Vite worker 配置成本、**且移动端官方不支持**。若未来要做"VS Code 级"的代码编辑主界面，再引入 Monaco。

⚠️ **2026 年变更提示**：CodeMirror 主仓库**已从 GitHub 迁出**至 `https://code.haverbeke.berlin/codemirror/dev`（GitHub 仓库首页仅留迁移说明）。许可证仍为 MIT，但如果你有依赖 GitHub 仓库地址的自动化（如 Dependabot 自定义、镜像脚本），需要更新。npm 包分发不受影响。

来源：[CodeMirror LICENSE (MIT)](https://raw.githubusercontent.com/codemirror/dev/main/LICENSE)、[CodeMirror 仓库迁移说明](https://raw.githubusercontent.com/codemirror/dev/main/README.md)、[codemirror npm 6.0.2](https://registry.npmjs.org/codemirror/latest)、[@uiw/react-codemirror npm 4.25.11](https://registry.npmjs.org/@uiw/react-codemirror/latest)、[monaco-editor npm 0.56.0](https://registry.npmjs.org/monaco-editor/latest)、[@monaco-editor/react npm 4.7.0](https://registry.npmjs.org/@monaco-editor/react/latest)

### D.2 ⚠️ WYSIWYG 编辑器的许可证地雷（对闭源商用极其重要）

**必须点名两个"看起来免费、实际不能用"的编辑器**：

| 方案 | 最新版本 | **许可证** | 闭源商用结论 |
|---|---|---|---|
| **TinyMCE** | **8.9.1** | **GPLv2+** | ❌ **默认不可用，且被技术性硬阻断**。官方 v8 文档原文："When using TinyMCE 8 in a self-hosted environment, a license key must be provided and it must be valid. Otherwise, **the editor will be disabled**." 且 "when using TinyMCE 8 in a self-hosted environment **for commercial use, a commercial license key manager addon is required in order for the editor to operate**"。key 形式：`'gpl'` / `T8LK:...`（商业自托管）/ `GPL+T8LK:...`。**唯一的 GPL 路径是把你的整个应用以 GPLv2+ 授权 → 与闭源不兼容**。⚠️ `tiny.cloud/pricing` 抓取为空，**价格未核实** |
| **CKEditor 5** | **48.5.1** | **GPL 2+ / 商业 双授权**（npm `license: "SEE LICENSE IN LICENSE.md"`） | ❌ **默认不可用**。仓库 `packages/ckeditor5/LICENSE.md` 原文：*"Licensed under a dual-license model, this software is available under: the **GNU General Public License Version 2 or later** ... or **commercial license terms from CKSource**"*。⚠️ **是 GPLv2+，不是 CKEditor 4 时代的 LGPL** → copyleft 覆盖你的应用。另：**解包 42.5 MB**（本表最重），且协作/修订历史/导出/AI/Word 导入导出/修订痕迹**全部商业版独占** |

**⚠️ 两者都是"文档里写着开源、但许可证对闭源商用不友好"的经典陷阱**，且 TinyMCE 8 还加了技术性强制（无 key 即禁用）。如果你的产品是闭源 SaaS 且不分发前端代码，TinyMCE 的 GPL 义务在某些解读下有争议空间（纯 SaaS 不"分发"），但**风险由你承担，不建议**。

**可安全用于闭源商用的 WYSIWYG 选项**（版本于 2026-09-21 核实，体积来自 Bundlephobia）：

| 方案 | 最新版本 | 许可证 | 体积 raw / gzip | 任意 HTML round-trip | 维护 |
|---|---|---|---|---|---|
| **TipTap（核心）** | **3.31.3** | **MIT** ✅ | core 113,934 / **34,819**；**StarterKit 337,362 / 105,469** | ⚠️ **受 schema 约束，schema 外有损** | 极活跃（2026-09-17） |
| **Lexical**（Meta） | **0.51.0** | **MIT** ✅ | 195,527 / **62,158** | ⚠️ 受 schema 约束；`@lexical/html` 提供导入导出 | 活跃，有 nightly |
| **ProseMirror** | `prosemirror-view` **1.42.4** | **MIT** ✅ | view 903,885 解包 | ⚠️ 受 schema 约束（它就是 TipTap 的引擎） | 活跃 |
| **Quill** | **2.0.3**（2024-11-30） | **BSD-3-Clause** ✅ | 197,224 / **57,468** | ⚠️ Delta 模型，有损 | ⚠️ **放缓，末次提交 2025-07-25** |
| **GrapesJS** | **0.23.6**（2026-08-25） | **BSD-3-Clause** ✅ | 1,103,086 / **286,959**（本表最大） | ✅ **任意 HTML 保真度最佳**（真正的页面构建器，内置 CodeMirror 5 做源码视图） | 活跃 |
| **Slate** | **0.126.2** | **MIT** ✅ | 未测 | ⚠️ **完全没有 HTML 模型**，序列化器要自己写 | 活跃（2026-09-13） |
| 🚫 TinyMCE | 8.9.1 | GPLv2+ / 商业 | 12.0 MB 解包 | 好 | 极活跃 |
| 🚫 CKEditor 5 | 48.5.1 | GPL2+ / 商业 | 42.5 MB 解包 | 好 | 极活跃 |
| Froala | 5.4.0 | **商业**（npm license 字段是一个报价 URL） | 11.7 MB 解包 | 好 | 商业 |

**关键的机制性认识**：**所有基于 schema 的编辑器（TipTap、Lexical、ProseMirror、Slate、Quill）对其 schema 之外的 HTML 都是"设计上有损"的**。因此"载入这段 HTML → 编辑 → 存回可辨认的同一份 HTML"这个诉求，诚实的选项只有三个：

1. **CodeMirror 6（源码）+ sandboxed iframe 实时预览** —— 对源码 **100% 保真**、零 HTML 变异、体积最小、成本最低。**推荐，而且它字面上就是"直接编辑 HTML"**。
2. **GrapesJS** —— 如果必须**可视化**编辑真实世界 HTML；代价是体积和宽度需求（它需要的是"主区域"，不是"窄栏"）。
3. **原生 `contenteditable` + 严格不做归一化** —— 保真度最高，但撤销/消毒/选区要全部自己实现。

⚠️ **安全（与许可证无关但同等重要）**：任何消费任意 HTML 的 WYSIWYG 都会把它渲染进活的 DOM。对"模型生成/用户上传"的 HTML，必须用 DOMPurify 之类消毒，并明确决策 `<script>` / `<iframe>` / 事件处理器在保存时是否保留。**源码模式的 CodeMirror 面板在编辑路径上完全绕开了这个风险**（但仍须在 sandboxed iframe 中预览）。

**针对"直接编辑并预览 HTML"的选择建议**：

- **如果 HTML 面板的语义是"编辑一段富文本内容块"** → **TipTap（MIT）** 或 **Lexical（MIT）**，两者都 MIT、都支持 HTML 输入输出、都有 React 19 支持。AI 产品建议 TipTap（生态与 AI 扩展更成熟）或 Lexical（无商业授权纠葛）。
- **如果 HTML 面板的语义是"编辑一个完整 HTML 页面 / 模板（含 <style>、布局）"** → **GrapesJS（BSD-3-Clause）** 是更贴合的工具；或者干脆用 **CodeMirror 6（源码模式）+ iframe 实时预览**（最轻、最可控，无 schema 损失）。
- **强烈建议考虑"CodeMirror 源码 + iframe 预览"作为 HTML 面板的默认形态**：对 AI 产品而言，HTML 往往由模型生成、需要精确可控与可回写，任何 WYSIWYG 的 schema 归一化都会引入"用户看到的内容 ≠ 实际 HTML"的失真。**源码模式 + 预览的 round-trip 保真度是 100%。**

来源：[TinyMCE License key 文档（GPLv2+ / 自托管必须有效 key）](https://www.tiny.cloud/docs/tinymce/latest/license-key/)、[CKEditor 5 License and legal（GPL 2+ / 商业授权）](https://raw.githubusercontent.com/ckeditor/ckeditor5/master/docs/getting-started/licensing/license-and-legal.md)、[Lexical npm 0.51.0 MIT](https://registry.npmjs.org/lexical/latest)、[Quill npm 2.0.3 BSD-3-Clause](https://registry.npmjs.org/quill/latest)、[GrapesJS LICENSE (BSD-3-Clause)](https://raw.githubusercontent.com/GrapesJS/grapesjs/master/LICENSE)

---

## E. 关键结论总表

### E.1 主表：方案 / 许可证 / 闭源商用可用性 / round-trip 保真度 / 包体积 / 集成工作量 / 是否需服务端

工作量口径：**1 名熟练前端，含基础集成与联调，不含产品设计与美术，不含深度保真度攻坚**。⚠️ 为粗估，用于横向比较，不可当排期承诺。

| # | 方案 | 许可证 | 闭源商用可用性 | round-trip 保真度 | 包体积 | 集成工作量(人日) | 需服务端 |
|---|---|---|---|---|---|---|---|
| **A. 电子表格** | | | | | | | |
| A1 | **AG Grid Community + SheetJS CE 0.20.3** | MIT + Apache-2.0 | ✅ **推荐**（需 SheetJS attribution） | 数据/样式良；**无公式引擎** | 20.6 MB + 7.2 MB | **8–15** | ❌ 否 |
| A2 | **RevoGrid + SheetJS CE** | MIT + Apache-2.0 | ✅ 可用 | 同上；虚拟滚动性能更好 | 7.2 MB + 7.2 MB | 8–15 | ❌ 否 |
| A3 | Univer OSS | Apache-2.0 | ⚠️ **无 xlsx 读写**，仅编辑 | — | 7.2 MB | 10–18 | ❌ 否 |
| A4 | **Univer Pro** | **npm 无 license 字段**（商业授权） | ❌ 需付费+评估水印限制 | ✅ 强 | 7.2 MB+ | **25–45** | ✅ **是**（转换服务端） |
| A5 | Handsontable (+HyperFormula) | 专有（$999+/dev）；**HyperFormula GPL-3.0-only** | ❌ 需商业授权（双份） | 强（含公式） | 33.0 MB + 12.3 MB | 10–18 | ❌ 否 |
| A6 | Luckysheet | MIT | ✅ 但**已停维护 5 年** | ⚠️ 老旧、需 Luckyexcel | 中 | 8–15 | ❌ 否 |
| A7 | x-spreadsheet | MIT | ✅ 可用但能力弱 | 弱 | 1.2 MB | 4–8 | ❌ 否 |
| A8 | jspreadsheet-ce | ⚠️ registry 无 license | ⚠️ **需先核实许可证** | 中 | 0.3 MB | 5–10 | ❌ 否 |
| A9 | ExcelJS | MIT | ✅ 可用（**3 年未更新**） | 读写强、样式强 | 21.8 MB | 6–12 | ❌ 否 |
| **B. Word** | | | | | | | |
| B1 | ⭐ **@docx-editor.dev/react（Core）** | **Apache-2.0** | ✅ **推荐，$0**，官方明示可商用 | ✅ **原生 OOXML，lossless round-trip**（93 项矩阵） | core 6.8 MB | **8–20** | ❌ 否 |
| B2 | @docx-editor.dev Pro | 专有 **$500/月/产品** | ⚠️ 需付费（含修订/批注/协作/API） | ✅ 同上 | core 6.8 MB | +5–10 | ❌ 否 |
| B3 | **SuperDoc 2.16** | **AGPL-3.0** | ❌ 需买商业授权（未公开报价） | ✅ **DOCX 原生，最强**（分页/页眉页脚/节） | 9.2 MB | 10–20 | ❌ 否 |
| B4 | **ONLYOFFICE Docs** | **AGPL-3.0 + §7 附加条款** | ❌ 需买商业授权；有强制 UI 归属与修改标注义务；**禁止白标** | ✅ Office 级 | 服务端（deb ~700 MB / exe ~1.04 GB） | **25–50**（含运维） | ✅ **是**（Docker 4C4G/40GB + 存储 + callbackUrl 后端，**后端强制**） |
| B5 | **Collabora Online** | **MPL-2.0 源码**（可执行形式为 proprietary 附加条款）✅ | ✅ **许可证友好**（无网络条款）；**CODE 不可用于生产，生产须买 COOL（€3/用户/月 ≤99 用户，明码标价）** | ⚠️ 中（受 LibreOffice 限制） | 服务端镜像 + WASM | **30–60**（需自写 WOPI host） | ✅ **是** |
| B6 | TipTap + mammoth + docx.js | MIT + BSD-2 + MIT | ✅ 许可证全绿 | ❌ **非 round-trip**，页眉页脚/分页/浮动图形/公式/修订全丢 | mammoth 2.1 + docx 4.4 + tiptap core 34.8 kB gzip | 6–12 | ❌ 否 |
| B6b | **Tiptap Conversion（付费）** | 专有（$49/月起，Beta） | ⚠️ 需订阅 | ⚠️ 官方自认：浮动图片/文本框/形状/节/脚注导出**均不支持**，分页需 Team 档 | — | 8–15 | ❌ 否（可本地部署） |
| B7 | docx-preview / docxjs | Apache-2.0 | ✅ | ❌ **仅渲染** | 小 | 2–4 | ❌ 否 |
| **C. PPT** | | | | | | | |
| C1 | ⭐ **pptx-viewer（pptx-react-viewer）** | **Apache-2.0** | ✅ **推荐**，明示可闭源商用 | ✅ 强（原生读写+保存，16 类元素/SmartArt/图表） | **40 MB**（react）/ 23 MB（core） | **8–20** | ❌ 否 |
| C2 | **PPTist** | **AGPL-3.0** | ❌ 需商业授权 **¥2999/年 或 ¥5699 永久** | 导入 ~85%+ / 导出 ~95%+ | 中（Vue 3 应用） | 10–20（+iframe 集成） | ❌ 否（但需自接后端） |
| C3 | pptxgenjs | MIT | ✅ | ❌ **仅生成，不能读** | 2.5 MB | 3–6 | ❌ 否 |
| C3b | **pptxtojson** | **MIT** | ✅ | ✅ **解析**（PPTX→JSON，~80–95%） | 小 | 2–4 | ❌ 否 |
| C4 | 自建画布编辑器（pptxtojson + pptxgenjs） | MIT | ✅ | 取决于自建质量（**不建议**） | — | **30–60**（只读）→ **120–200**（基础编辑）→ **300–500**（完整）→ **600–1000+**（近 95% 保真） | ❌ 否 |
| C5 | ONLYOFFICE（pptx） | AGPL-3.0 + 附加条款 | ❌ 同上 | ✅ Office 级 | 服务端 | 25–50 | ✅ **是** |
| C6 | `ranuts/document`（ONLYOFFICE WASM） | **AGPL-3.0 + §7（必须保留 ONLYOFFICE logo）** | ❌ 不可用（架构极具参考价值） | ✅ Office 级，**完全纯前端** | `x2t.wasm` ~42 MB | 5–10（若许可证允许） | ❌ 否 |
| **D. HTML** | | | | | | | |
| D1 | ⭐ **CodeMirror 6** | **MIT** | ✅ **推荐** | ✅ **100%（源码模式）** | basicSetup **118.8 kB gzip 实测**（HTML+CSS+JS 组合 ~140–175 kB gzip 为估算） | **2–5** | ❌ 否 |
| D2 | Monaco Editor | MIT | ✅（但**移动端官方不支持**） | ✅ 100% | **数 MB**（单 chunk gzip ~1.4 MB） | 4–8（含 Vite worker） | ❌ 否 |
| D3 | **TipTap 核心** | MIT ✅（10 个原 Pro 扩展已于 2025-06 转 MIT；Comments/DOCX I/O 等仍付费） | ⚠️ 受 schema 约束 | core 34.8 kB gzip / StarterKit 105.5 kB gzip | 4–8（**注意 React 19 UI Components 兼容缺口 + bug #7543**） | ❌ 否 |
| D4 | **Lexical** | MIT | ✅ | ✅ 高（`@lexical/html`） | 62.2 kB gzip | 4–8 | ❌ 否 |
| D5 | **GrapesJS** | BSD-3-Clause | ✅ | ✅ **整页 HTML 保真度最佳** | 287.0 kB gzip（最大） | 6–12 | ❌ 否 |
| D6 | Quill | BSD-3-Clause | ✅ | ⚠️ 中（Delta 模型有损） | 57.5 kB gzip | 3–6（维护放缓） | ❌ 否 |
| D7 | 🚫 **TinyMCE 8.9.1** | **GPLv2+** | ❌ **不可用**（自托管无有效 key 即禁用；商业使用需 `licensekeymanager`） | — | 12.0 MB 解包 | — | ❌ 否 |
| D8 | 🚫 **CKEditor 5 (48.5.1)** | **GPL 2+ / 商业双授权**（**非 LGPL**） | ❌ **不可用**（需商业授权） | — | 42.5 MB 解包 | — | ❌ 否 |
| D9 | Ace | BSD-3-Clause | ✅ 可用（触控弱、架构老） | ✅ 100% | ~55 MB 解包（全模式） | 3–6 | ❌ 否 |

### E.2 按需求的推荐组合

**方案甲（推荐）—— 纯前端、零授权成本、全 Apache/MIT/BSD：**

| 格式 | 选型 | 许可证 |
|---|---|---|
| Excel | **AG Grid Community + SheetJS CE 0.20.3** | MIT + Apache-2.0 |
| Word | **@docx-editor.dev/react（Core 档）** | Apache-2.0 |
| PPT | **pptx-viewer（pptx-react-viewer）** | Apache-2.0 |
| HTML | **CodeMirror 6**（+ 可选 TipTap/Lexical 富文本模式） | MIT |

- 全部**无需服务端**，全部**允许闭源商用**，零许可证成本。
- 需履行：SheetJS attribution、Apache-2.0 的 LICENSE/NOTICE 保留。
- 总集成工作量粗估：**20–48 人日**。
- 主要风险：`pptx-viewer` 单维护者 + AI 生成来源（须 POC）；`@docx-editor.dev` 字体度量需自备中文字体。

**方案乙 —— 要 Office 级保真度 + 愿意付费/运维：**

- Word / Excel / PPT 全部走 **ONLYOFFICE Docs**（商业授权）或 **Collabora Online**（MPL-2.0，许可证最干净但要自写 WOPI host）。
- 前端 iframe 集成 + 自建文件存储与保存回调后端。
- 工作量粗估：**40–80 人日**（含服务端运维、字体部署、JWT、回调、压测），另加服务器成本与**持续运维人力**。
- 仅当"三种格式的 Office 级保真度"是产品硬需求、且团队有后端运维能力时选择。

**ONLYOFFICE vs Collabora 的取舍（如果走方案乙，这是必答题）：**

| 维度 | ONLYOFFICE Docs | Collabora Online |
|---|---|---|
| **优先选它的理由** | **OOXML 原生**（保真度结构性更优）+ 集成工作量更小 + Developer 版明确为"白标嵌入 SaaS"而设计 | **许可证零义务**（MPL-2.0 不触及你的应用代码）+ **唯一明码标价**（€3/用户/月）+ 无需商业授权谈判 |
| 原生格式 | **OOXML 原生**（.docx/.xlsx/.pptx） | **ODF 原生**，OOXML 走导入导出过滤器 |
| 渲染模型 | **客户端**渲染 → 服务器轻、**浏览器重**（大文件 OOM 风险） | **服务端瓦片** → 客户端轻、**服务器重**（每并发编辑者成本） |
| 许可证/价格 | AGPL-3.0+§7 → 必须买商业授权（Enterprise From $1500 / Developer ~$3500） | MPL-2.0 源码可自建闭源产品；或买 COOL（€3/用户/月 ≤99） |
| 白标 | Community **禁止**；Developer/Enterprise 允许 | 从源码自建即可（**须移除 Collabora 商标**） |
| 免费版能上生产吗 | Community：不推荐（无 SLA、无集群） | CODE：**官方明确不推荐用于生产** |
| 共同短板 | **两者都不执行 MS VBA、都不含 MS 字体、pptx 都是最高风险格式、都需要你自建后端** | 同左 |

**方案丙 —— 只要预览不要编辑（或 PPT 只需"生成"不需"编辑上传件"）：**

- Word → `docx-preview`（Apache-2.0，仅渲染）；Excel → SheetJS 渲染为表格或 AG Grid；PPT → pptx-viewer 只读模式，或直接 **pptxgenjs(MIT) 生成 + 轻量预览**。
- 工作量可压到 **8–15 人日**，风险最低。
- ⚠️ **强烈建议先认真评估方案丙是否就是真实需求**：对 AI 聊天产品而言，"把 AI 生成的文档呈现/微调/导出"和"编辑用户上传的 Office 文件"是两个量级完全不同的产品承诺。**如果是前者，方案丙 + MIT 组件即可，完全不需要引入任何 AGPL 或付费组件。** 建议先按丙上线，再按甲补编辑能力。

### E.3 许可证红线清单（闭源商用，必须避免或必须付费）

| 方案 | 许可证 | 处理方式 |
|---|---|---|
| Handsontable | 专有 | 买授权（$999+/dev），且注意"不得用于竞争产品"条款 |
| HyperFormula | **GPL-3.0-only** | 买商业授权，否则**不能用于闭源** |
| SuperDoc | **AGPL-3.0** | 买商业授权（价格未公开） |
| ONLYOFFICE Docs (CE) | **AGPL-3.0 + §7 附加条款** | 买商业授权（Enterprise From $1500 / Developer 配置器约 $3500）；注意强制 UI 归属、修改标注义务、**禁止白标** |
| PPTist | **AGPL-3.0** | 买商业授权（¥2999/年 或 ¥5699 永久）或开源你的产品 |
| `ranuts/document` | **AGPL-3.0 + §7（必须保留 ONLYOFFICE logo）** | 不可用；如需采用须与法务评估商业授权路径 |
| Univer Pro | npm 无 license 字段 | 联系销售购买；注意 evaluation 水印/大小限制 |
| `@docx-editor.dev/pro`、`/editor-api` | Pro **Evaluation** License | ⚠️ **禁止生产使用**（"You may not use the Software for Production Use"）。生产必须购买 $500/月/产品 |
| **Collabora 官方可执行产物** | proprietary 附加条款 | 若要闭源：**从 MPL-2.0 源码自行构建**，或购买 COOL 订阅（€3/用户/月 ≤99 用户） |
| TinyMCE 8 | **GPLv2+** | 买商业授权，否则自托管会被禁用 |
| CKEditor 5 | **GPL 2+ / 商业**（**非 LGPL**） | 买商业授权 |
| Luckysheet | MIT（但停维护 5 年） | 法律上可用，**工程上不建议** |
| jspreadsheet-ce | registry 无 license 字段 | **使用前必须向作者确认** |
| `onlyoffice-x2t-wasm` | **无 LICENSE 文件**（上游 AGPL） | 许可证不明，**不要用** |

### E.4 明确的不确定项（请勿当作已核实结论）

**已在本轮解决、不再是未知项的**：ONLYOFFICE 定价（Enterprise From $1500 / Developer 约 $3500）与授权限制；Collabora 定价（€3/用户/月）与"MVP 源码 vs proprietary 可执行形式"区分；`ranuts/document` 的许可证（AGPL-3.0 + §7 保留 logo）；Tiptap 付费/免费扩展清单与定价；TinyMCE/CKEditor 的许可证与版本。

**仍然不确定（务必自行验证）**：

1. **`pptx-viewer` 的真实保真度**：官方自述能力很强，但**无第三方评测**，且是**单一维护者 + README 自陈 "Developed with Claude Code"**。**必须用你们真实样本做 POC**（重点：母版/主题继承、图表、SmartArt、渐变、中文文本）。
2. **`@docx-editor.dev` 的真实保真度**：官方 fidelity 矩阵很详细且诚实（明确列出 No/Partial 项），但**是厂商自述，我未独立验证**。**建议 POC**，重点：中文排版与字体度量、复杂表格、页眉页脚、分页位置。
3. **中文字体度量**：`@docx-editor.dev` 官方明确"没有字体文件就不保证与 Word 一致的分页/断行"，其 `fonts` 包只提供 6 种**西文**开源替代。**中文字体的合法授权与度量一致性完全未验证**，这是 Word/PPT 保真度的头号风险。
4. **CodeMirror 6 的语言包体积**：仅 `codemirror@6.0.2` 的 **118,750 B gzip 是实测**；HTML+CSS+JS 组合的 ~140–175 kB gzip **是估算**，请在你自己的 Vite 构建里测。
5. **CodeMirror 6 移动端/触控**：**未找到厂商明确声明**（不同于 Monaco 的明确 "No"），仅有间接证据。桌面优先可忽略。
6. **TipTap UI Components 对 React 19 的正式支持时间表**：官方仅说"在 React 18 上效果最好"，**无 ETA**；且 bug #7543（React 19 + `flushNode`/`ReactNodeViewRenderer`）**仍未修复**。
7. **Univer Pro 的具体价格与授权条款**：官网只有 Contact Us，`/pricing` 404。**未核实**。
8. **Univer 1.0 GA 时间表，以及届时 OSS/Pro 边界是否变化**：目前只有 alpha/beta/rc。**未核实**。
9. **SuperDoc 商业授权价格**：需通过 form 联系，**未核实**。
10. **jspreadsheet-ce 的许可证**：npm 元数据无 `license` 字段，**未核实**（CE 项目通常为 MIT，但必须确认）。
11. **`onlyoffice-x2t-wasm`（CryptPad 系）在 WASM 形式下的许可证**：仓库**无 LICENSE 文件**，上游为 AGPL-3.0 → 稳妥假设是 AGPL，但**分发形式下的具体条款未声明**。
12. **PPTist 的 85%/95% 是作者自评**，非独立实测；且 **PPTist 无 tagged release**（"版本"= master 截至 2026-09-19），演示站明确不是受支持的服务。
13. **PPTist 把 `pptxgenjs` 钉在 `^3.12.0`（当前 4.0.1）**：是否有意为之**未核实**（v4 改变了 Node 探测与 Vite/Web Worker 行为）。
14. **`pptx-viewer` 的 40 MB 包体积对首屏的实际影响未实测**（需在你自己的构建里做 code-splitting 后测量）。
15. **各家商业版（Syncfusion / Aspose / WPS / Zoho / Google Slides）2026 年定价未能读取**：仅能断言"商业、非开源"。**不要引用任何具体价格。**
16. **`pptx-preview` / 历史 `PPTXjs`** 仅核实到包元数据层，渲染保真度**未实测**。
17. **本报告中所有"人日"数字均为工程判断，不是实测数据**，锚点是 PPTist 的 2020-12 → 2026-09 时间线。仅用于量级比较，**不可当排期承诺**。

**仅针对服务端方案（ONLYOFFICE / Collabora）的额外不确定项：**

18. **Docker 镜像体积**：`hub.docker.com` 及其 JSON API 在本环境不可达 → **未核实**。
19. **ONLYOFFICE 按连接数的单价、档位分界点、批量折扣、完整商业授权条款文本**（登录门禁）→ **未核实**。
20. **Collabora 官方 SDK 文档与官方容量指南**：`sdk.collaboraonline.com`（含 `CO-SDK-manual.pdf`）在 **Anubis 反爬**后不可读；WOPI/postMessage/tutorial 链接**仅是指针、未读取**。`help.collaboraonline.com` **DNS 不解析**。
21. **Collabora COOL 的精确镜像名**，以及 CODE 与 COOL 镜像除品牌/支持外是否有差异 → **未核实**。
22. **Collabora 当前官方字体清单**：所引清单来自 **2022 年**论坛帖 → `[STALE-RISK]`。
23. **CVE-2025-24796 / CVE-2026-77276 的 CVSS 与受影响版本**：NVD 与 GitHub advisory 不可达，仅有标题与厂商描述。
24. **LibreOffice/Impress Bugzilla 详情**（403）："Impress 不支持行内公式"这一结论**仅基于 bug 标题**。
25. **没有任何独立的、动手实测的视觉保真度基准**可用于两者。厂商营销（"100% fidelity"）与 `mso-test` 的**完整性**数字都**不是视觉保真度证明**。**本报告作者未实际测试这两个产品。**
26. **两个 ONLYOFFICE 官方页面仍断言 Community 有 20 连接限制**（Enterprise FAQ、compare-editions），与 9.4 changelog/blog **矛盾** → `[STALE-RISK]`，以 changelog 为准。
27. **ONLYOFFICE 是否保证 `.docm`/`.xlsm` 中的 VBA 被保留**：**未找到任何官方保证**，只有已修复的 bug #3466 作为证据。
28. **两者官方的最大文档大小数值上限** → 未取得。

---

## F. 落地建议（行动清单）

1. **立刻做 POC（各 1–2 天）**，用你们**真实的用户文件**（含中文、复杂表格、图表、页眉页脚的文件）：
   - Word：`@docx-editor.dev/react`（Apache-2.0）——重点验证**中文字体度量与分页是否与 Word 一致**（官方明确说没有字体文件就不保证断行/分页一致）。
   - PPT：`pptx-viewer`（Apache-2.0）——重点验证 round-trip 后**母版/主题继承、图表、SmartArt、渐变、中文文本**是否漂移，以及 40 MB 包体积在 code-splitting 后的实际首屏影响。
   - Excel：SheetJS CE 0.20.3 + AG Grid——重点确认**是否需要公式计算**（若需要，SheetJS CE 无法满足，必须重新选型）。
   - HTML：CodeMirror 6 + sandboxed iframe——**最简单，基本无风险**，可作为第一块落地。
2. **明确功能边界，尤其是三件事**：① 是否需要**公式计算**（决定 Excel 方案）；② 是否需要**修订痕迹/批注/协作**（决定是否买 docx-editor Pro $500/月 或 SuperDoc 商业授权）；③ PPT 是"编辑用户上传的 pptx"还是"呈现 AI 生成的内容"（决定 pptx-viewer vs pptxgenjs 轻量路线）。
3. **把"字体"当作一等公民**：Word/PPT 的保真度瓶颈几乎总在字体度量。提前解决中文字体的**合法授权 + 字体文件分发（含子集化与体积）**，否则 POC 结论会失真。注意 `@docx-editor.dev/fonts` 只提供 6 种**西文**开源替代。
4. **HTML 面板默认用 CodeMirror 源码 + iframe 预览**（100% round-trip、体积最小），仅在明确需要时叠加 TipTap/Lexical 富文本模式，并接受 schema 归一化带来的失真。**预览必须放在 sandboxed iframe 里，编辑路径必须消毒 HTML（DOMPurify）。**
5. **不要为了"看起来省事"引入 ONLYOFFICE/Collabora**，除非 Office 级保真度是硬需求。它会带来：**一个需要长期运维的有状态服务 + 强制后端（ONLYOFFICE 的 `callbackUrl` / Collabora 的 WOPI host）+ JWT + 字体体系 + 不能白标（ONLYOFFICE Community）**。
6. **把许可证核对写进 CI**：本次调研里 **TinyMCE、CKEditor 5、Handsontable、HyperFormula、PPTist、SuperDoc、Univer Pro、`@docx-editor.dev/pro`、`ranuts/document`** 都是"文档看起来免费、实际不可闭源商用"的典型（其中 `@docx-editor.dev/pro` 与 TinyMCE 8 最阴险：包能在公共 npm 装到 / 许可证明写禁止生产使用 / 无 key 直接禁用编辑器）。建议引入 `license-checker` 或 `pnpm licenses list` 做依赖许可证门禁。
7. **对有能力的团队，做一次 30 分钟的法务沟通**：把 E.3 的红线清单交给法务，重点是"AGPL §13 对 SAAS 的实际含义"以及 SheetJS/Apache-2.0 的 attribution 履行方式。
8. **若走服务端方案，有 4 条高杠杆的保真度/成本加固（来自服务端方向的深度调研）**：
   - **入站统一规范化为 OOXML（`.docx/.xlsx/.pptx`）再交给编辑器**。这一条同时绕开 ONLYOFFICE 的"`.doc` 不能另存为 `.doc`"回退问题，和 Collabora 的 ODF 转换面 —— 是两家**性价比最高的保真度加固**。
   - **提前定下宏与字体的预期**：两家**都不跑 MS VBA**（ONLYOFFICE 只有沙箱化 JS 宏；Collabora 宏默认关闭且**无法在线编辑宏**）；两家**都不含 MS 字体**。要么接受度量替换，要么**为服务端渲染购买字体授权并安装**（ONLYOFFICE 有 Admin Panel 字体管理，相对最省事），要么直接拒绝宏格式上传。
   - **把 `.pptx` 当作两家共同的最高风险格式**，用你们用户真实 deck 语料做测试。ONLYOFFICE 有多个**仍开放**的 pptx bug；Collabora 自己的文档把母版背景图形与 AutoShapes 列入"必须人工重排"清单，且 Impress 不支持行内公式。
   - **有意识地选择渲染模型**：ONLYOFFICE＝客户端渲染（服务器便宜，但 ~25 MB 复杂 docx 可能让浏览器 OOM）；Collabora＝服务端瓦片（客户端便宜，但每并发编辑者有 CPU/RAM 成本，且两套容量估算相差约 3 倍 → **必须压测**）。这是真实的成本/扩展性决策，不是纯粹的技术细节。
   - **安全**：两者都在服务端解析不受信 OOXML/ODF，都有持续的 CVE 历史。**把编辑服务隔离在独立主机/网段**（注意 ONLYOFFICE 的 `ALLOW_PRIVATE_IP_ADDRESS=false`、Collabora 的 chroot + seccomp），保持补丁，且**优先选用受支持的正式版本而非滚动发布的 CODE**。

---

## 附 A：本次调研的核实方法说明

- 所有**许可证**结论均来自**一手来源**：仓库的 `LICENSE` / `COPYING` / `LICENSE.txt` / `LICENSE.md` 原文（`raw.githubusercontent.com`），或 npm registry 元数据中的 `license` 字段，或厂商官方文档/定价页原文。**未依赖二手博客或评测文章。**
- 所有**版本号**均来自 `registry.npmjs.org/<pkg>/latest` 的实时查询（2026-09-21）。
- **包体积**：npm `dist.unpackedSize` 为**解包后大小**（不等于打包后传输体积，仅用于横向比较）；标注为"实测"的 gzip 数字来自 Bundlephobia。
- 凡**无法从一手来源确认**的，已在 E.4 逐条列出并标注"未核实"，**未做推测填充**。
- 工作时间线（为避免误读）：**本轮 2026-09-21**。**Univer 尚未 GA 1.0**（只有 alpha/beta/rc）；**ONLYOFFICE 9.4 已于 2026-05-19 发布并取消 CE 连接限制**；**CodeMirror 主仓库已迁出 GitHub**。

## 附 B：工作区中同时产出的支撑报告

以下文件由本次调研过程写入工作区，含更详细的逐项证据与内联来源链接，可作为本报告的补充：

| 文件 | 内容 |
|---|---|
| `office-editor-tech-selection-2026-09.md` | **本报告**（总表 + 结论 + 红线清单） |
| `docx-browser-editor-research-2026-09-21.md` | Word/docx 方向深度报告（mammoth 官方哲学原文、Tiptap 付费清单与 React 19 兼容缺口、EigenPal 包名变迁链） |
| `research-pptx-and-html-editors-2026-09-21.md` | PPTX + HTML 编辑器深度报告（`pptxtojson` 数据模型、自建工作量分级、CodeMirror/Monaco 体积实测、TinyMCE/CKEditor 许可证逐字引用、12 项不确定项） |
| `document-editing-servers-license-report.md` | ONLYOFFICE / Collabora 服务端方案深度报告（定价配置器读数、Docker 依赖、WOPI/callbackUrl 集成契约、资源基准） |
