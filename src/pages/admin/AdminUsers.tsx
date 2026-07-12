/**
 * AdminUsers — list users, create accounts, reset passwords, change roles, and
 * ban / unban (realtime via the cache kill channel). Each row links to the
 * per-user conversation drill-down used for support / abuse triage (§8.1).
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router-dom'
import { MessageSquare, Plus, Pencil, Trash2, Search, Info, Ban, ShieldCheck, Library, MoreHorizontal } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUser, ApiUserGroup } from '@/api/types'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
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
import { useAuth } from '@/store/auth'
import { formatDateTime, cn } from '@/lib/utils'
import { envNum } from '@/lib/env-config'

// A user counts as online if they made an authenticated request in the last 5
// minutes (the middleware refreshes last_seen_at at most once/min).
const ONLINE_WINDOW_S = envNum('VITE_AIVORY_ONLINE_WINDOW_S', 300)

type Role = 'user' | 'admin'

export default function AdminUsers() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const me = useAuth((s) => s.user)
  const [rows, setRows] = useState<ApiUser[]>([])
  const [total, setTotal] = useState(0)
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)

  // New-user dialog
  const [createOpen, setCreateOpen] = useState(false)
  const [draft, setDraft] = useState<{ email: string; name: string; password: string; role: Role }>({
    email: '',
    name: '',
    password: '',
    role: 'user',
  })
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)

  // Edit-user dialog (role + reset password)
  const [editRow, setEditRow] = useState<ApiUser | null>(null)
  const [editRole, setEditRole] = useState<Role>('user')
  const [editGroup, setEditGroup] = useState('')
  // Membership expiry as a yyyy-mm-dd date input value ('' = permanent).
  const [editExpiry, setEditExpiry] = useState('')
  const [editPassword, setEditPassword] = useState('')
  const [editCredits, setEditCredits] = useState(0)
  const [saving, setSaving] = useState(false)
  // Read-only user info dialog.
  const [infoRow, setInfoRow] = useState<ApiUser | null>(null)
  // Delete-user confirmation.
  const [deleteRow, setDeleteRow] = useState<ApiUser | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [query, setQuery] = useState('')
  const [committedQuery, setCommittedQuery] = useState('')
  const [page, setPage] = useState(1)
  const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE_3', 50)
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const pageRows = rows

  // Debounce search: commit the query 400ms after the user stops typing.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  function handleQueryChange(v: string) {
    setQuery(v)
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      setCommittedQuery(v)
      setPage(1)
    }, 400)
  }

  const groupsLoadedRef = useRef(false)

  const load = useCallback(async (search: string, p: number, opts?: { silent?: boolean }) => {
    if (!opts?.silent) setLoading(true)
    try {
      const offset = (p - 1) * PAGE_SIZE
      if (!groupsLoadedRef.current) {
        const [resp, gs] = await Promise.all([adminApi.users(search, PAGE_SIZE, offset), adminApi.userGroups()])
        setRows(resp.users)
        setTotal(resp.total)
        setGroups(gs)
        groupsLoadedRef.current = true
      } else {
        const resp = await adminApi.users(search, PAGE_SIZE, offset)
        setRows(resp.users)
        setTotal(resp.total)
      }
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    void load(committedQuery, page)
  }, [committedQuery, page, load])

  // While any account is being purged in the background, refresh periodically
  // (silently — no loading flash) so the row disappears once the job completes,
  // and surface each job's progress text on the badge.
  const [deletionProgress, setDeletionProgress] = useState<Record<string, string>>({})
  const hasDeleting = rows.some((u) => u.status === 'deleting')
  useEffect(() => {
    if (!hasDeleting) return
    const tick = async () => {
      await load(committedQuery, page, { silent: true })
      try {
        const resp = await adminApi.userDeletions()
        setDeletionProgress(
          Object.fromEntries(resp.jobs.map((j) => [j.user_id, j.status === 'failed' ? `failed: ${j.error ?? ''}` : j.progress])),
        )
      } catch {
        /* polling is best-effort */
      }
    }
    const id = window.setInterval(() => void tick(), 4000)
    return () => window.clearInterval(id)
  }, [hasDeleting, committedQuery, page, load])

  async function reload() {
    await load(committedQuery, page)
  }

  function persistOrder(next: ApiUser[], prev: ApiUser[]) {
    void adminApi.reorderUsers(next.map((u) => u.id)).catch((e) => {
      setRows(prev)
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  async function ban(u: ApiUser) {
    try {
      await adminApi.banUser(u.id)
      toast.success(t('admin:users.banned'))
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }
  async function unban(u: ApiUser) {
    try {
      await adminApi.unbanUser(u.id)
      toast.success(t('admin:users.reinstated'))
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }
  async function remove() {
    if (!deleteRow) return
    setDeleting(true)
    try {
      await adminApi.deleteUser(deleteRow.id)
      toast.success(t('admin:users.deleteStarted', { defaultValue: 'Deletion started — cleaning up in the background' }))
      setDeleteRow(null)
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setDeleting(false)
    }
  }

  function openCreate() {
    setDraft({ email: '', name: '', password: '', role: 'user' })
    setCreateOpen(true)
  }

  async function submitCreate() {
    if (creatingRef.current) return
    if (!draft.email.trim() || !draft.email.includes('@')) {
      toast.error(t('admin:users.errors.emailRequired'))
      return
    }
    if (draft.password.length < 8) {
      toast.error(t('admin:users.errors.passwordShort'))
      return
    }
    creatingRef.current = true
    setCreating(true)
    try {
      await adminApi.createUser({
        email: draft.email.trim(),
        name: draft.name.trim(),
        password: draft.password,
        role: draft.role,
      })
      toast.success(t('admin:users.created'))
      setCreateOpen(false)
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      creatingRef.current = false
      setCreating(false)
    }
  }

  async function resetTwoFa() {
    if (!editRow) return
    try {
      await adminApi.disableUser2fa(editRow.id)
      setEditRow({ ...editRow, totp_enabled: false })
      toast.success(t('admin:users.twofaReset'))
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  function openEdit(u: ApiUser) {
    setEditRow(u)
    setEditRole(u.role)
    setEditGroup(u.group_id || (groups.find((g) => g.is_default)?.id ?? ''))
    setEditExpiry(expiryToInput(u.group_expires_at ?? 0))
    setEditPassword('')
    setEditCredits(u.credits_permanent ?? 0)
  }

  async function submitEdit() {
    if (!editRow) return
    if (editPassword && editPassword.length < 8) {
      toast.error(t('admin:users.errors.passwordShort'))
      return
    }
    setSaving(true)
    try {
      if (editRole !== editRow.role) {
        await adminApi.setUserRole(editRow.id, editRole)
        toast.success(t('admin:users.roleChanged'))
      }
      const newExpiry = inputToExpiry(editExpiry)
      if (editGroup && (editGroup !== editRow.group_id || newExpiry !== (editRow.group_expires_at ?? 0))) {
        await adminApi.setUserGroup(editRow.id, editGroup, newExpiry)
        toast.success(t('admin:users.groupChanged'))
      }
      if (editPassword) {
        await adminApi.setUserPassword(editRow.id, editPassword)
        toast.success(t('admin:users.passwordSet'))
      }
      if (editCredits !== (editRow.credits_permanent ?? 0)) {
        await adminApi.setUserCredits(editRow.id, Math.max(0, editCredits))
        toast.success(t('admin:users.creditsSaved'))
      }
      setEditRow(null)
      await reload()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:users.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:users.lead')}</p>
        </div>
        <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openCreate}>
          {t('admin:users.new')}
        </Button>
      </header>

      <div className="mt-6">
        <Input
          value={query}
          onChange={(e) => handleQueryChange(e.target.value)}
          leadingIcon={<Search size={14} aria-hidden />}
          placeholder={t('admin:users.searchPlaceholder')}
          className="max-w-sm"
        />
      </div>

      <section className="mt-5">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : (
          <AdminSortableList
            items={pageRows}
            onItemsChange={setRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            rowClassName="grid grid-cols-[auto_auto_1fr_auto] gap-3 items-center px-5 py-4"
            renderItem={(u) => {
              const isMe = me?.id === u.id
              const group = groups.find((g) => g.id === u.group_id)
              const lastSeen = u.last_seen_at ?? 0
              const online = lastSeen > 0 && Date.now() / 1000 - lastSeen < ONLINE_WINDOW_S
              return (
                <>
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span
                        role="img"
                        aria-label={online ? t('admin:users.online') : t('admin:users.offline')}
                        className={cn(
                          'size-2 shrink-0 rounded-full',
                          online ? 'bg-[var(--color-success)]' : 'bg-[var(--color-fg-faint)]',
                        )}
                      />
                      <span className="font-medium text-[var(--color-fg)]">{u.name || u.email}</span>
                      <Badge size="xs">{t(`admin:users.role${u.role === 'admin' ? 'Admin' : 'User'}`)}</Badge>
                      {group && !group.is_default ? <Badge size="xs" variant="neutral">{group.name}</Badge> : null}
                      {u.status !== 'active' ? (
                        <Badge
                          size="xs"
                          variant={u.status === 'deleting' ? 'warning' : 'neutral'}
                          title={u.status === 'deleting' ? deletionProgress[u.id] : undefined}
                        >
                          {t(`admin:users.status.${u.status}`, { defaultValue: u.status })}
                        </Badge>
                      ) : null}
                      {isMe ? <Badge size="xs" variant="neutral">{t('admin:users.you')}</Badge> : null}
                    </div>
                    <div className="mt-0.5 flex items-center gap-2 text-[12px] text-[var(--color-fg-subtle)]">
                      <span className="font-mono truncate">{u.email}</span>
                      <span aria-hidden>·</span>
                      <span className="shrink-0">
                        {online
                          ? t('admin:users.online')
                          : lastSeen > 0
                            ? t('admin:users.lastSeen', { when: formatDateTime(lastSeen * 1000) })
                            : t('admin:users.neverSeen')}
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-0.5">
                    <IconAction label={t('admin:users.viewInfo')} onClick={() => setInfoRow(u)}>
                      <Info size={15} aria-hidden />
                    </IconAction>
                    <IconAction label={t('admin:common.edit')} onClick={() => openEdit(u)}>
                      <Pencil size={15} aria-hidden />
                    </IconAction>
                    <DropdownMenu>
                      <Tooltip content={t('admin:users.more')}>
                        <DropdownMenuTrigger
                          aria-label={t('admin:users.more')}
                          className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                        >
                          <MoreHorizontal size={15} aria-hidden />
                        </DropdownMenuTrigger>
                      </Tooltip>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => navigate(`/admin/users/${encodeURIComponent(u.id)}/conversations`)}>
                          <MessageSquare size={14} aria-hidden />
                          {t('admin:users.viewConversations')}
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => navigate(`/admin/users/${encodeURIComponent(u.id)}/library`)}>
                          <Library size={14} aria-hidden />
                          {t('admin:users.viewLibrary')}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                    {u.status === 'active' ? (
                      <IconAction label={t('admin:users.ban')} onClick={() => void ban(u)} disabled={isMe}>
                        <Ban size={15} aria-hidden />
                      </IconAction>
                    ) : (
                      <IconAction label={t('admin:users.unban')} onClick={() => void unban(u)} disabled={u.status === 'deleting'}>
                        <ShieldCheck size={15} aria-hidden />
                      </IconAction>
                    )}
                    <IconAction label={t('admin:common.delete')} onClick={() => setDeleteRow(u)} disabled={isMe} danger>
                      <Trash2 size={15} aria-hidden />
                    </IconAction>
                  </div>
                </>
              )
            }}
          />
        )}
        {!loading ? <Pagination page={page} pageCount={pageCount} onPage={setPage} /> : null}
      </section>

      {/* New user */}
      <Dialog open={createOpen} onOpenChange={(next) => !creatingRef.current && setCreateOpen(next)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:users.newTitle')}</DialogTitle>
            <DialogDescription>{t('admin:users.newLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:users.fields.email')} htmlFor="u-email">
                <Input
                  id="u-email"
                  type="email"
                  value={draft.email}
                  onChange={(e) => setDraft({ ...draft, email: e.target.value })}
                  placeholder="user@example.com"
                />
              </Field>
              <Field label={t('admin:users.fields.name')} htmlFor="u-name">
                <Input
                  id="u-name"
                  value={draft.name}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  placeholder="Astrid Holm"
                />
              </Field>
              <Field label={t('admin:users.fields.password')} htmlFor="u-pw" hint={t('admin:users.fields.passwordHint')}>
                <Input
                  id="u-pw"
                  type="password"
                  value={draft.password}
                  onChange={(e) => setDraft({ ...draft, password: e.target.value })}
                  placeholder="••••••••"
                />
              </Field>
              <Field label={t('admin:users.fields.role')} htmlFor="u-role">
                <Select value={draft.role} onValueChange={(v) => setDraft({ ...draft, role: v as Role })}>
                  <SelectTrigger id="u-role">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">{t('admin:users.roleUser')}</SelectItem>
                    <SelectItem value="admin">{t('admin:users.roleAdmin')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreateOpen(false)} disabled={creating}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={creating} onClick={() => void submitCreate()}>
              {t('admin:users.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Edit user — role + reset password */}
      <Dialog open={Boolean(editRow)} onOpenChange={(o) => !o && setEditRow(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{editRow ? t('admin:users.editTitle', { name: editRow.name || editRow.email }) : ''}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:users.fields.role')} htmlFor="e-role">
                <Select
                  value={editRole}
                  onValueChange={(v) => setEditRole(v as Role)}
                  disabled={Boolean(editRow && me?.id === editRow.id)}
                >
                  <SelectTrigger id="e-role">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">{t('admin:users.roleUser')}</SelectItem>
                    <SelectItem value="admin">{t('admin:users.roleAdmin')}</SelectItem>
                  </SelectContent>
                </Select>
                {editRow && me?.id === editRow.id ? (
                  <p className="mt-1.5 text-[12px] text-[var(--color-fg-subtle)]">{t('admin:users.selfRoleHint')}</p>
                ) : null}
              </Field>
              <Field label={t('admin:users.fields.group')} htmlFor="e-group" hint={t('admin:users.fields.groupHint')}>
                <Select value={editGroup} onValueChange={setEditGroup}>
                  <SelectTrigger id="e-group">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {groups.map((g) => (
                      <SelectItem key={g.id} value={g.id}>
                        {g.name}
                        {g.is_default ? ` · ${t('admin:groups.default')}` : ''}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('admin:users.fields.groupExpiry')}
                htmlFor="e-expiry"
                hint={t('admin:users.fields.groupExpiryHint')}
              >
                <Input
                  id="e-expiry"
                  type="date"
                  value={editExpiry}
                  onChange={(e) => setEditExpiry(e.target.value)}
                />
              </Field>
              {editRow?.totp_enabled ? (
                <Field label={t('admin:users.fields.twofa')} hint={t('admin:users.twofaHint')}>
                  <Button variant="secondary" onClick={() => void resetTwoFa()}>
                    {t('admin:users.twofaReset')}
                  </Button>
                </Field>
              ) : null}
              <Field
                label={t('admin:users.fields.newPassword')}
                htmlFor="e-pw"
                hint={t('admin:users.fields.passwordEditHint')}
              >
                <Input
                  id="e-pw"
                  type="password"
                  value={editPassword}
                  onChange={(e) => setEditPassword(e.target.value)}
                  placeholder="••••••••"
                  autoComplete="new-password"
                />
              </Field>
              <Field
                label={t('admin:users.fields.permanentCredits')}
                htmlFor="e-credits"
                hint={t('admin:users.fields.permanentCreditsHint')}
              >
                <Input
                  id="e-credits"
                  type="number"
                  min={0}
                  value={String(editCredits)}
                  onChange={(e) => setEditCredits(Math.max(0, Number(e.target.value) || 0))}
                />
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditRow(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submitEdit()}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* User info (read-only) */}
      <Dialog open={Boolean(infoRow)} onOpenChange={(o) => !o && setInfoRow(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{infoRow ? infoRow.name || infoRow.email : ''}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            {infoRow ? (
              <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5 text-sm">
                <InfoLine label={t('admin:users.fields.email')} value={infoRow.email} mono />
                <InfoLine
                  label={t('admin:users.fields.role')}
                  value={t(`admin:users.role${infoRow.role === 'admin' ? 'Admin' : 'User'}`)}
                />
                <InfoLine label={t('admin:users.info.status')} value={infoRow.status} />
                <InfoLine
                  label={t('admin:users.fields.group')}
                  value={groups.find((g) => g.id === infoRow.group_id)?.name ?? '—'}
                />
                <InfoLine
                  label={t('admin:users.info.expiry')}
                  value={
                    (infoRow.group_expires_at ?? 0) > 0
                      ? formatDateTime((infoRow.group_expires_at ?? 0) * 1000)
                      : t('admin:users.info.permanent')
                  }
                />
                <InfoLine
                  label={t('admin:users.fields.permanentCredits')}
                  value={(infoRow.credits_permanent ?? 0).toLocaleString()}
                />
                {(() => {
                  const g = groups.find((x) => x.id === infoRow.group_id)
                  return g && g.credit_allowance > 0 ? (
                    <InfoLine label={t('admin:users.info.allowance')} value={g.credit_allowance.toLocaleString()} />
                  ) : null
                })()}
                <InfoLine
                  label={t('admin:users.info.twofa')}
                  value={infoRow.totp_enabled ? t('admin:users.info.enabled') : t('admin:users.info.disabled')}
                />
                <InfoLine
                  label={t('admin:users.info.lastSeen')}
                  value={infoRow.last_seen_at ? formatDateTime(infoRow.last_seen_at * 1000) : t('admin:users.neverSeen')}
                />
                <InfoLine label={t('admin:users.info.created')} value={formatDateTime(infoRow.created_at * 1000)} />
              </dl>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setInfoRow(null)}>
              {t('common:actions.close', { defaultValue: 'Close' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete user confirmation */}
      <Dialog open={Boolean(deleteRow)} onOpenChange={(o) => !o && setDeleteRow(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:users.deleteTitle')}</DialogTitle>
            <DialogDescription>
              {t('admin:users.deleteBody', { name: deleteRow?.name || deleteRow?.email || '' })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteRow(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void remove()}>
              {t('admin:common.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// IconAction — a compact, label-only-on-hover icon button (or link) for the
// user row. Tooltip carries the accessible name so the rail stays uncluttered.
function IconAction({
  label,
  onClick,
  href,
  disabled,
  danger,
  children,
}: {
  label: string
  onClick?: () => void
  href?: string
  disabled?: boolean
  danger?: boolean
  children: ReactNode
}) {
  const cls = cn(
    'inline-flex items-center justify-center size-8 rounded-[8px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-40 disabled:cursor-not-allowed',
    danger
      ? 'text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)]'
      : 'text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
  )
  return (
    <Tooltip content={label}>
      {href ? (
        <Link to={href} aria-label={label} className={cls}>
          {children}
        </Link>
      ) : (
        <button type="button" aria-label={label} onClick={onClick} disabled={disabled} className={cls}>
          {children}
        </button>
      )}
    </Tooltip>
  )
}

function InfoLine({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className={cn('text-right break-words text-[var(--color-fg)]', mono && 'font-mono text-[12.5px]')}>{value}</dd>
    </>
  )
}

// Membership expiry conversions between the API's unix seconds and the
// <input type="date"> yyyy-mm-dd value. '' / 0 means permanent.
function expiryToInput(sec: number): string {
  if (!sec || sec <= 0) return ''
  const d = new Date(sec * 1000)
  if (Number.isNaN(d.getTime())) return ''
  const z = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`
}
function inputToExpiry(s: string): number {
  if (!s) return 0
  const ms = Date.parse(`${s}T23:59:59`)
  return Number.isNaN(ms) ? 0 : Math.floor(ms / 1000)
}
