import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, ChevronDown, FolderUp, Loader2, X } from 'lucide-react'
import { ProgressRing } from '@/components/ui/progress-ring'
import { attachmentTileClass, fileIconFor } from '@/lib/file-icon'
import { folderGroupSize, pathWithinFolder } from '@/lib/folder-attachments'
import { cn } from '@/lib/utils'

/** The subset of an attachment the folder chip needs. */
export interface FolderChipMember {
  id: string
  name: string
  size: number
  kind: string
  relPath?: string
  uploading?: boolean
  uploadProgress?: number
  ingest?: 'parsing' | 'embedding' | 'ready' | 'failed'
}

interface FolderChipProps {
  folder: string
  members: FolderChipMember[]
  /** True when the rail is compressing chips (>4 attachments). */
  compact: boolean
  onRemoveMember: (id: string) => void
  onRemoveFolder: (ids: string[]) => void
}

function statusOf(member: FolderChipMember) {
  const busy = member.uploading || member.ingest === 'parsing' || member.ingest === 'embedding'
  return { busy, failed: member.ingest === 'failed' }
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  const rounded = value >= 10 || unit === 0 ? Math.round(value) : Math.round(value * 10) / 10
  return `${rounded} ${units[unit]}`
}

/**
 * One uploaded folder, collapsed to a single chip.
 *
 * A shared project is hundreds of files; rendering one chip each buries the
 * composer and tells the user nothing they did not already know. This node is
 * the unit they picked, and expands to the individual files (each still
 * independently removable) when they need to check what went up.
 *
 * Removal is offered at both levels: per file inside the expanded list, and for
 * the whole folder on the collapsed chip.
 */
export function FolderChip({ folder, members, compact, onRemoveMember, onRemoveFolder }: FolderChipProps) {
  const { t } = useTranslation('chat')
  const [expanded, setExpanded] = useState(false)

  const uploading = members.filter((m) => statusOf(m).busy)
  const failed = members.filter((m) => statusOf(m).failed)
  const allDone = uploading.length === 0
  // Aggregate progress so a 200-file upload reads as one moving number instead
  // of 200 spinners.
  const percent = members.length
    ? Math.round(
        members.reduce((total, member) => {
          const { busy } = statusOf(member)
          const value = busy ? Math.max(0, Math.min(100, member.uploadProgress ?? 0)) : 100
          return total + value
        }, 0) / members.length,
      )
    : 100
  const processing = uploading.some((m) => (m.uploadProgress ?? 0) >= 100)

  const status = failed.length
    ? t('composer.folderStatusFailed', {
        defaultValue: '{{count}} failed',
        count: failed.length,
      })
    : allDone
      ? t('composer.folderStatusReady', {
          defaultValue: '{{count}} files · {{size}}',
          count: members.length,
          size: formatBytes(folderGroupSize(members)),
        })
      : processing
        ? t('composer.processing', { defaultValue: 'Processing…' })
        : t('composer.folderStatusUploading', {
            defaultValue: 'Uploading {{done}}/{{total}}',
            done: members.length - uploading.length,
            total: members.length,
          })

  return (
    <span
      className={cn(
        'group/att relative flex h-14 items-center gap-2.5 rounded-[10px] border bg-[var(--color-surface-raised)] py-2 pl-2.5 pr-8 shadow-[var(--shadow-xs)]',
        compact
          ? 'w-[clamp(10rem,calc((100%_-_1.5rem)/4.5),15rem)] flex-none'
          : 'min-w-0 max-w-[min(28rem,calc(100vw-6rem))] flex-[1_1_15rem]',
        failed.length ? 'border-[var(--color-danger)]/50' : 'border-[var(--color-border)]',
      )}
    >
      <button
        type="button"
        onClick={() => setExpanded((value) => !value)}
        aria-expanded={expanded}
        aria-label={t(expanded ? 'composer.folderCollapse' : 'composer.folderExpand', {
          defaultValue: expanded ? 'Hide the files in {{folder}}' : 'Show the files in {{folder}}',
          folder,
        })}
        className="flex min-w-0 flex-1 items-center gap-2.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <span
          className={cn(
            'grid size-9 shrink-0 place-items-center rounded-[9px]',
            failed.length ? 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]' : attachmentTileClass({ kind: 'other', name: folder },)
          )}
          aria-hidden
        >
          {uploading.length > 0 && !processing ? (
            <ProgressRing value={percent} size={30} strokeWidth={3} showValue label={status} />
          ) : uploading.length > 0 ? (
            <Loader2 size={17} className="animate-spin" />
          ) : failed.length ? (
            <AlertTriangle size={17} />
          ) : (
            <FolderUp size={18} strokeWidth={2} />
          )}
        </span>
        <span className="grid min-w-0 flex-1 gap-0.5">
          <span className="flex min-w-0 items-center gap-1">
            <span className="truncate text-[0.8125rem] font-semibold leading-tight text-[var(--color-fg)]">
              {folder}
            </span>
            <ChevronDown
              size={12}
              aria-hidden
              className={cn(
                'shrink-0 text-[var(--color-fg-faint)] transition-transform duration-[var(--duration-fast)]',
                expanded && 'rotate-180',
              )}
            />
          </span>
          <span
            className={cn(
              'min-w-0 truncate text-[0.75rem] leading-tight',
              failed.length
                ? 'text-[var(--color-danger)]'
                : uploading.length > 0
                  ? 'text-[var(--color-fg-muted)]'
                  : 'text-[var(--color-fg-subtle)]',
            )}
          >
            {status}
          </span>
        </span>
      </button>
      <button
        type="button"
        aria-label={t('composer.folderRemoveAll', { defaultValue: 'Remove {{folder}}', folder })}
        onClick={() => onRemoveFolder(members.map((member) => member.id))}
        className="absolute right-1.5 top-1.5 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-xs)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <X size={13} aria-hidden />
      </button>

      {expanded ? (
        // Sits BELOW the chip and spans the rail, so a long list never makes one
        // chip absurdly tall next to its neighbours.
        <span className="absolute left-0 right-0 top-[calc(100%+0.25rem)] z-[var(--z-raised)] max-h-56 overflow-y-auto overscroll-contain rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface-raised)] p-1 shadow-[var(--shadow-md)] scrollbar-thin">
          <span className="flex items-center justify-between gap-2 px-2 py-1 text-[0.6875rem] font-medium text-[var(--color-fg-subtle)]">
            <span>{t('composer.folderFiles', { defaultValue: '{{count}} files', count: members.length })}</span>
          </span>
          {members.map((member) => {
            const { busy, failed: memberFailed } = statusOf(member)
            const Icon = fileIconFor(member.name, member.kind)
            return (
              <span key={member.id} className="flex items-center gap-2 rounded-[8px] px-2 py-1 hover:bg-[var(--color-bg-muted)]">
                <span className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden>
                  {busy ? <Loader2 size={13} className="animate-spin" /> : <Icon size={13} />}
                </span>
                <span className="grid min-w-0 flex-1">
                  <span className="truncate text-[0.75rem] leading-tight text-[var(--color-fg)]" title={member.relPath}>
                    {pathWithinFolder(member.relPath, folder) || member.name}
                  </span>
                </span>
                {memberFailed ? (
                  <AlertTriangle size={12} className="shrink-0 text-[var(--color-danger)]" aria-hidden />
                ) : null}
                <button
                  type="button"
                  aria-label={t('composer.folderRemoveFile', { defaultValue: 'Remove {{name}}', name: member.name })}
                  onClick={() => onRemoveMember(member.id)}
                  className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-[var(--color-fg-faint)] interactive hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <X size={11} aria-hidden />
                </button>
              </span>
            )
          })}
        </span>
      ) : null}
    </span>
  )
}
