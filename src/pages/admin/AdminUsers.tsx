/**
 * AdminUsers — list users, create accounts, reset passwords, change roles, and
 * ban / unban (realtime via the cache kill channel). Each row links to the
 * per-user conversation drill-down used for support / abuse triage (§8.1).
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { MessageSquare, Plus, Pencil, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUser, ApiUserGroup } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
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
import { formatRelativeDate, cn } from '@/lib/utils'

// A user counts as online if they made an authenticated request in the last 5
// minutes (the middleware refreshes last_seen_at at most once/min).
const ONLINE_WINDOW_S = 300

type Role = 'user' | 'admin'

export default function AdminUsers() {
  const { t } = useTranslation(['admin', 'common'])
  const me = useAuth((s) => s.user)
  const [rows, setRows] = useState<ApiUser[]>([])
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

  // Edit-user dialog (role + reset password)
  const [editRow, setEditRow] = useState<ApiUser | null>(null)
  const [editRole, setEditRole] = useState<Role>('user')
  const [editGroup, setEditGroup] = useState('')
  const [editPassword, setEditPassword] = useState('')
  const [saving, setSaving] = useState(false)
  // Delete-user confirmation.
  const [deleteRow, setDeleteRow] = useState<ApiUser | null>(null)
  const [deleting, setDeleting] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const [users, gs] = await Promise.all([adminApi.users(), adminApi.userGroups()])
      setRows(users)
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
  }, [])

  async function ban(u: ApiUser) {
    try {
      await adminApi.banUser(u.id)
      toast.success(t('admin:users.banned'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }
  async function unban(u: ApiUser) {
    try {
      await adminApi.unbanUser(u.id)
      toast.success(t('admin:users.reinstated'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }
  async function remove() {
    if (!deleteRow) return
    setDeleting(true)
    try {
      await adminApi.deleteUser(deleteRow.id)
      toast.success(t('admin:users.deleted'))
      setDeleteRow(null)
      await load()
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
    if (!draft.email.trim() || !draft.email.includes('@')) {
      toast.error(t('admin:users.errors.emailRequired'))
      return
    }
    if (draft.password.length < 8) {
      toast.error(t('admin:users.errors.passwordShort'))
      return
    }
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
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setCreating(false)
    }
  }

  async function resetTwoFa() {
    if (!editRow) return
    try {
      await adminApi.disableUser2fa(editRow.id)
      setEditRow({ ...editRow, totp_enabled: false })
      toast.success(t('admin:users.twofaReset'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  function openEdit(u: ApiUser) {
    setEditRow(u)
    setEditRole(u.role)
    setEditGroup(u.group_id || (groups.find((g) => g.is_default)?.id ?? ''))
    setEditPassword('')
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
      if (editGroup && editGroup !== editRow.group_id) {
        await adminApi.setUserGroup(editRow.id, editGroup)
        toast.success(t('admin:users.groupChanged'))
      }
      if (editPassword) {
        await adminApi.setUserPassword(editRow.id, editPassword)
        toast.success(t('admin:users.passwordSet'))
      }
      setEditRow(null)
      await load()
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

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((u) => {
              const isMe = me?.id === u.id
              const group = groups.find((g) => g.id === u.group_id)
              const lastSeen = u.last_seen_at ?? 0
              const online = lastSeen > 0 && Date.now() / 1000 - lastSeen < ONLINE_WINDOW_S
              return (
                <li key={u.id} className="grid grid-cols-[1fr_auto] gap-3 items-center px-5 py-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span
                        aria-hidden
                        title={online ? t('admin:users.online') : t('admin:users.offline')}
                        className={cn(
                          'size-2 shrink-0 rounded-full',
                          online ? 'bg-[var(--color-success)]' : 'bg-[var(--color-fg-faint)]',
                        )}
                      />
                      <span className="font-medium text-[var(--color-fg)]">{u.name || u.email}</span>
                      <Badge size="xs">{t(`admin:users.role${u.role === 'admin' ? 'Admin' : 'User'}`)}</Badge>
                      {group && !group.is_default ? <Badge size="xs" variant="neutral">{group.name}</Badge> : null}
                      {u.status !== 'active' ? <Badge size="xs" variant="neutral">{u.status}</Badge> : null}
                      {isMe ? <Badge size="xs" variant="neutral">{t('admin:users.you')}</Badge> : null}
                    </div>
                    <div className="mt-0.5 flex items-center gap-2 text-[12px] text-[var(--color-fg-subtle)]">
                      <span className="font-mono truncate">{u.email}</span>
                      <span aria-hidden>·</span>
                      <span className="shrink-0">
                        {online
                          ? t('admin:users.online')
                          : lastSeen > 0
                            ? t('admin:users.lastSeen', { when: formatRelativeDate(lastSeen * 1000) })
                            : t('admin:users.neverSeen')}
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button variant="ghost" size="sm" leadingIcon={<Pencil size={12} aria-hidden />} onClick={() => openEdit(u)}>
                      {t('admin:common.edit')}
                    </Button>
                    <Button asChild variant="ghost" size="sm" leadingIcon={<MessageSquare size={12} aria-hidden />}>
                      <Link to={`/admin/users/${encodeURIComponent(u.id)}/conversations`}>
                        {t('admin:users.viewConversations')}
                      </Link>
                    </Button>
                    {u.status === 'active' ? (
                      <Button variant="ghost" size="sm" disabled={isMe} onClick={() => void ban(u)}>
                        {t('admin:users.ban')}
                      </Button>
                    ) : (
                      <Button variant="ghost" size="sm" onClick={() => void unban(u)}>
                        {t('admin:users.unban')}
                      </Button>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={isMe}
                      leadingIcon={<Trash2 size={12} aria-hidden />}
                      className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
                      onClick={() => setDeleteRow(u)}
                    >
                      {t('admin:common.delete')}
                    </Button>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </section>

      {/* New user */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
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
            <Button variant="ghost" onClick={() => setCreateOpen(false)}>
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
