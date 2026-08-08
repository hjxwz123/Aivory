import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ImageOff, Loader2, RefreshCw, Trash2 } from 'lucide-react'
import { issueFeedbackApi } from '@/api'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'

const MAX_DESCRIPTION_LENGTH = 2000
const TARGET_SCREENSHOT_BYTES = 2_800_000

type CaptureState = 'idle' | 'capturing' | 'ready' | 'failed'

interface IssueFeedbackDialogProps {
  open: boolean
  messageId: string
  onOpenChange: (open: boolean) => void
}

function canvasToBlob(canvas: HTMLCanvasElement, quality: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => (blob ? resolve(blob) : reject(new Error('could not encode screenshot'))),
      'image/jpeg',
      quality,
    )
  })
}

async function compressScreenshot(canvas: HTMLCanvasElement): Promise<Blob> {
  let working = canvas
  for (let resize = 0; resize < 4; resize += 1) {
    for (const quality of [0.82, 0.7, 0.56]) {
      const blob = await canvasToBlob(working, quality)
      if (blob.size <= TARGET_SCREENSHOT_BYTES) return blob
    }
    const next = document.createElement('canvas')
    next.width = Math.max(1, Math.floor(working.width * 0.78))
    next.height = Math.max(1, Math.floor(working.height * 0.78))
    const context = next.getContext('2d')
    if (!context) throw new Error('could not resize screenshot')
    context.drawImage(working, 0, 0, next.width, next.height)
    working = next
  }
  throw new Error('screenshot is too large')
}

function canvasCompatibleColor(color: string): string {
  const canvas = document.createElement('canvas')
  canvas.width = 1
  canvas.height = 1
  const context = canvas.getContext('2d', { willReadFrequently: true })
  if (!context) throw new Error('could not resolve page background')
  context.clearRect(0, 0, 1, 1)
  context.fillStyle = color
  context.fillRect(0, 0, 1, 1)
  const [red, green, blue, alpha] = context.getImageData(0, 0, 1, 1).data
  return `rgba(${red}, ${green}, ${blue}, ${alpha / 255})`
}

async function captureCurrentPage(): Promise<Blob> {
  const { default: html2canvas } = await import('html2canvas')
  await document.fonts?.ready
  const width = Math.max(1, window.innerWidth)
  const height = Math.max(1, window.innerHeight)
  const backgroundColor = canvasCompatibleColor(window.getComputedStyle(document.body).backgroundColor)
  const canvas = await html2canvas(document.body, {
    allowTaint: false,
    backgroundColor,
    // The app theme uses CSS Color 4 values such as oklch(). html2canvas's
    // computed renderer cannot parse them, while the browser-native SVG path can.
    foreignObjectRendering: true,
    height,
    ignoreElements: (element) =>
      element.hasAttribute('data-feedback-capture-ignore') ||
      Boolean(element.closest('[data-feedback-capture-ignore]')) ||
      element.getAttribute('role') === 'tooltip' ||
      Boolean(element.closest('[role="tooltip"]')),
    logging: false,
    onclone: (clonedDocument) => {
      // html2canvas still parses these two colors before selecting its renderer.
      clonedDocument.documentElement.style.backgroundColor = backgroundColor
      clonedDocument.body.style.backgroundColor = backgroundColor
    },
    scale: Math.min(window.devicePixelRatio || 1, 1.5),
    scrollX: window.scrollX,
    scrollY: window.scrollY,
    useCORS: true,
    width,
    windowHeight: height,
    windowWidth: width,
    x: window.scrollX,
    y: window.scrollY,
  })
  return compressScreenshot(canvas)
}

export function IssueFeedbackDialog({ open, messageId, onOpenChange }: IssueFeedbackDialogProps) {
  const { t } = useTranslation(['chat', 'common'])
  const textareaId = useId()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const captureRequestRef = useRef(0)
  const screenshotUrlRef = useRef('')
  const [description, setDescription] = useState('')
  const [descriptionError, setDescriptionError] = useState('')
  const [screenshot, setScreenshot] = useState<Blob | null>(null)
  const [screenshotUrl, setScreenshotUrl] = useState('')
  const [captureState, setCaptureState] = useState<CaptureState>('idle')
  const [submitting, setSubmitting] = useState(false)

  const releaseScreenshot = useCallback(() => {
    if (screenshotUrlRef.current) {
      URL.revokeObjectURL(screenshotUrlRef.current)
      screenshotUrlRef.current = ''
    }
    setScreenshot(null)
    setScreenshotUrl('')
  }, [])

  const beginCapture = useCallback(async () => {
    const request = ++captureRequestRef.current
    releaseScreenshot()
    setCaptureState('capturing')
    try {
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const blob = await captureCurrentPage()
      if (request !== captureRequestRef.current) return
      const url = URL.createObjectURL(blob)
      screenshotUrlRef.current = url
      setScreenshot(blob)
      setScreenshotUrl(url)
      setCaptureState('ready')
    } catch {
      if (request === captureRequestRef.current) setCaptureState('failed')
    }
  }, [releaseScreenshot])

  useEffect(() => {
    if (!open || !messageId) return
    setDescription('')
    setDescriptionError('')
    setSubmitting(false)
    void beginCapture()
    return () => {
      captureRequestRef.current += 1
      releaseScreenshot()
    }
  }, [beginCapture, messageId, open, releaseScreenshot])

  useEffect(() => () => releaseScreenshot(), [releaseScreenshot])

  function removeScreenshot() {
    captureRequestRef.current += 1
    releaseScreenshot()
    setCaptureState('idle')
  }

  async function submit() {
    const normalized = description.trim()
    if (!normalized) {
      setDescriptionError(t('chat:issueFeedback.descriptionRequired'))
      textareaRef.current?.focus()
      return
    }
    setDescriptionError('')
    setSubmitting(true)
    try {
      await issueFeedbackApi.submit({
        messageId,
        description: normalized,
        pagePath: `${window.location.pathname}${window.location.search}${window.location.hash}`,
        viewportWidth: window.innerWidth,
        viewportHeight: window.innerHeight,
        screenshot: screenshot ?? undefined,
      })
      toast.success(t('chat:issueFeedback.submitted'))
      onOpenChange(false)
    } catch {
      toast.error(t('chat:issueFeedback.submitFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!submitting) onOpenChange(next)
      }}
    >
      <DialogContent size="lg" closeDisabled={submitting} captureIgnore>
        <DialogHeader>
          <DialogTitle>{t('chat:issueFeedback.title')}</DialogTitle>
          <DialogDescription>{t('chat:issueFeedback.subtitle')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-5">
          <section aria-labelledby={`${textareaId}-screenshot`}>
            <div className="mb-2 flex items-center justify-between gap-3">
              <h3 id={`${textareaId}-screenshot`} className="text-sm font-medium text-[var(--color-fg)]">
                {t('chat:issueFeedback.screenshot')}
              </h3>
              {captureState === 'ready' ? (
                <Tooltip content={t('chat:issueFeedback.removeScreenshot')}>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t('chat:issueFeedback.removeScreenshot')}
                    onClick={removeScreenshot}
                  >
                    <Trash2 size={15} aria-hidden />
                  </Button>
                </Tooltip>
              ) : null}
            </div>
            <div className="flex min-h-40 items-center justify-center overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)]">
              {captureState === 'capturing' ? (
                <div className="flex items-center gap-2 text-sm text-[var(--color-fg-muted)]" role="status">
                  <Loader2 size={16} className="animate-spin" aria-hidden />
                  {t('chat:issueFeedback.capturing')}
                </div>
              ) : captureState === 'ready' && screenshotUrl ? (
                <img
                  src={screenshotUrl}
                  alt={t('chat:issueFeedback.screenshotAlt')}
                  className="max-h-64 w-full object-contain"
                />
              ) : captureState === 'failed' ? (
                <div className="flex flex-col items-center px-5 py-6 text-center">
                  <ImageOff size={22} className="text-[var(--color-fg-subtle)]" aria-hidden />
                  <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
                    {t('chat:issueFeedback.captureFailed')}
                  </p>
                  <Button
                    variant="secondary"
                    size="sm"
                    className="mt-3"
                    leadingIcon={<RefreshCw size={14} aria-hidden />}
                    onClick={() => void beginCapture()}
                  >
                    {t('chat:issueFeedback.retryCapture')}
                  </Button>
                </div>
              ) : (
                <div className="flex flex-col items-center px-5 py-6 text-center">
                  <ImageOff size={22} className="text-[var(--color-fg-subtle)]" aria-hidden />
                  <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
                    {t('chat:issueFeedback.screenshotRemoved')}
                  </p>
                </div>
              )}
            </div>
          </section>

          <div>
            <div className="mb-2 flex items-baseline justify-between gap-3">
              <label htmlFor={textareaId} className="text-sm font-medium text-[var(--color-fg)]">
                {t('chat:issueFeedback.description')}
                <span className="ml-1 text-[var(--color-danger)]" aria-hidden>
                  *
                </span>
              </label>
              <span className="text-[11px] tabular-nums text-[var(--color-fg-subtle)]">
                {description.length}/{MAX_DESCRIPTION_LENGTH}
              </span>
            </div>
            <Textarea
              ref={textareaRef}
              id={textareaId}
              value={description}
              maxLength={MAX_DESCRIPTION_LENGTH}
              rows={5}
              required
              aria-required="true"
              invalid={Boolean(descriptionError)}
              aria-describedby={descriptionError ? `${textareaId}-error` : undefined}
              placeholder={t('chat:issueFeedback.descriptionPlaceholder')}
              disabled={submitting}
              onChange={(event) => {
                setDescription(event.target.value)
                if (descriptionError && event.target.value.trim()) setDescriptionError('')
              }}
            />
            {descriptionError ? (
              <p id={`${textareaId}-error`} role="alert" className="mt-1.5 text-[12px] text-[var(--color-danger)]">
                {descriptionError}
              </p>
            ) : null}
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={submitting} onClick={() => onOpenChange(false)}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            loading={submitting}
            disabled={captureState === 'capturing'}
            onClick={() => void submit()}
          >
            {t('chat:issueFeedback.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
