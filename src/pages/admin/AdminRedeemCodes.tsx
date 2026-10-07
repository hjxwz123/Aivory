/**
 * AdminRedeemCodes — generate, list, revoke, and delete redeem codes that grant
 * a user_group for a fixed duration (§ redeem codes).
 *
 * Single page with two zones:
 *   1. List of existing codes (filterable by status / batch), with row actions
 *      Copy / Enable-or-Disable / Delete.
 *   2. "New batch" dialog — pick group + duration + quantity + optional batch
 *      name and code-expiry deadline. Generating in bulk produces N rows at
 *      once; generating singly returns one row + an immediate copy affordance.
 *
 * Codes are single-use by default (max_uses=1); the editor exposes max_uses for
 * shared promo codes. Disabling a code is reversible and preserves the audit
 * trail; deleting it removes the row entirely (already-granted memberships
 * keep working until they naturally expire).
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Check,
  CheckCheck,
  CircleCheck,
  CircleDotDashed,
  CircleX,
  Copy,
  Download,
  Plus,
  RotateCcw,
  Ticket,
  Trash2,
} from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiRedeemCode, ApiUserGroup } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { Field } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Badge } from '@/components/ui/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip } from '@/components/ui/tooltip'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { useCopy } from '@/hooks/use-clipboard'
import { formatRelativeDate } from '@/lib/utils'
import { envNum } from '@/lib/env-config'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { getRedeemCodeStatus, type RedeemCodeStatus } from '@/lib/redeem-code-status'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminTable } from '@/components/admin/AdminTable'
import { AdminListFilter, AdminListToolbar } from '@/components/admin/admin-list-toolbar'

type StatusFilter = 'all' | RedeemCodeStatus

interface BatchDraft {
  kind: 'group' | 'credits'
  group_id: string
  duration_days: number
  credits: number
  max_uses: number
  expires_at: string // datetime-local format; converted to unix on submit
  note: string
  batch_name: string
  quantity: number
}

const EMPTY_DRAFT: BatchDraft = {
  kind: 'group',
  group_id: '',
  duration_days: 30,
  credits: 100,
  max_uses: 1,
  expires_at: '',
  note: '',
  batch_name: '',
  quantity: 10,
}

export default function AdminRedeemCodes() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiRedeemCode[]>([])
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [status, setStatus] = useState<StatusFilter>('all')
  const [batchFilter, setBatchFilter] = useState('')
  const [search, setSearch] = useState('')
  const loadRequestRef = useRef(0)
  const [newOpen, setNewOpen] = useState(false)
  const [draft, setDraft] = useState<BatchDraft>(EMPTY_DRAFT)
  const [submitting, setSubmitting] = useState(false)
  const submittingRef = useRef(false)
  const [confirmDelete, setConfirmDelete] = useState<ApiRedeemCode | null>(null)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const [togglingId, setTogglingId] = useState<string | null>(null)
  const [generated, setGenerated] = useState<ApiRedeemCode[] | null>(null)
  const [page, setPage] = useState(1)
  const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE_2', 20)
  const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE))
  const pageRows = rows.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)
  useEffect(() => {
    setPage(1)
  }, [status, batchFilter, search, rows.length])

  async function load(requestId = ++loadRequestRef.current) {
    setLoading(true)
    try {
      const codes = await adminApi.redeemCodes({
        search: search.trim() || undefined,
        status: status === 'all' ? undefined : status,
        batch: batchFilter.trim() || undefined,
        limit: 500,
      })
      if (requestId === loadRequestRef.current) setRows(codes)
    } catch (e) {
      if (requestId === loadRequestRef.current) toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      if (requestId === loadRequestRef.current) setLoading(false)
    }
  }

  useEffect(() => {
    const requestId = ++loadRequestRef.current
    setLoading(true)
    const timer = window.setTimeout(() => void load(requestId), 250)
    return () => { window.clearTimeout(timer); ++loadRequestRef.current }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, batchFilter, search])

  useEffect(() => {
    let active = true
    void adminApi.userGroups().then((gs) => { if (active) setGroups(gs) }).catch((e) => {
      if (active) toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
    return () => { active = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function openNew() {
    setDraft({ ...EMPTY_DRAFT, group_id: groups.find((g) => !g.is_default)?.id ?? groups[0]?.id ?? '' })
    setNewOpen(true)
    setGenerated(null)
  }

  async function submit() {
    if (submittingRef.current) return
    if (draft.kind === 'group' && !draft.group_id) {
      toast.error(t('admin:redeemCodes.errors.groupRequired'))
      return
    }
    if (draft.kind === 'credits' && draft.credits <= 0) {
      toast.error(t('admin:redeemCodes.errors.creditsRequired'))
      return
    }
    if (draft.quantity < 1 || draft.quantity > 1000) {
      toast.error(t('admin:redeemCodes.errors.quantityRange'))
      return
    }
    if (draft.duration_days < 0) {
      toast.error(t('admin:redeemCodes.errors.durationNegative'))
      return
    }
    submittingRef.current = true
    setSubmitting(true)
    try {
      const expiresUnix = draft.expires_at ? Math.floor(new Date(draft.expires_at).getTime() / 1000) : 0
      const res = await adminApi.createRedeemCode({
        kind: draft.kind,
        ...(draft.kind === 'group'
          ? { group_id: draft.group_id, duration_days: draft.duration_days }
          : { credits: draft.credits }),
        max_uses: draft.max_uses,
        expires_at: expiresUnix,
        note: draft.note,
        batch_name: draft.batch_name,
        quantity: draft.quantity,
      })
      const created = Array.isArray(res) ? res : [res]
      toast.success(t('admin:redeemCodes.createdToast', { count: created.length }))
      setGenerated(created)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      submittingRef.current = false
      setSubmitting(false)
    }
  }

  async function toggleEnabled(row: ApiRedeemCode) {
    if (togglingId) return
    setTogglingId(row.id)
    try {
      await adminApi.updateRedeemCode(row.id, { enabled: !row.enabled })
      toast.success(row.enabled ? t('admin:redeemCodes.disabled') : t('admin:redeemCodes.enabled'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setTogglingId(null)
    }
  }

  async function remove(row: ApiRedeemCode) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeRedeemCode(row.id)
      toast.success(t('admin:redeemCodes.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  const groupByID = useMemo(() => {
    const m = new Map<string, ApiUserGroup>()
    groups.forEach((g) => m.set(g.id, g))
    return m
  }, [groups])

  function exportCsv() {
    if (rows.length === 0) return
    const esc = (v: string | number) => {
      const s = String(v ?? '')
      return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
    }
    const header = ['code', 'kind', 'group', 'credits', 'status', 'duration_days', 'used_count', 'max_uses', 'batch_name', 'note', 'expires_at', 'created_at']
    const now = Math.floor(Date.now() / 1000)
    const iso = (unix: number) => (unix > 0 ? new Date(unix * 1000).toISOString() : '')
    const lines = [header.join(',')]
    for (const r of rows) {
      lines.push(
        [
          esc(r.code),
          esc(r.kind ?? 'group'),
          esc(r.kind === 'credits' ? '' : (groupByID.get(r.group_id)?.name ?? r.group_id)),
          esc(r.kind === 'credits' ? r.credits : ''),
          esc(getRedeemCodeStatus(r, now)),
          esc(r.kind === 'credits' ? '' : r.duration_days),
          esc(r.used_count),
          esc(r.max_uses),
          esc(r.batch_name ?? ''),
          esc(r.note ?? ''),
          esc(iso(r.expires_at)),
          esc(iso(r.created_at)),
        ].join(','),
      )
    }
    const blob = new Blob(['﻿' + lines.join('\n')], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `redeem-codes-${new Date().toISOString().slice(0, 10)}.csv`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
    toast.success(t('admin:redeemCodes.exported', { count: rows.length, defaultValue: 'Exported {{count}} codes' }))
  }

  return (
    <div>
      <AdminPageHeader
        title={t('admin:redeemCodes.title')}
        description={t('admin:redeemCodes.lead')}
      />
      <AdminListToolbar
        search={search}
        onSearchChange={setSearch}
        placeholder={t('admin:listToolbar.search.redeemCodes')}
        activeFilterCount={Number(status !== 'all') + Number(!!batchFilter.trim())}
        onResetFilters={() => { setStatus('all'); setBatchFilter('') }}
        filters={<>
          <AdminListFilter label={t('admin:redeemCodes.table.status')} value={status} onValueChange={(value) => setStatus(value as StatusFilter)} options={(['all', 'unused', 'partial', 'used', 'invalid'] as StatusFilter[]).map((value) => ({ value, label: t(`admin:redeemCodes.filters.${value}`) }))} />
          <Input aria-label={t('admin:redeemCodes.table.batch')} placeholder={t('admin:redeemCodes.table.batch')} value={batchFilter} onChange={(event) => setBatchFilter(event.target.value)} wrapperClassName="h-9 w-36 shrink-0 rounded-[8px] max-sm:h-11" className="min-w-0 text-[13px]" />
        </>}
        actions={(
          <>
            <Button
              size="sm"
              variant="secondary"
              leadingIcon={<Download size={15} aria-hidden />}
              disabled={rows.length === 0}
              onClick={exportCsv}
            >
              {t('admin:redeemCodes.export', { defaultValue: 'Export CSV' })}
            </Button>
            <Button
              size="sm"
              leadingIcon={<Plus size={15} aria-hidden />}
              onClick={openNew}
            >
              {t('admin:redeemCodes.new')}
            </Button>
          </>
        )}
      />

      <section className="mt-4">
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="grid place-items-center rounded-[12px] bg-[var(--color-bg-muted)]/30 px-4 py-10 sm:px-6 sm:py-16">
            <Ticket size={28} className="text-[var(--color-fg-faint)]" aria-hidden />
            <p className="mt-4 text-sm text-[var(--color-fg-muted)]">{t(search.trim() || batchFilter.trim() || status !== 'all' ? 'admin:common.noResults' : 'admin:redeemCodes.empty')}</p>
          </div>
        ) : (
          <>
            <AdminTable
              items={pageRows}
              rowKey={(rc) => rc.id}
              label={t('admin:redeemCodes.title')}
              columns={[
                { id: 'code', header: t('admin:redeemCodes.table.code'), width: 220, render: (rc) => <code className="font-mono text-[12px]">{rc.code}</code> },
                { id: 'batch', header: t('admin:redeemCodes.table.batch'), width: 140, render: (rc) => <span className="block truncate" title={rc.batch_name}>{rc.batch_name || '—'}</span> },
                { id: 'type', header: t('admin:redeemCodes.table.group'), width: 160, render: (rc) => <span>{rc.kind === 'credits' ? t('admin:redeemCodes.creditsAmount', { count: rc.credits }) : groupByID.get(rc.group_id)?.name || '—'}</span> },
                { id: 'duration', header: t('admin:redeemCodes.table.duration'), width: 100, render: (rc) => rc.kind === 'credits' ? '—' : rc.duration_days === 0 ? t('admin:redeemCodes.durationPermanent') : t('admin:redeemCodes.durationDays', { count: rc.duration_days }) },
                { id: 'status', header: t('admin:redeemCodes.table.status'), width: 100, render: (rc) => <CodeStatus row={rc} /> },
                { id: 'uses', header: t('admin:redeemCodes.table.uses'), width: 100, render: (rc) => <span className="tabular-nums">{rc.used_count}/{rc.max_uses}</span> },
                { id: 'expires', header: t('admin:redeemCodes.table.expiresAt'), width: 150, render: (rc) => <span className="text-[12px] text-[var(--color-fg-muted)]">{rc.expires_at > 0 ? formatRelativeDate(rc.expires_at * 1000) : t('admin:redeemCodes.noExpiry')}</span> },
                { id: 'created', header: t('admin:redeemCodes.table.createdAt'), width: 150, render: (rc) => <span className="text-[12px] text-[var(--color-fg-muted)]">{formatRelativeDate(rc.created_at * 1000)}</span> },
                { id: 'note', header: t('admin:redeemCodes.fields.note'), width: 200, render: (rc) => <span className="block truncate text-[var(--color-fg-muted)]" title={rc.note}>{rc.note || '—'}</span> },
                { id: 'actions', header: t('admin:common.actions'), width: 152, align: 'right', render: (rc) => <CodeActions row={rc} toggling={togglingId === rc.id} onToggleEnabled={() => void toggleEnabled(rc)} onDelete={() => setConfirmDelete(rc)} /> },
              ]}
            />
            <Pagination page={page} pageCount={pageCount} onPage={setPage} />
          </>
        )}
      </section>

      {/* New-batch dialog */}
      <Dialog open={newOpen} onOpenChange={(next) => !submittingRef.current && setNewOpen(next)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('admin:redeemCodes.newTitle')}</DialogTitle>
            <DialogDescription>{t('admin:redeemCodes.newLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            {generated ? (
              <GeneratedList
                codes={generated}
                onDone={() => {
                  setNewOpen(false)
                  setGenerated(null)
                }}
              />
            ) : (
              <div className="grid gap-4">
                <Field label={t('admin:redeemCodes.fields.kind')} hint={t('admin:redeemCodes.fields.kindHint')}>
                  <div className="flex items-center gap-2" role="radiogroup" aria-label={t('admin:redeemCodes.fields.kind')}>
                    {(['group', 'credits'] as const).map((k) => (
                      <button
                        key={k}
                        type="button"
                        role="radio"
                        aria-checked={draft.kind === k}
                        onClick={() => setDraft({ ...draft, kind: k })}
                        className={
                          'inline-flex items-center h-8 px-3 rounded-[8px] text-[12px] interactive ' +
                          (draft.kind === k
                            ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-sm)]'
                            : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]')
                        }
                      >
                        {t(`admin:redeemCodes.kinds.${k}`)}
                      </button>
                    ))}
                  </div>
                </Field>
                {draft.kind === 'group' ? (
                  <Field label={t('admin:redeemCodes.fields.group')} htmlFor="rc-group" hint={t('admin:redeemCodes.fields.groupHint')}>
                    <Select value={draft.group_id} onValueChange={(v) => setDraft({ ...draft, group_id: v })}>
                      <SelectTrigger id="rc-group">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {groups.map((g) => (
                          <SelectItem key={g.id} value={g.id}>
                            {g.name}{g.is_default ? ` · ${t('admin:groups.default', { defaultValue: 'Default' })}` : ''}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                ) : (
                  <Field label={t('admin:redeemCodes.fields.credits')} htmlFor="rc-credits" hint={t('admin:redeemCodes.fields.creditsHint')}>
                    <Input
                      id="rc-credits"
                      type="number"
                      min={1}
                      value={String(draft.credits)}
                      onChange={(e) => setDraft({ ...draft, credits: Math.max(0, Number(e.target.value) || 0) })}
                    />
                  </Field>
                )}
                <div className="grid gap-4 sm:grid-cols-2">
                  {draft.kind === 'group' ? (
                    <Field label={t('admin:redeemCodes.fields.durationDays')} htmlFor="rc-dur" hint={t('admin:redeemCodes.fields.durationDaysHint')}>
                      <Input
                        id="rc-dur"
                        type="number"
                        min={0}
                        value={String(draft.duration_days)}
                        onChange={(e) => setDraft({ ...draft, duration_days: Math.max(0, Number(e.target.value) || 0) })}
                      />
                    </Field>
                  ) : null}
                  <Field label={t('admin:redeemCodes.fields.quantity')} htmlFor="rc-qty" hint={t('admin:redeemCodes.fields.quantityHint')}>
                    <Input
                      id="rc-qty"
                      type="number"
                      min={1}
                      max={1000}
                      value={String(draft.quantity)}
                      onChange={(e) => setDraft({ ...draft, quantity: Math.min(1000, Math.max(1, Number(e.target.value) || 1)) })}
                    />
                  </Field>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field label={t('admin:redeemCodes.fields.maxUses')} htmlFor="rc-max" hint={t('admin:redeemCodes.fields.maxUsesHint')}>
                    <Input
                      id="rc-max"
                      type="number"
                      min={1}
                      value={String(draft.max_uses)}
                      onChange={(e) => setDraft({ ...draft, max_uses: Math.max(1, Number(e.target.value) || 1) })}
                    />
                  </Field>
                  <Field label={t('admin:redeemCodes.fields.expiresAt')} htmlFor="rc-exp" hint={t('admin:redeemCodes.fields.expiresAtHint')}>
                    <Input
                      id="rc-exp"
                      type="datetime-local"
                      value={draft.expires_at}
                      onChange={(e) => setDraft({ ...draft, expires_at: e.target.value })}
                    />
                  </Field>
                </div>
                <Field label={t('admin:redeemCodes.fields.batchName')} htmlFor="rc-batch" hint={t('admin:redeemCodes.fields.batchNameHint')}>
                  <Input
                    id="rc-batch"
                    value={draft.batch_name}
                    onChange={(e) => setDraft({ ...draft, batch_name: e.target.value })}
                    placeholder={t('admin:redeemCodes.fields.batchNamePlaceholder')}
                  />
                </Field>
                <Field label={t('admin:redeemCodes.fields.note')} htmlFor="rc-note">
                  <Textarea
                    id="rc-note"
                    rows={2}
                    value={draft.note}
                    onChange={(e) => setDraft({ ...draft, note: e.target.value })}
                    placeholder={t('admin:redeemCodes.fields.notePlaceholder')}
                  />
                </Field>
              </div>
            )}
          </DialogBody>
          {!generated && (
            <DialogFooter>
              <Button variant="ghost" onClick={() => setNewOpen(false)} disabled={submitting}>
                {t('common:actions.cancel')}
              </Button>
              <Button loading={submitting} onClick={() => void submit()}>
                {t('admin:redeemCodes.create')}
              </Button>
            </DialogFooter>
          )}
        </DialogContent>
      </Dialog>

      {/* Confirm delete */}
      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:redeemCodes.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:redeemCodes.removeBody', { code: confirmDelete.code }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)} disabled={deleting}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/* ───────────────────────── row ─────────────────────────── */

function CodeStatus({ row }: { row: ApiRedeemCode }) {
  const { t } = useTranslation('admin')
  const status = getRedeemCodeStatus(row)
  const presentation = {
    unused: { variant: 'success' as const, icon: <CircleCheck size={11} aria-hidden /> },
    partial: { variant: 'warning' as const, icon: <CircleDotDashed size={11} aria-hidden /> },
    used: { variant: 'neutral' as const, icon: <CheckCheck size={11} aria-hidden /> },
    invalid: { variant: 'danger' as const, icon: <CircleX size={11} aria-hidden /> },
  }[status]
  return <Badge size="xs" variant={presentation.variant} leadingIcon={presentation.icon}>{t(`redeemCodes.status.${status}`)}</Badge>
}

function CodeActions({ row, toggling, onToggleEnabled, onDelete }: {
  row: ApiRedeemCode
  toggling: boolean
  onToggleEnabled: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation(['admin', 'common'])
  const { copied, copy } = useCopy()
  return (
    <div className="flex items-center justify-end gap-1">
        <Tooltip content={copied ? t('admin:redeemCodes.copied') : t('admin:redeemCodes.copy')}>
          <Button
            variant="ghost"
            size="icon-sm"
            className="max-sm:size-11"
            aria-label={`${t('admin:redeemCodes.copy')}: ${row.code}`}
            onClick={() => void copy(row.code)}
          >
            {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
          </Button>
        </Tooltip>
        <Tooltip content={row.enabled ? t('admin:redeemCodes.disable') : t('admin:redeemCodes.enable')}>
          <Button
            variant="ghost"
            size="icon-sm"
            className="max-sm:size-11 max-sm:px-0"
            leadingIcon={<RotateCcw size={13} aria-hidden />}
            loading={toggling}
            disabled={toggling}
            onClick={onToggleEnabled}
            aria-label={`${row.enabled ? t('admin:redeemCodes.disable') : t('admin:redeemCodes.enable')}: ${row.code}`}
          >
            <span className="sr-only">
              {row.enabled ? t('admin:redeemCodes.disable') : t('admin:redeemCodes.enable')}
            </span>
          </Button>
        </Tooltip>
        <Tooltip content={t('common:actions.delete')}>
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] max-sm:size-11 max-sm:px-0"
            leadingIcon={<Trash2 size={13} aria-hidden />}
            onClick={onDelete}
            aria-label={`${t('common:actions.delete')}: ${row.code}`}
          >
            <span className="sr-only">{t('common:actions.delete')}</span>
          </Button>
        </Tooltip>
      </div>
  )
}

/* ──────── after-generate code list (inside new-batch dialog) ──────── */

function GeneratedList({ codes, onDone }: { codes: ApiRedeemCode[]; onDone: () => void }) {
  const { t } = useTranslation(['admin', 'common'])
  const { copied, copy } = useCopy()
  const allText = codes.map((c) => c.code).join('\n')

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-[12px] text-[var(--color-fg-muted)]">
          {t('admin:redeemCodes.createdToast', { count: codes.length })}
        </p>
        <Button
          variant="secondary"
          size="sm"
          leadingIcon={copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
          onClick={() => void copy(allText)}
        >
          {copied ? t('admin:redeemCodes.copied') : t('admin:redeemCodes.copyAll')}
        </Button>
      </div>
      <AdminTable
        className="max-h-[40vh] overflow-y-auto"
        items={codes}
        rowKey={(code) => code.id}
        label={t('admin:redeemCodes.title')}
        columns={[
          { id: 'code', header: t('admin:redeemCodes.table.code'), width: 260, render: (code) => <code className="break-all font-mono text-[13px]">{code.code}</code> },
          { id: 'actions', header: t('admin:common.actions'), width: 60, align: 'right', render: (code) => <Button variant="ghost" size="icon-sm" title={t('admin:redeemCodes.copy')} aria-label={t('admin:redeemCodes.copy')} onClick={() => void copy(code.code)}><Copy size={12} aria-hidden /></Button> },
        ]}
      />
      <div className="flex justify-end">
        <Button onClick={onDone}>{t('common:actions.close')}</Button>
      </div>
    </div>
  )
}
