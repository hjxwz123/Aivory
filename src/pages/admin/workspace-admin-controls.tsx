import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, workspacesApi } from '@/api'
import type { ApiUser, ApiWorkspaceMember } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, DialogFooter } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'

export function WorkspaceAdminControls({ workspaceId, members = [], ownerId, onSaved }: { workspaceId?: string; members?: ApiWorkspaceMember[]; ownerId?: string; onSaved: () => void }) {
  const { t } = useTranslation('admin')
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [search, setSearch] = useState('')
  const [users, setUsers] = useState<ApiUser[]>([])
  const [target, setTarget] = useState('')
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const mutation = useRef(false)
  const transfer = !!workspaceId
  const title = t(transfer ? 'workspaces.changeAdmin' : 'workspaces.createAdmin')
  useEffect(() => {
    if (!open || transfer) return
    let active = true
    setLoading(true)
    const timer = window.setTimeout(() => {
      adminApi.users(search, 50).then((r) => { if (active) { setUsers(r.users.filter((u) => u.status === 'active')); setError('') } }).catch((e) => { if (active) setError(e instanceof Error ? e.message : t('domains.loadFailed')) }).finally(() => { if (active) setLoading(false) })
    }, 250)
    return () => { active = false; window.clearTimeout(timer) }
  }, [open, search, transfer, t])
  async function save() {
    if (mutation.current || !target || (!transfer && !name.trim())) return
    mutation.current = true; setBusy(true); setError('')
    try {
      if (workspaceId) await workspacesApi.adminTransfer(workspaceId, target)
      else await workspacesApi.adminCreate(name.trim(), target)
      toast.success(t('domains.saved')); setOpen(false); setTarget(''); setName(''); onSaved()
    } catch (e) { setError(e instanceof Error ? e.message : t('domains.saveFailed')) }
    finally { mutation.current = false; setBusy(false) }
  }
  const candidates = transfer ? members.filter((m) => m.user_id !== ownerId).map((m) => ({ id: m.user_id, name: m.name, email: m.email })) : users
  return <>
    <Button variant={transfer ? 'secondary' : 'primary'} onClick={() => { setError(''); setOpen(true) }}>{title}</Button>
    <Dialog open={open} onOpenChange={(v) => { if (!busy) setOpen(v) }}>
      <DialogContent className="max-w-lg">
        <DialogHeader><DialogTitle>{title}</DialogTitle><DialogDescription>{t(transfer ? 'workspaces.changeAdminHint' : 'workspaces.createAdminHint')}</DialogDescription></DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); void save() }}>
          <DialogBody className="space-y-5">
            {!transfer && <>
              <div className="space-y-2"><label htmlFor="admin-workspace-name" className="text-sm font-medium">{t('workspaces.colName')}</label><Input id="admin-workspace-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} disabled={busy} /></div>
              <div className="space-y-2"><label htmlFor="admin-owner-search" className="text-sm font-medium">{t('workspaces.searchAdmin')}</label><Input id="admin-owner-search" value={search} onChange={(e) => { setSearch(e.target.value); setTarget('') }} disabled={busy} /></div>
            </>}
            <div className="space-y-2"><label id="workspace-admin-label" className="text-sm font-medium">{t('workspaces.roleAdmin')}</label>
              <Select value={target} onValueChange={setTarget} disabled={busy || loading}><SelectTrigger aria-labelledby="workspace-admin-label"><SelectValue placeholder={t(loading ? 'workspaces.loadingAdmins' : 'workspaces.chooseAdmin')} /></SelectTrigger><SelectContent>{candidates.map((u) => <SelectItem key={u.id} value={u.id}>{u.name} · {u.email || u.id}</SelectItem>)}</SelectContent></Select>
              {!loading && !candidates.length && <p className="text-sm text-[var(--color-fg-muted)]">{t('workspaces.noAdmins')}</p>}
            </div>
            {error && <p role="alert" className="text-sm text-[var(--color-danger)]">{error}</p>}
          </DialogBody>
          <DialogFooter><Button type="button" variant="ghost" disabled={busy} onClick={() => setOpen(false)}>{t('domains.cancel')}</Button><Button type="submit" loading={busy} disabled={!target || (!transfer && !name.trim())}>{t('domains.save')}</Button></DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  </>
}
