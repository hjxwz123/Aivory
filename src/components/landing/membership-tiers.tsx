import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { groupsApi } from '@/api'
import type { ApiUserGroup } from '@/api/types'
import { ScrollFloat } from '@/components/landing/fx/scroll-float'
import { UserGroupTierCard } from '@/components/subscription/user-group-tier-card'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { groupPriceAmount, type BillingCycle } from '@/lib/user-group-tier'

/** Public user-group showcase backed by the same tier card as the subscription page. */
export function MembershipTiers() {
  const { t, i18n } = useTranslation(['landing', 'common', 'subscription'])
  const [groups, setGroups] = useState<ApiUserGroup[] | null>(null)
  const [billingCycle, setBillingCycle] = useState<BillingCycle>('monthly')

  useEffect(() => {
    let active = true
    groupsApi
      .publicList()
      .then((items) => active && setGroups(items))
      .catch(() => active && setGroups([]))
    return () => {
      active = false
    }
  }, [])

  if (groups !== null && groups.length === 0) return null

  const sorted = (groups ?? []).slice().sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name))
  const paidGroups = sorted.filter(
    (group) => !group.is_default && groupPriceAmount(group, billingCycle) > 0,
  )
  const recommendedId =
    paidGroups.length === 0
      ? null
      : paidGroups.reduce((best, group) =>
          groupPriceAmount(group, billingCycle) > groupPriceAmount(best, billingCycle) ? group : best,
        ).id

  return (
    <section id="pricing" className="border-t border-[var(--color-divider)] py-24 sm:py-32">
      <div className="mx-auto max-w-[76rem] px-5 sm:px-8">
        <div className="max-w-2xl" data-reveal>
          <div className="font-mono text-[11px] uppercase tracking-[0.18em] text-[var(--color-accent)]">
            {t('landing:membership.eyebrow')}
          </div>
          <h2 className="mt-3 text-balance font-serif text-3xl tracking-tight text-[var(--color-fg)] sm:text-4xl">
            <ScrollFloat text={t('landing:membership.title')} />
          </h2>
          <p className="mt-5 text-pretty leading-relaxed text-[var(--color-fg-muted)]">
            {t('landing:membership.body')}
          </p>
        </div>

        <div className="mt-10 flex justify-end">
          <SegmentedControl<BillingCycle>
            compact
            label={t('subscription:billing.label')}
            value={billingCycle}
            onChange={setBillingCycle}
            options={[
              { value: 'monthly', label: t('subscription:billing.monthly') },
              { value: 'yearly', label: t('subscription:billing.yearly') },
            ]}
          />
        </div>

        {groups === null ? (
          <div
            className="mt-3 grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3"
            role="status"
            aria-label={t('common:aria.loading')}
          >
            <span className="sr-only">{t('common:aria.loading')}</span>
            {Array.from({ length: 3 }).map((_, index) => (
              <div
                key={index}
                className="animate-pulse rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] p-5"
              >
                <div className="h-5 w-24 rounded bg-[var(--color-bg-muted)]" />
                <div className="mt-3 h-3.5 w-full rounded bg-[var(--color-bg-muted)]" />
                <div className="mt-4 h-7 w-28 rounded bg-[var(--color-bg-muted)]" />
                <div className="mt-4 flex flex-col gap-2 border-t border-[var(--color-divider)] pt-4">
                  <div className="h-3 w-full rounded bg-[var(--color-bg-muted)]" />
                  <div className="h-3 w-5/6 rounded bg-[var(--color-bg-muted)]" />
                </div>
                <div className="mt-5 h-8 w-full rounded-[10px] bg-[var(--color-bg-muted)]" />
              </div>
            ))}
          </div>
        ) : (
          <div className="mt-3 grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3" data-reveal-group>
            {sorted.map((group) => (
              <UserGroupTierCard
                key={group.id}
                group={group}
                billingCycle={billingCycle}
                isRecommended={group.id === recommendedId}
                publicAction={{ href: '/register', label: t('landing:membership.cta') }}
                locale={i18n.resolvedLanguage}
              />
            ))}
          </div>
        )}

        <p className="mt-8 text-[12px] text-[var(--color-fg-subtle)]">{t('landing:membership.footnote')}</p>
      </div>
    </section>
  )
}
