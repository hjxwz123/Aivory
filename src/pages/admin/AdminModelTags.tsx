/**
 * AdminModelTags — manage the set of model tags (§ model tags). Admins create,
 * rename, and delete labels here; they're assigned to models on the model-edit
 * page and drive the picker's filter chips.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Tag } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiModelTag } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { toast } from '@/hooks/use-toast'

export default function AdminModelTags() {
  const { t } = useTranslation(['admin', 'common'])
  const [tags, setTags] = useState<ApiModelTag[]>([])
  const [loading, setLoading] = useState(true)
  const [newName, setNewName] = useState('')
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      setTags(await adminApi.modelTags())
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
      const tag = await adminApi.createModelTag(name, tags.length)
      setTags((ts) => [...ts, tag])
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

  async function rename(id: string, name: string) {
    const n = name.trim()
    if (!n) return
    try {
      const upd = await adminApi.updateModelTag(id, { name: n })
      setTags((ts) => ts.map((x) => (x.id === id ? upd : x)))
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    }
  }

  async function remove(id: string) {
    try {
      await adminApi.removeModelTag(id)
      setTags((ts) => ts.filter((x) => x.id !== id))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:modelTags.title')}</h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">{t('admin:modelTags.lead')}</p>
      </header>

      <section className="mt-8">
        <div className="flex gap-2">
          <Input
            value={newName}
            disabled={creating}
            onChange={(e) => setNewName(e.target.value)}
            placeholder={t('admin:modelTags.namePlaceholder')}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                void create()
              }
            }}
          />
          <Button onClick={() => void create()} loading={creating} leadingIcon={<Plus size={14} aria-hidden />}>
            {t('admin:modelTags.add')}
          </Button>
        </div>

        {loading ? (
          <div className="mt-6 text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : tags.length === 0 ? (
          <div className="mt-6 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:modelTags.empty')}
          </div>
        ) : (
          <ul className="mt-6 flex flex-col divide-y divide-[var(--color-divider)] rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {tags.map((tag) => (
              <li key={tag.id} className="flex items-center gap-3 px-4 py-2.5">
                <Tag size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                <Input
                  defaultValue={tag.name}
                  onBlur={(e) => {
                    if (e.target.value.trim() && e.target.value !== tag.name) void rename(tag.id, e.target.value)
                  }}
                  className="h-8 flex-1"
                />
                <button
                  type="button"
                  onClick={() => void remove(tag.id)}
                  aria-label={t('common:actions.delete', { defaultValue: 'Delete' })}
                  className="inline-flex size-8 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive"
                >
                  <Trash2 size={14} aria-hidden />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
