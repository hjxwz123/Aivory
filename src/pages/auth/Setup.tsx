import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Mail, Lock, User, ArrowRight, ShieldCheck } from 'lucide-react'
import { BlurText } from '@/components/landing/fx/blur-text'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { useAuth } from '@/store/auth'
import { authErrorText } from '@/lib/auth-errors'

/**
 * Setup — first-run screen for a fresh deployment with no accounts yet. The
 * details entered here create the very first user, which becomes the admin
 * (§ first-run setup). AuthGate routes every path here until it's done.
 */
export default function Setup() {
  const navigate = useNavigate()
  const { t } = useTranslation('auth')
  const setup = useAuth((s) => s.setup)

  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ name?: string; email?: string; pw?: string; general?: string }>({})

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const next: typeof errors = {}
    if (!name.trim()) next.name = t('errors.required')
    if (!email) next.email = t('errors.required')
    else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) next.email = t('errors.invalidEmail')
    if (!pw) next.pw = t('errors.required')
    else if (pw.length < 8) next.pw = t('errors.minPassword')
    setErrors(next)
    if (Object.keys(next).length) return
    setLoading(true)
    const ok = await setup(name.trim(), email, pw)
    setLoading(false)
    if (!ok) {
      setErrors({ general: authErrorText(t, useAuth.getState().error, t('errors.required')) })
      return
    }
    navigate('/', { replace: true })
  }

  return (
    <div>
      <div className="mx-auto mb-5 inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
        <ShieldCheck size={20} aria-hidden />
      </div>
      <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance">
        <BlurText text={t('setup.title')} delay={110} />
      </h1>
      <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">{t('setup.subtitle')}</p>

      <form className="mt-7 flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
        {errors.general ? (
          <div className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] px-3 py-2 text-sm text-[var(--color-danger)]">
            {errors.general}
          </div>
        ) : null}
        <Field label={t('register.name')} htmlFor="setup-name" error={errors.name}>
          <Input
            id="setup-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('register.namePlaceholder')}
            leadingIcon={<User size={14} aria-hidden />}
            autoComplete="name"
            invalid={!!errors.name}
          />
        </Field>
        <Field label={t('fields.email')} htmlFor="setup-email" error={errors.email}>
          <Input
            id="setup-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@example.com"
            leadingIcon={<Mail size={14} aria-hidden />}
            autoComplete="email"
            invalid={!!errors.email}
          />
        </Field>
        <Field label={t('fields.password')} htmlFor="setup-pw" hint={t('fields.passwordHint')} error={errors.pw}>
          <Input
            id="setup-pw"
            type="password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            leadingIcon={<Lock size={14} aria-hidden />}
            autoComplete="new-password"
            invalid={!!errors.pw}
          />
        </Field>
        <Button type="submit" size="lg" loading={loading} trailingIcon={<ArrowRight size={15} aria-hidden />} className="w-full">
          {t('setup.submit')}
        </Button>
      </form>
    </div>
  )
}
