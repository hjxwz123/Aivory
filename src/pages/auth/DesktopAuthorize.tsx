import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Monitor, Check } from 'lucide-react'
import { api } from '@/api'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/store/auth'
import { clearDesktopAuthorization } from '@/lib/desktop'

export default function DesktopAuthorize() {
  const { t } = useTranslation('auth')
  const [params] = useSearchParams()
  const id = params.get('request_id') || ''
  const user = useAuth((s) => s.user)
  const authPolicy = useAuth((s) => s.authPolicy)
  const needsPassword = user?.has_password === false && (user.oauth_initial_password_policy ?? authPolicy.oauth_initial_password_policy) === 'required'
  const [status, setStatus] = useState<'loading' | 'ready' | 'approved' | 'denied' | 'expired' | 'failed'>('loading')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let active = true
    setStatus('loading')
    if (needsPassword) return () => { active = false }
    void api<{ expires_at: number }>(`/auth/desktop/authorize?request_id=${encodeURIComponent(id)}`)
      .then(({ expires_at }) => {
        if (!active) return
        if (expires_at * 1000 <= Date.now()) { clearDesktopAuthorization(); setStatus('expired'); return }
        setStatus('ready')
      })
      .catch(() => { if (active) { clearDesktopAuthorization(); setStatus('expired') } })
    return () => { active = false }
  }, [id, needsPassword])

  async function respond(approve: boolean) {
    setBusy(true)
    try {
      await api('/auth/desktop/authorize', { method: 'POST', body: { request_id: id, approve } })
      clearDesktopAuthorization()
      setStatus(approve ? 'approved' : 'denied')
    } catch {
      setStatus('failed')
    } finally { setBusy(false) }
  }

  const complete = status === 'approved' || status === 'denied'
  return (
    <div className="login-content login-authorization">
      {status === 'approved' ? <Check className="login-twofa-icon" size={28} aria-hidden /> : <Monitor className="login-twofa-icon" size={28} aria-hidden />}
      <h1 id="login-title" className="login-title">{t(complete ? `desktop.${status}Title` : 'desktop.title')}</h1>
      <p className="login-intro">{t(complete ? `desktop.${status}Description` : 'desktop.description')}</p>
      {status === 'loading' ? <p role="status" className="text-sm text-[var(--color-fg-muted)]">{t('desktop.loading')}</p> : null}
      {status === 'ready' || status === 'failed' ? (
        <>
          <div className="my-5 rounded-lg bg-[var(--color-bg-muted)] p-4">
            <p className="text-xs text-[var(--color-fg-muted)]">{t('desktop.account')}</p>
            <p className="mt-1 break-words text-sm font-medium">{user?.email}</p>
          </div>
          {status === 'failed' ? <p className="login-error" role="alert">{t('desktop.failed')}</p> : null}
          <Button onClick={() => void respond(true)} loading={busy} className="login-submit">{t('desktop.approve')}</Button>
          <Button variant="ghost" onClick={() => void respond(false)} disabled={busy} className="mt-2 w-full">{t('desktop.deny')}</Button>
        </>
      ) : null}
      {status === 'expired' ? <p className="login-error" role="alert">{t('desktop.expired')}</p> : null}
      {complete || status === 'expired' ? <Link to="/" className="login-back">{t('desktop.back')}</Link> : null}
    </div>
  )
}
