import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate } from 'react-router-dom'
import { ArrowLeft, ArrowRight, Check, CircleAlert, Compass, Database, Mail, Search, Terminal, Waypoints, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiAdminOnboarding, ApiAdminOnboardingStep } from '@/api/types'
import { Button } from '@/components/ui/button'
import { useMediaQuery } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'

type OnboardingAction = 'skip' | 'complete'
type StepID = ApiAdminOnboardingStep['id']

interface AdminOnboardingTourProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Incrementing this refetches readiness before a manually replayed guide. */
  refreshKey: number
  /** Receives each successful snapshot, including the closed-state bootstrap fetch. */
  onSnapshot?: (snapshot: ApiAdminOnboarding) => void
  /** Called once a non-modal coachmark is visible, so startup notices can continue. */
  onPresented?: () => void
}

interface StepMeta {
  id: StepID
  to: string
  target: string
  icon: typeof Waypoints
  titleKey: string
}

interface TourStep {
  step: ApiAdminOnboardingStep
  required: boolean
}

interface TargetRect {
  top: number
  right: number
  bottom: number
  left: number
  width: number
  height: number
}

const TARGET_TIMEOUT_MS = 1_800

const STEP_META: Record<StepID, StepMeta> = {
  channel: {
    id: 'channel',
    to: '/admin/channels',
    target: 'channels-create',
    icon: Waypoints,
    titleKey: 'channel',
  },
  chat_model: {
    id: 'chat_model',
    to: '/admin/models?kind=chat',
    target: 'models-create',
    icon: Waypoints,
    titleKey: 'chatModel',
  },
  default_model: {
    id: 'default_model',
    to: '/admin/settings/model-policy',
    target: 'model-policy-default-model',
    icon: Compass,
    titleKey: 'defaultModel',
  },
  embedding: {
    id: 'embedding',
    to: '/admin/documents',
    target: 'documents-embedding-model',
    icon: Database,
    titleKey: 'embedding',
  },
  search: {
    id: 'search',
    to: '/admin/tools',
    target: 'tools-search-provider',
    icon: Search,
    titleKey: 'search',
  },
  sandbox: {
    id: 'sandbox',
    to: '/admin/tools',
    target: 'tools-sandbox-url',
    icon: Terminal,
    titleKey: 'sandbox',
  },
  smtp: {
    id: 'smtp',
    to: '/admin/settings/email',
    target: 'email-smtp-host',
    icon: Mail,
    titleKey: 'smtp',
  },
}

function makeTourSteps(data: ApiAdminOnboarding): TourStep[] {
  return [
    ...data.required.map((step) => ({ step, required: true })),
    ...data.optional.map((step) => ({ step, required: false })),
    ...data.full_optional.map((step) => ({ step, required: false })),
  ]
}

function firstStepFor(data: ApiAdminOnboarding): TourStep | undefined {
  const steps = makeTourSteps(data)
  return data.required.find((step) => !step.complete)
    ? steps.find((item) => item.required && !item.step.complete)
    : steps.find((item) => !item.step.complete) ?? steps[0]
}

function readTargetRect(element: HTMLElement): TargetRect {
  const rect = element.getBoundingClientRect()
  return {
    top: rect.top,
    right: rect.right,
    bottom: rect.bottom,
    left: rect.left,
    width: rect.width,
    height: rect.height,
  }
}

function isVisibleTarget(element: HTMLElement): boolean {
  const style = window.getComputedStyle(element)
  if (style.display === 'none' || style.visibility === 'hidden') return false
  const rect = element.getBoundingClientRect()
  return rect.width > 0 && rect.height > 0 && element.getClientRects().length > 0
}

function spotlightStyle(rect: TargetRect): CSSProperties {
  const inset = 6
  return {
    top: Math.max(4, rect.top - inset),
    left: Math.max(4, rect.left - inset),
    width: Math.min(window.innerWidth - 8, rect.width + inset * 2),
    height: Math.min(window.innerHeight - 8, rect.height + inset * 2),
    boxShadow: '0 0 0 1px var(--color-surface-raised), 0 0 0 9999px var(--color-overlay)',
  }
}

function coachmarkStyle(rect: TargetRect | null, compact: boolean): CSSProperties {
  if (compact || !rect) {
    return {
      left: '50%',
      bottom: 'max(0.75rem, var(--safe-bottom))',
      transform: 'translateX(-50%)',
    }
  }

  const viewportWidth = window.innerWidth
  const viewportHeight = window.innerHeight
  const width = Math.max(0, Math.min(336, viewportWidth - 24))
  const estimatedHeight = 226
  const below = rect.bottom + 14
  const above = rect.top - estimatedHeight - 14
  const top = below + estimatedHeight <= viewportHeight - 12 || above < 12
    ? Math.min(below, Math.max(12, viewportHeight - estimatedHeight - 12))
    : above

  return {
    top,
    left: Math.min(Math.max(12, rect.left), Math.max(12, viewportWidth - width - 12)),
    width,
  }
}

function coachmarkForState({
  title,
  description,
  icon: Icon,
  action,
  onClose,
  closeLabel,
}: {
  title: string
  description: string
  icon: typeof Compass | typeof CircleAlert
  action?: ReactNode
  onClose: () => void
  closeLabel: string
}) {
  return (
    <section
      role="dialog"
      aria-modal={false}
      aria-label={title}
      className="fixed bottom-[max(0.75rem,var(--safe-bottom))] left-1/2 z-[var(--z-tour)] w-[min(21rem,calc(100vw-1.5rem))] -translate-x-1/2 rounded-[12px] bg-[var(--color-surface-raised)] p-4 shadow-[var(--shadow-popover)]"
    >
      <div className="flex items-start gap-3">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-[8px] bg-[var(--color-accent-soft)] text-[var(--color-accent)]" aria-hidden>
          <Icon size={16} />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="text-[13px] font-semibold text-[var(--color-fg)]">{title}</h2>
          <p className="mt-1 text-[12px] leading-5 text-[var(--color-fg-muted)]">{description}</p>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label={closeLabel}
          className="-mr-1 -mt-1 inline-flex size-7 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <X size={15} aria-hidden />
        </button>
      </div>
      {action ? <div className="mt-4 flex items-center justify-end gap-2">{action}</div> : null}
    </section>
  )
}

export function AdminOnboardingTour({ open, onOpenChange, refreshKey, onSnapshot, onPresented }: AdminOnboardingTourProps) {
  const { t } = useTranslation('admin')
  const navigate = useNavigate()
  const location = useLocation()
  const compact = useMediaQuery('(max-width: 639px), (max-height: 560px)')
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const [data, setData] = useState<ApiAdminOnboarding | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [saving, setSaving] = useState<OnboardingAction | null>(null)
  const [completionConflict, setCompletionConflict] = useState(false)
  const [currentStepID, setCurrentStepID] = useState<StepID | null>(null)
  const [targetRect, setTargetRect] = useState<TargetRect | null>(null)
  const [targetUnavailable, setTargetUnavailable] = useState(false)
  const [targetRetryKey, setTargetRetryKey] = useState(0)
  const requestIDRef = useRef(0)
  const initializedRef = useRef(false)
  const presentationReportedRef = useRef(false)
  const coachmarkRef = useRef<HTMLElement>(null)

  const load = useCallback(async (): Promise<ApiAdminOnboarding | null> => {
    const requestID = ++requestIDRef.current
    setLoading(true)
    setLoadError(false)
    try {
      const next = await adminApi.onboarding()
      if (requestID !== requestIDRef.current) return null
      setData(next)
      onSnapshot?.(next)
      return next
    } catch (error) {
      if (requestID !== requestIDRef.current) return null
      setLoadError(true)
      if (error instanceof ApiError && error.status === 401) return null
      return null
    } finally {
      if (requestID === requestIDRef.current) setLoading(false)
    }
  }, [onSnapshot])

  useEffect(() => {
    setCompletionConflict(false)
    void load()
  }, [load, refreshKey])

  useEffect(() => () => { requestIDRef.current += 1 }, [])

  const tourSteps = useMemo(() => data ? makeTourSteps(data) : [], [data])
  const currentIndex = tourSteps.findIndex(({ step }) => step.id === currentStepID)
  const currentStep = currentIndex >= 0 ? tourSteps[currentIndex] : null
  const currentMeta = currentStep ? STEP_META[currentStep.step.id] : null
  const requiredComplete = data?.required.filter((step) => step.complete).length ?? 0
  const recommendedSteps = data ? [...data.optional, ...data.full_optional] : []
  const recommendedComplete = recommendedSteps.filter((step) => step.complete).length
  const allRequiredReady = data !== null && data.required.every((step) => step.complete)

  useEffect(() => {
    if (!open) {
      initializedRef.current = false
      presentationReportedRef.current = false
      setCurrentStepID(null)
      setTargetRect(null)
      setTargetUnavailable(false)
      return
    }
    if (!data || initializedRef.current) return
    initializedRef.current = true
    setCurrentStepID(firstStepFor(data)?.step.id ?? null)
  }, [data, open])

  useEffect(() => {
    if (!open || !currentMeta) return
    const destination = `${location.pathname}${location.search}`
    if (destination !== currentMeta.to) navigate(currentMeta.to)
  }, [currentMeta, location.pathname, location.search, navigate, open])

  useEffect(() => {
    if (!open || !currentMeta) return
    const meta = currentMeta
    let target: HTMLElement | null = null
    let frame: number | null = null
    let scrollFrame: number | null = null
    let unavailableTimer: number | null = null
    let scrolledToTarget = false
    let resizeObserver: ResizeObserver | null = null

    const clearFrame = () => {
      if (frame !== null) {
        window.cancelAnimationFrame(frame)
        frame = null
      }
    }

    const clearScrollFrame = () => {
      if (scrollFrame !== null) {
        window.cancelAnimationFrame(scrollFrame)
        scrollFrame = null
      }
    }

    function scheduleUnavailable(): void {
      if (unavailableTimer !== null) return
      unavailableTimer = window.setTimeout(() => {
        unavailableTimer = null
        if (!target) setTargetUnavailable(true)
      }, TARGET_TIMEOUT_MS)
    }

    function measure(): void {
      clearFrame()
      frame = window.requestAnimationFrame(() => {
        frame = null
        if (!target || !target.isConnected || !isVisibleTarget(target)) {
          findTarget()
          return
        }
        setTargetRect(readTargetRect(target))
      })
    }

    function findTarget(): void {
      const nextTarget = Array.from(document.querySelectorAll<HTMLElement>(`[data-admin-tour="${meta.target}"]`))
        .find(isVisibleTarget) ?? null
      if (nextTarget !== target) {
        resizeObserver?.disconnect()
        target = nextTarget
        if (target && typeof ResizeObserver !== 'undefined') {
          resizeObserver = new ResizeObserver(measure)
          resizeObserver.observe(target)
        }
      }
      if (!target) {
        setTargetRect(null)
        scheduleUnavailable()
        return
      }
      if (unavailableTimer !== null) {
        window.clearTimeout(unavailableTimer)
        unavailableTimer = null
      }
      setTargetUnavailable(false)
      if (!scrolledToTarget) {
        scrolledToTarget = true
        scrollFrame = window.requestAnimationFrame(() => {
          scrollFrame = null
          if (!target || !target.isConnected || !isVisibleTarget(target)) return
          target.scrollIntoView({
            block: 'center',
            inline: 'nearest',
            behavior: reducedMotion ? 'auto' : 'smooth',
          })
          measure()
        })
      }
      measure()
    }

    const observer = new MutationObserver(findTarget)
    observer.observe(document.body, { childList: true, subtree: true })
    const onViewportChange = () => measure()
    window.addEventListener('resize', onViewportChange)
    window.addEventListener('scroll', onViewportChange, true)
    window.visualViewport?.addEventListener('resize', onViewportChange)
    window.visualViewport?.addEventListener('scroll', onViewportChange)

    setTargetRect(null)
    setTargetUnavailable(false)
    findTarget()

    return () => {
      if (unavailableTimer !== null) window.clearTimeout(unavailableTimer)
      clearFrame()
      clearScrollFrame()
      observer.disconnect()
      resizeObserver?.disconnect()
      window.removeEventListener('resize', onViewportChange)
      window.removeEventListener('scroll', onViewportChange, true)
      window.visualViewport?.removeEventListener('resize', onViewportChange)
      window.visualViewport?.removeEventListener('scroll', onViewportChange)
    }
  }, [currentMeta, location.key, open, reducedMotion, targetRetryKey])

  useEffect(() => {
    if (!open) return
    const refresh = () => {
      if (document.visibilityState === 'visible') void load()
    }
    window.addEventListener('focus', refresh)
    document.addEventListener('visibilitychange', refresh)
    return () => {
      window.removeEventListener('focus', refresh)
      document.removeEventListener('visibilitychange', refresh)
    }
  }, [load, open])

  const dismiss = useCallback(async () => {
    if (saving) return
    if (data?.status !== 'unseen') {
      onOpenChange(false)
      return
    }
    setSaving('skip')
    setCompletionConflict(false)
    try {
      const next = await adminApi.updateOnboarding('skip')
      setData(next)
      onSnapshot?.(next)
      onOpenChange(false)
    } catch {
      setLoadError(true)
    } finally {
      setSaving(null)
    }
  }, [data?.status, onOpenChange, onSnapshot, saving])

  const finish = useCallback(async () => {
    if (saving || !allRequiredReady) return
    setSaving('complete')
    setCompletionConflict(false)
    try {
      const next = await adminApi.updateOnboarding('complete')
      setData(next)
      onSnapshot?.(next)
      onOpenChange(false)
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        setCompletionConflict(true)
        void load()
        return
      }
      setLoadError(true)
    } finally {
      setSaving(null)
    }
  }, [allRequiredReady, load, onOpenChange, onSnapshot, saving])

  const next = useCallback(async () => {
    const fresh = await load()
    const steps = fresh ? makeTourSteps(fresh) : tourSteps
    const index = steps.findIndex(({ step }) => step.id === currentStepID)
    if (index < 0) {
      const source = fresh ?? data
      setCurrentStepID(source ? firstStepFor(source)?.step.id ?? null : null)
      return
    }
    const nextIncomplete = steps.slice(index + 1).find(({ step }) => !step.complete)
    const followingStep = nextIncomplete ?? steps[index + 1]
    if (followingStep) setCurrentStepID(followingStep.step.id)
  }, [currentStepID, data, load, tourSteps])

  const previous = useCallback(() => {
    if (currentIndex > 0) setCurrentStepID(tourSteps[currentIndex - 1].step.id)
  }, [currentIndex, tourSteps])

  const retryTarget = useCallback(() => {
    setTargetUnavailable(false)
    setTargetRetryKey((current) => current + 1)
  }, [])

  const openTarget = useCallback(() => {
    if (currentMeta) navigate(currentMeta.to)
    retryTarget()
  }, [currentMeta, navigate, retryTarget])

  useEffect(() => {
    if (!open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing) return
      const openModal = document.querySelector<HTMLElement>('[role="dialog"][data-state="open"]')
      if (openModal && !coachmarkRef.current?.contains(openModal)) return
      event.preventDefault()
      void dismiss()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [dismiss, open])

  useEffect(() => {
    if (!open || !currentStep) return
    const frame = window.requestAnimationFrame(() => coachmarkRef.current?.focus())
    return () => window.cancelAnimationFrame(frame)
  }, [currentStep?.step.id, open])

  useEffect(() => {
    if (!open || presentationReportedRef.current) return
    const frame = window.requestAnimationFrame(() => {
      if (presentationReportedRef.current) return
      presentationReportedRef.current = true
      onPresented?.()
    })
    return () => window.cancelAnimationFrame(frame)
  }, [onPresented, open])

  if (!open || typeof document === 'undefined') return null

  const closeLabel = t('onboarding.tour.close')
  if (!data) {
    return createPortal(
      coachmarkForState({
        title: t('onboarding.tour.label'),
        description: loadError ? t('onboarding.loadFailed') : t('onboarding.tour.preparing'),
        icon: loadError ? CircleAlert : Compass,
        onClose: () => { void dismiss() },
        closeLabel,
        action: loadError ? <Button variant="secondary" size="sm" onClick={() => void load()}>{t('onboarding.tour.retry')}</Button> : undefined,
      }),
      document.body,
    )
  }
  const onboarding = data

  if (!currentStep || !currentMeta) {
    return createPortal(
      coachmarkForState({
        title: t('onboarding.tour.label'),
        description: t('onboarding.tour.targetUnavailable'),
        icon: CircleAlert,
        onClose: () => { void dismiss() },
        closeLabel,
      }),
      document.body,
    )
  }

  if (targetUnavailable) {
    return createPortal(
      coachmarkForState({
        title: t(`onboarding.task.${currentMeta.titleKey}.title`),
        description: t('onboarding.tour.targetUnavailable'),
        icon: CircleAlert,
        onClose: () => { void dismiss() },
        closeLabel,
        action: (
          <>
            <Button variant="ghost" size="sm" onClick={openTarget}>{t('onboarding.tour.openTarget')}</Button>
            <Button size="sm" onClick={retryTarget}>{t('onboarding.tour.retry')}</Button>
          </>
        ),
      }),
      document.body,
    )
  }

  const Icon = currentMeta.icon
  const phaseComplete = currentStep.required ? requiredComplete : recommendedComplete
  const phaseTotal = currentStep.required ? onboarding.required.length : recommendedSteps.length
  const phaseLabel = currentStep.required
    ? t('onboarding.tour.requiredProgress')
    : t('onboarding.tour.recommendedProgress')
  const instructionKey = currentStep.step.id === 'sandbox'
    ? `onboarding.task.sandbox.tour.${onboarding.deployment_profile}`
    : `onboarding.task.${currentMeta.titleKey}.tour`
  const canAdvance = currentIndex < tourSteps.length - 1

  return createPortal(
    <>
      {targetRect ? (
        <div
          aria-hidden
          className={cn(
            'pointer-events-none fixed z-[var(--z-tour)] rounded-[10px] border-2 border-[var(--color-accent)]',
            reducedMotion ? '' : 'transition-[top,left,width,height] duration-[var(--duration-fast)] ease-out',
          )}
          style={spotlightStyle(targetRect)}
        />
      ) : null}
      <section
        ref={coachmarkRef}
        role="dialog"
        aria-modal={false}
        aria-label={t('onboarding.tour.label')}
        aria-busy={loading || saving !== null || undefined}
        tabIndex={-1}
        className="fixed z-[var(--z-tour)] max-h-[calc(100dvh-1.5rem)] w-[min(21rem,calc(100vw-1.5rem))] overflow-y-auto rounded-[12px] bg-[var(--color-surface-raised)] p-4 shadow-[var(--shadow-popover)] focus:outline-none"
        style={coachmarkStyle(targetRect, compact)}
      >
        <div className="flex items-start gap-3">
          <span className={cn(
            'flex size-8 shrink-0 items-center justify-center rounded-[8px]',
            currentStep.step.complete
              ? 'bg-[var(--color-success-soft)] text-[var(--color-success)]'
              : currentStep.required
                ? 'bg-[var(--color-accent-soft)] text-[var(--color-accent)]'
                : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]',
          )} aria-hidden>
            {currentStep.step.complete ? <Check size={16} strokeWidth={2.5} /> : <Icon size={16} />}
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] leading-4">
              <span className="font-medium text-[var(--color-accent)]">{phaseLabel}</span>
              <span className="text-[var(--color-fg-subtle)]">{t(`onboarding.profile.${onboarding.deployment_profile}`)}</span>
              <span className="text-[var(--color-fg-subtle)]">
                {t('onboarding.tour.stepProgress', { current: currentIndex + 1, total: tourSteps.length })}
              </span>
            </div>
            <h2 className="mt-1 text-[14px] font-semibold leading-5 text-[var(--color-fg)]">
              {t(`onboarding.task.${currentMeta.titleKey}.title`)}
            </h2>
          </div>
          <button
            type="button"
            onClick={() => { void dismiss() }}
            disabled={saving !== null}
            aria-label={closeLabel}
            className="-mr-1 -mt-1 inline-flex size-7 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-50"
          >
            <X size={15} aria-hidden />
          </button>
        </div>

        <p className="mt-3 text-[12px] leading-5 text-[var(--color-fg-muted)]">
          {t(instructionKey)}
        </p>

        <div className="mt-3 flex items-center justify-between gap-3">
          <span className="flex min-w-0 items-center gap-1.5 text-[11px] text-[var(--color-fg-subtle)]">
            <span className={cn(
              'size-1.5 shrink-0 rounded-full',
              currentStep.step.complete ? 'bg-[var(--color-success)]' : 'bg-[var(--color-warning)]',
            )} aria-hidden />
            {currentStep.step.complete ? t('onboarding.tour.configured') : t('onboarding.tour.needsSetup')}
          </span>
          <span className="shrink-0 text-[11px] text-[var(--color-fg-subtle)]">
            {t('onboarding.tour.stepProgress', { current: phaseComplete, total: phaseTotal })}
          </span>
        </div>

        <div
          className="mt-2 h-1 overflow-hidden rounded-full bg-[var(--color-bg-muted)]"
          role="progressbar"
          aria-label={phaseLabel}
          aria-valuemin={0}
          aria-valuemax={phaseTotal}
          aria-valuenow={phaseComplete}
        >
          <span
            className="block h-full rounded-full bg-[var(--color-accent)]"
            style={{ width: `${phaseTotal ? (phaseComplete / phaseTotal) * 100 : 100}%` }}
          />
        </div>

        {completionConflict ? (
          <p className="mt-3 text-[11px] leading-4 text-[var(--color-warning)]" role="status">
            {t('onboarding.requiredChanged')}
          </p>
        ) : null}
        {loadError ? (
          <p className="mt-3 text-[11px] leading-4 text-[var(--color-warning)]" role="status">
            {t('onboarding.loadFailed')}
          </p>
        ) : null}

        <div className="mt-4 grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-end gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="shrink-0 px-2"
            leadingIcon={<ArrowLeft size={14} aria-hidden />}
            onClick={previous}
            disabled={currentIndex <= 0 || saving !== null}
          >
            {t('onboarding.tour.back')}
          </Button>
          <div className="flex min-w-0 flex-wrap items-center justify-end gap-1.5">
            <Button className="shrink-0 px-2" variant="ghost" size="sm" onClick={() => { void dismiss() }} disabled={saving !== null}>
              {onboarding.status === 'unseen' ? t('onboarding.tour.skip') : t('onboarding.tour.close')}
            </Button>
            {allRequiredReady && canAdvance ? (
              <Button className="shrink-0 px-2" variant="secondary" size="sm" onClick={() => { void next() }} disabled={saving !== null} trailingIcon={<ArrowRight size={14} aria-hidden />}>
                {t('onboarding.tour.next')}
              </Button>
            ) : null}
            <Button
              className="shrink-0 px-2"
              size="sm"
              onClick={() => {
                if (allRequiredReady) {
                  void finish()
                  return
                }
                void next()
              }}
              loading={saving !== null}
              disabled={saving !== null}
              trailingIcon={!allRequiredReady ? <ArrowRight size={14} aria-hidden /> : undefined}
            >
              {allRequiredReady ? t('onboarding.tour.finish') : t('onboarding.tour.next')}
            </Button>
          </div>
        </div>
      </section>
      <p className="sr-only" aria-live="polite" aria-atomic="true">
        {t('onboarding.tour.stepProgress', { current: currentIndex + 1, total: tourSteps.length })}
      </p>
    </>,
    document.body,
  )
}
