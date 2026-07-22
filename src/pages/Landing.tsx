import { useEffect, useRef } from 'react'
import { Link } from 'react-router-dom'
import { gsap } from 'gsap'
import { ScrollTrigger } from 'gsap/ScrollTrigger'
import { useGSAP } from '@gsap/react'
import {
  ArrowRight,
  Sparkles,
  BookOpen,
  Code2,
  Telescope,
  Lock,
  Cloud,
  ArrowUp,
  PenLine,
  Lightbulb,
  type LucideIcon,
} from 'lucide-react'
import { Logo } from '@/components/brand/logo'
import { MembershipTiers } from '@/components/landing/membership-tiers'
import { FlowField } from '@/components/landing/flow-field'
import { LiveDemo } from '@/components/landing/live-demo'
import { WritingScene, ResearchScene, CodeScene } from '@/components/landing/capability-scenes'
import { ScrollReveal } from '@/components/landing/fx/scroll-reveal'
import { Aurora } from '@/components/landing/fx/aurora'
import { SplitText } from '@/components/landing/fx/split-text'
import { ShinyText } from '@/components/landing/fx/shiny-text'
import { GradientText } from '@/components/landing/fx/gradient-text'
import { ClickSpark } from '@/components/landing/fx/click-spark'
import { SpotlightCard } from '@/components/landing/fx/spotlight-card'
import { GlareHover } from '@/components/landing/fx/glare-hover'
import { StarBorder } from '@/components/landing/fx/star-border'
import { CountUp } from '@/components/landing/fx/count-up'
import { Particles } from '@/components/landing/fx/particles'
import { ScrollFloat } from '@/components/landing/fx/scroll-float'
import { ScrollVelocity } from '@/components/landing/fx/scroll-velocity'
import { BlurText } from '@/components/landing/fx/blur-text'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { useTheme } from '@/store/theme'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

gsap.registerPlugin(ScrollTrigger, useGSAP)

const CAPABILITY_KEYS = [
  { icon: BookOpen, key: 'writing', Scene: WritingScene },
  { icon: Telescope, key: 'research', Scene: ResearchScene },
  { icon: Code2, key: 'code', Scene: CodeScene },
] as const

const USE_CASE_KEYS = [
  { key: 'writers', icon: PenLine },
  { key: 'researchers', icon: Telescope },
  { key: 'engineers', icon: Code2 },
  { key: 'thinkers', icon: Lightbulb },
] as const

// The mainstream models Aivory convenes (§ models showcase). Names + makers are
// proper nouns (untranslated); logos are vendored brand marks in /public/brand,
// painted in token ink via CSS mask. Claude leads (slightly larger) for hierarchy.
interface ModelSpec {
  key: string
  name: string
  maker: string
  slug: string
  lead?: boolean
}

const MODELS: readonly ModelSpec[] = [
  { key: 'claude', name: 'Claude', maker: 'Anthropic', slug: 'claude', lead: true },
  { key: 'gpt', name: 'GPT', maker: 'OpenAI', slug: 'openai' },
  { key: 'gemini', name: 'Gemini', maker: 'Google', slug: 'gemini' },
  { key: 'llama', name: 'Llama', maker: 'Meta', slug: 'meta' },
  { key: 'mistral', name: 'Mistral', maker: 'Mistral AI', slug: 'mistral' },
  { key: 'qwen', name: 'Qwen', maker: 'Alibaba', slug: 'qwen' },
  { key: 'deepseek', name: 'DeepSeek', maker: 'DeepSeek', slug: 'deepseek' },
  { key: 'grok', name: 'Grok', maker: 'xAI', slug: 'grok' },
]

const PRINCIPLE_KEYS = ['noDefaults', 'readingFirst', 'youOwnIt'] as const

export default function Landing() {
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation(['landing', 'common', 'nav'])

  const root = useRef<HTMLDivElement>(null)
  const topBtn = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    syncSystem()
  }, [syncSystem])

  // All page motion lives here (GSAP, scoped to `root`). gsap.matchMedia gates
  // everything behind prefers-reduced-motion: under "reduce" nothing animates
  // and every element keeps its natural (visible) state. useGSAP reverts all
  // tweens + ScrollTriggers on unmount.
  useGSAP(
    () => {
      const btn = topBtn.current
      if (btn) gsap.set(btn, { autoAlpha: 0 })

      const mm = gsap.matchMedia()
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        // Hero: a calm, choreographed reveal. Headline lines rise out of a mask.
        const tl = gsap.timeline({ defaults: { ease: 'power3.out' } })
        tl.from('.hero-badge', { y: 14, autoAlpha: 0, duration: 0.5 })
          .from('.hero-line', { yPercent: 115, duration: 0.9, stagger: 0.12 }, '-=0.15')
          // The violet ink wicks across line 2 just as it settles.
          .fromTo('.ink-overlay', { '--ink': 0 }, { '--ink': 1, duration: 1.2, ease: 'power2.inOut' }, '-=0.4')
          // The accent baseline segment draws in alongside it.
          .from('.hero-baseline', { scaleX: 0, duration: 0.8 }, '<')
          .from('.hero-sub', { y: 16, autoAlpha: 0, duration: 0.7 }, '-=0.85')
          .from('.hero-cta', { y: 22, autoAlpha: 0, duration: 0.7 }, '-=0.45')
          .from('.hero-meta', { y: 10, autoAlpha: 0, duration: 0.6 }, '-=0.5')
          .from('.hero-preview', { y: 48, autoAlpha: 0, duration: 1.0 }, '-=0.9')

        // Nav backdrop fades in over the first bit of scroll.
        gsap.fromTo('.nav-bg', { autoAlpha: 0 }, {
          autoAlpha: 1, ease: 'none',
          scrollTrigger: { start: 8, end: 96, scrub: true },
        })
        // Reading-progress hairline along the nav's bottom edge.
        gsap.fromTo('.nav-progress', { scaleX: 0 }, {
          scaleX: 1, ease: 'none', transformOrigin: 'left center',
          scrollTrigger: { start: 0, end: 'max', scrub: 0.3 },
        })

        // Section reveals — single elements rise as they enter the viewport.
        gsap.utils.toArray<HTMLElement>('[data-reveal]').forEach((el) => {
          gsap.from(el, {
            y: 30, autoAlpha: 0, duration: 0.8, ease: 'power3.out',
            scrollTrigger: { trigger: el, start: 'top 86%', once: true },
          })
        })
        // Grid reveals — children stagger in.
        gsap.utils.toArray<HTMLElement>('[data-reveal-group]').forEach((group) => {
          gsap.from(Array.from(group.children), {
            y: 26, autoAlpha: 0, duration: 0.7, stagger: 0.09, ease: 'power3.out',
            scrollTrigger: { trigger: group, start: 'top 84%', once: true },
          })
        })

        // Ambient depth: the warm gradient orbs drift perpetually — a calm,
        // Claude-like motion — each at its own slow cadence and scale breath.
        gsap.to('.orb-1', { xPercent: 8, yPercent: 12, scale: 1.08, duration: 16, ease: 'sine.inOut', repeat: -1, yoyo: true })
        gsap.to('.orb-2', { xPercent: -10, yPercent: -8, scale: 1.12, duration: 21, ease: 'sine.inOut', repeat: -1, yoyo: true })
        gsap.to('.orb-3', { xPercent: 6, yPercent: -12, scale: 1.06, duration: 26, ease: 'sine.inOut', repeat: -1, yoyo: true })

        const cleanups: Array<() => void> = []
        const fine = window.matchMedia('(pointer: fine)').matches

        // Magnetic CTAs — the control eases toward the cursor, then springs back.
        if (fine) {
          root.current?.querySelectorAll<HTMLElement>('[data-magnetic]').forEach((el) => {
            const xq = gsap.quickTo(el, 'x', { duration: 0.5, ease: 'power3' })
            const yq = gsap.quickTo(el, 'y', { duration: 0.5, ease: 'power3' })
            const move = (e: PointerEvent) => {
              const r = el.getBoundingClientRect()
              xq((e.clientX - (r.left + r.width / 2)) * 0.35)
              yq((e.clientY - (r.top + r.height / 2)) * 0.45)
            }
            const leave = () => {
              xq(0)
              yq(0)
            }
            el.addEventListener('pointermove', move)
            el.addEventListener('pointerleave', leave)
            cleanups.push(() => {
              el.removeEventListener('pointermove', move)
              el.removeEventListener('pointerleave', leave)
            })
          })
        }

        // Pointer-parallax tilt on the hero demo — subtle 3D depth, desktop only.
        const preview = root.current?.querySelector<HTMLElement>('.hero-preview')
        if (preview && fine) {
          gsap.set(preview, { transformPerspective: 1100, transformOrigin: 'center' })
          const rx = gsap.quickTo(preview, 'rotationX', { duration: 0.5, ease: 'power3' })
          const ry = gsap.quickTo(preview, 'rotationY', { duration: 0.5, ease: 'power3' })
          const onMove = (e: PointerEvent) => {
            const r = preview.getBoundingClientRect()
            ry(((e.clientX - r.left) / r.width - 0.5) * 5)
            rx(-((e.clientY - r.top) / r.height - 0.5) * 5)
          }
          const onLeave = () => {
            rx(0)
            ry(0)
          }
          preview.addEventListener('pointermove', onMove)
          preview.addEventListener('pointerleave', onLeave)
          cleanups.push(() => {
            preview.removeEventListener('pointermove', onMove)
            preview.removeEventListener('pointerleave', onLeave)
          })
        }

        if (cleanups.length) return () => cleanups.forEach((fn) => fn())
      })

      // Reduced motion: nothing animates. The one element whose *resting* state
      // is the end of an animation is the ink wash (hidden by default) — show it
      // fully wicked-in so line two keeps its violet treatment, statically. The
      // nav-progress hairline is a pure scroll affordance, so it stays hidden.
      mm.add('(prefers-reduced-motion: reduce)', () => {
        gsap.set('.ink-overlay', { '--ink': 1 })
      })

      // Scroll-to-top visibility (no React state, no window scroll listener).
      ScrollTrigger.create({
        start: 760,
        end: 'max',
        onToggle: (self) => {
          if (btn) gsap.to(btn, { autoAlpha: self.isActive ? 1 : 0, duration: 0.25, ease: 'power2.out' })
        },
      })
    },
    { scope: root },
  )

  return (
    // `isolate` scopes the -z-10 ornament (orbs/grain) to THIS element's
    // stacking context so it paints above the opaque page background instead
    // of being buried beneath it at the root context.
    <div ref={root} className="relative isolate min-h-svh overflow-x-clip bg-[var(--color-bg)] text-[var(--color-fg)]">
      {/* Every click anywhere on the page bursts a small ring of accent sparks —
          one shared canvas overlay, reduced-motion silent (§ welcome fx). */}
      <ClickSpark sparkSize={9} sparkRadius={18} sparkCount={8} duration={450}>
        {/* Background accents */}
        <BackgroundOrnament />

        {/* Nav */}
        <header className="sticky top-0 z-40 backdrop-blur-[1px]">
          <div
            aria-hidden
            className="nav-bg absolute inset-0 -z-10 bg-[var(--color-bg)]/85 border-b border-[var(--color-border-subtle)]"
          />
          {/* Reading-progress hairline — scaleX scrubbed by scroll (see useGSAP). */}
          <span
            aria-hidden
            className="nav-progress absolute inset-x-0 bottom-0 h-[2px] origin-left scale-x-0 bg-[var(--color-accent)]"
          />
          <div className="mx-auto max-w-[76rem] flex items-center justify-between px-5 sm:px-8 h-[64px]">
            <Link to="/" aria-label={t('common:aria.homeLink')}>
              <Logo size="md" />
            </Link>
            <nav className="hidden md:flex items-center gap-7 text-sm text-[var(--color-fg-muted)]">
              <a href="#capabilities" className="hover:text-[var(--color-fg)] interactive">{t('nav:capabilities')}</a>
              <a href="#how" className="hover:text-[var(--color-fg)] interactive">{t('nav:howItFeels')}</a>
              <a href="#models" className="hover:text-[var(--color-fg)] interactive">{t('nav:models')}</a>
              <a href="#pricing" className="hover:text-[var(--color-fg)] interactive">{t('nav:pricing')}</a>
              <a href="#safety" className="hover:text-[var(--color-fg)] interactive">{t('nav:safety')}</a>
            </nav>
            <div className="flex items-center gap-2">
              <LanguageToggle className="hidden sm:inline-flex" />
              <ThemeToggle className="hidden sm:inline-flex" />
              <Link to="/login">
                <Button variant="ghost" size="sm">
                  {t('common:actions.signIn')}
                </Button>
              </Link>
              <Link to="/chat">
                <Button size="sm" trailingIcon={<ArrowRight size={14} aria-hidden />}>
                  {t('common:actions.openAivory')}
                </Button>
              </Link>
            </div>
          </div>
        </header>

        {/* Hero — left-anchored editorial split; the second headline line carries
          a violet ink-wash (dual-layer masked real text, scrubbed by GSAP).
          `isolate`: the -z-10 backdrop (flow-field + aurora) needs a stacking
          context HERE, or negative z drops it behind the page's opaque
          background (it paints at the root context otherwise). */}
        <section className="relative isolate pt-16 sm:pt-24 pb-20 sm:pb-28">
          {/* Signature generative line-field — a living backdrop behind the hero,
            edge-faded so it never competes with the headline or the demo. */}
          <div
            aria-hidden
            className="pointer-events-none absolute inset-0 -z-10 overflow-hidden"
            style={{
              maskImage: 'radial-gradient(120% 90% at 50% 30%, #000 55%, transparent 100%)',
              WebkitMaskImage: 'radial-gradient(120% 90% at 50% 30%, #000 55%, transparent 100%)',
            }}
          >
            <FlowField />
            {/* A clay→sage aurora curtain washes over the line-field — the same
              warmth as the orbs, now alive. Token stops re-resolve on theme
              flip; renders nothing under reduced-motion (§ welcome fx). */}
            {/* Stops lean the accent toward the page background, so the curtain
              reads pastel on light and deep-glow on dark from ONE expression.
              A single-hue ramp (deep→pale→deep clay) — the shader lerps in
              RGB, where two-hue ramps mud out through gray. */}
            <Aurora
              className="opacity-60"
              colorStops={[
                'color-mix(in oklch, var(--color-accent) 62%, var(--color-bg))',
                'color-mix(in oklch, var(--color-accent) 30%, var(--color-bg))',
                'color-mix(in oklch, var(--color-accent) 62%, var(--color-bg))',
              ]}
              amplitude={1.05}
              blend={0.6}
              speed={0.5}
              intensity={1.25}
            />
            {/* Drifting motes over the aurora — they lean toward the pointer,
              so the whole first screen answers the hand (§ welcome fx). */}
            <Particles
              particleCount={160}
              particleSpread={11}
              speed={0.08}
              particleBaseSize={90}
              moveParticlesOnHover
              particleHoverFactor={1.6}
            />
          </div>
          <div className="mx-auto grid max-w-[76rem] items-center gap-12 px-5 sm:px-8 lg:grid-cols-[1.12fr_0.88fr] lg:gap-10">
            <div className="min-w-0 max-w-[40rem]">
              <div className="hero-badge inline-block">
                <Badge variant="sage" leadingIcon={<Sparkles size={11} aria-hidden />} className="mb-7">
                  {/* A slow sheen breathes across the announcement — the page's
                    single sage moment stays sage, just alive (§ welcome fx). */}
                  <ShinyText
                    text={t('landing:badgeNew')}
                    baseColor="var(--color-secondary)"
                    shineColor="var(--color-fg)"
                    speed={3.5}
                  />
                </Badge>
              </div>
              {/* Each line rises out of an overflow mask; line 2 holds the ink-wash. */}
              <h1 className="font-optical font-serif tracking-tight text-balance text-[clamp(2.5rem,6.4vw,5.5rem)] leading-[1.03] text-[var(--color-fg)]">
                <span className="block overflow-hidden pb-[0.08em]">
                  {/* Line 1 rises per-character out of the same overflow mask the
                    old block-rise used — one entrance language, finer grain.
                    Line 2 keeps its block rise + ink wash (§ welcome fx). */}
                  {/* pb/-mb: the inner mask needs the same descender room the
                    outer span reserves, else a wrapped second line clips. */}
                  <SplitText
                    text={t('landing:hero.titleLine1')}
                    splitType="chars"
                    delay={26}
                    duration={0.85}
                    from={{ yPercent: 115 }}
                    to={{ yPercent: 0 }}
                    threshold={0}
                    rootMargin="0px"
                    className="block pb-[0.08em] -mb-[0.08em]"
                  />
                </span>
                <span className="block overflow-hidden pb-[0.14em]">
                  <span className="hero-line block italic">
                    <span className="ink-wash relative inline-block">
                      <span className="block text-[var(--color-fg-muted)]">{t('landing:hero.titleLine2')}</span>
                      <span
                        aria-hidden
                        className="ink-overlay absolute inset-0 block"
                        style={{ color: 'color-mix(in oklch, var(--color-accent) 60%, var(--color-fg-muted))' }}
                      >
                        {t('landing:hero.titleLine2')}
                      </span>
                    </span>
                  </span>
                </span>
              </h1>
              {/* Baseline hairline with one accent segment that draws in. */}
              <div className="relative mt-6 h-px w-full max-w-[34rem] bg-[var(--color-divider)]">
                <span className="hero-baseline absolute left-0 top-0 h-px w-[120px] origin-left bg-[var(--color-accent)]" />
              </div>
              <p className="hero-sub mt-7 max-w-xl text-[var(--color-fg-muted)] text-pretty text-[15px] sm:text-base leading-relaxed">
                {t('landing:hero.subtitle')}
              </p>

              <div className="hero-cta mt-9 flex flex-wrap items-center gap-3">
                <Link to="/chat" data-magnetic className="inline-block will-change-transform">
                  <Button size="lg" trailingIcon={<ArrowRight size={15} aria-hidden />}>
                    {t('landing:cta.primary')}
                  </Button>
                </Link>
                <Link to="/login" data-magnetic className="inline-block will-change-transform">
                  <Button size="lg" variant="ghost">
                    {t('common:actions.signIn')}
                  </Button>
                </Link>
              </div>
              <div className="hero-meta mt-5 flex flex-wrap items-center gap-x-5 gap-y-2 text-[12px] text-[var(--color-fg-subtle)]">
                <span>{t('common:common.free')}</span>
                <span aria-hidden>·</span>
                <span>{t('common:common.noCard')}</span>
                <span aria-hidden>·</span>
                <span>{t('common:common.openAnytime')}</span>
              </div>
            </div>

            {/* The product itself is the hero: a live, looping demo that streams
              its replies and switches models mid-thread. On a faint dot-grid. */}
            <div className="hero-preview relative min-w-0 will-change-transform lg:-mr-6">
              <div
                aria-hidden
                className="mask-fade-bottom pointer-events-none absolute -inset-x-6 -inset-y-8 -z-10"
                style={{
                  backgroundImage:
                    'radial-gradient(circle, color-mix(in oklch, var(--color-fg) 8%, transparent) 1px, transparent 1px)',
                  backgroundSize: '16px 16px',
                }}
              />
              {/* Hovering the demo sweeps a soft light across its glass — pairs
                with the pointer tilt above. Shadow lives on the wrapper because
                its overflow clip would swallow the card's own (§ welcome fx). */}
              <GlareHover
                className="rounded-[18px] shadow-[var(--shadow-2xl)]"
                glareOpacity={0.09}
                glareSize={260}
                transitionDuration={800}
              >
                <LiveDemo />
              </GlareHover>
            </div>
          </div>
        </section>

        {/* Model band — two counter-drifting serif rails of the frontier models
          Aivory convenes; their speed answers the user's scroll velocity, so
          the page physically reacts to being read (§ welcome fx). Edge-faded,
          static under reduced-motion. */}
        <div
          className="relative overflow-hidden border-y border-[var(--color-divider)] py-8"
          aria-hidden
          style={{
            maskImage: 'linear-gradient(90deg, transparent, #000 10%, #000 90%, transparent)',
            WebkitMaskImage: 'linear-gradient(90deg, transparent, #000 10%, #000 90%, transparent)',
          }}
        >
          <ScrollVelocity
            texts={[
              MODELS.map((m) => m.name).join('  ·  ') + '  ·  ',
              MODELS.map((m) => m.maker).join('  ·  ') + '  ·  ',
            ]}
            velocity={70}
            className="px-4 font-serif text-4xl sm:text-6xl tracking-tight text-[var(--color-fg)] opacity-[0.14]"
          />
        </div>

        {/* Capabilities */}
        <section id="capabilities" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
          <div className="mx-auto max-w-[76rem] px-5 sm:px-8">
            <SectionHeader
              title={t('landing:capabilities.title')}
              body={t('landing:capabilities.body')}
            />
            {/* Living cards: each capability opens with a token-drawn scene that
              plays its story on loop — prose writing itself, sources converging
              on a claim, code being read — over the shared spotlight hover
              (§ welcome fx). */}
            <div className="mt-14 grid gap-5 md:grid-cols-3" data-reveal-group>
              {CAPABILITY_KEYS.map((c) => (
                <SpotlightCard
                  key={c.key}
                  spotlightColor="color-mix(in oklch, var(--color-accent) 10%, transparent)"
                  className="group rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] transition-transform duration-300 hover:-translate-y-1 hover:shadow-[var(--shadow-md)]"
                >
                  <div className="h-44 overflow-hidden border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)]/60">
                    <c.Scene />
                  </div>
                  <div className="p-6 sm:p-7">
                    <div className="flex items-center gap-3">
                      <span className="inline-flex size-9 items-center justify-center rounded-[10px] bg-[var(--color-accent-soft)] text-[var(--color-accent)] transition-transform duration-300 group-hover:-translate-y-0.5">
                        <c.icon size={16} aria-hidden />
                      </span>
                      <h3 className="font-serif text-xl tracking-tight text-[var(--color-fg)]">
                        {t(`landing:capabilities.items.${c.key}.title`)}
                      </h3>
                    </div>
                    <p className="mt-3.5 text-[14.5px] text-[var(--color-fg-muted)] leading-relaxed text-pretty">
                      {t(`landing:capabilities.items.${c.key}.body`)}
                    </p>
                  </div>
                </SpotlightCard>
              ))}
            </div>
          </div>
        </section>

        {/* How it feels */}
        <section id="how" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
          <div className="mx-auto max-w-[76rem] px-5 sm:px-8 grid grid-cols-1 lg:grid-cols-2 gap-12 lg:gap-20 items-start">
            <div data-reveal>
              <h2 className="font-serif tracking-tight text-3xl sm:text-4xl text-[var(--color-fg)] text-balance">
                <ScrollFloat text={t('landing:how.title')} />
              </h2>
              <p className="mt-5 text-[var(--color-fg-muted)] leading-relaxed text-pretty">
                {t('landing:how.body')}
              </p>
              {/* Editorial numbering: an oversized ghost numeral anchors each
                principle; the row lights and lifts on hover (§ welcome fx). */}
              <ul className="mt-10 space-y-2" data-reveal-group>
                {PRINCIPLE_KEYS.map((p, i) => (
                  <li key={p}>
                    <SpotlightCard
                      spotlightColor="color-mix(in oklch, var(--color-accent) 8%, transparent)"
                      className="group flex items-start gap-4 rounded-xl px-4 py-4 transition-colors duration-300 hover:bg-[var(--color-surface)]"
                    >
                      <span
                        aria-hidden
                        className="w-10 shrink-0 select-none font-serif text-[2rem] leading-none tracking-tight text-[color-mix(in_oklch,var(--color-accent)_38%,transparent)] transition-colors duration-300 group-hover:text-[var(--color-accent)]"
                      >
                        {String(i + 1).padStart(2, '0')}
                      </span>
                      <div>
                        <div className="font-medium text-[var(--color-fg)]">
                          <BlurText text={t(`landing:how.principles.${p}.title`)} delay={70} />
                        </div>
                        <div className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
                          {t(`landing:how.principles.${p}.body`)}
                        </div>
                      </div>
                    </SpotlightCard>
                  </li>
                ))}
              </ul>
            </div>
            <div data-reveal>
              <PullQuote />
            </div>
          </div>
        </section>

        {/* Use cases */}
        <section className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
          <div className="mx-auto max-w-[76rem] px-5 sm:px-8">
            <SectionHeader title={t('landing:useCases.title')} />
            {/* Numbered gallery cards: an oversized ghost numeral fills each
              card's corner, the icon chip lifts on hover, and every term
              drifts into focus as it scrolls in (§ welcome fx). */}
            <dl className="mt-12 grid grid-cols-1 gap-5 sm:grid-cols-2" data-reveal-group>
              {USE_CASE_KEYS.map(({ key, icon: Icon }, i) => (
                <SpotlightCard
                  key={key}
                  spotlightColor="color-mix(in oklch, var(--color-accent) 9%, transparent)"
                  className="group rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 sm:p-7 transition-transform duration-300 hover:-translate-y-1 hover:shadow-[var(--shadow-md)]"
                >
                  <span
                    aria-hidden
                    className="pointer-events-none absolute -top-7 right-1 select-none font-serif text-[7rem] leading-none tracking-tight text-[color-mix(in_oklch,var(--color-fg)_6%,transparent)] transition-colors duration-500 group-hover:text-[color-mix(in_oklch,var(--color-accent)_16%,transparent)]"
                  >
                    {String(i + 1).padStart(2, '0')}
                  </span>
                  <span className="relative inline-flex size-10 items-center justify-center rounded-[11px] bg-[var(--color-accent-soft)] text-[var(--color-accent)] transition-transform duration-300 group-hover:-translate-y-0.5">
                    <Icon size={17} aria-hidden />
                  </span>
                  <dt className="mt-4 font-serif text-xl tracking-tight text-[var(--color-fg)]">
                    <span className="relative inline-block">
                      {/* Each entry's term drifts into focus as it scrolls in. */}
                      <BlurText text={t(`landing:useCases.items.${key}.title`)} delay={80} />
                      <span
                        aria-hidden
                        className="absolute -bottom-1 left-0 h-px w-full origin-left scale-x-0 bg-[var(--color-accent)] transition-transform duration-300 group-hover:scale-x-100"
                      />
                    </span>
                  </dt>
                  <dd className="relative mt-2.5 max-w-[46ch] text-sm text-[var(--color-fg-muted)] leading-relaxed text-pretty">
                    {t(`landing:useCases.items.${key}.body`)}
                  </dd>
                </SpotlightCard>
              ))}
            </dl>
          </div>
        </section>

        {/* Models — 汇集主流大模型: a type-specimen rail of the real mainstream
          models, each logo painted in the product's own ink (CSS mask). */}
        <section id="models" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
          <div className="mx-auto max-w-[76rem] px-5 sm:px-8">
            <SectionHeader
              eyebrow={t('landing:models.eyebrow')}
              title={t('landing:models.title')}
              body={t('landing:models.body')}
            />
            {/* The section's facts, counted up as they scroll into view — serif
              numerals on a ruled band, no card chrome (§ welcome fx). */}
            <div
              className="mt-12 grid grid-cols-3 divide-x divide-[var(--color-divider)] border-y border-[var(--color-divider)]"
              data-reveal
            >
              {([
                ['models', MODELS.length],
                ['languages', 5],
                ['subscription', 1],
              ] as const).map(([k, n]) => (
                <div key={k} className="flex flex-col items-center gap-1.5 px-3 py-7 text-center">
                  <span className="font-serif text-4xl sm:text-5xl tracking-tight tabular-nums text-[var(--color-fg)]">
                    <CountUp to={n} duration={1.4} />
                  </span>
                  <span className="font-mono text-[10.5px] uppercase tracking-wider text-[var(--color-fg-subtle)]">
                    {t(`landing:models.stats.${k}`)}
                  </span>
                </div>
              ))}
            </div>
            <ul
              className="mt-10 grid grid-cols-2 sm:grid-cols-4 gap-px overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-border)]"
              data-reveal-group
            >
              {MODELS.map((m) => (
                <li key={m.key} className="group/m bg-[var(--color-bg)]">
                  <SpotlightCard
                    spotlightColor="color-mix(in oklch, var(--color-accent) 9%, transparent)"
                    className="flex h-full flex-col items-center gap-3 px-4 py-8 text-center transition-transform duration-300 hover:-translate-y-0.5"
                  >
                    <span
                      aria-hidden
                      className={cn(
                        'brand-mark text-[var(--color-fg-muted)] transition-colors duration-300 group-hover/m:text-[var(--color-fg)]',
                        m.lead ? 'size-9' : 'size-7',
                      )}
                      style={{ WebkitMaskImage: `url(/brand/${m.slug}.svg)`, maskImage: `url(/brand/${m.slug}.svg)` }}
                    />
                    <span className={cn('font-serif tracking-tight text-[var(--color-fg)]', m.lead ? 'text-xl' : 'text-lg')}>
                      {m.name}
                    </span>
                    <span className="font-mono text-[10.5px] uppercase tracking-wider text-[var(--color-fg-subtle)]">
                      {m.maker}
                    </span>
                    <span
                      aria-hidden
                      className="absolute bottom-0 left-1/2 h-px w-10 -translate-x-1/2 origin-center scale-x-0 bg-[var(--color-accent)] transition-transform duration-300 group-hover/m:scale-x-100"
                    />
                  </SpotlightCard>
                </li>
              ))}
            </ul>
            <div
              className="mt-12 grid grid-cols-1 border-t border-[var(--color-divider)] divide-y divide-[var(--color-divider)] sm:grid-cols-3 sm:divide-y-0 sm:divide-x"
              data-reveal-group
            >
              {(['switch', 'strength', 'oneSub'] as const).map((k) => (
                <p
                  key={k}
                  className="px-1 py-6 font-serif text-lg tracking-tight text-balance text-[var(--color-fg)] sm:px-6"
                >
                  {t(`landing:models.proof.${k}`)}
                </p>
              ))}
            </div>
            <p className="mt-6 text-[12px] text-[var(--color-fg-subtle)]">{t('landing:models.footnote')}</p>
          </div>
        </section>

        {/* Membership tiers (§ user groups) — public, fetched from the open endpoint. */}
        <MembershipTiers />

        {/* Safety */}
        <section id="safety" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
          <div className="mx-auto max-w-[76rem] px-5 sm:px-8 grid grid-cols-1 md:grid-cols-2 gap-12 items-start">
            <div data-reveal>
              <Badge variant="neutral">{t('landing:safety.eyebrow')}</Badge>
              <h2 className="mt-5 font-serif tracking-tight text-3xl sm:text-4xl text-[var(--color-fg)] text-balance">
                <ScrollFloat text={t('landing:safety.title')} />
              </h2>
              <p className="mt-5 text-[var(--color-fg-muted)] leading-relaxed">
                {t('landing:safety.body')}
              </p>
            </div>
            <ul className="space-y-3" data-reveal-group>
              <SafetyRow icon={Lock} title={t('landing:safety.rows.noTraining.title')} body={t('landing:safety.rows.noTraining.body')} />
              <SafetyRow icon={Cloud} title={t('landing:safety.rows.export.title')} body={t('landing:safety.rows.export.body')} />
              <SafetyRow icon={Sparkles} title={t('landing:safety.rows.memory.title')} body={t('landing:safety.rows.memory.body')} />
            </ul>
          </div>
        </section>

        {/* CTA — a drifting particle field behind the final ask; motes lean
          toward the pointer (§ welcome fx). isolate scopes the -z layer. */}
        <section className="relative isolate overflow-hidden py-28 sm:py-36">
          <div
            aria-hidden
            className="pointer-events-none absolute inset-0 -z-10"
            style={{
              maskImage: 'radial-gradient(90% 90% at 50% 50%, #000 55%, transparent 100%)',
              WebkitMaskImage: 'radial-gradient(90% 90% at 50% 50%, #000 55%, transparent 100%)',
            }}
          >
            <Particles
              particleCount={130}
              particleSpread={12}
              speed={0.07}
              particleBaseSize={80}
              moveParticlesOnHover
              particleHoverFactor={1.4}
            />
          </div>
          <div className="mx-auto max-w-[60rem] px-5 sm:px-8 text-center" data-reveal>
            <h2 className="font-serif tracking-tight text-balance text-3xl sm:text-5xl leading-[1.05] text-[var(--color-fg)]">
              {/* A slow clay glint pans through the closing line — mostly ink,
                one warm pass; static under reduced-motion (§ welcome fx). */}
              <GradientText
                colors={['var(--color-fg)', 'var(--color-accent)', 'var(--color-fg)']}
                animationSpeed={9}
              >
                {t('landing:cta.title')}
              </GradientText>
            </h2>
            <p className="mx-auto mt-6 max-w-md text-[var(--color-fg-muted)]">
              {t('landing:cta.body')}
            </p>
            <div className="mt-9 flex items-center justify-center gap-3 flex-wrap">
              {/* The final ask earns the page's one orbiting glow (§ welcome fx). */}
              <StarBorder as="span" className="rounded-[12px]" speed="7s" thickness={2}>
                <Link to="/chat" className="inline-block">
                  <Button size="lg" trailingIcon={<ArrowRight size={15} aria-hidden />}>
                    {t('landing:cta.primary')}
                  </Button>
                </Link>
              </StarBorder>
              <Link to="/register">
                <Button size="lg" variant="ghost">
                  {t('common:actions.signUp')}
                </Button>
              </Link>
            </div>
          </div>
        </section>

        {/* Footer */}
        <footer className="border-t border-[var(--color-divider)] py-14">
          <div className="mx-auto max-w-[76rem] px-5 sm:px-8 grid grid-cols-2 md:grid-cols-5 gap-10" data-reveal>
            <div className="col-span-2">
              <Logo size="md" />
              <p className="mt-3 max-w-xs text-sm leading-relaxed">
                {/* The sign-off breathes the same slow sheen as the hero badge. */}
                <ShinyText text={t('landing:footer.tagline')} speed={6} />
              </p>
            </div>
            <FooterCol
              title={t('nav:product')}
              links={[
                [t('common:actions.openAivory'), '/chat'],
                [t('common:actions.settings'), '/settings/account'],
              ]}
            />
            <FooterCol
              title={t('nav:company')}
              links={[
                [t('common:actions.signIn'), '/login'],
                [t('common:actions.signUp'), '/register'],
              ]}
            />
            <FooterCol
              title={t('nav:legal')}
              links={[
                [t('nav:privacy'), '/privacy'],
                [t('nav:terms'), '/terms'],
              ]}
            />
          </div>
          <div className="mx-auto max-w-[76rem] px-5 sm:px-8 mt-10 text-xs text-[var(--color-fg-subtle)]">
            <span>© {new Date().getFullYear()} Aivory</span>
          </div>
        </footer>

        {/* Scroll-to-top — visibility driven by ScrollTrigger (see useGSAP). */}
        <button
          ref={topBtn}
          type="button"
          onClick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
          aria-label={t('common:aria.backToTop')}
          className={cn(
            'fixed bottom-6 right-6 z-30 inline-flex items-center justify-center size-10 rounded-full',
            'bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-lg)]',
            'hover:opacity-90 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          )}
        >
          <ArrowUp size={16} aria-hidden />
        </button>
      </ClickSpark>
    </div>
  )
}

function SectionHeader({ eyebrow, title, body }: { eyebrow?: string; title: string; body?: string }) {
  return (
    <div className="max-w-2xl" data-reveal>
      {eyebrow && <Badge variant="neutral">{eyebrow}</Badge>}
      {/* Every section title floats in per-character, scrubbed by the scroll
          bar — the page stays alive below the fold (§ welcome fx). */}
      <h2
        className={cn(
          'font-serif tracking-tight text-3xl sm:text-4xl text-[var(--color-fg)] text-balance',
          eyebrow && 'mt-5',
        )}
      >
        <ScrollFloat text={title} />
      </h2>
      {body && <p className="mt-5 text-[var(--color-fg-muted)] leading-relaxed text-pretty">{body}</p>}
    </div>
  )
}

function SafetyRow({ icon: Icon, title, body }: { icon: LucideIcon; title: string; body: string }) {
  return (
    <li>
      {/* Same spotlight hover as the ledger rows/tiles — the card chrome sits
          on the wrapper so the glow clips to its radius (§ welcome fx). */}
      <SpotlightCard
        spotlightColor="color-mix(in oklch, var(--color-accent) 11%, transparent)"
        className="flex items-start gap-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5"
      >
        <span className="inline-flex size-9 items-center justify-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)] shrink-0">
          <Icon size={15} aria-hidden />
        </span>
        <div>
          <div className="font-medium text-[var(--color-fg)]">
            <BlurText text={title} delay={80} />
          </div>
          <div className="text-sm text-[var(--color-fg-muted)] leading-relaxed">{body}</div>
        </div>
      </SpotlightCard>
    </li>
  )
}

function FooterCol({ title, links }: { title: string; links: [string, string][] }) {
  return (
    <div>
      <h4 className="text-[12px] uppercase tracking-wider text-[var(--color-fg-subtle)]">{title}</h4>
      <ul className="mt-4 space-y-2.5 text-sm">
        {links.map(([label, href]) => (
          <li key={label}>
            <Link to={href} className="text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive">
              {label}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  )
}

function PullQuote() {
  const { t } = useTranslation('landing')
  return (
    // The quote card shares the ledger rows' spotlight hover (§ welcome fx);
    // the card chrome moved onto the wrapper so the glow clips to its radius.
    <SpotlightCard
      spotlightColor="color-mix(in oklch, var(--color-accent) 10%, transparent)"
      className="rounded-xl bg-[var(--color-bg-muted)]"
    >
      <figure className="relative px-8 py-10 sm:px-10 sm:py-12">
        {/* Oversized recessed quotation mark — sets the editorial register. */}
        <span
          aria-hidden
          className="pointer-events-none absolute -top-6 left-5 select-none font-serif text-[8rem] leading-none"
          style={{ color: 'color-mix(in oklch, var(--color-accent) 16%, transparent)' }}
        >
          &ldquo;
        </span>
        <blockquote className="relative font-serif text-[1.5rem] sm:text-[1.75rem] leading-[1.34] tracking-tight text-pretty text-[var(--color-fg)]">
          {/* Reading-pace reveal: each word sharpens from a blur as the scroll
            bar sweeps the quote — the page reads it with you (§ welcome fx). */}
          <ScrollReveal text={t('how.quote')} />
        </blockquote>
        <figcaption className="relative mt-7 flex items-center gap-3 text-sm text-[var(--color-fg-muted)]">
          <span className="inline-grid size-7 place-items-center rounded-full bg-[var(--color-accent-soft)] font-mono text-[var(--color-accent)]">A</span>
          <span>{t('how.quoteSource')}</span>
        </figcaption>
      </figure>
    </SpotlightCard>
  )
}

function BackgroundOrnament() {
  return (
    <div aria-hidden className="pointer-events-none absolute inset-0 -z-10 overflow-hidden">
      <div
        className="orb-1 absolute -top-40 left-1/2 -translate-x-1/2 size-[640px] rounded-full opacity-50 blur-3xl will-change-transform"
        style={{
          background: 'radial-gradient(closest-side, color-mix(in oklch, var(--color-accent-soft) 80%, transparent), transparent 70%)',
        }}
      />
      <div
        className="orb-2 absolute top-[420px] -left-32 size-[420px] rounded-full opacity-50 blur-3xl will-change-transform"
        style={{
          background: 'radial-gradient(closest-side, color-mix(in oklch, var(--color-secondary-soft) 60%, transparent), transparent 70%)',
        }}
      />
      <div
        className="orb-3 absolute top-[1100px] right-[-10rem] size-[520px] rounded-full opacity-40 blur-3xl will-change-transform"
        style={{
          background: 'radial-gradient(closest-side, color-mix(in oklch, var(--color-accent-soft) 55%, transparent), transparent 70%)',
        }}
      />
      {/* Warm film grain — premium texture over the gradient field. */}
      <div className="grain-overlay absolute inset-0 opacity-[0.04] mix-blend-soft-light" />
    </div>
  )
}
