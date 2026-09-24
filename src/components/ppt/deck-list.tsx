/**
 * "My PPT" list for the self-built AI PPT flow (§ AI PPT API mode).
 *
 * Reads our own deck records (never the vendor's listing) so ownership and
 * ordering are ours, and shows the mirrored file's availability.
 */
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowRight, Presentation, Trash2 } from 'lucide-react'

import { aipptApi, ApiError } from '@/api'
import type { ApiAiPPTDeck, ApiAiPPTDeckStatus } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/hooks/use-toast'

interface DeckListProps {
  onOpen: (deck: ApiAiPPTDeck) => void
  onCreate: () => void
  /** Bumped by the page after a generation so the list refetches. */
  refreshToken: number
}

const STATUS_VARIANT: Record<ApiAiPPTDeckStatus, 'neutral' | 'success' | 'warning' | 'danger'> = {
  draft: 'neutral',
  outline_ready: 'neutral',
  generating: 'warning',
  ready: 'success',
  failed: 'danger',
}

export function DeckList({ onOpen, onCreate, refreshToken }: DeckListProps) {
  const { t, i18n } = useTranslation('ppt')
  const [decks, setDecks] = useState<ApiAiPPTDeck[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<ApiAiPPTDeck | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [brokenCovers, setBrokenCovers] = useState<Set<string>>(() => new Set())

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const page = await aipptApi.decks({ limit: 50 })
      setDecks(page.decks)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t('errors.generic'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void load()
  }, [load, refreshToken])

  async function remove(deck: ApiAiPPTDeck) {
    setDeleting(true)
    try {
      await aipptApi.deleteDeck(deck.id)
      setDecks((current) => current.filter((item) => item.id !== deck.id))
      toast.success(t('decks.deleted'))
    } catch (err) {
      toast.error(t('errors.generic'), err instanceof ApiError ? err.message : undefined)
    } finally {
      setDeleting(false)
      setConfirming(null)
    }
  }

  if (loading && decks.length === 0) {
    return (
      <div role="status" aria-label={t('common:common.loading')} className="min-h-0 flex-1 overflow-y-auto">
        {Array.from({ length: 6 }).map((_, index) => (
          <div key={index} className="flex items-center gap-4 border-b border-[var(--color-divider)] py-4">
            <Skeleton className="aspect-video w-24 shrink-0 rounded-[8px] sm:w-36" />
            <div className="flex-1 space-y-3"><Skeleton className="h-4 w-2/3" /><Skeleton className="h-3 w-1/3" /></div>
          </div>
        ))}
      </div>
    )
  }
  if (error && decks.length === 0) {
    return (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('decks.loadFailed')}
        description={error}
        action={<Button variant="secondary" onClick={() => void load()}>{t('common:actions.tryAgain')}</Button>}
      />
    )
  }
  if (decks.length === 0) {
    return (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('decks.empty')}
        description={t('decks.emptyHint')}
        action={<Button size="sm" onClick={onCreate}>{t('result.newDeck')}</Button>}
      />
    )
  }

  return (
    <>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <p className="mb-3 text-sm leading-6 text-[var(--color-fg-muted)]">{t('decks.lead')}</p>
        <div className="divide-y divide-[var(--color-divider)]">
          {decks.map((deck) => {
            const cover = deck.cover_url && !brokenCovers.has(deck.id) ? aipptApi.resourceUrl(deck.cover_url) : null
            const title = deck.subject || t('decks.untitled')
            const updated = new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' })
              .format(new Date(deck.updated_at * 1000))
            return (
              <article key={deck.id} className="group flex items-center gap-2 py-2">
                <button
                  type="button"
                  onClick={() => onOpen(deck)}
                  aria-label={`${deck.status === 'ready' ? t('decks.open') : t('decks.continue')}: ${title}`}
                  className="flex min-w-0 flex-1 items-center gap-3 rounded-[10px] py-3 pr-2 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:gap-5 sm:pr-4"
                >
                  <span className="flex aspect-video w-20 shrink-0 items-center justify-center overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] sm:w-36">
                    {cover ? (
                      <img src={cover} alt="" loading="lazy" className="h-full w-full object-contain"
                        onError={() => setBrokenCovers((current) => new Set(current).add(deck.id))} />
                    ) : <Presentation size={22} strokeWidth={1.5} aria-hidden />}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                      <span className="line-clamp-2 break-words text-sm font-medium text-[var(--color-fg)]">{title}</span>
                      <Badge variant={STATUS_VARIANT[deck.status]}>{t(`decks.status.${deck.status}`)}</Badge>
                    </span>
                    <span className="mt-2 block text-xs leading-5 text-[var(--color-fg-muted)]">
                      <time dateTime={new Date(deck.updated_at * 1000).toISOString()}>{updated}</time>
                      {deck.credits > 0 ? ` · ${t('decks.credits', { credits: deck.credits })}` : ''}
                    </span>
                    {deck.error ? (
                      <span className="mt-1 line-clamp-2 break-words text-xs text-[var(--color-danger)]">{deck.error}</span>
                    ) : !deck.file_id && deck.status === 'ready' ? (
                      <span className="mt-1 block text-xs text-[var(--color-fg-muted)]">{t('result.mirrorPending')}</span>
                    ) : null}
                  </span>
                  <ArrowRight size={16} aria-hidden className="hidden shrink-0 text-[var(--color-fg-muted)] sm:block" />
                </button>
                <Button size="icon" variant="ghost" aria-label={`${t('decks.delete')}: ${title}`} onClick={() => setConfirming(deck)}>
                  <Trash2 size={15} aria-hidden />
                </Button>
              </article>
            )
          })}
        </div>
      </div>

      <Dialog open={confirming !== null} onOpenChange={(open) => (!open ? setConfirming(null) : null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('decks.deleteTitle')}</DialogTitle>
            <DialogDescription>{t('decks.deleteBody')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirming(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              loading={deleting}
              disabled={deleting}
              onClick={() => confirming && void remove(confirming)}
            >
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
