import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { motion } from 'framer-motion'
import { Mail, Lock, User, ArrowRight, ShieldCheck, Calculator, RefreshCw } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Separator } from '@/components/ui/separator'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { authApi, setAccessToken, ApiError } from '@/api'
import { cn } from '@/lib/utils'
import { useOAuthProviders } from '@/hooks/use-oauth-providers'
import { OAuthButtons } from '@/components/auth/oauth-buttons'

const ease: [number, number, number, number] = [0.2, 0.8, 0.2, 1]
const stagger = { hidden: {}, visible: { transition: { staggerChildren: 0.06, delayChildren: 0.04 } } }
const fadeUp = {
  hidden: { opacity: 0, y: 14 },
  visible: { opacity: 1, y: 0, transition: { duration: 0.45, ease } },
}

export default function Register() {
  const navigate = useNavigate()
  const { t } = useTranslation('auth')
  const register = useAuth((s) => s.register)
  const signupOpen = useAuth((s) => s.signupOpen)
  const captchaRequired = useAuth((s) => s.captchaRequired)
  const pendingVerification = useAuth((s) => s.pendingVerification)
  const { providers } = useOAuthProviders()

  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [agree, setAgree] = useState(false)
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ name?: string; email?: string; pw?: string; agree?: string; captcha?: string; general?: string }>({})

  // Arithmetic captcha (only when the admin requires it). Single-use server-side,
  // so we fetch a fresh one on mount and after every failed attempt.
  const [captcha, setCaptcha] = useState<{ id: string; question: string } | null>(null)
  const [captchaAnswer, setCaptchaAnswer] = useState('')
  const [captchaLoading, setCaptchaLoading] = useState(false)

  async function loadCaptcha() {
    setCaptchaLoading(true)
    try {
      setCaptcha(await authApi.captcha())
      setCaptchaAnswer('')
    } catch {
      /* leave the previous question in place; the user can retry */
    } finally {
      setCaptchaLoading(false)
    }
  }
  useEffect(() => {
    if (captchaRequired) void loadCaptcha()
  }, [captchaRequired])

  // Verification step state
  const [code, setCode] = useState('')
  const [verifyLoading, setVerifyLoading] = useState(false)
  const [verifyError, setVerifyError] = useState<string | undefined>()
  const [resending, setResending] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const next: typeof errors = {}
    if (!name.trim()) next.name = t('errors.required')
    if (!email) next.email = t('errors.required')
    else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) next.email = t('errors.invalidEmail')
    if (!pw) next.pw = t('errors.required')
    else if (pw.length < 8) next.pw = t('errors.minPassword')
    if (!agree) next.agree = t('errors.acceptTerms')
    if (captchaRequired && !captchaAnswer.trim()) next.captcha = t('errors.required')
    setErrors(next)
    if (Object.keys(next).length) return
    setLoading(true)
    const result = await register(
      email,
      pw,
      name.trim(),
      captchaRequired && captcha ? { id: captcha.id, answer: captchaAnswer.trim() } : undefined,
    )
    setLoading(false)
    if (result === 'verify') {
      // verification_required — the store sets pendingVerification, UI will switch
      return
    }
    if (!result) {
      const err = useAuth.getState().error
      // Map the backend's stable machine codes to friendly copy, and refresh the
      // single-use captcha so the user can immediately retry.
      if (err === 'captcha_failed') {
        void loadCaptcha()
        setErrors({ captcha: t('register.captchaWrong') })
        return
      }
      if (err === 'register_ip_limit') {
        if (captchaRequired) void loadCaptcha()
        setErrors({ general: t('register.ipLimited') })
        return
      }
      if (captchaRequired) void loadCaptcha()
      setErrors({ general: err ?? t('errors.required') })
      return
    }
    toast.success(t('register.welcome'), t('register.welcomeBody'))
    navigate('/')
  }

  async function submitCode(e: React.FormEvent) {
    e.preventDefault()
    const verifyEmail = pendingVerification ?? email
    if (!code.trim()) {
      setVerifyError(t('errors.required'))
      return
    }
    setVerifyLoading(true)
    setVerifyError(undefined)
    try {
      const resp = await authApi.verifyEmail(verifyEmail, code.trim())
      setAccessToken(resp.access_token)
      useAuth.getState().setUser(resp.user)
      useAuth.getState().clearPendingVerification()
      toast.success(t('register.welcome'), t('register.welcomeBody'))
      navigate('/')
    } catch (err) {
      setVerifyError(err instanceof ApiError ? err.message : t('errors.required'))
    } finally {
      setVerifyLoading(false)
    }
  }

  async function resendCode() {
    const verifyEmail = pendingVerification ?? email
    setResending(true)
    try {
      await authApi.sendCode(verifyEmail, 'verify')
      toast.success(t('register.codeSent'), t('register.codeSentBody'))
    } catch {
      // silently fail
    } finally {
      setResending(false)
    }
  }

  // Verification code step
  if (pendingVerification) {
    return (
      <motion.div initial="hidden" animate="visible" variants={stagger}>
        <motion.div
          variants={fadeUp}
          className="mx-auto mb-5 inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]"
        >
          <ShieldCheck size={20} aria-hidden />
        </motion.div>
        <motion.h1
          variants={fadeUp}
          className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance"
        >
          {t('register.verifyTitle')}
        </motion.h1>
        <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
          <Trans
            i18nKey="register.verifySubtitle"
            t={t}
            values={{ email: pendingVerification }}
            components={{ strong: <span className="text-[var(--color-fg)] font-medium" /> }}
          />
        </motion.p>

        <motion.form variants={stagger} className="mt-7 flex flex-col gap-4" onSubmit={(e) => void submitCode(e)}>
          {verifyError ? (
            <motion.div
              variants={fadeUp}
              className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3 py-2 text-sm"
            >
              {verifyError}
            </motion.div>
          ) : null}
          <motion.div variants={fadeUp}>
            <Field label={t('register.codeLabel')} htmlFor="code" error={verifyError}>
              <Input
                id="code"
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                placeholder={t('register.codePlaceholder')}
                leadingIcon={<ShieldCheck size={14} aria-hidden />}
                autoComplete="one-time-code"
                inputMode="numeric"
                maxLength={6}
                className="tracking-[0.3em] text-lg font-mono"
                invalid={!!verifyError}
              />
            </Field>
          </motion.div>
          <motion.div variants={fadeUp}>
            <Button type="submit" size="lg" loading={verifyLoading} className="w-full">
              {t('register.verifySubmit')}
            </Button>
          </motion.div>
          <motion.div variants={fadeUp} className="text-center">
            <button
              type="button"
              onClick={() => void resendCode()}
              disabled={resending}
              className="text-xs text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] disabled:opacity-50"
            >
              {t('register.resendCode')}
            </button>
          </motion.div>
        </motion.form>

        <motion.p variants={fadeUp} className="mt-7 text-center text-sm text-[var(--color-fg-muted)]">
          {t('register.haveAccount')}{' '}
          <Link to="/login" className="text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] font-medium">
            {t('register.haveAccountAction')}
          </Link>
        </motion.p>
      </motion.div>
    )
  }

  return (
    <motion.div initial="hidden" animate="visible" variants={stagger}>
      <motion.h1
        variants={fadeUp}
        className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance"
      >
        {t('register.title')}
      </motion.h1>
      <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
        {t('register.subtitle')}
      </motion.p>

      {providers.length > 0 ? (
        <>
          <motion.div variants={fadeUp} className="mt-7 flex flex-col gap-2">
            <OAuthButtons providers={providers} />
          </motion.div>
          <motion.div
            variants={fadeUp}
            className="my-6 flex items-center gap-3 text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)]"
          >
            <Separator className="flex-1" />
            <span>{t('login.or')}</span>
            <Separator className="flex-1" />
          </motion.div>
        </>
      ) : null}

      <motion.form variants={stagger} className={`${providers.length > 0 ? '' : 'mt-7 '}flex flex-col gap-4`} onSubmit={(e) => void submit(e)}>
        {!signupOpen ? (
          <motion.div
            variants={fadeUp}
            className="rounded-[10px] border border-[var(--color-warning-soft)] bg-[var(--color-warning-soft)] text-[var(--color-warning)] px-3 py-2 text-sm"
          >
            {t('register.signupClosed', { defaultValue: 'New signups are currently disabled.' })}
          </motion.div>
        ) : null}
        {errors.general ? (
          <motion.div
            variants={fadeUp}
            className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3 py-2 text-sm"
          >
            {errors.general}
          </motion.div>
        ) : null}
        <motion.div variants={fadeUp}>
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
        </motion.div>
        <motion.div variants={fadeUp}>
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
        </motion.div>
        <motion.div variants={fadeUp}>
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
        </motion.div>
        {captchaRequired ? (
          <motion.div variants={fadeUp}>
            <Field label={t('register.captchaLabel')} htmlFor="captcha" error={errors.captcha}>
              <div className="flex items-stretch gap-2">
                <div className="inline-flex select-none items-center gap-1.5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 font-mono text-sm text-[var(--color-fg)]">
                  <span aria-hidden className={cn('tabular-nums', captchaLoading && 'opacity-40')}>
                    {captcha?.question ?? '— + —'}
                  </span>
                  <span className="text-[var(--color-fg-subtle)]">=</span>
                </div>
                <Input
                  id="captcha"
                  value={captchaAnswer}
                  onChange={(e) => setCaptchaAnswer(e.target.value.replace(/[^\d-]/g, '').slice(0, 4))}
                  placeholder={t('register.captchaPlaceholder')}
                  leadingIcon={<Calculator size={14} aria-hidden />}
                  inputMode="numeric"
                  autoComplete="off"
                  invalid={!!errors.captcha}
                  className="flex-1"
                />
                <button
                  type="button"
                  onClick={() => void loadCaptcha()}
                  disabled={captchaLoading}
                  aria-label={t('register.captchaRefresh')}
                  className="inline-flex size-10 shrink-0 items-center justify-center rounded-[10px] border border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-50"
                >
                  <RefreshCw size={15} aria-hidden className={cn(captchaLoading && 'animate-[spin_0.8s_linear_infinite]')} />
                </button>
              </div>
            </Field>
          </motion.div>
        ) : null}
        <motion.label variants={fadeUp} className="flex items-start gap-3 mt-1 cursor-pointer select-none">
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
                terms: <Link to="/terms" target="_blank" className="text-[var(--color-accent)] hover:underline" />,
                privacy: <Link to="/privacy" target="_blank" className="text-[var(--color-accent)] hover:underline" />,
              }}
              values={{ terms: t('register.terms'), privacy: t('register.privacy') }}
            />
            {errors.agree && <span className="block text-[var(--color-danger)] mt-1">{errors.agree}</span>}
          </span>
        </motion.label>
        <motion.div variants={fadeUp}>
          <Button type="submit" size="lg" loading={loading} trailingIcon={<ArrowRight size={15} aria-hidden />} className="w-full">
            {t('register.submit')}
          </Button>
        </motion.div>
      </motion.form>

      <motion.p variants={fadeUp} className="mt-7 text-center text-sm text-[var(--color-fg-muted)]">
        {t('register.haveAccount')}{' '}
        <Link to="/login" className="text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] font-medium">
          {t('register.haveAccountAction')}
        </Link>
      </motion.p>
    </motion.div>
  )
}
