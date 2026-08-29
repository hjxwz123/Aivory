import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2 } from 'lucide-react'
import { ChatSidePanel, ChatSidePanelHeader } from '@/components/chat/chat-side-panel'
import { ProgressRing } from '@/components/ui/progress-ring'
import { useConversationFiles } from '@/store/conversation-files'
import { useConversations } from '@/store/conversations'
import { useWorkspaces } from '@/store/workspaces'
import { fileIconFor } from '@/lib/file-icon'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

/**
 * ConversationFilesPanel — the right-edge drawer listing every file the
 * conversation actually references (§ conversation files). Uploading here is
 * identical to attaching in the composer; removing detaches the file so future
 * turns stop seeing it (the originating message is untouched). Shares the right
 * column with the HTML preview + inline-thread drawers (mutually exclusive).
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
  const uploadPercent = Math.max(0, Math.min(100, Math.round(uploadJob?.progress ?? 0)))
  const uploadLabel = uploadJob
    ? uploadJob.phase === 'processing'
      ? t('files.processing')
      : t('files.uploadingPercent', { percent: uploadPercent })
    : t('files.uploading')

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
    setRemovingId(id)
    try {
      await remove(id)
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
            {files.map((f) => {
              const Icon = fileIconFor(f.filename, f.kind)
              return (
                <li
                  key={f.id}
                  className="group/file flex items-center gap-2.5 rounded-[10px] border border-transparent px-2.5 py-2 hover:border-[var(--color-border)] hover:bg-[var(--color-surface)]"
                >
                  <Icon size={16} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                  <a
                    href={f.url}
                    target="_blank"
                    rel="noreferrer"
                    className="flex min-w-0 flex-1 flex-col"
                  >
                    <span className="truncate text-[13px] text-[var(--color-fg)]">{f.filename}</span>
                    <span className="text-[11px] text-[var(--color-fg-subtle)]">{formatBytes(f.size_bytes)}</span>
                  </a>
                  {!isWorkspaceGuest ? <button
                    type="button"
                    onClick={() => void onRemove(f.id)}
                    disabled={removingId === f.id}
                    aria-label={t('files.remove', { name: f.filename })}
                    className="inline-flex size-7 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-0 interactive hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] group-hover/file:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <Trash2 size={14} aria-hidden />
                  </button> : null}
                </li>
              )
            })}
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
