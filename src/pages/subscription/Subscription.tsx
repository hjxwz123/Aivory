/**
 * Subscription — the member-facing view of the user-group tiers (§ user groups).
 * A billing dashboard (current plan + a prominent credit-balance region) over a
 * pricing grid, plus a redeem panel that flips the group via a code. Switching is
 * admin-assigned, so most CTAs open a "how to upgrade" note (unless a global
 * purchase link is configured).
 */
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { motion, useReducedMotion, type Variants } from 'framer-motion'
import { Check, Sparkles, Ticket, Clock, Wallet, Plus, RefreshCw, ArrowUpRight, AlertTriangle } from 'lucide-react'
import { groupsApi, redeemApi, authApi, ApiError } from '@/api'
import type { ApiUserGroup, ApiCredits } from '@/api/types'
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
import { cn, formatAbsoluteDate, formatDateTime, safeHref } from '@/lib/utils'

type TFn = (k: string, o?: Record<string, unknown>) => string

const EASE: [number, number, number, number] = [0.2, 0.8, 0.2, 1]

export default function Subscription() {
  const { t } = useTranslation(['subscription', 'common'])
  const user = useAuth((s) => s.user)
  const setUser = useAuth((s) => s.setUser)
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [credits, setCredits] = useState<ApiCredits | null>(null)
  const [loading, setLoading] = useState(true)
  const [upgrade, setUpgrade] = useState<ApiUserGroup | null>(null)
  const [redeemCode, setRedeemCode] = useState('')
  const [redeeming, setRedeeming] = useState(false)
  const [redeemSuccess, setRedeemSuccess] = useState<
    { kind: 'group'; group: string; date: string } | { kind: 'credits'; credits: number } | null
  >(null)
  // Set when the typed code grants a group different from the current one — the
  // override (immediate, not a renewal) must be confirmed before it applies.
  const [confirmOverride, setConfirmOverride] = useState<{ code: string; current: string; granted: string; date: string } | null>(null)

  useEffect(() => {
    let active = true
    groupsApi
      .list()
      .then((gs) => active && setGroups(gs))
      .catch((e) => active && toast.error(e instanceof ApiError ? e.message : t('subscription:loadFailed')))
      .finally(() => active && setLoading(false))
    authApi
      .credits()
      .then((c) => active && setCredits(c))
      .catch(() => undefined)
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
  // The flagship CTA: the priciest paid tier the viewer isn't already on. Matches
  // the "recommend the next tier up" convention without a per-group data flag.
  const recommendedId = useMemo(() => {
    const paid = sorted.filter((g) => !g.is_default && g.id !== currentId && (g.price_usd > 0 || g.price_cny > 0))
    if (paid.length === 0) return null
    return paid.reduce((a, b) => (b.price_usd > a.price_usd ? b : a)).id
  }, [sorted, currentId])

  const expiresAt = user?.group_expires_at ?? 0
  const expiresLabel = expiresAt > 0 ? t('subscription:expiresOn', { date: formatAbsoluteDate(expiresAt * 1000) }) : null

  function redeemErrorMessage(e: unknown): string {
    if (e instanceof ApiError) {
      switch (e.message) {
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
      return e.message || t('subscription:redeem.errors.generic')
    }
    return t('subscription:redeem.errors.generic')
  }

  async function applyRedeem(code: string, confirm: boolean) {
    setRedeeming(true)
    try {
      const res = await redeemApi.redeem(code, confirm)
      const date = (res.expires_at ?? 0) > 0 ? formatAbsoluteDate((res.expires_at ?? 0) * 1000) : ''
      // The code grants a different group — confirm the immediate override first.
      if (res.requires_confirmation) {
        setConfirmOverride({ code, current: res.current_group_name || '', granted: res.group_name || '', date })
        return
      }
      if (res.user) setUser(res.user)
      groupsApi.list().then(setGroups).catch(() => undefined)
      authApi.credits().then(setCredits).catch(() => undefined)
      setRedeemCode('')
      setConfirmOverride(null)
      setRedeemSuccess(
        res.kind === 'credits'
          ? { kind: 'credits', credits: res.credits ?? 0 }
          : { kind: 'group', group: res.group_name || '', date },
      )
    } catch (e) {
      toast.error(redeemErrorMessage(e))
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

  const reduce = useReducedMotion()
  const container: Variants = {
    hidden: {},
    show: { transition: { staggerChildren: reduce ? 0 : 0.05, delayChildren: reduce ? 0 : 0.03 } },
  }
  const item: Variants = {
    hidden: reduce ? { opacity: 1 } : { opacity: 0, y: 10 },
    show: { opacity: 1, y: 0, transition: { duration: 0.42, ease: EASE } },
  }

  const creditsOn = Boolean(credits?.enabled)
  const showAccount = Boolean(current) || creditsOn

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader title={t('subscription:title')} backTo="/" backLabel={t('subscription:back')} />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <motion.div
          variants={container}
          initial="hidden"
          animate="show"
          className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-10 sm:py-12 pb-28"
        >
          {/* ── Dashboard: current plan + credit balance ───────────────────── */}
          {loading ? (
            <AccountSkeleton />
          ) : showAccount ? (
            <motion.section
              variants={item}
              className="overflow-hidden rounded-[18px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-sm)]"
            >
              {current ? (
                <div className="flex flex-col gap-5 p-7 sm:flex-row sm:items-start sm:justify-between sm:p-9">
                  <div className="min-w-0">
                    <span className="inline-flex items-center gap-2 text-[12.5px] font-medium text-[var(--color-fg-muted)]">
                      <span className="size-1.5 rounded-full bg-[var(--color-secondary)]" aria-hidden />
                      {t('subscription:currentPlan')}
                    </span>
                    <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2">
                      <h1 className="font-serif text-[2.25rem] sm:text-[2.75rem] leading-[1.02] tracking-[-0.02em] text-balance text-[var(--color-fg)]">
                        {current.name}
                      </h1>
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
                      <p className="mt-3 max-w-prose text-[14px] leading-relaxed text-[var(--color-fg-muted)]">
                        {current.description}
                      </p>
                    ) : null}
                  </div>
                  {!current.is_default && (current.price_usd > 0 || current.price_cny > 0) ? (
                    <div className="shrink-0 sm:text-right">
                      <PriceTag group={current} t={t} size="lg" />
                      <span className="mt-1 block text-[11.5px] text-[var(--color-fg-subtle)]">
                        {t('subscription:cycleFee', { defaultValue: 'Current cycle fee' })}
                      </span>
                    </div>
                  ) : null}
                </div>
              ) : null}

              {creditsOn && credits ? <Balance credits={credits} hasPlanHeader={Boolean(current)} t={t} /> : null}
            </motion.section>
          ) : null}

          {/* ── All plans ──────────────────────────────────────────────────── */}
          <motion.div variants={item} className="mt-16 flex items-end justify-between gap-4">
            <div className="min-w-0">
              <h2 className="font-serif text-[1.625rem] tracking-[-0.015em] text-[var(--color-fg)]">
                {t('subscription:allPlans')}
              </h2>
              <p className="mt-1.5 max-w-[60ch] text-[14px] leading-relaxed text-[var(--color-fg-muted)]">
                {t('subscription:subtitle')}
              </p>
            </div>
            {sorted.length > 0 ? (
              <span className="hidden shrink-0 text-[12.5px] tabular-nums text-[var(--color-fg-subtle)] sm:inline">
                {t('subscription:planCount', { count: sorted.length, defaultValue: `${sorted.length} plans` })}
              </span>
            ) : null}
          </motion.div>

          {loading ? (
            <CardsSkeleton />
          ) : (
            <motion.div variants={container} className="mt-6 grid items-stretch gap-5 sm:grid-cols-2 lg:grid-cols-3">
              {sorted.map((g) => (
                <TierCard
                  key={g.id}
                  group={g}
                  isCurrent={g.id === currentId}
                  isRecommended={g.id === recommendedId}
                  variants={item}
                  groupBuyUrl={credits?.group_buy_url}
                  onUpgrade={() => setUpgrade(g)}
                  t={t}
                />
              ))}
            </motion.div>
          )}

          {/* ── Redeem a code ─────────────────────────────────────────────── */}
          <motion.section
            variants={item}
            className="mt-16 overflow-hidden rounded-[16px] border border-[var(--color-border)] bg-[var(--color-surface)]"
          >
            <div className="flex flex-col gap-5 p-6 sm:flex-row sm:items-center sm:gap-7 sm:p-7">
              <div className="flex items-start gap-3.5 sm:max-w-[40ch]">
                <span className="mt-0.5 inline-flex size-10 shrink-0 items-center justify-center rounded-[12px] bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]">
                  <Ticket size={18} aria-hidden />
                </span>
                <div className="min-w-0">
                  <h3 className="font-serif text-[1.25rem] tracking-[-0.01em] text-[var(--color-fg)]">
                    {t('subscription:redeem.title')}
                  </h3>
                  <p className="mt-1 text-[13px] leading-relaxed text-[var(--color-fg-muted)]">
                    {t('subscription:redeem.subtitle')}
                  </p>
                </div>
              </div>
              <form
                className="flex flex-1 flex-col gap-2.5 sm:flex-row sm:items-end sm:justify-end"
                onSubmit={(e) => {
                  e.preventDefault()
                  void submitRedeem()
                }}
              >
                <Field label={t('subscription:redeem.inputLabel')} htmlFor="redeem-code" className="sm:w-[18rem]">
                  <Input
                    id="redeem-code"
                    value={redeemCode}
                    onChange={(e) => setRedeemCode(e.target.value.toUpperCase())}
                    placeholder={t('subscription:redeem.inputPlaceholder')}
                    autoComplete="off"
                    spellCheck={false}
                    className="font-mono tracking-[0.18em]"
                  />
                </Field>
                <Button type="submit" loading={redeeming} disabled={!redeemCode.trim() || redeeming} className="shrink-0">
                  {redeeming ? t('subscription:redeem.redeeming') : t('subscription:redeem.submit')}
                </Button>
              </form>
            </div>
          </motion.section>
        </motion.div>
      </div>

      {/* Redeem success — celebratory confirmation (§ redeem codes). */}
      <Dialog open={Boolean(redeemSuccess)} onOpenChange={(o) => !o && setRedeemSuccess(null)}>
        <DialogContent size="sm">
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
                    ? t('subscription:redeem.successBodyUntil', { group: redeemSuccess.group, date: redeemSuccess.date })
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

      {/* Group-override confirm — the code grants a different tier, which replaces
          the current one immediately (not a renewal). Require an explicit OK. */}
      <Dialog open={Boolean(confirmOverride)} onOpenChange={(o) => !o && !redeeming && setConfirmOverride(null)}>
        <DialogContent size="sm">
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
            <Button
              loading={redeeming}
              onClick={() => confirmOverride && void applyRedeem(confirmOverride.code, true)}
            >
              {t('subscription:redeem.override.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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

/** Credit-balance region: timed meter + permanent balance, separated by a divider. */
function Balance({ credits, hasPlanHeader, t }: { credits: ApiCredits; hasPlanHeader: boolean; t: TFn }) {
  const timed = credits.timed
  const showTimed = Boolean(timed && timed.allowance > 0 && timed.period_seconds > 0)
  const pct = timed && timed.allowance > 0 ? Math.max(0, Math.min(100, (timed.remaining / timed.allowance) * 100)) : 0
  return (
    <div
      className={cn(
        'bg-[var(--color-bg-muted)] p-7 sm:p-9',
        hasPlanHeader && 'border-t border-[var(--color-divider)]',
      )}
    >
      <span className="inline-flex items-center gap-2 text-[13px] font-medium text-[var(--color-fg-muted)]">
        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" aria-hidden />
        {t('subscription:credits.sectionTitle', { defaultValue: 'Credit balance' })}
      </span>

      <div className={cn('mt-5 grid gap-8', showTimed ? 'sm:grid-cols-2 sm:gap-12' : 'sm:grid-cols-1')}>
        {/* Timed pool */}
        {showTimed && timed ? (
          <div>
            <div className="flex items-center justify-between gap-2">
              <span className="inline-flex items-center gap-1.5 text-[13px] font-medium text-[var(--color-accent)]">
                <Clock size={15} aria-hidden />
                {t('subscription:credits.timedTitle')}
              </span>
              <span className="text-[12px] font-semibold tabular-nums text-[var(--color-accent)]">{Math.round(pct)}%</span>
            </div>
            <div className="mt-3 flex items-baseline gap-1.5">
              <span className="font-serif text-[2.75rem] leading-none tabular-nums tracking-[-0.02em] text-[var(--color-fg)]">
                {fmtCredits(timed.remaining)}
              </span>
              <span className="text-[16px] tabular-nums text-[var(--color-fg-subtle)]">
                / {fmtCredits(timed.allowance)}
              </span>
            </div>
            <div className="mt-3.5 h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-accent-soft)]">
              <div
                className="h-full rounded-full bg-[var(--color-accent)] transition-[width] duration-700 ease-[var(--ease-out)]"
                style={{ width: `${pct}%` }}
              />
            </div>
            {timed.resets_at > 0 ? (
              <p className="mt-3 inline-flex items-center gap-1.5 text-[12.5px] text-[var(--color-fg-subtle)]">
                <RefreshCw size={13} aria-hidden />
                {t('subscription:credits.resetsOn', { date: formatDateTime(timed.resets_at * 1000) })}
              </p>
            ) : null}
          </div>
        ) : null}

        {/* Permanent pool */}
        <div className={cn('relative', showTimed && 'sm:border-l sm:border-[var(--color-divider)] sm:pl-12')}>
          <span className="inline-flex items-center gap-1.5 text-[13px] font-medium text-[var(--color-secondary)]">
            <Wallet size={15} aria-hidden />
            {t('subscription:credits.permanentTitle')}
          </span>
          <div className="mt-3 font-serif text-[2.75rem] leading-none tabular-nums tracking-[-0.02em] text-[var(--color-fg)]">
            {fmtCredits(credits.permanent)}
          </div>
          <p className="mt-2.5 max-w-[34ch] text-[13px] leading-relaxed text-[var(--color-fg-muted)]">
            {t('subscription:credits.permanentHint')}
          </p>
          {credits.buy_url ? (
            <Button asChild size="sm" variant="secondary" leadingIcon={<Plus size={14} aria-hidden />} className="mt-4">
              <a href={safeHref(credits.buy_url)} target="_blank" rel="noreferrer noopener">
                {t('subscription:credits.topUp')}
              </a>
            </Button>
          ) : (
            <span className="mt-4 inline-block rounded-[8px] bg-[var(--color-surface-sunken)] px-3 py-1.5 text-[12px] text-[var(--color-fg-subtle)]">
              {t('subscription:credits.topUpUnavailable')}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}

/**
 * One tier in the pricing grid. Cards are equal height (CTA pinned to the bottom);
 * the recommended tier is raised with an accent border + ribbon, the current tier
 * is recessed and its CTA disabled.
 */
function TierCard({
  group,
  isCurrent,
  isRecommended,
  variants,
  groupBuyUrl,
  onUpgrade,
  t,
}: {
  group: ApiUserGroup
  isCurrent: boolean
  isRecommended: boolean
  variants: Variants
  groupBuyUrl?: string
  onUpgrade: () => void
  t: TFn
}) {
  const bullets = group.features.filter((f) => f !== 'research')
  return (
    <motion.article
      variants={variants}
      className={cn(
        'relative flex flex-col rounded-[16px] p-7 transition-[border-color,box-shadow] duration-200',
        isRecommended
          ? 'border-2 border-[var(--color-accent)] bg-[var(--color-surface)] shadow-[var(--shadow-lg)] lg:-translate-y-3'
          : isCurrent
            ? 'border border-[var(--color-border)] bg-[var(--color-bg-muted)]'
            : 'border border-[var(--color-border)] bg-[var(--color-surface)] hover:border-[var(--color-border-strong)] hover:shadow-[var(--shadow-md)]',
      )}
    >
      {isRecommended ? (
        <span className="absolute right-7 top-0 -translate-y-1/2 rounded-full bg-[var(--color-accent)] px-3 py-1 text-[10.5px] font-semibold uppercase tracking-[0.06em] text-[var(--color-accent-fg)] shadow-[var(--shadow-sm)]">
          {t('subscription:recommended', { defaultValue: 'Recommend' })}
        </span>
      ) : null}

      <div className="flex items-center justify-between gap-2">
        <h3 className="font-serif text-[1.4rem] tracking-[-0.01em] text-[var(--color-fg)]">{group.name}</h3>
        {isCurrent ? (
          <Badge size="sm" variant="accent">
            {t('subscription:current')}
          </Badge>
        ) : group.is_default ? (
          <Badge size="sm" variant="neutral">
            {t('subscription:free')}
          </Badge>
        ) : null}
      </div>

      <p className="mt-2 min-h-[2.5rem] text-[13px] leading-relaxed text-[var(--color-fg-muted)]">
        {group.description || ' '}
      </p>

      <div className="mt-3">
        <PriceTag group={group} t={t} />
      </div>

      <hr className="mt-6 border-[var(--color-divider)]" />

      {bullets.length > 0 ? (
        <ul className="mt-6 flex flex-col gap-3">
          {bullets.map((f, i) => (
            <li key={i} className="flex items-start gap-2.5 text-[13px] text-[var(--color-fg)]">
              <Check
                size={16}
                aria-hidden
                className={cn('mt-[1px] shrink-0', isCurrent ? 'text-[var(--color-secondary)]' : 'text-[var(--color-accent)]')}
              />
              <span className="leading-snug">{f}</span>
            </li>
          ))}
        </ul>
      ) : null}

      <div className="mt-8 grow flex items-end">
        {isCurrent ? (
          <Button variant="secondary" disabled className="w-full">
            {t('subscription:youreOnThis')}
          </Button>
        ) : !group.is_default && groupBuyUrl ? (
          <Button asChild variant="primary" trailingIcon={<ArrowUpRight size={15} aria-hidden />} className="w-full">
            <a href={safeHref(groupBuyUrl)} target="_blank" rel="noreferrer noopener">
              {t('subscription:buyCta', { defaultValue: 'Purchase' })}
            </a>
          </Button>
        ) : (
          <Button variant={group.is_default ? 'secondary' : 'primary'} className="w-full" onClick={onUpgrade}>
            {group.is_default ? t('subscription:switchCta') : t('subscription:upgradeCta')}
          </Button>
        )}
      </div>
    </motion.article>
  )
}

/** Price as a typographic moment: serif numerals, small superscript currency. */
function PriceTag({ group, t, size = 'md' }: { group: ApiUserGroup; t: TFn; size?: 'md' | 'lg' }) {
  const free = group.is_default || (group.price_usd <= 0 && group.price_cny <= 0)
  const numCls = size === 'lg' ? 'text-[2.5rem]' : 'text-[2rem]'
  if (free) {
    return (
      <span className={cn('font-serif tracking-[-0.02em] leading-none text-[var(--color-fg)]', numCls)}>
        {t('subscription:priceFree')}
      </span>
    )
  }
  return (
    <div className={cn('flex items-baseline gap-2', size === 'lg' && 'sm:justify-end')}>
      <span className={cn('font-serif tracking-[-0.02em] leading-none tabular-nums text-[var(--color-fg)]', numCls)}>
        <span className="align-top text-[0.5em] text-[var(--color-fg-muted)]">$</span>
        {group.price_usd}
      </span>
      {group.price_cny > 0 ? (
        <span className="text-[13px] tabular-nums text-[var(--color-fg-subtle)] line-through decoration-[var(--color-border-strong)]">
          ¥{group.price_cny}
        </span>
      ) : null}
    </div>
  )
}

function fmtCredits(n: number): string {
  return Number.isInteger(n) ? String(n) : n.toFixed(1)
}

function AccountSkeleton() {
  return (
    <div className="animate-pulse overflow-hidden rounded-[18px] border border-[var(--color-border)] bg-[var(--color-surface)]">
      <div className="p-7 sm:p-9">
        <div className="h-3 w-24 rounded bg-[var(--color-bg-muted)]" />
        <div className="mt-4 h-10 w-60 rounded bg-[var(--color-bg-muted)]" />
        <div className="mt-4 h-4 w-80 max-w-full rounded bg-[var(--color-bg-muted)]" />
      </div>
      <div className="border-t border-[var(--color-divider)] bg-[var(--color-bg-muted)] p-7 sm:p-9">
        <div className="grid gap-12 sm:grid-cols-2">
          {Array.from({ length: 2 }).map((_, i) => (
            <div key={i}>
              <div className="h-3.5 w-28 rounded bg-[var(--color-surface-sunken)]" />
              <div className="mt-3 h-10 w-32 rounded bg-[var(--color-surface-sunken)]" />
              <div className="mt-4 h-1.5 w-full rounded-full bg-[var(--color-surface-sunken)]" />
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function CardsSkeleton() {
  return (
    <div className="mt-6 grid items-stretch gap-5 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: 3 }).map((_, i) => (
        <div key={i} className="animate-pulse rounded-[16px] border border-[var(--color-border)] bg-[var(--color-surface)] p-7">
          <div className="h-6 w-24 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-3 h-9 w-24 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-6 flex flex-col gap-3 border-t border-[var(--color-divider)] pt-6">
            <div className="h-3.5 w-full rounded bg-[var(--color-bg-muted)]" />
            <div className="h-3.5 w-5/6 rounded bg-[var(--color-bg-muted)]" />
            <div className="h-3.5 w-4/6 rounded bg-[var(--color-bg-muted)]" />
          </div>
          <div className="mt-8 h-10 w-full rounded-[10px] bg-[var(--color-bg-muted)]" />
        </div>
      ))}
    </div>
  )
}
