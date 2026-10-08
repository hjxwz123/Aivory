import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Monitor, Check } from 'lucide-react'
import { api, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/store/auth'
import { clearDesktopAuthorization } from '@/lib/desktop'
import { parseClientDevice } from '@/lib/client-device'

type AuthorizationInfo = {
  expires_at: number
  // Older servers and requests started before an upgrade omit these fields.
  client?: { user_agent?: string; ip?: string; location?: string }
}

export default function DesktopAuthorize() {
  const { t } = useTranslation(['auth', 'common'])
  const [params] = useSearchParams()
  const id = params.get('request_id') || ''
  const user = useAuth((s) => s.user)
  const authPolicy = useAuth((s) => s.authPolicy)
  const needsPassword = user?.has_password === false && (user.oauth_initial_password_policy ?? authPolicy.oauth_initial_password_policy) === 'required'
  const [status, setStatus] = useState<'loading' | 'ready' | 'approved' | 'denied' | 'expired' | 'failed'>('loading')
  const [busy, setBusy] = useState(false)
  const [info, setInfo] = useState<AuthorizationInfo | null>(null)

  useEffect(() => {
    let active = true
    setStatus('loading')
    setInfo(null)
    if (needsPassword) return () => { active = false }
    void api<AuthorizationInfo>(`/auth/desktop/authorize?request_id=${encodeURIComponent(id)}`)
      .then((request) => {
        if (!active) return
        if (request.expires_at * 1000 <= Date.now()) { clearDesktopAuthorization(); setStatus('expired'); return }
        setInfo(request)
        setStatus('ready')
      })
      .catch((error: unknown) => {
        if (!active) return
        if (error instanceof ApiError && error.status === 404) { clearDesktopAuthorization(); setStatus('expired') }
        else setStatus('failed')
      })
    return () => { active = false }
  }, [id, needsPassword])

  useEffect(() => {
    if (!info || !['ready', 'failed'].includes(status)) return
    const expire = () => {
      if (info.expires_at * 1000 <= Date.now()) {
        clearDesktopAuthorization()
        setStatus('expired')
      }
    }
    const timer = window.setTimeout(expire, Math.max(0, info.expires_at * 1000 - Date.now()))
    window.addEventListener('focus', expire)
    document.addEventListener('visibilitychange', expire)
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener('focus', expire)
      document.removeEventListener('visibilitychange', expire)
    }
  }, [info, status])

  async function respond(approve: boolean) {
    if (busy) return
    if (info && info.expires_at * 1000 <= Date.now()) {
      clearDesktopAuthorization()
      setStatus('expired')
      return
    }
    setBusy(true)
    try {
      await api('/auth/desktop/authorize', { method: 'POST', body: { request_id: id, approve } })
      clearDesktopAuthorization()
      setStatus(approve ? 'approved' : 'denied')
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) { clearDesktopAuthorization(); setStatus('expired') }
      else setStatus('failed')
    } finally { setBusy(false) }
  }

  const complete = status === 'approved' || status === 'denied'
  const client = info?.client
  const device = client?.user_agent ? parseClientDevice(client.user_agent, t('common:desktopApp')) : null
  const deviceLabel = device ? [device.browser + (device.appVersion ? ` ${device.appVersion}` : ''), device.os].filter(Boolean).join(' · ') : ''
  const network = [client?.ip, client?.location === 'Local network' ? t('desktop.localNetwork') : client?.location].filter(Boolean).join(' · ')
  return (
    <div className="login-content login-authorization">
      {status === 'approved' ? <Check className="login-twofa-icon" size={28} aria-hidden /> : <Monitor className="login-twofa-icon" size={28} aria-hidden />}
      <h1 id="login-title" className="login-title">{t(complete ? `desktop.${status}Title` : 'desktop.title')}</h1>
      <p className="login-intro">{t(complete ? `desktop.${status}Description` : 'desktop.description')}</p>
      {status === 'loading' ? <p role="status" className="text-sm text-[var(--color-fg-muted)]">{t('desktop.loading')}</p> : null}
      {status === 'ready' || status === 'failed' ? (
        <>
          <div className="login-authorization-info my-4 rounded-lg bg-[var(--color-bg-muted)] p-3">
            <p className="text-xs text-[var(--color-fg-muted)]">{t('desktop.account')}</p>
            <p className="mt-1 break-words text-sm font-medium">{user?.email}</p>
            {deviceLabel || network ? (
              <dl className="mt-3 grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] gap-x-3 gap-y-2 text-xs">
                {deviceLabel ? <><dt className="text-[var(--color-fg-muted)]">{t('desktop.device')}</dt><dd className="break-words text-[var(--color-fg)]">{deviceLabel}</dd></> : null}
                {network ? <><dt className="text-[var(--color-fg-muted)]">{t('desktop.network')}</dt><dd className="break-words text-[var(--color-fg)]">{network}</dd></> : null}
              </dl>
            ) : null}
          </div>
          {status === 'failed' ? <p className="login-error" role="alert">{t('desktop.failed')}</p> : null}
          <div className="login-authorization-actions">
            <Button variant="ghost" onClick={() => void respond(false)} disabled={busy}>{t('desktop.deny')}</Button>
            <Button onClick={() => void respond(true)} loading={busy} className="login-submit">{t('desktop.approve')}</Button>
          </div>
        </>
      ) : null}
      {status === 'expired' ? <p className="login-error" role="alert">{t('desktop.expired')}</p> : null}
      {complete || status === 'expired' ? <Link to="/" className="login-back">{t('desktop.back')}</Link> : null}
    </div>
  )
}
