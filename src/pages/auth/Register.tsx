import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { ShieldCheck } from 'lucide-react'
import { AuthField } from '@/components/auth/auth-field'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'
import { authApi, resetAuthFailureState, setAccessToken, ApiError } from '@/api'
import { useOAuthProviders } from '@/hooks/use-oauth-providers'
import { OAuthButtons } from '@/components/auth/oauth-buttons'
import { PuzzleCaptchaDialog } from '@/components/auth/puzzle-captcha-dialog'
import { authErrorText } from '@/lib/auth-errors'
import { emailRetryAfterFromBody, useEmailCooldown } from '@/hooks/use-email-cooldown'

export default function Register() {
  const navigate = useNavigate()
  const { t } = useTranslation('auth')
  const register = useAuth((s) => s.register)
  const signupOpen = useAuth((s) => s.signupOpen)
  const oauthSignupOpen = useAuth((s) => s.authPolicy.oauth_auto_provision_enabled)
  const captchaRequired = useAuth((s) => s.captchaRequired)
  const pendingVerification = useAuth((s) => s.pendingVerification)
  const pendingVerificationRetryAfter = useAuth((s) => s.pendingVerificationRetryAfter)
  const startEmailVerification = useAuth((s) => s.startEmailVerification)
  const { providers } = useOAuthProviders()
  const [searchParams, setSearchParams] = useSearchParams()

  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [pw, setPw] = useState('')
  const [agree, setAgree] = useState(false)
  const [loading, setLoading] = useState(false)
  const [errors, setErrors] = useState<{ name?: string; email?: string; pw?: string; agree?: string; captcha?: string; general?: string }>({})

  // Slider-puzzle captcha (only when the admin requires it) — solved in a modal
  // (PuzzleCaptchaDialog) that returns a single-use pass token. The register call
  // consumes the token; every retry obtains a fresh one.
  const submittingRef = useRef(false)
  const [captchaOpen, setCaptchaOpen] = useState(false)

  // Verification step state
  const [code, setCode] = useState('')
  const [verifyLoading, setVerifyLoading] = useState(false)
  const [verifyError, setVerifyError] = useState<string | undefined>()
  const [resending, setResending] = useState(false)
  const { remaining: resendCooldown, start: startResendCooldown } = useEmailCooldown(
    pendingVerification ? pendingVerificationRetryAfter : 0,
  )

  useEffect(() => {
    const verifyEmail = searchParams.get('verify_email')?.trim()
    if (!verifyEmail) return
    const retryAfter = Number.parseInt(searchParams.get('retry_after') ?? '0', 10)
    startEmailVerification(verifyEmail, Number.isFinite(retryAfter) ? retryAfter : 0)
    searchParams.delete('verify_email')
    searchParams.delete('retry_after')
    setSearchParams(searchParams, { replace: true })
  }, [searchParams, setSearchParams, startEmailVerification])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    // The form is only rendered while password signup is open, but a stale
    // submit event must not reach the server after the policy changes.
    if (!signupOpen || submittingRef.current || captchaOpen) return
    const next: typeof errors = {}
    if (!name.trim()) next.name = t('errors.required')
    if (!email) next.email = t('errors.required')
    else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) next.email = t('errors.invalidEmail')
    if (!pw) next.pw = t('errors.required')
    else if (pw.length < 8) next.pw = t('errors.minPassword')
    if (!agree) next.agree = t('errors.acceptTerms')
    setErrors(next)
    if (Object.keys(next).length) return
    // Refresh the policy before deciding whether to open the puzzle. A failed
    // probe still falls back to the server-authoritative registration response.
    submittingRef.current = true
    setLoading(true)
    try {
      const policy = await authApi.signupOpen()
      useAuth.setState({ signupOpen: policy.open, captchaRequired: policy.captcha_required })
    } catch { /* Registration also enforces the current policy. */ }
    submittingRef.current = false
    setLoading(false)
    if (!useAuth.getState().signupOpen) return
    if (useAuth.getState().captchaRequired) {
      setCaptchaOpen(true)
      return
    }
    void finishRegister(null)
  }

  async function finishRegister(token: string | null) {
    if (submittingRef.current || !useAuth.getState().signupOpen) return
    submittingRef.current = true
    setLoading(true)
    let result: Awaited<ReturnType<typeof register>>
    try {
      // Pass the fresh token directly; never reuse a consumed token or drop it
      // because this render still holds the previous captcha policy.
      result = await register(email, pw, name.trim(), token ?? undefined)
    } finally {
      submittingRef.current = false
      setLoading(false)
    }
    if (result === 'verify') {
      // verification_required — the store sets pendingVerification, UI will switch
      return
    }
    if (!result) {
      const err = useAuth.getState().error
      // The server may require a puzzle even when the public policy was stale.
      if (err === 'captcha_failed') {
        setErrors({ captcha: t('register.captchaWrong', { defaultValue: '验证失败，请重试' }) })
        useAuth.setState({ captchaRequired: true })
        setCaptchaOpen(true)
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

  // Continue with the fresh single-use token returned by the dialog.
  function onCaptchaSolved(token: string) {
    setErrors({})
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
      setAccessToken(resp.access_token, resp.request_signing_key)
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

  if (pendingVerification) {
    return (
      <div className="login-content">
        <ShieldCheck size={28} className="login-twofa-icon" aria-hidden />
        <h1 id="login-title" className="login-title">{t('register.verifyTitle')}</h1>
        <p className="login-intro">
          <Trans
            i18nKey="register.verifySubtitle"
            t={t}
            values={{ email: pendingVerification }}
            components={{ strong: <strong /> }}
          />
        </p>
        <p className="login-delivery-hint">{t('emailDeliveryHint')}</p>
        <form onSubmit={(e) => void submitCode(e)} noValidate>
          <AuthField
            id="register-code"
            name="code"
            label={t('register.codeLabel')}
            value={code}
            onChange={(e) => {
              const next = e.target.value.replace(/\D/g, '').slice(0, 6)
              setCode(next)
              setVerifyError(undefined)
              if (next.length === 6 && !(e.nativeEvent as InputEvent).isComposing && !verifyLoading) {
                void verifyCode(next)
              }
            }}
            placeholder={t('register.codePlaceholder')}
            autoComplete="one-time-code"
            inputMode="numeric"
            maxLength={6}
            className="login-code"
            required
            error={verifyError}
          />
          <Button type="submit" loading={verifyLoading} className="login-submit">{t('register.verifySubmit')}</Button>
          <button
            type="button"
            onClick={() => void resendCode()}
            disabled={resending || resendCooldown > 0}
            className="login-resend"
          >
            {resendCooldown > 0
              ? t('register.resendCountdown', { seconds: resendCooldown })
              : t('register.resendCode')}
          </button>
        </form>
        <p className="login-switch">
          <span>{t('register.haveAccount')}</span>
          <Link to="/login">{t('register.haveAccountAction')}</Link>
        </p>
      </div>
    )
  }

  const showProviders = providers.length > 0 && oauthSignupOpen

  return (
    <div className="login-content">
      <h1 id="login-title" className="login-title">{t('register.title')}</h1>
      <p className="login-intro">{t('register.subtitle')}</p>
      {showProviders ? (
        <div className="login-providers">
          <OAuthButtons providers={providers} captchaRequired={captchaRequired} />
        </div>
      ) : null}
      {showProviders && signupOpen ? <div className="login-divider">{t('login.emailDivider')}</div> : null}

      {signupOpen ? (
        <form onSubmit={(e) => void submit(e)} noValidate>
          {errors.general || errors.captcha ? (
            <p className="login-error" role="alert">{errors.general || errors.captcha}</p>
          ) : null}
          <AuthField
            id="register-name"
            name="name"
            label={t('register.name')}
            value={name}
            onChange={(e) => { setName(e.target.value); setErrors({}) }}
            placeholder={t('register.namePlaceholder')}
            autoComplete="name"
            required
            error={errors.name}
          />
          <AuthField
            id="register-email"
            name="email"
            type="email"
            label={t('login.emailLabel')}
            value={email}
            onChange={(e) => { setEmail(e.target.value); setErrors({}) }}
            placeholder="you@example.com"
            autoComplete="email"
            inputMode="email"
            autoCapitalize="none"
            spellCheck={false}
            required
            error={errors.email}
          />
          <AuthField
            id="register-pw"
            name="password"
            type="password"
            label={t('fields.password')}
            value={pw}
            onChange={(e) => { setPw(e.target.value); setErrors({}) }}
            autoComplete="new-password"
            placeholder={t('fields.passwordHint')}
            description={t('fields.passwordHint')}
            required
            minLength={8}
            error={errors.pw}
          />
          <div className="login-agreement">
            <div className="login-agreement-label">
              <input
                id="register-agree"
                type="checkbox"
                checked={agree}
                onChange={(e) => { setAgree(e.target.checked); setErrors((current) => ({ ...current, agree: undefined })) }}
                aria-invalid={Boolean(errors.agree) || undefined}
                aria-describedby={errors.agree ? 'register-agree-error' : undefined}
              />
              <label htmlFor="register-agree">
                <Trans
                  i18nKey="register.agree"
                  t={t}
                  components={{
                    terms: <Link to="/terms" target="_blank" rel="noopener noreferrer" />,
                    privacy: <Link to="/privacy" target="_blank" rel="noopener noreferrer" />,
                  }}
                  values={{ terms: t('register.terms'), privacy: t('register.privacy') }}
                />
              </label>
            </div>
            {errors.agree ? <p id="register-agree-error" className="login-error login-field-error" role="alert">{errors.agree}</p> : null}
          </div>
          <Button type="submit" loading={loading} className="login-submit">{t('register.submit')}</Button>
        </form>
      ) : (
        <p className="login-notice" role="status">
          {showProviders ? t('register.passwordSignupClosed') : t('register.signupClosed')}
        </p>
      )}

      <p className="login-switch">
        <span>{t('register.haveAccount')}</span>
        <Link to="/login">{t('register.haveAccountAction')}</Link>
      </p>
      {captchaRequired || captchaOpen ? (
        <PuzzleCaptchaDialog
          open={captchaOpen}
          onOpenChange={setCaptchaOpen}
          purpose="register"
          onSolved={onCaptchaSolved}
        />
      ) : null}
    </div>
  )
}
