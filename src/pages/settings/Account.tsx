import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Mail, User, Lock, AlertTriangle, LogOut } from 'lucide-react'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { useAuth } from '@/store/auth'
import { authApi, ApiError } from '@/api'
import { toast } from '@/hooks/use-toast'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/label'

export default function Account() {
  const { t } = useTranslation(['settings', 'common'])
  const navigate = useNavigate()
  const user = useAuth((s) => s.user)
  const updateProfile = useAuth((s) => s.updateProfile)
  const logout = useAuth((s) => s.logout)
  const [name, setName] = useState(user?.name ?? '')
  const [email, setEmail] = useState(user?.email ?? '')
  const [saving, setSaving] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [pwOpen, setPwOpen] = useState(false)
  const [pw, setPw] = useState({ current: '', next: '' })

  useEffect(() => {
    if (!user) return
    setName(user.name)
    setEmail(user.email)
  }, [user])

  async function save() {
    setSaving(true)
    try {
      await updateProfile({ name, email })
      toast.success(t('settings:account.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : 'Failed')
    } finally {
      setSaving(false)
    }
  }

  async function changePassword() {
    if (pw.next.length < 8) {
      toast.error('Password must be at least 8 characters')
      return
    }
    try {
      await authApi.changePassword(pw.current, pw.next)
      toast.success('Password updated — please sign in again')
      setPwOpen(false)
      setPw({ current: '', next: '' })
      await logout()
      navigate('/login')
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : 'Failed')
    }
  }

  return (
    <div className="max-w-[44rem]">
      <header className="mb-8">
        <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)]">{t('settings:account.title')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">{t('settings:account.subtitle')}</p>
      </header>

      <SettingsSection title={t('settings:account.profile')}>
        <SettingsRow label={t('settings:account.rows.avatar')} description={t('settings:account.rows.avatarBody')}>
          <Avatar size="xl" tone="clay">
            <AvatarFallback>{initials(name || email || '?')}</AvatarFallback>
          </Avatar>
        </SettingsRow>
        <SettingsRow label={t('settings:account.rows.name')} description={t('settings:account.rows.nameBody')}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            leadingIcon={<User size={14} aria-hidden />}
            className="w-64"
          />
        </SettingsRow>
        <SettingsRow label={t('settings:account.rows.email')} description={t('settings:account.rows.emailBody')}>
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            leadingIcon={<Mail size={14} aria-hidden />}
            className="w-64"
          />
        </SettingsRow>
        <div className="px-5 sm:px-6 py-4 flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={() => { setName(user?.name ?? ''); setEmail(user?.email ?? '') }}>
            {t('common:actions.reset')}
          </Button>
          <Button onClick={() => void save()} loading={saving}>
            {t('common:actions.save')}
          </Button>
        </div>
      </SettingsSection>

      <SettingsSection title={t('settings:account.security')}>
        <SettingsRow label={t('settings:account.securityRows.password')} description={t('settings:account.securityRows.passwordBody')}>
          <Button variant="secondary" onClick={() => setPwOpen(true)}>
            <Lock size={13} aria-hidden /> {t('common:actions.changePassword')}
          </Button>
        </SettingsRow>
        <SettingsRow label={t('settings:account.securityRows.sessions')} description={t('settings:account.securityRows.sessionsBody')}>
          <Button variant="ghost" onClick={() => void (async () => { await logout(); navigate('/login') })()}>
            <LogOut size={13} aria-hidden /> Sign out
          </Button>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection title={t('settings:account.danger')} description={t('settings:account.dangerBody')}>
        <SettingsRow
          label={t('settings:account.dangerRows.delete')}
          description={t('settings:account.dangerRows.deleteBody')}
        >
          <Button variant="destructive" onClick={() => setConfirmDelete(true)} leadingIcon={<AlertTriangle size={14} aria-hidden />}>
            {t('settings:account.dangerRows.delete')}
          </Button>
        </SettingsRow>
      </SettingsSection>

      <Dialog open={pwOpen} onOpenChange={setPwOpen}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('common:actions.changePassword')}</DialogTitle>
            <DialogDescription>You'll be signed out on success.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-3">
              <Field label="Current password" htmlFor="pw-cur">
                <Input
                  id="pw-cur"
                  type="password"
                  value={pw.current}
                  onChange={(e) => setPw({ ...pw, current: e.target.value })}
                />
              </Field>
              <Field label="New password" htmlFor="pw-new" hint="Minimum 8 characters">
                <Input
                  id="pw-new"
                  type="password"
                  value={pw.next}
                  onChange={(e) => setPw({ ...pw, next: e.target.value })}
                />
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setPwOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button onClick={() => void changePassword()}>{t('common:actions.save')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('settings:account.dangerRows.dialogTitle')}</DialogTitle>
            <DialogDescription>
              {t('settings:account.dangerRows.dialogBody')}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                setConfirmDelete(false)
                toast.success(t('settings:account.dangerRows.queued'))
              }}
            >
              {t('settings:account.dangerRows.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
