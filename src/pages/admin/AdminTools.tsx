/**
 * AdminTools — outbound services the assistant invokes during a conversation:
 * web search (SearXNG / Serper / Brave) and the code sandbox sidecar.
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

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'disabled_tools',
  'search_provider',
  'search_base_url',
  'search_api_key',
  'search_engines',
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
    savingRef.current = true
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const k of OWNED_KEYS) {
        if (k in draft) patch[k] = draft[k]
      }
      const saved = await adminApi.updateSettings(patch)
      setDraft((current) => {
        const next = { ...current }
        for (const key of OWNED_KEYS) {
          if (key in saved) next[key] = saved[key]
        }
        if ('sandbox_configured' in saved) next.sandbox_configured = saved.sandbox_configured
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

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:tools.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:tools.lead')}</p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : settingsLoadFailed ? (
        <div className="mt-8 flex min-h-64 flex-col items-center justify-center rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center">
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
        <section className="mt-8 flex flex-col gap-5" aria-busy={saving || Boolean(togglingMCPID)}>
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">
              {t('admin:tools.availabilityTitle', { defaultValue: 'Global tool availability' })}
            </h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              {t('admin:tools.availabilityLead', {
                defaultValue: 'A disabled tool is removed from every model, including models that explicitly allow it.',
              })}
            </p>
            {builtinToolsLoadFailed ? (
              <div className="mt-4 flex flex-col items-start gap-2 border-y border-[var(--color-divider)] py-4">
                <p className="text-sm text-[var(--color-danger)]">{t('admin:tools.builtinLoadFailed')}</p>
                <Button size="sm" variant="ghost" leadingIcon={<RefreshCw size={14} aria-hidden />} loading={builtinToolsRefreshing} onClick={() => void retryBuiltinTools()}>
                  {t('common:actions.tryAgain')}
                </Button>
              </div>
            ) : builtinTools.length === 0 ? (
              <p className="mt-4 text-sm text-[var(--color-fg-muted)]">
                {t('admin:tools.availabilityEmpty', { defaultValue: 'No platform tools are registered.' })}
              </p>
            ) : (
              <div className="mt-4 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
                {builtinTools.map((tool) => {
                  const requiresSandbox = tool.name === 'python_execute'
                  const unavailable = requiresSandbox && !sandboxConfigured
                  const enabled = !unavailable && !readStringArray('disabled_tools').includes(tool.name)
                  return (
                    <label key={tool.name} className="flex min-h-14 items-center gap-4 py-2.5">
                      <span className="min-w-0 flex-1">
                        <span className="block text-[13px] font-medium text-[var(--color-fg)]">
                          {t(`admin:models.builtinTools.names.${tool.name}`, { defaultValue: tool.name })}
                        </span>
                        <span className="mt-0.5 block text-[12px] leading-4 text-[var(--color-fg-subtle)]">
                          {t(`admin:models.builtinTools.descriptions.${tool.name}`, { defaultValue: tool.description })}
                        </span>
                      </span>
                      <span className="flex shrink-0 items-center gap-2.5">
                        {unavailable ? (
                          <span className="max-w-[8.5rem] text-right text-[11px] leading-4 text-[var(--color-fg-subtle)] sm:max-w-none sm:whitespace-nowrap">
                            {t('admin:tools.configureSandboxFirst')}
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
                })}
              </div>
            )}
          </div>

          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div>
                <h2 className="font-serif text-lg text-[var(--color-fg)]">
                  {t('admin:tools.mcpAvailabilityTitle')}
                </h2>
                <p className="mt-1 max-w-2xl text-xs text-[var(--color-fg-subtle)]">
                  {t('admin:tools.mcpAvailabilityLead')}
                </p>
              </div>
              <Button
                asChild
                size="sm"
                variant="secondary"
                leadingIcon={<Settings2 size={13} aria-hidden />}
                className="shrink-0 max-sm:min-h-[var(--tap-min)]"
              >
                <Link to="/admin/mcp">{t('admin:tools.manageMCP')}</Link>
              </Button>
            </div>
            {mcpLoadFailed ? (
              <div className="mt-4 flex flex-col items-start gap-2 border-y border-[var(--color-divider)] py-4">
                <p className="text-sm text-[var(--color-danger)]">{t('admin:tools.mcpLoadFailed')}</p>
                <Button size="sm" variant="ghost" leadingIcon={<RefreshCw size={14} aria-hidden />} loading={mcpRefreshing} onClick={() => void retryMCPServers()}>
                  {t('common:actions.tryAgain')}
                </Button>
              </div>
            ) : mcpServers.length === 0 ? (
              <p className="mt-4 text-sm text-[var(--color-fg-muted)]">{t('admin:tools.mcpAvailabilityEmpty')}</p>
            ) : (
              <div className="mt-4 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
                {mcpServers.map((server) => (
                  <label key={server.id} htmlFor={`tool-mcp-${server.id}`} className="flex min-h-14 items-center gap-3 py-2.5">
                    <span className="grid size-8 shrink-0 place-items-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                      <LucideGlyph name={server.icon || 'Blocks'} size={15} aria-hidden />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-[13px] font-medium text-[var(--color-fg)]">{server.name}</span>
                      <span className="mt-0.5 block text-[12px] leading-4 text-[var(--color-fg-subtle)]">
                        {server.description}
                      </span>
                      {server.last_error ? (
                        <span className="mt-0.5 block text-[11px] leading-4 text-[var(--color-danger)]">
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
                ))}
              </div>
            )}
          </div>

          {/* Web search ------------------------------------------------------ */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:settings.fields.searchSection')}</h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:settings.fields.searchLead')}</p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.searchProvider')}
                htmlFor="search-provider"
                hint={t('admin:settings.fields.searchProviderHint')}
              >
                <Select
                  value={searchProvider || 'none'}
                  disabled={saving}
                  onValueChange={(v) =>
                    setDraft({ ...draft, search_provider: v === 'none' ? '' : v })
                  }
                >
                  <SelectTrigger id="search-provider">
                    <SelectValue placeholder={t('admin:settings.fields.searchProviderPlaceholder')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t('admin:settings.fields.searchNone')}</SelectItem>
                    <SelectItem value="searxng">{t('admin:settings.fields.searchSearxng')}</SelectItem>
                    <SelectItem value="serper">{t('admin:settings.fields.searchSerper')}</SelectItem>
                    <SelectItem value="brave">{t('admin:settings.fields.searchBrave')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>

              {searchProvider === 'searxng' && (
                <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
                  <Field
                    label={t('admin:settings.fields.searchBaseUrl')}
                    htmlFor="search-url"
                    hint={t('admin:settings.fields.searchBaseUrlHint')}
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
                </div>
              )}

              {(searchProvider === 'serper' || searchProvider === 'brave') && (
                <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
                  <Field
                    label={t('admin:settings.fields.searchApiKey')}
                    htmlFor="search-key"
                    hint={t('admin:settings.fields.searchApiKeyHint')}
                  >
                    <Input
                      id="search-key"
                      type="password"
                      autoComplete="off"
                      value={readString('search_api_key')}
                      disabled={saving}
                      onChange={(e) => setDraft({ ...draft, search_api_key: e.target.value })}
                    />
                  </Field>
                </div>
              )}
            </div>
          </div>

          {/* Code sandbox ---------------------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:settings.fields.sandboxSection')}</h2>
            {!sandboxConfigured ? (
              <p className="mt-2 flex items-center gap-2 text-xs text-[var(--color-fg-muted)]" role="status">
                <AlertTriangle size={14} aria-hidden className="shrink-0" />
                <span>{t('admin:settings.fields.sandboxMissing')}</span>
              </p>
            ) : null}
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.sandboxUrl')}
                htmlFor="sandbox-url"
                hint={t('admin:settings.fields.sandboxUrlHint')}
              >
                <Input
                  id="sandbox-url"
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
                hint={t('admin:settings.fields.sandboxExecTimeoutHint')}
              >
                <Input
                  id="sandbox-exec-timeout"
                  type="number"
                  min={10}
                  max={600}
                  placeholder="120"
                  value={readString('sandbox_exec_timeout_sec')}
                  disabled={saving}
                  onChange={(e) => setDraft({ ...draft, sandbox_exec_timeout_sec: e.target.value })}
                />
              </Field>
              <Field
                label={t('admin:settings.fields.sandboxIdleTtl')}
                htmlFor="sandbox-idle-ttl"
                hint={t('admin:settings.fields.sandboxIdleTtlHint')}
              >
                <Input
                  id="sandbox-idle-ttl"
                  type="number"
                  min={60}
                  max={86400}
                  placeholder="1800"
                  value={readString('sandbox_idle_ttl_sec')}
                  disabled={saving}
                  onChange={(e) => setDraft({ ...draft, sandbox_idle_ttl_sec: e.target.value })}
                />
              </Field>
            </div>
          </div>

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
