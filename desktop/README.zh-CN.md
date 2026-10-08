# Aivory 桌面版

桌面版是加载现有 Aivory 网站的 Electron 壳。对话、流式输出、管理后台、设置、文件
预览等功能全部使用同一套网页与服务端。网站更新后，桌面版下次加载即可使用新的界面，
不需要单独构建一份前端。

## 环境要求

- Node.js 22.12 或更高版本、npm。
- 已部署的 Aivory 网站，同时提供网页和 `/api` 接口。
- 推荐在目标系统上构建。GitHub Actions 已提供 Windows、macOS（Apple Silicon 和
  Intel）、Linux 四组构建。

## 服务器地址

开发和打包时可设置 `AIVORY_DESKTOP_BASE_URL`，例如 `https://chat.example.com`，也可以留空。
填写网站根地址，不要加 `/api`、其他路径、账号密码、查询参数或片段。
本地开发可填写 `http://127.0.0.1:5173`，正式部署请使用 HTTPS。

可以在进程环境变量中设置，也可以参考 [`.env.example`](.env.example)，创建
`desktop/.env`。优先级为：进程环境变量、`desktop/.env.local`、`desktop/.env`。
不会读取根目录或服务端的 `.env`，安装包仅包含校验后的可选默认服务器地址。

首次启动优先读取本地保存的地址；没有保存记录时，将安装包默认地址保存到
Electron `userData/server.json`。两者均没有时，显示随安装包附带的服务器配置页。
后续启动与更新都优先使用本地地址，即使新版安装包默认地址不同或为空也不会覆盖。
图标菜单或“视图 → 服务器设置”可更换地址，打开设置不刷新当前页面，保存同一地址
也保留草稿。更换地址会重新加载，登录 Cookie 和网页存储按服务器隔离。

## 启动与打包

在仓库根目录运行：

```bash
npm run desktop:install

# 连接已经启动的本地 Vite 开发服务
AIVORY_DESKTOP_BASE_URL=http://127.0.0.1:5173 npm run desktop:dev

# 为当前系统构建安装包
AIVORY_DESKTOP_BASE_URL=https://chat.example.com npm run desktop:build

# 通用安装包，首次启动由用户填写地址
AIVORY_DESKTOP_BASE_URL= npm run desktop:build

# 只构建可运行的应用目录，不生成安装程序
AIVORY_DESKTOP_BASE_URL=https://chat.example.com npm run desktop:pack
```

Windows PowerShell：

```powershell
npm run desktop:install
$env:AIVORY_DESKTOP_BASE_URL = "https://chat.example.com"
npm run desktop:dev
npm run desktop:build
```

可以继续传递 electron-builder 参数：

```bash
npm run desktop:build -- --win --x64
npm run desktop:build -- --mac --arm64
npm run desktop:build -- --mac --x64
npm run desktop:build -- --linux --x64
```

版本号自动使用根目录 `package.json`。安装包位于 `desktop/release/`：Windows 为
`.exe`，macOS 为 `.dmg` 和 `.zip`，Linux 为 `.AppImage` 和 `.deb`。图标使用现有
Aivory 当前 Logo 生成的 1024px 图标，再转换为各系统的原生图标格式；开发模式下的
macOS Dock 也使用该图标。桌面构建依赖单独安装，不影响原有网页构建和 Docker 部署。

## GitHub Actions 构建

进入 **Actions → Build desktop app → Run workflow**，可填写默认网址或留空构建通用包，完成后在
该次运行的 Artifacts 中下载各系统安装包。可选 `release_tag` 填写已有的正式发布
Release 标签，四组构建全部成功后，会将安装包上传至该 Release；标签必须与
`package.json` 版本一致。留空则只生成 Artifacts。不会新建 Release 或上传应用商店。

发布 Release（包括测试版）会自动触发构建，使用对应标签的代码，在 GitHub 的
Windows、Linux 和两种架构的 macOS 环境中生成通用安装包。四组构建全部成功后，
自动上传安装包与 `SHA256SUMS.txt` 校验文件至该 Release。自动构建不预设服务器地址，
各部署的用户首次启动时填写自己的网站；更新后沿用已保存的地址。

网页版的“下载 App”入口也在 **管理后台 → 系统 → 桌面客户端**
（`/admin/settings/desktop`）配置。填写下载页面或安装包直链、开启显示并保存后，
新对话欢迎页和头像菜单会在新标签页打开这个地址。桌面客户端隐藏这两个入口。
下载入口默认关闭，与向已有客户端发布更新分别配置。

在 **管理后台 → 系统 → 桌面客户端**（`/admin/settings/desktop`）管理本部署的更新。
后台复用带缓存的 GitHub 版本检测，发现新版本时提醒管理员配置安装包。为自己的
服务器构建客户端，上传到自有 GitHub Release、对象存储或下载服务器，选择“自建安装包”
并填写版本号及各系统、架构的地址；也可选择“官方安装包”，后台自动读取对应 Release
中已上传完成的安装程序。官方包首次使用需填写地址，已配置的客户端更新后沿用本地地址。
确认安装包可用后开启“向用户发布更新”并保存。
选择另一个上游版本会清空旧地址并关闭发布；没有填写地址的平台不会提示更新。

桌面版启动 15 秒后以及之后每 4 小时读取当前网站的 `/api/public/desktop-update`，
也可以从图标菜单、帮助菜单或设置 → 关于手动检查。只有管理员已发布、版本更高，
并有对应系统和架构的安装包地址时，才显示简短的更新确认。稳定版忽略测试版本，
测试版也接受较新的测试版。点击“更新”在系统浏览器中打开管理员配置的安装包地址，
退出应用后安装。客户端只读取管理员已发布的地址；官方包地址在发布时保存，普通
客户端检查不请求 GitHub。

需要部署包含新接口的服务端；旧服务器没有该接口时不提供桌面更新。已安装的旧版
客户端仍使用原来的 GitHub 检测逻辑，需要先安装包含这次修改的客户端。
旧客户端需先运行一次含本地地址保存功能的版本，之后安装通用更新包才能自动沿用地址。

默认 CI 产物未签名。需要正式签名时，在构建环境中提供 electron-builder 支持的
签名变量：macOS 使用 `CSC_LINK`、`CSC_KEY_PASSWORD`，Windows 使用
`WIN_CSC_LINK`、`WIN_CSC_KEY_PASSWORD`。macOS 公证还需要 Apple 凭据和相应的
Developer ID 证书。使用构建环境变量或 CI Secrets 提供凭据，不要提交到代码仓库。
electron-builder 会在正常构建流程中处理签名。

## 运行行为

- 主窗口关闭按钮只最小化，保留窗口内容和任务栏 / Dock 图标。macOS 顶部菜单栏、
  Windows / Linux 系统托盘均显示 Aivory 图标；macOS 图标随系统深浅色自适应。在 Dock 图标
  菜单选择“退出 Aivory”；Windows / Linux 在系统托盘图标菜单选择退出，Windows
  任务栏图标菜单也提供退出任务。应用菜单的退出、系统注销与关闭仍能真正结束进程。
  macOS 环境不支持原生最小化时，会隐藏主窗口并保留 Dock 图标；点击图标即可恢复。
- 登录 Cookie 和本地设置在重启后保留，不同服务器的会话相互隔离。
- 桌面请求的 User-Agent 附加 `AivoryDesktop/{version}`；反馈、登录记录、会话设备和
  审计日志的客户端位置显示“App 版”。浏览器发起的记录继续显示原浏览器。
- `/api`、SSE 流式请求、WebSocket、上传和预览继续使用网站同源地址。
- 支付的整页跳转保留在桌面窗口内，使用同一个会话。
- 站内新窗口、Blob 预览使用隔离的桌面窗口；外部网站的新窗口链接使用系统浏览器。
- 文件下载使用系统保存对话框，语音功能可申请麦克风权限。
- 远程网页无法访问 Node.js 或任意 Electron API。隔离的预加载脚本提供四个受限入口：
  读取应用版本、浏览器登录、取消浏览器登录、检查更新；主进程仅接受配置站点的主窗口
  主框架调用。保持沙箱、上下文隔离、浏览器安全检查和
  HTTPS 证书校验。
- 原生菜单和连接错误提示跟随系统语言，支持简体中文、繁体中文、英文、日文和法文。
  网页语言、深浅色及主题继续使用原有设置。
- 断网时显示随安装包附带的少线条状态界面，网页无法加载时也能显示。已加载过网页时，
  跟随网页的语言、深浅色和配色；首次启动默认跟随系统语言和深浅色。
- 离线界面覆盖原页面，保留网页进程和草稿；恢复连接后移除覆盖层，不主动刷新原页面。
  首次连接失败可以重试或在浏览器中打开；浏览器报告网络可用时，每 10 秒自动尝试恢复。
  恢复检查只在离线界面显示期间运行，不改变正常请求和流式输出的路径。

服务端的 `ALLOWED_ORIGINS` 按现有网站域名配置即可，不需要增加 `file://` 或桌面端
特殊 CORS 配置。

## 使用系统浏览器登录

登录页面提供“使用浏览器登录”。跳转地址严格使用当前保存的服务器配置，例如
配置 `https://chat.example.com` 会打开
`https://chat.example.com/desktop/authorize?request_id=…`。用户在网站完成现有密码、
第三方登录、通行密钥或两步验证后，确认授权账号；桌面版自动恢复窗口并建立独立会话。
已经在浏览器登录的用户可以直接确认授权。取消或超过 5 分钟，需要重新发起。
桌面登录页只提供浏览器登录与管理员允许的账号密码登录，不展示第三方或通行密钥
登录入口，也不在桌面窗口内自动跳转第三方登录。第三方登录可在系统浏览器中完成，
再授权桌面应用。桌面登录表单隐藏欢迎标题与副标题，二次验证说明仍保留；
普通网页登录方式不变。

此功能要求网站和服务端部署支持 `/api/auth/desktop/*` 的新版代码。授权采用 S256
证明和短期一次性凭据，长期令牌与校验秘密不会进入浏览器 URL。桌面端仅在等待授权
期间每 2 秒查询一次；聊天和流式输出路径不变。复用现有缓存，无需新增数据库表；
多实例部署继续使用既有 Redis 共享缓存。

## 验证

```bash
npm run desktop:test
# 需要已安装根目录的 puppeteer-core 等依赖，并在有图形界面的系统运行
npm run desktop:test:smoke
npm run desktop:test:server
npm run desktop:test:updates
npm run desktop:test:records
npm run desktop:test:auth
npm run desktop:test:download
```

冒烟测试使用真实 Electron 和本地测试服务，覆盖 Cookie、流式响应、新窗口、跳转、
离线恢复、草稿保留、关闭最小化与真正退出、更新提示、浏览器授权以及重启后的会话保留，
结束后关闭测试进程。权限规则测试不会申请
操作系统麦克风权限，临时数据不会写入仓库。
