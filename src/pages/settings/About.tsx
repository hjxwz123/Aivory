import { useTranslation } from 'react-i18next'
import { ExternalLink, Github, FileText, Tag } from 'lucide-react'
import { Logo } from '@/components/brand/logo'

const APP_VERSION = '2.3.0'
const GITHUB_URL = 'https://github.com/hjxwz123/Aivory'
const LICENSE_URL = 'https://github.com/hjxwz123/Aivory/blob/main/LICENSE'

function InfoRow({ icon: Icon, label, children }: { icon: React.ElementType; label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between px-5 sm:px-6 py-4">
      <div className="flex items-center gap-3 text-sm text-[var(--color-fg-muted)]">
        <Icon size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
        <span>{label}</span>
      </div>
      <div className="text-sm text-[var(--color-fg)]">{children}</div>
    </div>
  )
}

export default function About() {
  const { t } = useTranslation('settings')

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
      </div>

      <p className="mt-6 text-[11px] text-[var(--color-fg-faint)] text-center">
        {t('about.copyright', { year: new Date().getFullYear() })}
      </p>
    </div>
  )
}
