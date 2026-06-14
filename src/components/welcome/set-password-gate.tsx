import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ShieldCheck } from 'lucide-react'
import { authApi, ApiError } from '@/api'
import { useAuth } from '@/store/auth'
import { toast } from '@/hooks/use-toast'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'

/**
 * SetPasswordGate — a mandatory, non-dismissable dialog shown to accounts that
 * have no password (created via a third-party / OAuth login). It blocks the app
 * until the user chooses a password, so that password-gated actions (change
 * password, delete account, direct sign-in) actually work for them. The wizard
 * (WelcomeCard) is held back until this is cleared, so the two never stack.
 */
export function SetPasswordGate() {
  const { t } = useTranslation(['welcome', 'common'])
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const setUser = useAuth((s) => s.setUser)

  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [saving, setSaving] = useState(false)

  const open = status === 'authenticated' && user?.has_password === false

  async function submit() {
    if (next.length < 8) {
      toast.error(t('welcome:setPassword.tooShort'))
      return
    }
    if (next !== confirm) {
      toast.error(t('welcome:setPassword.mismatch'))
      return
    }
    setSaving(true)
    try {
      await authApi.setPassword(next)
      if (user) setUser({ ...user, has_password: true })
      toast.success(t('welcome:setPassword.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('welcome:setPassword.failed'))
    } finally {
      setSaving(false)
    }
  }

  if (!open) return null

  return (
    // Mandatory: ignore every dismissal path (overlay, Esc, close button) so the
    // user can only leave by setting a password.
    <Dialog open onOpenChange={() => {}}>
      <DialogContent
        size="md"
        showClose={false}
        onEscapeKeyDown={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
        onPointerDownOutside={(e) => e.preventDefault()}
        className="p-0 overflow-hidden"
      >
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
          className="flex flex-col"
        >
          <div className="px-6 sm:px-8 pt-8 pb-2">
            <span className="inline-flex items-center justify-center size-11 rounded-[12px] bg-[var(--color-bg-muted)] border border-[var(--color-border)] text-[var(--color-secondary)]">
              <ShieldCheck size={20} aria-hidden />
            </span>
            <DialogTitle className="mt-5 font-serif text-2xl tracking-tight text-[var(--color-fg)]">
              {t('welcome:setPassword.title')}
            </DialogTitle>
            <DialogDescription className="mt-2 text-sm leading-relaxed text-[var(--color-fg-muted)]">
              {t('welcome:setPassword.subtitle')}
            </DialogDescription>
          </div>

          <div className="px-6 sm:px-8 pt-5 flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="set-pw-new">{t('welcome:setPassword.newLabel')}</Label>
              <Input
                id="set-pw-new"
                type="password"
                autoComplete="new-password"
                autoFocus
                value={next}
                onChange={(e) => setNext(e.target.value)}
              />
              <p className="text-[12px] text-[var(--color-fg-subtle)]">{t('welcome:setPassword.hint')}</p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="set-pw-confirm">{t('welcome:setPassword.confirmLabel')}</Label>
              <Input
                id="set-pw-confirm"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
              />
            </div>
          </div>

          <div className="mt-7 border-t border-[var(--color-divider)] px-6 sm:px-8 py-4">
            <Button
              type="submit"
              className="w-full"
              loading={saving}
              disabled={next.length < 8 || confirm.length < 8}
            >
              {t('welcome:setPassword.submit')}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
