/**
 * Subscription — the member-facing view of the user-group tiers (§ user groups).
 * An editorial billing surface: an account panel (current plan + a prominent
 * credit-balance region), every tier as a price card, and a redeem panel that
 * flips the group via a code. Switching is admin-assigned, so most CTAs open a
 * short "how to upgrade" note (unless a global purchase link is configured).
 */
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { motion, useReducedMotion, type Variants } from 'framer-motion'
import { Check, Sparkles, Ticket, Coins, Wallet, Plus, RefreshCw, ArrowUpRight } from 'lucide-react'
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
  // Celebratory modal shown after a successful redemption (§ redeem codes).
  const [redeemSuccess, setRedeemSuccess] = useState<{ group: string; date: string } | null>(null)

  useEffect(() => {
    let active = true
    groupsApi
      .list()
      .then((gs) => active && setGroups(gs))
      .catch((e) => active && toast.error(e instanceof ApiError ? e.message : t('subscription:loadFailed')))
      .finally(() => active && setLoading(false))
    // Credits ride on top of the tier list — a failure must never block the view.
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
  // Membership window (set by redeem codes). 0 = permanent or default tier.
  const expiresAt = user?.group_expires_at ?? 0
  const expiresLabel = expiresAt > 0 ? t('subscription:expiresOn', { date: formatAbsoluteDate(expiresAt * 1000) }) : null

  /**
   * Maps the backend's `error` codes to a user-facing toast. The backend uses
   * sentinel strings (code_invalid / code_used / …) so the frontend can pick the
   * right translation without parsing free-form messages.
   */
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

  async function submitRedeem() {
    const code = redeemCode.trim()
    if (!code) {
      toast.error(t('subscription:redeem.errors.empty'))
      return
    }
    setRedeeming(true)
    try {
      const res = await redeemApi.redeem(code)
      setUser(res.user)
      groupsApi.list().then(setGroups).catch(() => undefined)
      authApi.credits().then(setCredits).catch(() => undefined)
      setRedeemCode('')
      const date = res.expires_at > 0 ? formatAbsoluteDate(res.expires_at * 1000) : ''
      setRedeemSuccess({ group: res.group_name, date })
    } catch (e) {
      toast.error(redeemErrorMessage(e))
    } finally {
      setRedeeming(false)
    }
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
          {/* ── Account panel: current plan + credit balance ───────────────── */}
          {loading ? (
            <AccountSkeleton />
          ) : showAccount ? (
            <motion.section
              variants={item}
              className="overflow-hidden rounded-[18px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-sm)]"
            >
              {current ? (
                <div className="flex flex-col gap-6 p-7 sm:flex-row sm:items-end sm:justify-between sm:p-9">
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
                  <div className="shrink-0 sm:text-right">
                    <PriceTag group={current} t={t} size="lg" />
                  </div>
                </div>
              ) : null}

              {/* Credit balance — its own recessed region, independent of plan. */}
              {creditsOn && credits ? (
                <Balance credits={credits} hasPlanHeader={Boolean(current)} t={t} />
              ) : null}
            </motion.section>
          ) : null}

          {/* ── All plans ──────────────────────────────────────────────────── */}
          <motion.div variants={item} className="mt-16 flex items-baseline justify-between gap-4">
            <h2 className="font-serif text-[1.625rem] tracking-[-0.015em] text-[var(--color-fg)]">
              {t('subscription:allPlans')}
            </h2>
            {sorted.length > 0 ? (
              <span className="text-[12.5px] tabular-nums text-[var(--color-fg-subtle)]">
                {t('subscription:planCount', { count: sorted.length, defaultValue: `${sorted.length} plans` })}
              </span>
            ) : null}
          </motion.div>
          <motion.p variants={item} className="mt-1.5 max-w-[60ch] text-[14px] leading-relaxed text-[var(--color-fg-muted)]">
            {t('subscription:subtitle')}
          </motion.p>

          {loading ? (
            <CardsSkeleton />
          ) : (
            <motion.div variants={container} className="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {sorted.map((g) => (
                <TierCard
                  key={g.id}
                  group={g}
                  isCurrent={g.id === currentId}
                  variants={item}
                  hoverY={reduce ? 0 : -4}
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
            <DialogTitle className="text-center">{t('subscription:redeem.success')}</DialogTitle>
            <DialogDescription className="text-center">
              {redeemSuccess
                ? redeemSuccess.date
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

/**
 * Balance — the prominent credit-balance region. A recessed panel with the timed
 * pool (large remaining + usage meter + minute-precise refresh) and the permanent
 * pool (balance + top-up), separated by a divider. Renders whenever credits are
 * enabled, independent of whether the current plan resolved.
 */
function Balance({ credits, hasPlanHeader, t }: { credits: ApiCredits; hasPlanHeader: boolean; t: TFn }) {
  const timed = credits.timed
  const showTimed = Boolean(timed && timed.allowance > 0 && timed.period_seconds > 0)
  const pct = timed && timed.allowance > 0 ? Math.max(0, Math.min(100, (timed.remaining / timed.allowance) * 100)) : 0
  return (
    <div className={cn('bg-[var(--color-bg-muted)] p-7 sm:p-9', hasPlanHeader && 'border-t border-[var(--color-divider)]')}>
      <span className="inline-flex items-center gap-2 text-[13px] font-medium text-[var(--color-fg-muted)]">
        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" aria-hidden />
        {t('subscription:credits.sectionTitle', { defaultValue: 'Credit balance' })}
      </span>

      <div
        className={cn(
          'mt-5 grid gap-7',
          showTimed ? 'sm:grid-cols-2 sm:gap-10' : 'sm:grid-cols-1',
        )}
      >
        {/* Timed pool */}
        {showTimed && timed ? (
          <div>
            <div className="flex items-center justify-between gap-2">
              <span className="inline-flex items-center gap-1.5 text-[13px] font-medium text-[var(--color-fg-muted)]">
                <Coins size={15} aria-hidden className="text-[var(--color-accent)]" />
                {t('subscription:credits.timedTitle')}
              </span>
              <span className="text-[12px] tabular-nums text-[var(--color-fg-subtle)]">{Math.round(pct)}%</span>
            </div>
            <div className="mt-3 flex items-baseline gap-2">
              <span className="font-serif text-[2.75rem] leading-none tabular-nums tracking-[-0.02em] text-[var(--color-fg)]">
                {fmtCredits(timed.remaining)}
              </span>
              <span className="text-[14px] tabular-nums text-[var(--color-fg-subtle)]">
                / {fmtCredits(timed.allowance)}
              </span>
            </div>
            <div className="mt-4 h-2.5 w-full overflow-hidden rounded-full bg-[var(--color-surface-sunken)]">
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
        <div className={cn(showTimed && 'sm:border-l sm:border-[var(--color-divider)] sm:pl-10')}>
          <span className="inline-flex items-center gap-1.5 text-[13px] font-medium text-[var(--color-fg-muted)]">
            <Wallet size={15} aria-hidden className="text-[var(--color-secondary)]" />
            {t('subscription:credits.permanentTitle')}
          </span>
          <div className="mt-3 font-serif text-[2.75rem] leading-none tabular-nums tracking-[-0.02em] text-[var(--color-fg)]">
            {fmtCredits(credits.permanent)}
          </div>
          <p className="mt-2 max-w-[34ch] text-[12.5px] leading-snug text-[var(--color-fg-subtle)]">
            {t('subscription:credits.permanentHint')}
          </p>
          {credits.buy_url ? (
            <Button
              asChild
              size="sm"
              variant="secondary"
              leadingIcon={<Plus size={14} aria-hidden />}
              className="mt-4"
            >
              <a href={safeHref(credits.buy_url)} target="_blank" rel="noreferrer noopener">
                {t('subscription:credits.topUp')}
              </a>
            </Button>
          ) : (
            <p className="mt-4 text-[12px] text-[var(--color-fg-subtle)]">{t('subscription:credits.topUpUnavailable')}</p>
          )}
        </div>
      </div>
    </div>
  )
}

/** One tier in the plan grid. The current tier reads as selected; others lift on hover. */
function TierCard({
  group,
  isCurrent,
  variants,
  hoverY,
  groupBuyUrl,
  onUpgrade,
  t,
}: {
  group: ApiUserGroup
  isCurrent: boolean
  variants: Variants
  hoverY: number
  groupBuyUrl?: string
  onUpgrade: () => void
  t: TFn
}) {
  // Reserved functional flags (e.g. 'research') are capabilities, not bullets.
  const bullets = group.features.filter((f) => f !== 'research')
  return (
    <motion.article
      variants={variants}
      whileHover={isCurrent ? undefined : { y: hoverY }}
      transition={{ duration: 0.2, ease: EASE }}
      className={cn(
        'relative flex flex-col rounded-[14px] border bg-[var(--color-surface)] p-6',
        'transition-[border-color,box-shadow] duration-200',
        isCurrent
          ? 'border-[var(--color-accent)] shadow-[var(--shadow-md)]'
          : 'border-[var(--color-border)] hover:border-[var(--color-border-strong)] hover:shadow-[var(--shadow-md)]',
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-2">
          <h3 className="font-serif text-[1.25rem] tracking-[-0.01em] text-[var(--color-fg)]">{group.name}</h3>
          {!isCurrent && group.is_default ? (
            <Badge size="xs" variant="neutral">
              {t('subscription:free')}
            </Badge>
          ) : null}
        </div>
        {isCurrent ? (
          <Badge size="xs" variant="accent">
            {t('subscription:current')}
          </Badge>
        ) : null}
      </div>

      {group.description ? (
        <p className="mt-2 text-[13px] leading-relaxed text-[var(--color-fg-muted)]">{group.description}</p>
      ) : null}

      <div className="mt-5">
        <PriceTag group={group} t={t} />
      </div>

      {bullets.length > 0 ? (
        <ul className="mt-5 flex flex-col gap-2.5 border-t border-[var(--color-divider)] pt-5">
          {bullets.map((f, i) => (
            <li key={i} className="flex items-start gap-2.5 text-[13px] text-[var(--color-fg)]">
              <Check size={15} aria-hidden className="mt-[2px] shrink-0 text-[var(--color-secondary)]" />
              <span className="leading-snug">{f}</span>
            </li>
          ))}
        </ul>
      ) : null}

      <div className="mt-7 grow flex items-end">
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
          <Button variant={group.is_default ? 'ghost' : 'primary'} className="w-full" onClick={onUpgrade}>
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
  const numCls = size === 'lg' ? 'text-[2.5rem]' : 'text-[1.75rem]'
  if (free) {
    return (
      <span className={cn('font-serif tracking-[-0.02em] leading-none text-[var(--color-fg)]', numCls)}>
        {t('subscription:priceFree')}
      </span>
    )
  }
  return (
    <div className="flex items-baseline gap-2.5 sm:justify-end">
      <span className={cn('font-serif tracking-[-0.02em] leading-none tabular-nums text-[var(--color-fg)]', numCls)}>
        <span className="align-top text-[0.5em] text-[var(--color-fg-muted)]">$</span>
        {group.price_usd}
      </span>
      {group.price_cny > 0 ? (
        <span className="text-[13px] tabular-nums text-[var(--color-fg-subtle)]">¥{group.price_cny}</span>
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
        <div className="grid gap-10 sm:grid-cols-2">
          {Array.from({ length: 2 }).map((_, i) => (
            <div key={i}>
              <div className="h-3.5 w-28 rounded bg-[var(--color-surface-sunken)]" />
              <div className="mt-3 h-10 w-32 rounded bg-[var(--color-surface-sunken)]" />
              <div className="mt-4 h-2.5 w-full rounded-full bg-[var(--color-surface-sunken)]" />
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function CardsSkeleton() {
  return (
    <div className="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: 3 }).map((_, i) => (
        <div key={i} className="animate-pulse rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
          <div className="h-5 w-24 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-4 h-8 w-20 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-6 flex flex-col gap-2.5 border-t border-[var(--color-divider)] pt-5">
            <div className="h-3.5 w-full rounded bg-[var(--color-bg-muted)]" />
            <div className="h-3.5 w-5/6 rounded bg-[var(--color-bg-muted)]" />
            <div className="h-3.5 w-4/6 rounded bg-[var(--color-bg-muted)]" />
          </div>
          <div className="mt-7 h-9 w-full rounded-[10px] bg-[var(--color-bg-muted)]" />
        </div>
      ))}
    </div>
  )
}
