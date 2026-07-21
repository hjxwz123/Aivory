import { FileWarning } from 'lucide-react'
import {
  type KeyboardEvent as ReactKeyboardEvent,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from 'react'

import { Skeleton } from '@/components/ui/skeleton'
import { MAX_OFFICE_PREVIEW_BYTES } from '@/lib/file-preview-kind'
import {
  OoxmlArchiveLimitError,
  validateOoxmlArchive,
  type OoxmlArchiveLimits,
} from '@/lib/ooxml-archive'

const MAX_RENDERED_ROWS = 250
const MAX_RENDERED_COLUMNS = 50
const SPREADSHEET_ARCHIVE_LIMITS: Readonly<OoxmlArchiveLimits> = Object.freeze({
  maxEntries: 2_000,
  maxEntryUncompressedBytes: 8 * 1024 * 1024,
  maxTotalUncompressedBytes: 32 * 1024 * 1024,
  maxCompressionRatio: 250,
})

type PreviewStatus = 'loading' | 'ready' | 'error' | 'too-large'
type CellValue = string | number | boolean | Date | null

interface WorkbookSheet {
  sheet: string
  data: CellValue[][]
  totalRows: number
  totalColumns: number
}

type WorkerResponse =
  | { ok: true; sheets: WorkbookSheet[] }
  | { ok: false; message: string }

export interface SpreadsheetTruncationDetails {
  shownRows: number
  totalRows: number
  shownColumns: number
  totalColumns: number
}

export interface SpreadsheetNativePreviewLabels {
  loading: string
  error: string
  tooLarge: string
  emptySheet: string
  sheets: string
  rowHeader: string
  trueValue: string
  falseValue: string
  truncated: (details: SpreadsheetTruncationDetails) => string
}

export interface SpreadsheetNativePreviewProps {
  data: ArrayBuffer
  name: string
  labels: SpreadsheetNativePreviewLabels
  locale?: string
  onError?: (error: Error) => void
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

function columnName(index: number): string {
  let value = index + 1
  let result = ''

  while (value > 0) {
    value -= 1
    result = String.fromCharCode(65 + (value % 26)) + result
    value = Math.floor(value / 26)
  }

  return result
}

function createDateFormatter(locale: string | undefined, includeTime: boolean): Intl.DateTimeFormat {
  try {
    return new Intl.DateTimeFormat(locale, {
      dateStyle: 'medium',
      ...(includeTime ? { timeStyle: 'short' as const } : {}),
    })
  } catch {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: 'medium',
      ...(includeTime ? { timeStyle: 'short' as const } : {}),
    })
  }
}

function cellText(
  value: CellValue | undefined,
  labels: SpreadsheetNativePreviewLabels,
  dateFormatter: Intl.DateTimeFormat,
  dateTimeFormatter: Intl.DateTimeFormat,
): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'boolean') return value ? labels.trueValue : labels.falseValue
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) return ''
    const hasTime = value.getHours() !== 0 || value.getMinutes() !== 0 || value.getSeconds() !== 0
    return (hasTime ? dateTimeFormatter : dateFormatter).format(value)
  }
  return String(value)
}

export function SpreadsheetNativePreview({
  data,
  name,
  labels,
  locale,
  onError,
}: SpreadsheetNativePreviewProps) {
  const [status, setStatus] = useState<PreviewStatus>('loading')
  const [sheets, setSheets] = useState<WorkbookSheet[]>([])
  const [activeIndex, setActiveIndex] = useState(0)
  const onErrorRef = useRef(onError)
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([])
  const id = useId()
  const dateFormatter = useMemo(() => createDateFormatter(locale, false), [locale])
  const dateTimeFormatter = useMemo(() => createDateFormatter(locale, true), [locale])

  onErrorRef.current = onError

  useEffect(() => {
    let disposed = false

    if (data.byteLength > MAX_OFFICE_PREVIEW_BYTES) {
      const error = new Error(
        `Spreadsheet preview exceeds the ${MAX_OFFICE_PREVIEW_BYTES}-byte limit: ${name}`,
      )
      setSheets([])
      setActiveIndex(0)
      setStatus('too-large')
      onErrorRef.current?.(error)
      return () => {
        disposed = true
      }
    }

    try {
      validateOoxmlArchive(data, SPREADSHEET_ARCHIVE_LIMITS)
    } catch (cause) {
      const error = asError(cause)
      setSheets([])
      setActiveIndex(0)
      setStatus(cause instanceof OoxmlArchiveLimitError ? 'too-large' : 'error')
      onErrorRef.current?.(error)
      return () => {
        disposed = true
      }
    }

    setSheets([])
    setActiveIndex(0)
    setStatus('loading')

    let parser: Worker | null = null
    const terminateParser = () => {
      parser?.terminate()
      parser = null
    }

    try {
      parser = new Worker(new URL('./spreadsheet-parser.worker.ts', import.meta.url), { type: 'module' })
      parser.onmessage = (event: MessageEvent<WorkerResponse>) => {
        terminateParser()
        if (disposed) return
        if (event.data.ok) {
          setSheets(event.data.sheets)
          setStatus('ready')
          return
        }
        const error = new Error(event.data.message)
        setSheets([])
        setStatus('error')
        onErrorRef.current?.(error)
      }
      parser.onerror = (event) => {
        event.preventDefault()
        terminateParser()
        if (disposed) return
        const error = new Error(event.message || 'Spreadsheet worker failed')
        setSheets([])
        setStatus('error')
        onErrorRef.current?.(error)
      }

      const input = data.slice(0)
      parser.postMessage({ data: input }, [input])
    } catch (cause) {
      terminateParser()
      const error = asError(cause)
      setSheets([])
      setStatus('error')
      onErrorRef.current?.(error)
    }

    return () => {
      disposed = true
      terminateParser()
    }
  }, [data, name])

  const safeActiveIndex = Math.min(activeIndex, Math.max(0, sheets.length - 1))
  const activeSheet = sheets[safeActiveIndex]
  const dimensions = useMemo(() => {
    if (!activeSheet) return { totalColumns: 0, totalRows: 0 }
    return {
      totalColumns: activeSheet.totalColumns,
      totalRows: activeSheet.totalRows,
    }
  }, [activeSheet])
  const renderedRowCount = Math.min(activeSheet?.data.length ?? 0, MAX_RENDERED_ROWS)
  const renderedColumnCount = Math.min(
    activeSheet?.data.reduce((max, row) => Math.max(max, row.length), 0) ?? 0,
    MAX_RENDERED_COLUMNS,
  )
  const isTruncated =
    dimensions.totalRows > MAX_RENDERED_ROWS || dimensions.totalColumns > MAX_RENDERED_COLUMNS

  const moveToTab = (nextIndex: number) => {
    setActiveIndex(nextIndex)
    tabRefs.current[nextIndex]?.focus()
    tabRefs.current[nextIndex]?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }

  const handleTabKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    if (sheets.length < 2) return

    let nextIndex: number | null = null
    if (event.key === 'ArrowRight' || event.key === 'ArrowDown') {
      nextIndex = (index + 1) % sheets.length
    } else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') {
      nextIndex = (index - 1 + sheets.length) % sheets.length
    } else if (event.key === 'Home') {
      nextIndex = 0
    } else if (event.key === 'End') {
      nextIndex = sheets.length - 1
    }

    if (nextIndex === null) return
    event.preventDefault()
    moveToTab(nextIndex)
  }

  const failureLabel = status === 'too-large' ? labels.tooLarge : labels.error

  if (status === 'loading') {
    return (
      <div
        className="flex h-full min-h-[28rem] flex-col gap-4 overflow-hidden bg-[var(--color-surface)] p-4"
        role="status"
        aria-live="polite"
      >
        <span className="text-[13px] text-[var(--color-fg-muted)]">{labels.loading}</span>
        <Skeleton className="h-9 w-full rounded-[6px]" />
        <div className="grid flex-1 grid-cols-4 gap-px overflow-hidden bg-[var(--color-divider)]">
          {Array.from({ length: 20 }, (_, index) => (
            <Skeleton key={index} className="min-h-9 rounded-none" />
          ))}
        </div>
      </div>
    )
  }

  if (status === 'error' || status === 'too-large') {
    return (
      <div
        className="flex h-full min-h-[28rem] items-center justify-center bg-[var(--color-surface)] p-6"
        role="alert"
      >
        <div className="max-w-md text-center">
          <FileWarning
            size={24}
            strokeWidth={1.6}
            className="mx-auto mb-3 text-[var(--color-danger)]"
            aria-hidden
          />
          <p className="text-sm leading-relaxed text-[var(--color-fg-muted)]">{failureLabel}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-[28rem] min-w-0 flex-col overflow-hidden bg-[var(--color-surface)]">
      {sheets.length > 0 && (
        <div className="shrink-0 overflow-x-auto border-b border-[var(--color-divider)] px-2">
          <div className="inline-flex min-w-full items-end gap-1" role="tablist" aria-label={labels.sheets}>
            {sheets.map((sheet, index) => {
              const isActive = index === safeActiveIndex
              return (
                <button
                  key={`${sheet.sheet}-${index}`}
                  ref={(element) => {
                    tabRefs.current[index] = element
                  }}
                  id={`${id}-tab-${index}`}
                  type="button"
                  role="tab"
                  aria-controls={`${id}-panel`}
                  aria-selected={isActive}
                  tabIndex={isActive ? 0 : -1}
                  title={sheet.sheet}
                  className={[
                    'relative h-8 max-w-56 shrink-0 truncate px-3 text-[13px] font-medium [@media(pointer:coarse)]:h-11',
                    'interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    isActive
                      ? 'text-[var(--color-fg)] after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:bg-[var(--color-accent)]'
                      : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                  ].join(' ')}
                  onClick={() => setActiveIndex(index)}
                  onKeyDown={(event) => handleTabKeyDown(event, index)}
                >
                  {sheet.sheet}
                </button>
              )
            })}
          </div>
        </div>
      )}

      {!activeSheet || renderedRowCount === 0 || renderedColumnCount === 0 ? (
        <div
          id={`${id}-panel`}
          role="tabpanel"
          aria-labelledby={sheets.length > 0 ? `${id}-tab-${safeActiveIndex}` : undefined}
          className="flex min-h-0 flex-1 items-center justify-center p-6 text-sm text-[var(--color-fg-muted)]"
        >
          {labels.emptySheet}
        </div>
      ) : (
        <>
          <div
            id={`${id}-panel`}
            role="tabpanel"
            aria-labelledby={`${id}-tab-${safeActiveIndex}`}
            className="min-h-0 flex-1 overflow-auto overscroll-contain"
          >
            <table
              aria-label={`${name}: ${activeSheet.sheet}`}
              className="table-fixed border-separate border-spacing-0 text-[12.5px] leading-5 text-[var(--color-fg)]"
              style={{
                minWidth: '100%',
                width: `${48 + renderedColumnCount * 160}px`,
              }}
            >
              <colgroup>
                <col style={{ width: '48px' }} />
                {Array.from({ length: renderedColumnCount }, (_, index) => (
                  <col key={index} style={{ width: '160px' }} />
                ))}
              </colgroup>
              <thead className="sticky top-0 z-20 bg-[var(--color-bg-muted)]">
                <tr>
                  <th
                    scope="col"
                    className="sticky left-0 z-30 h-8 border-b border-r border-[var(--color-divider)] bg-[var(--color-bg-muted)] text-center font-medium text-[var(--color-fg-subtle)]"
                  >
                    <span aria-hidden>#</span>
                    <span className="sr-only">{labels.rowHeader}</span>
                  </th>
                  {Array.from({ length: renderedColumnCount }, (_, columnIndex) => (
                    <th
                      key={columnIndex}
                      scope="col"
                      className="h-8 border-b border-r border-[var(--color-divider)] px-2 text-center font-medium text-[var(--color-fg-subtle)] last:border-r-0"
                    >
                      {columnName(columnIndex)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {activeSheet.data.slice(0, renderedRowCount).map((row, rowIndex) => (
                  <tr key={rowIndex}>
                    <th
                      scope="row"
                      className="sticky left-0 z-10 h-8 border-b border-r border-[var(--color-divider)] bg-[var(--color-bg-muted)] px-2 text-right font-normal tabular-nums text-[var(--color-fg-subtle)]"
                    >
                      {rowIndex + 1}
                    </th>
                    {Array.from({ length: renderedColumnCount }, (_, columnIndex) => {
                      const value = row[columnIndex]
                      const text = cellText(value, labels, dateFormatter, dateTimeFormatter)
                      const alignment =
                        typeof value === 'number'
                          ? 'text-right tabular-nums'
                          : typeof value === 'boolean'
                            ? 'text-center'
                            : 'text-left'

                      return (
                        <td
                          key={columnIndex}
                          title={text || undefined}
                          className={`h-8 truncate border-b border-r border-[var(--color-divider)] px-2.5 align-middle last:border-r-0 ${alignment}`}
                        >
                          {text}
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {isTruncated && (
            <p
              className="shrink-0 border-t border-[var(--color-divider)] bg-[var(--color-bg-muted)] px-3 py-2 text-[12px] leading-5 text-[var(--color-fg-muted)]"
              role="status"
            >
              {labels.truncated({
                shownRows: renderedRowCount,
                totalRows: dimensions.totalRows,
                shownColumns: renderedColumnCount,
                totalColumns: dimensions.totalColumns,
              })}
            </p>
          )}
        </>
      )}
    </div>
  )
}
