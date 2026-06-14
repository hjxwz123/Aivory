/**
 * Subscription — the member-facing view of the user-group tiers (§ user groups).
 * Shows the viewer's current group and every available group as editorial price
 * cards (USD / CNY + feature list). Switching is admin-assigned, so the CTA on
 * other tiers opens a short "how to upgrade" note rather than a checkout.
 */
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Sparkles } from 'lucide-react'
import { groupsApi, ApiError } from '@/api'
import type { ApiUserGroup } from '@/api/types'
import { useAuth } from '@/store/auth'
import { ContentHeader } from '@/components/layout/content-header'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

export default function Subscription() {
  const { t } = useTranslation(['subscription', 'common'])
  const user = useAuth((s) => s.user)
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [upgrade, setUpgrade] = useState<ApiUserGroup | null>(null)

  useEffect(() => {
    let active = true
    groupsApi
      .list()
      .then((gs) => active && setGroups(gs))
      .catch((e) => active && toast.error(e instanceof ApiError ? e.message : t('subscription:loadFailed')))
      .finally(() => active && setLoading(false))
    return () => {
      active = false
    }
  }, [t])

  const currentId = user?.group_id || groups.find((g) => g.is_default)?.id || ''
  const sorted = useMemo(
    () => groups.slice().sort((a, b) => a.sort_order - b.sort_order || a.price_usd - b.price_usd),
    [groups],
  )
  const current = sorted.find((g) => g.id === currentId)

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader title={t('subscription:title')} backTo="/" backLabel={t('subscription:back')} />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-10 pb-24">
          <p className="max-w-[60ch] text-[var(--color-fg-muted)] text-[15px] leading-relaxed">
            {t('subscription:subtitle')}
          </p>

          {/* Current plan strip */}
          {current ? (
          <div className="mt-10 flex flex-col gap-3 rounded-[16px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5 sm:flex-row sm:items-center sm:justify-between">
            <div className="min-w-0">
              <div className="flex items-center gap-2 text-[12px] uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">
                <Sparkles size={13} aria-hidden className="text-[var(--color-secondary)]" />
                {t('subscription:currentPlan')}
              </div>
              <div className="mt-1 flex items-center gap-2">
                <span className="font-serif text-xl text-[var(--color-fg)]">{current.name}</span>
                {current.is_default ? (
                  <Badge size="xs" variant="neutral">
                    {t('subscription:free')}
                  </Badge>
                ) : null}
              </div>
              {current.description ? (
                <p className="mt-1 text-sm text-[var(--color-fg-muted)] max-w-prose">{current.description}</p>
              ) : null}
            </div>
            <div className="shrink-0 text-right">
              <PriceTag group={current} t={t} />
            </div>
          </div>
        ) : null}

        {/* All tiers */}
        <h2 className="mt-12 mb-5 font-serif text-xl text-[var(--color-fg)]">{t('subscription:allPlans')}</h2>
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {sorted.map((g) => {
              const isCurrent = g.id === currentId
              return (
                <article
                  key={g.id}
                  className={cn(
                    'flex flex-col rounded-[16px] border bg-[var(--color-surface)] px-6 py-6 transition-colors',
                    isCurrent
                      ? 'border-[var(--color-accent)] ring-1 ring-[var(--color-accent)]/30'
                      : 'border-[var(--color-border)]',
                  )}
                >
                  <div className="flex items-start justify-between gap-2">
                    <h3 className="font-serif text-lg text-[var(--color-fg)]">{g.name}</h3>
                    {isCurrent ? (
                      <Badge size="xs" variant="accent">
                        {t('subscription:current')}
                      </Badge>
                    ) : g.is_default ? (
                      <Badge size="xs" variant="neutral">
                        {t('subscription:free')}
                      </Badge>
                    ) : null}
                  </div>

                  {g.description ? (
                    <p className="mt-1.5 text-[13px] leading-relaxed text-[var(--color-fg-muted)]">{g.description}</p>
                  ) : null}

                  <div className="mt-4">
                    <PriceTag group={g} t={t} />
                  </div>

                  {g.features.length > 0 ? (
                    <ul className="mt-5 flex flex-col gap-2.5">
                      {g.features.map((f, i) => (
                        <li key={i} className="flex items-start gap-2 text-[13px] text-[var(--color-fg)]">
                          <Check size={15} aria-hidden className="mt-[2px] shrink-0 text-[var(--color-secondary)]" />
                          <span className="leading-snug">{f}</span>
                        </li>
                      ))}
                    </ul>
                  ) : null}

                  <div className="mt-6 pt-2 grow flex items-end">
                    {isCurrent ? (
                      <Button variant="secondary" disabled className="w-full">
                        {t('subscription:youreOnThis')}
                      </Button>
                    ) : (
                      <Button
                        variant={g.is_default ? 'ghost' : 'primary'}
                        className="w-full"
                        onClick={() => setUpgrade(g)}
                      >
                        {g.is_default ? t('subscription:switchCta') : t('subscription:upgradeCta')}
                      </Button>
                    )}
                  </div>
                </article>
              )
            })}
          </div>
        )}
        </div>
      </div>

      {/* Admin-assigned: explain how to switch rather than charging the user. */}
      <Dialog open={Boolean(upgrade)} onOpenChange={(o) => !o && setUpgrade(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{upgrade ? t('subscription:upgradeTitle', { name: upgrade.name }) : ''}</DialogTitle>
            <DialogDescription>{t('subscription:upgradeBody')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button onClick={() => setUpgrade(null)}>{t('common:actions.gotIt')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function PriceTag({ group, t }: { group: ApiUserGroup; t: (k: string) => string }) {
  if (group.is_default || (group.price_usd <= 0 && group.price_cny <= 0)) {
    return <span className="font-serif text-2xl text-[var(--color-fg)]">{t('subscription:priceFree')}</span>
  }
  return (
    <div className="flex items-baseline gap-2">
      <span className="font-serif text-2xl text-[var(--color-fg)] tabular-nums">${group.price_usd}</span>
      {group.price_cny > 0 ? (
        <span className="text-[13px] text-[var(--color-fg-subtle)] tabular-nums">¥{group.price_cny}</span>
      ) : null}
    </div>
  )
}
