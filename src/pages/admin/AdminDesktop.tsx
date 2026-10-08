import { useEffect, useState } from 'react'
import { RefreshCw, Save } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { DESKTOP_PLATFORMS, desktopUpdateApi, notifyDesktopDownloadChanged, notifyDesktopUpdateChanged, type DesktopDownloadConfig, type DesktopUpdateConfig, type DesktopUpdateState } from '@/api/desktop-update'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { SettingsActions, SettingsBlock, SettingsRow, SettingsSection } from '@/components/settings/settings-section'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'

const emptyConfig: DesktopUpdateConfig = { enabled: false, source: 'custom', version: '', downloads: {} }

export default function AdminDesktop() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<DesktopUpdateConfig>(emptyConfig)
  const [download, setDownload] = useState<DesktopDownloadConfig>({ enabled: false, url: '' })
  const [state, setState] = useState<DesktopUpdateState | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadFailed, setLoadFailed] = useState(false)
  const [checking, setChecking] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    let active = true
    void adminApi.settings().then((settings) => {
      const config = settings.desktop_update as Partial<DesktopUpdateConfig> | null
      if (active) setDraft({
        enabled: config?.enabled === true,
        source: config?.source === 'official' ? 'official' : 'custom',
        version: typeof config?.version === 'string' ? config.version : '',
        downloads: config?.downloads && typeof config.downloads === 'object' ? config.downloads : {},
      })
      const entry = settings.desktop_download as Partial<DesktopDownloadConfig> | null
      if (active) setDownload({ enabled: entry?.enabled === true, url: typeof entry?.url === 'string' ? entry.url : '' })
    }).catch(() => { if (active) setLoadFailed(true) }).finally(() => { if (active) setLoading(false) })
    void desktopUpdateApi.state().then((next) => { if (active) setState(next) }).catch(() => {})
    return () => { active = false }
  }, [])

  useEffect(() => {
    if (!state || draft.source !== 'official') return
    setDraft((current) => {
      if (current.source !== 'official' || Object.keys(current.downloads).length) return current
      const downloads = state.releases?.find((release) => release.version === current.version.trim().replace(/^v/, ''))?.downloads ?? {}
      return Object.keys(downloads).length ? { ...current, enabled: false, downloads } : current
    })
  }, [state, draft.source, draft.version])

  async function check() {
    setChecking(true)
    try {
      const next = await desktopUpdateApi.check()
      setState(next)
      notifyDesktopUpdateChanged()
      if (next.check_error) toast.error(t('admin:desktop.checkFailed'))
    } catch { toast.error(t('admin:desktop.checkFailed')) }
    finally { setChecking(false) }
  }

  async function save() {
    const entry: DesktopDownloadConfig = { enabled: download.enabled, url: download.url?.trim() || '' }
    if (entry.url) {
      try {
        const url = new URL(entry.url)
        if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) throw new Error('invalid_url')
      } catch { toast.error(t('admin:desktop.invalidDownloadUrl')); return }
    }
    if (entry.enabled && !entry.url) { toast.error(t('admin:desktop.downloadIncomplete')); return }
    const config: DesktopUpdateConfig = { ...draft, version: draft.version.trim(), downloads: {} }
    for (const platform of DESKTOP_PLATFORMS) {
      const address = draft.downloads[platform]?.trim()
      if (!address) continue
      try {
        const url = new URL(address)
        if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.hash) throw new Error('invalid_url')
      } catch { toast.error(t('admin:desktop.invalidUrl')); return }
      config.downloads[platform] = address
    }
    if (config.enabled && (!config.version || !Object.keys(config.downloads).length)) {
      toast.error(t('admin:desktop.publishIncomplete'))
      return
    }
    setSaving(true)
    try {
      const settings = await adminApi.updateSettings({ desktop_update: config, desktop_download: entry })
      setDraft(settings.desktop_update as DesktopUpdateConfig)
      setDownload(settings.desktop_download as DesktopDownloadConfig)
      notifyDesktopUpdateChanged()
      notifyDesktopDownloadChanged()
      toast.success(t('admin:settings.saved'))
      void desktopUpdateApi.state().then(setState).catch(() => {})
    } catch (error) {
      toast.error(error instanceof ApiError && error.status === 400 ? t('admin:desktop.invalidConfig') : t('admin:common.failed'))
    } finally { setSaving(false) }
  }

  function useVersion(version: string) {
    if (version === draft.version) return
    setDraft((current) => ({ ...current, enabled: false, version,
      downloads: current.source === 'official' ? officialDownloads(version) : {},
    }))
  }

  function officialDownloads(version: string) {
    return state?.releases?.find((release) => release.version === version.trim().replace(/^v/, ''))?.downloads ?? {}
  }

  return (
    <div>
      <AdminPageHeader title={t('admin:desktop.title')} actions={
        <Button variant="ghost" size="sm" leadingIcon={<RefreshCw size={15} />} loading={checking} onClick={() => void check()}>
          {t('admin:desktop.check')}
        </Button>
      } />
      {loading ? <PanelFallback /> : loadFailed ? <p role="alert" className="mt-6 text-sm text-[var(--color-danger)]">{t('admin:common.failed')}</p> : (
        <div className="mt-6">
          <SettingsSection title={t('admin:desktop.downloadEntry')} id="desktop-download-entry">
            <SettingsRow label={t('admin:desktop.showDownload')} description={t('admin:desktop.showDownloadHint')} htmlFor="desktop-download-visible">
              <Switch id="desktop-download-visible" checked={download.enabled} onCheckedChange={(enabled) => setDownload((current) => ({ ...current, enabled }))} />
            </SettingsRow>
            <SettingsBlock>
              <Field label={t('admin:desktop.downloadUrl')} htmlFor="desktop-download-url">
                <Input id="desktop-download-url" type="url" maxLength={2048} placeholder="https://" value={download.url ?? ''} onChange={(event) => setDownload((current) => ({ ...current, url: event.target.value }))} />
              </Field>
              <p className="mt-2 text-xs text-[var(--color-fg-muted)]">{t('admin:desktop.downloadUrlHint')}</p>
            </SettingsBlock>
          </SettingsSection>
          {state?.latest_version ? (
            <div role="status" className="mb-5 flex flex-wrap items-center justify-between gap-3 rounded-[8px] bg-[var(--color-bg-muted)] px-4 py-3">
              <p className="min-w-0 text-sm">{t('admin:desktop.latest', { version: state.latest_version })}</p>
              {state.latest_version !== draft.version ? <Button size="sm" variant="ghost" onClick={() => useVersion(state.latest_version!)}>{t('admin:desktop.useVersion')}</Button> : null}
            </div>
          ) : state?.check_error ? <p role="status" className="mb-5 text-sm text-[var(--color-fg-muted)]">{t('admin:desktop.checkFailed')}</p> : null}
          <SettingsSection title={t('admin:desktop.release')} id="desktop-release">
            <SettingsRow label={t('admin:desktop.published')} htmlFor="desktop-published">
              <Switch id="desktop-published" checked={draft.enabled} onCheckedChange={(enabled) => setDraft((current) => ({ ...current, enabled }))} />
            </SettingsRow>
            <SettingsBlock>
              <Field label={t('admin:desktop.source')} htmlFor="desktop-source" className="mb-5 max-w-sm">
                <Select value={draft.source ?? 'custom'} onValueChange={(source: 'custom' | 'official') => setDraft((current) => ({
                  ...current, source, enabled: false, downloads: source === 'official' ? officialDownloads(current.version) : {},
                }))}>
                  <SelectTrigger id="desktop-source"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="custom">{t('admin:desktop.customPackages')}</SelectItem>
                    <SelectItem value="official">{t('admin:desktop.officialPackages')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('admin:desktop.version')} htmlFor="desktop-version" className="max-w-sm">
                <Input id="desktop-version" value={draft.version} maxLength={128} placeholder="2.5.1" onChange={(event) => setDraft((current) => ({ ...current, enabled: false, version: event.target.value,
                  downloads: current.source === 'official' ? officialDownloads(event.target.value) : current.downloads,
                }))} />
              </Field>
              {draft.source === 'official' ? <p className="mt-3 text-xs text-[var(--color-fg-muted)]">{t('admin:desktop.officialServerHint')}</p> : null}
            </SettingsBlock>
          </SettingsSection>
          <SettingsSection title={t('admin:desktop.downloads')} id="desktop-downloads">
            {draft.source === 'official' && !Object.keys(draft.downloads).length ? <SettingsBlock><p role="status" className="text-sm text-[var(--color-fg-muted)]">{t('admin:desktop.officialUnavailable')}</p></SettingsBlock> : null}
            <SettingsBlock className="grid gap-5 sm:grid-cols-2">
              {DESKTOP_PLATFORMS.map((platform) => (
                <Field key={platform} label={t(`admin:desktop.platforms.${platform}`)} htmlFor={`desktop-${platform}`}>
                  <Input id={`desktop-${platform}`} type="url" readOnly={draft.source === 'official'} maxLength={2048} placeholder={draft.source === 'official' ? t('admin:desktop.notAvailable') : 'https://'} value={draft.downloads[platform] ?? ''} onChange={(event) => setDraft((current) => ({ ...current, downloads: { ...current.downloads, [platform]: event.target.value } }))} />
                </Field>
              ))}
            </SettingsBlock>
          </SettingsSection>
          <SettingsActions>
            <Button size="sm" loading={saving} leadingIcon={<Save size={15} />} onClick={() => void save()}>{t('common:actions.save')}</Button>
          </SettingsActions>
        </div>
      )}
    </div>
  )
}
