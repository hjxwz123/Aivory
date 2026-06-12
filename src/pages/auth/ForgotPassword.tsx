import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { Mail, ArrowLeft, Check } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'

export default function ForgotPassword() {
  const { t } = useTranslation('auth')
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | undefined>()

  function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      setError(t('errors.invalidEmail'))
      return
    }
    setError(undefined)
    setLoading(true)
    setTimeout(() => {
      setLoading(false)
      setSent(true)
    }, 600)
  }

  if (sent) {
    return (
      <div className="text-center">
        <div className="mx-auto inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-success-soft)] text-[var(--color-success)] mb-5">
          <Check size={18} aria-hidden />
        </div>
        <h1 className="font-serif tracking-tight text-2xl text-[var(--color-fg)]">{t('forgot.checkInbox')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)] leading-relaxed">
          <Trans
            i18nKey="forgot.checkInboxBody"
            t={t}
            values={{ email }}
            components={{ strong: <span className="text-[var(--color-fg)] font-medium" /> }}
          />
        </p>
        <Link
          to="/login"
          className="mt-7 inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
        >
          <ArrowLeft size={13} aria-hidden /> {t('forgot.back')}
        </Link>
      </div>
    )
  }

  return (
    <div>
      <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance">
        {t('forgot.title')}
      </h1>
      <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
        {t('forgot.subtitle')}
      </p>

      <form onSubmit={submit} className="mt-7 flex flex-col gap-4">
        <Field label={t('fields.email')} htmlFor="email" error={error}>
          <Input
            id="email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@example.com"
            leadingIcon={<Mail size={14} aria-hidden />}
            autoComplete="email"
            invalid={!!error}
          />
        </Field>
        <Button type="submit" size="lg" loading={loading}>
          {t('forgot.submit')}
        </Button>
      </form>

      <Link
        to="/login"
        className="mt-7 inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
      >
        <ArrowLeft size={13} aria-hidden /> {t('forgot.back')}
      </Link>
    </div>
  )
}
