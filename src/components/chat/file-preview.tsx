/**
 * Conversation attachment preview. The dialog is only chrome; all document
 * rendering is shared with the Files workspace, including PDF.js and Office
 * previews. Authenticated bytes are fetched once and exposed through a short-
 * lived object URL for open/download actions.
 */
import { useEffect, useRef, useState } from 'react'
import { Download, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DocumentPreview } from '@/components/files/document-preview'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { Attachment } from '@/types/chat'
import { cn } from '@/lib/utils'

interface PreviewFile {
  name: string
  url?: string
  kind: Attachment['kind']
}

interface FilePreviewProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  file: PreviewFile | null
}

interface LoadedPreview {
  data?: ArrayBuffer
  objectUrl?: string
  mimeType?: string
  loading: boolean
  error?: string
}

export function FilePreview({ open, onOpenChange, file }: FilePreviewProps) {
  const { t } = useTranslation(['chat', 'common', 'files'])
  const [attempt, setAttempt] = useState(0)
  const [preview, setPreview] = useState<LoadedPreview>({ loading: false })
  const objectUrlRef = useRef<string | null>(null)

  useEffect(() => {
    const sourceUrl = file?.url
    if (!open || !sourceUrl) {
      setPreview({ loading: false })
      return
    }

    const controller = new AbortController()
    let disposed = false
    if (objectUrlRef.current) {
      URL.revokeObjectURL(objectUrlRef.current)
      objectUrlRef.current = null
    }
    setPreview({ loading: true })

    void (async () => {
      try {
        const response = await fetch(sourceUrl, {
          credentials: 'include',
          signal: controller.signal,
        })
        if (!response.ok) throw new Error(`preview failed (${response.status})`)
        const blob = await response.blob()
        const data = await blob.arrayBuffer()
        if (disposed) return
        const objectUrl = URL.createObjectURL(blob)
        objectUrlRef.current = objectUrl
        setPreview({ data, objectUrl, mimeType: blob.type, loading: false })
      } catch (error) {
        if (disposed || controller.signal.aborted) return
        setPreview({
          loading: false,
          error: error instanceof Error ? error.message : t('chat:filePreview.failed'),
        })
      }
    })()

    return () => {
      disposed = true
      controller.abort()
      if (objectUrlRef.current) {
        URL.revokeObjectURL(objectUrlRef.current)
        objectUrlRef.current = null
      }
    }
  }, [attempt, file?.url, open, t])

  if (!file) return null
  const actionUrl = preview.objectUrl ?? file.url

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="full" className="h-[min(92dvh,56rem)]">
        <DialogHeader>
          <DialogTitle className="truncate pr-8 text-lg">{file.name}</DialogTitle>
        </DialogHeader>

        <DialogBody className="min-h-0 overflow-hidden border-t border-[var(--color-divider)] px-0 pb-0">
          <DocumentPreview
            name={file.name}
            mimeType={preview.mimeType}
            backendKind={file.kind}
            data={preview.data}
            objectUrl={preview.objectUrl}
            loading={preview.loading}
            error={preview.error}
            onRetry={() => setAttempt((value) => value + 1)}
          />
        </DialogBody>

        <DialogFooter>
          {actionUrl ? (
            <>
              <a
                href={actionUrl}
                target="_blank"
                rel="noreferrer"
                className={cn(
                  'inline-flex h-9 items-center gap-1.5 rounded-[10px] border border-[var(--color-border)] px-3.5 text-sm font-medium text-[var(--color-fg-muted)] interactive',
                  'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <ExternalLink size={14} aria-hidden />
                {t('chat:filePreview.open')}
              </a>
              <a
                href={actionUrl}
                download={file.name}
                className={cn(
                  'inline-flex h-9 items-center gap-1.5 rounded-[10px] bg-[var(--color-fg)] px-3.5 text-sm font-medium text-[var(--color-fg-inverted)] interactive',
                  'hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <Download size={14} aria-hidden />
                {t('chat:filePreview.download')}
              </a>
            </>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
