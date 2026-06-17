import { useEffect, useRef } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { FolderOpen, Plus, Trash2, X, Loader2 } from 'lucide-react'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { useConversationFiles } from '@/store/conversation-files'
import { useMediaQuery } from '@/hooks/use-media-query'
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
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const { t } = useTranslation('chat')
  const { pathname } = useLocation()

  // Leaving the page closes the drawer — it's pinned to one conversation.
  const prevPath = useRef(pathname)
  useEffect(() => {
    if (prevPath.current === pathname) return
    prevPath.current = pathname
    close()
  }, [pathname, close])

  if (isDesktop) {
    if (!open) return null
    return (
      <aside
        aria-label={t('files.title')}
        className={cn(
          'hidden lg:flex flex-col shrink-0 h-full w-[clamp(20rem,30vw,30rem)]',
          'border-l border-[var(--color-divider)] bg-[var(--color-bg)]',
          'animate-[panel-in_240ms_var(--ease-out)]',
        )}
      >
        <FilesBody onClose={close} />
      </aside>
    )
  }

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) close() }}>
      <SheetContent side="right" size="lg" label={t('files.title')} className="w-[min(26rem,94vw)] p-0">
        <FilesBody onClose={close} />
      </SheetContent>
    </Sheet>
  )
}

function FilesBody({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation('chat')
  const files = useConversationFiles((s) => s.files)
  const loading = useConversationFiles((s) => s.loading)
  const uploading = useConversationFiles((s) => s.uploading)
  const upload = useConversationFiles((s) => s.upload)
  const remove = useConversationFiles((s) => s.remove)
  const inputRef = useRef<HTMLInputElement>(null)

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    const list = e.target.files
    if (!list || !list.length) return
    try {
      await upload(list)
      toast.success(t('files.added'))
    } catch {
      toast.error(t('files.addFailed'))
    } finally {
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  async function onRemove(id: string) {
    try {
      await remove(id)
    } catch {
      toast.error(t('files.removeFailed'))
    }
  }

  return (
    <>
      <header className="flex items-center gap-2 h-12 px-3 border-b border-[var(--color-divider)] shrink-0">
        <FolderOpen size={14} aria-hidden className="text-[var(--color-fg-muted)]" />
        <span className="flex-1 min-w-0 truncate font-serif tracking-tight text-[15px] text-[var(--color-fg)]">
          {t('files.title')}
        </span>
        <Tooltip content={t('files.close')}>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('files.close')}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <X size={14} aria-hidden />
          </button>
        </Tooltip>
      </header>

      <div className="px-3 pt-3 shrink-0">
        <input ref={inputRef} type="file" multiple hidden onChange={(e) => void onPick(e)} />
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
          {uploading ? <Loader2 size={14} className="animate-spin" aria-hidden /> : <Plus size={14} aria-hidden />}
          {uploading ? t('files.uploading') : t('files.add')}
        </button>
      </div>

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
                  <button
                    type="button"
                    onClick={() => void onRemove(f.id)}
                    aria-label={t('files.remove', { name: f.filename })}
                    className="inline-flex size-7 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-0 interactive hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] group-hover/file:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <Trash2 size={14} aria-hidden />
                  </button>
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
