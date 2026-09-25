import { useEffect } from 'react'
import { Link, Outlet } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Monitor, Moon, Sun } from 'lucide-react'
import { LoginHero } from '@/components/auth/login-hero'
import { TracedLogo } from '@/components/brand/logo'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { useTheme } from '@/store/theme'
import '@/styles/login.css'

const themeOptions = [
  { value: 'light', icon: Sun },
  { value: 'dark', icon: Moon },
  { value: 'system', icon: Monitor },
] as const

export function LoginLayout() {
  const { t } = useTranslation(['auth', 'common', 'settings'])
  const pref = useTheme((s) => s.pref)
  const setPref = useTheme((s) => s.setPref)
  const syncSystem = useTheme((s) => s.syncSystem)

  useEffect(() => syncSystem(), [syncSystem])

  return (
    <div className="login-page">
      <header className="login-header">
        <Link to="/" className="login-brand" aria-label={t('common:appName')}>
          <TracedLogo tone="system" />
        </Link>
        <div className="login-tools">
          <LanguageToggle variant="text" />
          <div className="login-themes" role="group" aria-label={t('common:aria.themeGroup')}>
            {themeOptions.map(({ value, icon: Icon }) => (
              <button key={value} type="button" className="login-theme-button" aria-pressed={pref === value} aria-label={t(`settings:appearance.${value}`)} title={t(`settings:appearance.${value}`)} onClick={() => setPref(value)}>
                <Icon size={16} strokeWidth={1.6} aria-hidden />
              </button>
            ))}
          </div>
        </div>
      </header>
      <main className="login-main">
        <LoginHero />
        <section className="login-panel" aria-labelledby="login-title">
          <Outlet />
        </section>
      </main>
      <footer className="login-footer">© {new Date().getFullYear()} {t('common:appName')}</footer>
    </div>
  )
}
