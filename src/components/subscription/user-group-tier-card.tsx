import { Check } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { ApiUserGroup } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { formatCurrencyMinor } from '@/lib/currency'
import { groupPriceAmount, type BillingCycle } from '@/lib/user-group-tier'
import { cn } from '@/lib/utils'

interface PublicAction {
  href: string
  label: string
}

interface UserGroupTierCardProps {
  group: ApiUserGroup
  billingCycle: BillingCycle
  isCurrent?: boolean
  canRenew?: boolean
  isPermanentlyOwned?: boolean
  isRecommended?: boolean
  onSwitch?: () => void
  onPurchase?: () => void
  publicAction?: PublicAction
  locale?: string
}

const actionClassName =
  'h-auto min-h-11 w-full whitespace-normal py-1.5 text-center leading-snug sm:min-h-8'

export function UserGroupTierCard({
  group,
  billingCycle,
  isCurrent = false,
  canRenew = false,
  isPermanentlyOwned = false,
  isRecommended = false,
  onSwitch,
  onPurchase,
  publicAction,
  locale,
}: UserGroupTierCardProps) {
  const { t } = useTranslation('subscription')
  const features = group.features.filter((feature) => feature !== 'research')
  const amount = groupPriceAmount(group, billingCycle)
  const available = group.is_default || amount > 0
  const purchaseUnavailable = !group.is_default && group.is_purchasable === false
  const canPurchaseRenewal = isCurrent && canRenew && amount > 0 && !purchaseUnavailable

  return (
    <article
      className={cn(
        'flex min-w-0 flex-col rounded-[10px] border bg-[var(--color-surface)] p-5 font-sans sm:row-span-5 sm:grid sm:grid-rows-subgrid',
        isRecommended
          ? 'border-[var(--color-accent)]'
          : isCurrent
            ? 'border-[var(--color-border)] bg-[var(--color-bg-muted)]'
            : 'border-[var(--color-border)]',
      )}
    >
      <div className="flex min-h-6 items-start justify-between gap-2">
        <h3 className="min-w-0 break-words text-[1rem] font-semibold leading-snug text-[var(--color-fg)] [overflow-wrap:anywhere]">
          {group.name}
        </h3>
        <div className="flex shrink-0 flex-wrap justify-end gap-1.5">
          {isCurrent ? (
            <Badge size="sm" variant="accent">
              {t('current')}
            </Badge>
          ) : isPermanentlyOwned ? (
            <Badge size="sm" variant="sage">
              {t('permanentlyOwned')}
            </Badge>
          ) : group.is_default ? (
            <Badge size="sm" variant="neutral">
              {t('free')}
            </Badge>
          ) : null}
          {isRecommended ? (
            <Badge size="sm" variant="sage">
              {t('recommended')}
            </Badge>
          ) : null}
        </div>
      </div>

      <p
        className="mt-2 break-words text-[12.5px] leading-relaxed text-[var(--color-fg-muted)] [overflow-wrap:anywhere] sm:mt-0"
        aria-hidden={!group.description}
      >
        {group.description}
      </p>

      <div className="mt-4 min-h-8 sm:mt-0">
        <PriceTag group={group} billingCycle={billingCycle} locale={locale} />
      </div>

      <ul
        className={cn(
          'mt-4 flex flex-col gap-2 border-t border-[var(--color-divider)] pt-4 sm:mt-0 sm:pt-3',
          features.length === 0 && 'hidden sm:flex',
        )}
      >
        {features.map((feature, index) => (
          <li key={index} className="flex items-start gap-2 text-[12.5px] text-[var(--color-fg)]">
            <Check
              size={14}
              aria-hidden
              className={cn(
                'mt-0.5 shrink-0',
                isCurrent ? 'text-[var(--color-secondary)]' : 'text-[var(--color-accent)]',
              )}
            />
            <span className="min-w-0 break-words leading-snug [overflow-wrap:anywhere]">{feature}</span>
          </li>
        ))}
      </ul>

      <div className="mt-5 flex grow items-end sm:mt-0">
        {isCurrent && canRenew && purchaseUnavailable ? (
          <Button size="sm" variant="secondary" disabled className={actionClassName}>
            {t('purchaseUnavailable')}
          </Button>
        ) : canPurchaseRenewal ? (
          <Button size="sm" variant="primary" className={actionClassName} onClick={onPurchase}>
            {t('renewCta')}
          </Button>
        ) : isCurrent ? (
          <Button size="sm" variant="secondary" disabled className={actionClassName}>
            {t('youreOnThis')}
          </Button>
        ) : isPermanentlyOwned ? (
          <Button size="sm" variant="secondary" disabled className={actionClassName}>
            {t('alreadyOwned')}
          </Button>
        ) : purchaseUnavailable ? (
          <Button size="sm" variant="secondary" disabled className={actionClassName}>
            {t('purchaseUnavailable')}
          </Button>
        ) : !available ? (
          <Button size="sm" variant="secondary" disabled className={actionClassName}>
            {t('billing.unavailable')}
          </Button>
        ) : publicAction ? (
          <Button
            asChild
            size="sm"
            variant={group.is_default ? 'secondary' : 'primary'}
            className={actionClassName}
          >
            <Link to={publicAction.href}>{publicAction.label}</Link>
          </Button>
        ) : (
          <Button
            size="sm"
            variant={group.is_default ? 'secondary' : 'primary'}
            className={actionClassName}
            onClick={group.is_default ? onSwitch : onPurchase}
          >
            {group.is_default ? t('switchCta') : t('buyCta')}
          </Button>
        )}
      </div>
    </article>
  )
}

function PriceTag({
  group,
  billingCycle,
  locale,
}: {
  group: ApiUserGroup
  billingCycle: BillingCycle
  locale?: string
}) {
  const { t } = useTranslation('subscription')
  const amount = groupPriceAmount(group, billingCycle)

  if (group.is_default) {
    return <span className="text-[1.5rem] font-semibold leading-none text-[var(--color-fg)]">{t('priceFree')}</span>
  }
  if (amount <= 0) {
    return <span className="text-[13px] font-medium text-[var(--color-fg-subtle)]">{t('billing.unavailable')}</span>
  }
  return (
    <div className="flex min-w-0 flex-wrap items-baseline gap-x-1.5 gap-y-1">
      <span className="text-[1.5rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
        {formatCurrencyMinor(amount, group.settlement_currency, locale)}
      </span>
      <span className="text-[12px] text-[var(--color-fg-subtle)]">
        {billingCycle === 'monthly' ? t('billing.perMonth') : t('billing.perYear')}
      </span>
    </div>
  )
}
