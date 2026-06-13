import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Lock } from 'lucide-react'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { useSettings } from '@/store/settings'
import { useModels } from '@/store/models'
import { modelsApi, authApi } from '@/api/endpoints'
import type { ApiModel } from '@/api/types'
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'

export default function Models() {
  const models = useSettings((s) => s.models)
  const setModels = useSettings((s) => s.setModels)
  const list = useModels((s) => s.models)
  const load = useModels((s) => s.load)
  const { t } = useTranslation(['settings', 'common'])

  // Image-generation model pre-selection (§4.12-B). Persists to user settings.
  const [imageModels, setImageModels] = useState<ApiModel[]>([])
  const [imageModelId, setImageModelId] = useState('')
  useEffect(() => {
    if (list.length === 0) void load()
    void modelsApi.listImage().then((r) => setImageModels(r.models ?? [])).catch(() => {})
    void authApi
      .getSettings()
      .then((s) => setImageModelId(typeof s.image_model_id === 'string' ? s.image_model_id : ''))
      .catch(() => {})
  }, [list.length, load])

  const onPickImageModel = (id: string) => {
    setImageModelId(id)
    void authApi.updateSettings({ image_model_id: id }).then(() => toast.success(t('common:actions.save')))
  }

  return (
    <div className="max-w-[44rem]">
      <header className="mb-8">
        <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)]">{t('settings:models.title')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">
          {t('settings:models.subtitle')}
        </p>
      </header>

      <SettingsSection title={t('settings:models.defaultModel')}>
        <SettingsRow label={t('settings:models.default')} description={t('settings:models.defaultBody')}>
          <Select
            value={models.defaultModelId}
            onValueChange={(id) => setModels({ defaultModelId: id })}
          >
            <SelectTrigger className="w-64" aria-label={t('settings:models.defaultModel')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {list.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  <span className="inline-flex items-center gap-2">{m.label}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </SettingsRow>
        <SettingsRow label={t('settings:models.responseLength')} description={t('settings:models.responseLengthBody')}>
          <Select
            value={models.responseLength}
            onValueChange={(v) => setModels({ responseLength: v as typeof models.responseLength })}
          >
            <SelectTrigger className="w-64">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="concise">{t('settings:models.concise')}</SelectItem>
              <SelectItem value="balanced">{t('settings:models.balanced')}</SelectItem>
              <SelectItem value="detailed">{t('settings:models.detailed')}</SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection title={t('settings:models.imageTitle')}>
        <SettingsRow label={t('settings:models.imageModel')} description={t('settings:models.imageModelBody')}>
          <Select value={imageModelId} onValueChange={onPickImageModel} disabled={imageModels.length === 0}>
            <SelectTrigger className="w-64" aria-label={t('settings:models.imageModel')}>
              <SelectValue
                placeholder={
                  imageModels.length === 0 ? t('settings:models.imageNone') : t('settings:models.imagePick')
                }
              />
            </SelectTrigger>
            <SelectContent>
              {imageModels.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  <span className="inline-flex items-center gap-2">{m.label}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection
        title={t('settings:models.custom')}
        description={t('settings:models.customBody')}
      >
        <div className="p-5 sm:p-6 space-y-4">
          <Textarea
            value={models.customInstructions}
            onChange={(e) => setModels({ customInstructions: e.target.value })}
            placeholder={t('settings:models.customPlaceholder')}
            className="min-h-[160px]"
          />
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-[var(--color-fg-subtle)]">
              {t('settings:models.charactersOf', {
                used: models.customInstructions.length.toLocaleString(),
                max: (2000).toLocaleString(),
              })}
            </p>
            <Button variant="secondary" onClick={() => toast.success(t('settings:models.customSaved'))}>
              {t('common:actions.save')}
            </Button>
          </div>
        </div>
      </SettingsSection>

      <SettingsSection title={t('settings:models.available')}>
        {list.length === 0 ? (
          <div className="px-5 sm:px-6 py-6 text-sm text-[var(--color-fg-muted)]">{t('common:common.loading')}</div>
        ) : (
          list.map((m) => (
            <div key={m.id} className="px-5 sm:px-6 py-4">
              <div className="flex items-start gap-3">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <h3 className="font-medium text-[var(--color-fg)]">{m.label}</h3>
                    <span className="text-[10px] uppercase tracking-wider text-[var(--color-fg-subtle)]">{m.kind}</span>
                  </div>
                  <p className="mt-1 text-xs text-[var(--color-fg-muted)] leading-relaxed">{m.description}</p>
                </div>
                {!m.enabled && (
                  <Button variant="ghost" size="sm" disabled>
                    <Lock size={12} aria-hidden /> {t('settings:models.locked')}
                  </Button>
                )}
              </div>
            </div>
          ))
        )}
      </SettingsSection>
    </div>
  )
}
