/**
 * AdminModelEdit — full settings page for one model.
 *
 * Opened from the gear-icon "Settings" button on the AdminModels list. Three
 * sectioned blocks:
 *   1. Basic          — channel, kind, label, request_id, icon, description, enabled
 *   2. Model behaviour — chat tools/vision/system prompt and chat/image param_controls
 *   3. Pricing        — chat: in/out/cache_read/cache_write · image: per-image · embedding: dim
 *
 * No GET-single endpoint upstream — we re-fetch the model list and find by ID,
 * which is cheap (admin model lists are small) and stays consistent with how
 * the list page reads.
 */
import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  BookOpen,
  Check,
  Globe,
  Image as ImageIcon,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  SquareTerminal,
  Trash2,
  Wrench,
} from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type {
  ApiBuiltinTool,
  ApiChannel,
  ApiModel,
  ApiModelTag,
  ApiOfficialToolDefinition,
  ApiSkill,
} from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { IconPicker } from '@/components/admin/icon-picker'
import { IconUploader } from '@/components/admin/icon-uploader'
import { ParamControlsEditor } from '@/components/admin/param-controls-editor'
import { ModelQuotaEditor } from '@/components/admin/model-quota-editor'
import { toast } from '@/hooks/use-toast'
import { resolveBuiltinToolNames, toggleBuiltinToolName } from '@/lib/builtin-tools'
import { cn } from '@/lib/utils'
import { PanelFallback } from '@/components/ui/panel-fallback'

const KINDS = ['chat', 'image', 'embedding'] as const
const TOOL_MODES = ['native', 'prompt', 'none'] as const
const BUILTIN_TOOL_ICONS: Record<string, typeof Wrench> = {
  web_search: Search,
  web_fetch: Globe,
  python_execute: SquareTerminal,
  image_generate: ImageIcon,
  search_knowledge_base: BookOpen,
  use_skill: BookOpen,
  save_memory: Sparkles,
}
type OfficialToolDraft = Omit<ApiOfficialToolDefinition, 'request'> & { request_text: string }
type Draft = Partial<ApiModel> & {
  param_controls_text: string
  extra_params_text: string
  official_tools_draft: OfficialToolDraft[]
  official_tools_dirty: boolean
}

function pcToText(pc: unknown): string {
  if (typeof pc === 'string') return pc
  try {
    return JSON.stringify(pc ?? [], null, 2)
  } catch {
    return '[]'
  }
}

function humanizeToolName(name: string): string {
  return name.replace(/[_-]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function extraParamsToText(params: unknown): string {
  if (typeof params === 'string') return params
  try {
    return JSON.stringify(params ?? {}, null, 2)
  } catch {
    return '{}'
  }
}

type ExtraParamsValidation =
  | { valid: true; value: Record<string, unknown> }
  | { valid: false; error: 'invalidJSON' | 'notObject' }

function parseExtraParams(text: string): ExtraParamsValidation {
  const trimmed = text.trim()
  if (!trimmed) return { valid: true, value: {} }
  try {
    const parsed: unknown = JSON.parse(trimmed)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return { valid: false, error: 'notObject' }
    }
    return { valid: true, value: parsed as Record<string, unknown> }
  } catch {
    return { valid: false, error: 'invalidJSON' }
  }
}

function legacyOfficialToolDraft(value: unknown): OfficialToolDraft | null {
  if (typeof value !== 'string') return null
  const name = value.trim()
  if (!name) return null
  const known: Record<string, { icon: string; request: Record<string, unknown> }> = {
    web_search: {
      icon: 'search',
      request: { tools: [{ type: 'web_search', search_context_size: 'medium' }] },
    },
    code_interpreter: {
      icon: 'terminal',
      request: { tools: [{ type: 'code_interpreter', container: { type: 'auto' } }] },
    },
    image_generation: {
      icon: 'image',
      request: { tools: [{ type: 'image_generation' }] },
    },
  }
  const fallback = { icon: 'wrench', request: { tools: [{ type: name }] } }
  const definition = known[name] ?? fallback
  return {
    name,
    icon: definition.icon,
    request_text: extraParamsToText(definition.request),
  }
}

function modelToDraft(m: ApiModel): Draft {
  const officialTools = Array.isArray(m.official_tools)
    ? (m.official_tools as unknown[]).flatMap((tool) => {
        const legacy = legacyOfficialToolDraft(tool)
        if (legacy) return [legacy]
        if (typeof tool !== 'object' || tool === null || typeof (tool as ApiOfficialToolDefinition).name !== 'string') {
          return []
        }
        const definition = tool as ApiOfficialToolDefinition
        return [{
          name: definition.name,
          icon: typeof definition.icon === 'string' ? definition.icon : '',
          request_text: extraParamsToText(definition.request),
        }]
      })
    : []
  return {
    ...m,
    param_controls_text: pcToText(m.param_controls),
    extra_params_text: extraParamsToText(m.extra_params),
    official_tools_draft: officialTools,
    official_tools_dirty: false,
  }
}

export default function AdminModelEdit() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const { id = '' } = useParams<{ id: string }>()
  const [channels, setChannels] = useState<ApiChannel[]>([])
  const [allTags, setAllTags] = useState<ApiModelTag[]>([])
  const [allSkills, setAllSkills] = useState<ApiSkill[]>([])
  const [builtinTools, setBuiltinTools] = useState<ApiBuiltinTool[]>([])
  const [builtinToolsLoading, setBuiltinToolsLoading] = useState(true)
  const [builtinToolsError, setBuiltinToolsError] = useState(false)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [loading, setLoading] = useState(true)
  const [notFound, setNotFound] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      setBuiltinToolsLoading(true)
      setBuiltinToolsError(false)
      try {
        const [c, m, tg, sk, bt] = await Promise.all([
          adminApi.channels(),
          adminApi.models(),
          adminApi.modelTags().catch(() => [] as ApiModelTag[]),
          adminApi.skills().catch(() => [] as ApiSkill[]),
          // Keep the rest of model editing available during a rolling deploy
          // where an older backend may not expose the registry endpoint yet,
          // without mistaking that failure for a real empty registry.
          adminApi
            .builtinTools()
            .then((tools) => ({ tools, failed: false }))
            .catch(() => ({ tools: [] as ApiBuiltinTool[], failed: true })),
        ])
        if (cancelled) return
        setChannels(c)
        setAllTags(tg)
        setAllSkills(sk)
        setBuiltinTools(bt.tools)
        setBuiltinToolsError(bt.failed)
        const found = m.find((row) => row.id === id)
        if (!found) {
          setNotFound(true)
          setDraft(null)
        } else {
          setNotFound(false)
          setDraft(modelToDraft(found))
        }
      } catch (e) {
        if (!cancelled) toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      } finally {
        if (!cancelled) {
          setBuiltinToolsLoading(false)
          setLoading(false)
        }
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [id, t])

  function patch(p: Partial<Draft>) {
    setDraft((d) => (d ? { ...d, ...p } : d))
  }

  function toggleTag(tagId: string) {
    setDraft((d) => {
      if (!d) return d
      const cur = d.tags ?? []
      return { ...d, tags: cur.includes(tagId) ? cur.filter((x) => x !== tagId) : [...cur, tagId] }
    })
  }

  function toggleSkill(skillId: string) {
    setDraft((d) => {
      if (!d) return d
      const cur = d.skills ?? []
      return { ...d, skills: cur.includes(skillId) ? cur.filter((x) => x !== skillId) : [...cur, skillId] }
    })
  }

  function toggleBuiltinTool(name: string) {
    const availableNames = builtinTools.map((tool) => tool.name)
    setDraft((current) =>
      current
        ? {
            ...current,
            builtin_tools: toggleBuiltinToolName(current.builtin_tools, availableNames, name),
          }
        : current,
    )
  }

  async function retryBuiltinTools() {
    if (builtinToolsLoading) return
    setBuiltinToolsLoading(true)
    try {
      const tools = await adminApi.builtinTools()
      setBuiltinTools(tools)
      setBuiltinToolsError(false)
    } catch (e) {
      setBuiltinToolsError(true)
      toast.error(e instanceof ApiError ? e.message : t('admin:models.fields.builtinToolsLoadFailed'))
    } finally {
      setBuiltinToolsLoading(false)
    }
  }

  const channel = channels.find((c) => c.id === draft?.channel_id)
  const extraParamsValidation = draft?.kind === 'chat' ? parseExtraParams(draft.extra_params_text) : null
  const extraParamsError =
    extraParamsValidation && !extraParamsValidation.valid
      ? t(
          extraParamsValidation.error === 'invalidJSON'
            ? 'admin:models.errors.invalidExtraParamsJSON'
            : 'admin:models.errors.extraParamsMustBeObject',
        )
      : undefined

  // §fallback channel: the backup must match the primary's type + api_format (the
  // retry reuses the primary provider's wire format — only URL/key differ). Show
  // compatible channels only, but always keep the current selection visible.
  const fallbackOptions = channels.filter(
    (c) =>
      c.id !== draft?.channel_id &&
      ((c.type === channel?.type && (c.api_format ?? '') === (channel?.api_format ?? '')) ||
        c.id === draft?.fallback_channel_id),
  )
  const builtinToolNames = builtinTools.map((tool) => tool.name)
  const selectedBuiltinToolNames = resolveBuiltinToolNames(draft?.builtin_tools, builtinToolNames)
  const selectedBuiltinToolSet = new Set(selectedBuiltinToolNames)
  const builtinToolsUseDefault = draft?.builtin_tools == null

  async function save() {
    if (!draft) return
    if (!draft.channel_id || !draft.label?.trim() || !draft.request_id?.trim()) {
      toast.error(t('admin:models.errors.missingFields'))
      return
    }
    let parsedPC: unknown = []
    try {
      parsedPC = JSON.parse(draft.param_controls_text || '[]')
    } catch {
      toast.error(t('admin:models.errors.invalidJSON'))
      return
    }
    const parsedExtraParams = draft.kind === 'chat' ? parseExtraParams(draft.extra_params_text) : null
    if (parsedExtraParams && !parsedExtraParams.valid) {
      toast.error(
        t(
          parsedExtraParams.error === 'invalidJSON'
            ? 'admin:models.errors.invalidExtraParamsJSON'
            : 'admin:models.errors.extraParamsMustBeObject',
        ),
      )
      return
    }
    const officialTools: ApiOfficialToolDefinition[] = []
    const seenOfficialToolNames = new Set<string>()
    for (const tool of draft.official_tools_draft) {
      const name = tool.name.trim()
      if (!name) {
        toast.error(t('admin:models.errors.officialToolNameRequired', { defaultValue: 'Every provider tool needs a name.' }))
        return
      }
      if (seenOfficialToolNames.has(name)) {
        toast.error(t('admin:models.errors.officialToolNameDuplicate', { defaultValue: 'Provider tool names must be unique.' }))
        return
      }
      const parsed = parseExtraParams(tool.request_text)
      if (!parsed.valid) {
        toast.error(t('admin:models.errors.officialToolJSONInvalid', { name, defaultValue: `Invalid request JSON for ${name}.` }))
        return
      }
      seenOfficialToolNames.add(name)
      officialTools.push({ name, icon: tool.icon.trim(), request: parsed.value })
    }
    setSaving(true)
    try {
      // skills bind through their own endpoint (model_skills, §4.17), so keep
      // them out of the model PATCH payload.
      const {
        param_controls_text: _omit,
        extra_params_text: _omitExtraParams,
        extra_params: _omitExtraParamsValue,
        official_tools_draft: _omitOfficialToolDraft,
        official_tools_dirty: officialToolsDirty,
        official_tools: _omitOfficialTools,
        builtin_tools: builtinToolsConfig,
        skills: skillIds,
        ...rest
      } = draft
      void _omit
      void _omitExtraParams
      void _omitExtraParamsValue
      void _omitOfficialToolDraft
      void _omitOfficialTools
      const payload: Partial<ApiModel> = {
        ...rest,
        param_controls: parsedPC,
        // PATCH merges into the existing model. Non-chat kinds must explicitly
        // clear an earlier chat-model value instead of merely omitting the key.
        extra_params: parsedExtraParams?.valid ? parsedExtraParams.value : {},
      }
      if (draft.kind === 'chat') payload.builtin_tools = builtinToolsConfig ?? null
      if (officialToolsDirty) payload.official_tools = officialTools
      const updated = await adminApi.updateModel(id, payload)
      if (draft.kind === 'chat') {
        await adminApi.setModelSkills(id, skillIds ?? [])
      }
      // PATCH may not echo back skills — preserve the just-saved selection so the
      // chips don't flicker empty after save.
      setDraft({ ...modelToDraft(updated), skills: skillIds ?? [] })
      toast.success(t('admin:models.updated'))
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      setSaving(false)
    }
  }

  // §fast-mode: mark/clear THE fast model. A dedicated endpoint (not the generic
  // save) because it clears the flag on all other models in one transaction,
  // forces Deep Research off, and refuses to leave the advanced picker empty —
  // the backend's validation error surfaces via the toast.
  async function handleFastToggle(v: boolean) {
    try {
      await adminApi.setFastModel(id, v)
      patch(v ? { fast: true, research_enabled: false } : { fast: false })
      toast.success(
        v
          ? t('admin:models.fastMarked', { defaultValue: 'Now the fast model' })
          : t('admin:models.fastCleared', { defaultValue: 'No longer the fast model' }),
      )
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  const hasOfficialImageGeneration =
    draft?.official_tools_draft.some((tool) => tool.name === 'image_generation') ?? false

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate('/admin/models')}
        className="inline-flex items-center gap-1.5 text-[12.5px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] interactive rounded-[6px] -ml-2 px-2 py-1.5 mb-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <ArrowLeft size={12} aria-hidden />
        {t('admin:models.backToList')}
      </button>

      {loading ? (
        <PanelFallback />
      ) : notFound || !draft ? (
        <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
          {t('admin:models.notFound')}
        </div>
      ) : (
        <>
          <header>
            <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">
              {draft.label || t('admin:models.editorTitle')}
            </h1>
            <p className="mt-2 text-[var(--color-fg-muted)] text-sm font-mono">{draft.request_id}</p>
          </header>

          {/* Section: Basic --------------------------------------------------- */}
          <section className="mt-8 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:models.sections.basic')}</h2>
            <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label={t('admin:models.fields.channel')} htmlFor="m-ch">
                <Select
                  value={draft.channel_id ?? ''}
                  onValueChange={(v) =>
                    // Clear the fallback if the new primary IS the current fallback —
                    // otherwise fallback_channel_id == channel_id (a no-op the backend
                    // ignores) and the fallback Select would render blank.
                    patch(v === draft.fallback_channel_id ? { channel_id: v, fallback_channel_id: '' } : { channel_id: v })
                  }
                >
                  <SelectTrigger id="m-ch">
                    <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
                  </SelectTrigger>
                  <SelectContent>
                    {channels.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name} ({c.type})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('admin:models.fields.fallbackChannel', { defaultValue: 'Fallback channel' })}
                htmlFor="m-fb-ch"
                hint={t('admin:models.fields.fallbackChannelHint', {
                  defaultValue:
                    'Retried automatically when a request on the primary channel fails, before the user sees an error. Must match the primary type & format — only the URL and key differ. Optional.',
                })}
              >
                <Select
                  value={draft.fallback_channel_id && draft.fallback_channel_id !== draft.channel_id ? draft.fallback_channel_id : 'none'}
                  onValueChange={(v) => patch({ fallback_channel_id: v === 'none' ? '' : v })}
                >
                  <SelectTrigger id="m-fb-ch">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">
                      {t('admin:models.fields.fallbackNone', { defaultValue: 'None' })}
                    </SelectItem>
                    {fallbackOptions.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name} ({c.type})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('admin:models.fields.kind')} htmlFor="m-kind">
                <Select
                  value={draft.kind ?? 'chat'}
                  onValueChange={(v) => {
                    const kind = v as ApiModel['kind']
                    // extra_params are supported only for chat models. Clear a
                    // previous chat value before an image/embedding save so the
                    // backend never receives stale unsupported configuration.
                    patch(kind === 'chat' ? { kind } : { kind, extra_params_text: '{}' })
                  }}
                >
                  <SelectTrigger id="m-kind">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {KINDS.map((k) => (
                      <SelectItem key={k} value={k}>
                        {k}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('admin:models.fields.label')} htmlFor="m-label">
                <Input
                  id="m-label"
                  value={draft.label ?? ''}
                  onChange={(e) => patch({ label: e.target.value })}
                  placeholder="Claude Opus 4.8"
                />
              </Field>
              <Field label={t('admin:models.fields.requestId')} htmlFor="m-req">
                <Input
                  id="m-req"
                  value={draft.request_id ?? ''}
                  onChange={(e) => patch({ request_id: e.target.value })}
                  placeholder="claude-opus-4-8"
                />
              </Field>
              <Field label={t('admin:models.fields.icon')} htmlFor="m-icon" className="sm:col-span-2">
                <IconUploader
                  id="m-icon"
                  value={draft.icon ?? ''}
                  onChange={(v) => patch({ icon: v })}
                  placeholder="🌟 or https://example.com/icon.png"
                />
              </Field>
              <Field label={t('admin:models.fields.description')} htmlFor="m-desc" className="sm:col-span-2">
                <Input
                  id="m-desc"
                  value={draft.description ?? ''}
                  onChange={(e) => patch({ description: e.target.value })}
                />
              </Field>
              <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5 sm:col-span-2">
                <span className="text-sm">{t('admin:models.fields.enabled')}</span>
                <Switch
                  checked={draft.enabled ?? true}
                  onCheckedChange={(v) => patch({ enabled: v })}
                />
              </label>
              <Field label={t('admin:models.fields.sortOrder')} htmlFor="m-sort" hint={t('admin:models.fields.sortOrderHint')}>
                <Input
                  id="m-sort"
                  type="number"
                  value={String(draft.sort_order ?? 0)}
                  onChange={(e) => patch({ sort_order: Number(e.target.value) })}
                />
              </Field>
            </div>
          </section>

          {/* Section: Tags (§ model tags) ------------------------------------- */}
          <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <div className="flex items-center justify-between gap-3">
              <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:models.sections.tags')}</h2>
              <button
                type="button"
                onClick={() => navigate('/admin/model-tags')}
                className="text-xs text-[var(--color-accent)] hover:underline interactive"
              >
                {t('admin:modelTags.manage')}
              </button>
            </div>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:models.tagsHint')}</p>
            {allTags.length === 0 ? (
              <p className="mt-3 text-sm text-[var(--color-fg-muted)]">{t('admin:modelTags.emptyHint')}</p>
            ) : (
              <div className="mt-3 flex flex-wrap gap-2">
                {allTags.map((tag) => {
                  const on = (draft.tags ?? []).includes(tag.id)
                  return (
                    <button
                      key={tag.id}
                      type="button"
                      onClick={() => toggleTag(tag.id)}
                      aria-pressed={on}
                      className={cn(
                        'rounded-full px-3 py-1 text-sm interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                        on
                          ? 'bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
                          : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                      )}
                    >
                      {tag.name}
                    </button>
                  )
                })}
              </div>
            )}
          </section>

          {/* Section: Skills (chat only, §4.17) ------------------------------- */}
          {draft.kind === 'chat' && (
            <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
              <div className="flex items-center justify-between gap-3">
                <h2 className="font-serif text-lg text-[var(--color-fg)]">
                  {t('admin:models.sections.skills', { defaultValue: 'Skills' })}
                </h2>
                <button
                  type="button"
                  onClick={() => navigate('/admin/skills')}
                  className="text-xs text-[var(--color-accent)] hover:underline interactive"
                >
                  {t('admin:skills.manage', { defaultValue: 'Manage skills' })}
                </button>
              </div>
              <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
                {t('admin:models.skillsHint', {
                  defaultValue:
                    'Checked skills are listed in this model’s system prompt; the model loads each one’s full instructions on demand via use_skill.',
                })}
              </p>
              {allSkills.length === 0 ? (
                <p className="mt-3 text-sm text-[var(--color-fg-muted)]">{t('admin:skills.empty')}</p>
              ) : (
                <div className="mt-3 flex flex-wrap gap-2">
                  {allSkills.map((sk) => {
                    const on = (draft.skills ?? []).includes(sk.id)
                    return (
                      <button
                        key={sk.id}
                        type="button"
                        onClick={() => toggleSkill(sk.id)}
                        aria-pressed={on}
                        title={sk.description}
                        className={cn(
                          'rounded-full px-3 py-1 text-sm interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                          on
                            ? 'bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
                            : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                          !sk.enabled && 'opacity-60',
                        )}
                      >
                        {sk.name}
                        {!sk.enabled ? ` · ${t('admin:skills.disabledTag', { defaultValue: 'disabled' })}` : ''}
                      </button>
                    )
                  })}
                </div>
              )}
            </section>
          )}

          {/* Section: Chat behaviour (chat only) ------------------------------ */}
          {draft.kind === 'chat' && (
            <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
              <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:models.sections.behaviour')}</h2>
              <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
                <Field label={t('admin:models.fields.toolMode')} htmlFor="m-tool">
                  <Select
                    value={draft.tool_mode ?? 'native'}
                    onValueChange={(v) => patch({ tool_mode: v as ApiModel['tool_mode'] })}
                  >
                    <SelectTrigger id="m-tool">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {TOOL_MODES.map((tm) => (
                        <SelectItem key={tm} value={tm}>
                          {tm}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field
                  label={t('admin:models.fields.builtinToolsLabel', { defaultValue: 'Built-in tools' })}
                  hint={t('admin:models.fields.builtinToolsHint', {
                    defaultValue:
                      'Limit the platform tools this model may use through tool calling. Default all also includes tools registered in the future.',
                  })}
                  className="min-w-0 sm:col-span-2"
                >
                  <div className="overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)]">
                    <div className="flex flex-col gap-2 border-b border-[var(--color-divider)] px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between">
                      <div className="inline-flex w-fit items-center gap-1 rounded-[9px] border border-[var(--color-border-subtle)] bg-[var(--color-surface)] p-0.5">
                        <button
                          type="button"
                          aria-pressed={builtinToolsUseDefault}
                          onClick={() => patch({ builtin_tools: null })}
                          className={cn(
                            'h-7 rounded-[7px] px-2.5 text-[12px] font-medium interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                            builtinToolsUseDefault
                              ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]'
                              : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                          )}
                        >
                          {t('admin:models.fields.builtinToolsDefaultAll', { defaultValue: 'Default all' })}
                        </button>
                        <button
                          type="button"
                          aria-pressed={!builtinToolsUseDefault}
                          disabled={builtinToolsLoading || builtinToolsError}
                          onClick={() => patch({ builtin_tools: [...builtinToolNames] })}
                          className={cn(
                            'h-7 rounded-[7px] px-2.5 text-[12px] font-medium interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                            builtinToolsLoading || builtinToolsError
                              ? 'cursor-not-allowed opacity-40'
                              : !builtinToolsUseDefault
                                ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]'
                                : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                          )}
                        >
                          {t('admin:models.fields.builtinToolsCustom', { defaultValue: 'Custom' })}
                        </button>
                      </div>
                      <div className="flex min-w-0 items-center gap-1.5">
                        <span className="mr-auto truncate text-[11.5px] tabular-nums text-[var(--color-fg-subtle)] sm:mr-1">
                          {t('admin:models.fields.builtinToolsSelected', {
                            selected: selectedBuiltinToolNames.length,
                            total: builtinTools.length,
                            defaultValue: '{{selected}}/{{total}} enabled',
                          })}
                        </span>
                        {!builtinToolsUseDefault ? (
                          <>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              disabled={builtinToolsLoading || builtinToolsError}
                              onClick={() => patch({ builtin_tools: [...builtinToolNames] })}
                            >
                              {t('admin:models.fields.builtinToolsSelectAll', { defaultValue: 'Select all' })}
                            </Button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              disabled={builtinToolsLoading || builtinToolsError}
                              onClick={() => patch({ builtin_tools: [] })}
                            >
                              {t('admin:models.fields.builtinToolsClear', { defaultValue: 'Clear' })}
                            </Button>
                          </>
                        ) : null}
                      </div>
                    </div>
                    {builtinToolsError ? (
                      <div
                        className="flex flex-col items-start gap-2 px-3 py-4 sm:flex-row sm:items-center sm:justify-between"
                        role="alert"
                      >
                        <p className="text-sm text-[var(--color-danger)]">
                          {t('admin:models.fields.builtinToolsLoadFailed', {
                            defaultValue: 'Could not load the built-in tool registry. The saved policy has not been changed.',
                          })}
                        </p>
                        <Button
                          type="button"
                          variant="secondary"
                          size="sm"
                          disabled={builtinToolsLoading}
                          onClick={() => void retryBuiltinTools()}
                        >
                          <RefreshCw size={14} className={builtinToolsLoading ? 'animate-spin' : undefined} aria-hidden />
                          {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
                        </Button>
                      </div>
                    ) : builtinToolsLoading ? (
                      <p className="px-3 py-4 text-sm text-[var(--color-fg-muted)]" role="status">
                        {t('common:common.loading', { defaultValue: 'Loading...' })}
                      </p>
                    ) : builtinTools.length === 0 ? (
                      <p className="px-3 py-4 text-sm text-[var(--color-fg-muted)]">
                        {t('admin:models.fields.builtinToolsEmpty', { defaultValue: 'No built-in tools are registered.' })}
                      </p>
                    ) : (
                      <div className="grid grid-cols-1 gap-0.5 p-1 md:grid-cols-2">
                        {builtinTools.map((tool) => {
                          const checked = selectedBuiltinToolSet.has(tool.name)
                          const Icon = BUILTIN_TOOL_ICONS[tool.name] ?? Wrench
                          return (
                            <button
                              key={tool.name}
                              type="button"
                              role="checkbox"
                              aria-checked={checked}
                              disabled={builtinToolsUseDefault || builtinToolsLoading || builtinToolsError}
                              onClick={() => toggleBuiltinTool(tool.name)}
                              className={cn(
                                'flex min-w-0 items-start gap-2.5 rounded-[8px] px-2.5 py-2 text-left',
                                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                                builtinToolsUseDefault || builtinToolsLoading || builtinToolsError
                                  ? 'cursor-default'
                                  : 'interactive',
                                checked
                                  ? builtinToolsUseDefault
                                    ? 'bg-[var(--color-surface)]/70'
                                    : 'bg-[var(--color-secondary-soft)]'
                                  : 'hover:bg-[var(--color-surface)]',
                              )}
                            >
                              <Icon
                                size={15}
                                aria-hidden
                                className={cn(
                                  'mt-0.5 shrink-0',
                                  checked && !builtinToolsUseDefault
                                    ? 'text-[var(--color-secondary)]'
                                    : 'text-[var(--color-fg-muted)]',
                                )}
                              />
                              <span className="min-w-0 flex-1">
                                <span
                                  className={cn(
                                    'block truncate text-[12.5px] font-medium',
                                    checked ? 'text-[var(--color-fg)]' : 'text-[var(--color-fg-muted)]',
                                  )}
                                >
                                  {t(`admin:models.builtinTools.names.${tool.name}`, {
                                    defaultValue: humanizeToolName(tool.name),
                                  })}
                                </span>
                                <span className="mt-0.5 block line-clamp-2 text-[11px] leading-snug text-[var(--color-fg-subtle)]">
                                  {t(`admin:models.builtinTools.descriptions.${tool.name}`, {
                                    defaultValue: tool.description,
                                  })}
                                </span>
                              </span>
                              {checked ? (
                                <Check
                                  size={14}
                                  aria-hidden
                                  className={cn(
                                    'mt-0.5 shrink-0',
                                    builtinToolsUseDefault
                                      ? 'text-[var(--color-fg-subtle)]'
                                      : 'text-[var(--color-secondary)]',
                                  )}
                                />
                              ) : null}
                            </button>
                          )
                        })}
                      </div>
                    )}
                  </div>
                </Field>
                <div className="grid grid-cols-1 items-end gap-3 sm:col-span-2 sm:grid-cols-3">
                  <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                    <span className="text-sm">{t('admin:models.fields.vision')}</span>
                    <Switch
                      checked={draft.vision ?? true}
                      onCheckedChange={(v) => patch({ vision: v })}
                    />
                  </label>
                  <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                    <span className="text-sm">{t('admin:models.fields.stream')}</span>
                    <Switch
                      checked={draft.stream ?? true}
                      onCheckedChange={(v) => patch({ stream: v })}
                    />
                  </label>
                  <label className="flex items-center justify-between gap-3 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                    <span className="text-sm">{t('admin:models.fields.researchEnabled')}</span>
                    <Switch
                      // §fast-mode: the fast model can never run Deep Research.
                      checked={draft.fast ? false : draft.research_enabled ?? true}
                      disabled={draft.fast}
                      onCheckedChange={(v) => patch({ research_enabled: v })}
                    />
                  </label>
                  {draft.kind === 'chat' && (
                    <label className="flex items-center justify-between gap-3 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5 sm:col-span-2">
                      <div className="min-w-0">
                        <span className="text-sm">{t('admin:models.fields.fastModel', { defaultValue: 'Fast model' })}</span>
                        <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
                          {t('admin:models.fields.fastModelHint', {
                            defaultValue: 'Serve “快速” mode. Hidden from the advanced picker; its name is never shown to users; Deep Research is forced off.',
                          })}
                        </p>
                      </div>
                      <Switch checked={draft.fast ?? false} onCheckedChange={handleFastToggle} />
                    </label>
                  )}
                </div>
                <Field label={t('admin:models.fields.systemPrompt')} htmlFor="m-sys" className="sm:col-span-2">
                  <Textarea
                    id="m-sys"
                    rows={4}
                    value={draft.system_prompt ?? ''}
                    onChange={(e) => patch({ system_prompt: e.target.value })}
                  />
                </Field>
                <Field
                  label={t('admin:models.fields.paramControls')}
                  hint={t('admin:models.fields.paramControlsHint')}
                  className="sm:col-span-2"
                >
                  <ParamControlsEditor
                    value={draft.param_controls_text}
                    onChange={(v) => patch({ param_controls_text: v })}
                  />
                </Field>
                <Field
                  label={t('admin:models.fields.extraParams')}
                  htmlFor="m-extra-params"
                  hint={t('admin:models.fields.extraParamsHint')}
                  error={extraParamsError}
                  className="sm:col-span-2"
                >
                  <Textarea
                    id="m-extra-params"
                    rows={5}
                    value={draft.extra_params_text}
                    onChange={(e) => patch({ extra_params_text: e.target.value })}
                    placeholder={'{\n  "reasoning_effort": "medium"\n}'}
                    invalid={Boolean(extraParamsError)}
                    className="min-h-[7.5rem] font-mono text-[12px] leading-relaxed"
                  />
                </Field>

                <Field
                  label={t('admin:models.fields.officialToolsLabel')}
                  hint={t('admin:models.fields.officialToolsHint')}
                  className="min-w-0 sm:col-span-2"
                >
                  <div className="divide-y divide-[var(--color-divider)] rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)]">
                    {draft.official_tools_draft.length === 0 ? (
                      <p className="px-3 py-4 text-sm text-[var(--color-fg-muted)]">
                        {t('admin:models.fields.officialToolsEmpty', { defaultValue: 'No provider-native tools configured.' })}
                      </p>
                    ) : (
                      draft.official_tools_draft.map((tool, index) => {
                        const parsed = parseExtraParams(tool.request_text)
                        const setTool = (next: Partial<OfficialToolDraft>) => {
                          const tools = draft.official_tools_draft.map((item, itemIndex) =>
                            itemIndex === index ? { ...item, ...next } : item,
                          )
                          patch({ official_tools_draft: tools, official_tools_dirty: true })
                        }
                        return (
                          <div key={`official-tool-${index}`} className="min-w-0 px-3 py-3">
                            <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-start">
                              <div className="min-w-0 flex-1 space-y-3">
                                <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-[minmax(10rem,0.75fr)_minmax(16rem,1.25fr)]">
                                  <Field
                                    label={t('admin:models.fields.officialToolName', { defaultValue: 'Tool name' })}
                                    htmlFor={`m-official-name-${index}`}
                                    className="min-w-0"
                                  >
                                    <Input
                                      id={`m-official-name-${index}`}
                                      value={tool.name}
                                      onChange={(event) => setTool({ name: event.target.value })}
                                      placeholder="web_search"
                                      className="min-w-0 font-mono"
                                      wrapperClassName="min-w-0 w-full"
                                    />
                                  </Field>
                                  <Field
                                    label={t('admin:models.fields.officialToolIcon', { defaultValue: 'Icon' })}
                                    htmlFor={`m-official-icon-${index}`}
                                    className="min-w-0"
                                  >
                                    <IconPicker
                                      id={`m-official-icon-${index}`}
                                      value={tool.icon}
                                      onChange={(icon) => setTool({ icon })}
                                    />
                                  </Field>
                                </div>
                                <Field
                                  label={t('admin:models.fields.officialToolJSON', { defaultValue: 'Request JSON' })}
                                  htmlFor={`m-official-request-${index}`}
                                  error={
                                    parsed.valid
                                      ? undefined
                                      : t('admin:models.errors.officialToolJSONMustBeObject', {
                                          defaultValue: 'Enter a valid JSON object.',
                                        })
                                  }
                                >
                                  <Textarea
                                    id={`m-official-request-${index}`}
                                    rows={6}
                                    value={tool.request_text}
                                    onChange={(event) => setTool({ request_text: event.target.value })}
                                    invalid={!parsed.valid}
                                    spellCheck={false}
                                    className="min-h-[8.5rem] font-mono text-[12px] leading-relaxed"
                                    placeholder={'{\n  "tools": [\n    { "type": "web_search" }\n  ]\n}'}
                                  />
                                </Field>
                              </div>
                              <Button
                                type="button"
                                variant="ghost"
                                size="icon-sm"
                                className="self-end sm:self-auto"
                                aria-label={t('admin:models.fields.removeOfficialTool', { defaultValue: 'Remove tool' })}
                                onClick={() =>
                                  patch({
                                    official_tools_draft: draft.official_tools_draft.filter((_, itemIndex) => itemIndex !== index),
                                    official_tools_dirty: true,
                                  })
                                }
                              >
                                <Trash2 size={14} aria-hidden />
                              </Button>
                            </div>
                          </div>
                        )
                      })
                    )}
                    <div className="px-3 py-2.5">
                      <Button
                        type="button"
                        variant="secondary"
                        size="sm"
                        leadingIcon={<Plus size={14} aria-hidden />}
                        onClick={() =>
                          patch({
                            official_tools_draft: [
                              ...draft.official_tools_draft,
                              { name: '', icon: '', request_text: '{\n  "tools": []\n}' },
                            ],
                            official_tools_dirty: true,
                          })
                        }
                      >
                        {t('admin:models.fields.addOfficialTool', { defaultValue: 'Add provider tool' })}
                      </Button>
                    </div>
                  </div>
                </Field>

                {/* § moderation: screen each user prompt before generation. */}
                <Field label={t('admin:models.fields.moderationLabel')} className="sm:col-span-2">
                  <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                    <label className="flex items-center justify-between">
                      <span className="text-sm">{t('admin:models.fields.moderationEnable')}</span>
                      <Switch
                        checked={draft.moderation_enabled ?? false}
                        onCheckedChange={(v) => patch({ moderation_enabled: v })}
                      />
                    </label>
                    {draft.moderation_enabled && (
                      <div className="mt-3 max-w-[16rem]">
                        <Select
                          value={draft.moderation_mode ?? 'keyword'}
                          onValueChange={(v) => patch({ moderation_mode: v as ApiModel['moderation_mode'] })}
                        >
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="keyword">{t('admin:models.fields.moderationModeKeyword')}</SelectItem>
                            <SelectItem value="model">{t('admin:models.fields.moderationModeModel')}</SelectItem>
                          </SelectContent>
                        </Select>
                      </div>
                    )}
                    <p className="mt-2 text-[11px] text-[var(--color-fg-subtle)]">
                      {t('admin:models.fields.moderationHint')}
                    </p>
                  </div>
                </Field>
              </div>
            </section>
          )}

          {/* Image providers also consume the same declarative request-control
              mappings; embedding models have no per-request UI controls. */}
          {(draft.kind === 'image' || hasOfficialImageGeneration) && (
            <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-5 sm:px-6">
              <h2 className="font-serif text-lg text-[var(--color-fg)]">
                {t('admin:models.sections.imageBehaviour', { defaultValue: 'Image generation' })}
              </h2>
              <div className="mt-4">
                <Field
                  label={t('admin:models.fields.paramControls')}
                  hint={t('admin:models.fields.paramControlsHint')}
                >
                  <ParamControlsEditor
                    value={draft.param_controls_text}
                    onChange={(value) => patch({ param_controls_text: value })}
                  />
                </Field>
              </div>
            </section>
          )}

          {/* Section: Permissions / quotas (chat + image models). §4.20: image
              models need per-group free allotment too — without a quota row the
              backend treats the model as free+unlimited and never charges credits. */}
          {draft.kind === 'chat' || draft.kind === 'image' ? (
            <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
              <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:models.sections.permissions')}</h2>
              <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('admin:models.permissionsLead')}</p>
              <div className="mt-4">
                <ModelQuotaEditor modelId={id} />
              </div>
            </section>
          ) : null}

          {/* Section: Pricing ------------------------------------------------- */}
          <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:models.sections.pricing')}</h2>
            <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
              {draft.kind !== 'image' && (
                <>
                  <Field label={t('admin:models.fields.priceIn')} htmlFor="m-pi">
                    <Input
                      id="m-pi"
                      type="number"
                      step="0.0001"
                      value={String(draft.price_input ?? 0)}
                      onChange={(e) => patch({ price_input: Number(e.target.value) })}
                    />
                  </Field>
                  <Field label={t('admin:models.fields.priceOut')} htmlFor="m-po">
                    <Input
                      id="m-po"
                      type="number"
                      step="0.0001"
                      value={String(draft.price_output ?? 0)}
                      onChange={(e) => patch({ price_output: Number(e.target.value) })}
                    />
                  </Field>
                  <Field label={t('admin:models.fields.priceCacheRead')} htmlFor="m-pcr">
                    <Input
                      id="m-pcr"
                      type="number"
                      step="0.0001"
                      value={String(draft.price_cache_read ?? 0)}
                      onChange={(e) => patch({ price_cache_read: Number(e.target.value) })}
                    />
                  </Field>
                  <Field label={t('admin:models.fields.priceCacheWrite')} htmlFor="m-pcw">
                    <Input
                      id="m-pcw"
                      type="number"
                      step="0.0001"
                      value={String(draft.price_cache_write ?? 0)}
                      onChange={(e) => patch({ price_cache_write: Number(e.target.value) })}
                    />
                  </Field>
                </>
              )}
              {(draft.kind === 'image' || hasOfficialImageGeneration) && (
                <Field label={t('admin:models.fields.priceImage')} htmlFor="m-img" className="sm:col-span-2">
                  <Input
                    id="m-img"
                    type="number"
                    step="0.001"
                    value={String(draft.price_per_image ?? 0)}
                    onChange={(e) => patch({ price_per_image: Number(e.target.value) })}
                  />
                </Field>
              )}
              {draft.kind === 'image' && (
                <Field
                  label={t('admin:models.fields.imageTimeout', { defaultValue: 'Generation timeout (seconds)' })}
                  htmlFor="m-imgto"
                  hint={t('admin:models.fields.imageTimeoutHint', {
                    defaultValue: 'Cut a single image request after this many seconds. 0 = no per-model cap.',
                  })}
                  className="sm:col-span-2"
                >
                  <Input
                    id="m-imgto"
                    type="number"
                    min="0"
                    value={String(draft.image_timeout_sec ?? 0)}
                    onChange={(e) => {
                      // Blank → NaN → 0 (no cap); never send a negative.
                      const n = Number(e.target.value)
                      patch({ image_timeout_sec: Number.isFinite(n) && n > 0 ? Math.floor(n) : 0 })
                    }}
                  />
                </Field>
              )}
              {draft.kind === 'embedding' && (
                <Field label={t('admin:models.fields.dim')} htmlFor="m-dim" className="sm:col-span-2">
                  <Input
                    id="m-dim"
                    type="number"
                    value={String(draft.dim ?? 0)}
                    onChange={(e) => patch({ dim: Number(e.target.value) })}
                  />
                </Field>
              )}
            </div>
          </section>

          {/* Sticky save bar */}
          <div className="mt-6 flex items-center justify-end gap-2">
            <Button variant="ghost" onClick={() => navigate('/admin/models')} disabled={saving}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void save()} loading={saving}>
              {t('common:actions.save')}
            </Button>
          </div>
        </>
      )}
    </div>
  )
}
