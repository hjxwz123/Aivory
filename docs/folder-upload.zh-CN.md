# 文件夹上传

文件夹上传仅在沙盒已配置、用户组允许文件上传及 `python_execute`、工作空间允许文件上传和工具调用时开放。工作空间访客不能上传。普通单文件附件仍使用 `/api/files`，行为不变。

## 流程

1. 浏览器使用 File System Access API 选择目录；不支持时回退到 `webkitdirectory`。保留选中目录下所有文件的原始字节和相对路径，不按扩展名过滤、不压缩图片、不解析内容。
   拖拽目录不会进入普通文件附件流程；界面会提示使用“上传文件夹”入口。
2. 浏览器在一次 multipart 请求中提交目录名、路径清单和全部文件到 `POST /api/conversations/:id/sandbox/folders`。文件数上限 300、总大小上限 200 MiB、单文件上限 40 MiB；超限时拒绝整个选择，不静默丢弃文件。
3. API 校验会话访问、用户组与工作空间权限、目录路径和大小后，为该会话创建或复用沙盒会话。文件原始字节直接转交沙盒 sidecar，写入 `/workspace/folders/<目录名>/...`。API 不写 `UPLOAD_DIR`、`files` 或 `documents` 表，也不触发 RAG/文档解析。
4. 目录保存在会话沙盒工作区，`python_execute` 可直接遍历 `/workspace/folders`。普通附件仍在执行工具时暂存到 `/workspace/uploads`；重置普通附件输入不会清除 `/workspace/folders`。

## 安全与边界

- 服务端拒绝绝对路径、空路径段、`.`、`..`、控制字符、超长路径段、重复路径及与 multipart 文件名不符的路径。sidecar 还会校验目标的真实路径必须留在 `/workspace` 内。
- 上传前及每个文件写入沙盒前重新检查权限，防止上传过程中撤销权限后继续写入。
- 工作空间工具调用开关及工具允许列表都必须放行 `python_execute`；已废弃的 `AllowSandbox` 开关不再单独影响新权限判定。
- 请求按 multipart 流读取，每次最多持有一个受限大小的文件；sidecar 的单文件写入接口不解析文件内容。
- 空目录没有文件可提交，不会在沙盒中出现。沙盒工作区按实例配置归档和回收，不应视为永久存储。
- 单次请求写入 sidecar 时若中途失败，已写入的文件可能留在沙盒；界面会明确提示。重新上传同名路径会覆盖相应文件。清理完整工作区使用现有沙盒清理功能。
- 旧版本已写入 `files.rel_path` 的目录附件保持只读兼容，继续在工具执行时暂存于 `/workspace/uploads`；新的 `/api/files` 请求不再接受 `folder_name`/`rel_path`。

## 验证

- `go test ./internal/api -run 'TestSandboxFolderUpload|TestLegacyFileEndpointRejectsFolderMetadata'`
- `go test ./internal/store ./internal/tools`
- `tsc -b --noEmit`
