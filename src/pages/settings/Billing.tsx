import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Sparkles, CreditCard } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/tooltip'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { authApi } from '@/api'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

const TIERS = [
  { id: 'free', price: '$0', features: 3, highlight: false },
  { id: 'pro', price: '$20', features: 5, highlight: true },
  { id: 'max', price: '$60', features: 5, highlight: false },
] as const

type Plan = (typeof TIERS)[number]['id']

export default function Billing() {
  const { t } = useTranslation(['settings', 'common'])
  const [usage, setUsage] = useState({ cost: 0, messages: 0, days: 30 })
  const currentPlan: Plan = 'free'

  useEffect(() => {
    authApi
      .usage()
      .then(setUsage)
      .catch(() => undefined)
  }, [])

  const planLabel = (plan: Plan) =>
    t(`settings:billing.tiers.${plan}.name`, { defaultValue: plan })

  return (
    <div className="max-w-[56rem]">
      <header className="mb-8">
        <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)]">{t('settings:billing.title')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">{t('settings:billing.subtitle')}</p>
      </header>

      <SettingsSection title={t('settings:billing.current')}>
        <SettingsRow
          label={planLabel(currentPlan)}
          description={
            currentPlan === 'free'
              ? t('settings:billing.currentFree')
              : t('settings:billing.currentPaid')
          }
        >
          <Badge variant="accent" size="sm" leadingIcon={<Sparkles size={11} aria-hidden />}>
            {currentPlan.toUpperCase()}
          </Badge>
        </SettingsRow>
        <SettingsRow label="Recent usage" description={`Last ${usage.days} days · ${usage.messages} messages`}>
          <span className="font-mono text-sm text-[var(--color-fg)]">${usage.cost.toFixed(4)}</span>
        </SettingsRow>
        <SettingsRow label={t('settings:billing.payment')} description={t('settings:billing.paymentBody')}>
          <Button
            variant="secondary"
            leadingIcon={<CreditCard size={13} aria-hidden />}
            onClick={() => toast.info(t('settings:billing.paymentMocked'))}
          >
            {t('common:actions.updateCard')}
          </Button>
        </SettingsRow>
        <SettingsRow label={t('settings:billing.invoices')} description={t('settings:billing.invoicesBody')}>
          <Button variant="ghost" onClick={() => toast.info(t('settings:billing.invoicesMocked'))}>
            {t('common:actions.viewInvoices')}
          </Button>
        </SettingsRow>
      </SettingsSection>

      <section className="mt-8">
        <h2 className="font-serif tracking-tight text-xl text-[var(--color-fg)] mb-4">
          {t('settings:billing.switchPlan')}
        </h2>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          {TIERS.map((tier) => {
            const features = t(`settings:billing.tiers.${tier.id}.features`, {
              returnObjects: true,
              defaultValue: [],
            }) as string[]
            const name = t(`settings:billing.tiers.${tier.id}.name`)
            const blurb = t(`settings:billing.tiers.${tier.id}.blurb`)
            return (
              <div
                key={tier.id}
                className={cn(
                  'rounded-2xl border bg-[var(--color-surface)] p-6 flex flex-col',
                  tier.highlight
                    ? 'border-[var(--color-accent)] shadow-[0_0_0_4px_var(--color-accent-soft)]'
                    : 'border-[var(--color-border)]',
                )}
              >
                <div className="flex items-center gap-2">
                  <h3 className="font-serif text-xl tracking-tight text-[var(--color-fg)]">{name}</h3>
                  {tier.highlight && (
                    <Badge variant="accent" size="xs">
                      {t('common:common.mostPopular')}
                    </Badge>
                  )}
                </div>
                <p className="text-sm text-[var(--color-fg-muted)] mt-1.5">{blurb}</p>
                <div className="mt-5 flex items-baseline gap-1">
                  <span className="font-serif text-3xl text-[var(--color-fg)] tracking-tight">{tier.price}</span>
                  <span className="text-xs text-[var(--color-fg-muted)]">{t('common:common.perMonth')}</span>
                </div>
                <ul className="mt-5 space-y-2 text-sm flex-1">
                  {features.map((f) => (
                    <li key={f} className="flex items-start gap-2 text-[var(--color-fg)]">
                      <Check size={13} className="mt-1 text-[var(--color-success)]" aria-hidden />
                      {f}
                    </li>
                  ))}
                </ul>
                <Tooltip content={t('common:common.mockNotice')}>
                  <Button
                    className="mt-6"
                    variant={currentPlan === tier.id ? 'secondary' : 'primary'}
                    disabled={currentPlan === tier.id}
                    onClick={() => toast.info(t('settings:billing.switchMocked', { name }))}
                  >
                    {currentPlan === tier.id
                      ? t('common:common.current')
                      : t('settings:billing.switchTo', { name })}
                  </Button>
                </Tooltip>
              </div>
            )
          })}
        </div>
      </section>
    </div>
  )
}
