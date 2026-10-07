import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Lock } from 'lucide-react'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { useSettings } from '@/store/settings'
import { useModels } from '@/store/models'
import { useAuth } from '@/store/auth'
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
import { userCan } from '@/lib/user-permissions'
import { workspaceCapabilitiesForScope } from '@/lib/workspace-permissions'
import { useWorkspaces } from '@/store/workspaces'
import { persistUserSettings } from '@/lib/user-settings'
import { browserSpeechSupported } from '@/lib/browser-speech'
import {
  defaultSpeechEngine,
  loadSttCapability,
  resolveSpeechEngine,
  speechEnginePreference,
  type SpeechEngine,
  type SttCapability,
} from '@/lib/speech-recognition'

/**
 * Default speech recognition for the composer microphone (§ voice). Unset means
 * automatic: the administrator's service when configured, else the browser's.
 */
function VoiceInputSettings() {
  const { t } = useTranslation(['settings', 'common'])
  const user = useAuth((s) => s.user)
  const userId = user?.id
  const [capability, setCapability] = useState<SttCapability | null>(null)
  const [browserSupported] = useState(browserSpeechSupported)
  const [saving, setSaving] = useState(false)
  useEffect(() => {
    let live = true
    void loadSttCapability(userId).then((next) => {
      if (live) setCapability(next)
    })
    return () => {
      live = false
    }
  }, [userId])

  const serverEnabled = Boolean(capability?.enabled)
  const preference = speechEnginePreference(user?.settings)
  const selected = preference ?? defaultSpeechEngine(serverEnabled)
  const effective = capability ? resolveSpeechEngine(preference, { serverEnabled, browserSupported }) : selected
  const engineLabel = (engine: SpeechEngine) =>
    engine === 'model' ? t('settings:models.voiceModel') : t('settings:models.voiceBrowser')

  const onPick = (value: string) => {
    if ((value !== 'model' && value !== 'browser') || value === preference) return
    setSaving(true)
    void persistUserSettings({ speech_recognition: value })
      .then(() => toast.success(t('common:actions.save')))
      .catch((e) =>
        toast.error(t('common:actions.failed', { defaultValue: 'Failed to save' }), e instanceof Error ? e.message : undefined),
      )
      .finally(() => setSaving(false))
  }

  const modelDetail = !capability
    ? ''
    : !serverEnabled
      ? t('settings:models.voiceModelUnavailable')
      : capability.creditsPerMinute > 0
        ? t('settings:models.voiceCostPerMinute', {
            credits: Number(capability.creditsPerMinute.toFixed(4)).toLocaleString(),
          })
        : t('settings:models.voiceFree')
  const browserDetail = browserSupported ? t('settings:models.voiceFree') : t('settings:models.voiceBrowserUnsupported')

  return (
    <SettingsSection title={t('settings:models.voiceTitle')}>
      <SettingsRow label={t('settings:models.voiceEngine')} description={t('settings:models.voiceEngineBody')}>
        <Select value={selected} onValueChange={onPick} disabled={!capability || saving}>
          <SelectTrigger className="w-64" aria-label={t('settings:models.voiceEngine')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="model" disabled={!serverEnabled}>
              {engineLabel('model')}
              {modelDetail ? <span className="ml-1.5 text-[var(--color-fg-subtle)]">· {modelDetail}</span> : null}
            </SelectItem>
            <SelectItem value="browser" disabled={!browserSupported}>
              {engineLabel('browser')}
              <span className="ml-1.5 text-[var(--color-fg-subtle)]">· {browserDetail}</span>
            </SelectItem>
          </SelectContent>
        </Select>
      </SettingsRow>
      {capability && effective !== selected ? (
        <p className="pb-3 text-xs text-[var(--color-warning)]">
          {effective
            ? t('settings:models.voiceFallback', { engine: engineLabel(effective) })
            : t('settings:models.voiceUnavailable')}
        </p>
      ) : null}
    </SettingsSection>
  )
}

export default function Models() {
  const models = useSettings((s) => s.models)
  const setModels = useSettings((s) => s.setModels)
  const list = useModels((s) => s.models)
  const imageModels = useModels((s) => s.imageModels)
  const modelsLoaded = useModels((s) => s.loaded)
  const load = useModels((s) => s.load)
  const setGlobalDefaultModel = useModels((s) => s.setDefaultId)
  const user = useAuth((s) => s.user)
  const workspacePolicy = useWorkspaces((state) =>
    state.activeId ? state.policies[state.activeId] : undefined,
  )
  const workspaceId = useWorkspaces((state) => state.activeId)
  const workspacesLoaded = useWorkspaces((state) => state.loaded)
  const workspacePolicyLoading = useWorkspaces((state) =>
    state.activeId ? state.policyLoading[state.activeId] === true : false,
  )
  const workspaceSwitching = useWorkspaces((state) => state.switching)
  const workspacePolicyError = useWorkspaces((state) =>
    state.activeId ? state.policyErrors[state.activeId] : null,
  )
  const workspaceCaps = workspaceCapabilitiesForScope(workspaceId, workspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: workspaceSwitching,
    policyError: workspacePolicyError,
  })
  const canDraw = userCan(user, 'allow_drawing') && workspaceCaps.drawing
  const canUseVoice = userCan(user, 'allow_voice_transcription')
  const { t } = useTranslation(['settings', 'common'])

  // Image-generation model pre-selection (§4.12-B). Persists to user settings.
  const [imageModelId, setImageModelId] = useState(() => {
    const value = useAuth.getState().user?.settings?.image_model_id
    return typeof value === 'string' ? value : ''
  })

  // Custom-instructions save: in-flight guard so the request can't be double-fired.
  const savingRef = useRef(false)
  const [savingInstructions, setSavingInstructions] = useState(false)
  useEffect(() => {
    if (!modelsLoaded) void load()
  }, [load, modelsLoaded])

  const onPickImageModel = (id: string) => {
    setImageModelId(id)
    void persistUserSettings({ image_model_id: id }).then(() => toast.success(t('common:actions.save')))
  }

  const onPickResponseLength = (v: typeof models.responseLength) => {
    setModels({ responseLength: v })
    void persistUserSettings({ response_length: v }).catch(() => {
      /* best-effort — local state is the source of truth */
    })
  }

  const onPickDefaultModel = (id: string) => {
    const prev = models.defaultModelId
    setModels({ defaultModelId: id })
    setGlobalDefaultModel(id)
    void persistUserSettings({ default_model_id: id })
      .then(() => toast.success(t('common:actions.save')))
      .catch((e) => {
        setModels({ defaultModelId: prev })
        setGlobalDefaultModel(prev)
        toast.error(t('common:actions.failed', { defaultValue: 'Failed to save' }), e instanceof Error ? e.message : undefined)
      })
  }

  const onSaveInstructions = async () => {
    if (savingRef.current) return
    savingRef.current = true
    setSavingInstructions(true)
    try {
      await persistUserSettings({ persona_custom: models.customInstructions })
      toast.success(t('settings:models.customSaved'))
    } catch {
      toast.error(t('common:actions.failed', { defaultValue: 'Failed to save' }))
    } finally {
      savingRef.current = false
      setSavingInstructions(false)
    }
  }

  return (
    <div className="mx-auto max-w-[60rem]">
      <header className="mb-6">
        <h1 className="text-xl font-semibold tracking-normal text-[var(--color-fg)]">{t('settings:models.title')}</h1>
        <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">
          {t('settings:models.subtitle')}
        </p>
      </header>

      <SettingsSection title={t('settings:models.defaultModel')}>
        <SettingsRow label={t('settings:models.default')} description={t('settings:models.defaultBody')}>
          <Select
            value={models.defaultModelId}
            onValueChange={onPickDefaultModel}
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
            onValueChange={(v) => onPickResponseLength(v as typeof models.responseLength)}
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

      {canDraw ? (
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
      ) : null}

      {canUseVoice ? <VoiceInputSettings /> : null}

      <SettingsSection
        title={t('settings:models.custom')}
        description={t('settings:models.customBody')}
      >
        <div className="space-y-3 p-4">
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
            <Button
              variant="secondary"
              loading={savingInstructions}
              onClick={() => void onSaveInstructions()}
            >
              {t('common:actions.save')}
            </Button>
          </div>
        </div>
      </SettingsSection>

      <SettingsSection title={t('settings:models.available')}>
        {list.length === 0 ? (
          <div className="py-5 text-sm text-[var(--color-fg-muted)]">{t('common:common.loading')}</div>
        ) : (
          list.map((m) => (
            <div key={m.id} className="py-3.5">
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
