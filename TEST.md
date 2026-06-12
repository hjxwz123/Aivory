# Aurelia — 本地测试指令

把每一步的输出贴回来，我会据此修。

---

## 1. 安装依赖

```bash
cd /www/chat && rm -rf node_modules package-lock.json && npm install --no-audit --no-fund 2>&1 | tail -40
```

期望看到：
- `added NNN packages` 类成功信息。
- 没有 `ERESOLVE` 报错（如果有依赖冲突，把 npm 输出全贴出来）。

如果包安装失败，先把这一步报错贴出来再继续。

---

## 2. 类型检查（最关键）

```bash
cd /www/chat && npx tsc -b --pretty false 2>&1 | head -200
```

期望：**0 errors**，命令静默退出（或只输出空行）。

如果有报错：
- 把所有报错完整贴回来（最多 200 行）。
- 我会根据具体 `error TSxxxx` 修。

---

## 3. ESLint

```bash
cd /www/chat && npx eslint . --max-warnings=999 2>&1 | tail -60
```

期望：要么 `0 errors 0 warnings`，要么只有 warnings、没有 errors。把 errors 的报告贴回来。

---

## 4. 构建

```bash
cd /www/chat && npm run build 2>&1 | tail -50
```

期望：
- `tsc -b` 静默通过。
- Vite 输出类似：
  ```
  ✓ XX modules transformed.
  dist/assets/index-XXXXX.css   YY.YY kB
  dist/assets/index-XXXXX.js   YYY.YY kB
  ✓ built in X.XXs
  ```

如果失败，贴出完整错误。

---

## 5. dev 跑起来人工验

```bash
cd /www/chat && npm run dev
```

启动后浏览器打开：

### 5.1 首页（Landing）
- 路径：`/`
- 检查：
  - Hero 大标题 + 副标题居中。
  - Hero 下面有一个 composer（输入框）。
  - 在 hero composer 里输入 `outline an essay on attention`，回车 → 应跳到 `/chat/<某id>` 并开始流式输出。
  - 滚动到底部 — 各个 section 间距宽松、有呼吸感。
  - 截图首屏发给我。

### 5.2 Chat
- 路径：`/chat` 或点 hero 的发送按钮。
- 空状态：「Good [morning/evening/etc], Astrid.」+ 4 张建议卡。
- 点任一建议卡 → 跳新对话开始流式输出。
- 输入框里：
  - 测试 `⌘/Ctrl + Enter` 发送。
  - 测试 `Shift + Enter` 换行。
  - 测试 `Esc` 关闭弹窗。
  - 测试 `⌘/Ctrl + K` 打开命令菜单。
  - 测试 `⌘/Ctrl + B` 折叠 / 展开侧边栏。
- 流式输出过程中按「停止」按钮 — 应停止。
- 鼠标 hover 一条消息 → 出现 copy / regenerate / 点赞 / 点踩 / 更多 操作。
- 点用户消息的「编辑」铅笔 → 进入编辑模式，改字后 `⌘+Enter` 保存并重生成。
- 侧边栏点对话项的「⋯」 → 重命名 / 删除 / 归档。
- 截图聊天页（含流式）发给我。

### 5.3 Auth
- 路径：`/login`、`/register`、`/forgot-password`
- 表单提交：
  - 空字段提交 → 应显示 error。
  - 填好 → 短暂 loading → toast 「Welcome back」并跳 /chat。
- 截图 login 发给我。

### 5.4 Settings
- 路径：`/settings/account`、`/settings/appearance`、`/settings/models`、`/settings/privacy`、`/settings/shortcuts`、`/settings/billing`
- Appearance 页面切 Light / Dark / System — 整个页面应该平滑切换、无白屏闪。
- Models 页面：切默认模型、改 custom instructions、点保存 → toast 提示。
- Shortcuts 页面：键盘 chip 显示正常。
- Billing 页面：3 张定价卡，当前 plan 标 Most popular。
- 截图 Appearance 浅 + 深各一张发给我。

### 5.5 404
- 路径：随便输 `/whatever-does-not-exist`。
- 应显示「Lost the thread.」serif 大标题。

### 5.6 移动端
- 浏览器调成 iPhone 视图（DevTools 切换设备）。
- `/chat` 应：
  - 顶部小 bar + 汉堡 → 点击展开 drawer-sidebar。
  - composer 固定底部。
  - 消息一栏适配窄屏。
- 截图发我。

### 5.7 浏览器控制台
- 跑完一遍以上各页面，看控制台是否有红色 error。把任意 error 截图或文本发我。

---

## 6.（可选）一次性运行所有静态检查

```bash
cd /www/chat && \
  echo "=== TYPECHECK ===" && npx tsc -b --pretty false 2>&1 | head -60 ; \
  echo "=== LINT ===" && npx eslint . --max-warnings=999 2>&1 | tail -30 ; \
  echo "=== BUILD ===" && npm run build 2>&1 | tail -20
```

---

## 反馈格式

把每一步输出按顺序贴回来即可。如果 `tsc` 有报错，请只贴前 30-60 行就够 — 我会逐个修复。
