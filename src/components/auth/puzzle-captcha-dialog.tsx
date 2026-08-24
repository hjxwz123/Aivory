import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { authApi } from '@/api'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import {
  PuzzleCaptcha,
  type PuzzleCaptchaPurpose,
  type PuzzleData,
  type PuzzleSolution,
  type PuzzleStatus,
} from './puzzle-captcha'

interface PuzzleCaptchaDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  purpose: PuzzleCaptchaPurpose
  /** Fired with the single-use pass token once the puzzle is solved + verified. */
  onSolved: (token: string) => void
}

/**
 * PuzzleCaptchaDialog — the modal security check (§ registration captcha). A fresh
 * puzzle loads each time it opens; on release the solution is verified server-side
 * for immediate green/red feedback. A correct drag yields a single-use pass token
 * (handed back via onSolved); a wrong one shakes red and re-rolls the puzzle.
 */
export function PuzzleCaptchaDialog({ open, onOpenChange, purpose, onSolved }: PuzzleCaptchaDialogProps) {
  const { t } = useTranslation('auth')
  const [data, setData] = useState<PuzzleData | null>(null)
  const [loading, setLoading] = useState(false)
  const [status, setStatus] = useState<PuzzleStatus>('idle')
  const verifyingRef = useRef(false)

  const load = useCallback(async () => {
    setLoading(true)
    setStatus('idle')
    try {
      setData(await authApi.captcha(purpose))
    } catch {
      setData(null)
    } finally {
      setLoading(false)
    }
  }, [purpose])

  // Fresh puzzle each time the dialog opens; clear on close.
  useEffect(() => {
    if (open) void load()
    else {
      setData(null)
      setStatus('idle')
    }
  }, [load, open])

  async function onRelease(solution: PuzzleSolution | null) {
    if (solution == null || !data || verifyingRef.current) return
    verifyingRef.current = true
    setStatus('verifying')
    try {
      const res = await authApi.captchaVerify(data.id, solution)
      if (res.ok && res.token) {
        setStatus('success')
        const token = res.token
        window.setTimeout(() => {
          onSolved(token)
          onOpenChange(false)
        }, 550)
      } else {
        setStatus('error')
        window.setTimeout(() => void load(), 700) // re-roll a fresh puzzle
      }
    } catch {
      setStatus('error')
      window.setTimeout(() => void load(), 700)
    } finally {
      verifyingRef.current = false
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="sm">
        <DialogHeader className="pb-2">
          <DialogTitle>
            {t('register.captchaTitle')}
          </DialogTitle>
        </DialogHeader>
        <div className="px-6 pb-6">
          <PuzzleCaptcha
            data={data}
            loading={loading}
            status={status}
            onChange={(f) => void onRelease(f)}
            onRefresh={() => void load()}
          />
        </div>
      </DialogContent>
    </Dialog>
  )
}
