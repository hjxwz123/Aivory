import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { MonitorUp } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { desktopUpdateApi, type DesktopUpdateState } from '@/api/desktop-update'

export function DesktopUpdateNotice() {
  const { t } = useTranslation('admin')
  const { pathname } = useLocation()
  const [state, setState] = useState<DesktopUpdateState | null>(null)

  useEffect(() => {
    let active = true
    const load = () => { void desktopUpdateApi.state().then((next) => { if (active) setState(next) }).catch(() => {}) }
    load()
    const timer = window.setInterval(load, 5 * 60 * 1000)
    window.addEventListener('aivory:desktop-update-changed', load)
    return () => { active = false; window.clearInterval(timer); window.removeEventListener('aivory:desktop-update-changed', load) }
  }, [])

  if (!state?.update_available || pathname === '/admin/settings/desktop') return null

  return (
    <div className="shrink-0 px-4 py-2 sm:px-8">
      <div role="status" className="mx-auto flex max-w-[var(--layout-content-max-w)] flex-wrap items-center gap-x-3 gap-y-2 rounded-[8px] bg-[var(--color-bg-muted)] px-3 py-2 text-xs">
        <MonitorUp size={15} className="shrink-0 text-[var(--color-accent)]" aria-hidden />
        <span className="min-w-0 flex-1">{t('desktop.notice', { version: state.latest_version })}</span>
        <Link to="/admin/settings/desktop" className="font-medium text-[var(--color-accent)] hover:underline">{t('desktop.configure')}</Link>
      </div>
    </div>
  )
}
