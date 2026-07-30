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
      <header className="flex flex-col items-stretch gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:redeemCodes.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:redeemCodes.lead')}</p>
        </div>
        <div className="grid w-full grid-cols-2 gap-2 sm:flex sm:w-auto sm:items-center">
          <Button
            variant="secondary"
            className="w-full sm:w-auto"
            leadingIcon={<Download size={15} aria-hidden />}
            disabled={rows.length === 0}
            onClick={exportCsv}
          >
            {t('admin:redeemCodes.export', { defaultValue: 'Export CSV' })}
          </Button>
          <Button className="w-full sm:w-auto" leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
            {t('admin:redeemCodes.new')}
          </Button>
        </div>
      </header>

      {/* Filters */}
      <div className="mt-6 grid gap-3 sm:flex sm:flex-wrap sm:items-center sm:gap-2">
        <div className="grid w-full grid-cols-2 gap-2 min-[420px]:grid-cols-3 sm:flex sm:w-auto sm:flex-wrap">
          {(['all', 'unused', 'partial', 'used', 'invalid'] as StatusFilter[]).map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => setStatus(s)}
              className={
                'inline-flex min-h-11 items-center justify-center rounded-[8px] px-3 text-[12px] interactive last:col-span-2 sm:h-8 sm:min-h-0 ' +
                (status === s
                  ? 'bg-[var(--color-surface)] border border-[var(--color-border-strong)] text-[var(--color-fg)]'
                  : 'border border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]')
              }
            >
              {t(`admin:redeemCodes.filters.${s}`)}
            </button>
          ))}
        </div>
        <div className="w-full sm:ml-auto sm:w-56">
          <Input
            placeholder={t('admin:redeemCodes.table.batch')}
            value={batchFilter}
            onChange={(e) => setBatchFilter(e.target.value)}
          />
        </div>
      </div>

      <section className="mt-4 sm:mt-6">
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="grid place-items-center rounded-[14px] border border-dashed border-[var(--color-border)] bg-[var(--color-bg-muted)]/30 px-4 py-10 sm:px-6 sm:py-16">
            <Ticket size={28} className="text-[var(--color-fg-faint)]" aria-hidden />
            <p className="mt-4 text-sm text-[var(--color-fg-muted)]">{t('admin:redeemCodes.empty')}</p>
          </div>
        ) : (
          <>
            <ul className="flex flex-col divide-y divide-[var(--color-divider)] overflow-hidden rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
              {pageRows.map((rc) => (
                <CodeRow
                  key={rc.id}
                  row={rc}
                  group={groupByID.get(rc.group_id)}
                  toggling={togglingId === rc.id}
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
                            ? 'bg-[var(--color-surface)] border border-[var(--color-border-strong)] text-[var(--color-fg)]'
                            : 'border border-[var(--color-border)] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]')
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

function CodeRow({
  row,
  group,
  toggling,
  onToggleEnabled,
  onDelete,
}: {
  row: ApiRedeemCode
  group?: ApiUserGroup
  toggling: boolean
  onToggleEnabled: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation(['admin', 'common'])
  const { copied, copy } = useCopy()

  const codeStatus = getRedeemCodeStatus(row)
  const statusPresentation = {
    unused: {
      variant: 'success' as const,
      icon: <CircleCheck size={11} aria-hidden />,
    },
    partial: {
      variant: 'warning' as const,
      icon: <CircleDotDashed size={11} aria-hidden />,
    },
    used: {
      variant: 'neutral' as const,
      icon: <CheckCheck size={11} aria-hidden />,
    },
    invalid: {
      variant: 'danger' as const,
      icon: <CircleX size={11} aria-hidden />,
    },
  }[codeStatus]

  const isCredits = row.kind === 'credits'
  const durationLabel = isCredits
    ? t('admin:redeemCodes.creditsAmount', { count: row.credits })
    : row.duration_days === 0
      ? t('admin:redeemCodes.durationPermanent')
      : t('admin:redeemCodes.durationDays', { count: row.duration_days })

  return (
    <li className="grid grid-cols-1 gap-3 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:px-5 sm:py-4">
      <div className="min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <code className="font-mono text-[13px] tracking-[0.08em] text-[var(--color-fg)] bg-[var(--color-bg-muted)] px-2 py-0.5 rounded-[6px]">
            {row.code}
          </code>
          <Badge size="xs" variant={statusPresentation.variant} leadingIcon={statusPresentation.icon}>
            {t(`admin:redeemCodes.status.${codeStatus}`)}
          </Badge>
          {isCredits ? (
            <Badge size="xs" variant="sage">
              {t('admin:redeemCodes.kinds.credits')}
            </Badge>
          ) : group ? (
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
      <div className="flex items-center justify-end gap-1 border-t border-[var(--color-divider)] pt-2 sm:border-0 sm:pt-0">
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
            size="sm"
            className="max-sm:size-11 max-sm:px-0"
            leadingIcon={<RotateCcw size={13} aria-hidden />}
            loading={toggling}
            disabled={toggling}
            onClick={onToggleEnabled}
            aria-label={`${row.enabled ? t('admin:redeemCodes.disable') : t('admin:redeemCodes.enable')}: ${row.code}`}
          >
            <span className="hidden sm:inline">
              {row.enabled ? t('admin:redeemCodes.disable') : t('admin:redeemCodes.enable')}
            </span>
          </Button>
        </Tooltip>
        <Tooltip content={t('common:actions.delete')}>
          <Button
            variant="ghost"
            size="sm"
            className="text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] max-sm:size-11 max-sm:px-0"
            leadingIcon={<Trash2 size={13} aria-hidden />}
            onClick={onDelete}
            aria-label={`${t('common:actions.delete')}: ${row.code}`}
          >
            <span className="hidden sm:inline">{t('common:actions.delete')}</span>
          </Button>
        </Tooltip>
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
      <ul className="max-h-[40vh] overflow-y-auto rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)]/40">
        {codes.map((c) => (
          <li
            key={c.id}
            className="flex min-w-0 items-center justify-between gap-2 border-b border-[var(--color-divider)] px-3 py-2 last:border-b-0"
          >
            <code className="min-w-0 break-all font-mono text-[13px] tracking-[0.08em] text-[var(--color-fg)]">{c.code}</code>
            <Button
              variant="ghost"
              size="icon-sm"
              className="shrink-0 max-sm:size-11"
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
