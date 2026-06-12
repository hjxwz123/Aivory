import { Outlet, Link } from 'react-router-dom'
import { useEffect } from 'react'
import { Logo } from '@/components/brand/logo'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { useTheme } from '@/store/theme'
import { useTranslation } from 'react-i18next'

export default function AuthLayout() {
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation('common')
  useEffect(() => syncSystem(), [syncSystem])

  return (
    <div className="relative min-h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)] flex flex-col">
      {/* Background */}
      <div aria-hidden className="pointer-events-none absolute inset-0 -z-10">
        <div
          className="absolute -top-40 left-1/2 -translate-x-1/2 size-[640px] rounded-full opacity-40 blur-3xl"
          style={{ background: 'radial-gradient(closest-side, color-mix(in oklch, var(--color-accent-soft) 70%, transparent), transparent 70%)' }}
        />
      </div>

      <header className="flex items-center justify-between px-5 sm:px-8 h-16">
        <Link to="/" aria-label={t('appName')}>
          <Logo size="md" />
        </Link>
        <div className="flex items-center gap-2">
          <LanguageToggle />
          <ThemeToggle />
        </div>
      </header>

      <main className="flex-1 grid place-items-center px-5 py-10">
        <div className="w-full max-w-[420px]">
          <Outlet />
        </div>
      </main>

      <footer className="px-5 sm:px-8 py-6 text-center text-xs text-[var(--color-fg-subtle)]">
        © {new Date().getFullYear()} {t('appName')}. {t('tagline')}
      </footer>
    </div>
  )
}
