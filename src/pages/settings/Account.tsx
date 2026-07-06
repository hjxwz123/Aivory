import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Mail, User, Lock, AlertTriangle, ShieldCheck, Copy, Upload } from 'lucide-react'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { ActiveSessions } from '@/components/settings/active-sessions'
import { IdentitySources } from '@/components/settings/identity-sources'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { useAuth } from '@/store/auth'
import { authApi, ApiError } from '@/api'
import { resizeImageForUpload } from '@/lib/resize-image'
import { formatAbsoluteDate } from '@/lib/utils'
import { toast } from '@/hooks/use-toast'
import { useCopy } from '@/hooks/use-clipboard'
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
  const { copy } = useCopy()
  const user = useAuth((s) => s.user)
  const updateProfile = useAuth((s) => s.updateProfile)
  const setUser = useAuth((s) => s.setUser)
  const logout = useAuth((s) => s.logout)
  const [name, setName] = useState(user?.name ?? '')
  const [email, setEmail] = useState(user?.email ?? '')
  const [saving, setSaving] = useState(false)
  const [avatarBusy, setAvatarBusy] = useState(false)
  const avatarRef = useRef<HTMLInputElement>(null)
  const avatarUrl = (user?.settings as Record<string, unknown> | undefined)?.avatar_url as string | undefined
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [deletePassword, setDeletePassword] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [pwOpen, setPwOpen] = useState(false)
  const [pw, setPw] = useState({ current: '', next: '' })
  const [isChangingPassword, setIsChangingPassword] = useState(false)

  // Two-factor (TOTP) state.
  const twoFactorOn = Boolean(user?.totp_enabled)
  const [setup, setSetup] = useState<{ secret: string; otpauth_url: string } | null>(null)
  const [enableOpen, setEnableOpen] = useState(false)
  const [disableOpen, setDisableOpen] = useState(false)
  const [code, setCode] = useState('')
  const [twoFaBusy, setTwoFaBusy] = useState(false)

  async function refreshUser() {
    try {
      setUser(await authApi.me())
    } catch {
      /* ignore — next hydrate will reconcile */
    }
  }

  async function beginEnable() {
    setTwoFaBusy(true)
    try {
      const s = await authApi.setup2fa()
      setSetup(s)
      setCode('')
      setEnableOpen(true)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.twofa.failed'))
    } finally {
      setTwoFaBusy(false)
    }
  }

  async function confirmEnable() {
    if (code.trim().length < 6) {
      toast.error(t('settings:account.twofa.codeRequired'))
      return
    }
    setTwoFaBusy(true)
    try {
      await authApi.enable2fa(code.trim())
      await refreshUser()
      setEnableOpen(false)
      setSetup(null)
      toast.success(t('settings:account.twofa.enabled'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.twofa.invalid'))
    } finally {
      setTwoFaBusy(false)
    }
  }

  async function confirmDisable() {
    if (code.trim().length < 6) {
      toast.error(t('settings:account.twofa.codeRequired'))
      return
    }
    setTwoFaBusy(true)
    try {
      await authApi.disable2fa(code.trim())
      await refreshUser()
      setDisableOpen(false)
      toast.success(t('settings:account.twofa.disabled'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.twofa.invalid'))
    } finally {
      setTwoFaBusy(false)
    }
  }

  useEffect(() => {
    if (!user) return
    setName(user.name)
    setEmail(user.email)
  }, [user])

  async function save() {
    setSaving(true)
    try {
      await updateProfile({ name })
      toast.success(t('settings:account.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : 'Failed')
    } finally {
      setSaving(false)
    }
  }

  async function setAvatar(url: string) {
    // Persist into user settings (returned with /me) so it shows everywhere.
    await authApi.updateSettings({ avatar_url: url })
    if (user) setUser({ ...user, settings: { ...(user.settings ?? {}), avatar_url: url } })
  }
  async function onPickAvatar(file: File | undefined) {
    if (!file) return
    setAvatarBusy(true)
    try {
      // Avatars render at ≤96px; downscale + re-encode client-side so a large
      // photo fits the server's 256 KiB cap instead of being rejected.
      const prepared = await resizeImageForUpload(file, 512)
      const res = await authApi.uploadAvatar(prepared)
      await setAvatar(res.url)
      toast.success(t('settings:account.avatar.updated'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.avatar.failed'))
    } finally {
      setAvatarBusy(false)
      if (avatarRef.current) avatarRef.current.value = ''
    }
  }
  async function removeAvatar() {
    setAvatarBusy(true)
    try {
      await setAvatar('')
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.avatar.failed'))
    } finally {
      setAvatarBusy(false)
    }
  }

  async function changePassword() {
    if (pw.next.length < 8) {
      toast.error(t('settings:account.passwordMinLength'))
      return
    }
    setIsChangingPassword(true)
    try {
      await authApi.changePassword(pw.current, pw.next)
      toast.success(t('settings:account.passwordUpdated'))
      setPwOpen(false)
      setPw({ current: '', next: '' })
      await logout()
      navigate('/login')
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : 'Failed')
    } finally {
      setIsChangingPassword(false)
    }
  }

  return (
    <div className="mx-auto max-w-[60rem]">
      <header className="mb-8">
        <h1 className="tracking-tight text-3xl text-[var(--color-fg)]">{t('settings:account.title')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">{t('settings:account.subtitle')}</p>
      </header>

      <SettingsSection title={t('settings:account.profile')}>
        <SettingsRow label={t('settings:account.rows.avatar')} description={t('settings:account.rows.avatarBody')}>
          <div className="flex items-center gap-3">
            <Avatar size="xl" tone="clay">
              {avatarUrl ? <AvatarImage src={avatarUrl} alt={name} /> : null}
              <AvatarFallback>{initials(name || email || '?')}</AvatarFallback>
            </Avatar>
            <input
              ref={avatarRef}
              type="file"
              accept="image/png,image/jpeg"
              className="hidden"
              onChange={(e) => void onPickAvatar(e.target.files?.[0])}
            />
            <Button
              variant="secondary"
              size="sm"
              loading={avatarBusy}
              leadingIcon={<Upload size={13} aria-hidden />}
              onClick={() => avatarRef.current?.click()}
            >
              {avatarUrl ? t('settings:account.avatar.change') : t('settings:account.avatar.upload')}
            </Button>
            {avatarUrl ? (
              <Button variant="ghost" size="sm" disabled={avatarBusy} onClick={() => void removeAvatar()}>
                {t('settings:account.avatar.remove')}
              </Button>
            ) : null}
          </div>
        </SettingsRow>
        <SettingsRow label={t('settings:account.rows.name')} description={t('settings:account.rows.nameBody')}>
          <label htmlFor="account-name" className="sr-only">{t('settings:account.rows.name')}</label>
          <Input
            id="account-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            leadingIcon={<User size={14} aria-hidden />}
            className="w-64"
          />
        </SettingsRow>
        <SettingsRow label={t('settings:account.rows.email')} description={t('settings:account.rows.emailLocked')}>
          <label htmlFor="account-email" className="sr-only">{t('settings:account.rows.email')}</label>
          <Input
            id="account-email"
            type="email"
            value={email}
            readOnly
            disabled
            leadingIcon={<Mail size={14} aria-hidden />}
            className="w-64"
          />
        </SettingsRow>
        <div className="px-5 sm:px-6 py-4 flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={() => { setName(user?.name ?? ''); setEmail(user?.email ?? '') }}>
            {t('common:actions.reset')}
          </Button>
          <Button variant="secondary" onClick={() => void save()} loading={saving}>
            {t('common:actions.save')}
          </Button>
        </div>
      </SettingsSection>

      <SettingsSection title={t('settings:account.security')}>
        <SettingsRow
          label={t('settings:account.securityRows.password')}
          description={
            user?.password_changed_at
              ? t('settings:account.securityRows.passwordChangedOn', {
                  date: formatAbsoluteDate(user.password_changed_at * 1000),
                })
              : t('settings:account.securityRows.passwordNeverChanged')
          }
        >
          <Button variant="secondary" onClick={() => setPwOpen(true)}>
            <Lock size={13} aria-hidden /> {t('common:actions.changePassword')}
          </Button>
        </SettingsRow>
        <SettingsRow
          label={t('settings:account.twofa.label')}
          description={twoFactorOn ? t('settings:account.twofa.onBody') : t('settings:account.twofa.offBody')}
        >
          {twoFactorOn ? (
            <Button variant="ghost" onClick={() => { setCode(''); setDisableOpen(true) }}>
              {t('settings:account.twofa.disable')}
            </Button>
          ) : (
            <Button variant="secondary" loading={twoFaBusy} onClick={() => void beginEnable()}>
              <ShieldCheck size={13} aria-hidden /> {t('settings:account.twofa.enable')}
            </Button>
          )}
        </SettingsRow>
      </SettingsSection>

      <IdentitySources />

      <ActiveSessions />

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
            <DialogDescription>{t('settings:account.signOutOnSuccess')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-3">
              <Field label={t('settings:account.currentPassword')} htmlFor="pw-cur">
                <Input
                  id="pw-cur"
                  type="password"
                  value={pw.current}
                  onChange={(e) => setPw({ ...pw, current: e.target.value })}
                />
              </Field>
              <Field label={t('settings:account.newPassword')} htmlFor="pw-new" hint={t('settings:account.newPasswordHint')}>
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
            <Button variant="ghost" onClick={() => setPwOpen(false)} disabled={isChangingPassword}>{t('common:actions.cancel')}</Button>
            <Button onClick={() => void changePassword()} disabled={isChangingPassword} loading={isChangingPassword}>
              {isChangingPassword ? t('common:actions.saving', { defaultValue: 'Saving…' }) : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Enable 2FA — show the secret for the authenticator, then confirm a code */}
      <Dialog open={enableOpen} onOpenChange={(o) => { setEnableOpen(o); if (!o) setSetup(null) }}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('settings:account.twofa.enableTitle')}</DialogTitle>
            <DialogDescription>{t('settings:account.twofa.enableLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <div>
                {setup?.otpauth_url && (
                  <div className="flex justify-center my-4">
                    <img
                      src={`https://api.qrserver.com/v1/create-qr-code/?data=${encodeURIComponent(setup.otpauth_url)}&size=200x200&margin=8`}
                      alt={t('settings:account.twofa.qrAlt', { defaultValue: 'Scan with authenticator app' })}
                      width={200}
                      height={200}
                      className="rounded-[10px] border border-[var(--color-border)]"
                    />
                  </div>
                )}
                <div className="text-[12px] font-medium text-[var(--color-fg-muted)] mb-1.5">
                  {t('settings:account.twofa.secretLabel')}
                </div>
                <div className="flex items-center gap-2">
                  <code className="flex-1 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2 font-mono text-[13px] tracking-[0.15em] break-all">
                    {setup ? setup.secret.replace(/(.{4})/g, '$1 ').trim() : ''}
                  </code>
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={t('common:actions.copy')}
                    onClick={() => setup && void copy(setup.secret)}
                  >
                    <Copy size={14} aria-hidden />
                  </Button>
                </div>
                <p className="mt-1.5 text-[12px] text-[var(--color-fg-subtle)]">{t('settings:account.twofa.secretHint', { defaultValue: "Can't scan? Enter this code manually." })}</p>
              </div>
              <Field label={t('settings:account.twofa.codeLabel')} htmlFor="twofa-enable-code">
                <Input
                  id="twofa-enable-code"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                  placeholder="000000"
                  className="font-mono tracking-[0.3em]"
                />
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEnableOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button loading={twoFaBusy} onClick={() => void confirmEnable()}>{t('settings:account.twofa.enable')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Disable 2FA — confirm with a current code */}
      <Dialog open={disableOpen} onOpenChange={setDisableOpen}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('settings:account.twofa.disableTitle')}</DialogTitle>
            <DialogDescription>{t('settings:account.twofa.disableLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <Field label={t('settings:account.twofa.codeLabel')} htmlFor="twofa-disable-code">
              <Input
                id="twofa-disable-code"
                inputMode="numeric"
                autoComplete="one-time-code"
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                placeholder="000000"
                className="font-mono tracking-[0.3em]"
              />
            </Field>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDisableOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button variant="destructive" loading={twoFaBusy} onClick={() => void confirmDisable()}>
              {t('settings:account.twofa.disable')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmDelete} onOpenChange={(o) => { setConfirmDelete(o); if (!o) setDeletePassword('') }}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('settings:account.dangerRows.dialogTitle')}</DialogTitle>
            <DialogDescription>
              {t('settings:account.dangerRows.dialogBody')}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <Field label={t('settings:account.dangerRows.passwordConfirm', { defaultValue: 'Enter your password to confirm' })} htmlFor="delete-pw">
              <Input
                id="delete-pw"
                type="password"
                value={deletePassword}
                onChange={(e) => setDeletePassword(e.target.value)}
                placeholder="••••••••"
              />
            </Field>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)} disabled={deleting}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              disabled={deleting || deletePassword.length < 1}
              loading={deleting}
              onClick={() => {
                setDeleting(true)
                void authApi.deleteAccount(deletePassword).then(async () => {
                  setConfirmDelete(false)
                  toast.success(t('settings:account.dangerRows.deleted', { defaultValue: 'Account deleted' }))
                  await logout()
                  navigate('/login')
                }).catch((e) => {
                  toast.error(e instanceof ApiError ? e.message : 'Failed to delete account')
                }).finally(() => setDeleting(false))
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
