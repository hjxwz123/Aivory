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
import { Copy, Plus, Trash2, RotateCcw, Ticket, Check, Download } from 'lucide-react'
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

type StatusFilter = 'all' | 'unused' | 'redeemed' | 'disabled' | 'expired'

interface BatchDraft {
  group_id: string
  duration_days: number
  max_uses: number
  expires_at: string // datetime-local format; converted to unix on submit
  note: string
  batch_name: string
  quantity: number
}

const EMPTY_DRAFT: BatchDraft = {
  group_id: '',
  duration_days: 30,
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
  const [newOpen, setNewOpen] = useState(false)
  const [draft, setDraft] = useState<BatchDraft>(EMPTY_DRAFT)
  const [submitting, setSubmitting] = useState(false)
  const submittingRef = useRef(false)
  const [confirmDelete, setConfirmDelete] = useState<ApiRedeemCode | null>(null)
  const [generated, setGenerated] = useState<ApiRedeemCode[] | null>(null)
  const [page, setPage] = useState(1)
  const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE_2', 20)
  const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE))
  const pageRows = rows.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)
  useEffect(() => {
    setPage(1)
  }, [status, batchFilter, rows.length])

  async function load() {
    setLoading(true)
    try {
      const [codes, gs] = await Promise.all([
        adminApi.redeemCodes({
          status: status === 'all' ? undefined : status,
          batch: batchFilter || undefined,
          limit: 500,
        }),
        adminApi.userGroups(),
      ])
      setRows(codes)
      setGroups(gs)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, batchFilter])

  function openNew() {
    setDraft({ ...EMPTY_DRAFT, group_id: groups.find((g) => !g.is_default)?.id ?? groups[0]?.id ?? '' })
    setNewOpen(true)
    setGenerated(null)
  }

  async function submit() {
    if (submittingRef.current) return
    if (!draft.group_id) {
      toast.error(t('admin:redeemCodes.errors.groupRequired'))
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
        group_id: draft.group_id,
        duration_days: draft.duration_days,
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
    try {
      await adminApi.updateRedeemCode(row.id, { enabled: !row.enabled })
      toast.success(row.enabled ? t('admin:redeemCodes.disabled') : t('admin:redeemCodes.enabled'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  async function remove(row: ApiRedeemCode) {
    try {
      await adminApi.removeRedeemCode(row.id)
      toast.success(t('admin:redeemCodes.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
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
    const header = ['code', 'group', 'status', 'duration_days', 'used_count', 'max_uses', 'batch_name', 'note', 'expires_at', 'created_at']
    const now = Math.floor(Date.now() / 1000)
    const statusOf = (r: ApiRedeemCode) => {
      if (!r.enabled) return 'disabled'
      if (r.expires_at > 0 && r.expires_at < now) return 'expired'
      if (r.used_count >= r.max_uses) return 'redeemed'
      if (r.used_count > 0) return 'partial'
      return 'unused'
    }
    const iso = (unix: number) => (unix > 0 ? new Date(unix * 1000).toISOString() : '')
    const lines = [header.join(',')]
    for (const r of rows) {
      lines.push(
        [
          esc(r.code),
          esc(groupByID.get(r.group_id)?.name ?? r.group_id),
          esc(statusOf(r)),
          esc(r.duration_days),
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
      <header className="flex items-end justify-between gap-4 flex-wrap">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:redeemCodes.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:redeemCodes.lead')}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            leadingIcon={<Download size={15} aria-hidden />}
            disabled={rows.length === 0}
            onClick={exportCsv}
          >
            {t('admin:redeemCodes.export', { defaultValue: 'Export CSV' })}
          </Button>
          <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
            {t('admin:redeemCodes.new')}
          </Button>
        </div>
      </header>

      {/* Filters */}
      <div className="mt-6 flex items-center gap-2 flex-wrap">
        {(['all', 'unused', 'redeemed', 'disabled', 'expired'] as StatusFilter[]).map((s) => (
          <button
            key={s}
            type="button"
            onClick={() => setStatus(s)}
            className={
              'inline-flex items-center h-8 px-3 rounded-[8px] text-[12px] interactive ' +
              (status === s
                ? 'bg-[var(--color-surface)] border border-[var(--color-border-strong)] text-[var(--color-fg)]'
                : 'border border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]')
            }
          >
            {t(`admin:redeemCodes.filters.${s}`)}
          </button>
        ))}
        <div className="ml-auto w-56">
          <Input
            placeholder={t('admin:redeemCodes.table.batch')}
            value={batchFilter}
            onChange={(e) => setBatchFilter(e.target.value)}
          />
        </div>
      </div>

      <section className="mt-6">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="grid place-items-center px-6 py-16 rounded-[14px] border border-dashed border-[var(--color-border)] bg-[var(--color-bg-muted)]/30">
            <Ticket size={28} className="text-[var(--color-fg-faint)]" aria-hidden />
            <p className="mt-4 text-sm text-[var(--color-fg-muted)]">{t('admin:redeemCodes.empty')}</p>
          </div>
        ) : (
          <>
            <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
              {pageRows.map((rc) => (
                <CodeRow
                  key={rc.id}
                  row={rc}
                  group={groupByID.get(rc.group_id)}
                  onToggleEnabled={() => void toggleEnabled(rc)}
                  onDelete={() => setConfirmDelete(rc)}
                />
              ))}
            </ul>
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
                <div className="grid grid-cols-2 gap-4">
                  <Field label={t('admin:redeemCodes.fields.durationDays')} htmlFor="rc-dur" hint={t('admin:redeemCodes.fields.durationDaysHint')}>
                    <Input
                      id="rc-dur"
                      type="number"
                      min={0}
                      value={String(draft.duration_days)}
                      onChange={(e) => setDraft({ ...draft, duration_days: Math.max(0, Number(e.target.value) || 0) })}
                    />
                  </Field>
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
                <div className="grid grid-cols-2 gap-4">
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
            <Button variant="ghost" onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/* ───────────────────────── row ─────────────────────────── */

function CodeRow({
  row,
  group,
  onToggleEnabled,
  onDelete,
}: {
  row: ApiRedeemCode
  group?: ApiUserGroup
  onToggleEnabled: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation(['admin', 'common'])
  const { copied, copy } = useCopy()

  const now = Math.floor(Date.now() / 1000)
  const codeExpired = row.expires_at > 0 && row.expires_at < now
  const fullyUsed = row.used_count >= row.max_uses

  let statusLabel = t('admin:redeemCodes.status.unused')
  let statusVariant: 'neutral' | 'sage' = 'neutral'
  if (!row.enabled) statusLabel = t('admin:redeemCodes.status.disabled')
  else if (codeExpired) statusLabel = t('admin:redeemCodes.status.expired')
  else if (fullyUsed) {
    statusLabel = t('admin:redeemCodes.status.redeemed')
    statusVariant = 'sage'
  } else if (row.used_count > 0) statusLabel = t('admin:redeemCodes.status.partial')

  const durationLabel =
    row.duration_days === 0
      ? t('admin:redeemCodes.durationPermanent')
      : t('admin:redeemCodes.durationDays', { count: row.duration_days })

  return (
    <li className="grid grid-cols-[1fr_auto] gap-3 items-center px-5 py-4">
      <div className="min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <code className="font-mono text-[13px] tracking-[0.08em] text-[var(--color-fg)] bg-[var(--color-bg-muted)] px-2 py-0.5 rounded-[6px]">
            {row.code}
          </code>
          <Badge size="xs" variant={statusVariant}>
            {statusLabel}
          </Badge>
          {group ? (
            <Badge size="xs" variant="neutral">
              {group.name}
            </Badge>
          ) : null}
          {row.batch_name ? (
            <span className="text-[11px] text-[var(--color-fg-subtle)]">{row.batch_name}</span>
          ) : null}
        </div>
        <div className="mt-1 text-[11.5px] text-[var(--color-fg-subtle)] tabular-nums">
          {durationLabel}
          <span aria-hidden className="mx-1.5 opacity-50">·</span>
          {row.used_count}/{row.max_uses} {t('admin:redeemCodes.table.uses')}
          <span aria-hidden className="mx-1.5 opacity-50">·</span>
          {row.expires_at > 0
            ? t('admin:redeemCodes.table.expiresAt') + ' ' + formatRelativeDate(row.expires_at * 1000)
            : t('admin:redeemCodes.noExpiry')}
          <span aria-hidden className="mx-1.5 opacity-50">·</span>
          {t('admin:redeemCodes.table.createdAt')} {formatRelativeDate(row.created_at * 1000)}
        </div>
        {row.note ? (
          <p className="mt-1 text-[12px] text-[var(--color-fg-muted)] line-clamp-1">{row.note}</p>
        ) : null}
      </div>
      <div className="flex items-center gap-1">
        <Tooltip content={copied ? t('admin:redeemCodes.copied') : t('admin:redeemCodes.copy')}>
          <Button
            variant="ghost"
            size="sm"
            aria-label={t('admin:redeemCodes.copy')}
            onClick={() => void copy(row.code)}
          >
            {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
          </Button>
        </Tooltip>
        <Button
          variant="ghost"
          size="sm"
          leadingIcon={<RotateCcw size={13} aria-hidden />}
          onClick={onToggleEnabled}
        >
          {row.enabled ? t('admin:redeemCodes.disable') : t('admin:redeemCodes.enable')}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          leadingIcon={<Trash2 size={13} aria-hidden />}
          onClick={onDelete}
        >
          {t('common:actions.delete')}
        </Button>
      </div>
    </li>
  )
}

/* ──────── after-generate code list (inside new-batch dialog) ──────── */

function GeneratedList({ codes, onDone }: { codes: ApiRedeemCode[]; onDone: () => void }) {
  const { t } = useTranslation(['admin', 'common'])
  const { copied, copy } = useCopy()
  const allText = codes.map((c) => c.code).join('\n')

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
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
      <ul className="max-h-[40vh] overflow-y-auto rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)]/40">
        {codes.map((c) => (
          <li
            key={c.id}
            className="flex items-center justify-between gap-2 px-3 py-1.5 border-b border-[var(--color-divider)] last:border-b-0"
          >
            <code className="font-mono text-[13px] tracking-[0.08em] text-[var(--color-fg)]">{c.code}</code>
            <Button
              variant="ghost"
              size="sm"
              aria-label={t('admin:redeemCodes.copy')}
              onClick={() => void copy(c.code)}
            >
              <Copy size={12} aria-hidden />
            </Button>
          </li>
        ))}
      </ul>
      <div className="flex justify-end">
        <Button onClick={onDone}>{t('common:actions.close')}</Button>
      </div>
    </div>
  )
}
