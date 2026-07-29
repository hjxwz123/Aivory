/**
 * AdminTools — outbound services the assistant invokes during a conversation:
 * web search (SearXNG / Serper / Brave) and the code sandbox sidecar.
 *
 * Shares the global `/admin/settings` endpoint with other admin pages; PATCH
 * is scoped to the keys this page owns so concurrent edits don't clobber
 * fields managed elsewhere.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiBuiltinTool } from '@/api/types'
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
  'sandbox_base_url',
  'sandbox_api_key',
  'sandbox_exec_timeout_sec',
  'sandbox_idle_ttl_sec',
] as const

export default function AdminTools() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [builtinTools, setBuiltinTools] = useState<ApiBuiltinTool[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const [s, tools] = await Promise.all([
        adminApi.settings(),
        adminApi.builtinTools().catch(() => [] as ApiBuiltinTool[]),
      ])
      setDraft(s)
      setBuiltinTools(tools)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function save() {
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const k of OWNED_KEYS) {
        if (k in draft) patch[k] = draft[k]
      }
      await adminApi.updateSettings(patch)
      toast.success(t('admin:settings.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
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

  function setToolEnabled(name: string, enabled: boolean) {
    const disabled = new Set(readStringArray('disabled_tools'))
    if (enabled) disabled.delete(name)
    else disabled.add(name)
    setDraft({ ...draft, disabled_tools: [...disabled] })
  }

  const searchProvider = readString('search_provider')

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:tools.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:tools.lead')}</p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">
              {t('admin:tools.availabilityTitle', { defaultValue: 'Global tool availability' })}
            </h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              {t('admin:tools.availabilityLead', {
                defaultValue: 'A disabled tool is removed from every model, including models that explicitly allow it.',
              })}
            </p>
            {builtinTools.length === 0 ? (
              <p className="mt-4 text-sm text-[var(--color-fg-muted)]">
                {t('admin:tools.availabilityEmpty', { defaultValue: 'No platform tools are registered.' })}
              </p>
            ) : (
              <div className="mt-4 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
                {builtinTools.map((tool) => {
                  const enabled = !readStringArray('disabled_tools').includes(tool.name)
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
                      <Switch checked={enabled} onCheckedChange={(value) => setToolEnabled(tool.name, value)} />
                    </label>
                  )
                })}
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
                      onChange={(e) => setDraft({ ...draft, search_base_url: e.target.value })}
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
