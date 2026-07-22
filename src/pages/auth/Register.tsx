import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { motion } from 'framer-motion'
import { Mail, Lock, User, ArrowRight, ShieldCheck, Eye, EyeOff } from 'lucide-react'
import { BlurText } from '@/components/landing/fx/blur-text'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Separator } from '@/components/ui/separator'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { authApi, resetAuthFailureState, setAccessToken, ApiError } from '@/api'
import { useOAuthProviders } from '@/hooks/use-oauth-providers'
import { OAuthButtons } from '@/components/auth/oauth-buttons'
import { PuzzleCaptchaDialog } from '@/components/auth/puzzle-captcha-dialog'
import { authErrorText } from '@/lib/auth-errors'
import { emailRetryAfterFromBody, useEmailCooldown } from '@/hooks/use-email-cooldown'
import { cn } from '@/lib/utils'

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
  const pendingVerificationRetryAfter = useAuth((s) => s.pendingVerificationRetryAfter)
  const { providers } = useOAuthProviders()

  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [agree, setAgree] = useState(false)
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ name?: string; email?: string; pw?: string; agree?: string; captcha?: string; general?: string }>({})

  // Slider-puzzle captcha (only when the admin requires it) — solved in a modal
  // (PuzzleCaptchaDialog) that returns a single-use pass token. The register call
  // consumes the token; on captcha failure we clear it and re-open the dialog.
  const [captchaToken, setCaptchaToken] = useState<string | null>(null)
  const [captchaOpen, setCaptchaOpen] = useState(false)

  // Verification step state
  const [code, setCode] = useState('')
  const [verifyLoading, setVerifyLoading] = useState(false)
  const [verifyError, setVerifyError] = useState<string | undefined>()
  const [resending, setResending] = useState(false)
  const { remaining: resendCooldown, start: startResendCooldown } = useEmailCooldown(
    pendingVerification ? pendingVerificationRetryAfter : 0,
  )

  function submit(e: React.FormEvent) {
    e.preventDefault()
    // Belt-and-braces: the fields/button are already disabled when signups are
    // closed, but a stray Enter-key submit shouldn't reach the server either.
    if (!signupOpen) return
    const next: typeof errors = {}
    if (!name.trim()) next.name = t('errors.required')
    if (!email) next.email = t('errors.required')
    else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) next.email = t('errors.invalidEmail')
    if (!pw) next.pw = t('errors.required')
    else if (pw.length < 8) next.pw = t('errors.minPassword')
    if (!agree) next.agree = t('errors.acceptTerms')
    setErrors(next)
    if (Object.keys(next).length) return
    // Captcha gate: pop the modal first (it hands back a pass token via onSolved,
    // which then continues the registration). No captcha required → go straight on.
    if (captchaRequired && !captchaToken) {
      setCaptchaOpen(true)
      return
    }
    void finishRegister(captchaToken)
  }

  async function finishRegister(token: string | null) {
    setLoading(true)
    const result = await register(email, pw, name.trim(), captchaRequired ? token ?? undefined : undefined)
    setLoading(false)
    if (result === 'verify') {
      // verification_required — the store sets pendingVerification, UI will switch
      return
    }
    if (!result) {
      const err = useAuth.getState().error
      // The pass token is single-use server-side, so any failure invalidates it —
      // clear it so the next attempt re-solves the puzzle.
      setCaptchaToken(null)
      if (err === 'captcha_failed') {
        setErrors({ captcha: t('register.captchaWrong', { defaultValue: '验证失败，请重试' }) })
        if (captchaRequired) setCaptchaOpen(true)
        return
      }
      if (err === 'register_ip_limit') {
        setErrors({ general: t('register.ipLimited') })
        return
      }
      setErrors({ general: authErrorText(t, err, t('errors.required')) })
      return
    }
    toast.success(t('register.welcome'), t('register.welcomeBody'))
    navigate('/')
  }

  // The dialog verified a solution and minted a token → store it and continue.
  function onCaptchaSolved(token: string) {
    setCaptchaToken(token)
    void finishRegister(token)
  }

  function submitCode(e: React.FormEvent) {
    e.preventDefault()
    void verifyCode(code)
  }

  // Takes the code as an argument (not from state) so the auto-submit in
  // onChange can pass the freshly typed value — state hasn't committed yet.
  async function verifyCode(value: string) {
    if (verifyLoading) return
    const verifyEmail = pendingVerification ?? email
    if (!value.trim()) {
      setVerifyError(t('errors.required'))
      return
    }
    setVerifyLoading(true)
    setVerifyError(undefined)
    try {
      const resp = await authApi.verifyEmail(verifyEmail, value.trim())
      resetAuthFailureState()
      setAccessToken(resp.access_token)
      useAuth.getState().setUser(resp.user)
      useAuth.getState().clearPendingVerification()
      toast.success(t('register.welcome'), t('register.welcomeBody'))
      navigate('/')
    } catch (err) {
      setVerifyError(authErrorText(t, err instanceof ApiError ? err.message : null, t('errors.required')))
    } finally {
      setVerifyLoading(false)
    }
  }

  async function resendCode() {
    if (resending || resendCooldown > 0) return
    const verifyEmail = pendingVerification ?? email
    setResending(true)
    try {
      const resp = await authApi.sendCode(verifyEmail, 'verify')
      startResendCooldown(resp.retry_after)
      toast.success(t('register.codeSent'), t('register.codeSentBody'))
    } catch (err) {
      const retryAfter = err instanceof ApiError ? emailRetryAfterFromBody(err.body) : 0
      if (retryAfter > 0) startResendCooldown(retryAfter)
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
                onChange={(e) => {
                  const next = e.target.value.replace(/\D/g, '').slice(0, 6)
                  setCode(next)
                  // Auto-submit on the 6th digit (typed or pasted). Skip
                  // transient IME composition values; button/Enter still work.
                  if (next.length === 6 && !(e.nativeEvent as InputEvent).isComposing && !verifyLoading) {
                    void verifyCode(next)
                  }
                }}
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
              disabled={resending || resendCooldown > 0}
              className="min-w-28 text-xs tabular-nums text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] disabled:opacity-50"
            >
              {resendCooldown > 0
                ? t('register.resendCountdown', { seconds: resendCooldown })
                : t('register.resendCode')}
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
      {/* The title drifts into focus (BlurText) instead of riding the fadeUp
          stagger — one entrance per element (§ welcome fx). */}
      <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance">
        <BlurText text={t('register.title')} delay={110} />
      </h1>
      <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
        {t('register.subtitle')}
      </motion.p>

      {/* A first-time visitor clicking "Continue with …" here would be trying to
          SIGN UP — hide the section once that's closed (an existing user
          signing back IN via OAuth still works fine from the login page). */}
      {providers.length > 0 && signupOpen ? (
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

      <motion.form
        variants={stagger}
        className={`${providers.length > 0 && signupOpen ? '' : 'mt-7 '}flex flex-col gap-4`}
        onSubmit={(e) => void submit(e)}
      >
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
              disabled={!signupOpen}
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
              disabled={!signupOpen}
            />
          </Field>
        </motion.div>
        <motion.div variants={fadeUp}>
          <Field label={t('fields.password')} htmlFor="pw" hint={t('fields.passwordHint')} error={errors.pw}>
            <Input
              id="pw"
              type={showPw ? 'text' : 'password'}
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              leadingIcon={<Lock size={14} aria-hidden />}
              autoComplete="new-password"
              invalid={!!errors.pw}
              disabled={!signupOpen}
              trailingSlot={
                <button
                  type="button"
                  onClick={() => setShowPw((s) => !s)}
                  disabled={!signupOpen}
                  aria-label={showPw ? t('fields.hidePassword') : t('fields.showPassword')}
                  className="inline-flex items-center justify-center size-7 rounded-[6px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:pointer-events-none disabled:opacity-60"
                >
                  {showPw ? <EyeOff size={13} aria-hidden /> : <Eye size={13} aria-hidden />}
                </button>
              }
            />
          </Field>
        </motion.div>
        <motion.label
          variants={fadeUp}
          className={cn(
            'flex items-start gap-3 mt-1 select-none',
            signupOpen ? 'cursor-pointer' : 'cursor-not-allowed opacity-60',
          )}
        >
          <Switch
            checked={agree}
            onCheckedChange={(v) => setAgree(Boolean(v))}
            aria-invalid={!!errors.agree}
            disabled={!signupOpen}
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
          <Button
            type="submit"
            size="lg"
            loading={loading}
            disabled={!signupOpen}
            trailingIcon={<ArrowRight size={15} aria-hidden />}
            className="w-full"
          >
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

      {/* Modal security check — opens on submit when a captcha is required. */}
      {captchaRequired ? (
        <PuzzleCaptchaDialog open={captchaOpen} onOpenChange={setCaptchaOpen} onSolved={onCaptchaSolved} />
      ) : null}
    </motion.div>
  )
}
