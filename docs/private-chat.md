# 隐私对话

首页右上角的隐私图标打开 `/private-chat`。这是独立于普通聊天的新通道，不是普通会话上的一个“不显示历史”开关。

前端沿用普通聊天的布局和侧边栏，侧边栏仅显示原有普通会话，不加入隐私内容。桌面空白页的输入框居中，移动端输入框位于底部；数据销毁提示放在输入框占位文字中，不显示额外说明段落。隐私页不挂载普通会话的排队发送器、文件或工具预览面板。

## 数据边界

- 消息、思考内容、草稿和图片只存于当前页面的 React 状态和内存引用，不进入普通会话 store、localStorage、sessionStorage、IndexedDB 或侧边栏。
- 刷新、清空、离开页面时销毁页面状态，并取消当前请求；浏览器往返缓存的 `pagehide` / `pageshow` 也会清空内容。自动版本更新不会打断有内容的隐私页，但用户主动刷新仍会清空。
- 隐私页使用个人账户的模型目录和额度，不绑定工作区、项目、知识库或普通会话。
- 服务端仅在请求生命周期内处理正文，不创建 conversations、messages、files、documents 或 artifacts 记录，不向 Redis、任务队列或可重放 SSE 缓存写入消息。
- 使用日志与计费台账仍关联当前账户，记录模型、用量、费用、状态和随机的单次计费标识。后台标题固定显示“匿名对话”，没有可打开的对话链接。这里的“匿名”指没有对话标题和正文，不表示费用无法关联账户。
- 即使管理员开启完整请求日志和请求正文日志，隐私调用也不保存请求 URL、Header、Body 或上游原始错误。错误只记录固定类别。

## 能力与限制

- 仅可显式选择已启用的普通对话模型，不使用隐藏快速模型。
- 不接入文件上传、联网搜索、MCP、内置工具、技能、记忆、RAG、标题生成、意图路由、自动摘要、审校、绘图工具或模型降级路由。
- 唯一允许的辅助模型请求是管理员为当前模型配置的内容审核；审核也走隐私链路，不记录审核正文。关键词审核与已有的审核失败回退规则保留。
- 只有管理员启用 `vision` 的模型显示图片选择按钮，服务端每次发送重新校验。对话中已有图片时，前端禁止切到非识图模型，避免静默丢失上下文。
- 图片仅允许 PNG、JPEG、WebP、GIF，前端转为 base64，不调用 `/api/files`。单张不超过 5 MiB，并受现有部署图片上限约束；整个对话最多 16 张图片、127 条历史消息、1 MiB 文本，完整 JSON 请求最多 32 MiB。达到上限时提示清空，不调用摘要模型。
- 服务商适配器复用现有 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages、Gemini GenerateContent 的原生消息与内联图片格式。隐私请求移除工具、远程会话引用、显式缓存及标识字段；OpenAI 请求强制 `store: false`。异常返回的工具调用不会执行或继续工具循环。
- Markdown 使用不带工具操作的独立渲染器，不执行 HTML、Mermaid 或代码，不自动加载回答中的远程图片。显式点击外部链接使用 `noreferrer`。
- 隐私内容仅发送至签名的 `POST /api/private-chat`，所选模型目录独立读取；共用侧边栏可正常读取普通会话及导航数据，但不接收隐私消息。SSE 不支持断线重放，停止或断开会取消服务商请求。已经消耗的用量仍结算。

## API

请求要求登录和现有 HMAC 请求签名。只接受下面的字段，未知字段拒绝：

```json
{
  "model_id": "configured-chat-model-id",
  "messages": [
    {
      "role": "user",
      "text": "描述这张图片",
      "images": [
        { "mime_type": "image/png", "data": "BASE64_IMAGE_BYTES" }
      ]
    }
  ]
}
```

`messages` 必须从 user 开始、按 user/assistant 交替、以 user 结束。多轮请求由客户端携带内存中的历史，不返回或接受持久会话 ID、文件 URL、工具记录或服务商原始状态。思考内容不作为后续轮次的历史重放。

响应为 `Cache-Control: no-store, no-transform` 的 SSE，事件包括 `text_delta`、`thinking_delta`、内联 `image`、`done` 和安全错误码 `error`。请求超时为 10 分钟，每 15 秒发送保活注释。

## 部署必须检查

应用不落库、不落文件，不等同于整个部署环境或模型服务商的零保留承诺。实际生产部署需要同时审查以下边界：

1. **反向代理 / CDN / WAF / APM**：关闭该路径的请求正文记录、响应正文记录、采样追踪、镜像和缓存。不能使用会把大请求暂存到磁盘的默认代理缓冲配置。
2. **Nginx**：为 `/api/private-chat` 单独设置以下规则，并保留现有部署的上游地址、TLS、安全 Header 和访问控制。不要在其他层重新打开正文缓冲或正文日志。

```nginx
location = /api/private-chat {
    client_max_body_size 32m;
    client_body_buffer_size 32m;
    client_body_in_file_only off;
    proxy_http_version 1.1;
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_cache off;
    proxy_max_temp_file_size 0;
    proxy_read_timeout 660s;
    proxy_send_timeout 660s;
    access_log off;
    proxy_pass http://127.0.0.1:8787;
}
```

3. **主机与运行时**：避免会导出进程内存的 core dump、调试采样和错误追踪；高隐私部署应禁用 swap 或采用符合要求的加密与密钥管理。刷新销毁是解除应用引用，不是对所有物理内存作安全擦除。
4. **模型服务商及中转渠道**：正文必然发送给管理员配置的上游。`store: false` 和不创建远程缓存不能替代其滥用监测、数据保留政策或账户级零保留协议。
5. **浏览器**：扩展程序、系统截图和用户主动复制/保存不在应用可控制范围内。请勿把“隐私对话”描述为端到端加密或对服务商不可见。

## 验证

```sh
npm run typecheck
npx vitest run tests/frontend/lib/private-chat.test.ts tests/frontend/lib/private-chat-i18n.test.ts tests/frontend/components/private-markdown.test.tsx
cd server
go test ./internal/api ./internal/llm ./internal/store -run TestPrivate
```

测试覆盖原生图片请求格式、工具与持久化字段移除、审核模型隔离、取消与计费、日志脱敏、无会话/文件落库、签名正文限制、图片识别权限、Markdown 外部资源隔离及五语言文案完整性。服务商协议测试使用本地模拟上游，不消耗真实服务商额度。
