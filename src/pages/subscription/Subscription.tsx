import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, ArrowUpRight, Check, Clock, RefreshCw, Sparkles, Ticket, Wallet } from 'lucide-react'
import { authApi, creditPackagesApi, groupsApi, redeemApi, ApiError } from '@/api'
import type { ApiCreditPackage, ApiCredits, ApiUserGroup } from '@/api/types'
import { useAuth } from '@/store/auth'
import { ContentHeader } from '@/components/layout/content-header'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { formatCurrencyMinor } from '@/lib/currency'
import { cn, formatAbsoluteDate, formatDateTime, safeHref } from '@/lib/utils'

type TFn = (key: string, options?: Record<string, unknown>) => string
type CatalogTab = 'groups' | 'credit-packages'
type BillingCycle = 'monthly' | 'yearly'

function groupPriceAmount(group: ApiUserGroup, cycle: BillingCycle): number {
  return cycle === 'monthly' ? group.monthly_price_amount_minor : group.yearly_price_amount_minor
}

export default function Subscription() {
  const { t, i18n } = useTranslation(['subscription', 'common'])
  const user = useAuth((state) => state.user)
  const setUser = useAuth((state) => state.setUser)
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [creditPackages, setCreditPackages] = useState<ApiCreditPackage[]>([])
  const [credits, setCredits] = useState<ApiCredits | null>(null)
  const [groupsLoading, setGroupsLoading] = useState(true)
  const [packagesLoading, setPackagesLoading] = useState(true)
  const [catalogTab, setCatalogTab] = useState<CatalogTab>('groups')
  const [billingCycle, setBillingCycle] = useState<BillingCycle>('monthly')
  const [upgrade, setUpgrade] = useState<ApiUserGroup | null>(null)
  const [redeemCode, setRedeemCode] = useState('')
  const [redeeming, setRedeeming] = useState(false)
  const [redeemSuccess, setRedeemSuccess] = useState<
    { kind: 'group'; group: string; date: string } | { kind: 'credits'; credits: number } | null
  >(null)
  const [confirmOverride, setConfirmOverride] = useState<{
    code: string
    current: string
    granted: string
    date: string
  } | null>(null)

  useEffect(() => {
    let active = true

    groupsApi
      .list()
      .then((items) => active && setGroups(items))
      .catch((error) => {
        if (active) toast.error(error instanceof ApiError ? error.message : t('subscription:loadFailed'))
      })
      .finally(() => active && setGroupsLoading(false))

    creditPackagesApi
      .list()
      .then((items) => active && setCreditPackages(items))
      .catch(() => {
        if (active) toast.error(t('subscription:loadPackagesFailed'))
      })
      .finally(() => active && setPackagesLoading(false))

    authApi
      .credits()
      .then((value) => active && setCredits(value))
      .catch(() => undefined)

    return () => {
      active = false
    }
  }, [t])

  const currentId = user?.group_id || groups.find((group) => group.is_default)?.id || ''
  const sortedGroups = useMemo(
    () => groups.slice().sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name)),
    [groups],
  )
  const sortedPackages = useMemo(
    () =>
      creditPackages
        .slice()
        .sort((a, b) => a.sort_order - b.sort_order || a.price_amount_minor - b.price_amount_minor),
    [creditPackages],
  )
  const current = sortedGroups.find((group) => group.id === currentId)
  const recommendedId = useMemo(() => {
    const paid = sortedGroups.filter(
      (group) => !group.is_default && group.id !== currentId && groupPriceAmount(group, billingCycle) > 0,
    )
    if (paid.length === 0) return null
    return paid.reduce((best, group) =>
      groupPriceAmount(group, billingCycle) > groupPriceAmount(best, billingCycle) ? group : best,
    ).id
  }, [billingCycle, currentId, sortedGroups])

  const expiresAt = user?.group_expires_at ?? 0
  const expiresLabel = expiresAt > 0 ? t('subscription:expiresOn', { date: formatAbsoluteDate(expiresAt * 1000) }) : null

  function redeemErrorMessage(error: unknown): string {
    if (error instanceof ApiError) {
      switch (error.message) {
        case 'code_invalid':
          return t('subscription:redeem.errors.invalid')
        case 'code_expired':
          return t('subscription:redeem.errors.expired')
        case 'code_used':
          return t('subscription:redeem.errors.alreadyUsed')
        case 'code_disabled':
          return t('subscription:redeem.errors.disabled')
        case 'code_already_owned':
          return t('subscription:redeem.errors.alreadyOwned')
      }
      return error.message || t('subscription:redeem.errors.generic')
    }
    return t('subscription:redeem.errors.generic')
  }

  async function applyRedeem(code: string, confirm: boolean) {
    setRedeeming(true)
    try {
      const response = await redeemApi.redeem(code, confirm)
      const date =
        (response.expires_at ?? 0) > 0 ? formatAbsoluteDate((response.expires_at ?? 0) * 1000) : ''
      if (response.requires_confirmation) {
        setConfirmOverride({
          code,
          current: response.current_group_name || '',
          granted: response.group_name || '',
          date,
        })
        return
      }
      if (response.user) setUser(response.user)
      groupsApi.list().then(setGroups).catch(() => undefined)
      authApi.credits().then(setCredits).catch(() => undefined)
      setRedeemCode('')
      setConfirmOverride(null)
      setRedeemSuccess(
        response.kind === 'credits'
          ? { kind: 'credits', credits: response.credits ?? 0 }
          : { kind: 'group', group: response.group_name || '', date },
      )
    } catch (error) {
      toast.error(redeemErrorMessage(error))
    } finally {
      setRedeeming(false)
    }
  }

  function submitRedeem() {
    const code = redeemCode.trim()
    if (!code) {
      toast.error(t('subscription:redeem.errors.empty'))
      return
    }
    void applyRedeem(code, false)
  }

  const creditsOn = Boolean(credits?.enabled)
  const showAccount = Boolean(current) || creditsOn
  const showingGroups = catalogTab === 'groups'
  const catalogCount = showingGroups ? sortedGroups.length : sortedPackages.length

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--color-bg)] font-sans text-[var(--color-fg)]">
      <ContentHeader title={t('subscription:title')} backTo="/" backLabel={t('subscription:back')} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <main className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 py-6 pb-20 sm:px-8 sm:py-8">
          {groupsLoading ? (
            <AccountSkeleton />
          ) : showAccount ? (
            <section className="overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]">
              <div
                className={cn(
                  current && creditsOn && credits &&
                    'md:grid md:grid-cols-[minmax(0,0.8fr)_minmax(0,1.7fr)]',
                )}
              >
                {current ? (
                  <div className="min-w-0 p-3.5 sm:p-4">
                    <span className="inline-flex items-center gap-1.5 text-[11px] font-medium text-[var(--color-fg-muted)]">
                      <span className="size-1.5 rounded-full bg-[var(--color-secondary)]" aria-hidden />
                      {t('subscription:currentPlan')}
                    </span>
                    <div className="mt-1 flex flex-wrap items-center gap-1.5">
                      <h1 className="text-[1.125rem] font-semibold leading-tight text-[var(--color-fg)]">{current.name}</h1>
                      {current.is_default ? (
                        <Badge size="sm" variant="neutral">
                          {t('subscription:free')}
                        </Badge>
                      ) : null}
                      {expiresLabel ? (
                        <Badge size="sm" variant="sage">
                          {expiresLabel}
                        </Badge>
                      ) : null}
                    </div>
                    {current.description ? (
                      <p className="mt-1 max-w-prose text-[11.5px] leading-snug text-[var(--color-fg-muted)]">
                        {current.description}
                      </p>
                    ) : null}
                  </div>
                ) : null}

                {creditsOn && credits ? (
                  <Balance credits={credits} hasPlanHeader={Boolean(current)} locale={i18n.resolvedLanguage} t={t} />
                ) : null}
              </div>
            </section>
          ) : null}

          <section className="mt-8" aria-labelledby="subscription-catalog-heading">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <SegmentedControl<CatalogTab>
                label={t('subscription:catalog.label')}
                value={catalogTab}
                onChange={setCatalogTab}
                options={[
                  { value: 'groups', label: t('subscription:catalog.userGroups') },
                  { value: 'credit-packages', label: t('subscription:catalog.creditPackages') },
                ]}
              />
              {showingGroups ? (
                <SegmentedControl<BillingCycle>
                  label={t('subscription:billing.label')}
                  value={billingCycle}
                  onChange={setBillingCycle}
                  options={[
                    { value: 'monthly', label: t('subscription:billing.monthly') },
                    { value: 'yearly', label: t('subscription:billing.yearly') },
                  ]}
                />
              ) : null}
            </div>

            <div className="mt-4 flex items-end justify-between gap-4">
              <div className="min-w-0">
                <h2 id="subscription-catalog-heading" className="text-[1.25rem] font-semibold text-[var(--color-fg)]">
                  {showingGroups ? t('subscription:allPlans') : t('subscription:packages.title')}
                </h2>
                <p className="mt-1 max-w-[60ch] text-[13px] leading-relaxed text-[var(--color-fg-muted)]">
                  {showingGroups ? t('subscription:subtitle') : t('subscription:packages.subtitle')}
                </p>
              </div>
              {catalogCount > 0 ? (
                <span className="hidden shrink-0 text-[12px] tabular-nums text-[var(--color-fg-subtle)] sm:inline">
                  {showingGroups
                    ? t('subscription:planCount', { count: catalogCount })
                    : t('subscription:packages.count', { count: catalogCount })}
                </span>
              ) : null}
            </div>

            <div id="subscription-catalog" className="mt-4">
              {showingGroups ? (
                groupsLoading ? (
                  <CardsSkeleton />
                ) : sortedGroups.length > 0 ? (
                  <div className="grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    {sortedGroups.map((group) => (
                      <TierCard
                        key={group.id}
                        group={group}
                        billingCycle={billingCycle}
                        isCurrent={group.id === currentId}
                        isRecommended={group.id === recommendedId}
                        groupBuyUrl={credits?.group_buy_url}
                        onUpgrade={() => setUpgrade(group)}
                        locale={i18n.resolvedLanguage}
                        t={t}
                      />
                    ))}
                  </div>
                ) : (
                  <EmptyCatalog>{t('subscription:noGroups')}</EmptyCatalog>
                )
              ) : packagesLoading ? (
                <CardsSkeleton />
              ) : sortedPackages.length > 0 ? (
                <div className="grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  {sortedPackages.map((creditPackage) => (
                    <CreditPackageCard
                      key={creditPackage.id}
                      creditPackage={creditPackage}
                      buyUrl={credits?.buy_url}
                      locale={i18n.resolvedLanguage}
                      t={t}
                    />
                  ))}
                </div>
              ) : (
                <EmptyCatalog>{t('subscription:packages.empty')}</EmptyCatalog>
              )}
            </div>
          </section>

          <section className="mt-8 overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            <div className="flex flex-col gap-4 p-5 sm:flex-row sm:items-center sm:gap-6">
              <div className="flex items-start gap-3 sm:max-w-[38ch]">
                <span className="inline-flex size-9 shrink-0 items-center justify-center rounded-[8px] bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]">
                  <Ticket size={17} aria-hidden />
                </span>
                <div className="min-w-0">
                  <h3 className="text-[1rem] font-semibold text-[var(--color-fg)]">{t('subscription:redeem.title')}</h3>
                  <p className="mt-1 text-[12.5px] leading-relaxed text-[var(--color-fg-muted)]">
                    {t('subscription:redeem.subtitle')}
                  </p>
                </div>
              </div>
              <form
                className="flex flex-1 flex-col gap-2 sm:flex-row sm:items-end sm:justify-end"
                onSubmit={(event) => {
                  event.preventDefault()
                  submitRedeem()
                }}
              >
                <Field label={t('subscription:redeem.inputLabel')} htmlFor="redeem-code" className="sm:w-[18rem]">
                  <Input
                    id="redeem-code"
                    value={redeemCode}
                    onChange={(event) => setRedeemCode(event.target.value.toUpperCase())}
                    placeholder={t('subscription:redeem.inputPlaceholder')}
                    autoComplete="off"
                    spellCheck={false}
                    className="font-sans tracking-[0.18em]"
                  />
                </Field>
                <Button type="submit" size="sm" loading={redeeming} disabled={!redeemCode.trim() || redeeming}>
                  {redeeming ? t('subscription:redeem.redeeming') : t('subscription:redeem.submit')}
                </Button>
              </form>
            </div>
          </section>
        </main>
      </div>

      <Dialog open={Boolean(redeemSuccess)} onOpenChange={(open) => !open && setRedeemSuccess(null)}>
        <DialogContent size="sm" className="font-sans">
          <DialogHeader>
            <div className="mx-auto mb-1 inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]">
              <Sparkles size={22} aria-hidden />
            </div>
            <DialogTitle className="text-center">
              {redeemSuccess?.kind === 'credits'
                ? t('subscription:redeem.successCredits')
                : t('subscription:redeem.success')}
            </DialogTitle>
            <DialogDescription className="text-center">
              {redeemSuccess
                ? redeemSuccess.kind === 'credits'
                  ? t('subscription:redeem.successBodyCredits', { count: redeemSuccess.credits })
                  : redeemSuccess.date
                    ? t('subscription:redeem.successBodyUntil', {
                        group: redeemSuccess.group,
                        date: redeemSuccess.date,
                      })
                    : t('subscription:redeem.successBody', { group: redeemSuccess.group })
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button className="w-full" onClick={() => setRedeemSuccess(null)}>
              {t('common:actions.gotIt')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(confirmOverride)}
        onOpenChange={(open) => !open && !redeeming && setConfirmOverride(null)}
      >
        <DialogContent size="sm" className="font-sans">
          <DialogHeader>
            <div className="mx-auto mb-1 inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-warning-soft)] text-[var(--color-warning)]">
              <AlertTriangle size={22} aria-hidden />
            </div>
            <DialogTitle className="text-center">{t('subscription:redeem.override.title')}</DialogTitle>
            <DialogDescription className="text-center">
              {confirmOverride
                ? confirmOverride.date
                  ? t('subscription:redeem.override.bodyUntil', {
                      current: confirmOverride.current,
                      granted: confirmOverride.granted,
                      date: confirmOverride.date,
                    })
                  : t('subscription:redeem.override.body', {
                      current: confirmOverride.current,
                      granted: confirmOverride.granted,
                    })
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={redeeming} onClick={() => setConfirmOverride(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={redeeming} onClick={() => confirmOverride && void applyRedeem(confirmOverride.code, true)}>
              {t('subscription:redeem.override.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(upgrade)} onOpenChange={(open) => !open && setUpgrade(null)}>
        <DialogContent size="sm" className="font-sans">
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

function SegmentedControl<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: T
  options: Array<{ value: T; label: string }>
  onChange: (value: T) => void
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="inline-flex min-h-9 max-w-full items-center gap-0.5 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-0.5"
    >
      {options.map((option) => {
        const active = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(option.value)}
            className={cn(
              'min-h-8 min-w-0 rounded-[6px] px-3 py-1 text-center text-[12.5px] font-medium leading-tight transition-colors',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              active
                ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
                : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
            )}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}

function Balance({
  credits,
  hasPlanHeader,
  locale,
  t,
}: {
  credits: ApiCredits
  hasPlanHeader: boolean
  locale?: string
  t: TFn
}) {
  const timed = credits.timed
  const showTimed = Boolean(timed && timed.allowance > 0 && timed.period_seconds > 0)
  const percentage =
    timed && timed.allowance > 0 ? Math.max(0, Math.min(100, (timed.remaining / timed.allowance) * 100)) : 0

  return (
    <div
      className={cn(
        'min-w-0 bg-[var(--color-bg-muted)] p-3.5 sm:p-4',
        hasPlanHeader && 'border-t border-[var(--color-divider)] md:border-l md:border-t-0',
      )}
    >
      <span className="inline-flex items-center gap-1.5 text-[11px] font-medium text-[var(--color-fg-muted)]">
        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" aria-hidden />
        {t('subscription:credits.sectionTitle')}
      </span>

      <div className={cn('mt-2 grid gap-3', showTimed ? 'sm:grid-cols-2 sm:gap-4' : 'sm:grid-cols-1')}>
        {showTimed && timed ? (
          <div>
            <div className="flex items-center justify-between gap-2">
              <span className="inline-flex items-center gap-1.5 text-[12px] font-medium text-[var(--color-accent)]">
                <Clock size={13} aria-hidden />
                {t('subscription:credits.timedTitle')}
              </span>
              <span className="text-[11px] font-semibold tabular-nums text-[var(--color-accent)]">
                {Math.round(percentage)}%
              </span>
            </div>
            <div className="mt-1 flex items-baseline gap-1.5">
              <span className="text-[1.375rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
                {formatCredits(timed.remaining, locale)}
              </span>
              <span className="text-[12px] tabular-nums text-[var(--color-fg-subtle)]">
                / {formatCredits(timed.allowance, locale)}
              </span>
            </div>
            <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-[var(--color-accent-soft)]">
              <div
                className="h-full rounded-full bg-[var(--color-accent)]"
                style={{ width: `${percentage}%` }}
              />
            </div>
            {timed.resets_at > 0 ? (
              <p className="mt-1.5 inline-flex items-center gap-1.5 text-[11px] text-[var(--color-fg-subtle)]">
                <RefreshCw size={11} aria-hidden />
                {t('subscription:credits.resetsOn', { date: formatDateTime(timed.resets_at * 1000) })}
              </p>
            ) : null}
          </div>
        ) : null}

        <div className={cn(showTimed && 'sm:border-l sm:border-[var(--color-divider)] sm:pl-4')}>
          <span className="inline-flex items-center gap-1.5 text-[12px] font-medium text-[var(--color-secondary)]">
            <Wallet size={13} aria-hidden />
            {t('subscription:credits.permanentTitle')}
          </span>
          <div className="mt-1 text-[1.375rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
            {formatCredits(credits.permanent, locale)}
          </div>
          <p className="mt-1 max-w-[40ch] text-[11px] leading-snug text-[var(--color-fg-muted)]">
            {t('subscription:credits.permanentHint')}
          </p>
        </div>
      </div>
    </div>
  )
}

function TierCard({
  group,
  billingCycle,
  isCurrent,
  isRecommended,
  groupBuyUrl,
  onUpgrade,
  locale,
  t,
}: {
  group: ApiUserGroup
  billingCycle: BillingCycle
  isCurrent: boolean
  isRecommended: boolean
  groupBuyUrl?: string
  onUpgrade: () => void
  locale?: string
  t: TFn
}) {
  const features = group.features.filter((feature) => feature !== 'research')
  const amount = groupPriceAmount(group, billingCycle)
  const available = group.is_default || amount > 0
  const purchaseHref = safeHref(groupBuyUrl)

  return (
    <article
      className={cn(
        'flex min-w-0 flex-col rounded-[8px] border bg-[var(--color-surface)] p-5',
        isRecommended
          ? 'border-[var(--color-accent)]'
          : isCurrent
            ? 'border-[var(--color-border)] bg-[var(--color-bg-muted)]'
            : 'border-[var(--color-border)]',
      )}
    >
      <div className="flex min-h-6 items-start justify-between gap-2">
        <h3 className="min-w-0 text-[1rem] font-semibold leading-snug text-[var(--color-fg)]">{group.name}</h3>
        <div className="flex shrink-0 flex-wrap justify-end gap-1.5">
          {isCurrent ? (
            <Badge size="sm" variant="accent">
              {t('subscription:current')}
            </Badge>
          ) : group.is_default ? (
            <Badge size="sm" variant="neutral">
              {t('subscription:free')}
            </Badge>
          ) : null}
          {isRecommended ? (
            <Badge size="sm" variant="sage">
              {t('subscription:recommended')}
            </Badge>
          ) : null}
        </div>
      </div>

      {group.description ? (
        <p className="mt-2 text-[12.5px] leading-relaxed text-[var(--color-fg-muted)]">{group.description}</p>
      ) : null}

      <div className="mt-4 min-h-8">
        <PriceTag group={group} billingCycle={billingCycle} locale={locale} t={t} />
      </div>

      {features.length > 0 ? (
        <ul className="mt-4 flex flex-col gap-2 border-t border-[var(--color-divider)] pt-4">
          {features.map((feature, index) => (
            <li key={index} className="flex items-start gap-2 text-[12.5px] text-[var(--color-fg)]">
              <Check
                size={14}
                aria-hidden
                className={cn('mt-0.5 shrink-0', isCurrent ? 'text-[var(--color-secondary)]' : 'text-[var(--color-accent)]')}
              />
              <span className="leading-snug">{feature}</span>
            </li>
          ))}
        </ul>
      ) : null}

      <div className="mt-5 flex grow items-end">
        {isCurrent ? (
          <Button
            size="sm"
            variant="secondary"
            disabled
            className="h-auto min-h-8 w-full whitespace-normal py-1.5 text-center leading-snug"
          >
            {t('subscription:youreOnThis')}
          </Button>
        ) : !available ? (
          <Button
            size="sm"
            variant="secondary"
            disabled
            className="h-auto min-h-8 w-full whitespace-normal py-1.5 text-center leading-snug"
          >
            {t('subscription:billing.unavailable')}
          </Button>
        ) : !group.is_default && purchaseHref ? (
          <Button
            asChild
            size="sm"
            variant="primary"
            trailingIcon={<ArrowUpRight size={14} aria-hidden />}
            className="h-auto min-h-8 w-full whitespace-normal py-1.5 text-center leading-snug"
          >
            <a href={purchaseHref} target="_blank" rel="noreferrer noopener">
              {t('subscription:buyCta')}
            </a>
          </Button>
        ) : (
          <Button
            size="sm"
            variant={group.is_default ? 'secondary' : 'primary'}
            className="h-auto min-h-8 w-full whitespace-normal py-1.5 text-center leading-snug"
            onClick={onUpgrade}
          >
            {group.is_default ? t('subscription:switchCta') : t('subscription:upgradeCta')}
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
  t,
}: {
  group: ApiUserGroup
  billingCycle: BillingCycle
  locale?: string
  t: TFn
}) {
  const amount = groupPriceAmount(group, billingCycle)
  if (group.is_default) {
    return <span className="text-[1.5rem] font-semibold leading-none text-[var(--color-fg)]">{t('subscription:priceFree')}</span>
  }
  if (amount <= 0) {
    return <span className="text-[13px] font-medium text-[var(--color-fg-subtle)]">{t('subscription:billing.unavailable')}</span>
  }
  return (
    <div className="flex min-w-0 flex-wrap items-baseline gap-x-1.5 gap-y-1">
      <span className="text-[1.5rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
        {formatCurrencyMinor(amount, group.settlement_currency, locale)}
      </span>
      <span className="text-[12px] text-[var(--color-fg-subtle)]">
        {billingCycle === 'monthly' ? t('subscription:billing.perMonth') : t('subscription:billing.perYear')}
      </span>
    </div>
  )
}

function CreditPackageCard({
  creditPackage,
  buyUrl,
  locale,
  t,
}: {
  creditPackage: ApiCreditPackage
  buyUrl?: string
  locale?: string
  t: TFn
}) {
  const purchaseHref = safeHref(buyUrl)

  return (
    <article className="flex min-w-0 flex-col rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
      <div className="flex min-h-6 items-start justify-between gap-2">
        <h3 className="min-w-0 text-[1rem] font-semibold leading-snug text-[var(--color-fg)]">{creditPackage.name}</h3>
        <Badge size="sm" variant="neutral" className="shrink-0">
          {t('subscription:permanent')}
        </Badge>
      </div>
      {creditPackage.description ? (
        <p className="mt-2 text-[12.5px] leading-relaxed text-[var(--color-fg-muted)]">
          {creditPackage.description}
        </p>
      ) : null}

      <div className="mt-4">
        <div className="text-[1.75rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
          {formatCredits(creditPackage.credits, locale)}
        </div>
        <div className="mt-1 text-[12px] text-[var(--color-fg-muted)]">{t('subscription:packages.credits')}</div>
      </div>
      <div className="mt-4 border-t border-[var(--color-divider)] pt-4 text-[1.25rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
        {formatCurrencyMinor(creditPackage.price_amount_minor, creditPackage.settlement_currency, locale)}
      </div>

      <div className="mt-5 flex grow items-end">
        {purchaseHref ? (
          <Button
            asChild
            size="sm"
            variant="primary"
            trailingIcon={<ArrowUpRight size={14} aria-hidden />}
            className="h-auto min-h-8 w-full whitespace-normal py-1.5 text-center leading-snug"
          >
            <a href={purchaseHref} target="_blank" rel="noreferrer noopener">
              {t('subscription:packages.buy')}
            </a>
          </Button>
        ) : (
          <Button
            size="sm"
            variant="secondary"
            disabled
            className="h-auto min-h-8 w-full whitespace-normal py-1.5 text-center leading-snug"
          >
            {t('subscription:credits.topUpUnavailable')}
          </Button>
        )}
      </div>
    </article>
  )
}

function formatCredits(value: number, locale?: string): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value)
}

function EmptyCatalog({ children }: { children: string }) {
  return (
    <div className="rounded-[8px] border border-dashed border-[var(--color-border)] px-4 py-8 text-center text-[13px] text-[var(--color-fg-muted)]">
      {children}
    </div>
  )
}

function AccountSkeleton() {
  return (
    <div className="animate-pulse overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]">
      <div className="md:grid md:grid-cols-[minmax(0,0.8fr)_minmax(0,1.7fr)]">
        <div className="p-3.5 sm:p-4">
          <div className="h-3 w-20 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-2 h-5 w-36 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-2 h-3 w-52 max-w-full rounded bg-[var(--color-bg-muted)]" />
        </div>
        <div className="border-t border-[var(--color-divider)] bg-[var(--color-bg-muted)] p-3.5 sm:p-4 md:border-l md:border-t-0">
          <div className="h-3 w-20 rounded bg-[var(--color-surface-sunken)]" />
          <div className="mt-2 grid gap-3 sm:grid-cols-2 sm:gap-4">
            {Array.from({ length: 2 }).map((_, index) => (
              <div key={index}>
                <div className="h-3 w-24 rounded bg-[var(--color-surface-sunken)]" />
                <div className="mt-2 h-6 w-24 rounded bg-[var(--color-surface-sunken)]" />
                <div className="mt-2 h-1 w-full rounded-full bg-[var(--color-surface-sunken)]" />
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

function CardsSkeleton() {
  return (
    <div className="grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: 3 }).map((_, index) => (
        <div
          key={index}
          className="animate-pulse rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] p-5"
        >
          <div className="h-5 w-24 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-3 h-3.5 w-full rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-4 h-7 w-28 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-4 flex flex-col gap-2 border-t border-[var(--color-divider)] pt-4">
            <div className="h-3 w-full rounded bg-[var(--color-bg-muted)]" />
            <div className="h-3 w-5/6 rounded bg-[var(--color-bg-muted)]" />
          </div>
          <div className="mt-5 h-8 w-full rounded-[8px] bg-[var(--color-bg-muted)]" />
        </div>
      ))}
    </div>
  )
}
