import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Pin } from 'lucide-react'
import type { Project } from '@/types/project'
import { accentClasses } from '@/lib/project-helpers'
import { cn, formatRelativeDate, truncate } from '@/lib/utils'

interface ProjectRowProps {
  project: Project
  chatCount: number
}

/** A compact project index row with a stable 64px desktop rhythm. */
export function ProjectRow({ project, chatCount }: ProjectRowProps) {
  const { t } = useTranslation('projects')
  const accent = accentClasses(project.accent)
  const marker = project.emoji?.trim() || project.name.trim().charAt(0).toUpperCase() || '\u00b7'

  return (
    <Link
      to={`/projects/${project.id}`}
      aria-label={t('card.openAria', { name: project.name })}
      className={cn(
        'group/row relative grid min-h-16 items-center',
        'grid-cols-[2rem_minmax(0,1fr)] gap-x-3',
        'sm:grid-cols-[2rem_minmax(0,1fr)_auto]',
        'px-2.5 py-2 -mx-2.5 sm:px-3 sm:-mx-3',
        'rounded-[8px] interactive',
        'hover:bg-[var(--color-bg-muted)]',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
      )}
    >
      {/* The marker carries the project accent without adding card chrome. */}
      <span
        className={cn(
          'row-span-2 sm:row-span-1 inline-flex size-8 shrink-0 items-center justify-center',
          'self-start sm:self-center rounded-[8px] text-[14px] font-medium leading-none',
          'transition-colors duration-150',
          accent.tint,
          accent.text,
        )}
        aria-hidden
      >
        {marker}
      </span>

      {/* Title block */}
      <div className="min-w-0 self-center">
        <div className="flex min-w-0 items-center gap-1.5">
          <h3 className="truncate text-[14.5px] font-medium leading-[18px] tracking-normal text-[var(--color-fg)]">
            {project.name}
          </h3>
          {project.pinned ? (
            <Pin
              size={12}
              className={cn('shrink-0', accent.text)}
              aria-hidden
            />
          ) : null}
        </div>
        {project.description ? (
          <p className="truncate text-[12.5px] leading-4 text-[var(--color-fg-muted)]">
            {truncate(project.description, 120)}
          </p>
        ) : null}
      </div>

      {/* Metadata wraps only when a narrow viewport cannot hold both groups. */}
      <div
        className={cn(
          'col-start-2 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0',
          'text-[11.5px] leading-4 text-[var(--color-fg-subtle)] tabular-nums',
          'sm:col-start-3 sm:row-start-1 sm:flex-nowrap sm:justify-end sm:pl-4 sm:text-right',
        )}
      >
        <span className="whitespace-nowrap">
          {t('card.files', { count: project.files.length })}
          <span aria-hidden className="mx-1.5 opacity-50">·</span>
          {t('card.chats', { count: chatCount })}
        </span>
        <time className="whitespace-nowrap" dateTime={new Date(project.updatedAt).toISOString()}>
          {t('card.updated', { when: formatRelativeDate(project.updatedAt) })}
        </time>
      </div>
    </Link>
  )
}
