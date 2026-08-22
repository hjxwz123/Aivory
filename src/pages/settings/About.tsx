import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ExternalLink, Github, FileText, Mail, ShieldCheck, Tag } from 'lucide-react'
import { Logo } from '@/components/brand/logo'
import { useLegalConfig } from '@/hooks/use-legal-config'

const APP_VERSION = '2.3.1'
const GITHUB_URL = 'https://github.com/hjxwz123/Aivory'
const LICENSE_URL = 'https://github.com/hjxwz123/Aivory/blob/main/LICENSE'

function InfoRow({ icon: Icon, label, children }: { icon: React.ElementType; label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 px-5 py-4 sm:px-6">
      <div className="flex min-w-0 items-center gap-3 text-sm text-[var(--color-fg-muted)]">
        <Icon size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
        <span>{label}</span>
      </div>
      <div className="min-w-0 text-right text-sm text-[var(--color-fg)]">{children}</div>
    </div>
  )
}

export default function About() {
  const { t } = useTranslation('settings')
  const legalConfig = useLegalConfig()

  return (
    // pt matches the wrapper padding the settings pane moved into pinned page
    // headers — About has no header, so it pads itself.
    <div className="mx-auto max-w-[60rem] pt-6 sm:pt-8">
      {/* Hero */}
      <div className="mb-10 flex flex-col items-start gap-4">
        <Logo size="lg" />
        <p className="text-[var(--color-fg-muted)] text-sm leading-relaxed max-w-md">
          {t('about.description')}
        </p>
      </div>

      {/* Info card */}
      <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] divide-y divide-[var(--color-divider)]">
        <InfoRow icon={Tag} label={t('about.version')}>
          <span className="font-mono text-[13px] tabular-nums">{APP_VERSION}</span>
        </InfoRow>

        <InfoRow icon={FileText} label={t('about.license')}>
          <a
            href={LICENSE_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[4px]"
          >
            Apache 2.0
            <ExternalLink size={11} aria-hidden />
          </a>
        </InfoRow>

        <InfoRow icon={Github} label={t('about.source')}>
          <a
            href={GITHUB_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[4px]"
          >
            hjxwz123/Aivory
            <ExternalLink size={11} aria-hidden />
          </a>
        </InfoRow>

        <InfoRow icon={FileText} label={t('about.terms')}>
          <Link
            to="/terms"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 rounded-[4px] text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            {t('about.viewTerms')}
            <ExternalLink size={11} aria-hidden />
          </Link>
        </InfoRow>

        <InfoRow icon={ShieldCheck} label={t('about.privacyPolicy')}>
          <Link
            to="/privacy"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 rounded-[4px] text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            {t('about.viewPrivacy')}
            <ExternalLink size={11} aria-hidden />
          </Link>
        </InfoRow>

        <InfoRow icon={Mail} label={t('about.contactEmail')}>
          <a
            href={`mailto:${legalConfig.contactEmail}`}
            className="break-all rounded-[4px] text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            {legalConfig.contactEmail}
          </a>
        </InfoRow>
      </div>

      <p className="mt-6 text-[11px] text-[var(--color-fg-faint)] text-center">
        {t('about.copyright', { year: new Date().getFullYear() })}
      </p>
    </div>
  )
}
