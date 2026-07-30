import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  AlertTriangle,
  ArrowRight,
  CheckCircle2,
  CircleDollarSign,
  Cpu,
  Network,
  Users,
} from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'

interface OverviewData {
  settings: Record<string, unknown>
  channelCount: number
  modelIds: Set<string>
  modelCount: number
  groupCount: number
  paymentChannelCount: number
  paymentMethodCount: number
  userCount: number
}

interface HealthCheck {
  key: string
  ok: boolean
  label: string
  detail: string
  to: string
}

function readString(settings: Record<string, unknown>, key: string): string {
  const value = settings[key]
  return typeof value === 'string' ? value.trim() : ''
}

export default function AdminOverview() {
  const { t } = useTranslation(['admin', 'common'])
  const [data, setData] = useState<OverviewData | null>(null)
  const [loading, setLoading] = useState(true)

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
      setData({
        settings,
        channelCount: channels.length,
        modelIds: new Set(models.map((model) => model.id)),
        modelCount: models.length,
        groupCount: groups.length,
        paymentChannelCount: paymentChannels.length,
        paymentMethodCount: paymentMethods.length,
        userCount: usersPage.total,
      })
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

  const defaultModel = readString(data.settings, 'default_model_id')
  const taskModel = readString(data.settings, 'task_model_id')
  const emailVerification = data.settings.email_verification_required === true
  const smtpReady = Boolean(readString(data.settings, 'smtp_host') && readString(data.settings, 'smtp_from'))
  const storageProvider = readString(data.settings, 'storage_provider')
  const storageReady =
    storageProvider === 'local' ||
    (storageProvider === 's3' && Boolean(readString(data.settings, 'storage_s3_bucket'))) ||
    (storageProvider === 'aliyun_oss' &&
      Boolean(
        readString(data.settings, 'storage_aliyun_bucket') &&
        readString(data.settings, 'storage_aliyun_endpoint') &&
        readString(data.settings, 'storage_aliyun_access_key_id') &&
        readString(data.settings, 'storage_aliyun_access_key_secret'),
      ))

  const checks: HealthCheck[] = [
    {
      key: 'channel',
      ok: data.channelCount > 0,
      label: t('admin:overview.checks.channels', { defaultValue: 'Upstream channel' }),
      detail: data.channelCount > 0
        ? t('admin:overview.checks.channelsReady', { defaultValue: '{{count}} channel(s) configured', count: data.channelCount })
        : t('admin:overview.checks.channelsMissing', { defaultValue: 'Create a channel before adding usable models.' }),
      to: '/admin/channels',
    },
    {
      key: 'default-model',
      ok: Boolean(defaultModel && data.modelIds.has(defaultModel)),
      label: t('admin:overview.checks.defaultModel', { defaultValue: 'Default chat model' }),
      detail: defaultModel && data.modelIds.has(defaultModel)
        ? t('admin:overview.checks.configured', { defaultValue: 'Configured' })
        : t('admin:overview.checks.defaultModelMissing', { defaultValue: 'Chat cannot start until a valid default model is selected.' }),
      to: '/admin/settings/model-policy',
    },
    {
      key: 'task-model',
      ok: Boolean(taskModel && data.modelIds.has(taskModel)),
      label: t('admin:overview.checks.taskModel', { defaultValue: 'Internal task model' }),
      detail: taskModel && data.modelIds.has(taskModel)
        ? t('admin:overview.checks.configured', { defaultValue: 'Configured' })
        : t('admin:overview.checks.taskModelMissing', { defaultValue: 'Titles, routing, summaries and memory extraction need a task model.' }),
      to: '/admin/settings/model-policy',
    },
    {
      key: 'mail',
      ok: !emailVerification || smtpReady,
      label: t('admin:overview.checks.email', { defaultValue: 'Email verification' }),
      detail: !emailVerification
        ? t('admin:overview.checks.notRequired', { defaultValue: 'Not required' })
        : smtpReady
          ? t('admin:overview.checks.smtpReady', { defaultValue: 'SMTP is configured' })
          : t('admin:overview.checks.smtpMissing', { defaultValue: 'Verification is enabled but SMTP is incomplete.' }),
      to: smtpReady ? '/admin/settings/registration' : '/admin/settings/email',
    },
    {
      key: 'storage',
      ok: storageReady,
      label: t('admin:overview.checks.storage', { defaultValue: 'Persistent storage' }),
      detail: !storageProvider
        ? t('admin:overview.checks.storageMissing', {
            defaultValue: 'Archived workspaces and application objects are not persisted.',
          })
        : storageProvider === 'local'
          ? t('admin:overview.checks.storageLocal', {
              defaultValue: 'Local storage selected; workspace persistence still requires the sandbox volume to be mounted.',
            })
          : storageReady
            ? t('admin:overview.checks.storageReady', {
                defaultValue: 'Provider: {{provider}}',
                provider: storageProvider,
              })
            : t('admin:overview.checks.storageIncomplete', {
                defaultValue: '{{provider}} is selected but required storage fields are incomplete.',
                provider: storageProvider,
              }),
      to: '/admin/storage',
    },
    {
      key: 'payments',
      ok: data.paymentChannelCount === 0 || data.paymentMethodCount > 0,
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

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">
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
                {item.value.toLocaleString()}
              </span>
              <span className="block truncate text-[12px] text-[var(--color-fg-subtle)]">{item.label}</span>
            </div>
          </Link>
        ))}
      </div>

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
    </div>
  )
}
