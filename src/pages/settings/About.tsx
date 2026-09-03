import { useTranslation } from 'react-i18next'
import { ArrowUpRight, BookOpen, FileText, Github, Mail, Scale, ShieldCheck } from 'lucide-react'
import { TracedLogo } from '@/components/brand/logo'
import { useLegalConfig } from '@/hooks/use-legal-config'

const APP_VERSION = '2.4.6'
const DOCS_URL = 'https://docs.aivorygo.com'
const GITHUB_URL = 'https://github.com/hjxwz123/Aivory'
const TERMS_URL = '/terms'
const PRIVACY_URL = '/privacy'
const LICENSE_URL = 'https://github.com/hjxwz123/Aivory/blob/main/LICENSE'

function ResourceCard({
  icon: Icon,
  label,
  value,
  href,
  external = false,
}: {
  icon: React.ElementType
  label: string
  value: string
  href: string
  external?: boolean
}) {
  return (
    <a
      href={href}
      target={external ? '_blank' : undefined}
      rel={external ? 'noopener noreferrer' : undefined}
      className="group flex min-w-0 items-center gap-3 rounded-[8px] bg-[var(--color-bg-muted)]/70 p-3 interactive hover:bg-[var(--color-surface-sunken)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
    >
      <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-[7px] bg-[var(--color-surface)] text-[var(--color-fg-muted)] shadow-[var(--shadow-xs)]">
        <Icon size={15} aria-hidden />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-[var(--color-fg)]">{label}</div>
        <div className="mt-0.5 truncate text-xs text-[var(--color-fg-muted)]">{value}</div>
      </div>
      {external ? (
        <ArrowUpRight
          size={12}
          aria-hidden
          className="shrink-0 text-[var(--color-fg-faint)] transition-transform duration-150 group-hover:-translate-y-px group-hover:translate-x-px group-hover:text-[var(--color-fg-muted)]"
        />
      ) : null}
    </a>
  )
}

export default function About() {
  const { t } = useTranslation('settings')
  const legalConfig = useLegalConfig()

  return (
    // About has no pinned page header, so it owns the equivalent top padding.
    <div className="mx-auto flex min-h-full w-full max-w-[60rem] flex-col pt-5 sm:pt-6">
      <div className="max-w-[42rem]">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <TracedLogo size="lg" />
          <span className="shrink-0 rounded-full bg-[var(--color-bg-muted)] px-2.5 py-1 font-mono text-[11px] tabular-nums text-[var(--color-fg-muted)]">
            v{APP_VERSION}
          </span>
        </div>
        <p className="mt-1 text-xs font-medium text-[var(--color-fg-subtle)]">{t('about.tagline')}</p>
        <div className="mt-4 space-y-2.5 text-sm leading-relaxed text-[var(--color-fg-muted)]">
          <p>{t('about.description')}</p>
          <p className="text-[13px] text-[var(--color-fg-subtle)]">{t('about.descriptionDetail')}</p>
        </div>
      </div>

      <section className="mt-7" aria-labelledby="about-resources-title">
        <h2 id="about-resources-title" className="text-sm font-medium text-[var(--color-fg)]">
          {t('about.resources')}
        </h2>

        <nav className="mt-3 grid gap-2.5 md:grid-cols-2" aria-label={t('about.resources')}>
          <ResourceCard icon={BookOpen} label={t('about.documentation')} value="docs.aivorygo.com" href={DOCS_URL} external />
          <ResourceCard icon={Github} label={t('about.source')} value="hjxwz123/Aivory" href={GITHUB_URL} external />
          <ResourceCard icon={FileText} label={t('about.terms')} value={t('about.viewTerms')} href={TERMS_URL} />
          <ResourceCard icon={ShieldCheck} label={t('about.privacyPolicy')} value={t('about.viewPrivacy')} href={PRIVACY_URL} />
        </nav>
      </section>

      <footer className="mt-auto pt-6 text-xs text-[var(--color-fg-subtle)]">
        <div className="border-t border-[var(--color-divider)] pt-4">
          <div className="flex flex-col gap-2.5 md:flex-row md:items-center md:justify-between">
            <a
              href={`mailto:${legalConfig.contactEmail}`}
              className="inline-flex min-w-0 items-center gap-2 rounded-[4px] hover:text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <Mail size={14} className="shrink-0" aria-hidden />
              <span className="shrink-0">{t('about.contactSupport')}:</span>
              <span className="truncate font-medium text-[var(--color-accent)]">{legalConfig.contactEmail}</span>
            </a>
            <span className="inline-flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[11px] text-[var(--color-fg-faint)]">
              <span>{t('about.copyright', { year: new Date().getFullYear() })}</span>
              <span aria-hidden>·</span>
              <a
                href={LICENSE_URL}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 rounded-[4px] hover:text-[var(--color-fg-muted)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <Scale size={11} aria-hidden />
                Apache 2.0
              </a>
            </span>
          </div>
        </div>
      </footer>
    </div>
  )
}
