import { useEffect, useState } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { motion } from 'framer-motion'
import { Mail, Lock, ArrowRight, Eye, EyeOff, ShieldCheck, ArrowLeft } from 'lucide-react'
import { BlurText } from '@/components/landing/fx/blur-text'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { useOAuthProviders } from '@/hooks/use-oauth-providers'
import { OAuthButtons } from '@/components/auth/oauth-buttons'
import { PuzzleCaptchaDialog } from '@/components/auth/puzzle-captcha-dialog'
import { authErrorText } from '@/lib/auth-errors'

/**
 * Only follow a post-login `from` when it's a root-relative internal path
 * (`/…`). Rejecting the protocol-relative `//evil.com` form (and any absolute
 * URL) blocks an open-redirect via crafted `location.state` (§ auth E2).
 */
function safeRedirect(from: unknown): string {
  return typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') ? from : '/'
}

const ease: [number, number, number, number] = [0.2, 0.8, 0.2, 1]
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
  const banned = useAuth((s) => s.banned)
  const loginTwoFactor = useAuth((s) => s.loginTwoFactor)
  const pendingTwoFactor = useAuth((s) => s.pendingTwoFactor)
  const clearPendingTwoFactor = useAuth((s) => s.clearPendingTwoFactor)
  const startTwoFactor = useAuth((s) => s.startTwoFactor)
  const loginCaptchaRequired = useAuth((s) => s.loginCaptchaRequired)
  const { providers } = useOAuthProviders()
  const [searchParams, setSearchParams] = useSearchParams()
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ email?: string; pw?: string; general?: string }>({})
  const [code, setCode] = useState('')
  const show2fa = Boolean(pendingTwoFactor)

  // Slider-puzzle captcha (only when the admin requires it on sign-in) — same
  // modal + single-use pass token flow as the register form (§ anti
  // credential-stuffing).
  const [captchaToken, setCaptchaToken] = useState<string | null>(null)
  const [captchaOpen, setCaptchaOpen] = useState(false)

  // Surface a failed OAuth round-trip (the callback redirects here with
  // ?oauth_error=…), then strip the param so a refresh doesn't re-toast.
  useEffect(() => {
    const err = searchParams.get('oauth_error')
    if (!err) return
    toast.error(t('login.oauthFailed'), t(`login.oauthErrors.${err}`, { defaultValue: err }))
    searchParams.delete('oauth_error')
    setSearchParams(searchParams, { replace: true })
  }, [searchParams, setSearchParams, t])

  // An OAuth login for a 2FA-enabled account redirects back here with ?twofa=1;
  // the ticket itself rides a short-lived HttpOnly cookie (§A10), so we just flip
  // to the code step with an empty ticket — the backend reads it from the cookie.
  useEffect(() => {
    if (searchParams.get('twofa') !== '1') return
    startTwoFactor('')
    searchParams.delete('twofa')
    setSearchParams(searchParams, { replace: true })
  }, [searchParams, setSearchParams, startTwoFactor])

  function submit(e: React.FormEvent) {
    e.preventDefault()
    const next: typeof errors = {}
    if (!email) next.email = t('errors.required')
    else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) next.email = t('errors.invalidEmail')
    if (!pw) next.pw = t('errors.required')
    setErrors(next)
    if (Object.keys(next).length) return
    // Captcha gate: pop the modal first (it hands back a pass token via
    // onSolved, which then continues sign-in). No captcha required → go
    // straight on.
    if (loginCaptchaRequired && !captchaToken) {
      setCaptchaOpen(true)
      return
    }
    void finishLogin(captchaToken)
  }

  async function finishLogin(token: string | null) {
    setLoading(true)
    const ok = await login(email, pw, loginCaptchaRequired ? token ?? undefined : undefined)
    setLoading(false)
    if (ok === '2fa') {
      // Password accepted; the 2FA code form now takes over.
      setErrors({})
      setCode('')
      return
    }
    if (!ok) {
      const err = useAuth.getState().error
      // The pass token is single-use server-side, so any failure invalidates
      // it — clear it so the next attempt re-solves the puzzle.
      setCaptchaToken(null)
      if (err === 'captcha_failed') {
        setErrors({ general: authErrorText(t, err, t('errors.required')) })
        if (loginCaptchaRequired) setCaptchaOpen(true)
        return
      }
      // Suspended account → the banned banner already explains it; don't also
      // show a generic error.
      if (useAuth.getState().banned) {
        setErrors({})
        return
      }
      // If account is pending verification, redirect to register page
      // where the verification code UI will show
      if (useAuth.getState().pendingVerification) {
        navigate('/register')
        return
      }
      setErrors({ general: authErrorText(t, err, t('errors.required')) })
      return
    }
    toast.success(t('login.welcome'), t('login.signingIn'))
    const from = safeRedirect((location.state as { from?: string } | null)?.from)
    navigate(from, { replace: true })
  }

  // The dialog verified a solution and minted a token → store it and continue.
  function onCaptchaSolved(token: string) {
    setCaptchaToken(token)
    void finishLogin(token)
  }

  async function submitCode(e: React.FormEvent) {
    e.preventDefault()
    if (code.trim().length < 6) {
      setErrors({ general: t('twofa.codeRequired') })
      return
    }
    setLoading(true)
    const ok = await loginTwoFactor(code.trim())
    setLoading(false)
    if (!ok) {
      setErrors({ general: authErrorText(t, useAuth.getState().error, t('twofa.invalid')) })
      return
    }
    toast.success(t('login.welcome'), t('login.signingIn'))
    const from = safeRedirect((location.state as { from?: string } | null)?.from)
    navigate(from, { replace: true })
  }

  function cancelTwoFactor() {
    clearPendingTwoFactor()
    setCode('')
    setErrors({})
  }

  if (show2fa) {
    return (
      <motion.div initial="hidden" animate="visible" variants={stagger}>
        <motion.div
          variants={fadeUp}
          className="inline-flex size-11 items-center justify-center rounded-[12px] bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]"
        >
          <ShieldCheck size={20} aria-hidden />
        </motion.div>
        <motion.h1
          variants={fadeUp}
          className="mt-5 font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance"
        >
          {t('twofa.title')}
        </motion.h1>
        <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
          {t('twofa.subtitle')}
        </motion.p>

        <motion.form variants={stagger} className="mt-7 flex flex-col gap-4" onSubmit={(e) => void submitCode(e)}>
          {errors.general ? (
            <motion.div
              variants={fadeUp}
              className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3 py-2 text-sm"
            >
              {errors.general}
            </motion.div>
          ) : null}
          <motion.div variants={fadeUp}>
            <Field label={t('twofa.codeLabel')} htmlFor="code">
              <Input
                id="code"
                inputMode="numeric"
                autoComplete="one-time-code"
                autoFocus
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                leadingIcon={<ShieldCheck size={14} aria-hidden />}
                placeholder="000000"
                className="tracking-[0.4em] font-mono text-center text-lg"
              />
            </Field>
          </motion.div>
          <motion.div variants={fadeUp}>
            <Button type="submit" size="lg" loading={loading} trailingIcon={<ArrowRight size={15} aria-hidden />} className="w-full">
              {t('twofa.verify')}
            </Button>
          </motion.div>
          <motion.button
            type="button"
            variants={fadeUp}
            onClick={cancelTwoFactor}
            className="inline-flex items-center justify-center gap-1.5 text-xs text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
          >
            <ArrowLeft size={12} aria-hidden />
            {t('twofa.back')}
          </motion.button>
        </motion.form>
      </motion.div>
    )
  }

  return (
    <motion.div initial="hidden" animate="visible" variants={stagger}>
      {/* The title drifts into focus (BlurText) instead of riding the fadeUp
          stagger — one entrance per element (§ welcome fx). */}
      <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance">
        <BlurText text={t('login.title')} delay={110} />
      </h1>
      <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
        {t('login.subtitle')}
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
      ) : (
        <div className="mt-7" />
      )}

      <motion.form
        variants={stagger}
        className="flex flex-col gap-4"
        onSubmit={(e) => void submit(e)}
      >
        {banned ? (
          <motion.div
            variants={fadeUp}
            role="alert"
            className="rounded-[10px] border border-[var(--color-danger)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3.5 py-3 text-sm"
          >
            <div className="font-medium">{t('login.suspended.title')}</div>
            <p className="mt-0.5 text-[13px] text-[var(--color-fg-muted)]">{t('login.suspended.body')}</p>
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

      {/* Modal security check — opens on submit when the admin requires a
          captcha on sign-in. */}
      {loginCaptchaRequired ? (
        <PuzzleCaptchaDialog open={captchaOpen} onOpenChange={setCaptchaOpen} onSolved={onCaptchaSolved} />
      ) : null}
    </motion.div>
  )
}
