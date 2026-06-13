import { useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { motion } from 'framer-motion'
import { Mail, Lock, ArrowRight, Eye, EyeOff } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'

const ease = [0.2, 0.8, 0.2, 1]
const stagger = { hidden: {}, visible: { transition: { staggerChildren: 0.06, delayChildren: 0.04 } } }
const fadeUp = {
  hidden: { opacity: 0, y: 14 },
  visible: { opacity: 1, y: 0, transition: { duration: 0.45, ease } },
}

export default function Login() {
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useTranslation('auth')
  const login = useAuth((s) => s.login)
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ email?: string; pw?: string; general?: string }>({})

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const next: typeof errors = {}
    if (!email) next.email = t('errors.required')
    else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) next.email = t('errors.invalidEmail')
    if (!pw) next.pw = t('errors.required')
    setErrors(next)
    if (Object.keys(next).length) return
    setLoading(true)
    const ok = await login(email, pw)
    setLoading(false)
    if (!ok) {
      const err = useAuth.getState().error
      // If account is pending verification, redirect to register page
      // where the verification code UI will show
      if (useAuth.getState().pendingVerification) {
        navigate('/register')
        return
      }
      setErrors({ general: err ?? t('errors.required') })
      return
    }
    toast.success(t('login.welcome'), t('login.signingIn'))
    const from = (location.state as { from?: string } | null)?.from ?? '/'
    navigate(from, { replace: true })
  }

  return (
    <motion.div initial="hidden" animate="visible" variants={stagger}>
      <motion.h1
        variants={fadeUp}
        className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance"
      >
        {t('login.title')}
      </motion.h1>
      <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
        {t('login.subtitle')}
      </motion.p>

      <motion.div variants={fadeUp} className="mt-7 flex flex-col gap-2">
        <OAuthRow />
      </motion.div>

      <motion.div
        variants={fadeUp}
        className="my-6 flex items-center gap-3 text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)]"
      >
        <Separator className="flex-1" />
        <span>{t('login.or')}</span>
        <Separator className="flex-1" />
      </motion.div>

      <motion.form
        variants={stagger}
        className="flex flex-col gap-4"
        onSubmit={(e) => void submit(e)}
      >
        {errors.general ? (
          <motion.div
            variants={fadeUp}
            className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3 py-2 text-sm"
          >
            {errors.general}
          </motion.div>
        ) : null}
        <motion.div variants={fadeUp}>
          <Field label={t('fields.email')} htmlFor="email" error={errors.email}>
            <Input
              id="email"
              type="email"
              value={email}
              autoComplete="email"
              onChange={(e) => setEmail(e.target.value)}
              leadingIcon={<Mail size={14} aria-hidden />}
              placeholder={t('fields.emailPlaceholder')}
              invalid={!!errors.email}
            />
          </Field>
        </motion.div>
        <motion.div variants={fadeUp}>
          <Field label={t('fields.password')} htmlFor="pw" error={errors.pw}>
            <Input
              id="pw"
              type={showPw ? 'text' : 'password'}
              value={pw}
              autoComplete="current-password"
              onChange={(e) => setPw(e.target.value)}
              leadingIcon={<Lock size={14} aria-hidden />}
              invalid={!!errors.pw}
              trailingSlot={
                <button
                  type="button"
                  onClick={() => setShowPw((s) => !s)}
                  aria-label={showPw ? t('fields.hidePassword') : t('fields.showPassword')}
                  className="inline-flex items-center justify-center size-7 rounded-[6px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]"
                >
                  {showPw ? <EyeOff size={13} aria-hidden /> : <Eye size={13} aria-hidden />}
                </button>
              }
            />
          </Field>
        </motion.div>
        <motion.div variants={fadeUp} className="text-right">
          <Link to="/forgot-password" className="text-xs text-[var(--color-accent)] hover:text-[var(--color-accent-hover)]">
            {t('login.forgot')}
          </Link>
        </motion.div>
        <motion.div variants={fadeUp}>
          <Button type="submit" size="lg" loading={loading} trailingIcon={<ArrowRight size={15} aria-hidden />} className="w-full">
            {t('login.submit')}
          </Button>
        </motion.div>
      </motion.form>

      <motion.p variants={fadeUp} className="mt-7 text-center text-sm text-[var(--color-fg-muted)]">
        {t('login.noAccount')}{' '}
        <Link to="/register" className="text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] font-medium">
          {t('login.noAccountAction')}
        </Link>
      </motion.p>
    </motion.div>
  )
}

function OAuthRow() {
  const { t } = useTranslation('auth')
  function mock(provider: string) {
    toast.info(t('login.providerMocked', { provider }), t('login.providerMockedBody'))
  }
  return (
    <>
      <Button variant="secondary" size="lg" onClick={() => mock('Google')}>
        <GoogleGlyph /> {t('login.google')}
      </Button>
      <Button variant="secondary" size="lg" onClick={() => mock('GitHub')}>
        <GithubGlyph /> {t('login.github')}
      </Button>
      <Button variant="secondary" size="lg" onClick={() => mock('Apple')}>
        <AppleGlyph /> {t('login.apple')}
      </Button>
    </>
  )
}

function GoogleGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden>
      <path
        fill="currentColor"
        d="M21.6 12.2c0-.7-.06-1.36-.18-2H12v3.8h5.4a4.6 4.6 0 0 1-2 3v2.5h3.23c1.9-1.74 2.97-4.3 2.97-7.3Zm-9.6 9.6c2.7 0 4.96-.9 6.62-2.43l-3.23-2.5c-.9.6-2.05.96-3.4.96-2.6 0-4.8-1.76-5.6-4.12H3.07v2.58A9.99 9.99 0 0 0 12 21.8Zm-5.6-9.7a6 6 0 0 1 0-3.8V5.72H3.06a10 10 0 0 0 0 8.56l3.34-2.18Zm5.6-6.5c1.46 0 2.78.5 3.82 1.49l2.86-2.86C16.96 2.97 14.7 2 12 2A9.99 9.99 0 0 0 3.07 7.72l3.34 2.58c.8-2.36 3-4.12 5.6-4.12Z"
      />
    </svg>
  )
}
function GithubGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden>
      <path
        fill="currentColor"
        d="M12 2C6.5 2 2 6.6 2 12.25c0 4.5 2.87 8.33 6.84 9.68.5.1.68-.23.68-.5v-1.7c-2.78.62-3.37-1.36-3.37-1.36-.45-1.18-1.1-1.5-1.1-1.5-.9-.62.07-.6.07-.6 1 .07 1.52 1.05 1.52 1.05.88 1.55 2.32 1.1 2.88.85.09-.66.35-1.1.63-1.36-2.22-.26-4.55-1.14-4.55-5.07 0-1.12.39-2.03 1.03-2.75-.1-.26-.45-1.3.1-2.71 0 0 .84-.27 2.75 1.05a9.42 9.42 0 0 1 5 0c1.91-1.32 2.75-1.05 2.75-1.05.55 1.41.2 2.45.1 2.7.64.73 1.03 1.64 1.03 2.76 0 3.94-2.34 4.8-4.57 5.06.36.32.68.94.68 1.89v2.8c0 .27.18.6.69.5A10.04 10.04 0 0 0 22 12.25C22 6.6 17.52 2 12 2Z"
      />
    </svg>
  )
}
function AppleGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden>
      <path
        fill="currentColor"
        d="M16.4 12.66c0-2.06 1.7-3.05 1.77-3.1-.97-1.4-2.46-1.6-2.99-1.62-1.28-.13-2.5.75-3.14.75-.65 0-1.65-.74-2.73-.72-1.4.02-2.7.81-3.42 2.07-1.46 2.53-.37 6.27 1.05 8.32.69 1 1.51 2.13 2.59 2.1 1.05-.05 1.45-.68 2.71-.68 1.27 0 1.62.68 2.73.66 1.13-.02 1.84-1.02 2.53-2.03.8-1.15 1.12-2.27 1.14-2.32-.02-.01-2.18-.84-2.21-3.34ZM14.34 5.9c.58-.7.97-1.66.86-2.62-.83.03-1.84.55-2.43 1.24-.53.61-1 1.59-.88 2.53.93.07 1.87-.47 2.45-1.15Z"
      />
    </svg>
  )
}
