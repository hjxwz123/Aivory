import { useEffect, useRef } from 'react'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'
import { useTranslation } from 'react-i18next'
import { Aurora } from '@/components/landing/fx/aurora'
import { ShinyText } from '@/components/landing/fx/shiny-text'
import { SplitText } from '@/components/landing/fx/split-text'
import { cn } from '@/lib/utils'

gsap.registerPlugin(useGSAP)

// The Aivory mark — the hollow triangular vessel from components/brand/logo.
const MARK_PATH =
  'M16 4.5c-1.05 0-2.02.6-2.47 1.55L4.34 24.6c-.74 1.55.4 3.4 2.13 3.4h19.06c1.74 0 2.87-1.85 2.13-3.4L18.47 6.05A2.72 2.72 0 0 0 16 4.5Zm0 4.3 9.8 20.6H6.2L16 8.8Z'

/**
 * AuthHero — the interactive brand scene on the auth pages' left panel. Built
 * around the Aivory triangular mark: the outline draws itself, a light runs
 * along it, the apex glows, particles converge to the apex ("attention focusing
 * to a point"), and the whole scene parallaxes toward the pointer.
 *
 * All motion is GSAP, gated behind prefers-reduced-motion (which leaves a calm
 * static scene). useGSAP reverts everything on unmount.
 */
export function AuthHero() {
  const { t } = useTranslation('common')
  const root = useRef<HTMLDivElement>(null)
  const scene = useRef<HTMLDivElement>(null)
  const rings = useRef<HTMLDivElement>(null)

  useGSAP(
    () => {
      const mm = gsap.matchMedia()
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        // ── Entrance ──────────────────────────────────────────────────────
        const tl = gsap.timeline({ defaults: { ease: 'power3.out' } })
        tl.from('.hero-ring', { scale: 0.35, autoAlpha: 0, duration: 1.1, stagger: 0.12 })
          .from('.hero-mark', { scale: 0.6, autoAlpha: 0, duration: 0.9, ease: 'power4.out' }, '-=0.75')
          .fromTo(
            '.hero-draw',
            { strokeDashoffset: 100 },
            { strokeDashoffset: 0, duration: 1.5, ease: 'power2.inOut' },
            '-=0.65',
          )
          .from('.hero-apex', { scale: 0, autoAlpha: 0, duration: 0.5, ease: 'back.out(2)' }, '-=0.4')
          // The wordmark's own rise moved into <SplitText> (per-char).
          .from('.hero-tagline', { y: 12, autoAlpha: 0, duration: 0.6 }, '-=0.3')

        // ── Continuous ────────────────────────────────────────────────────
        // A bright segment runs along the outline forever.
        gsap.to('.hero-spark', { strokeDashoffset: -100, duration: 3.4, ease: 'none', repeat: -1 })
        // The apex glow breathes.
        gsap.to('.hero-glow', {
          attr: { r: 3.4 },
          opacity: 0.85,
          duration: 1.7,
          ease: 'sine.inOut',
          yoyo: true,
          repeat: -1,
        })
        // The outermost ring's accent point orbits slowly.
        gsap.to('.hero-orbit', { rotate: 360, transformOrigin: '50% 50%', duration: 64, ease: 'none', repeat: -1 })
        // The mark drifts up and down a touch — alive, not static.
        gsap.to('.hero-mark', { y: -10, duration: 4.2, ease: 'sine.inOut', yoyo: true, repeat: -1 })

        // ── Pointer parallax ──────────────────────────────────────────────
        const xScene = gsap.quickTo(scene.current, 'x', { duration: 0.7, ease: 'power3' })
        const yScene = gsap.quickTo(scene.current, 'y', { duration: 0.7, ease: 'power3' })
        const xRings = gsap.quickTo(rings.current, 'x', { duration: 1, ease: 'power3' })
        const yRings = gsap.quickTo(rings.current, 'y', { duration: 1, ease: 'power3' })
        const rotMark = gsap.quickTo('.hero-mark-tilt', 'rotate', { duration: 0.8, ease: 'power3' })

        function onMove(e: PointerEvent) {
          const el = root.current
          if (!el) return
          const r = el.getBoundingClientRect()
          const nx = (e.clientX - r.left) / r.width - 0.5 // -0.5 … 0.5
          const ny = (e.clientY - r.top) / r.height - 0.5
          xScene(nx * 26)
          yScene(ny * 26)
          xRings(nx * 54)
          yRings(ny * 54)
          rotMark(nx * 8)
        }
        function onLeave() {
          xScene(0)
          yScene(0)
          xRings(0)
          yRings(0)
          rotMark(0)
        }
        const el = root.current
        el?.addEventListener('pointermove', onMove)
        el?.addEventListener('pointerleave', onLeave)
        return () => {
          el?.removeEventListener('pointermove', onMove)
          el?.removeEventListener('pointerleave', onLeave)
        }
      })
    },
    { scope: root },
  )

  return (
    <div
      ref={root}
      className="relative size-full overflow-hidden bg-[var(--color-surface-sunken)] grid place-items-center"
    >
      {/* The same clay aurora curtain as the landing hero, faded out before it
          reaches the mark so the brand scene stays the focal point. First in
          DOM = painted beneath the rings/particles (§ welcome fx). */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0"
        style={{
          maskImage: 'linear-gradient(to bottom, #000 0%, #000 45%, transparent 85%)',
          WebkitMaskImage: 'linear-gradient(to bottom, #000 0%, #000 45%, transparent 85%)',
        }}
      >
        <Aurora
          className="opacity-50"
          colorStops={[
            'color-mix(in oklch, var(--color-accent) 58%, var(--color-bg))',
            'color-mix(in oklch, var(--color-accent) 26%, var(--color-bg))',
            'color-mix(in oklch, var(--color-accent) 58%, var(--color-bg))',
          ]}
          amplitude={1.0}
          blend={0.6}
          speed={0.45}
          intensity={1.1}
        />
      </div>
      <ConvergingParticles />

      {/* Concentric rings + orbiting accent (parallax layer). */}
      <div ref={rings} aria-hidden className="pointer-events-none absolute inset-0 grid place-items-center">
        {[440, 640, 860].map((s) => (
          <div
            key={s}
            className="hero-ring absolute rounded-full border border-[var(--color-border)]/55"
            style={{ width: s, height: s }}
          />
        ))}
        <div className="hero-orbit absolute" style={{ width: 640, height: 640 }}>
          <span className="absolute left-1/2 top-0 size-2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-[var(--color-secondary)] shadow-[0_0_14px_var(--color-secondary)]" />
        </div>
      </div>

      {/* Mark + wordmark (parallax layer). */}
      <div ref={scene} className="relative z-10 flex flex-col items-center px-12 text-center">
        <div className="hero-mark">
          <div className="hero-mark-tilt">
            <AnimatedMark />
          </div>
        </div>

        <span className="mt-8 block overflow-hidden pb-[0.08em] font-serif text-[2rem] xl:text-[2.6rem] tracking-tight text-[var(--color-fg)]">
          {/* Per-character masked rise — the same headline entrance language as
              the landing hero (§ welcome fx). */}
          <SplitText
            text={t('appName')}
            splitType="chars"
            delay={40}
            duration={0.8}
            from={{ yPercent: 120 }}
            to={{ yPercent: 0 }}
            threshold={0}
            rootMargin="0px"
            className="block pb-[0.08em] -mb-[0.08em]"
          />
        </span>
        <p className="hero-tagline mt-3 text-sm tracking-wide">
          {/* Same slow sheen as the landing badge/footer sign-off. */}
          <ShinyText text={t('tagline')} baseColor="var(--color-fg-subtle)" speed={5} />
        </p>
      </div>
    </div>
  )
}

/** The large animated mark: soft fill, self-drawing outline, a running light, glowing apex. */
function AnimatedMark() {
  return (
    <svg width={172} height={172} viewBox="0 0 32 32" role="img" aria-label="Aivory" className="overflow-visible">
      <defs>
        <linearGradient id="hero-stroke" x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="var(--color-accent)" />
          <stop offset="1" stopColor="var(--color-secondary)" />
        </linearGradient>
        <linearGradient id="hero-fill" x1="16" y1="4" x2="16" y2="28" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="var(--color-accent)" stopOpacity="0.16" />
          <stop offset="1" stopColor="var(--color-secondary)" stopOpacity="0.05" />
        </linearGradient>
        <filter id="hero-blur" x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur stdDeviation="0.7" />
        </filter>
      </defs>

      {/* Soft inner fill */}
      <path d={MARK_PATH} fill="url(#hero-fill)" />
      {/* Base outline (draws on entrance) */}
      <path
        d={MARK_PATH}
        pathLength={100}
        fill="none"
        stroke="url(#hero-stroke)"
        strokeWidth={0.5}
        strokeLinejoin="round"
        className="hero-draw"
        style={{ strokeDasharray: 100 }}
      />
      {/* Bright segment that runs along the outline */}
      <path
        d={MARK_PATH}
        pathLength={100}
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth={0.9}
        strokeLinecap="round"
        strokeLinejoin="round"
        className="hero-spark"
        style={{ strokeDasharray: '5 95' }}
        filter="url(#hero-blur)"
      />
      {/* Apex accent: a glow that breathes + a crisp dot */}
      <circle cx="16" cy="22.2" r="2.6" fill="var(--color-accent)" opacity="0.4" filter="url(#hero-blur)" className="hero-glow" />
      <circle cx="16" cy="22.2" r="1.4" fill="var(--color-accent)" className="hero-apex" />
    </svg>
  )
}

/**
 * ConvergingParticles — soft motes drift across the panel and accelerate toward
 * the mark's apex, fading as they arrive: "attention focusing to a point".
 * Hidden under reduced motion (a static glow stands in via CSS).
 */
function ConvergingParticles() {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    let raf = 0
    let w = 0
    let h = 0
    let target = { x: 0, y: 0 }
    const dpr = Math.min(window.devicePixelRatio || 1, 2)

    function resize() {
      const rect = canvas!.getBoundingClientRect()
      w = rect.width
      h = rect.height
      canvas!.width = w * dpr
      canvas!.height = h * dpr
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0)
      // Apex sits a little above the panel centre (where the mark's tip is).
      target = { x: w / 2, y: h * 0.42 }
    }
    resize()
    window.addEventListener('resize', resize)

    type P = { x: number; y: number; life: number; max: number; size: number; sage: boolean }
    const particles: P[] = []
    function spawn(): P {
      // Start somewhere out on the panel, biased to the lower half.
      const edge = Math.random()
      const x = edge * w
      const y = h * (0.45 + Math.random() * 0.6)
      const max = 160 + Math.random() * 140
      return { x, y, life: 0, max, size: 1 + Math.random() * 2, sage: Math.random() < 0.35 }
    }
    for (let i = 0; i < 48; i++) {
      const p = spawn()
      p.life = Math.random() * p.max
      particles.push(p)
    }

    const style = getComputedStyle(document.documentElement)

    function draw() {
      ctx!.clearRect(0, 0, w, h)
      const accent = style.getPropertyValue('--color-accent').trim() || '#6a4de6'
      const secondary = style.getPropertyValue('--color-secondary').trim() || '#5aa17f'
      ctx!.globalCompositeOperation = 'lighter'

      for (const p of particles) {
        p.life += 1
        const tprog = p.life / p.max
        // Ease toward the apex; faster as it nears the end.
        const k = 0.012 + tprog * tprog * 0.06
        p.x += (target.x - p.x) * k
        p.y += (target.y - p.y) * k
        // Fade in then out.
        const alpha = Math.sin(Math.min(1, tprog) * Math.PI) * 0.5
        const r = p.size * (1 - tprog * 0.5)
        const grad = ctx!.createRadialGradient(p.x, p.y, 0, p.x, p.y, Math.max(0.5, r * 6))
        grad.addColorStop(0, p.sage ? secondary : accent)
        grad.addColorStop(1, 'transparent')
        ctx!.globalAlpha = alpha
        ctx!.fillStyle = grad
        ctx!.beginPath()
        ctx!.arc(p.x, p.y, r * 6, 0, Math.PI * 2)
        ctx!.fill()
        if (tprog >= 1) Object.assign(p, spawn())
      }
      ctx!.globalAlpha = 1
      ctx!.globalCompositeOperation = 'source-over'
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
      <canvas ref={canvasRef} aria-hidden className={cn('absolute inset-0 size-full', 'motion-safe:block motion-reduce:hidden')} />
      <div
        aria-hidden
        className="absolute inset-0 motion-safe:hidden motion-reduce:block"
        style={{
          background:
            'radial-gradient(ellipse at 50% 42%, color-mix(in oklch, var(--color-accent-soft) 55%, transparent) 0%, transparent 65%)',
        }}
      />
    </>
  )
}
