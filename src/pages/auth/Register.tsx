import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { Mail, Lock, User, ArrowRight } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'

export default function Register() {
  const navigate = useNavigate()
  const { t } = useTranslation('auth')
  const register = useAuth((s) => s.register)
  const signupOpen = useAuth((s) => s.signupOpen)
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [agree, setAgree] = useState(false)
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ name?: string; email?: string; pw?: string; agree?: string; general?: string }>({})

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const next: typeof errors = {}
    if (!name.trim()) next.name = t('errors.required')
    if (!email) next.email = t('errors.required')
    else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) next.email = t('errors.invalidEmail')
    if (!pw) next.pw = t('errors.required')
    else if (pw.length < 8) next.pw = t('errors.minPassword')
    if (!agree) next.agree = t('errors.acceptTerms')
    setErrors(next)
    if (Object.keys(next).length) return
    setLoading(true)
    const ok = await register(email, pw, name.trim())
    setLoading(false)
    if (!ok) {
      const err = useAuth.getState().error
      setErrors({ general: err ?? t('errors.required') })
      return
    }
    toast.success(t('register.welcome'), t('register.welcomeBody'))
    navigate('/')
  }

  return (
    <div>
      <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance">
        {t('register.title')}
      </h1>
      <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
        {t('register.subtitle')}
      </p>

      <form className="mt-7 flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
        {!signupOpen ? (
          <div className="rounded-[10px] border border-[var(--color-warning-soft)] bg-[var(--color-warning-soft)] text-[var(--color-warning)] px-3 py-2 text-sm">
            {t('register.signupClosed', { defaultValue: 'New signups are currently disabled.' })}
          </div>
        ) : null}
        {errors.general ? (
          <div className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3 py-2 text-sm">
            {errors.general}
          </div>
        ) : null}
        <Field label={t('register.name')} htmlFor="name" error={errors.name}>
          <Input
            id="name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('register.namePlaceholder')}
            leadingIcon={<User size={14} aria-hidden />}
            autoComplete="name"
            invalid={!!errors.name}
          />
        </Field>
        <Field label={t('fields.email')} htmlFor="email" error={errors.email}>
          <Input
            id="email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@example.com"
            leadingIcon={<Mail size={14} aria-hidden />}
            autoComplete="email"
            invalid={!!errors.email}
          />
        </Field>
        <Field label={t('fields.password')} htmlFor="pw" hint={t('fields.passwordHint')} error={errors.pw}>
          <Input
            id="pw"
            type="password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            leadingIcon={<Lock size={14} aria-hidden />}
            autoComplete="new-password"
            invalid={!!errors.pw}
          />
        </Field>
        <label className="flex items-start gap-3 mt-1 cursor-pointer select-none">
          <Switch
            checked={agree}
            onCheckedChange={(v) => setAgree(Boolean(v))}
            aria-invalid={!!errors.agree}
          />
          <span className="text-xs text-[var(--color-fg-muted)] leading-snug">
            <Trans
              i18nKey="register.agree"
              t={t}
              components={{
                terms: <Link to="#" className="text-[var(--color-accent)] hover:underline" />,
                privacy: <Link to="#" className="text-[var(--color-accent)] hover:underline" />,
              }}
              values={{ terms: t('register.terms'), privacy: t('register.privacy') }}
            />
            {errors.agree && <span className="block text-[var(--color-danger)] mt-1">{errors.agree}</span>}
          </span>
        </label>
        <Button type="submit" size="lg" loading={loading} trailingIcon={<ArrowRight size={15} aria-hidden />}>
          {t('register.submit')}
        </Button>
      </form>

      <p className="mt-7 text-center text-sm text-[var(--color-fg-muted)]">
        {t('register.haveAccount')}{' '}
        <Link to="/login" className="text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] font-medium">
          {t('register.haveAccountAction')}
        </Link>
      </p>
    </div>
  )
}
