import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  AlertTriangle,
  ArrowRight,
  CheckCircle2,
  CircleDollarSign,
  Cpu,
  FileText,
  Image,
  MessageSquareText,
  Network,
  Users,
} from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUsageTotals } from '@/api/types'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { inputOutputTokens } from '@/lib/admin-analytics'
import { getOverviewHealth } from '@/lib/admin-overview'
import { useLanguage } from '@/store/language'

interface OverviewData {
  settings: Record<string, unknown>
  channelCount: number
  modelIds: Set<string>
  modelCount: number
  groupCount: number
  paymentChannelCount: number
  paymentMethodCount: number
  userCount: number
  today: ApiUsageTotals | null
}

interface HealthCheck {
  key: string
  ok: boolean
  label: string
  detail: string
  to: string
}

export default function AdminOverview() {
  const { t } = useTranslation(['admin', 'common'])
  const lang = useLanguage((state) => state.lang)
  const [data, setData] = useState<OverviewData | null>(null)
  const [loading, setLoading] = useState(true)
  const numberFormat = useMemo(() => new Intl.NumberFormat(lang, { maximumFractionDigits: 0 }), [lang])
  const compactNumberFormat = useMemo(
    () => new Intl.NumberFormat(lang, { notation: 'compact', maximumFractionDigits: 1 }),
    [lang],
  )

  async function load() {
    setLoading(true)
    try {
      const [settings, channels, models, groups, paymentChannels, paymentMethods, usersPage] = await Promise.all([
        adminApi.settings(),
        adminApi.channels(),
        adminApi.models(),
        adminApi.userGroups(),
        adminApi.paymentChannels(),
        adminApi.paymentMethods(),
        adminApi.users('', 1, 0),
      ])
      const overview: OverviewData = {
        settings,
        channelCount: channels.length,
        modelIds: new Set(models.map((model) => model.id)),
        modelCount: models.length,
        groupCount: groups.length,
        paymentChannelCount: paymentChannels.length,
        paymentMethodCount: paymentMethods.length,
        userCount: usersPage.total,
        today: null,
      }
      const today = getOverviewHealth(overview).allReady
        ? await adminApi.analytics({ days: 1 }).then((analytics) => analytics.totals).catch(() => null)
        : null
      setData({ ...overview, today })
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (loading) return <PanelFallback />

  if (!data) {
    return (
      <div className="flex min-h-64 flex-col items-center justify-center gap-3 text-sm text-[var(--color-fg-muted)]">
        <p>{t('admin:overview.loadFailed', { defaultValue: 'Could not load the admin overview.' })}</p>
        <Button variant="secondary" size="sm" onClick={() => void load()}>
          {t('common:actions.retry', { defaultValue: 'Retry' })}
        </Button>
      </div>
    )
  }

  const health = getOverviewHealth(data)

  const checks: HealthCheck[] = [
    {
      key: 'channel',
      ok: health.channelReady,
      label: t('admin:overview.checks.channels', { defaultValue: 'Upstream channel' }),
      detail: health.channelReady
        ? t('admin:overview.checks.channelsReady', { defaultValue: '{{count}} channel(s) configured', count: data.channelCount })
        : t('admin:overview.checks.channelsMissing', { defaultValue: 'Create a channel before adding usable models.' }),
      to: '/admin/channels',
    },
    {
      key: 'default-model',
      ok: health.defaultModelReady,
      label: t('admin:overview.checks.defaultModel', { defaultValue: 'Default chat model' }),
      detail: health.defaultModelReady
        ? t('admin:overview.checks.configured', { defaultValue: 'Configured' })
        : t('admin:overview.checks.defaultModelMissing', { defaultValue: 'Chat cannot start until a valid default model is selected.' }),
      to: '/admin/settings/model-policy',
    },
    {
      key: 'task-model',
      ok: health.taskModelReady,
      label: t('admin:overview.checks.taskModel', { defaultValue: 'Internal task model' }),
      detail: health.taskModelReady
        ? t('admin:overview.checks.configured', { defaultValue: 'Configured' })
        : t('admin:overview.checks.taskModelMissing', { defaultValue: 'Titles, routing, summaries and memory extraction need a task model.' }),
      to: '/admin/settings/model-policy',
    },
    {
      key: 'mail',
      ok: health.emailReady,
      label: t('admin:overview.checks.email', { defaultValue: 'Email verification' }),
      detail: !health.emailVerification
        ? t('admin:overview.checks.notRequired', { defaultValue: 'Not required' })
        : health.smtpReady
          ? t('admin:overview.checks.smtpReady', { defaultValue: 'SMTP is configured' })
          : t('admin:overview.checks.smtpMissing', { defaultValue: 'Verification is enabled but SMTP is incomplete.' }),
      to: health.smtpReady ? '/admin/settings/registration' : '/admin/settings/email',
    },
    {
      key: 'storage',
      ok: health.storageReady,
      label: t('admin:overview.checks.storage', { defaultValue: 'Persistent storage' }),
      detail: !health.storageProvider
        ? t('admin:overview.checks.storageMissing', {
            defaultValue: 'Archived workspaces and application objects are not persisted.',
          })
        : health.storageProvider === 'local'
          ? t('admin:overview.checks.storageLocal', {
              defaultValue: 'Local storage selected; workspace persistence still requires the sandbox volume to be mounted.',
            })
          : health.storageReady
            ? t('admin:overview.checks.storageReady', {
                defaultValue: 'Provider: {{provider}}',
                provider: health.storageProvider,
              })
            : t('admin:overview.checks.storageIncomplete', {
                defaultValue: '{{provider}} is selected but required storage fields are incomplete.',
                provider: health.storageProvider,
              }),
      to: '/admin/storage',
    },
    {
      key: 'payments',
      ok: health.paymentsReady,
      label: t('admin:overview.checks.payments', { defaultValue: 'Payment checkout' }),
      detail: data.paymentChannelCount === 0
        ? t('admin:overview.checks.paymentsUnused', { defaultValue: 'No payment channel configured' })
        : data.paymentMethodCount > 0
          ? t('admin:overview.checks.paymentMethodsReady', { defaultValue: '{{count}} payment method(s) available', count: data.paymentMethodCount })
          : t('admin:overview.checks.paymentMethodMissing', { defaultValue: 'A channel exists but no user-facing payment method is bound to it.' }),
      to: data.paymentChannelCount === 0 ? '/admin/payment-channels' : '/admin/payment-methods',
    },
  ]

  const summary = [
    {
      key: 'users',
      icon: Users,
      label: t('admin:overview.metrics.users', { defaultValue: 'Users' }),
      value: data.userCount,
      to: '/admin/users',
    },
    {
      key: 'models',
      icon: Cpu,
      label: t('admin:overview.metrics.models', { defaultValue: 'Models' }),
      value: data.modelCount,
      to: '/admin/models',
    },
    {
      key: 'channels',
      icon: Network,
      label: t('admin:overview.metrics.channels', { defaultValue: 'Channels' }),
      value: data.channelCount,
      to: '/admin/channels',
    },
    {
      key: 'groups',
      icon: CircleDollarSign,
      label: t('admin:overview.metrics.plans', { defaultValue: 'Plans' }),
      value: data.groupCount,
      to: '/admin/user-groups',
    },
  ]

  const todaySummary = data.today
    ? [
        {
          key: 'turns',
          icon: MessageSquareText,
          label: t('admin:analytics.stats.turns', { defaultValue: 'Delivered turns' }),
          value: numberFormat.format(data.today.turns),
        },
        {
          key: 'users',
          icon: Users,
          label: t('admin:analytics.stats.users', { defaultValue: 'Active users' }),
          value: numberFormat.format(data.today.users),
        },
        {
          key: 'tokens',
          icon: FileText,
          label: t('admin:analytics.stats.tokens', { defaultValue: 'Input + output tokens' }),
          value: compactNumberFormat.format(inputOutputTokens(data.today)),
        },
        {
          key: 'images',
          icon: Image,
          label: t('admin:analytics.details.images', { defaultValue: 'Images' }),
          value: numberFormat.format(data.today.images_count),
        },
      ]
    : null

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:overview.title', { defaultValue: 'Admin overview' })}
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:overview.lead', { defaultValue: 'Configuration health and the main resources managed by this deployment.' })}
        </p>
      </header>

      <div className="mt-8 grid grid-cols-2 border-y border-[var(--color-divider)] lg:grid-cols-4">
        {summary.map((item) => (
          <Link
            key={item.key}
            to={item.to}
            className="group flex min-w-0 items-center gap-3 border-[var(--color-divider)] px-4 py-4 interactive even:border-l hover:bg-[var(--color-bg-muted)]/55 focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] lg:border-l lg:first:border-l-0"
          >
            <item.icon
              size={17}
              className="shrink-0 text-[var(--color-fg-subtle)] group-hover:text-[var(--color-fg-muted)]"
              aria-hidden
            />
            <div className="min-w-0">
              <span className="block text-lg font-semibold tabular-nums text-[var(--color-fg)]">
                {numberFormat.format(item.value)}
              </span>
              <span className="block truncate text-[12px] text-[var(--color-fg-subtle)]">{item.label}</span>
            </div>
          </Link>
        ))}
      </div>

      {health.allReady ? (
        <section className="mt-9">
          <div className="flex items-end justify-between gap-4">
            <div>
              <h2 className="text-base font-semibold text-[var(--color-fg)]">
                {t('admin:overview.todayTitle', { defaultValue: "Today's statistics" })}
              </h2>
              <p className="mt-1 text-[13px] text-[var(--color-fg-muted)]">
                {t('admin:overview.todayLead', { defaultValue: 'Usage recorded over the past 24 hours.' })}
              </p>
            </div>
            <Button asChild variant="ghost" size="sm">
              <Link to="/admin/analytics">
                {t('admin:overview.openAnalytics', { defaultValue: 'Open analytics' })}
                <ArrowRight size={14} aria-hidden />
              </Link>
            </Button>
          </div>

          {todaySummary ? (
            <div className="mt-4 grid grid-cols-2 border-y border-[var(--color-divider)] lg:grid-cols-4">
              {todaySummary.map((item) => (
                <Link
                  key={item.key}
                  to="/admin/analytics"
                  className="group flex min-w-0 items-center gap-3 border-[var(--color-divider)] px-4 py-4 interactive even:border-l hover:bg-[var(--color-bg-muted)]/55 focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] lg:border-l lg:first:border-l-0"
                >
                  <item.icon
                    size={17}
                    className="shrink-0 text-[var(--color-fg-subtle)] group-hover:text-[var(--color-fg-muted)]"
                    aria-hidden
                  />
                  <div className="min-w-0">
                    <span className="block text-lg font-semibold tabular-nums text-[var(--color-fg)]">{item.value}</span>
                    <span className="block truncate text-[12px] text-[var(--color-fg-subtle)]">{item.label}</span>
                  </div>
                </Link>
              ))}
            </div>
          ) : (
            <div className="mt-4 border-y border-[var(--color-divider)] px-2 py-5 text-[13px] text-[var(--color-fg-muted)]" role="status">
              {t('admin:overview.todayLoadFailed', { defaultValue: "Today's statistics could not be loaded. Refresh to try again." })}
            </div>
          )}
        </section>
      ) : (
      <section className="mt-9">
        <div className="flex items-end justify-between gap-4">
          <div>
            <h2 className="text-base font-semibold text-[var(--color-fg)]">
              {t('admin:overview.healthTitle', { defaultValue: 'Configuration health' })}
            </h2>
            <p className="mt-1 text-[13px] text-[var(--color-fg-muted)]">
              {t('admin:overview.healthLead', { defaultValue: 'Resolve warnings before enabling the related feature for users.' })}
            </p>
          </div>
          <Button variant="ghost" size="sm" onClick={() => void load()}>
            {t('common:actions.refresh', { defaultValue: 'Refresh' })}
          </Button>
        </div>

        <ul className="mt-4 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
          {checks.map((check) => (
            <li key={check.key}>
              <Link to={check.to} className="group flex min-h-16 items-center gap-3 px-2 py-3 interactive hover:bg-[var(--color-bg-muted)]/55">
                {check.ok ? (
                  <CheckCircle2 size={17} className="shrink-0 text-[var(--color-success)]" aria-hidden />
                ) : (
                  <AlertTriangle size={17} className="shrink-0 text-[var(--color-warning)]" aria-hidden />
                )}
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-medium text-[var(--color-fg)]">{check.label}</p>
                  <p className="mt-0.5 text-[12px] leading-4 text-[var(--color-fg-subtle)]">{check.detail}</p>
                </div>
                <ArrowRight size={14} className="shrink-0 text-[var(--color-fg-faint)] group-hover:text-[var(--color-fg-muted)]" aria-hidden />
              </Link>
            </li>
          ))}
        </ul>
      </section>
      )}
    </div>
  )
}
