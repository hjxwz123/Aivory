import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { authApi } from '@/api'
import type { ApiCreditAdjustmentNotification } from '@/api/types'
import { Dialog, DialogBody, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { acquireStartupDialog } from '@/lib/startup-dialog-queue'
import { useAuth } from '@/store/auth'

const DIALOG_EXIT_MS = 180

export function CreditAdjustmentNotice() {
  const { t, i18n } = useTranslation('common')
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const onboarded = Boolean((user?.settings as Record<string, unknown> | undefined)?.onboarded)
  const eligible = status === 'authenticated' && Boolean(user) && user?.has_password !== false && onboarded

  const [notice, setNotice] = useState<ApiCreditAdjustmentNotification | null>(null)
  const [open, setOpen] = useState(false)
  const [claimSequence, setClaimSequence] = useState(0)
  const releaseRef = useRef<(() => void) | null>(null)

  useEffect(() => {
    if (!eligible || notice || open) return
    let cancelled = false

    void acquireStartupDialog().then(async (release) => {
      if (cancelled) {
        release()
        return
      }
      try {
        const response = await authApi.claimCreditAdjustmentNotification()
        if (cancelled || !response.notification) {
          release()
          return
        }
        releaseRef.current = release
        setNotice(response.notification)
        setOpen(true)
      } catch {
        release()
      }
    })

    return () => {
      cancelled = true
    }
  }, [claimSequence, eligible, notice, open, user?.id])

  useEffect(() => () => {
    releaseRef.current?.()
    releaseRef.current = null
  }, [])

  useEffect(() => {
    if (eligible) return
    setOpen(false)
    setNotice(null)
    releaseRef.current?.()
    releaseRef.current = null
  }, [eligible])

  function close() {
    if (!open) return
    setOpen(false)
    const release = releaseRef.current
    releaseRef.current = null
    window.setTimeout(() => {
      release?.()
      setNotice(null)
      setClaimSequence((value) => value + 1)
    }, DIALOG_EXIT_MS)
  }

  if (!notice) return null

  const amount = new Intl.NumberFormat(i18n.resolvedLanguage, { maximumFractionDigits: 6 }).format(notice.amount)

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) close() }}>
      <DialogContent size="sm" aria-describedby={undefined}>
        <DialogTitle className="sr-only">{t('creditAdjustment.title')}</DialogTitle>
        <DialogBody className="px-6 pb-7 pt-7 pr-14">
          <div className="space-y-3 text-center">
            <p className="text-[15px] font-medium leading-6 text-[var(--color-fg)]">
              {t(`creditAdjustment.${notice.direction}`, { amount })}
            </p>
            <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-fg-muted)]">
              {notice.reason}
            </p>
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
