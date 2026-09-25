import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { ShieldCheck, ArrowLeft, Fingerprint } from 'lucide-react'
import { AuthField } from '@/components/auth/auth-field'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { OAuthButtons } from '@/components/auth/oauth-buttons'
import { PuzzleCaptchaDialog } from '@/components/auth/puzzle-captcha-dialog'
import { authErrorText } from '@/lib/auth-errors'
import { isPasskeyAvailable } from '@/lib/passkey'
import {
  loadRememberedPassword,
  rememberPasswordPreference,
  setRememberPasswordPreference,
  storeRememberedPassword,
} from '@/lib/password-credentials'

/**
 * Only follow a post-login `from` when it's a root-relative internal path
 * (`/…`). Rejecting the protocol-relative `//evil.com` form (and any absolute
 * URL) blocks an open-redirect via crafted `location.state` (§ auth E2).
 */
function safeRedirect(from: unknown): string {
  return typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') ? from : '/'
}

export default function Login() {
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useTranslation('auth')
  const login = useAuth((s) => s.login)
  const banned = useAuth((s) => s.banned)
  const loginTwoFactor = useAuth((s) => s.loginTwoFactor)
  const loginWithPasskey = useAuth((s) => s.loginWithPasskey)
  const pendingTwoFactor = useAuth((s) => s.pendingTwoFactor)
  const clearPendingTwoFactor = useAuth((s) => s.clearPendingTwoFactor)
  const startTwoFactor = useAuth((s) => s.startTwoFactor)
  const loginCaptchaRequired = useAuth((s) => s.loginCaptchaRequired)
  const registrationCaptchaRequired = useAuth((s) => s.captchaRequired)
  const authPolicy = useAuth((s) => s.authPolicy)
  const authPolicyLoaded = useAuth((s) => s.authPolicyLoaded)
  const providers = authPolicy.providers
  const [searchParams, setSearchParams] = useSearchParams()
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [rememberPassword, setRememberPassword] = useState(rememberPasswordPreference)
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ email?: string; pw?: string; general?: string }>({})
  const [code, setCode] = useState('')
  const [passkeyBusy, setPasskeyBusy] = useState(false)
  const show2fa = Boolean(pendingTwoFactor)
  const showPasswordLogin = authPolicy.entry_mode === 'login_page' && authPolicy.password_login_enabled
  // Admin policy + browser capability gate the passkey button; an absent
  // policy field (older server) means enabled.
  const showPasskey = authPolicy.passkey_login_enabled !== false && isPasskeyAvailable()
  const providerRequired = !showPasswordLogin && !showPasskey

  // Slider-puzzle captcha (only when the admin requires it on sign-in) — same
  // modal + single-use pass token flow as the register form (§ anti
  // credential-stuffing).
  const [captchaToken, setCaptchaToken] = useState<string | null>(null)
  const [captchaOpen, setCaptchaOpen] = useState(false)
  const handledOAuthError = useRef('')

  useEffect(() => {
    let active = true
    void loadRememberedPassword().then((credential) => {
      if (!active || !credential) return
      setEmail((current) => current || credential.email)
      setPw((current) => current || credential.password)
    })
    return () => {
      active = false
    }
  }, [])

  // Surface a failed OAuth round-trip (the callback redirects here with
  // ?oauth_error=…), then strip the param so a refresh doesn't re-toast.
  useEffect(() => {
    const err = searchParams.get('oauth_error')
    if (!err || !authPolicyLoaded || handledOAuthError.current === err) return
    handledOAuthError.current = err
    toast.error(t('login.oauthFailed'), t(`login.oauthErrors.${err}`, { defaultValue: err }))
    // Preserve the failure marker in auto-redirect mode. AuthGate uses it to
    // avoid sending the browser straight back into the same failed provider.
    if (authPolicy.entry_mode === 'auto_redirect') return
    searchParams.delete('oauth_error')
    setSearchParams(searchParams, { replace: true })
  }, [authPolicy.entry_mode, authPolicyLoaded, searchParams, setSearchParams, t])

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
      if (rememberPassword) void storeRememberedPassword(email, pw)
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
    if (rememberPassword) void storeRememberedPassword(email, pw)
    toast.success(t('login.welcome'), t('login.signingIn'))
    const from = safeRedirect((location.state as { from?: string } | null)?.from)
    navigate(from, { replace: true })
  }

  // The dialog verified a solution and minted a token → store it and continue.
  function onCaptchaSolved(token: string) {
    setCaptchaToken(token)
    void finishLogin(token)
  }

  async function passkeyLogin() {
    setPasskeyBusy(true)
    const ok = await loginWithPasskey()
    setPasskeyBusy(false)
    if (!ok) {
      // A dismissed biometric prompt leaves error null — stay quiet.
      const err = useAuth.getState().error
      if (err) setErrors({ general: authErrorText(t, err, t('login.passkeyFailed')) })
      return
    }
    toast.success(t('login.welcome'), t('login.signingIn'))
    const from = safeRedirect((location.state as { from?: string } | null)?.from)
    navigate(from, { replace: true })
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
      <div className="login-content">
        <ShieldCheck className="login-twofa-icon" size={28} aria-hidden />
        <h1 id="login-title" className="login-title">{t('twofa.title')}</h1>
        <p className="login-intro">{t('twofa.subtitle')}</p>
        <form onSubmit={(e) => void submitCode(e)} noValidate>
          {errors.general ? (
            <p id="login-code-error" className="login-error" role="alert">{errors.general}</p>
          ) : null}
          <AuthField
            id="code"
            name="code"
            label={t('twofa.codeLabel')}
            className="login-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus
            required
            maxLength={6}
            value={code}
            onChange={(e) => { setCode(e.target.value.replace(/\D/g, '').slice(0, 6)); setErrors({}) }}
            placeholder="000000"
            aria-invalid={Boolean(errors.general) || undefined}
            aria-describedby={errors.general ? 'login-code-error' : undefined}
          />
          <Button type="submit" loading={loading} className="login-submit">
            {t('twofa.verify')}
          </Button>
          <button type="button" onClick={cancelTwoFactor} className="login-back" disabled={loading}>
            <ArrowLeft size={12} aria-hidden />
            {t('twofa.back')}
          </button>
        </form>
      </div>
    )
  }

  return (
    <div className="login-content">
      <h1 id="login-title" className="login-title">{t('login.title')}</h1>
      <p className="login-intro">{t('login.subtitle')}</p>

      {providers.length > 0 || showPasskey ? (
        <div className="login-providers">
          <OAuthButtons providers={providers} captchaRequired={registrationCaptchaRequired} />
          {showPasskey ? (
            <Button
              type="button"
              variant="secondary"
              loading={passkeyBusy}
              disabled={loading}
              onClick={() => void passkeyLogin()}
              leadingIcon={<Fingerprint size={17} strokeWidth={1.6} aria-hidden />}
            >
              {t('login.passkeyLabel')}
            </Button>
          ) : null}
        </div>
      ) : providerRequired ? (
        <p className="login-notice" role="alert">{t('login.noProviders')}</p>
      ) : null}

      {(providers.length > 0 || showPasskey) && showPasswordLogin ? (
        <div className="login-divider">{t('login.emailDivider')}</div>
      ) : null}

      {banned ? (
        <div className="login-notice login-notice-error" role="alert">
          <strong>{t('login.suspended.title')}</strong>
          <p>{t('login.suspended.body')}</p>
        </div>
      ) : null}
      {errors.general ? (
        <p id="login-error" className="login-error" role="alert">{errors.general}</p>
      ) : null}

      {showPasswordLogin ? (
        <form
          autoComplete={rememberPassword ? 'on' : 'off'}
          onSubmit={(e) => void submit(e)}
          noValidate
        >
          <AuthField
            id="email"
            name="email"
            type="email"
            inputMode="email"
            label={t('login.emailLabel')}
            value={email}
            autoComplete={rememberPassword ? 'email' : 'off'}
            autoCapitalize="none"
            spellCheck={false}
            required
            onChange={(e) => { setEmail(e.target.value); setErrors({}) }}
            placeholder="you@example.com"
            error={errors.email}
          />
          <AuthField
            id="pw"
            name="password"
            type="password"
            label={t('fields.password')}
            headingAction={<Link to="/forgot-password" className="login-forgot">{t('login.forgot')}</Link>}
            value={pw}
            autoComplete={rememberPassword ? 'current-password' : 'off'}
            onChange={(e) => { setPw(e.target.value); setErrors({}) }}
            placeholder={t('login.passwordPlaceholder')}
            required
            error={errors.pw}
          />
          <label className="login-remember">
            <input
              type="checkbox"
              checked={rememberPassword}
              onChange={(event) => {
                const remember = event.target.checked
                setRememberPassword(remember)
                setRememberPasswordPreference(remember)
              }}
            />
            <span>{t('login.rememberPassword')}</span>
          </label>
          <Button type="submit" loading={loading} disabled={passkeyBusy} className="login-submit">
            {t('login.submit')}
          </Button>
        </form>
      ) : null}

      {showPasswordLogin ? (
        <p className="login-switch">
          <span>{t('login.noAccount')}</span>
          <Link to="/register">{t('login.noAccountAction')}</Link>
        </p>
      ) : null}
      <p className="login-terms">
        <Trans
          t={t}
          i18nKey="login.agree"
          components={{
            terms: <Link to="/terms" target="_blank" rel="noopener noreferrer" />,
            privacy: <Link to="/privacy" target="_blank" rel="noopener noreferrer" />,
          }}
        />
      </p>

      {showPasswordLogin && loginCaptchaRequired ? (
        <PuzzleCaptchaDialog
          open={captchaOpen}
          onOpenChange={setCaptchaOpen}
          purpose="login"
          onSolved={onCaptchaSolved}
        />
      ) : null}
    </div>
  )
}
