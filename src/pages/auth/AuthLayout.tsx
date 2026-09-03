import { Outlet, Link, useLocation } from 'react-router-dom'
import { useEffect, useRef } from 'react'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'
import { TracedLogo } from '@/components/brand/logo'
import { AuthHero } from '@/components/auth/auth-hero'
import { ClickSpark } from '@/components/landing/fx/click-spark'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { useTheme } from '@/store/theme'
import { useTranslation } from 'react-i18next'

gsap.registerPlugin(useGSAP)

export default function AuthLayout() {
  const syncSystem = useTheme((s) => s.syncSystem)
  const { pathname } = useLocation()
  const { t } = useTranslation(['common', 'auth'])
  const onLogin = pathname === '/login'
  useEffect(() => syncSystem(), [syncSystem])

  const root = useRef<HTMLDivElement>(null)

  // The brand panel owns its own (richer) motion in AuthHero; here we only ease
  // the form card in. Gated by prefers-reduced-motion; useGSAP reverts on unmount.
  useGSAP(
    () => {
      const mm = gsap.matchMedia()
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        gsap.from('.auth-card', { y: 18, autoAlpha: 0, duration: 0.6, delay: 0.15, ease: 'power3.out' })
      })
    },
    { scope: root },
  )

  return (
    <div ref={root} className="relative h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
    {/* Same accent click-burst as the landing page (§ welcome fx). */}
    <ClickSpark sparkSize={9} sparkRadius={18} sparkCount={8} duration={450} className="flex h-full w-full">
      {/* ── Left brand panel (hidden on mobile) ─────────────────────── */}
      <aside className="hidden h-full w-[50%] lg:block">
        <AuthHero />
      </aside>

      {/* ── Right form panel ────────────────────────────────────────── */}
      {/* `isolate`: the -z-10 mobile glow needs a stacking context here or it
          paints behind the page's opaque background (root-context negative z). */}
      <div className="relative isolate flex h-full min-w-0 flex-1 flex-col">
        {/* Mobile-only background glow */}
        <div aria-hidden className="pointer-events-none absolute inset-0 -z-10 lg:hidden">
          <div
            className="absolute -top-40 left-1/2 -translate-x-1/2 size-[640px] rounded-full opacity-40 blur-3xl"
            style={{ background: 'radial-gradient(closest-side, color-mix(in oklch, var(--color-accent-soft) 70%, transparent), transparent 70%)' }}
          />
        </div>

        <header className="flex h-16 shrink-0 items-center justify-between px-5 sm:px-8">
          <Link to="/" aria-label={t('appName')} className="lg:invisible">
            <TracedLogo size="md" />
          </Link>
          <div className="flex items-center gap-2">
            <LanguageToggle />
            <ThemeToggle />
          </div>
        </header>

        <main className="grid min-h-0 flex-1 place-items-center px-5 py-5 sm:py-8 [@media(max-height:740px)]:py-2">
          <div className="auth-card max-h-full w-full max-w-[420px] overflow-y-auto py-1">
            <Outlet />
          </div>
        </main>

        <footer className="flex min-h-10 shrink-0 items-center justify-center px-5 pb-3 text-center text-xs text-[var(--color-fg-subtle)] sm:px-8">
          {onLogin ? (
            <nav aria-label={t('auth:login.legalLinksLabel')} className="flex items-center justify-center gap-3">
              <Link className="transition-colors hover:text-[var(--color-fg)]" to="/terms" target="_blank" rel="noopener noreferrer">
                {t('auth:legal.terms.title')}
              </Link>
              <span aria-hidden className="text-[var(--color-fg-faint)]">·</span>
              <Link className="transition-colors hover:text-[var(--color-fg)]" to="/privacy" target="_blank" rel="noopener noreferrer">
                {t('auth:legal.privacy.title')}
              </Link>
            </nav>
          ) : (
            <span className="lg:hidden">© {new Date().getFullYear()} {t('common:appName')}</span>
          )}
        </footer>
      </div>
    </ClickSpark>
    </div>
  )
}
