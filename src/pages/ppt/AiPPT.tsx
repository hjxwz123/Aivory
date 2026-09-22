/**
 * The AI PPT page (§ AI PPT / Docmee iframe, "接入方案二").
 *
 * The Docmee iframe SDK is loaded from a URL pinned in admin settings and mounted
 * into a plain container here — no npm import, no build-time dependency on a
 * third party. Everything that matters for billing stays on our side:
 *
 *   - the server mints the short-lived iframe token (the API key never reaches
 *     the browser);
 *   - a generation attempt holds `credits_per_ppt` from the user's balance before
 *     it starts, and is settled once the upstream reports the deck id;
 *   - a failed/abandoned generation is refunded, and an unstettled hold expires
 *     server-side so a closed tab cannot strand credits.
 *
 * The page is deliberately honest about state: an unconfigured integration says
 * so (and points at the admin setting) instead of rendering an empty frame.
 */
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Coins, ExternalLink, LayoutDashboard, Presentation, RefreshCw, Sparkles } from 'lucide-react'

import { ApiError, aipptApi } from '@/api'
import type { ApiAiPPTConfig } from '@/api/types'
import { ContentHeader } from '@/components/layout/content-header'
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
import { createAiPPTBilling, type AiPPTBilling } from '@/lib/aippt-billing'
import {
  DocmeeSDKError,
  loadDocmeeSDK,
  type DocmeeInstance,
  type DocmeeMessage,
} from '@/lib/docmee-sdk'
import { normalizeLanguage } from '@/i18n'
import { cn } from '@/lib/utils'
import { useAiPPT } from '@/store/aippt'
import { useTheme } from '@/store/theme'

type SDKState = 'idle' | 'loading' | 'ready' | 'error'

/** Pages the embedded SDK can show; mapping is page → our own header toggle. */
const SDK_PAGES = ['creator', 'dashboard'] as const
type SDKPage = (typeof SDK_PAGES)[number]

function apiErrorCode(error: unknown): string | null {
  if (!(error instanceof ApiError)) return null
  const body = error.body as { code?: unknown } | null
  return typeof body?.code === 'string' ? body.code : null
}

export default function AiPPT() {
  const { t, i18n } = useTranslation(['ppt', 'common'])
  const navigate = useNavigate()
  const resolvedTheme = useTheme((s) => s.resolved)
  const config = useAiPPT((s) => s.config)
  const configStatus = useAiPPT((s) => s.status)
  const configError = useAiPPT((s) => s.error)
  const available = useAiPPT((s) => s.available)
  const loadConfig = useAiPPT((s) => s.load)
  const setAvailable = useAiPPT((s) => s.setAvailable)

  const [sdkState, setSdkState] = useState<SDKState>('idle')
  const [sdkError, setSdkError] = useState<string | null>(null)
  const [page, setPage] = useState<SDKPage>('creator')
  const [insufficientOpen, setInsufficientOpen] = useState(false)
  const [billingBusy, setBillingBusy] = useState(false)

  const containerRef = useRef<HTMLDivElement | null>(null)
  const instanceRef = useRef<DocmeeInstance | null>(null)
  const billingRef = useRef<AiPPTBilling | null>(null)
  /** Set while a generation hold is being requested, to debounce the SDK event. */
  const holdRef = useRef<Promise<boolean> | null>(null)
  /** True once the upstream actually began generating this page's deck. */
  const generationStartedRef = useRef(false)

  const lang = useMemo(() => normalizeLanguage(i18n.language) ?? 'en', [i18n.language])
  const price = config?.credits_per_ppt ?? 0
  const billed = Boolean(config?.credits_enabled && price > 0)

  // ----- billing ------------------------------------------------------------

  const ensureBilling = useCallback((): AiPPTBilling => {
    if (!billingRef.current) {
      billingRef.current = createAiPPTBilling({
        attempt: () => aipptApi.attempt(),
        charge: (attemptId, pptId) => aipptApi.charge(attemptId, pptId),
        release: (attemptId) => aipptApi.release(attemptId),
        onBalance: (next) => setAvailable(next),
        onCharged: (outcome) => {
          if (outcome.alreadyCharged) return
          toast.success(
            t('ppt:charged.title', { price: outcome.credits }),
            t('ppt:charged.description', { available: outcome.creditsAvailable }),
          )
        },
      })
    }
    return billingRef.current
  }, [setAvailable, t])

  /** Open (or reuse) the credit hold. False = block generation and explain why. */
  const beginPaidGeneration = useCallback(async (): Promise<boolean> => {
    if (!billed) return true
    // Coalesce the outline/ppt beforeGenerate pair (and SDK retries) into one hold.
    if (holdRef.current) return holdRef.current
    setBillingBusy(true)
    const pending = ensureBilling()
      .ensure()
      .then(() => true)
      .catch((error: unknown) => {
        // Only a refused hold means "you cannot afford this". Anything else is a
        // service problem and must not be dressed up as a billing dialog.
        if (apiErrorCode(error) === 'insufficient_credits') {
          setInsufficientOpen(true)
          return false
        }
        toast.error(t('ppt:errors.generic'), error instanceof Error ? error.message : undefined)
        return false
      })
      .finally(() => {
        holdRef.current = null
        setBillingBusy(false)
      })
    holdRef.current = pending
    return pending
  }, [billed, ensureBilling, t])

  const settleGeneration = useCallback(
    (pptId?: string) => {
      if (!billed) return
      void ensureBilling()
        .settle(pptId)
        .catch((error: unknown) => {
          toast.error(t('ppt:errors.charge'), error instanceof Error ? error.message : undefined)
        })
    },
    [billed, ensureBilling, t],
  )

  const abandonGeneration = useCallback(() => {
    void billingRef.current?.fail()
  }, [])

  // ----- SDK lifecycle ------------------------------------------------------

  const mountSDK = useCallback(
    async (current: ApiAiPPTConfig) => {
      const container = containerRef.current
      if (!container) return
      setSdkState('loading')
      setSdkError(null)
      try {
        const DocmeeUI = await loadDocmeeSDK(current.sdk_url)
        const token = (await aipptApi.token()).token
        instanceRef.current?.destroy()
        container.replaceChildren()
        instanceRef.current = new DocmeeUI({
          container,
          token,
          page,
          creatorVersion: current.creator_version,
          mode: resolvedTheme,
          lang,
          // The international build must be told explicitly; the China build
          // resolves its own API origin and must not receive an empty DOMAIN.
          ...(current.domain ? { DOMAIN: current.domain } : {}),
          ...(current.sdk_base_url ? { baseURL: current.sdk_base_url } : {}),
          downloadButton: current.download_button,
          isMobile: window.matchMedia('(max-width: 767px)').matches,
          onMessage: (message: DocmeeMessage) => {
            switch (message.type) {
              case 'mounted':
                setSdkState('ready')
                return undefined
              case 'beforeGenerate': {
                const subtype = typeof message.data?.subtype === 'string' ? message.data.subtype : ''
                // 'outline' only needs the affordability pre-check, which the
                // hold itself performs — the same coalesced hold is reused when
                // the user confirms the template and generation really starts.
                if (subtype === 'outline' || subtype === 'ppt') {
                  if (subtype === 'ppt') generationStartedRef.current = true
                  return beginPaidGeneration()
                }
                return true
              }
              case 'charge':
              case 'afterGenerate': {
                const id = typeof message.data?.id === 'string' ? message.data.id : undefined
                settleGeneration(id)
                return undefined
              }
              case 'error': {
                // The SDK has shipped `code` as both a number and a string.
                const code = message.data?.code
                if (code === 88 || code === '88') {
                  setInsufficientOpen(true)
                } else if (message.data?.message) {
                  toast.error(t('ppt:errors.upstream'), String(message.data.message))
                }
                // A failed generation must not keep the user's credits on hold.
                abandonGeneration()
                return undefined
              }
              case 'invalid-token': {
                // The upstream token expired (or was rotated): mint a fresh one
                // in place rather than forcing a page reload.
                void aipptApi
                  .token()
                  .then((fresh) => instanceRef.current?.updateToken(fresh.token))
                  .catch(() => setSdkState('error'))
                return undefined
              }
              default:
                return undefined
            }
          },
        })
      } catch (error) {
        setSdkState('error')
        setSdkError(
          error instanceof DocmeeSDKError
            ? error.message
            : error instanceof Error
              ? error.message
              : String(error),
        )
      }
    },
    [abandonGeneration, beginPaidGeneration, lang, page, resolvedTheme, settleGeneration, t],
  )

  // Load the shared config once (the sidebar reads the same store); the SDK
  // only mounts when the integration is actually usable.
  useEffect(() => {
    void loadConfig()
  }, [loadConfig])

  useEffect(() => {
    if (!config?.enabled) return
    void mountSDK(config)
    // Re-mounting on language/theme changes would restart the SDK session, so
    // only the resolved config identity drives this effect.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config?.enabled, config?.sdk_url, config?.creator_version, config?.domain, config?.sdk_base_url])

  useEffect(
    () => () => {
      instanceRef.current?.destroy()
      instanceRef.current = null
      const billing = billingRef.current
      billingRef.current = null
      if (!billing) return
      // Leaving the page refunds a hold that never produced anything (the user
      // was only browsing the creator). A generation that already started keeps
      // its hold: the deck may still be produced upstream, and releasing now
      // would hand it out for free. That hold expires server-side instead.
      if (generationStartedRef.current) billing.reset()
      else void billing.fail()
    },
    [],
  )

  // Switching between "create" and "my decks" is an SDK navigation, not a
  // remount: the session (and any open hold) survives.
  const goTo = useCallback(
    (next: SDKPage) => {
      if (next === page) return
      setPage(next)
      instanceRef.current?.navigate?.({ page: next })
      if (!instanceRef.current?.navigate && config) void mountSDK(config)
    },
    [config, mountSDK, page],
  )

  const reload = useCallback(() => {
    if (config) void mountSDK(config)
  }, [config, mountSDK])

  const creditChip = billed ? (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs',
        'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]',
      )}
    >
      <Coins size={13} aria-hidden />
      {t('ppt:price', { price })}
      <span className="text-[var(--color-divider)]" aria-hidden>
        ·
      </span>
      {t('ppt:balance', { available })}
    </span>
  ) : null

  let body: ReactNode
  if (configStatus === 'error') {
    body = (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('ppt:loadFailed.title')}
        description={configError ?? t('ppt:loadFailed.description')}
        action={
          <Button variant="outline" onClick={() => void loadConfig(true)}>
            {t('common:actions.tryAgain')}
          </Button>
        }
      />
    )
  } else if (!config) {
    body = (
      <div className="flex h-full w-full items-center justify-center p-6">
        <Skeleton className="h-full w-full max-w-5xl rounded-[12px]" />
      </div>
    )
  } else if (!config.configured) {
    body = (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('ppt:unconfigured.title')}
        description={t('ppt:unconfigured.description')}
      />
    )
  } else if (!config.enabled) {
    body = (
      <EmptyState
        icon={<Presentation size={22} aria-hidden />}
        title={t('ppt:disabled.title')}
        description={t('ppt:disabled.description')}
      />
    )
  } else {
    body = (
      <div className="relative h-full w-full min-h-0">
        <div ref={containerRef} className="h-full w-full min-h-0" />
        {sdkState === 'loading' || sdkState === 'idle' ? (
          <div className="absolute inset-0 flex items-center justify-center bg-[var(--color-bg)]">
            <Skeleton className="h-full w-full rounded-[12px]" />
          </div>
        ) : null}
        {sdkState === 'error' ? (
          <div className="absolute inset-0 flex items-center justify-center bg-[var(--color-bg)] p-6">
            <EmptyState
              icon={<Presentation size={22} aria-hidden />}
              title={t('ppt:sdkFailed.title')}
              description={sdkError ?? t('ppt:sdkFailed.description')}
              action={
                <Button variant="outline" onClick={reload}>
                  <RefreshCw size={15} aria-hidden className="mr-1.5" />
                  {t('ppt:sdkFailed.retry')}
                </Button>
              }
            />
          </div>
        ) : null}
      </div>
    )
  }

  return (
    <>
      <ContentHeader
        title={t('ppt:title')}
        fluid
        actions={
          <div className="flex items-center gap-2">
            {creditChip}
            <div className="hidden items-center rounded-full bg-[var(--color-bg-muted)] p-0.5 sm:flex">
              <button
                type="button"
                onClick={() => goTo('creator')}
                aria-pressed={page === 'creator'}
                className={cn(
                  'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs interactive',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  page === 'creator'
                    ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
                    : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                )}
              >
                <Sparkles size={13} aria-hidden />
                {t('ppt:pages.creator')}
              </button>
              <button
                type="button"
                onClick={() => goTo('dashboard')}
                aria-pressed={page === 'dashboard'}
                className={cn(
                  'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs interactive',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  page === 'dashboard'
                    ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
                    : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                )}
              >
                <LayoutDashboard size={13} aria-hidden />
                {t('ppt:pages.dashboard')}
              </button>
            </div>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t('ppt:reload')}
              onClick={reload}
              disabled={!config?.enabled || billingBusy}
            >
              <RefreshCw size={15} aria-hidden />
            </Button>
          </div>
        }
      />

      <main className="min-h-0 flex-1 overflow-hidden p-2 sm:p-3">
        <div className="h-full min-h-0 w-full overflow-hidden rounded-[12px] border border-[var(--color-divider)]/60 bg-[var(--color-surface)]">
          {body}
        </div>
      </main>

      {/* Insufficient credits: the deck is not generated and nothing is charged. */}
      <Dialog open={insufficientOpen} onOpenChange={setInsufficientOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('ppt:insufficient.title')}</DialogTitle>
            <DialogDescription>
              {t('ppt:insufficient.description', {
                price,
                available,
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setInsufficientOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              onClick={() => {
                setInsufficientOpen(false)
                navigate('/subscription')
              }}
            >
              <ExternalLink size={15} aria-hidden className="mr-1.5" />
              {t('ppt:insufficient.action')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
