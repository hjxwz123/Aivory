import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiSystemPromptConfig } from '@/api/types'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/components/ui/label'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

const GROUPS = ['routing', 'memory', 'moderation', 'conversation', 'rag', 'research', 'image', 'tools']
const LOCALES = ['zh', 'zh-Hant', 'en', 'ja', 'fr']

export default function AdminSystemPrompts() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [config, setConfig] = useState<ApiSystemPromptConfig | null>(null)
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [query, setQuery] = useState('')
  const [group, setGroup] = useState('all')
  const [engine, setEngine] = useState('all')
  const [locale, setLocale] = useState(() => LOCALES.includes(i18n.language) ? i18n.language : 'en')
  const [selectedId, setSelectedId] = useState('decision.tool_scope.instructions')
  const listRef = useRef<HTMLElement>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const result = await adminApi.systemPrompts()
      setConfig(result)
      setDraft(result.overrides)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => { void load() }, [load])

  const changed = useMemo(() => Object.fromEntries(
    (config?.prompts ?? [])
      .filter((p) => (draft[p.id] ?? '') !== (config?.overrides[p.id] ?? ''))
      .map((p) => [p.id, draft[p.id] ?? '']),
  ), [config, draft])
  const changeCount = Object.keys(changed).length

  useEffect(() => {
    if (!changeCount) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [changeCount])

  const visible = useMemo(() => {
    const text = query.trim().toLocaleLowerCase()
    return (config?.prompts ?? []).filter((p) =>
      (group === 'all' || p.group === group) &&
      (engine === 'all' || p.engine === engine) &&
      (locale === 'all' || !p.locale || p.locale === locale) &&
      (!text || `${p.name} ${p.id} ${p.format} ${p.notes} ${p.default} ${draft[p.id] ?? ''}`.toLocaleLowerCase().includes(text)),
    )
  }, [config, draft, query, group, engine, locale])
  const selected = visible.find((p) => p.id === selectedId) ?? visible[0]
  useEffect(() => {
    const list = listRef.current
    const item = list?.querySelector<HTMLElement>('[aria-current="true"]')
    if (!list || !item) return
    const frame = list.getBoundingClientRect()
    const row = item.getBoundingClientRect()
    if (row.top < frame.top) list.scrollTop += row.top - frame.top
    else if (row.bottom > frame.bottom) list.scrollTop += row.bottom - frame.bottom
  }, [selected?.id])
  const value = selected ? draft[selected.id] ?? '' : ''
  const byteCount = new TextEncoder().encode(value).length
  const maxBytes = selected?.max_bytes ?? config?.max_prompt_bytes ?? 65536
  const tooLong = !!config && config.prompts.some((p) => changed[p.id] !== undefined && new TextEncoder().encode(changed[p.id]).length > (p.max_bytes ?? config.max_prompt_bytes))
  const invalidVariables = !!config && config.prompts.some((p) => changed[p.id]?.trim() && p.variables?.some((variable) => !changed[p.id].includes(variable)))
  const missingVariables = selected?.variables?.filter((variable) => value.trim() && !value.includes(variable)) ?? []

  async function save() {
    setSaving(true)
    try {
      const result = await adminApi.updateSystemPrompts(changed)
      setConfig(result)
      setDraft(result.overrides)
      toast.success(t('admin:systemPrompts.saved'))
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div>
      <AdminPageHeader
        title={t('admin:systemPrompts.title')}
        description={t('admin:systemPrompts.lead')}
        showDescription
        actionsClassName="w-full sm:w-auto"
        actions={(
          <div className="flex items-center gap-2">
            <Button variant="outline" disabled={!changeCount || saving} onClick={() => setDraft(config?.overrides ?? {})}>
              {t('admin:systemPrompts.discard')}
            </Button>
            <Button disabled={!changeCount || saving || tooLong || invalidVariables} onClick={() => void save()}>
              {saving ? t('admin:common.saving') : t('admin:systemPrompts.save', { count: changeCount })}
            </Button>
          </div>
        )}
      />
      <p className="mt-3 text-xs leading-5 text-[var(--color-fg-muted)]">{t('admin:systemPrompts.scopeNote')}</p>

      {loading ? <PanelFallback /> : error ? (
        <div role="alert" className="mt-8 space-y-3">
          <p className="text-sm">{error}</p>
          <Button variant="outline" onClick={() => void load()}>{t('admin:systemPrompts.retry')}</Button>
        </div>
      ) : config ? (
        <>
          <div className="mt-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-[minmax(12rem,1fr)_repeat(3,minmax(8rem,auto))]">
            <Input aria-label={t('admin:systemPrompts.search')} placeholder={t('admin:systemPrompts.search')} value={query} onChange={(e) => setQuery(e.target.value)} />
            <Select value={group} onValueChange={setGroup}>
              <SelectTrigger aria-label={t('admin:systemPrompts.group')}><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('admin:systemPrompts.allGroups')}</SelectItem>
                {GROUPS.map((key) => <SelectItem key={key} value={key}>{t(`admin:systemPrompts.groups.${key}`)}</SelectItem>)}
              </SelectContent>
            </Select>
            <Select value={engine} onValueChange={setEngine}>
              <SelectTrigger aria-label={t('admin:systemPrompts.engine')}><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('admin:systemPrompts.allEngines')}</SelectItem>
                {['chat', 'decision', 'shared'].map((key) => <SelectItem key={key} value={key}>{t(`admin:systemPrompts.engines.${key}`)}</SelectItem>)}
              </SelectContent>
            </Select>
            <Select value={locale} onValueChange={setLocale}>
              <SelectTrigger aria-label={t('admin:systemPrompts.locale')}><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('admin:systemPrompts.allLocales')}</SelectItem>
                {LOCALES.map((key) => <SelectItem key={key} value={key}>{key}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>
          <p className="my-3 text-xs text-[var(--color-fg-muted)]" aria-live="polite">
            {t('admin:systemPrompts.count', { count: visible.length, total: config.prompts.length })}
            {changeCount > 0 && ` · ${t('admin:systemPrompts.unsaved', { count: changeCount })}`}
          </p>
          <div className="grid min-w-0 gap-6 lg:grid-cols-[minmax(14rem,18rem)_minmax(0,1fr)]">
            <nav ref={listRef} aria-label={t('admin:systemPrompts.list')} className="max-h-64 space-y-1 overflow-y-auto overscroll-contain border-y border-[var(--color-border)] py-2 lg:max-h-[70vh] lg:border-y-0 lg:border-r lg:pr-4">
              {visible.length === 0 && <p className="p-3 text-sm text-[var(--color-fg-muted)]">{t('admin:systemPrompts.empty')}</p>}
              {visible.map((p) => (
                <button
                  type="button"
                  key={p.id}
                  aria-current={selected?.id === p.id ? 'true' : undefined}
                  onClick={() => setSelectedId(p.id)}
                  className={cn('w-full rounded-md px-3 py-2.5 text-left text-sm transition-colors hover:bg-[var(--color-bg-hover)] focus-visible:outline-2 focus-visible:outline-offset-2', selected?.id === p.id && 'bg-[var(--color-bg-muted)]')}
                >
                  <span className="flex items-start justify-between gap-2">
                    <span>{p.name}</span>
                    <span className="shrink-0 text-xs text-[var(--color-fg-subtle)]">{p.locale ?? t(`admin:systemPrompts.engines.${p.engine}`)}</span>
                  </span>
                  <span className="mt-1 block break-all font-mono text-[11px] text-[var(--color-fg-subtle)]">{p.id}</span>
                  <span className="mt-1 block text-[11px] text-[var(--color-fg-muted)]">
                    {(draft[p.id] ?? '').trim() ? t('admin:systemPrompts.custom') : t('admin:systemPrompts.defaultStatus')}
                    {(draft[p.id] ?? '') !== (config.overrides[p.id] ?? '') && ` · ${t('admin:systemPrompts.modified')}`}
                  </span>
                </button>
              ))}
            </nav>
            {selected && (
              <section aria-labelledby="prompt-editor-heading" className="min-w-0 space-y-5">
                <div>
                  <h2 id="prompt-editor-heading" className="text-base font-semibold">{selected.name}</h2>
                  <p className="mt-1 break-all font-mono text-xs text-[var(--color-fg-muted)]">{selected.id}</p>
                </div>
                <div className="space-y-2 border-l-2 border-[var(--color-border)] pl-3 text-xs leading-5 text-[var(--color-fg-muted)]">
                  <p><strong className="text-[var(--color-fg)]">{t('admin:systemPrompts.format')}：</strong>{selected.format}</p>
                  <p>{selected.notes}</p>
                  {!!selected.variables?.length && <p>{t('admin:systemPrompts.variables')}：<code>{selected.variables.join(' · ')}</code></p>}
                </div>
                <Field label={t('admin:systemPrompts.override')} htmlFor="system-prompt-override" hint={t('admin:systemPrompts.blankHint')}>
                  <Textarea
                    id="system-prompt-override"
                    value={value}
                    rows={12}
                    disabled={saving}
                    onChange={(e) => setDraft((old) => ({ ...old, [selected.id]: e.target.value }))}
                    placeholder={t('admin:systemPrompts.blankHint')}
                    spellCheck={false}
                    aria-invalid={byteCount > maxBytes || missingVariables.length > 0}
                    aria-describedby="system-prompt-help"
                    className="resize-y font-mono text-[13px] leading-6"
                  />
                  <div id="system-prompt-help" className="mt-2 flex flex-wrap justify-between gap-2 text-xs text-[var(--color-fg-muted)]">
                    <span>{byteCount.toLocaleString()} / {maxBytes.toLocaleString()} bytes</span>
                    <span>{value.trim() ? t('admin:systemPrompts.custom') : t('admin:systemPrompts.defaultStatus')}</span>
                  </div>
                  {missingVariables.length > 0 && <p role="alert" className="mt-2 text-xs">{t('admin:systemPrompts.missingVariables', { variables: missingVariables.join(', ') })}</p>}
                  {tooLong && <p role="alert" className="mt-2 text-xs">{t('admin:systemPrompts.tooLong')}</p>}
                </Field>
                <div className="flex flex-wrap gap-2">
                  <Button variant="outline" disabled={saving || !value} onClick={() => setDraft((old) => ({ ...old, [selected.id]: '' }))}>{t('admin:systemPrompts.reset')}</Button>
                  <Button variant="ghost" disabled={saving || !selected.default} onClick={() => setDraft((old) => ({ ...old, [selected.id]: selected.default }))}>{t('admin:systemPrompts.copyDefault')}</Button>
                </div>
                <details key={selected.id} className="border-t border-[var(--color-border)] pt-4">
                  <summary className="cursor-pointer text-sm font-medium">{t('admin:systemPrompts.builtin')}</summary>
                  <pre className="mt-3 max-h-[32rem] overflow-auto whitespace-pre-wrap break-words rounded-md bg-[var(--color-bg-muted)] p-4 font-mono text-xs leading-6">{selected.default || t('admin:systemPrompts.noDefault')}</pre>
                </details>
              </section>
            )}
          </div>
        </>
      ) : null}
    </div>
  )
}
