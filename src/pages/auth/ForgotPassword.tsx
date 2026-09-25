import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { ArrowLeft, Check, ShieldCheck } from 'lucide-react'
import { AuthField } from '@/components/auth/auth-field'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { authApi, ApiError } from '@/api'
import { authErrorText } from '@/lib/auth-errors'
import { emailRetryAfterFromBody, useEmailCooldown } from '@/hooks/use-email-cooldown'

type Step = 'email' | 'code' | 'done'

export default function ForgotPassword() {
  const { t } = useTranslation('auth')
  const navigate = useNavigate()
  const [step, setStep] = useState<Step>('email')
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [newPw, setNewPw] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | undefined>()
  const [resending, setResending] = useState(false)
  const { remaining: resendCooldown, start: startResendCooldown } = useEmailCooldown()

  async function submitEmail(e: React.FormEvent) {
    e.preventDefault()
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      setError(t('errors.invalidEmail'))
      return
    }
    setError(undefined)
    setLoading(true)
    try {
      const resp = await authApi.forgotPassword(email)
      startResendCooldown(resp.retry_after)
    } catch (err) {
      // Always proceed — backend returns 200 to prevent enumeration
      const retryAfter = err instanceof ApiError ? emailRetryAfterFromBody(err.body) : 0
      if (retryAfter > 0) startResendCooldown(retryAfter)
    }
    setLoading(false)
    setStep('code')
  }

  async function submitCode(e: React.FormEvent) {
    e.preventDefault()
    const errs: string[] = []
    if (!code.trim()) errs.push(t('errors.required'))
    if (newPw.length < 8) errs.push(t('errors.minPassword'))
    if (errs.length) {
      setError(errs.join(' '))
      return
    }
    setError(undefined)
    setLoading(true)
    try {
      await authApi.resetPassword(email, code.trim(), newPw)
      setStep('done')
    } catch (err) {
      setError(authErrorText(t, err instanceof ApiError ? err.message : null, t('errors.required')))
    } finally {
      setLoading(false)
    }
  }

  async function resendCode() {
    if (resending || resendCooldown > 0) return
    setResending(true)
    try {
      const resp = await authApi.sendCode(email, 'reset')
      startResendCooldown(resp.retry_after)
      toast.success(t('forgot.codeSent'))
    } catch (err) {
      const retryAfter = err instanceof ApiError ? emailRetryAfterFromBody(err.body) : 0
      if (retryAfter > 0) startResendCooldown(retryAfter)
    } finally {
      setResending(false)
    }
  }

  if (step === 'done') {
    return (
      <div className="login-content">
        <Check size={28} className="login-success-icon" aria-hidden />
        <h1 id="login-title" className="login-title">{t('forgot.resetSuccess')}</h1>
        <p className="login-intro">{t('forgot.resetSuccessBody')}</p>
        <Button className="login-submit" onClick={() => navigate('/login')}>{t('forgot.back')}</Button>
      </div>
    )
  }

  if (step === 'code') {
    return (
      <div className="login-content">
        <ShieldCheck size={28} className="login-twofa-icon" aria-hidden />
        <h1 id="login-title" className="login-title">{t('forgot.resetTitle')}</h1>
        <p className="login-intro">
          <Trans
            i18nKey="forgot.resetSubtitle"
            t={t}
            values={{ email }}
            components={{ strong: <strong /> }}
          />
        </p>
        <p className="login-delivery-hint">{t('emailDeliveryHint')}</p>
        <form onSubmit={(e) => void submitCode(e)} noValidate>
          {error ? <p id="reset-error" className="login-error" role="alert">{error}</p> : null}
          <AuthField
            id="reset-code"
            name="code"
            label={t('forgot.codeLabel')}
            value={code}
            onChange={(e) => { setCode(e.target.value.replace(/\D/g, '').slice(0, 6)); setError(undefined) }}
            placeholder={t('forgot.codePlaceholder')}
            autoComplete="one-time-code"
            inputMode="numeric"
            maxLength={6}
            className="login-code"
            required
            aria-describedby={error ? 'reset-error' : undefined}
          />
          <AuthField
            id="new-pw"
            name="password"
            type="password"
            label={t('forgot.newPassword')}
            value={newPw}
            onChange={(e) => { setNewPw(e.target.value); setError(undefined) }}
            placeholder={t('fields.passwordHint')}
            description={t('fields.passwordHint')}
            autoComplete="new-password"
            required
            minLength={8}
            aria-describedby={error ? 'reset-error' : undefined}
          />
          <Button type="submit" loading={loading} className="login-submit">{t('forgot.resetSubmit')}</Button>
          <button
            type="button"
            onClick={() => void resendCode()}
            disabled={resending || resendCooldown > 0}
            className="login-resend"
          >
            {resendCooldown > 0
              ? t('forgot.resendCountdown', { seconds: resendCooldown })
              : t('forgot.resendCode')}
          </button>
        </form>
        <Link to="/login" className="login-back"><ArrowLeft size={12} aria-hidden />{t('forgot.back')}</Link>
      </div>
    )
  }

  return (
    <div className="login-content">
      <h1 id="login-title" className="login-title">{t('forgot.title')}</h1>
      <p className="login-intro">{t('forgot.subtitle')}</p>
      <form onSubmit={(e) => void submitEmail(e)} noValidate>
        <AuthField
          id="forgot-email"
          name="email"
          type="email"
          label={t('login.emailLabel')}
          value={email}
          onChange={(e) => { setEmail(e.target.value); setError(undefined) }}
          placeholder="you@example.com"
          autoComplete="email"
          inputMode="email"
          autoCapitalize="none"
          spellCheck={false}
          required
          error={error}
        />
        <Button type="submit" loading={loading} className="login-submit">{t('forgot.submit')}</Button>
      </form>
      <Link to="/login" className="login-back"><ArrowLeft size={12} aria-hidden />{t('forgot.back')}</Link>
    </div>
  )
}
