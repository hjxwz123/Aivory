import { useTranslation } from 'react-i18next'
import { buildShortcuts } from '@/lib/shortcuts'
import { Kbd } from '@/components/ui/kbd'
import { SettingsSection } from './SettingsLayout'

export default function Shortcuts() {
  const { t } = useTranslation('settings')
  const all = buildShortcuts()
  const grouped: Record<string, typeof all> = { global: [], composer: [], sidebar: [] }
  for (const s of all) grouped[s.scope].push(s)

  return (
    <div className="mx-auto max-w-[60rem]">
      <header className="mb-6">
        <h1 className="text-xl font-semibold tracking-normal text-[var(--color-fg)]">{t('shortcuts.title')}</h1>
        <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('shortcuts.subtitle')}</p>
      </header>

      {(['global', 'composer', 'sidebar'] as const).map(
        (scope) =>
          grouped[scope].length > 0 && (
            <SettingsSection key={scope} title={t(`shortcuts.groups.${scope}`)}>
              {grouped[scope].map((s) => (
                <div key={s.id} className="flex items-center gap-4 px-4 py-3">
                  <span className="flex-1 text-sm text-[var(--color-fg)]">
                    {t(`shortcuts.items.${s.id}` as const, { defaultValue: s.name })}
                  </span>
                  <span className="inline-flex items-center gap-1">
                    {s.display.map((k, i) => (
                      <Kbd key={`${k}-${i}`} size="md">
                        {k}
                      </Kbd>
                    ))}
                  </span>
                </div>
              ))}
            </SettingsSection>
          ),
      )}
    </div>
  )
}
