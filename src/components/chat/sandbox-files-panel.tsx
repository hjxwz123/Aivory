import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, ChevronRight, Folder, Home, RefreshCw } from 'lucide-react'
import { conversationsApi } from '@/api/endpoints'
import { ChatSidePanel, ChatSidePanelHeader } from '@/components/chat/chat-side-panel'
import { FilePreview } from '@/components/chat/file-preview'
import { Tooltip } from '@/components/ui/tooltip'
import { fileIconFor } from '@/lib/file-icon'
import { sandboxEntriesAtPath, sandboxFileKind } from '@/lib/sandbox-browser'
import { cn } from '@/lib/utils'
import { useSandboxFiles } from '@/store/sandbox-files'
import type { Attachment } from '@/types/chat'

interface PreviewTarget {
  name: string
  url: string
  kind: Attachment['kind']
  authenticated: true
}

export function SandboxFilesPanel() {
  const open = useSandboxFiles((state) => state.open)
  const close = useSandboxFiles((state) => state.close)
  const { t } = useTranslation('chat')
  const { pathname } = useLocation()
  const previousPath = useRef(pathname)

  useEffect(() => {
    if (previousPath.current === pathname) return
    previousPath.current = pathname
    close()
  }, [pathname, close])

  return (
    <ChatSidePanel open={open} title={t('sandbox.title')} onClose={close}>
      <SandboxFilesBody onClose={close} />
    </ChatSidePanel>
  )
}

function SandboxFilesBody({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation('chat')
  const conversationId = useSandboxFiles((state) => state.conversationId)
  const files = useSandboxFiles((state) => state.files)
  const session = useSandboxFiles((state) => state.session)
  const currentPath = useSandboxFiles((state) => state.currentPath)
  const loading = useSandboxFiles((state) => state.loading)
  const unavailable = useSandboxFiles((state) => state.unavailable)
  const error = useSandboxFiles((state) => state.error)
  const load = useSandboxFiles((state) => state.load)
  const enter = useSandboxFiles((state) => state.enter)
  const up = useSandboxFiles((state) => state.up)
  const [preview, setPreview] = useState<PreviewTarget | null>(null)
  const entries = useMemo(() => sandboxEntriesAtPath(files, currentPath), [currentPath, files])
  const breadcrumbs = currentPath ? currentPath.split('/') : []

  function previewFile(name: string, path: string) {
    if (!conversationId) return
    setPreview({
      name,
      url: conversationsApi.sandboxFileUrl(conversationId, path),
      kind: sandboxFileKind(name),
      authenticated: true,
    })
  }

  return (
    <>
      <ChatSidePanelHeader title={t('sandbox.title')} closeLabel={t('sandbox.close')} onClose={onClose}>
        <Tooltip content={t('sandbox.refresh')}>
          <button
            type="button"
            onClick={() => void load()}
            disabled={loading}
            aria-label={t('sandbox.refresh')}
            className="interactive inline-flex size-8 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : undefined} aria-hidden />
          </button>
        </Tooltip>
      </ChatSidePanelHeader>

      <nav aria-label={t('sandbox.location')} className="flex h-10 shrink-0 items-center gap-0.5 border-y border-[var(--color-divider)] px-3">
        <Tooltip content={t('sandbox.back')}>
          <button
            type="button"
            onClick={up}
            disabled={!currentPath}
            aria-label={t('sandbox.back')}
            className="interactive mr-1 inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <ArrowLeft size={14} aria-hidden />
          </button>
        </Tooltip>
        <button
          type="button"
          onClick={() => enter('')}
          aria-label={t('sandbox.root')}
          className="interactive inline-flex size-7 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <Home size={13} aria-hidden />
        </button>
        <div className="flex min-w-0 items-center overflow-hidden">
          {breadcrumbs.map((part, index) => {
            const path = breadcrumbs.slice(0, index + 1).join('/')
            return (
              <span key={path} className="flex min-w-0 items-center">
                <ChevronRight size={12} className="shrink-0 text-[var(--color-fg-faint)]" aria-hidden />
                <button
                  type="button"
                  onClick={() => enter(path)}
                  className={cn(
                    'interactive min-w-0 truncate rounded-[6px] px-1.5 py-1 text-[12px] hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    index === breadcrumbs.length - 1 ? 'text-[var(--color-fg)]' : 'text-[var(--color-fg-muted)]',
                  )}
                >
                  {part}
                </button>
              </span>
            )
          })}
        </div>
      </nav>

      <div className="flex-1 min-h-0 overflow-y-auto px-3 py-3">
        {loading ? (
          <div className="grid h-32 place-items-center text-sm text-[var(--color-fg-subtle)]">{t('sandbox.loading')}</div>
        ) : error ? (
          <div className="grid h-32 place-items-center px-4 text-center text-sm text-[var(--color-fg-muted)]">{t('sandbox.loadFailed')}</div>
        ) : !session ? (
          <div className="grid h-32 place-items-center px-4 text-center text-sm text-[var(--color-fg-muted)]">{t('sandbox.none')}</div>
        ) : unavailable ? (
          <div className="grid h-32 place-items-center px-4 text-center text-sm text-[var(--color-fg-muted)]">{t('sandbox.unavailable')}</div>
        ) : entries.length === 0 ? (
          <div className="grid h-32 place-items-center px-4 text-center text-sm text-[var(--color-fg-muted)]">{t('sandbox.empty')}</div>
        ) : (
          <ul className="flex flex-col gap-1">
            {entries.map((entry) => {
              const Icon = entry.type === 'directory' ? Folder : fileIconFor(entry.name, sandboxFileKind(entry.name))
              return (
                <li
                  key={entry.path}
                  className="group/file flex items-center gap-2.5 rounded-[10px] border border-transparent px-2.5 py-2 hover:border-[var(--color-border)] hover:bg-[var(--color-surface)]"
                >
                  <Icon size={16} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                  {entry.type === 'file' ? (
                    <button
                      type="button"
                      onClick={() => previewFile(entry.name, entry.path)}
                      className="min-w-0 flex-1 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <span className="block truncate text-[13px] text-[var(--color-fg)]">{entry.name}</span>
                      <span className="text-[11px] text-[var(--color-fg-subtle)]">{formatBytes(entry.size)}</span>
                    </button>
                  ) : (
                    <button
                      type="button"
                      onDoubleClick={() => enter(entry.path)}
                      onKeyDown={(event) => { if (event.key === 'Enter') enter(entry.path) }}
                      className="min-w-0 flex-1 truncate text-left text-[13px] text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      {entry.name}
                    </button>
                  )}
                  {entry.type === 'directory' ? (
                    <button
                      type="button"
                      onClick={() => enter(entry.path)}
                      aria-label={t('sandbox.openFolder', { name: entry.name })}
                      className="interactive inline-flex size-7 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-faint)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <ChevronRight size={14} aria-hidden />
                    </button>
                  ) : null}
                </li>
              )
            })}
          </ul>
        )}
      </div>

      <FilePreview open={Boolean(preview)} onOpenChange={(open) => { if (!open) setPreview(null) }} file={preview} />
    </>
  )
}

function formatBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  const value = bytes / Math.pow(1024, index)
  return `${index === 0 ? value : value.toFixed(1)} ${units[index]}`
}
