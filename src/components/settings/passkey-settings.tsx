/**
 * PasskeySettings — the account page's "passkeys" section. Mirrors the identity
 * sources section: one bordered list, a 36px icon tile per row, title +
 * secondary line, and a single small action on the right.
 *
 * Registering runs a WebAuthn ceremony (begin → navigator.credentials.create →
 * finish) bound to the deployment's HTTPS origin; passkeys are optional, so the
 * section degrades to an explanatory row when the browser lacks support.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { KeyRound, X } from 'lucide-react'
import { authApi, ApiError } from '@/api'
import type { ApiPasskey } from '@/api/types'
import { PasskeyError, createPasskeyCredential, isPasskeyAvailable } from '@/lib/passkey'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
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
import { useLanguage } from '@/store/language'

function deviceDate(unixSec: number, locale: string): string {
  try {
    return new Intl.DateTimeFormat(locale, { year: 'numeric', month: 'short', day: 'numeric' }).format(
      new Date(unixSec * 1000),
    )
  } catch {
    return ''
  }
}

function PasskeyTile() {
  return (
    <div className="shrink-0 size-9 inline-flex items-center justify-center rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
      <KeyRound size={17} aria-hidden />
    </div>
  )
}

export function PasskeySettings() {
  const { t } = useTranslation(['settings', 'common'])
  const lang = useLanguage((s) => s.lang)
  const supported = isPasskeyAvailable()
  const [passkeys, setPasskeys] = useState<ApiPasskey[]>([])
  const [loading, setLoading] = useState(supported)
  const [addOpen, setAddOpen] = useState(false)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [deleteId, setDeleteId] = useState<string | null>(null)

  useEffect(() => {
    let active = true
    if (!supported) return
    void authApi
      .passkeys()
      .then((rows) => {
        if (active) setPasskeys(rows)
      })
      .catch(() => undefined)
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [supported])

  async function addPasskey() {
    setBusy(true)
    try {
      const options = await authApi.beginPasskeyRegistration(name.trim())
      const response = await createPasskeyCredential(options)
      await authApi.finishPasskeyRegistration(response)
      setPasskeys(await authApi.passkeys())
      setAddOpen(false)
      setName('')
      toast.success(t('settings:account.passkey.added'))
    } catch (e) {
      const code = e instanceof PasskeyError ? e.code : e instanceof ApiError ? e.message : 'passkey_registration_failed'
      // A dismissed biometric prompt is not an error worth a toast.
      if (code !== 'passkey_cancelled') {
        // Codes without a translation (deployment/proxy errors, stale clients)
        // fall back to the generic toast — keep the raw cause in the console so
        // support can tell what actually failed.
        console.error('[passkey] registration failed', { code, error: e })
        toast.error(t(`settings:account.passkey.errors.${code}`, { defaultValue: t('settings:account.passkey.failed') }))
      }
    } finally {
      setBusy(false)
    }
  }

  async function removePasskey() {
    if (!deleteId) return
    setBusy(true)
    try {
      await authApi.deletePasskey(deleteId)
      setPasskeys((current) => current.filter((row) => row.id !== deleteId))
      toast.success(t('settings:account.passkey.deleted'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('settings:account.passkey.failed'))
    } finally {
      setBusy(false)
      setDeleteId(null)
    }
  }

  return (
    <section className="mb-8 last:mb-0">
      <div className="mb-3">
        <h2 className="text-lg font-medium tracking-normal text-[var(--color-fg)]">
          {t('settings:account.passkey.label')}
        </h2>
        <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('settings:account.passkey.body')}</p>
      </div>

      <div className="divide-y divide-[var(--color-divider)] rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)]">
        {loading ? (
          <div className="px-4 py-5 text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
        ) : (
          <>
            {passkeys.map((passkey) => (
              <div key={passkey.id} className="flex items-center gap-3 px-4 py-3">
                <PasskeyTile />
                <div className="min-w-0 flex-1">
                  <span className="text-sm font-medium text-[var(--color-fg)] truncate">
                    {passkey.name || t('settings:account.passkey.unnamed')}
                  </span>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] truncate">
                    {passkey.last_used_at
                      ? t('settings:account.passkey.lastUsed', { date: deviceDate(passkey.last_used_at, lang) })
                      : t('settings:account.passkey.neverUsed')}
                    {' · '}
                    {deviceDate(passkey.created_at, lang)}
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  loading={busy && deleteId === passkey.id}
                  onClick={() => setDeleteId(passkey.id)}
                  leadingIcon={<X size={14} aria-hidden />}
                >
                  {t('settings:account.passkey.delete')}
                </Button>
              </div>
            ))}

            {supported ? (
              <div className="flex items-center gap-3 px-4 py-3">
                <PasskeyTile />
                <div className="min-w-0 flex-1">
                  <span className="text-sm font-medium text-[var(--color-fg)] truncate">
                    {t('settings:account.passkey.add')}
                  </span>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)]">
                    {passkeys.length ? t('settings:account.passkey.addHint') : t('settings:account.passkey.empty')}
                  </div>
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  loading={busy && addOpen}
                  onClick={() => {
                    setName('')
                    setAddOpen(true)
                  }}
                  leadingIcon={<KeyRound size={14} aria-hidden />}
                >
                  {t('settings:account.passkey.add')}
                </Button>
              </div>
            ) : (
              <div className="flex items-center gap-3 px-4 py-3">
                <PasskeyTile />
                <div className="min-w-0 flex-1">
                  <span className="text-sm font-medium text-[var(--color-fg)] truncate">
                    {t('settings:account.passkey.add')}
                  </span>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)]">
                    {t('settings:account.passkey.unsupported')}
                  </div>
                </div>
              </div>
            )}
          </>
        )}
      </div>

      {/* Add — name the device, then the browser runs the WebAuthn ceremony. */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('settings:account.passkey.addTitle')}</DialogTitle>
            <DialogDescription>{t('settings:account.passkey.addLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <Field label={t('settings:account.passkey.nameLabel')} htmlFor="passkey-name">
              <Input
                id="passkey-name"
                value={name}
                maxLength={64}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('settings:account.passkey.namePlaceholder')}
                autoComplete="off"
              />
            </Field>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setAddOpen(false)} disabled={busy}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={busy} onClick={() => void addPasskey()}>
              {t('settings:account.passkey.add')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteId !== null} onOpenChange={(open) => { if (!open) setDeleteId(null) }}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('settings:account.passkey.deleteTitle')}</DialogTitle>
            <DialogDescription>{t('settings:account.passkey.deleteLead')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteId(null)} disabled={busy}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={busy} onClick={() => void removePasskey()}>
              {t('settings:account.passkey.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}