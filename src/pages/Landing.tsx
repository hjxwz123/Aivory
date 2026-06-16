import { useEffect, useRef } from 'react'
import { Link, useNavigate } from 'react-router-dom'
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
  Check,
  Quote,
  ArrowUp,
  type LucideIcon,
} from 'lucide-react'
import { Logo } from '@/components/brand/logo'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { Composer } from '@/components/chat/composer'
import { useConversations } from '@/store/conversations'
import { useModels } from '@/store/models'
import { useTheme } from '@/store/theme'
import { useTranslation, Trans } from 'react-i18next'
import { cn } from '@/lib/utils'

gsap.registerPlugin(ScrollTrigger, useGSAP)

const CAPABILITY_KEYS = [
  { icon: BookOpen, key: 'writing' },
  { icon: Telescope, key: 'research' },
  { icon: Code2, key: 'code' },
] as const

const USE_CASE_KEYS = ['writers', 'researchers', 'engineers', 'thinkers'] as const

const PRINCIPLE_KEYS = ['noDefaults', 'readingFirst', 'youOwnIt'] as const

export default function Landing() {
  const navigate = useNavigate()
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation(['landing', 'common', 'nav'])
  // Pull only the actions to avoid re-rendering Landing on every streaming token.
  const createConversation = useConversations((s) => s.createConversation)
  const sendMessage = useConversations((s) => s.sendMessage)
  const defaultModelId = useModels((s) => s.defaultId)

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
          .from('.hero-sub', { y: 16, autoAlpha: 0, duration: 0.7 }, '-=0.5')
          .from('.hero-composer', { y: 22, autoAlpha: 0, duration: 0.7 }, '-=0.45')
          .from('.hero-meta', { y: 10, autoAlpha: 0, duration: 0.6 }, '-=0.5')
          .from('.hero-preview', { y: 48, autoAlpha: 0, duration: 1.0 }, '-=0.35')

        // Nav backdrop fades in over the first bit of scroll.
        gsap.fromTo('.nav-bg', { autoAlpha: 0 }, {
          autoAlpha: 1, ease: 'none',
          scrollTrigger: { start: 8, end: 96, scrub: true },
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

        // Depth: the soft background orbs drift at different rates on scroll.
        gsap.to('.orb-1', { yPercent: 22, ease: 'none', scrollTrigger: { start: 'top top', end: 'max', scrub: true } })
        gsap.to('.orb-2', { yPercent: -16, ease: 'none', scrollTrigger: { start: 'top top', end: 'max', scrub: true } })
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

  async function handleHeroSubmit(text: string) {
    const conv = await createConversation(defaultModelId)
    if (!conv) return
    navigate(`/chat/${conv.id}`)
    void sendMessage({ conversationId: conv.id, text, modelId: conv.modelId || defaultModelId })
  }

  return (
    <div ref={root} className="relative min-h-svh overflow-x-clip bg-[var(--color-bg)] text-[var(--color-fg)]">
      {/* Background accents */}
      <BackgroundOrnament />

      {/* Nav */}
      <header className="sticky top-0 z-40 backdrop-blur-[1px]">
        <div
          aria-hidden
          className="nav-bg absolute inset-0 -z-10 bg-[var(--color-bg)]/85 border-b border-[var(--color-border-subtle)]"
        />
        <div className="mx-auto max-w-[76rem] flex items-center justify-between px-5 sm:px-8 h-[64px]">
          <Link to="/" aria-label={t('common:aria.homeLink')}>
            <Logo size="md" />
          </Link>
          <nav className="hidden md:flex items-center gap-7 text-sm text-[var(--color-fg-muted)]">
            <a href="#capabilities" className="hover:text-[var(--color-fg)] interactive">{t('nav:capabilities')}</a>
            <a href="#how" className="hover:text-[var(--color-fg)] interactive">{t('nav:howItFeels')}</a>
            <a href="#models" className="hover:text-[var(--color-fg)] interactive">{t('nav:models')}</a>
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
                {t('common:actions.openAurelia')}
              </Button>
            </Link>
          </div>
        </div>
      </header>

      {/* Hero */}
      <section className="relative pt-20 sm:pt-32 pb-20 sm:pb-28">
        <div className="mx-auto max-w-[68rem] px-5 sm:px-8">
          <div className="text-center">
            <div className="hero-badge inline-block">
              <Badge variant="sage" leadingIcon={<Sparkles size={11} aria-hidden />} className="mb-7">
                {t('landing:badgeNew')}
              </Badge>
            </div>
            {/* Headline lines rise out of an overflow mask (per-line reveal). */}
            <h1 className="font-serif tracking-tight text-balance text-[2.5rem] sm:text-[3.75rem] lg:text-[4.5rem] leading-[1.04] text-[var(--color-fg)]">
              <span className="block overflow-hidden pb-[0.08em]">
                <span className="hero-line block">{t('landing:hero.titleLine1')}</span>
              </span>
              <span className="block overflow-hidden pb-[0.08em]">
                <span className="hero-line block italic text-[var(--color-fg-muted)]">{t('landing:hero.titleLine2')}</span>
              </span>
            </h1>
            <p className="hero-sub mx-auto mt-7 max-w-xl text-[var(--color-fg-muted)] text-pretty text-[15px] sm:text-base leading-relaxed">
              {t('landing:hero.subtitle')}
            </p>
          </div>

          <div className="hero-composer mx-auto mt-10 max-w-[36rem]">
            <Composer
              modelId={defaultModelId}
              onModelChange={() => { /* hero composer ignores; real switch happens in /chat */ }}
              onSubmit={(text) => void handleHeroSubmit(text)}
              placeholder={t('landing:hero.placeholder')}
            />
            <div className="hero-meta mt-3 flex items-center justify-center gap-x-5 gap-y-2 text-[12px] text-[var(--color-fg-subtle)] flex-wrap">
              <span>{t('common:common.free')}</span>
              <span aria-hidden>·</span>
              <span>{t('common:common.noCard')}</span>
              <span aria-hidden>·</span>
              <span>{t('common:common.openAnytime')}</span>
            </div>
          </div>
        </div>

        {/* Floating product preview */}
        <div className="hero-preview mx-auto mt-20 max-w-[60rem] px-5 sm:px-8 will-change-transform">
          <ProductPreview />
        </div>
      </section>

      {/* Capabilities */}
      <section id="capabilities" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
        <div className="mx-auto max-w-[76rem] px-5 sm:px-8">
          <SectionHeader
            eyebrow={t('landing:capabilities.eyebrow')}
            title={t('landing:capabilities.title')}
            body={t('landing:capabilities.body')}
          />
          <div className="mt-14 grid grid-cols-1 md:grid-cols-3 gap-6" data-reveal-group>
            {CAPABILITY_KEYS.map((c) => (
              <div
                key={c.key}
                className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 transition-[transform,border-color,box-shadow] duration-300 hover:-translate-y-0.5 hover:border-[var(--color-border-strong)] hover:shadow-[var(--shadow-sm)]"
              >
                <div className="inline-flex size-9 items-center justify-center rounded-[10px] bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
                  <c.icon size={16} aria-hidden />
                </div>
                <h3 className="mt-5 font-serif text-xl tracking-tight text-[var(--color-fg)]">
                  {t(`landing:capabilities.items.${c.key}.title`)}
                </h3>
                <p className="mt-2 text-sm text-[var(--color-fg-muted)] leading-relaxed">
                  {t(`landing:capabilities.items.${c.key}.body`)}
                </p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* How it feels */}
      <section id="how" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
        <div className="mx-auto max-w-[76rem] px-5 sm:px-8 grid grid-cols-1 lg:grid-cols-2 gap-12 lg:gap-20 items-start">
          <div data-reveal>
            <Badge variant="neutral">{t('landing:how.eyebrow')}</Badge>
            <h2 className="mt-5 font-serif tracking-tight text-3xl sm:text-4xl text-[var(--color-fg)] text-balance">
              {t('landing:how.title')}
            </h2>
            <p className="mt-5 text-[var(--color-fg-muted)] leading-relaxed text-pretty">
              {t('landing:how.body')}
            </p>
            <ul className="mt-10 space-y-4">
              {PRINCIPLE_KEYS.map((p) => (
                <li key={p} className="flex items-start gap-3">
                  <span className="mt-1 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-success-soft)] text-[var(--color-success)] shrink-0">
                    <Check size={11} aria-hidden />
                  </span>
                  <div>
                    <div className="font-medium text-[var(--color-fg)]">
                      {t(`landing:how.principles.${p}.title`)}
                    </div>
                    <div className="text-sm text-[var(--color-fg-muted)]">
                      {t(`landing:how.principles.${p}.body`)}
                    </div>
                  </div>
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
          <SectionHeader eyebrow={t('landing:useCases.eyebrow')} title={t('landing:useCases.title')} />
          <div className="mt-12 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-5" data-reveal-group>
            {USE_CASE_KEYS.map((key, i) => (
              <div
                key={key}
                className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5 min-h-[180px] flex flex-col"
              >
                <span className="text-[var(--color-fg-subtle)] font-mono text-xs">0{i + 1}</span>
                <h3 className="mt-3 font-serif text-lg tracking-tight text-[var(--color-fg)]">
                  {t(`landing:useCases.items.${key}.title`)}
                </h3>
                <p className="mt-2 text-sm text-[var(--color-fg-muted)] leading-relaxed">
                  {t(`landing:useCases.items.${key}.body`)}
                </p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Models */}
      <section id="models" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
        <div className="mx-auto max-w-[76rem] px-5 sm:px-8">
          <SectionHeader
            eyebrow={t('landing:models.eyebrow')}
            title={t('landing:models.title')}
            body={t('landing:models.body')}
          />
          <div className="mt-14 grid grid-cols-1 md:grid-cols-2 gap-5" data-reveal-group>
            <ModelCard name="Aurelia Prose" tier={t('landing:models.tiers.standard')} body="Fast, conversational. The everyday model." accent="sage" />
            <ModelCard name="Aurelia Reason" tier={t('landing:models.tiers.pro')} body="Deliberate, structured analysis. For research and code." accent="clay" />
            <ModelCard name="Aurelia Canvas" tier={t('landing:models.tiers.pro')} body="Iterative editing of long-form writing and code." accent="sage" />
            <ModelCard name="Aurelia Deep" tier={t('landing:models.tiers.max')} body="Long deliberation with citations and tool use." accent="clay" locked lockedNote={t('landing:models.lockedNote')} />
          </div>
        </div>
      </section>

      {/* Safety */}
      <section id="safety" className="py-24 sm:py-32 border-t border-[var(--color-divider)]">
        <div className="mx-auto max-w-[76rem] px-5 sm:px-8 grid grid-cols-1 md:grid-cols-2 gap-12 items-start">
          <div data-reveal>
            <Badge variant="neutral">{t('landing:safety.eyebrow')}</Badge>
            <h2 className="mt-5 font-serif tracking-tight text-3xl sm:text-4xl text-[var(--color-fg)] text-balance">
              {t('landing:safety.title')}
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

      {/* CTA */}
      <section className="py-28 sm:py-36">
        <div className="mx-auto max-w-[60rem] px-5 sm:px-8 text-center" data-reveal>
          <h2 className="font-serif tracking-tight text-balance text-3xl sm:text-5xl leading-[1.05] text-[var(--color-fg)]">
            {t('landing:cta.title')}
          </h2>
          <p className="mx-auto mt-6 max-w-md text-[var(--color-fg-muted)]">
            {t('landing:cta.body')}
          </p>
          <div className="mt-9 flex items-center justify-center gap-3 flex-wrap">
            <Link to="/chat">
              <Button size="lg" trailingIcon={<ArrowRight size={15} aria-hidden />}>
                {t('landing:cta.primary')}
              </Button>
            </Link>
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
        <div className="mx-auto max-w-[76rem] px-5 sm:px-8 grid grid-cols-2 md:grid-cols-5 gap-10">
          <div className="col-span-2">
            <Logo size="md" />
            <p className="mt-3 max-w-xs text-sm text-[var(--color-fg-muted)] leading-relaxed">
              {t('landing:footer.tagline')}
            </p>
          </div>
          <FooterCol
            title={t('nav:product')}
            links={[
              [t('common:actions.openAurelia'), '/chat'],
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
              [t('nav:privacy'), '#'],
              [t('nav:terms'), '#'],
            ]}
          />
        </div>
        <div className="mx-auto max-w-[76rem] px-5 sm:px-8 mt-10 text-xs text-[var(--color-fg-subtle)]">
          <span>© {new Date().getFullYear()} Aurelia</span>
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
    </div>
  )
}

function SectionHeader({ eyebrow, title, body }: { eyebrow: string; title: string; body?: string }) {
  return (
    <div className="max-w-2xl" data-reveal>
      <Badge variant="neutral">{eyebrow}</Badge>
      <h2 className="mt-5 font-serif tracking-tight text-3xl sm:text-4xl text-[var(--color-fg)] text-balance">
        {title}
      </h2>
      {body && <p className="mt-5 text-[var(--color-fg-muted)] leading-relaxed text-pretty">{body}</p>}
    </div>
  )
}

function ModelCard({
  name,
  tier,
  body,
  accent,
  locked,
  lockedNote,
}: {
  name: string
  tier: string
  body: string
  accent: 'clay' | 'sage'
  locked?: boolean
  lockedNote?: string
}) {
  // Map tier label → badge variant by accent prop (locale-agnostic).
  const tierVariant = accent === 'clay' && locked ? 'accent' : accent === 'clay' ? 'sage' : 'sage'
  return (
    <div className="group/m rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 flex flex-col">
      <div className="flex items-center gap-2">
        <Sparkles
          size={14}
          className={cn(accent === 'clay' ? 'text-[var(--color-accent)]' : 'text-[var(--color-secondary)]')}
          aria-hidden
        />
        <span className="font-serif text-xl tracking-tight text-[var(--color-fg)]">{name}</span>
        <Badge variant={locked ? 'accent' : tierVariant} size="xs" className="ml-auto">
          {tier}
        </Badge>
      </div>
      <p className="mt-2.5 text-sm text-[var(--color-fg-muted)] leading-relaxed">{body}</p>
      {locked && lockedNote && (
        <span className="mt-4 inline-flex items-center gap-1.5 text-xs text-[var(--color-fg-subtle)]">
          <Lock size={11} aria-hidden /> {lockedNote}
        </span>
      )}
    </div>
  )
}

function SafetyRow({ icon: Icon, title, body }: { icon: LucideIcon; title: string; body: string }) {
  return (
    <li className="flex items-start gap-3 rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
      <span className="inline-flex size-9 items-center justify-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)] shrink-0">
        <Icon size={15} aria-hidden />
      </span>
      <div>
        <div className="font-medium text-[var(--color-fg)]">{title}</div>
        <div className="text-sm text-[var(--color-fg-muted)] leading-relaxed">{body}</div>
      </div>
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
    <figure className="relative rounded-3xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8 sm:p-10">
      <Quote size={20} className="text-[var(--color-accent)]" aria-hidden />
      <blockquote className="mt-5 font-serif text-[1.5rem] sm:text-[1.75rem] leading-[1.32] tracking-tight text-[var(--color-fg)]">
        {t('how.quote')}
      </blockquote>
      <figcaption className="mt-6 flex items-center gap-3 text-sm text-[var(--color-fg-muted)]">
        <span className="inline-block size-7 rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)] grid place-items-center font-mono">A</span>
        <span>{t('how.quoteSource')}</span>
      </figcaption>
    </figure>
  )
}

function ProductPreview() {
  const { t } = useTranslation('landing')
  return (
    <div
      className="relative rounded-[26px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-2xl)] overflow-hidden"
      aria-hidden
    >
      {/* Window chrome */}
      <div className="flex items-center gap-1.5 px-4 h-9 border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)]">
        <span className="size-2.5 rounded-full bg-[var(--color-fg-faint)]" />
        <span className="size-2.5 rounded-full bg-[var(--color-fg-faint)]" />
        <span className="size-2.5 rounded-full bg-[var(--color-fg-faint)]" />
        <span className="mx-auto text-[11px] text-[var(--color-fg-subtle)] font-mono">aurelia.app/chat</span>
      </div>
      <div className="grid grid-cols-[200px_1fr] min-h-[420px]">
        {/* Mini sidebar */}
        <div className="border-r border-[var(--color-divider)] bg-[var(--color-bg-muted)]/60 p-3 max-md:hidden">
          <Logo size="sm" />
          <div className="mt-5 space-y-1">
            <MiniNavRow label={t('preview.essayTitle')} active />
            <MiniNavRow label={t('preview.readingList')} />
          </div>
          <div className="mt-5">
            <div className="text-[10px] uppercase tracking-wider text-[var(--color-fg-subtle)] mb-1.5 px-2">
              {t('preview.today')}
            </div>
            <MiniNavRow label={t('preview.essayTitle')} />
            <MiniNavRow label={t('preview.readingList')} />
          </div>
          <div className="mt-5">
            <div className="text-[10px] uppercase tracking-wider text-[var(--color-fg-subtle)] mb-1.5 px-2">
              {t('preview.yesterday')}
            </div>
            <MiniNavRow label={t('preview.newsletterName')} />
          </div>
        </div>
        {/* Mini conversation */}
        <div className="p-6 sm:p-8 flex flex-col">
          <div className="flex items-center gap-2 mb-5">
            <span className="size-6 rounded-full bg-[var(--color-secondary-soft)] text-[var(--color-secondary)] inline-flex items-center justify-center text-[11px] font-medium">
              A
            </span>
            <span className="font-serif text-[15px] tracking-tight text-[var(--color-fg)]">Aurelia</span>
          </div>
          <p className="text-[var(--color-fg)] leading-relaxed text-[14px] max-w-[36rem]">
            <Trans
              i18nKey="preview.essayLead"
              t={t}
              values={{ topic: t('preview.essayTopic') }}
              components={{ italic: <em /> }}
            />
          </p>
          <p className="mt-3 text-[var(--color-fg)] leading-relaxed text-[14px] max-w-[36rem]">
            <span className="font-serif text-base">
              <Trans
                i18nKey="preview.section1"
                t={t}
                values={{ italic: t('preview.section1Italic') }}
                components={{ italic: <em /> }}
              />
            </span>
            <br />
            <span className="text-[var(--color-fg-muted)]">{t('preview.section1Body')}</span>
          </p>
          <p className="mt-3 text-[var(--color-fg)] leading-relaxed text-[14px] max-w-[36rem]">
            <span className="font-serif text-base">{t('preview.section2')}</span>
            <br />
            <span className="text-[var(--color-fg-muted)]">{t('preview.section2Body')}</span>
          </p>
          <div className="mt-auto rounded-[16px] border border-[var(--color-border)] bg-[var(--color-bg)] p-2.5 max-w-[36rem]">
            <div className="px-1.5 py-2 text-[var(--color-fg-faint)] text-sm">{t('preview.askPlaceholder')}</div>
            <div className="flex items-center gap-1 px-1.5 pt-1">
              <span className="text-[11px] text-[var(--color-fg-subtle)]">Aurelia Reason</span>
              <span className="ml-auto inline-flex size-7 items-center justify-center rounded-[8px] bg-[var(--color-accent)] text-[var(--color-accent-fg)]">
                <ArrowUp size={13} aria-hidden />
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function MiniNavRow({ label, active }: { label: string; active?: boolean }) {
  return (
    <div
      className={cn(
        'px-2 py-1.5 rounded-[8px] text-[12px] truncate',
        active ? 'bg-[var(--color-surface)] text-[var(--color-fg)] font-medium' : 'text-[var(--color-fg-muted)]',
      )}
    >
      {label}
    </div>
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
    </div>
  )
}
