/**
 * AdminImageStyles — manage §4.20 image styles. Each style has a name, an example
 * thumbnail, and a HIDDEN prompt that's composed into the final image prompt
 * server-side (users never see it). The composer's style picker shows the
 * enabled styles' name + thumbnail only.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Palette } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiImageStyle } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { IconUploader } from '@/components/admin/icon-uploader'
import { toast } from '@/hooks/use-toast'

export default function AdminImageStyles() {
  const { t } = useTranslation(['admin', 'common'])
  const [styles, setStyles] = useState<ApiImageStyle[]>([])
  const [loading, setLoading] = useState(true)
  const [newName, setNewName] = useState('')
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      setStyles(await adminApi.imageStyles())
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

  async function create() {
    if (creatingRef.current) return
    const name = newName.trim()
    if (!name) return
    creatingRef.current = true
    setCreating(true)
    try {
      const st = await adminApi.createImageStyle({ name, enabled: true, sort_order: styles.length })
      setStyles((s) => [...s, st])
      setNewName('')
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      creatingRef.current = false
      setCreating(false)
    }
  }

  async function remove(id: string) {
    try {
      await adminApi.removeImageStyle(id)
      setStyles((s) => s.filter((x) => x.id !== id))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  function patchLocal(id: string, patch: Partial<ApiImageStyle>) {
    setStyles((s) => s.map((x) => (x.id === id ? { ...x, ...patch } : x)))
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">
          {t('admin:imageStyles.title', { defaultValue: 'Image styles' })}
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:imageStyles.lead', {
            defaultValue: 'Looks users can pick when drawing. The style prompt is hidden from users.',
          })}
        </p>
      </header>

      <section className="mt-8">
        <div className="flex gap-2">
          <Input
            value={newName}
            disabled={creating}
            onChange={(e) => setNewName(e.target.value)}
            placeholder={t('admin:imageStyles.namePlaceholder', { defaultValue: 'Style name (e.g. Watercolor)' })}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                void create()
              }
            }}
          />
          <Button onClick={() => void create()} loading={creating} leadingIcon={<Plus size={14} aria-hidden />}>
            {t('admin:imageStyles.add', { defaultValue: 'Add style' })}
          </Button>
        </div>

        {loading ? (
          <div className="mt-6 text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : styles.length === 0 ? (
          <div className="mt-6 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:imageStyles.empty', { defaultValue: 'No styles yet. Add one above.' })}
          </div>
        ) : (
          <ul className="mt-6 flex flex-col gap-3">
            {styles.map((st) => (
              <StyleCard key={st.id} style={st} onPatch={(p) => patchLocal(st.id, p)} onRemove={() => void remove(st.id)} />
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function StyleCard({
  style,
  onPatch,
  onRemove,
}: {
  style: ApiImageStyle
  onPatch: (patch: Partial<ApiImageStyle>) => void
  onRemove: () => void
}) {
  const { t } = useTranslation(['admin', 'common'])
  const [saving, setSaving] = useState(false)

  async function save() {
    const name = style.name.trim()
    if (!name) {
      toast.error(t('admin:imageStyles.nameRequired', { defaultValue: 'Name is required.' }))
      return
    }
    setSaving(true)
    try {
      const upd = await adminApi.updateImageStyle(style.id, {
        name,
        example_image_url: style.example_image_url,
        hidden_prompt: style.hidden_prompt ?? '',
        enabled: style.enabled,
        sort_order: style.sort_order,
      })
      onPatch(upd)
      toast.success(t('admin:common.saved', { defaultValue: 'Saved' }))
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
    <li className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="flex gap-4">
        {/* Example thumbnail */}
        <div className="size-20 shrink-0 overflow-hidden rounded-[10px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)]">
          {style.example_image_url ? (
            <img src={style.example_image_url} alt="" className="size-full object-cover" />
          ) : (
            <span className="grid size-full place-items-center text-[var(--color-fg-faint)]">
              <Palette size={20} aria-hidden />
            </span>
          )}
        </div>

        <div className="flex min-w-0 flex-1 flex-col gap-3">
          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-[12rem] flex-1">
              <Label htmlFor={`name-${style.id}`}>{t('admin:imageStyles.name', { defaultValue: 'Name' })}</Label>
              <Input
                id={`name-${style.id}`}
                value={style.name}
                onChange={(e) => onPatch({ name: e.target.value })}
                className="mt-1 h-9"
              />
            </div>
            <div className="w-24">
              <Label htmlFor={`order-${style.id}`}>{t('admin:imageStyles.order', { defaultValue: 'Order' })}</Label>
              <Input
                id={`order-${style.id}`}
                type="number"
                value={style.sort_order}
                onChange={(e) => onPatch({ sort_order: Number(e.target.value) || 0 })}
                className="mt-1 h-9"
              />
            </div>
            <div className="flex items-center gap-2 pb-1.5">
              <Switch
                checked={style.enabled}
                onCheckedChange={(v) => onPatch({ enabled: v })}
                aria-label={t('admin:imageStyles.enabled', { defaultValue: 'Enabled' })}
              />
              <span className="text-sm text-[var(--color-fg-muted)]">
                {t('admin:imageStyles.enabled', { defaultValue: 'Enabled' })}
              </span>
            </div>
          </div>

          <div>
            <Label htmlFor={`img-${style.id}`}>
              {t('admin:imageStyles.example', { defaultValue: 'Example image' })}
            </Label>
            <div className="mt-1">
              <IconUploader
                id={`img-${style.id}`}
                value={style.example_image_url}
                onChange={(v) => onPatch({ example_image_url: v })}
                placeholder={t('admin:imageStyles.examplePlaceholder', { defaultValue: 'Image URL or upload' })}
              />
            </div>
          </div>

          <div>
            <Label htmlFor={`prompt-${style.id}`}>
              {t('admin:imageStyles.hiddenPrompt', { defaultValue: 'Style prompt (hidden from users)' })}
            </Label>
            <Textarea
              id={`prompt-${style.id}`}
              value={style.hidden_prompt ?? ''}
              onChange={(e) => onPatch({ hidden_prompt: e.target.value })}
              rows={3}
              placeholder={t('admin:imageStyles.hiddenPromptPlaceholder', {
                defaultValue: 'e.g. soft watercolor, textured paper, muted palette, hand-painted edges',
              })}
              className="mt-1"
            />
          </div>

          <div className="flex items-center justify-end gap-2">
            <button
              type="button"
              onClick={onRemove}
              aria-label={t('common:actions.delete', { defaultValue: 'Delete' })}
              className="inline-flex size-9 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive"
            >
              <Trash2 size={15} aria-hidden />
            </button>
            <Button onClick={() => void save()} loading={saving} size="sm">
              {t('common:actions.save', { defaultValue: 'Save' })}
            </Button>
          </div>
        </div>
      </div>
    </li>
  )
}
