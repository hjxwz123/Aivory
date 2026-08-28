import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowRight, Check, CircleAlert, Compass, Database, Mail, Search, Terminal, Waypoints } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiAdminOnboarding, ApiAdminOnboardingStep } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

type OnboardingAction = 'skip' | 'complete'

interface AdminOnboardingDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Incrementing this refetches readiness before a manually reopened guide. */
  refreshKey: number
  /** Receives each successful guide snapshot; the first controls automatic display. */
  onSnapshot?: (snapshot: ApiAdminOnboarding) => void
  /** Closes through the shell's progress-navigation path, without dismissing the guide. */
  onNavigate: (to: string) => void
}

type StepID = ApiAdminOnboardingStep['id']

interface StepMeta {
  id: StepID
  to: string
  icon: typeof Waypoints
  titleKey: string
}

const STEP_META: Record<StepID, StepMeta> = {
  channel: { id: 'channel', to: '/admin/channels', icon: Waypoints, titleKey: 'channel' },
  chat_model: { id: 'chat_model', to: '/admin/models', icon: Waypoints, titleKey: 'chatModel' },
  default_model: { id: 'default_model', to: '/admin/settings/model-policy', icon: Compass, titleKey: 'defaultModel' },
  embedding: { id: 'embedding', to: '/admin/documents', icon: Database, titleKey: 'embedding' },
  search: { id: 'search', to: '/admin/tools', icon: Search, titleKey: 'search' },
  sandbox: { id: 'sandbox', to: '/admin/tools', icon: Terminal, titleKey: 'sandbox' },
  smtp: { id: 'smtp', to: '/admin/settings/email', icon: Mail, titleKey: 'smtp' },
}

function stepMeta(step: ApiAdminOnboardingStep): StepMeta {
  return STEP_META[step.id]
}

function StepRow({ step, required, profile, onNavigate }: {
  step: ApiAdminOnboardingStep
  required: boolean
  profile: ApiAdminOnboarding['deployment_profile']
  onNavigate: (to: string) => void
}) {
  const { t } = useTranslation('admin')
  const meta = stepMeta(step)
  const Icon = meta.icon
  const stateLabel = step.complete ? t('onboarding.taskComplete') : t('onboarding.taskIncomplete')
  const descriptionKey = step.id === 'sandbox'
    ? `onboarding.task.sandbox.description.${profile}`
    : `onboarding.task.${meta.titleKey}.description`

  return (
    <li>
      <button
        type="button"
        onClick={() => onNavigate(meta.to)}
        className={cn(
          'group flex w-full min-h-[4.5rem] items-center gap-3 px-3 py-3 text-left interactive',
          'hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]',
        )}
      >
        <span
          className={cn(
            'flex size-8 shrink-0 items-center justify-center rounded-[8px]',
            step.complete
              ? 'bg-[var(--color-success-soft)] text-[var(--color-success)]'
              : required
                ? 'bg-[var(--color-accent-soft)] text-[var(--color-accent)]'
                : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]',
          )}
          aria-hidden
        >
          {step.complete ? <Check size={16} strokeWidth={2.5} /> : <Icon size={16} />}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-[13px] font-medium text-[var(--color-fg)]">
            {t(`onboarding.task.${meta.titleKey}.title`)}
          </span>
          <span className="mt-0.5 block text-[12px] leading-4 text-[var(--color-fg-subtle)]">
            {t(descriptionKey)}
          </span>
        </span>
        <span className="sr-only shrink-0 text-[11px] text-[var(--color-fg-faint)] sm:not-sr-only sm:inline">
          {stateLabel}
        </span>
        <span className="flex shrink-0 items-center gap-1 text-[12px] font-medium text-[var(--color-accent)]">
          <span className="hidden lg:inline">{t(`onboarding.task.${meta.titleKey}.action`)}</span>
          <ArrowRight size={14} aria-hidden className="transition-transform duration-150 group-hover:translate-x-0.5" />
        </span>
      </button>
    </li>
  )
}

function GuideGroup({
  title,
  lead,
  steps,
  required,
  profile,
  onNavigate,
}: {
  title: string
  lead: string
  steps: ApiAdminOnboardingStep[]
  required: boolean
  profile: ApiAdminOnboarding['deployment_profile']
  onNavigate: (to: string) => void
}) {
  if (steps.length === 0) return null
  return (
    <section className={cn(!required && 'mt-6')}>
      <div className="flex items-baseline justify-between gap-3 px-1">
        <div>
          <h3 className="text-[13px] font-semibold text-[var(--color-fg)]">{title}</h3>
          <p className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)]">{lead}</p>
        </div>
      </div>
      <ul className="mt-2 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
        {steps.map((step) => <StepRow key={step.id} step={step} required={required} profile={profile} onNavigate={onNavigate} />)}
      </ul>
    </section>
  )
}

export function AdminOnboardingDialog({ open, onOpenChange, refreshKey, onSnapshot, onNavigate }: AdminOnboardingDialogProps) {
  const { t } = useTranslation(['admin', 'common'])
  const [data, setData] = useState<ApiAdminOnboarding | null>(null)
  const [dataFresh, setDataFresh] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [completionConflict, setCompletionConflict] = useState(false)
  const [saving, setSaving] = useState<OnboardingAction | null>(null)
  const requestIDRef = useRef(0)

  const load = async () => {
    const requestID = ++requestIDRef.current
    setLoading(true)
    setLoadError(false)
    setDataFresh(false)
    try {
      const next = await adminApi.onboarding()
      if (requestID !== requestIDRef.current) return
      setData(next)
      setDataFresh(true)
      onSnapshot?.(next)
    } catch (error) {
      if (requestID !== requestIDRef.current) return
      setLoadError(true)
      setDataFresh(false)
      if (error instanceof ApiError && error.status === 401) return
    } finally {
      if (requestID === requestIDRef.current) setLoading(false)
    }
  }

  useEffect(() => {
    setCompletionConflict(false)
    void load()
    // `refreshKey` is intentionally the externally-controlled reload signal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshKey])

  useEffect(() => () => { requestIDRef.current += 1 }, [])

  const requiredComplete = useMemo(
    () => data?.required.filter((step) => step.complete).length ?? 0,
    [data],
  )
  const allRequiredReady = data !== null && dataFresh && requiredComplete === data.required.length
  const canFinish = allRequiredReady && !loading && !loadError
  const recommended = useMemo(
    () => data ? [...data.optional, ...data.full_optional] : [],
    [data],
  )

  async function persist(action: OnboardingAction) {
    if (saving) return
    setCompletionConflict(false)
    setSaving(action)
    try {
      const next = await adminApi.updateOnboarding(action)
      setData(next)
      onSnapshot?.(next)
      onOpenChange(false)
    } catch (error) {
      if (action === 'complete' && error instanceof ApiError && error.status === 409) {
        // Required configuration changed after this dialog's snapshot. Refresh
        // rather than leaving a now-invalid primary action enabled.
        setCompletionConflict(true)
        void load()
        return
      }
      setLoadError(true)
    } finally {
      setSaving(null)
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (saving) return
    if (!nextOpen && open && data?.status === 'unseen') {
      void persist('skip')
      return
    }
    onOpenChange(nextOpen)
  }

  function handleNavigate(to: string) {
    onNavigate(to)
  }

  const profile = data?.deployment_profile ?? 'full'

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent
        size="lg"
        closeDisabled={saving !== null}
        aria-label={t('onboarding.dialogLabel')}
        className="max-w-[min(96vw,42rem)]"
      >
        <DialogHeader className="px-5 pb-4 pt-5 sm:px-6 sm:pt-6">
          <div className="flex items-start gap-3 pr-8">
            <span className="flex size-10 shrink-0 items-center justify-center rounded-[10px] bg-[var(--color-accent-soft)] text-[var(--color-accent)]" aria-hidden>
              <Compass size={19} />
            </span>
            <div className="min-w-0">
              <p className="text-[11px] font-medium text-[var(--color-accent)]">{t('onboarding.eyebrow')}</p>
              <DialogTitle className="mt-0.5 text-[18px] leading-6">{t('onboarding.title')}</DialogTitle>
              <DialogDescription className="max-w-[34rem] text-[13px] leading-5">{t('onboarding.lead')}</DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <DialogBody className="px-5 pb-5 sm:px-6">
          {loading && !data ? (
            <div className="flex min-h-52 flex-col items-center justify-center gap-3 text-[13px] text-[var(--color-fg-muted)]" role="status" aria-live="polite">
              <span aria-hidden className="inline-block size-4 rounded-full border-2 border-[var(--color-accent)] border-r-transparent animate-[spin_800ms_linear_infinite]" />
              <span>{t('onboarding.loading')}</span>
            </div>
          ) : loadError && !data ? (
            <div className="flex min-h-52 flex-col items-center justify-center gap-3 text-center">
              <CircleAlert size={22} className="text-[var(--color-warning)]" aria-hidden />
              <p className="text-[13px] text-[var(--color-fg-muted)]">{t('onboarding.loadFailed')}</p>
              <Button variant="secondary" size="sm" onClick={() => void load()}>{t('onboarding.retry')}</Button>
            </div>
          ) : data ? (
            <>
              <div
                className="flex flex-wrap items-start justify-between gap-3 border-b border-[var(--color-divider)] pb-4"
                aria-busy={loading || undefined}
              >
                <div className="min-w-0">
                  <Badge variant={profile === 'personal' ? 'sage' : 'accent'}>
                    {t(`onboarding.profile.${profile}`)}
                  </Badge>
                  <p className="mt-1 max-w-[28rem] text-[12px] leading-4 text-[var(--color-fg-subtle)]">
                    {t(`onboarding.profileDescription.${profile}`)}
                  </p>
                </div>
                <span className={cn(
                  'pt-0.5 text-[12px] font-medium',
                  loadError
                    ? 'text-[var(--color-warning)]'
                    : allRequiredReady
                      ? 'text-[var(--color-success)]'
                      : 'text-[var(--color-fg-muted)]',
                )}>
                  {loadError
                    ? t('onboarding.loadFailed')
                    : loading
                      ? t('onboarding.loading')
                      : allRequiredReady
                        ? t('onboarding.allRequiredReady')
                        : t('onboarding.progress', { completed: requiredComplete, total: data.required.length })}
                </span>
              </div>
              <div
                className="mt-3 h-1 overflow-hidden rounded-full bg-[var(--color-bg-muted)]"
                role="progressbar"
                aria-label={t('onboarding.progress', { completed: requiredComplete, total: data.required.length })}
                aria-valuemin={0}
                aria-valuemax={data.required.length}
                aria-valuenow={requiredComplete}
                aria-busy={loading || undefined}
              >
                <span
                  className="block h-full rounded-full bg-[var(--color-accent)] transition-[width] duration-300"
                  style={{ width: `${data.required.length ? (requiredComplete / data.required.length) * 100 : 100}%` }}
                />
              </div>
              <GuideGroup
                title={t('onboarding.required.title')}
                lead={t('onboarding.required.lead')}
                steps={data.required}
                required
                profile={profile}
                onNavigate={handleNavigate}
              />
              <GuideGroup
                title={t('onboarding.optional.title')}
                lead={t('onboarding.optional.lead')}
                steps={recommended}
                required={false}
                profile={profile}
                onNavigate={handleNavigate}
              />
              {completionConflict ? (
                <p className="mt-4 text-[12px] text-[var(--color-warning)]" role="status">
                  {t('onboarding.requiredChanged')}
                </p>
              ) : null}
              {loadError ? (
                <div className="mt-4 flex flex-wrap items-center justify-between gap-3" role="alert">
                  <p className="text-[12px] text-[var(--color-warning)]">{t('onboarding.loadFailed')}</p>
                  <Button variant="secondary" size="sm" onClick={() => void load()}>{t('onboarding.retry')}</Button>
                </div>
              ) : null}
            </>
          ) : null}
        </DialogBody>

        {data ? (
          <DialogFooter className={cn(
            'px-5 sm:px-6 max-sm:flex-col max-sm:items-stretch max-sm:gap-2 max-sm:[&_button]:w-full',
            data.status === 'completed' ? 'justify-end' : 'justify-between',
          )}>
            <Button
              variant={data.status === 'completed' ? 'secondary' : 'ghost'}
              onClick={() => data.status === 'unseen' ? void persist('skip') : onOpenChange(false)}
              loading={saving === 'skip'}
              disabled={saving !== null}
            >
              {data.status === 'unseen' ? t('onboarding.skip') : t('common:actions.close')}
            </Button>
            {data.status !== 'completed' ? (
              <Button onClick={() => void persist('complete')} loading={saving === 'complete'} disabled={!canFinish || saving !== null}>
                {t('onboarding.finish')}
              </Button>
            ) : null}
          </DialogFooter>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}
