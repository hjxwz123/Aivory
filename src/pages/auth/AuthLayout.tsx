import { Outlet, Link } from 'react-router-dom'
import { useEffect, useRef } from 'react'
import { motion } from 'framer-motion'
import { Logo, LogoMark } from '@/components/brand/logo'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { useTheme } from '@/store/theme'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

const ease = [0.2, 0.8, 0.2, 1]

export default function AuthLayout() {
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation('common')
  useEffect(() => syncSystem(), [syncSystem])

  return (
    <div className="relative min-h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)] flex">
      {/* ── Left brand panel (hidden on mobile) ─────────────────────── */}
      <aside className="hidden lg:flex w-[50%] min-h-svh relative overflow-hidden flex-col items-center justify-center bg-[var(--color-surface-sunken)]">
        <AuroraBackground />

        <div className="relative z-10 flex flex-col items-center text-center px-12">
          <motion.div
            initial={{ opacity: 0, scale: 0.85 }}
            animate={{ opacity: 1, scale: 1 }}
            transition={{ duration: 0.8, ease }}
          >
            <LogoMark size={56} />
          </motion.div>

          <motion.span
            initial={{ opacity: 0, y: 12 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.6, delay: 0.2, ease }}
            className="mt-6 font-serif text-[2rem] xl:text-[2.5rem] tracking-tight text-[var(--color-fg)]"
          >
            {t('appName')}
          </motion.span>

          <motion.p
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            transition={{ duration: 0.5, delay: 0.45, ease }}
            className="mt-3 text-sm text-[var(--color-fg-subtle)] tracking-wide"
          >
            {t('tagline')}
          </motion.p>
        </div>
      </aside>

      {/* ── Right form panel ────────────────────────────────────────── */}
      <div className="flex-1 min-w-0 flex flex-col min-h-svh relative">
        {/* Mobile-only background glow */}
        <div aria-hidden className="pointer-events-none absolute inset-0 -z-10 lg:hidden">
          <div
            className="absolute -top-40 left-1/2 -translate-x-1/2 size-[640px] rounded-full opacity-40 blur-3xl"
            style={{ background: 'radial-gradient(closest-side, color-mix(in oklch, var(--color-accent-soft) 70%, transparent), transparent 70%)' }}
          />
        </div>

        <header className="flex items-center justify-between px-5 sm:px-8 h-16">
          <Link to="/" aria-label={t('appName')} className="lg:invisible">
            <Logo size="md" />
          </Link>
          <div className="flex items-center gap-2">
            <LanguageToggle />
            <ThemeToggle />
          </div>
        </header>

        <main className="flex-1 grid place-items-center px-5 py-10">
          <motion.div
            initial={{ opacity: 0, y: 18 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.55, delay: 0.08, ease }}
            className="w-full max-w-[420px]"
          >
            <Outlet />
          </motion.div>
        </main>

        <footer className="px-5 sm:px-8 py-6 text-center text-xs text-[var(--color-fg-subtle)] lg:hidden">
          © {new Date().getFullYear()} {t('appName')}
        </footer>
      </div>
    </div>
  )
}

/**
 * AuroraBackground — soft, slowly-moving glow field. Canvas with gentle
 * floating orbs that drift and pulse. Falls back to a static CSS gradient
 * when prefers-reduced-motion is active.
 *
 * Reads accent/secondary token colors so it adapts to light/dark + accent.
 */
function AuroraBackground() {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reducedMotion) return

    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    let raf = 0
    let w = 0
    let h = 0

    function resize() {
      const dpr = Math.min(window.devicePixelRatio, 2)
      const rect = canvas!.getBoundingClientRect()
      w = rect.width
      h = rect.height
      canvas!.width = w * dpr
      canvas!.height = h * dpr
      ctx!.scale(dpr, dpr)
    }
    resize()
    window.addEventListener('resize', resize)

    const orbs = Array.from({ length: 5 }, (_, i) => ({
      x: w * (0.2 + Math.random() * 0.6),
      y: h * (0.15 + Math.random() * 0.7),
      r: 140 + Math.random() * 200,
      vx: (Math.random() - 0.5) * 0.12,
      vy: (Math.random() - 0.5) * 0.1,
      phase: Math.random() * Math.PI * 2,
      useSecondary: i >= 3,
    }))

    const style = getComputedStyle(document.documentElement)

    function draw(t: number) {
      ctx!.clearRect(0, 0, w, h)

      const accent = style.getPropertyValue('--color-accent-soft').trim()
      const secondary = style.getPropertyValue('--color-secondary-soft').trim()

      for (const orb of orbs) {
        orb.x += orb.vx
        orb.y += orb.vy
        if (orb.x < -orb.r) orb.x = w + orb.r
        if (orb.x > w + orb.r) orb.x = -orb.r
        if (orb.y < -orb.r) orb.y = h + orb.r
        if (orb.y > h + orb.r) orb.y = -orb.r

        const pulse = 0.55 + 0.45 * Math.sin(t * 0.0005 + orb.phase)
        const rad = orb.r * pulse

        const grad = ctx!.createRadialGradient(orb.x, orb.y, 0, orb.x, orb.y, rad)
        grad.addColorStop(0, orb.useSecondary ? secondary : accent)
        grad.addColorStop(1, 'transparent')
        ctx!.globalAlpha = 0.4
        ctx!.fillStyle = grad
        ctx!.beginPath()
        ctx!.arc(orb.x, orb.y, rad, 0, Math.PI * 2)
        ctx!.fill()
      }
      ctx!.globalAlpha = 1
      raf = requestAnimationFrame(draw)
    }
    raf = requestAnimationFrame(draw)

    return () => {
      cancelAnimationFrame(raf)
      window.removeEventListener('resize', resize)
    }
  }, [])

  return (
    <>
      <canvas
        ref={canvasRef}
        aria-hidden
        className={cn(
          'absolute inset-0 size-full',
          'motion-safe:block motion-reduce:hidden',
        )}
      />
      {/* Static fallback for reduced-motion */}
      <div
        aria-hidden
        className="absolute inset-0 motion-safe:hidden motion-reduce:block"
        style={{
          background: 'radial-gradient(ellipse at 40% 45%, color-mix(in oklch, var(--color-accent-soft) 50%, transparent) 0%, transparent 70%), radial-gradient(ellipse at 65% 60%, color-mix(in oklch, var(--color-secondary-soft) 40%, transparent) 0%, transparent 70%)',
        }}
      />
    </>
  )
}
