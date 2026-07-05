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

/**
 * Editorial table-of-contents row. Replaces the prior tinted-card grid:
 * a 3px accent rule on the left, a Fraunces project name, a Geist sub-line,
 * and tabular metadata right-aligned at the baseline. Hover dims the page
 * background tint and lengthens the accent rule. No halos, no chips.
 */
export function ProjectRow({ project, chatCount }: ProjectRowProps) {
  const { t } = useTranslation('projects')
  const accent = accentClasses(project.accent)

  return (
    <Link
      to={`/projects/${project.id}`}
      aria-label={t('card.openAria', { name: project.name })}
      className={cn(
        'group/row relative grid items-baseline',
        'grid-cols-[3px_1fr] gap-x-5 gap-y-2',
        'sm:grid-cols-[3px_1fr_180px] sm:gap-x-7',
        'py-6 sm:py-7 px-4 sm:px-6 -mx-4 sm:-mx-6',
        'rounded-[12px] interactive',
        'hover:bg-[var(--color-bg-muted)]',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
      )}
    >
      {/* Accent rule */}
      <span
        className={cn(
          'self-stretch rounded-full transition-[transform,opacity] duration-[180ms] ease-out',
          'origin-top scale-y-[0.85] opacity-80 group-hover/row:scale-y-100 group-hover/row:opacity-100',
          accent.bar,
        )}
        aria-hidden
      />

      {/* Title block */}
      <div className="min-w-0">
        <div className="flex items-baseline gap-2 flex-wrap">
          <h3 className="text-[22px] sm:text-[26px] leading-[1.15] tracking-tight text-[var(--color-fg)] truncate-2">
            {project.name}
          </h3>
          {project.pinned ? (
            <Pin
              size={11}
              className={cn('shrink-0 translate-y-[-1px]', accent.text)}
              aria-hidden
            />
          ) : null}
        </div>
        {project.description ? (
          <p className="mt-2 text-[13.5px] sm:text-[14px] text-[var(--color-fg-muted)] leading-relaxed max-w-[60ch]">
            {truncate(project.description, 160)}
          </p>
        ) : null}
      </div>

      {/* Metadata column. Mobile: stacks under title in its own row spanning
          both content columns; desktop: right-aligned third column. */}
      <div
        className={cn(
          'col-start-2 sm:col-start-3 flex flex-col gap-1 self-start',
          'text-[11.5px] text-[var(--color-fg-subtle)] tabular-nums',
          'sm:text-right sm:mt-1',
        )}
      >
        <span>
          {t('card.files', { count: project.files.length })}
          <span aria-hidden className="mx-1.5 opacity-50">·</span>
          {t('card.chats', { count: chatCount })}
        </span>
        <time dateTime={new Date(project.updatedAt).toISOString()}>
          {t('card.updated', { when: formatRelativeDate(project.updatedAt) })}
        </time>
      </div>
    </Link>
  )
}
