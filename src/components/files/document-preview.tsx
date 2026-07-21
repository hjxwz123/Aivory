import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, FileQuestion, RefreshCw } from 'lucide-react'
import { DocxNativePreview } from '@/components/files/docx-native-preview'
import { PdfNativePreview } from '@/components/files/pdf-native-preview'
import { PptxNativePreview } from '@/components/files/pptx-native-preview'
import { SpreadsheetNativePreview } from '@/components/files/spreadsheet-native-preview'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { documentPreviewKind, MAX_TEXT_PREVIEW_BYTES } from '@/lib/file-preview-kind'

interface DocumentPreviewProps {
  name: string
  mimeType?: string
  backendKind?: string
  data?: ArrayBuffer
  objectUrl?: string
  loading?: boolean
  error?: string
  onRetry?: () => void
}

function PreviewNotice({
  kind,
  title,
  retry,
}: {
  kind: 'error' | 'unsupported'
  title: string
  retry?: () => void
}) {
  const { t } = useTranslation(['files', 'common'])
  const Icon = kind === 'error' ? AlertTriangle : FileQuestion
  return (
    <div className="flex h-full min-h-[20rem] items-center justify-center p-6" role={kind === 'error' ? 'alert' : undefined}>
      <div className="max-w-sm text-center">
        <span
          className={
            kind === 'error'
              ? 'mx-auto inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-danger-soft)] text-[var(--color-danger)]'
              : 'mx-auto inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]'
          }
        >
          <Icon size={21} aria-hidden />
        </span>
        <p className="mt-4 text-sm leading-relaxed text-[var(--color-fg-muted)]">{title}</p>
        {retry ? (
          <Button
            variant="secondary"
            size="sm"
            className="mt-4"
            leadingIcon={<RefreshCw size={14} aria-hidden />}
            onClick={retry}
          >
            {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

function PreviewLoading({ label }: { label: string }) {
  return (
    <div className="flex h-full min-h-[20rem] flex-col items-center gap-4 overflow-hidden p-4 sm:p-6" role="status">
      <span className="text-[0.8125rem] text-[var(--color-fg-muted)]">{label}</span>
      <Skeleton className="h-full min-h-[24rem] w-full max-w-[52rem] rounded-[4px]" />
    </div>
  )
}

/**
 * Shared, chrome-free preview surface used by Files, chat attachments, and the
 * admin inventory. Office/PDF renderers are internally lazy-loaded, so merely
 * importing this component does not pull their parsers into the route chunk.
 */
export function DocumentPreview({
  name,
  mimeType,
  backendKind,
  data,
  objectUrl,
  loading = false,
  error,
  onRetry,
}: DocumentPreviewProps) {
  const { t, i18n } = useTranslation('files')
  const kind = documentPreviewKind(name, mimeType, backendKind)
  const text = useMemo(() => {
    if (kind !== 'text' || !data || data.byteLength > MAX_TEXT_PREVIEW_BYTES) return null
    return new TextDecoder('utf-8', { fatal: false }).decode(data)
  }, [data, kind])

  if (loading) return <PreviewLoading label={t('preview.loading')} />
  if (error) return <PreviewNotice kind="error" title={error || t('preview.failed')} retry={onRetry} />
  if (!data && kind !== 'unsupported' && !(kind === 'image' && objectUrl)) {
    return <PreviewLoading label={t('preview.loading')} />
  }

  if (kind === 'image' && objectUrl) {
    return (
      <div className="flex h-full min-h-[20rem] items-center justify-center overflow-auto bg-[var(--color-preview-canvas)] p-4 sm:p-6">
        <img
          src={objectUrl}
          alt={name}
          className="block max-h-full max-w-full object-contain"
          draggable={false}
        />
      </div>
    )
  }

  if (kind === 'pdf' && data) {
    return (
      <PdfNativePreview
        data={data}
        name={name}
        className="h-full"
        labels={{
          loading: t('preview.loading'),
          previousPage: t('pdf.previousPage'),
          nextPage: t('pdf.nextPage'),
          page: (page) => t('pdf.page', { page }),
          of: (total) => t('pdf.of', { total }),
          zoomOut: t('pdf.zoomOut'),
          zoomIn: t('pdf.zoomIn'),
          fitWidth: t('pdf.fitWidth'),
          renderFailed: t('pdf.renderFailed'),
        }}
      />
    )
  }

  if (kind === 'docx' && data) {
    return (
      <DocxNativePreview
        data={data}
        name={name}
        labels={{
          loading: t('docx.loading'),
          error: t('docx.failed'),
          tooLarge: t('docx.tooLarge'),
        }}
      />
    )
  }

  if (kind === 'pptx' && data) {
    return (
      <PptxNativePreview
        data={data}
        name={name}
        labels={{
          loading: t('pptx.loading'),
          error: t('pptx.failed'),
          tooLarge: t('pptx.tooLarge'),
          slideError: t('pptx.failed'),
        }}
      />
    )
  }

  if (kind === 'xlsx' && data) {
    return (
      <SpreadsheetNativePreview
        data={data}
        name={name}
        locale={i18n.language}
        labels={{
          loading: t('spreadsheet.loading'),
          error: t('spreadsheet.failed'),
          tooLarge: t('spreadsheet.tooLarge'),
          emptySheet: t('spreadsheet.empty'),
          sheets: t('spreadsheet.sheet'),
          rowHeader: t('spreadsheet.row'),
          trueValue: 'TRUE',
          falseValue: 'FALSE',
          truncated: ({ shownRows, shownColumns }) =>
            t('spreadsheet.truncated', { rows: shownRows, columns: shownColumns }),
        }}
      />
    )
  }

  if (kind === 'text' && data) {
    if (data.byteLength > MAX_TEXT_PREVIEW_BYTES) {
      return <PreviewNotice kind="unsupported" title={t('preview.tooLarge')} />
    }
    return (
      <pre className="h-full min-h-[20rem] overflow-auto whitespace-pre-wrap break-words bg-[var(--color-surface)] p-4 font-mono text-[0.78125rem] leading-relaxed text-[var(--color-fg)] sm:p-6">
        {text}
      </pre>
    )
  }

  return <PreviewNotice kind="unsupported" title={t('preview.unsupported')} />
}
