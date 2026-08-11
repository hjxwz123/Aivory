import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AlertCircle,
  Cable,
  Eye,
  EyeOff,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from 'lucide-react'
import { adminApi, ApiError, type ApiMCPServer, type ApiMCPServerInput } from '@/api'
import { IconPicker } from '@/components/admin/icon-picker'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { LucideGlyph } from '@/components/ui/lucide-icon'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'

interface HeaderDraft {
  id: number
  key: string
  value: string
  revealed: boolean
}

interface MCPDraft {
  name: string
  icon: string
  description: string
  url: string
  headers: HeaderDraft[]
  enabled: boolean
}

interface EditorState {
  open: boolean
  row?: ApiMCPServer
  draft: MCPDraft
}

let nextHeaderID = 0

function makeHeader(key = '', value = ''): HeaderDraft {
  nextHeaderID += 1
  return { id: nextHeaderID, key, value, revealed: false }
}

function newDraft(): MCPDraft {
  return {
    name: '',
    icon: 'Blocks',
    description: '',
    url: '',
    headers: [],
    enabled: false,
  }
}

function draftFromServer(server: ApiMCPServer): MCPDraft {
  return {
    name: server.name,
    icon: server.icon || 'Blocks',
    description: server.description,
    url: server.url,
    headers: Object.entries(server.headers ?? {}).map(([key, value]) => makeHeader(key, value)),
    enabled: server.enabled,
  }
}

function formatTimestamp(value?: number): string {
  if (!value) return ''
  const milliseconds = value < 10_000_000_000 ? value * 1000 : value
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(milliseconds))
}

export default function AdminMCP() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiMCPServer[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [busyAction, setBusyAction] = useState('')
  const [togglingID, setTogglingID] = useState('')
  const [confirmDelete, setConfirmDelete] = useState<ApiMCPServer | null>(null)
  const [editor, setEditor] = useState<EditorState>({ open: false, draft: newDraft() })
  const savingRef = useRef(false)
  const deletingRef = useRef(false)

  async function refreshRows() {
    setRows(await adminApi.mcpServers())
  }

  async function load() {
    setLoading(true)
    try {
      await refreshRows()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function openNew() {
    setEditor({ open: true, draft: newDraft() })
  }

  function openEdit(row: ApiMCPServer) {
    setEditor({ open: true, row, draft: draftFromServer(row) })
  }

  function updateDraft(patch: Partial<MCPDraft>) {
    setEditor((current) => ({
      ...current,
      draft: { ...current.draft, ...patch },
    }))
  }

  function updateHeader(id: number, patch: Partial<HeaderDraft>) {
    updateDraft({
      headers: editor.draft.headers.map((header) => (
        header.id === id ? { ...header, ...patch } : header
      )),
    })
  }

  function removeHeader(id: number) {
    updateDraft({ headers: editor.draft.headers.filter((header) => header.id !== id) })
  }

  function payloadFromDraft(): ApiMCPServerInput | null {
    const name = editor.draft.name.trim()
    const icon = editor.draft.icon.trim()
    const description = editor.draft.description.trim()
    const url = editor.draft.url.trim()
    if (!name || !icon || !description || !url) {
      toast.error(t('admin:mcp.errors.missingFields'))
      return null
    }

    try {
      const parsed = new URL(url)
      if (
        (parsed.protocol !== 'http:' && parsed.protocol !== 'https:')
        || !parsed.host
        || parsed.username
        || parsed.password
        || parsed.hash
      ) {
        throw new Error('unsupported URL')
      }
    } catch {
      toast.error(t('admin:mcp.errors.invalidUrl'))
      return null
    }

    const headers: Record<string, string> = {}
    const normalizedNames = new Set<string>()
    for (const row of editor.draft.headers) {
      const key = row.key.trim()
      const value = row.value.trim()
      if (!key && !value) continue
      if (!key || !/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(key)) {
        toast.error(t('admin:mcp.errors.invalidHeader'))
        return null
      }
      const normalized = key.toLowerCase()
      if (normalizedNames.has(normalized)) {
        toast.error(t('admin:mcp.errors.duplicateHeader', { name: key }))
        return null
      }
      normalizedNames.add(normalized)
      headers[key] = value
    }

    return {
      name,
      icon,
      description,
      url,
      headers,
      enabled: editor.draft.enabled,
    }
  }

  async function submit() {
    if (savingRef.current) return
    const payload = payloadFromDraft()
    if (!payload) return

    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updateMCPServer(editor.row.id, payload)
        toast.success(t('admin:mcp.updated'))
      } else {
        await adminApi.createMCPServer(payload)
        toast.success(t('admin:mcp.created'))
      }
      setEditor((current) => ({ ...current, open: false }))
      await refreshRows()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function remove(server: ApiMCPServer) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeMCPServer(server.id)
      toast.success(t('admin:mcp.removed'))
      setConfirmDelete(null)
      await refreshRows()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  async function toggleEnabled(server: ApiMCPServer, enabled: boolean) {
    if (togglingID) return
    setTogglingID(server.id)
    setRows((current) => current.map((row) => row.id === server.id ? { ...row, enabled } : row))
    try {
      const updated = await adminApi.updateMCPServer(server.id, { enabled })
      setRows((current) => current.map((row) => row.id === server.id ? updated : row))
    } catch (error) {
      setRows((current) => current.map((row) => row.id === server.id ? server : row))
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setTogglingID('')
    }
  }

  async function runAction(server: ApiMCPServer, action: 'test' | 'sync') {
    const key = `${action}:${server.id}`
    if (busyAction) return
    setBusyAction(key)
    try {
      if (action === 'test') await adminApi.testMCPServer(server.id)
      else await adminApi.syncMCPServer(server.id)
      toast.success(t(action === 'test' ? 'admin:mcp.testSucceeded' : 'admin:mcp.syncSucceeded'))
      await refreshRows()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
      try {
        await refreshRows()
      } catch {
        // Keep the original action error visible; the next page load can retry the list.
      }
    } finally {
      setBusyAction('')
    }
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header className="flex flex-col items-start gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
            {t('admin:mcp.title')}
          </h1>
          <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">{t('admin:mcp.lead')}</p>
        </div>
        <Button
          leadingIcon={<Plus size={15} aria-hidden />}
          onClick={openNew}
          className="max-sm:min-h-[var(--tap-min)]"
        >
          {t('admin:mcp.new')}
        </Button>
      </header>

      <section className="mt-8" aria-label={t('admin:mcp.listLabel')}>
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center">
            <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:mcp.emptyTitle')}</p>
            <p className="mx-auto mt-1 max-w-lg text-sm text-[var(--color-fg-muted)]">{t('admin:mcp.emptyBody')}</p>
          </div>
        ) : (
          <div className="divide-y divide-[var(--color-divider)] overflow-hidden rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((server) => {
              const toolCount = server.discovered_tools?.length ?? 0
              const syncedAt = formatTimestamp(server.last_synced_at)
              const testKey = `test:${server.id}`
              const syncKey = `sync:${server.id}`
              return (
                <article key={server.id} className="px-4 py-4 sm:px-5">
                  <div className="flex min-w-0 items-start gap-3">
                    <span className="grid size-10 shrink-0 place-items-center rounded-[9px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                      <LucideGlyph name={server.icon || 'Blocks'} size={17} aria-hidden />
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <h2 className="min-w-0 truncate text-sm font-semibold text-[var(--color-fg)]" title={server.name}>
                          {server.name}
                        </h2>
                        {server.last_error ? (
                          <Badge size="xs" variant="danger">{t('admin:mcp.status.error')}</Badge>
                        ) : server.last_synced_at ? (
                          <Badge size="xs" variant="success">{t('admin:mcp.status.connected')}</Badge>
                        ) : (
                          <Badge size="xs" variant="neutral">{t('admin:mcp.status.notTested')}</Badge>
                        )}
                        {toolCount > 0 ? (
                          <Badge size="xs" variant="neutral">
                            {t('admin:mcp.toolCount', { count: toolCount })}
                          </Badge>
                        ) : null}
                      </div>
                      <p className="mt-1 line-clamp-2 text-[12px] leading-4 text-[var(--color-fg-subtle)]">
                        {server.description}
                      </p>
                      <p
                        className="mt-1.5 truncate font-mono text-[11px] text-[var(--color-fg-faint)]"
                        title={server.url}
                        dir="ltr"
                      >
                        {server.url}
                      </p>
                      {server.last_error ? (
                        <p className="mt-2 flex items-start gap-1.5 text-[12px] leading-4 text-[var(--color-danger)]">
                          <AlertCircle size={13} aria-hidden className="mt-0.5 shrink-0" />
                          <span className="break-words">{server.last_error}</span>
                        </p>
                      ) : syncedAt ? (
                        <p className="mt-1.5 text-[11px] text-[var(--color-fg-faint)]">
                          {t('admin:mcp.lastSynced', { time: syncedAt })}
                        </p>
                      ) : null}
                    </div>
                  </div>

                  <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-[var(--color-divider)] pt-3 sm:ml-[3.25rem]">
                    <label htmlFor={`mcp-enabled-${server.id}`} className="flex min-h-8 items-center gap-2 text-[12px] font-medium text-[var(--color-fg-muted)]">
                      <Switch
                        id={`mcp-enabled-${server.id}`}
                        checked={server.enabled}
                        disabled={Boolean(togglingID)}
                        onCheckedChange={(value) => void toggleEnabled(server, value)}
                      />
                      {server.enabled ? t('admin:mcp.status.enabled') : t('admin:mcp.status.disabled')}
                    </label>
                    <div className="flex flex-wrap items-center justify-end gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        leadingIcon={<Cable size={13} aria-hidden />}
                        loading={busyAction === testKey}
                        disabled={Boolean(busyAction)}
                        onClick={() => void runAction(server, 'test')}
                      >
                        {t('admin:mcp.actions.test')}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        leadingIcon={<RefreshCw size={13} aria-hidden />}
                        loading={busyAction === syncKey}
                        disabled={Boolean(busyAction)}
                        onClick={() => void runAction(server, 'sync')}
                      >
                        {t('admin:mcp.actions.sync')}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        leadingIcon={<Pencil size={13} aria-hidden />}
                        aria-label={`${t('admin:common.edit')}: ${server.name}`}
                        onClick={() => openEdit(server)}
                        className="max-sm:size-[var(--tap-min)] max-sm:gap-0 max-sm:px-0"
                      >
                        <span className="max-sm:sr-only">{t('admin:common.edit')}</span>
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        leadingIcon={<Trash2 size={13} aria-hidden />}
                        aria-label={`${t('admin:common.remove')}: ${server.name}`}
                        onClick={() => setConfirmDelete(server)}
                        className="max-sm:size-[var(--tap-min)] max-sm:gap-0 max-sm:px-0"
                      >
                        <span className="max-sm:sr-only">{t('admin:common.remove')}</span>
                      </Button>
                    </div>
                  </div>
                </article>
              )
            })}
          </div>
        )}
      </section>

      <Dialog
        open={editor.open}
        onOpenChange={(open) => {
          if (!savingRef.current) setEditor((current) => ({ ...current, open }))
        }}
      >
        <DialogContent size="lg" closeDisabled={saving}>
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:mcp.editorTitle') : t('admin:mcp.newTitle')}</DialogTitle>
            <DialogDescription>
              {editor.row ? t('admin:mcp.editorDescriptionEdit') : t('admin:mcp.editorDescriptionNew')}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('admin:mcp.fields.name')} htmlFor="mcp-name">
                <Input
                  id="mcp-name"
                  value={editor.draft.name}
                  onChange={(event) => updateDraft({ name: event.target.value })}
                  placeholder={t('admin:mcp.fields.namePlaceholder')}
                />
              </Field>
              <Field label={t('admin:mcp.fields.icon')} htmlFor="mcp-icon">
                <IconPicker
                  id="mcp-icon"
                  value={editor.draft.icon}
                  onChange={(icon) => updateDraft({ icon })}
                  aria-label={t('admin:mcp.fields.icon')}
                />
              </Field>
              <Field label={t('admin:mcp.fields.description')} htmlFor="mcp-description" className="sm:col-span-2">
                <Textarea
                  id="mcp-description"
                  rows={3}
                  value={editor.draft.description}
                  onChange={(event) => updateDraft({ description: event.target.value })}
                  placeholder={t('admin:mcp.fields.descriptionPlaceholder')}
                />
              </Field>
              <Field
                label={t('admin:mcp.fields.url')}
                htmlFor="mcp-url"
                hint={t('admin:mcp.fields.urlHint')}
                className="sm:col-span-2"
              >
                <Input
                  id="mcp-url"
                  type="url"
                  dir="ltr"
                  autoComplete="url"
                  value={editor.draft.url}
                  onChange={(event) => updateDraft({ url: event.target.value })}
                  placeholder="https://example.com/mcp"
                />
              </Field>

              <fieldset className="min-w-0 sm:col-span-2">
                <legend className="sr-only">{t('admin:mcp.fields.headers')}</legend>
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <p className="text-sm font-medium leading-tight text-[var(--color-fg)]" aria-hidden="true">
                      {t('admin:mcp.fields.headers')}
                    </p>
                    <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:mcp.fields.headersHint')}</p>
                  </div>
                  <Button
                    size="sm"
                    variant="secondary"
                    leadingIcon={<Plus size={13} aria-hidden />}
                    onClick={() => updateDraft({ headers: [...editor.draft.headers, makeHeader()] })}
                  >
                    {t('admin:mcp.actions.addHeader')}
                  </Button>
                </div>

                {editor.draft.headers.length === 0 ? (
                  <p className="mt-3 rounded-[10px] bg-[var(--color-bg-muted)] px-3 py-4 text-center text-[12px] text-[var(--color-fg-subtle)]">
                    {t('admin:mcp.fields.noHeaders')}
                  </p>
                ) : (
                  <div className="mt-3 grid gap-2">
                    {editor.draft.headers.map((header, index) => (
                      <div
                        key={header.id}
                        className="relative grid gap-2 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-3 pr-12 sm:grid-cols-2"
                      >
                        <Input
                          aria-label={t('admin:mcp.fields.headerNameLabel', { index: index + 1 })}
                          value={header.key}
                          onChange={(event) => updateHeader(header.id, { key: event.target.value })}
                          placeholder={t('admin:mcp.fields.headerNamePlaceholder')}
                          autoCapitalize="off"
                          autoCorrect="off"
                          spellCheck={false}
                          className="font-mono text-[13px]"
                        />
                        <Input
                          aria-label={t('admin:mcp.fields.headerValueLabel', { index: index + 1 })}
                          type={header.revealed ? 'text' : 'password'}
                          value={header.value}
                          onChange={(event) => updateHeader(header.id, { value: event.target.value })}
                          placeholder={t('admin:mcp.fields.headerValuePlaceholder')}
                          autoComplete="new-password"
                          className="font-mono text-[13px]"
                          trailingSlot={(
                            <button
                              type="button"
                              aria-label={header.revealed ? t('admin:mcp.actions.hideHeader') : t('admin:mcp.actions.showHeader')}
                              title={header.revealed ? t('admin:mcp.actions.hideHeader') : t('admin:mcp.actions.showHeader')}
                              onClick={() => updateHeader(header.id, { revealed: !header.revealed })}
                              className="inline-flex size-7 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-faint)] interactive hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                            >
                              {header.revealed ? <EyeOff size={14} aria-hidden /> : <Eye size={14} aria-hidden />}
                            </button>
                          )}
                        />
                        <button
                          type="button"
                          aria-label={t('admin:mcp.actions.removeHeader', { index: index + 1 })}
                          title={t('admin:mcp.actions.removeHeader', { index: index + 1 })}
                          onClick={() => removeHeader(header.id)}
                          className="absolute right-2 top-2 inline-flex size-8 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] interactive hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                        >
                          <X size={14} aria-hidden />
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </fieldset>

              <label
                htmlFor="mcp-enabled"
                className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5 sm:col-span-2"
              >
                <span>
                  <span className="block text-sm font-medium text-[var(--color-fg)]">{t('admin:mcp.fields.enabled')}</span>
                  <span className="mt-0.5 block text-[12px] text-[var(--color-fg-subtle)]">{t('admin:mcp.fields.enabledHint')}</span>
                </span>
                <Switch
                  id="mcp-enabled"
                  checked={editor.draft.enabled}
                  onCheckedChange={(enabled) => updateDraft({ enabled })}
                />
              </label>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={saving}
              onClick={() => setEditor((current) => ({ ...current, open: false }))}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submit()}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(confirmDelete)}
        onOpenChange={(open) => {
          if (!open && !deletingRef.current) setConfirmDelete(null)
        }}
      >
        <DialogContent size="sm" closeDisabled={deleting}>
          <DialogHeader>
            <DialogTitle>{t('admin:mcp.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:mcp.removeBody', { name: confirmDelete.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              loading={deleting}
              onClick={() => confirmDelete && void remove(confirmDelete)}
            >
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
