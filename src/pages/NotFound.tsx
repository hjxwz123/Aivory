import { Link } from 'react-router-dom'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowRight } from 'lucide-react'
import { Logo } from '@/components/brand/logo'
import { Button } from '@/components/ui/button'
import { useTheme } from '@/store/theme'

export default function NotFound() {
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation(['errors', 'common'])
  useEffect(() => syncSystem(), [syncSystem])

  return (
    <div className="relative min-h-svh bg-[var(--color-bg)] text-[var(--color-fg)] flex flex-col">
      <header className="px-5 sm:px-8 h-16 flex items-center">
        <Link to="/" aria-label={t('common:appName')}>
          <Logo size="md" />
        </Link>
      </header>
      <main className="flex-1 grid place-items-center px-5">
        <div className="max-w-md text-center">
          <p className="font-mono text-[11px] tracking-wider text-[var(--color-fg-subtle)] uppercase">
            {t('errors:notFound.kicker')}
          </p>
          <h1 className="mt-3 font-serif tracking-tight text-balance text-[2.75rem] sm:text-[3.5rem] leading-[1.05] text-[var(--color-fg)]">
            {t('errors:notFound.title')}
          </h1>
          <p className="mt-5 text-[var(--color-fg-muted)] leading-relaxed text-pretty">
            {t('errors:notFound.body')}
          </p>
          <div className="mt-9 flex items-center justify-center gap-2">
            <Link to="/chat">
              <Button trailingIcon={<ArrowRight size={14} aria-hidden />}>
                {t('common:actions.openAivory')}
              </Button>
            </Link>
            <Link to="/">
              <Button variant="ghost">{t('errors:notFound.back')}</Button>
            </Link>
          </div>
        </div>
      </main>
    </div>
  )
}
