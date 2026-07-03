/**
 * AdminModelEdit — full settings page for one model.
 *
 * Opened from the gear-icon "Settings" button on the AdminModels list. Three
 * sectioned blocks:
 *   1. Basic          — channel, kind, label, request_id, icon, description, enabled
 *   2. Chat behaviour — tool_mode, vision, stream, system_prompt, param_controls (chat only)
 *   3. Pricing        — chat: in/out/cache_read/cache_write · image: per-image · embedding: dim
 *
 * No GET-single endpoint upstream — we re-fetch the model list and find by ID,
 * which is cheap (admin model lists are small) and stays consistent with how
 * the list page reads.
 */
import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiChannel, ApiModel, ApiModelTag, ApiSkill } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { IconUploader } from '@/components/admin/icon-uploader'
import { ParamControlsEditor } from '@/components/admin/param-controls-editor'
import { ModelQuotaEditor } from '@/components/admin/model-quota-editor'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

const KINDS = ['chat', 'image', 'embedding'] as const
const TOOL_MODES = ['native', 'prompt', 'none'] as const
// OpenAI Responses hosted tools the admin can enable (§2.3-B). Names are the
// wire `type` values OpenAI expects.
const OFFICIAL_TOOLS = ['web_search', 'code_interpreter', 'image_generation'] as const

type Draft = Partial<ApiModel> & { param_controls_text: string }

function pcToText(pc: unknown): string {
  if (typeof pc === 'string') return pc
  try {
    return JSON.stringify(pc ?? [], null, 2)
  } catch {
    return '[]'
  }
}

function modelToDraft(m: ApiModel): Draft {
  return {
    ...m,
    param_controls_text: pcToText(m.param_controls),
  }
}

export default function AdminModelEdit() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const { id = '' } = useParams<{ id: string }>()
  const [channels, setChannels] = useState<ApiChannel[]>([])
  const [allTags, setAllTags] = useState<ApiModelTag[]>([])
  const [allSkills, setAllSkills] = useState<ApiSkill[]>([])
  const [draft, setDraft] = useState<Draft | null>(null)
  const [loading, setLoading] = useState(true)
  const [notFound, setNotFound] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      try {
        const [c, m, tg, sk] = await Promise.all([
          adminApi.channels(),
          adminApi.models(),
          adminApi.modelTags().catch(() => [] as ApiModelTag[]),
          adminApi.skills().catch(() => [] as ApiSkill[]),
        ])
        if (cancelled) return
        setChannels(c)
        setAllTags(tg)
        setAllSkills(sk)
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
        if (!cancelled) setLoading(false)
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

  // §2.3-B: the official/system tool switch only applies to an OpenAI channel
  // running the Responses API.
  const channel = channels.find((c) => c.id === draft?.channel_id)
  const isOpenAIResponses = channel?.type === 'openai' && channel?.api_format === 'responses'
  const officialTools = draft?.official_tools ?? []

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
    setSaving(true)
    try {
      // skills bind through their own endpoint (model_skills, §4.17), so keep
      // them out of the model PATCH payload.
      const { param_controls_text: _omit, skills: skillIds, ...rest } = draft
      void _omit
      const payload: Partial<ApiModel> = {
        ...rest,
        param_controls: parsedPC,
      }
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
        <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
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
            <div className="mt-4 grid grid-cols-2 gap-4">
              <Field label={t('admin:models.fields.channel')} htmlFor="m-ch">
                <Select value={draft.channel_id ?? ''} onValueChange={(v) => patch({ channel_id: v })}>
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
              <Field label={t('admin:models.fields.kind')} htmlFor="m-kind">
                <Select
                  value={draft.kind ?? 'chat'}
                  onValueChange={(v) => patch({ kind: v as ApiModel['kind'] })}
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
              <Field label={t('admin:models.fields.icon')} htmlFor="m-icon" className="col-span-2">
                <IconUploader
                  id="m-icon"
                  value={draft.icon ?? ''}
                  onChange={(v) => patch({ icon: v })}
                  placeholder="🌟 or https://example.com/icon.png"
                />
              </Field>
              <Field label={t('admin:models.fields.description')} htmlFor="m-desc" className="col-span-2">
                <Input
                  id="m-desc"
                  value={draft.description ?? ''}
                  onChange={(e) => patch({ description: e.target.value })}
                />
              </Field>
              <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5 col-span-2">
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
              <div className="mt-4 grid grid-cols-2 gap-4">
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
                <div className="grid grid-cols-1 gap-3 items-end sm:grid-cols-3 col-span-2">
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
                      checked={draft.research_enabled ?? true}
                      onCheckedChange={(v) => patch({ research_enabled: v })}
                    />
                  </label>
                </div>
                <Field label={t('admin:models.fields.systemPrompt')} htmlFor="m-sys" className="col-span-2">
                  <Textarea
                    id="m-sys"
                    rows={4}
                    value={draft.system_prompt ?? ''}
                    onChange={(e) => patch({ system_prompt: e.target.value })}
                  />
                </Field>
                <Field label={t('admin:models.fields.paramControls')} className="col-span-2">
                  <ParamControlsEditor
                    value={draft.param_controls_text}
                    onChange={(v) => patch({ param_controls_text: v })}
                  />
                </Field>

                {/* §2.3-B: OpenAI Responses — official (hosted) vs system tools. */}
                {isOpenAIResponses && (
                  <Field label={t('admin:models.fields.officialToolsLabel')} className="col-span-2">
                    <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                      <label className="flex items-center justify-between">
                        <span className="text-sm">{t('admin:models.fields.useOfficialTools')}</span>
                        <Switch
                          checked={officialTools.length > 0}
                          onCheckedChange={(v) => patch({ official_tools: v ? ['web_search'] : [] })}
                        />
                      </label>
                      {officialTools.length > 0 && (
                        <div className="mt-3 flex flex-wrap gap-2">
                          {OFFICIAL_TOOLS.map((name) => {
                            const on = officialTools.includes(name)
                            return (
                              <button
                                key={name}
                                type="button"
                                onClick={() =>
                                  patch({
                                    official_tools: on
                                      ? officialTools.filter((x) => x !== name)
                                      : [...officialTools, name],
                                  })
                                }
                                className={cn(
                                  'rounded-[8px] border px-2.5 py-1 font-mono text-[12px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                                  on
                                    ? 'border-[var(--color-accent)] bg-[var(--color-accent-soft)] text-[var(--color-accent)]'
                                    : 'border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)]',
                                )}
                              >
                                {name}
                              </button>
                            )
                          })}
                        </div>
                      )}
                      <p className="mt-2 text-[11px] text-[var(--color-fg-subtle)]">
                        {t('admin:models.fields.officialToolsHint')}
                      </p>
                    </div>
                  </Field>
                )}

                {/* § moderation: screen each user prompt before generation. */}
                <Field label={t('admin:models.fields.moderationLabel')} className="col-span-2">
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
            <div className="mt-4 grid grid-cols-2 gap-4">
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
              {draft.kind === 'image' && (
                <Field label={t('admin:models.fields.priceImage')} htmlFor="m-img" className="col-span-2">
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
                  className="col-span-2"
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
                <Field label={t('admin:models.fields.dim')} htmlFor="m-dim" className="col-span-2">
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
