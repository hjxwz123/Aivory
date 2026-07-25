import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AlertCircle,
  AlertTriangle,
  Check,
  Copy,
  Link2,
  Trash2,
} from 'lucide-react'
import { conversationsApi, ApiError } from '@/api'
import type { ApiShareInfo } from '@/api/types'
import { useCopy } from '@/hooks/use-clipboard'
import { toast } from '@/hooks/use-toast'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'

type LoadState = 'idle' | 'loading' | 'ready' | 'error'
type BusyAction = 'share' | 'revoke' | null

function publicShareUrl(shareId?: string): string {
  if (!shareId || typeof window === 'undefined') return ''
  return `${window.location.origin}/share/${encodeURIComponent(shareId)}`
}

interface ShareConversationDialogProps {
  conversationId: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ShareConversationDialog({
  conversationId,
  open,
  onOpenChange,
}: ShareConversationDialogProps) {
  const { t } = useTranslation(['chat', 'common'])
  const [share, setShare] = useState<ApiShareInfo | null>(null)
  const [loadState, setLoadState] = useState<LoadState>('idle')
  const [busyAction, setBusyAction] = useState<BusyAction>(null)
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const requestVersion = useRef(0)
  const { copied, copy } = useCopy()

  const shareUrl = publicShareUrl(share?.id)

  const loadShare = useCallback(async () => {
    const version = ++requestVersion.current
    setShare(null)
    setLoadState('loading')
    try {
      const result = await conversationsApi.getShare(conversationId)
      if (requestVersion.current !== version) return
      setShare(result.share)
      setLoadState('ready')
    } catch {
      if (requestVersion.current !== version) return
      setLoadState('error')
    }
  }, [conversationId])

  useEffect(() => {
    if (!open) {
      requestVersion.current += 1
      setConfirmRevoke(false)
      return
    }
    setConfirmRevoke(false)
    void loadShare()
  }, [loadShare, open])

  function handleOpenChange(next: boolean) {
    if (!next && busyAction) return
    if (!next) setConfirmRevoke(false)
    onOpenChange(next)
  }

  async function saveShare(mode: 'create' | 'update') {
    setBusyAction('share')
    try {
      const nextShare = await conversationsApi.createShare(conversationId)
      setShare(nextShare)
      setLoadState('ready')
      const copied = await copy(publicShareUrl(nextShare.id))
      if (copied) {
        toast.success(
          mode === 'create'
            ? t('chat:share.createdAndCopied')
            : t('chat:share.updatedAndCopied'),
        )
      } else {
        toast.error(
          mode === 'create'
            ? t('chat:share.createdCopyFailed')
            : t('chat:share.updatedCopyFailed'),
        )
      }
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('chat:share.failed'))
    } finally {
      setBusyAction(null)
    }
  }

  async function copyShareLink() {
    if (!shareUrl) return
    const copied = await copy(shareUrl)
    if (copied) toast.success(t('chat:share.linkCopied'))
    else toast.error(t('chat:share.copyFailed'))
  }

  async function revokeShare() {
    setBusyAction('revoke')
    try {
      await conversationsApi.deleteShare(conversationId)
      setShare(null)
      setConfirmRevoke(false)
      toast.success(t('chat:share.revoked'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('chat:share.failed'))
    } finally {
      setBusyAction(null)
    }
  }

  const isLoading = loadState === 'loading' || loadState === 'idle'
  const description = share ? t('chat:share.bodyShared') : t('chat:share.body')

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent
        size="sm"
        showClose={!busyAction}
        aria-busy={busyAction ? true : undefined}
        className="rounded-[22px] border-0 shadow-[var(--shadow-lg)] max-sm:rounded-[24px] sm:max-w-[27rem]"
      >
        <DialogHeader className="px-5 pb-3 pr-12 pt-5 sm:px-6 sm:pb-3 sm:pr-12 sm:pt-6">
          <DialogTitle>{t('chat:share.title')}</DialogTitle>
          <DialogDescription className={isLoading ? 'sr-only' : undefined}>
            {description}
          </DialogDescription>
        </DialogHeader>

        {isLoading || loadState === 'error' || confirmRevoke || share ? (
          <DialogBody className="px-5 pb-1 sm:px-6">
            {isLoading ? (
              <ShareDialogSkeleton label={t('common:common.loading')} />
            ) : loadState === 'error' ? (
              <div
                role="alert"
                className="flex items-start gap-2.5 rounded-[12px] bg-[var(--color-danger-soft)] px-3 py-2.5 text-[var(--color-danger)]"
              >
                <AlertCircle size={18} className="mt-0.5 shrink-0" aria-hidden />
                <p className="text-sm leading-relaxed text-current">
                  {t('chat:share.loadFailed')}
                </p>
              </div>
            ) : confirmRevoke ? (
              <div
                role="alert"
                className="flex items-start gap-2.5 rounded-[12px] bg-[var(--color-warning-soft)] px-3 py-2.5"
              >
                <AlertTriangle
                  size={18}
                  className="mt-0.5 shrink-0 text-[var(--color-warning)]"
                  aria-hidden
                />
                <div className="min-w-0">
                  <p className="text-sm font-semibold text-[var(--color-fg)]">
                    {t('chat:share.revokeConfirmTitle')}
                  </p>
                  <p className="mt-0.5 text-[13px] leading-relaxed text-[var(--color-fg-muted)]">
                    {t('chat:share.revokeConfirmBody')}
                  </p>
                </div>
              </div>
            ) : share ? (
              <div className="flex min-w-0 items-center gap-2 rounded-[12px] bg-[var(--color-bg-muted)] py-1.5 pl-3 pr-1.5">
                <Link2 size={15} className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                <input
                  id="share-conversation-link"
                  type="text"
                  dir="ltr"
                  readOnly
                  spellCheck={false}
                  value={shareUrl}
                  aria-label={t('chat:share.linkLabel')}
                  title={shareUrl}
                  onFocus={(event) => event.currentTarget.select()}
                  className="h-5 min-w-0 flex-1 cursor-text bg-transparent p-0 font-mono text-[11.5px] leading-5 text-[var(--color-fg-muted)] outline-none selection:bg-[var(--color-accent-soft)] selection:text-[var(--color-fg)]"
                />
                <Tooltip content={copied ? t('common:actions.copied') : t('chat:share.copyCta')}>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 shrink-0 rounded-[8px]"
                    aria-label={t('chat:share.copyCta')}
                    onClick={() => void copyShareLink()}
                  >
                    {copied ? <Check size={15} aria-hidden /> : <Copy size={15} aria-hidden />}
                  </Button>
                </Tooltip>
              </div>
            ) : null}
          </DialogBody>
        ) : null}

        {loadState === 'error' ? (
          <DialogFooter className="border-t-0">
            <Button onClick={() => void loadShare()}>
              {t('common:actions.tryAgain')}
            </Button>
          </DialogFooter>
        ) : confirmRevoke ? (
          <DialogFooter className="border-t-0 max-sm:grid max-sm:grid-cols-2">
            <Button
              variant="ghost"
              className="min-w-0 whitespace-normal px-2 leading-tight"
              disabled={Boolean(busyAction)}
              onClick={() => setConfirmRevoke(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              className="min-w-0 whitespace-normal px-2 leading-tight"
              loading={busyAction === 'revoke'}
              leadingIcon={<Trash2 size={15} aria-hidden />}
              onClick={() => void revokeShare()}
            >
              {t('chat:share.confirmRevokeCta')}
            </Button>
          </DialogFooter>
        ) : loadState === 'ready' && !share ? (
          <DialogFooter className="border-t-0">
            <Button
              loading={busyAction === 'share'}
              leadingIcon={<Link2 size={15} aria-hidden />}
              onClick={() => void saveShare('create')}
            >
              {busyAction === 'share'
                ? t('chat:share.creatingLink')
                : t('chat:share.createCta')}
            </Button>
          </DialogFooter>
        ) : loadState === 'ready' && share ? (
          <DialogFooter className="border-t-0 max-sm:grid max-sm:grid-cols-[auto_minmax(0,1fr)]">
            <Button
              variant="ghost"
              className="min-w-0 whitespace-normal px-2.5 leading-tight text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)]"
              disabled={Boolean(busyAction)}
              leadingIcon={<Trash2 size={15} aria-hidden />}
              onClick={() => setConfirmRevoke(true)}
            >
              {t('chat:share.revokeCta')}
            </Button>
            <Button
              className="min-w-0 whitespace-normal px-2 leading-tight"
              loading={busyAction === 'share'}
              leadingIcon={<Copy size={15} aria-hidden />}
              onClick={() => void saveShare('update')}
            >
              {busyAction === 'share'
                ? t('chat:share.updatingAndCopying')
                : t('chat:share.updateAndCopyCta')}
            </Button>
          </DialogFooter>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function ShareDialogSkeleton({ label }: { label: string }) {
  return (
    <div role="status" aria-live="polite" className="pb-3 pt-1">
      <span className="sr-only">{label}</span>
      <Skeleton className="h-11 w-full rounded-[12px]" />
    </div>
  )
}
