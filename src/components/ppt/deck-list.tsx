/**
 * "My PPT" list for the self-built AI PPT flow (§ AI PPT API mode).
 *
 * Reads our own deck records (never the vendor's listing) so ownership and
 * ordering are ours, and shows the mirrored file's availability.
 */
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Clock, FileText, ImageOff, Presentation, Trash2 } from 'lucide-react'

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
import { cn } from '@/lib/utils'

interface DeckListProps {
  onOpen: (deck: ApiAiPPTDeck) => void
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

export function DeckList({ onOpen, refreshToken }: DeckListProps) {
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
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {Array.from({ length: 6 }).map((_, index) => (
          <Skeleton key={index} className="h-40 rounded-[12px]" />
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
        action={
          <Button variant="outline" onClick={() => void load()}>
            {t('template.loadMore')}
          </Button>
        }
      />
    )
  }
  if (decks.length === 0) {
    return (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('decks.empty')}
        description={t('decks.emptyHint')}
      />
    )
  }

  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {decks.map((deck) => {
          const cover =
            deck.cover_url && !brokenCovers.has(deck.id) ? aipptApi.resourceUrl(deck.cover_url) : null
          const updated = new Intl.DateTimeFormat(i18n.language, {
            dateStyle: 'medium',
            timeStyle: 'short',
          }).format(new Date(deck.updated_at * 1000))
          return (
            <article
              key={deck.id}
              className={cn(
                'flex flex-col overflow-hidden rounded-[12px] border border-[var(--color-border)]',
                'bg-[var(--color-surface)]',
              )}
            >
              <button
                type="button"
                onClick={() => onOpen(deck)}
                className="block aspect-[16/9] w-full bg-[var(--color-bg-muted)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                {cover ? (
                  <img
                    src={cover}
                    alt=""
                    loading="lazy"
                    className="h-full w-full object-cover"
                    onError={() => setBrokenCovers((current) => new Set(current).add(deck.id))}
                  />
                ) : (
                  <div className="flex h-full w-full items-center justify-center text-[var(--color-fg-muted)]">
                    <ImageOff size={20} aria-hidden />
                  </div>
                )}
              </button>
              <div className="flex min-w-0 flex-1 flex-col gap-2 p-3">
                <div className="flex items-start gap-2">
                  <h3 className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--color-fg)]">
                    {deck.subject || t('decks.untitled')}
                  </h3>
                  <Badge variant={STATUS_VARIANT[deck.status]}>{t(`decks.status.${deck.status}`)}</Badge>
                </div>
                <p className="flex items-center gap-1.5 text-xs text-[var(--color-fg-muted)]">
                  <Clock size={12} aria-hidden />
                  {updated}
                  {deck.credits > 0 ? <span className="ml-1">· {t('decks.credits', { credits: deck.credits })}</span> : null}
                </p>
                {deck.error ? (
                  <p className="text-xs text-[var(--color-danger)]">{deck.error}</p>
                ) : !deck.file_id && deck.status === 'ready' ? (
                  <p className="text-xs text-[var(--color-fg-muted)]">{t('result.mirrorPending')}</p>
                ) : null}
                <div className="mt-auto flex items-center gap-2">
                  <Button size="sm" variant="secondary" onClick={() => onOpen(deck)}>
                    {deck.file_id ? (
                      <>
                        <FileText size={13} aria-hidden className="mr-1.5" />
                        {t('decks.open')}
                      </>
                    ) : (
                      t('decks.continue')
                    )}
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('decks.delete')}
                    onClick={() => setConfirming(deck)}
                  >
                    <Trash2 size={14} aria-hidden />
                  </Button>
                </div>
              </div>
            </article>
          )
        })}
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
