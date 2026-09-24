import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, Folder as FolderIcon, Plus, Trash2 } from 'lucide-react'
import { ChatSidePanel, ChatSidePanelHeader } from '@/components/chat/chat-side-panel'
import { ProgressRing } from '@/components/ui/progress-ring'
import { useConversationFiles } from '@/store/conversation-files'
import { useConversations } from '@/store/conversations'
import { useWorkspaces } from '@/store/workspaces'
import { fileIconFor } from '@/lib/file-icon'
import { fileFolderTree, type FolderTreeNode } from '@/lib/folder-attachments'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'
import type { ApiConversationFile } from '@/api/types'

/**
 * ConversationFilesPanel — the right-edge drawer listing every file the
 * conversation actually references (§ conversation files). Uploading here is
 * identical to attaching in the composer; removing detaches the file so future
 * turns stop seeing it while its originating message keeps a deleted-file
 * marker. Shares the right column with the HTML preview + inline-thread drawers.
 */
export function ConversationFilesPanel() {
  const open = useConversationFiles((s) => s.open)
  const close = useConversationFiles((s) => s.close)
  const { t } = useTranslation('chat')
  const { pathname } = useLocation()

  // Leaving the page closes the drawer — it's pinned to one conversation.
  const prevPath = useRef(pathname)
  useEffect(() => {
    if (prevPath.current === pathname) return
    prevPath.current = pathname
    close()
  }, [pathname, close])

  return (
    <ChatSidePanel open={open} title={t('files.title')} onClose={close}>
      <FilesBody onClose={close} />
    </ChatSidePanel>
  )
}

function FilesBody({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation('chat')
  const files = useConversationFiles((s) => s.files)
  const conversationId = useConversationFiles((s) => s.conversationId)
  const loading = useConversationFiles((s) => s.loading)
  const uploading = useConversationFiles((s) => s.uploading)
  const uploadJob = useConversationFiles((s) => s.uploadJob)
  const upload = useConversationFiles((s) => s.upload)
  const remove = useConversationFiles((s) => s.remove)
  const conversation = useConversations((s) => s.conversations.find((item) => item.id === conversationId))
  const isWorkspaceGuest = useWorkspaces((s) =>
    conversation?.workspaceId
      ? s.workspaces.find((workspace) => workspace.id === conversation.workspaceId)?.role === 'guest'
      : false,
  )
  const inputRef = useRef<HTMLInputElement>(null)
  // Belt-and-suspenders against a microtask-window double-click: the store
  // removes the row optimistically (synchronously) so it unmounts almost
  // immediately, but this also disables the trash button in the meantime.
  const [removingId, setRemovingId] = useState<string | null>(null)
  // Which directory rows are open. Collapsed by default: the point of the tree
  // is that a 300-file project is one row until the user asks for more.
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const uploadPercent = Math.max(0, Math.min(100, Math.round(uploadJob?.progress ?? 0)))
  const uploadLabel = uploadJob
    ? uploadJob.phase === 'processing'
      ? t('files.processing')
      : t('files.uploadingPercent', { percent: uploadPercent })
    : t('files.uploading')

  // One row per uploaded folder — and per subdirectory inside it — instead of
  // one row per file. A single-file upload has no relPath and stays a top-level
  // row, exactly as before.
  const tree = useMemo(
    () => fileFolderTree(files, (file) => ({ relPath: file.rel_path, size: file.size_bytes })),
    [files],
  )

  function toggleFolder(path: string) {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  async function onRemoveFolder(node: FolderTreeNode<ApiConversationFile>) {
    if (isWorkspaceGuest || removingId) return
    const ids: string[] = []
    const collect = (current: FolderTreeNode<ApiConversationFile>) => {
      current.files.forEach((file) => ids.push(file.id))
      current.children.forEach(collect)
    }
    collect(node)
    for (const id of ids) await onRemove(id)
  }

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    if (isWorkspaceGuest) return
    const list = e.target.files
    if (!list || !list.length) return
    try {
      // Conversation files are sandbox inputs regardless of the active model's
      // native vision capability. Provider serialization handles image stripping
      // for text-only models; the original bytes remain available to Python.
      await upload(Array.from(list))
      toast.success(t('files.added'))
    } catch {
      toast.error(t('files.addFailed'))
    } finally {
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  async function onRemove(id: string) {
    if (isWorkspaceGuest) return
    if (removingId) return
    if (!conversationId) return
    const targetConversationId = conversationId
    setRemovingId(id)
    try {
      await remove(id)
      useConversations
        .getState()
        .markAttachmentsDeleted([id], targetConversationId)
    } catch {
      toast.error(t('files.removeFailed'))
    } finally {
      setRemovingId(null)
    }
  }

  return (
    <>
      <ChatSidePanelHeader title={t('files.title')} closeLabel={t('files.close')} onClose={onClose} />

      {!isWorkspaceGuest ? <div className="px-3 pt-3 shrink-0">
        <input
          ref={inputRef}
          type="file"
          multiple
          hidden
          onChange={(e) => void onPick(e)}
        />
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          disabled={uploading}
          className={cn(
            'inline-flex w-full items-center justify-center gap-1.5 h-9 rounded-[10px] text-sm font-medium interactive',
            'border border-dashed border-[var(--color-border)] text-[var(--color-fg-muted)]',
            'hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] disabled:opacity-60',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          )}
        >
          {uploading ? (
            <ProgressRing value={uploadPercent} size={22} strokeWidth={2.5} showValue label={uploadLabel} />
          ) : (
            <Plus size={14} aria-hidden />
          )}
          <span className="min-w-0 truncate">{uploading ? uploadLabel : t('files.add')}</span>
        </button>
        {uploading && uploadJob ? (
          <p className="mt-1 truncate px-1 text-[11px] text-[var(--color-fg-subtle)]">{uploadJob.name}</p>
        ) : null}
      </div> : null}

      <p className="px-4 pt-2.5 pb-1 text-[11px] leading-snug text-[var(--color-fg-subtle)] shrink-0">
        {t('files.hint')}
      </p>

      <div className="flex-1 min-h-0 overflow-y-auto px-3 py-2">
        {loading ? (
          <div className="grid h-32 place-items-center text-sm text-[var(--color-fg-subtle)]">
            {t('files.loading')}
          </div>
        ) : files.length === 0 ? (
          <div className="grid h-32 place-items-center px-4 text-center text-sm text-[var(--color-fg-muted)]">
            {t('files.empty')}
          </div>
        ) : (
          <ul className="flex flex-col gap-1">
            {tree.folders.map((folder) => (
              <FolderRows
                key={`folder:${folder.path}`}
                node={folder}
                depth={0}
                expanded={expanded}
                onToggle={toggleFolder}
                onRemoveFile={(id) => void onRemove(id)}
                onRemoveFolder={(node) => void onRemoveFolder(node)}
                removingId={removingId}
                readOnly={isWorkspaceGuest}
              />
            ))}
            {tree.rootFiles.map((f) => (
              <FileRow
                key={f.id}
                file={f}
                depth={0}
                readOnly={isWorkspaceGuest}
                removing={removingId === f.id}
                onRemove={() => void onRemove(f.id)}
              />
            ))}
          </ul>
        )}
      </div>
    </>
  )
}

function formatBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  const v = n / Math.pow(1024, i)
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}

interface FileRowProps {
  file: ApiConversationFile
  depth: number
  readOnly: boolean
  removing: boolean
  onRemove: () => void
}

/** One file. `depth` indents it inside the directory that contains it. */
function FileRow({ file, depth, readOnly, removing, onRemove }: FileRowProps) {
  const { t } = useTranslation('chat')
  const Icon = fileIconFor(file.filename, file.kind)
  return (
    <li
      style={{ paddingLeft: `${0.625 + depth * 0.875}rem` }}
      className="group/file flex items-center gap-2.5 rounded-[10px] border border-transparent py-2 pr-2.5 hover:border-[var(--color-border)] hover:bg-[var(--color-surface)]"
    >
      <Icon size={16} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
      <a href={file.url} target="_blank" rel="noreferrer" className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-[13px] text-[var(--color-fg)]">{file.filename}</span>
        <span className="text-[11px] text-[var(--color-fg-subtle)]">{formatBytes(file.size_bytes)}</span>
      </a>
      {!readOnly ? (
        <button
          type="button"
          onClick={onRemove}
          disabled={removing}
          aria-label={t('files.remove', { name: file.filename })}
          className="inline-flex size-7 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-0 interactive hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] group-hover/file:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <Trash2 size={14} aria-hidden />
        </button>
      ) : null}
    </li>
  )
}

interface FolderRowsProps {
  node: FolderTreeNode<ApiConversationFile>
  depth: number
  expanded: Set<string>
  onToggle: (path: string) => void
  onRemoveFile: (id: string) => void
  onRemoveFolder: (node: FolderTreeNode<ApiConversationFile>) => void
  removingId: string | null
  readOnly: boolean
}

/**
 * A directory row plus, when open, its files and subdirectories.
 *
 * This is what puts an uploaded folder back together in the drawer: the folder
 * the user picked is one row (name, file count, size) and its subdirectories are
 * rows inside it, rather than every file appearing as an interchangeable sibling.
 */
function FolderRows({
  node,
  depth,
  expanded,
  onToggle,
  onRemoveFile,
  onRemoveFolder,
  removingId,
  readOnly,
}: FolderRowsProps) {
  const { t } = useTranslation('chat')
  const open = expanded.has(node.path)
  return (
    <li className="flex flex-col">
      <div
        style={{ paddingLeft: `${0.625 + depth * 0.875}rem` }}
        className="group/folder flex items-center gap-1 rounded-[10px] border border-transparent py-2 pr-2.5 hover:border-[var(--color-border)] hover:bg-[var(--color-surface)]"
      >
        <button
          type="button"
          onClick={() => onToggle(node.path)}
          aria-expanded={open}
          aria-label={
            open
              ? t('composer.folderCollapse', { defaultValue: 'Collapse {{folder}}', folder: node.name })
              : t('composer.folderExpand', { defaultValue: 'Expand {{folder}}', folder: node.name })
          }
          className="flex min-w-0 flex-1 items-center gap-2 text-left interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          {open ? (
            <ChevronDown size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
          ) : (
            <ChevronRight size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
          )}
          <FolderIcon size={16} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden />
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate text-[13px] font-medium text-[var(--color-fg)]" title={node.path}>
              {node.name}
            </span>
            <span className="text-[11px] text-[var(--color-fg-subtle)]">
              {t('files.folderSummary', {
                defaultValue: '{{count}} files · {{size}}',
                count: node.fileCount,
                size: formatBytes(node.size),
              })}
            </span>
          </span>
        </button>
        {!readOnly ? (
          <button
            type="button"
            onClick={() => onRemoveFolder(node)}
            disabled={Boolean(removingId)}
            aria-label={t('composer.folderRemoveAll', { defaultValue: 'Remove {{folder}}', folder: node.name })}
            className="inline-flex size-7 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-0 interactive hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] group-hover/folder:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <Trash2 size={14} aria-hidden />
          </button>
        ) : null}
      </div>
      {open ? (
        <ul className="flex flex-col gap-1">
          {node.children.map((child) => (
            <FolderRows
              key={`folder:${child.path}`}
              node={child}
              depth={depth + 1}
              expanded={expanded}
              onToggle={onToggle}
              onRemoveFile={onRemoveFile}
              onRemoveFolder={onRemoveFolder}
              removingId={removingId}
              readOnly={readOnly}
            />
          ))}
          {node.files.map((file) => (
            <FileRow
              key={file.id}
              file={file}
              depth={depth + 1}
              readOnly={readOnly}
              removing={removingId === file.id}
              onRemove={() => onRemoveFile(file.id)}
            />
          ))}
        </ul>
      ) : null}
    </li>
  )
}
