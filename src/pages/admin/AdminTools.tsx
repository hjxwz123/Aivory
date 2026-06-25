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
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'search_provider',
  'search_base_url',
  'search_api_key',
  'sandbox_base_url',
  'sandbox_api_key',
  'sandbox_exec_timeout_sec',
] as const

export default function AdminTools() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const s = await adminApi.settings()
      setDraft(s)
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

  const searchProvider = readString('search_provider')

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:tools.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:tools.lead')}</p>
      </header>

      {loading ? (
        <div className="mt-8 text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
      ) : (
        <section className="mt-8 flex flex-col gap-5">
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
                  type="url"
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
                  type="password"
                  autoComplete="off"
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
