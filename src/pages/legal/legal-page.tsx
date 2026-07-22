import { useEffect } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, ArrowRight, ChevronDown } from 'lucide-react'
import { Logo, LogoMark } from '@/components/brand/logo'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { ThemeToggle } from '@/components/ui/theme-toggle'

interface LegalSection {
  id: string
  heading: string
  paragraphs: string[]
  items?: string[]
  note?: string
}

const EFFECTIVE_DATE = '2026-07-22'

function sectionAnchor(doc: 'privacy' | 'terms', section: LegalSection, index: number) {
  return section.id || `${doc}-section-${index + 1}`
}

function Contents({
  doc,
  sections,
  mobile = false,
  label,
}: {
  doc: 'privacy' | 'terms'
  sections: LegalSection[]
  mobile?: boolean
  label: string
}) {
  const links = (
    <ol className="mt-3 space-y-0.5">
      {sections.map((section, index) => (
        <li key={sectionAnchor(doc, section, index)}>
          <a
            href={`#${sectionAnchor(doc, section, index)}`}
            className="block break-words rounded-[6px] py-1.5 text-sm leading-snug text-[var(--color-fg-muted)] transition-colors hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            {section.heading}
          </a>
        </li>
      ))}
    </ol>
  )

  if (mobile) {
    return (
      <details className="group mt-8 border-y border-[var(--color-divider)] lg:hidden">
        <summary className="flex min-h-12 cursor-pointer list-none items-center justify-between gap-3 py-3 text-sm font-medium text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] [&::-webkit-details-marker]:hidden">
          {label}
          <ChevronDown
            size={16}
            className="shrink-0 text-[var(--color-fg-subtle)] transition-transform group-open:rotate-180"
            aria-hidden
          />
        </summary>
        <nav aria-label={label} className="pb-4">
          {links}
        </nav>
      </details>
    )
  }

  return (
    <aside className="scrollbar-thin sticky top-24 hidden max-h-[calc(100svh-8rem)] self-start overflow-y-auto pr-4 lg:block">
      <nav aria-label={label}>
        <h2 className="text-sm font-medium text-[var(--color-fg)]">{label}</h2>
        {links}
      </nav>
    </aside>
  )
}

/** Shared long-form layout for the public Privacy Policy and Terms of Service. */
export function LegalPage({ doc }: { doc: 'privacy' | 'terms' }) {
  const { t } = useTranslation(['auth', 'common'])
  const { hash } = useLocation()
  const sectionsValue = t(`legal.${doc}.sections`, { returnObjects: true })
  const sections = Array.isArray(sectionsValue) ? (sectionsValue as LegalSection[]) : []
  const title = t(`legal.${doc}.title`)
  const contentsLabel = t('legal.contents')
  const otherDoc = doc === 'privacy' ? 'terms' : 'privacy'
  const otherDocHref = otherDoc === 'privacy' ? '/privacy' : '/terms'
  const otherDocLabel = t(otherDoc === 'privacy' ? 'legal.readPrivacy' : 'legal.readTerms')

  useEffect(() => {
    const previousTitle = document.title
    document.title = `${title} | ${t('common:appName')}`
    return () => {
      document.title = previousTitle
    }
  }, [t, title])

  useEffect(() => {
    if (!hash || sections.length === 0) return
    let anchor = hash.slice(1)
    try {
      anchor = decodeURIComponent(anchor)
    } catch {
      // Keep the literal hash when it contains malformed percent encoding.
    }
    const frame = requestAnimationFrame(() => document.getElementById(anchor)?.scrollIntoView())
    return () => cancelAnimationFrame(frame)
  }, [hash, sections.length])

  return (
    <div className="min-h-svh overflow-x-clip bg-[var(--color-bg)] text-[var(--color-fg)]">
      <header className="sticky top-0 z-[var(--z-sticky)] border-b border-[var(--color-divider)] bg-[var(--color-bg)]">
        <div className="mx-auto flex h-16 max-w-[76rem] items-center justify-between gap-2 px-4 sm:gap-4 sm:px-8">
          <Link to="/" aria-label={t('common:appName')} className="shrink-0">
            <LogoMark size={22} className="sm:hidden" />
            <Logo size="md" className="hidden sm:inline-flex" />
          </Link>
          <div className="flex min-w-0 items-center gap-1.5 sm:gap-2">
            <LanguageToggle />
            <ThemeToggle />
            <Link
              to="/welcome"
              aria-label={t('legal.back')}
              className="inline-flex size-8 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] transition-colors hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:h-8 sm:w-auto sm:gap-1.5 sm:px-2"
            >
              <ArrowLeft size={14} aria-hidden />
              <span className="hidden text-sm sm:inline">{t('legal.back')}</span>
            </Link>
          </div>
        </div>
      </header>

      <main className="mx-auto grid w-full max-w-[72rem] grid-cols-1 gap-10 px-5 py-10 sm:px-8 sm:py-14 lg:grid-cols-[15rem_minmax(0,46rem)] lg:gap-12 lg:py-16">
        <Contents doc={doc} sections={sections} label={contentsLabel} />

        <article className="min-w-0 break-words">
          <header>
            <h1 className="max-w-[18ch] text-balance font-serif text-3xl tracking-tight text-[var(--color-fg)] sm:text-4xl">
              {title}
            </h1>
            <p className="mt-3 text-sm text-[var(--color-fg-subtle)]">
              {t('legal.updated')}{' '}
              <time dateTime={EFFECTIVE_DATE}>{t(`legal.${doc}.effectiveDate`)}</time>
            </p>
            <p className="mt-6 max-w-[68ch] text-pretty text-base leading-7 text-[var(--color-fg)]">
              {t(`legal.${doc}.intro`)}
            </p>
            <aside className="mt-8 border-y border-[var(--color-divider)] py-4">
              <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('legal.operatorNotice')}</h2>
              <p className="mt-2 max-w-[68ch] text-pretty text-sm leading-6 text-[var(--color-fg-muted)]">
                {t(`legal.${doc}.notice`)}
              </p>
            </aside>
          </header>

          <Contents doc={doc} sections={sections} label={contentsLabel} mobile />

          <div className="mt-10">
            {sections.map((section, index) => {
              const anchor = sectionAnchor(doc, section, index)
              const paragraphs = Array.isArray(section.paragraphs) ? section.paragraphs : []
              const items = Array.isArray(section.items) ? section.items : []

              return (
                <section
                  id={anchor}
                  key={anchor}
                  className="scroll-mt-24 border-t border-[var(--color-divider)] py-9 first:pt-9 sm:py-10"
                >
                  <h2 className="max-w-[32ch] text-balance font-serif text-xl tracking-tight text-[var(--color-fg)] sm:text-2xl">
                    {section.heading}
                  </h2>
                  <div className="mt-4 max-w-[68ch] space-y-4 text-pretty text-base leading-7 text-[var(--color-fg)]">
                    {paragraphs.map((paragraph, paragraphIndex) => (
                      <p key={`${anchor}-paragraph-${paragraphIndex}`}>{paragraph}</p>
                    ))}
                    {items.length > 0 ? (
                      <ul className="list-disc space-y-2 pl-5 marker:text-[var(--color-fg-subtle)]">
                        {items.map((item, itemIndex) => (
                          <li key={`${anchor}-item-${itemIndex}`} className="pl-1">
                            {item}
                          </li>
                        ))}
                      </ul>
                    ) : null}
                  </div>
                  {section.note ? (
                    <p className="mt-5 max-w-[68ch] text-pretty text-sm leading-6 text-[var(--color-fg-muted)]">
                      {section.note}
                    </p>
                  ) : null}
                </section>
              )
            })}
          </div>

          <footer className="border-t border-[var(--color-divider)] pb-6 pt-7">
            <Link
              to={otherDocHref}
              className="inline-flex min-h-10 items-center gap-2 rounded-[8px] text-sm font-medium text-[var(--color-accent)] transition-colors hover:text-[var(--color-accent-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              {otherDocLabel}
              <ArrowRight size={14} aria-hidden />
            </Link>
          </footer>
        </article>
      </main>
    </div>
  )
}
