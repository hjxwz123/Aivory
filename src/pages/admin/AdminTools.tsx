/**
 * AdminTools — outbound services the assistant invokes during a conversation:
 * web search, web reading (built-in / DeepSeek / AnySearch), maps, and the code
 * sandbox sidecar.
 *
 * Shares the global `/admin/settings` endpoint with other admin pages; PATCH
 * is scoped to the keys this page owns so concurrent edits don't clobber
 * fields managed elsewhere.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { AlertTriangle, RefreshCw, Settings2 } from 'lucide-react'
import { adminApi, ApiError, type ApiMCPServer } from '@/api'
import type { ApiBuiltinTool } from '@/api/types'
import { LucideGlyph } from '@/components/ui/lucide-icon'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { SettingsBlock, SettingsSection } from '@/components/settings/settings-section'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'disabled_tools',
  'search_provider',
  'search_base_url',
  'search_api_key',
  'search_secret_id',
  'search_secret_key',
  'search_engines',
  'search_result_count',
  'fetch_provider',
  'fetch_api_key',
  'fetch_jina_base_url',
  'fetch_jina_url_mode',
  'map_provider',
  'map_amap_web_api_key',
  'map_amap_js_api_key',
  'map_amap_security_secret',
  'sandbox_base_url',
  'sandbox_api_key',
  'sandbox_exec_timeout_sec',
  'sandbox_idle_ttl_sec',
] as const

export default function AdminTools() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [builtinTools, setBuiltinTools] = useState<ApiBuiltinTool[]>([])
  const [mcpServers, setMcpServers] = useState<ApiMCPServer[]>([])
  const [loading, setLoading] = useState(true)
  const [settingsLoadFailed, setSettingsLoadFailed] = useState(false)
  const [builtinToolsLoadFailed, setBuiltinToolsLoadFailed] = useState(false)
  const [mcpLoadFailed, setMCPLoadFailed] = useState(false)
  const [builtinToolsRefreshing, setBuiltinToolsRefreshing] = useState(false)
  const [mcpRefreshing, setMCPRefreshing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [togglingMCPID, setTogglingMCPID] = useState('')
  const savingRef = useRef(false)
  const togglingMCPRef = useRef('')
  const loadRequestRef = useRef(0)
  const builtinToolsRequestRef = useRef(0)
  const mcpRequestRef = useRef(0)

  async function load() {
    const requestID = ++loadRequestRef.current
    const builtinToolsRequestID = ++builtinToolsRequestRef.current
    const mcpRequestID = ++mcpRequestRef.current
    setLoading(true)
    setSettingsLoadFailed(false)
    setBuiltinToolsLoadFailed(false)
    setMCPLoadFailed(false)
    setBuiltinTools([])
    setMcpServers([])
    const [settingsResult, toolsResult, mcpResult] = await Promise.allSettled([
      adminApi.settings(),
      adminApi.builtinTools(),
      adminApi.mcpServers(),
    ])
    if (requestID !== loadRequestRef.current) return
    if (settingsResult.status === 'fulfilled') {
      setDraft(settingsResult.value)
    } else {
      setDraft({})
      setSettingsLoadFailed(true)
    }
    if (builtinToolsRequestID === builtinToolsRequestRef.current) {
      if (toolsResult.status === 'fulfilled') {
        setBuiltinTools(toolsResult.value)
      } else {
        setBuiltinToolsLoadFailed(true)
      }
    }
    if (mcpRequestID === mcpRequestRef.current) {
      if (mcpResult.status === 'fulfilled') {
        setMcpServers(mcpResult.value)
      } else {
        setMCPLoadFailed(true)
      }
    }
    setLoading(false)
  }
  useEffect(() => {
    void load()
    return () => {
      loadRequestRef.current += 1
      builtinToolsRequestRef.current += 1
      mcpRequestRef.current += 1
    }
  }, [])

  async function retryBuiltinTools() {
    const requestID = ++builtinToolsRequestRef.current
    setBuiltinToolsRefreshing(true)
    try {
      const tools = await adminApi.builtinTools()
      if (requestID !== builtinToolsRequestRef.current) return
      setBuiltinTools(tools)
      setBuiltinToolsLoadFailed(false)
    } catch (error) {
      if (requestID !== builtinToolsRequestRef.current) return
      setBuiltinTools([])
      setBuiltinToolsLoadFailed(true)
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      if (requestID === builtinToolsRequestRef.current) setBuiltinToolsRefreshing(false)
    }
  }

  async function retryMCPServers() {
    const requestID = ++mcpRequestRef.current
    setMCPRefreshing(true)
    try {
      const servers = await adminApi.mcpServers()
      if (requestID !== mcpRequestRef.current) return
      setMcpServers(servers)
      setMCPLoadFailed(false)
    } catch (error) {
      if (requestID !== mcpRequestRef.current) return
      setMcpServers([])
      setMCPLoadFailed(true)
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      if (requestID === mcpRequestRef.current) setMCPRefreshing(false)
    }
  }

  async function save() {
    if (savingRef.current) return
    if (readString('map_provider') === 'amap' && ['map_amap_web_api_key', 'map_amap_js_api_key', 'map_amap_security_secret'].some(key => !readString(key).trim())) {
      toast.error(t('admin:tools.mapCredentialsRequired'))
      return
    }
    const searchResultCount = draft.search_result_count ?? 5
    if (readString('search_provider') === 'searxng' && (
      typeof searchResultCount !== 'number' || !Number.isInteger(searchResultCount) ||
      searchResultCount < 1 || searchResultCount > 50
    )) {
      toast.error(t('admin:settings.fields.searchResultCountInvalid'))
      return
    }
    const fetchProvider = readString('fetch_provider') || 'builtin'
    if (fetchProvider === 'builtin' && readString('fetch_jina_base_url').trim()) {
      try {
        const rawUrl = readString('fetch_jina_base_url').trim()
        const url = new URL(rawUrl)
        if (rawUrl.length > 2048 || !['http:', 'https:'].includes(url.protocol) || !url.hostname || url.username || url.password || rawUrl.includes('?') || rawUrl.includes('#') || /\s/.test(rawUrl)) throw new Error('invalid reader URL')
      } catch {
        toast.error(t('admin:tools.fetchJinaUrlInvalid'))
        return
      }
    }
    savingRef.current = true
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const k of OWNED_KEYS) {
        if (k === 'search_result_count') continue
        if (k === 'search_base_url' && ['anysearch', 'deepseek'].includes(readString('search_provider'))) continue
        if (k in draft) patch[k] = draft[k]
      }
      if (readString('search_provider') === 'searxng') patch.search_result_count = searchResultCount
      const saved = await adminApi.updateSettings(patch)
      setDraft((current) => {
        const next = { ...current }
        for (const key of OWNED_KEYS) {
          if (key in saved) next[key] = saved[key]
        }
        if ('sandbox_configured' in saved) next.sandbox_configured = saved.sandbox_configured
        if ('image_model_configured' in saved) next.image_model_configured = saved.image_model_configured
        return next
      })
      toast.success(t('admin:settings.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  function readString(key: string, fallback = ''): string {
    const v = draft[key]
    return typeof v === 'string' ? v : fallback
  }

  function readStringArray(key: string): string[] {
    const value = draft[key]
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
  }

  function readBool(key: string, fallback = false): boolean {
    const value = draft[key]
    return typeof value === 'boolean' ? value : fallback
  }

  function setToolEnabled(name: string, enabled: boolean) {
    setDraft((current) => {
      const value = current.disabled_tools
      const disabled = new Set(
        Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [],
      )
      if (enabled) disabled.delete(name)
      else disabled.add(name)
      return { ...current, disabled_tools: [...disabled] }
    })
  }

  async function setMCPEnabled(server: ApiMCPServer, enabled: boolean) {
    if (togglingMCPRef.current) return
    togglingMCPRef.current = server.id
    setTogglingMCPID(server.id)
    setMcpServers((current) => current.map((row) => row.id === server.id ? { ...row, enabled } : row))
    try {
      const updated = await adminApi.updateMCPServer(server.id, { enabled })
      setMcpServers((current) => current.map((row) => row.id === server.id ? updated : row))
    } catch (error) {
      setMcpServers((current) => current.map((row) => row.id === server.id ? server : row))
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      togglingMCPRef.current = ''
      setTogglingMCPID('')
    }
  }

  const searchProvider = readString('search_provider')
  const sandboxConfigured = readBool('sandbox_configured', true)
  const imageModelConfigured = readBool('image_model_configured', true)

  return (
    <div>
      <AdminPageHeader
        title={t('admin:tools.title')}
      />

      {loading ? (
        <PanelFallback />
      ) : settingsLoadFailed ? (
        <div className="mt-8 flex min-h-64 flex-col items-center justify-center rounded-[12px] bg-[var(--color-surface)] px-6 py-10 text-center">
          <AlertTriangle size={22} aria-hidden className="text-[var(--color-danger)]" />
          <p className="mt-3 text-sm font-medium text-[var(--color-fg)]">{t('admin:tools.loadFailed')}</p>
          <Button
            className="mt-4"
            size="sm"
            variant="secondary"
            leadingIcon={<RefreshCw size={14} aria-hidden />}
            onClick={() => void load()}
          >
            {t('common:actions.tryAgain')}
          </Button>
        </div>
      ) : (
        <section className="admin-form-layout mt-6 flex w-full min-w-0 flex-col gap-8" aria-busy={saving || Boolean(togglingMCPID)}>
          <SettingsSection
            title={t('admin:tools.availabilityTitle', { defaultValue: 'Global tool availability' })}
            className="mb-0"
          >
            {builtinToolsLoadFailed ? (
              <SettingsBlock className="flex flex-col items-start gap-2">
                <p className="text-sm text-[var(--color-danger)]">{t('admin:tools.builtinLoadFailed')}</p>
                <Button size="sm" variant="ghost" leadingIcon={<RefreshCw size={14} aria-hidden />} loading={builtinToolsRefreshing} onClick={() => void retryBuiltinTools()}>
                  {t('common:actions.tryAgain')}
                </Button>
              </SettingsBlock>
            ) : builtinTools.length === 0 ? (
              <SettingsBlock>
                <p className="text-sm text-[var(--color-fg-muted)]">
                  {t('admin:tools.availabilityEmpty', { defaultValue: 'No platform tools are registered.' })}
                </p>
              </SettingsBlock>
            ) : (
              builtinTools.map((tool) => {
                const requiresSandbox = tool.name === 'python_execute'
                const requiresImageModel = tool.name === 'image_generate'
                const unavailable =
                  (requiresSandbox && !sandboxConfigured) ||
                  (requiresImageModel && !imageModelConfigured)
                const enabled = !unavailable && !readStringArray('disabled_tools').includes(tool.name)
                return (
                  <label key={tool.name} className="flex min-h-14 items-center gap-4 px-4 py-3">
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm font-medium text-[var(--color-fg)]">
                        {t(`admin:models.builtinTools.names.${tool.name}`, { defaultValue: tool.name })}
                      </span>
                      <span className="mt-0.5 block text-xs leading-normal text-[var(--color-fg-muted)]">
                        {t(`admin:models.builtinTools.descriptions.${tool.name}`, { defaultValue: tool.description })}
                      </span>
                    </span>
                    <span className="flex shrink-0 items-center gap-2.5">
                      {unavailable ? (
                        <span className="max-w-[8.5rem] text-right text-xs leading-4 text-[var(--color-fg-subtle)] sm:max-w-none sm:whitespace-nowrap">
                          {requiresImageModel
                            ? t('admin:tools.configureImageModelFirst')
                            : t('admin:tools.configureSandboxFirst')}
                        </span>
                      ) : null}
                      <Switch
                        checked={enabled}
                        disabled={saving || unavailable}
                        onCheckedChange={(value) => setToolEnabled(tool.name, value)}
                      />
                    </span>
                  </label>
                )
              })
            )}
          </SettingsSection>

          <SettingsSection
            title={t('admin:tools.mcpAvailabilityTitle')}
            className="mb-0"
            actions={(
              <Button
                asChild
                size="sm"
                variant="secondary"
                leadingIcon={<Settings2 size={14} aria-hidden />}
                className="max-sm:min-h-[var(--tap-min)]"
              >
                <Link to="/admin/mcp">{t('admin:tools.manageMCP')}</Link>
              </Button>
            )}
          >
            {mcpLoadFailed ? (
              <SettingsBlock className="flex flex-col items-start gap-2">
                <p className="text-sm text-[var(--color-danger)]">{t('admin:tools.mcpLoadFailed')}</p>
                <Button size="sm" variant="ghost" leadingIcon={<RefreshCw size={14} aria-hidden />} loading={mcpRefreshing} onClick={() => void retryMCPServers()}>
                  {t('common:actions.tryAgain')}
                </Button>
              </SettingsBlock>
            ) : mcpServers.length === 0 ? (
              <SettingsBlock>
                <p className="text-sm text-[var(--color-fg-muted)]">{t('admin:tools.mcpAvailabilityEmpty')}</p>
              </SettingsBlock>
            ) : (
              mcpServers.map((server) => (
                <label key={server.id} htmlFor={`tool-mcp-${server.id}`} className="flex min-h-14 items-center gap-3 px-4 py-3">
                  <span className="grid size-8 shrink-0 place-items-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                    <LucideGlyph name={server.icon || 'Blocks'} size={15} aria-hidden />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-sm font-medium text-[var(--color-fg)]">{server.name}</span>
                    <span className="mt-0.5 block text-xs leading-normal text-[var(--color-fg-muted)]">
                      {server.description}
                    </span>
                    {server.last_error ? (
                      <span className="mt-0.5 block text-xs leading-normal text-[var(--color-danger)]">
                        {t('admin:tools.mcpUnavailable')}
                      </span>
                    ) : null}
                  </span>
                  <Switch
                    id={`tool-mcp-${server.id}`}
                    checked={server.enabled}
                    disabled={Boolean(togglingMCPID) || saving}
                    onCheckedChange={(value) => void setMCPEnabled(server, value)}
                  />
                </label>
              ))
            )}
          </SettingsSection>

          {/* Web search ------------------------------------------------------ */}
          <SettingsSection
            title={t('admin:settings.fields.searchSection')}
            className="mb-0"
            bodyClassName="divide-y-0"
          >
            <SettingsBlock>
              <div className={`admin-form-grid${searchProvider === 'searxng' ? '' : ' admin-form-grid--pair'}`}>
                <Field
                  label={t('admin:settings.fields.searchProvider')}
                  htmlFor="search-provider"
                >
                  <Select
                    value={searchProvider || 'none'}
                    disabled={saving}
                    onValueChange={(v) =>
                      setDraft({ ...draft, search_provider: v === 'none' ? '' : v })
                    }
                  >
                    <SelectTrigger id="search-provider" data-admin-tour="tools-search-provider">
                      <SelectValue placeholder={t('admin:settings.fields.searchProviderPlaceholder')} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="none">{t('admin:settings.fields.searchNone')}</SelectItem>
                      <SelectItem value="duckduckgo">{t('admin:settings.fields.searchDuckduckgo')}</SelectItem>
                      <SelectItem value="searxng">{t('admin:settings.fields.searchSearxng')}</SelectItem>
                      <SelectItem value="serper">{t('admin:settings.fields.searchSerper')}</SelectItem>
                      <SelectItem value="brave">{t('admin:settings.fields.searchBrave')}</SelectItem>
                      <SelectItem value="tavily">{t('admin:settings.fields.searchTavily')}</SelectItem>
                      <SelectItem value="anysearch">{t('admin:settings.fields.searchAnysearch')}</SelectItem>
                      <SelectItem value="youcom">You.com</SelectItem>
                      <SelectItem value="keenable">Keenable</SelectItem>
                      <SelectItem value="yuanbao">{t('admin:settings.fields.searchYuanbao')}</SelectItem>
                      <SelectItem value="deepseek">DeepSeek</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>

                {searchProvider === 'searxng' && (
                  <>
                    <Field
                      label={t('admin:settings.fields.searchBaseUrl')}
                      htmlFor="search-url"
                    >
                      <Input
                        id="search-url"
                        type="url"
                        placeholder="https://searxng.your-domain.tld"
                        value={readString('search_base_url')}
                        disabled={saving}
                        onChange={(e) => setDraft({ ...draft, search_base_url: e.target.value })}
                      />
                    </Field>
                    <Field
                      label={t('admin:settings.fields.searchEngines')}
                      htmlFor="search-engines"
                      hint={t('admin:settings.fields.searchEnginesHint')}
                    >
                      <Input
                        id="search-engines"
                        placeholder={t('admin:settings.fields.searchEnginesPlaceholder')}
                        value={readString('search_engines')}
                        disabled={saving}
                        onChange={(e) => setDraft({ ...draft, search_engines: e.target.value })}
                      />
                    </Field>
                    <Field
                      label={t('admin:settings.fields.searchResultCount')}
                      htmlFor="search-result-count"
                    >
                      <Input
                        id="search-result-count"
                        type="number"
                        min={1}
                        max={50}
                        step={1}
                        wrapperClassName="w-32"
                        value={typeof draft.search_result_count === 'number' || draft.search_result_count === ''
                          ? draft.search_result_count : 5}
                        disabled={saving}
                        onChange={(e) => setDraft({
                          ...draft,
                          search_result_count: e.target.value === '' ? '' : Number(e.target.value),
                        })}
                      />
                    </Field>
                  </>
                )}

                {searchProvider === 'yuanbao' && (
                  <>
                    <Field
                      label={t('admin:settings.fields.searchSecretId')}
                      htmlFor="search-secret-id"
                      hint={t('admin:settings.fields.searchSecretHint')}
                    >
                      <Input
                        id="search-secret-id"
                        type="password"
                        autoComplete="off"
                        value={readString('search_secret_id')}
                        disabled={saving}
                        onChange={(e) => setDraft((current) => ({ ...current, search_secret_id: e.target.value }))}
                      />
                    </Field>
                    <Field
                      label={t('admin:settings.fields.searchSecretKey')}
                      htmlFor="search-secret-key"
                    >
                      <Input
                        id="search-secret-key"
                        type="password"
                        autoComplete="new-password"
                        value={readString('search_secret_key')}
                        disabled={saving}
                        onChange={(e) => setDraft((current) => ({ ...current, search_secret_key: e.target.value }))}
                      />
                    </Field>
                  </>
                )}

                {['serper', 'brave', 'tavily', 'anysearch', 'youcom', 'keenable', 'deepseek'].includes(searchProvider) && (
                  <Field
                    label={t(['anysearch', 'youcom', 'keenable'].includes(searchProvider)
                      ? 'admin:settings.fields.searchApiKeyOptional'
                      : 'admin:settings.fields.searchApiKey')}
                    htmlFor="search-key"
                  >
                    <Input
                      id="search-key"
                      type="password"
                      autoComplete="off"
                      value={readString('search_api_key')}
                      disabled={saving}
                      onChange={(e) => {
                        const value = e.target.value
                        setDraft((current) => ({ ...current, search_api_key: value }))
                      }}
                    />
                  </Field>
                )}
              </div>
            </SettingsBlock>
          </SettingsSection>

          <SettingsSection title={t('admin:tools.fetchSection')} description={t('admin:tools.fetchDescription')} className="mb-0" bodyClassName="divide-y-0">
            <SettingsBlock>
              <div className="admin-form-grid admin-form-grid--pair">
                <Field label={t('admin:tools.fetchProvider')} htmlFor="fetch-provider">
                  <Select value={readString('fetch_provider') || 'builtin'} disabled={saving} onValueChange={value => setDraft(current => ({ ...current, fetch_provider: value }))}>
                    <SelectTrigger id="fetch-provider"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="builtin">{t('admin:tools.fetchBuiltin')}</SelectItem>
                      <SelectItem value="deepseek">DeepSeek</SelectItem>
                      <SelectItem value="anysearch">AnySearch</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                {(!readString('fetch_provider') || readString('fetch_provider') === 'builtin') ? <>
                  <Field label={t('admin:tools.fetchJinaBaseUrl')} htmlFor="fetch-jina-url" hint={t('admin:tools.fetchJinaHint')}>
                    <Input
                      id="fetch-jina-url"
                      type="url"
                      placeholder={readString('fetch_jina_default_base_url', 'https://r.jina.ai')}
                      value={readString('fetch_jina_base_url')}
                      disabled={saving}
                      onChange={event => setDraft(current => ({ ...current, fetch_jina_base_url: event.target.value }))}
                    />
                  </Field>
                  <Field label={t('admin:tools.fetchJinaUrlMode')} htmlFor="fetch-jina-mode">
                    <Select value={readString('fetch_jina_url_mode') || 'default'} disabled={saving} onValueChange={value => setDraft(current => ({ ...current, fetch_jina_url_mode: value === 'default' ? '' : value }))}>
                      <SelectTrigger id="fetch-jina-mode"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="default">{t('admin:tools.fetchJinaModeDefault')}</SelectItem>
                        <SelectItem value="escaped">{t('admin:tools.fetchJinaModeEscaped')}</SelectItem>
                        <SelectItem value="raw">{t('admin:tools.fetchJinaModeRaw')}</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                </> : (
                  <Field label={t(readString('fetch_provider') === 'anysearch' ? 'admin:settings.fields.searchApiKeyOptional' : 'admin:settings.fields.searchApiKey')} htmlFor="fetch-api-key" hint={t('admin:tools.fetchApiKeyHint')}>
                    <Input
                      id="fetch-api-key"
                      type="password"
                      autoComplete="new-password"
                      value={readString('fetch_api_key')}
                      disabled={saving}
                      onChange={event => setDraft(current => ({ ...current, fetch_api_key: event.target.value }))}
                    />
                  </Field>
                )}
              </div>
            </SettingsBlock>
          </SettingsSection>

          <SettingsSection title={t('admin:tools.mapSection')} className="mb-0" bodyClassName="divide-y-0">
            <SettingsBlock>
              <div className="admin-form-grid admin-form-grid--pair">
                <Field label={t('admin:tools.mapProvider')} htmlFor="map-provider">
                  <Select value={readString('map_provider') || 'osm'} disabled={saving} onValueChange={value => setDraft(current => ({ ...current, map_provider: value }))}>
                    <SelectTrigger id="map-provider"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="osm">{t('admin:tools.mapOSM')}</SelectItem>
                      <SelectItem value="amap">{t('admin:tools.mapAMap')}</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                {readString('map_provider') === 'amap' && <>
                  <Field label={t('admin:tools.mapWebKey')} htmlFor="map-amap-web-key" hint={t('admin:tools.mapWebKeyHint')}>
                    <Input
                      id="map-amap-web-key"
                      type="password"
                      autoComplete="new-password"
                      value={readString('map_amap_web_api_key')}
                      disabled={saving}
                      onChange={event => {
                        const value = event.target.value
                        setDraft(current => ({ ...current, map_amap_web_api_key: value }))
                      }}
                    />
                  </Field>
                  <Field label={t('admin:tools.mapJSKey')} htmlFor="map-amap-js-key" hint={t('admin:tools.mapJSKeyHint')}>
                    <Input
                      id="map-amap-js-key"
                      type="password"
                      autoComplete="new-password"
                      value={readString('map_amap_js_api_key')}
                      disabled={saving}
                      onChange={event => {
                        const value = event.target.value
                        setDraft(current => ({ ...current, map_amap_js_api_key: value }))
                      }}
                    />
                  </Field>
                  <Field label={t('admin:tools.mapSecurityCode')} htmlFor="map-amap-security-code" hint={t('admin:tools.mapSecurityCodeHint')}>
                    <Input
                      id="map-amap-security-code"
                      type="password"
                      autoComplete="new-password"
                      value={readString('map_amap_security_secret')}
                      disabled={saving}
                      onChange={event => {
                        const value = event.target.value
                        setDraft(current => ({ ...current, map_amap_security_secret: value }))
                      }}
                    />
                  </Field>
                </>}
              </div>
            </SettingsBlock>
          </SettingsSection>

          {/* Code sandbox ---------------------------------------------------- */}
          <SettingsSection
            title={t('admin:settings.fields.sandboxSection')}
            className="mb-0"
            bodyClassName="divide-y-0"
          >
            <SettingsBlock>
              {!sandboxConfigured ? (
                <p className="mb-4 flex items-center gap-2 text-xs text-[var(--color-fg-muted)]" role="status">
                  <AlertTriangle size={14} aria-hidden className="shrink-0" />
                  <span>{t('admin:settings.fields.sandboxMissing')}</span>
                </p>
              ) : null}
              <div className="admin-form-grid admin-form-grid--pair">
                <Field
                  label={t('admin:settings.fields.sandboxUrl')}
                  htmlFor="sandbox-url"
                  hint={t('admin:settings.fields.sandboxUrlHint')}
                >
                  <Input
                    id="sandbox-url"
                    data-admin-tour="tools-sandbox-url"
                    name="sandbox_base_url"
                    type="url"
                    autoComplete="off"
                    placeholder="http://your-server:48217"
                    value={readString('sandbox_base_url')}
                    disabled={saving}
                    onChange={(e) => setDraft({ ...draft, sandbox_base_url: e.target.value })}
                  />
                </Field>
                <Field
                  label={t('admin:settings.fields.sandboxKey')}
                  htmlFor="sandbox-key"
                  hint={t('admin:settings.fields.sandboxKeyHint')}
                >
                  <Input
                    id="sandbox-key"
                    name="sandbox_api_key"
                    type="password"
                    autoComplete="new-password"
                    value={readString('sandbox_api_key')}
                    disabled={saving}
                    onChange={(e) => setDraft({ ...draft, sandbox_api_key: e.target.value })}
                  />
                </Field>
                <Field
                  label={t('admin:settings.fields.sandboxExecTimeout')}
                  htmlFor="sandbox-exec-timeout"
                >
                  <Input
                    id="sandbox-exec-timeout"
                    type="number"
                    min={10}
                    max={600}
                    wrapperClassName="w-40"
                    placeholder="120"
                    value={readString('sandbox_exec_timeout_sec')}
                    disabled={saving}
                    onChange={(e) => setDraft({ ...draft, sandbox_exec_timeout_sec: e.target.value })}
                  />
                </Field>
                <Field
                  label={t('admin:settings.fields.sandboxIdleTtl')}
                  htmlFor="sandbox-idle-ttl"
                >
                  <Input
                    id="sandbox-idle-ttl"
                    type="number"
                    min={60}
                    max={86400}
                    wrapperClassName="w-40"
                    placeholder="1800"
                    value={readString('sandbox_idle_ttl_sec')}
                    disabled={saving}
                    onChange={(e) => setDraft({ ...draft, sandbox_idle_ttl_sec: e.target.value })}
                  />
                </Field>
              </div>
            </SettingsBlock>
          </SettingsSection>

          <div className="flex justify-end">
            <Button loading={saving} onClick={() => void save()}>
              {t('common:actions.save')}
            </Button>
          </div>
        </section>
      )}
    </div>
  )
}
