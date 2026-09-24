/**
 * Docmee editor dialog for a finished deck (§ AI PPT).
 *
 * Our own UI owns creation; real slide-level editing happens in the vendor's
 * editor, which is delivered as an iframe SDK. The deck is saved upstream by that
 * editor, so closing the dialog pulls the file back into the user's own files.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Loader2, RefreshCw } from 'lucide-react'

import { aipptApi, ApiError } from '@/api'
import type { ApiAiPPTDeck } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { createDocmeeEditor, DocmeeEditorError, type DocmeeEditorHandle } from '@/lib/docmee-editor'
import { useAiPPT } from '@/store/aippt'
import { useTheme } from '@/store/theme'

interface DocmeeEditorDialogProps {
  deck: ApiAiPPTDeck | null
  onClose: () => void
  /** Called with the refreshed deck after the edited file was pulled back. */
  onSynced: (deck: ApiAiPPTDeck) => void
}

type Phase = 'loading' | 'ready' | 'error'

export function DocmeeEditorDialog({ deck, onClose, onSynced }: DocmeeEditorDialogProps) {
  const { t, i18n } = useTranslation('ppt')
  const resolvedTheme = useTheme((s) => s.resolved)
  const setAvailable = useAiPPT((s) => s.setAvailable)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const editorRef = useRef<DocmeeEditorHandle | null>(null)
  const [phase, setPhase] = useState<Phase>('loading')
  const [error, setError] = useState<string | null>(null)
  const [syncing, setSyncing] = useState(false)
  /** Only pull the file back if the editor actually mounted. */
  const mountedRef = useRef(false)

  const syncFile = useCallback(
    async (silent: boolean) => {
      if (!deck) return
      setSyncing(true)
      try {
        const result = await aipptApi.refreshFile(deck.id)
        onSynced(result.deck)
        if (!silent) toast.success(t('editor.synced'))
      } catch (err) {
        // A failed pull must not look like a failed edit: the deck is saved
        // upstream either way.
        toast.error(t('editor.syncFailed'), err instanceof ApiError ? err.message : undefined)
      } finally {
        setSyncing(false)
      }
    },
    [deck, onSynced, t],
  )

  const mount = useCallback(async () => {
    if (!deck) return
    setPhase('loading')
    setError(null)
    try {
      const session = await aipptApi.editor(deck.id)
      if (typeof session.credits_available === 'number') setAvailable(session.credits_available)
      const container = containerRef.current
      if (!container) return
      editorRef.current?.destroy()
      editorRef.current = await createDocmeeEditor({
        container,
        token: session.token,
        pptId: session.ppt_id,
        sdkUrl: session.sdk_url,
        domain: session.domain,
        mode: resolvedTheme,
        lang: i18n.language,
      })
      mountedRef.current = true
      setPhase('ready')
    } catch (err) {
      setPhase('error')
      setError(
        err instanceof DocmeeEditorError || err instanceof Error
          ? err.message
          : t('editor.failed'),
      )
    }
  }, [deck, i18n.language, resolvedTheme, setAvailable, t])

  useEffect(() => {
    if (!deck) return
    void mount()
    return () => {
      editorRef.current?.destroy()
      editorRef.current = null
    }
  }, [deck, mount])

  function close() {
    const wasMounted = mountedRef.current
    mountedRef.current = false
    editorRef.current?.destroy()
    editorRef.current = null
    onClose()
    // The editor persists to Docmee; pull the result into our storage so the
    // user's copy matches what they just edited.
    if (wasMounted) void syncFile(true).then(() => toast.success(t('editor.synced')))
  }

  return (
    <Dialog open={deck !== null} onOpenChange={(open) => (!open ? close() : undefined)}>
      <DialogContent className="max-w-6xl p-0">
        <DialogHeader className="px-4 pt-4 sm:px-5">
          <DialogTitle>{deck?.subject || t('editor.title')}</DialogTitle>
        </DialogHeader>

        <div className="relative mx-4 mb-4 h-[75vh] min-h-[420px] overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] sm:mx-5">
          <div ref={containerRef} className="h-full w-full" />
          {phase === 'loading' ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-[var(--color-bg)]">
              <Loader2 size={18} aria-hidden className="animate-spin text-[var(--color-fg-muted)]" />
              <p className="text-xs text-[var(--color-fg-muted)]">{t('editor.loading')}</p>
            </div>
          ) : null}
          {phase === 'error' ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-[var(--color-bg)] px-6 text-center">
              <AlertTriangle size={20} aria-hidden className="text-[var(--color-danger)]" />
              <p className="max-w-md text-sm text-[var(--color-fg-muted)]">{error}</p>
              <Button variant="outline" size="sm" onClick={() => void mount()}>
                <RefreshCw size={13} aria-hidden className="mr-1.5" />
                {t('editor.retry')}
              </Button>
            </div>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--color-divider)] px-4 py-3 sm:px-5">
          <p className="text-xs text-[var(--color-fg-muted)]">{t('editor.lead')}</p>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              loading={syncing}
              disabled={syncing || phase !== 'ready'}
              onClick={() => void syncFile(false)}
            >
              {t('editor.syncNow')}
            </Button>
            <Button size="sm" onClick={close}>
              {t('editor.done')}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
