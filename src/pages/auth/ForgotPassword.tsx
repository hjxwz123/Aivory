import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Trans, useTranslation } from 'react-i18next'
import { motion } from 'framer-motion'
import { Mail, ArrowLeft, Check, ShieldCheck, Lock } from 'lucide-react'
import { BlurText } from '@/components/landing/fx/blur-text'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { toast } from '@/hooks/use-toast'
import { authApi, ApiError } from '@/api'
import { authErrorText } from '@/lib/auth-errors'
import { emailRetryAfterFromBody, useEmailCooldown } from '@/hooks/use-email-cooldown'

const ease: [number, number, number, number] = [0.2, 0.8, 0.2, 1]
const stagger = { hidden: {}, visible: { transition: { staggerChildren: 0.06, delayChildren: 0.04 } } }
const fadeUp = {
  hidden: { opacity: 0, y: 14 },
  visible: { opacity: 1, y: 0, transition: { duration: 0.45, ease } },
}

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

  // Step 3: success
  if (step === 'done') {
    return (
      <motion.div initial="hidden" animate="visible" variants={stagger} className="text-center">
        <motion.div
          variants={fadeUp}
          className="mx-auto inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-success-soft)] text-[var(--color-success)] mb-5"
        >
          <Check size={18} aria-hidden />
        </motion.div>
        <motion.h1
          variants={fadeUp}
          className="font-serif tracking-tight text-2xl text-[var(--color-fg)]"
        >
          {t('forgot.resetSuccess')}
        </motion.h1>
        <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)] leading-relaxed">
          {t('forgot.resetSuccessBody')}
        </motion.p>
        <motion.div variants={fadeUp} className="mt-7">
          <Button size="lg" className="w-full" onClick={() => navigate('/login')}>
            {t('forgot.back')}
          </Button>
        </motion.div>
      </motion.div>
    )
  }

  // Step 2: enter code + new password
  if (step === 'code') {
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
          {t('forgot.resetTitle')}
        </motion.h1>
        <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
          <Trans
            i18nKey="forgot.resetSubtitle"
            t={t}
            values={{ email }}
            components={{ strong: <span className="text-[var(--color-fg)] font-medium" /> }}
          />
        </motion.p>

        <motion.form variants={stagger} className="mt-7 flex flex-col gap-4" onSubmit={(e) => void submitCode(e)}>
          {error ? (
            <motion.div
              variants={fadeUp}
              className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3 py-2 text-sm"
            >
              {error}
            </motion.div>
          ) : null}
          <motion.div variants={fadeUp}>
            <Field label={t('forgot.codeLabel')} htmlFor="reset-code">
              <Input
                id="reset-code"
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                placeholder={t('forgot.codePlaceholder')}
                leadingIcon={<ShieldCheck size={14} aria-hidden />}
                autoComplete="one-time-code"
                inputMode="numeric"
                maxLength={6}
                className="tracking-[0.3em] text-lg font-mono"
              />
            </Field>
          </motion.div>
          <motion.div variants={fadeUp}>
            <Field label={t('forgot.newPassword')} htmlFor="new-pw" hint={t('fields.passwordHint')}>
              <Input
                id="new-pw"
                type="password"
                value={newPw}
                onChange={(e) => setNewPw(e.target.value)}
                leadingIcon={<Lock size={14} aria-hidden />}
                autoComplete="new-password"
              />
            </Field>
          </motion.div>
          <motion.div variants={fadeUp}>
            <Button type="submit" size="lg" loading={loading} className="w-full">
              {t('forgot.resetSubmit')}
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
                ? t('forgot.resendCountdown', { seconds: resendCooldown })
                : t('forgot.resendCode')}
            </button>
          </motion.div>
        </motion.form>

        <Link
          to="/login"
          className="mt-7 inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
        >
          <ArrowLeft size={13} aria-hidden /> {t('forgot.back')}
        </Link>
      </motion.div>
    )
  }

  // Step 1: enter email
  return (
    <motion.div initial="hidden" animate="visible" variants={stagger}>
      {/* The title drifts into focus (BlurText) instead of riding the fadeUp
          stagger — one entrance per element (§ welcome fx). */}
      <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)] text-balance">
        <BlurText text={t('forgot.title')} delay={110} />
      </h1>
      <motion.p variants={fadeUp} className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
        {t('forgot.subtitle')}
      </motion.p>

      <motion.form
        variants={stagger}
        className="mt-7 flex flex-col gap-4"
        onSubmit={(e) => void submitEmail(e)}
      >
        {error ? (
          <motion.div
            variants={fadeUp}
            className="rounded-[10px] border border-[var(--color-danger-soft)] bg-[var(--color-danger-soft)] text-[var(--color-danger)] px-3 py-2 text-sm"
          >
            {error}
          </motion.div>
        ) : null}
        <motion.div variants={fadeUp}>
          <Field label={t('fields.email')} htmlFor="email">
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
        </motion.div>
        <motion.div variants={fadeUp}>
          <Button type="submit" size="lg" loading={loading} className="w-full">
            {t('forgot.submit')}
          </Button>
        </motion.div>
      </motion.form>

      <motion.div variants={fadeUp}>
        <Link
          to="/login"
          className="mt-7 inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
        >
          <ArrowLeft size={13} aria-hidden /> {t('forgot.back')}
        </Link>
      </motion.div>
    </motion.div>
  )
}
