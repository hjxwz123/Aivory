import { useTranslation } from 'react-i18next'
import { AlertTriangle, Loader2, X } from 'lucide-react'
import type { Attachment } from '@/types/chat'
import { ProgressRing } from '@/components/ui/progress-ring'
import { DOCUMENT_PARSER_NOT_CONFIGURED } from '@/lib/document-errors'
import { attachmentKindLabel, attachmentTileClass, fileIconFor } from '@/lib/file-icon'
import { cn } from '@/lib/utils'

/**
 * The subset of a pending attachment the chip renders.
 *
 * Declared structurally rather than imported from the composer so this file can
 * be read (and the chip reasoned about) without the composer's store wiring.
 * `kind` keeps the real union so the file-icon helpers accept it.
 */
export interface AttachmentChipAttachment {
  id: string
  name: string
  size: number
  kind: Attachment['kind']
  previewUrl?: string
  uploading?: boolean
  uploadProgress?: number
  ingest?: 'parsing' | 'embedding' | 'ready' | 'failed'
  ingestErrorCode?: string
  /** Conversation scope + document id, needed to retry a failed ingest. */
  uploadScopeId?: string
  documentId?: string
}

interface AttachmentChipProps {
  attachment: AttachmentChipAttachment
  /** >4 attachments: chips stop shrinking and the rail scrolls instead. */
  manyAttachments: boolean
  onRemove: (id: string) => void
  onRetryIngest: (attachment: AttachmentChipAttachment) => void
}

/**
 * One loose (non-folder) attachment: an image thumbnail or a labelled file chip.
 *
 * Extracted verbatim from the composer's chip rail when folder grouping was
 * added, so folder members and single files share one visual language: the
 * folder node renders its own header, while its expanded rows list plain files.
 */
export function AttachmentChip({
  attachment: a,
  manyAttachments,
  onRemove,
  onRetryIngest,
}: AttachmentChipProps) {
  const { t } = useTranslation('chat')
  const busy = a.uploading || a.ingest === 'parsing' || a.ingest === 'embedding'
  const failed = a.ingest === 'failed'
  const uploadPercent = Math.max(0, Math.min(100, Math.round(a.uploadProgress ?? 0)))
  // Browser progress hits 100% when the bytes are handed to the socket, but the
  // request isn't done until the server has received + written the file (and any
  // reverse proxy has finished buffering it). Show a neutral "processing" state
  // so a parked 100% doesn't read as frozen.
  const serverProcessing = a.uploading && uploadPercent >= 100
  const status = serverProcessing
    ? t('composer.processing', { defaultValue: 'Processing…' })
    : a.uploading
      ? t('composer.uploadingPercent', { defaultValue: 'Uploading {{percent}}%', percent: uploadPercent })
      : a.ingest === 'embedding'
        ? t('composer.indexing')
        : a.ingest === 'parsing'
          ? t('composer.parsing')
          : attachmentKindLabel(a)

  if (a.kind === 'image' && a.previewUrl) {
    return (
      <span className="group/att relative inline-block shrink-0">
        <img
          src={a.previewUrl}
          alt={a.name}
          className="size-14 rounded-[10px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] object-cover"
        />
        {busy ? (
          <span className="absolute inset-0 grid place-items-center rounded-[10px] bg-[var(--color-overlay)]">
            {a.uploading && !serverProcessing ? (
              <ProgressRing
                value={uploadPercent}
                size={34}
                strokeWidth={3}
                showValue
                label={status}
                className="text-[var(--color-fg-inverted)]"
              />
            ) : (
              <Loader2 size={13} className="animate-spin text-[var(--color-fg-inverted)]" aria-hidden />
            )}
          </span>
        ) : null}
        <button
          type="button"
          aria-label={t('composer.removeAttachment', { defaultValue: 'Remove {{name}}', name: a.name })}
          onClick={() => onRemove(a.id)}
          className="absolute -right-1.5 -top-1.5 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-sm)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <X size={13} aria-hidden />
        </button>
      </span>
    )
  }

  const Icon = fileIconFor(a.name, a.kind)
  return (
    <span
      className={cn(
        'group/att relative flex h-14 items-center gap-2.5 rounded-[10px] border bg-[var(--color-surface-raised)] py-2 pl-2.5 pr-8 shadow-[var(--shadow-xs)]',
        manyAttachments
          ? // >4 chips: stop shrinking. Fixed width targets ~4.5 chips per row
            // (half chip visible = "there's more" affordance), floored so names
            // stay readable on narrow rails.
            'w-[clamp(10rem,calc((100%_-_1.5rem)/4.5),15rem)] flex-none'
          : 'min-w-0 max-w-[min(28rem,calc(100vw-6rem))] flex-[1_1_15rem]',
        failed ? 'border-[var(--color-danger)]/50' : 'border-[var(--color-border)]',
      )}
    >
      <span
        className={cn(
          'grid size-9 shrink-0 place-items-center rounded-[9px]',
          failed ? 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]' : attachmentTileClass(a),
        )}
        aria-hidden
      >
        {busy ? (
          a.uploading && !serverProcessing ? (
            <ProgressRing value={uploadPercent} size={30} strokeWidth={3} showValue label={status} />
          ) : (
            <Loader2 size={17} className="animate-spin" />
          )
        ) : failed ? (
          <AlertTriangle size={17} />
        ) : (
          <Icon size={18} strokeWidth={2} />
        )}
      </span>
      <span className="grid min-w-0 flex-1 gap-0.5 text-left">
        <span className="truncate text-[0.8125rem] font-semibold leading-tight text-[var(--color-fg)]">
          {a.name}
        </span>
        <span
          className={cn(
            'min-w-0 text-[0.75rem] leading-tight',
            !failed && 'truncate',
            failed
              ? 'text-[var(--color-danger)]'
              : busy
                ? 'text-[var(--color-fg-muted)]'
                : 'text-[var(--color-fg-subtle)]',
          )}
        >
          {failed ? (
            <span className="flex min-w-0 items-center gap-1">
              <span className="truncate">
                {a.ingestErrorCode === DOCUMENT_PARSER_NOT_CONFIGURED
                  ? t('composer.parserNotConfigured', { defaultValue: 'Document parsing isn\'t configured. Ask your admin to enable it, then' })
                  : t('composer.ingestFailedAction', { defaultValue: 'Parsing failed. Remove it or' })}
              </span>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onRetryIngest(a)
                }}
                className="shrink-0 font-semibold underline underline-offset-2 hover:text-[var(--color-danger)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                {t('composer.retry', { defaultValue: 'Retry' })}
              </button>
            </span>
          ) : (
            status
          )}
        </span>
      </span>
      <button
        type="button"
        aria-label={t('composer.removeAttachment', { defaultValue: 'Remove {{name}}', name: a.name })}
        onClick={() => onRemove(a.id)}
        className="absolute right-1.5 top-1.5 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-xs)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <X size={13} aria-hidden />
      </button>
    </span>
  )
}
