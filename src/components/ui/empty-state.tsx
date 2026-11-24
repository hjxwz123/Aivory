import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { SceneOrnament } from '@/components/chat/home-scene'

export interface EmptyStateProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  icon?: ReactNode
  illustrated?: boolean
  title: ReactNode
  titleClassName?: string
  description?: ReactNode
  action?: ReactNode
  /** Match the surrounding page, content panel, or compact table/list. */
  size?: 'page' | 'panel' | 'table'
  tone?: 'neutral' | 'danger'
}

export function EmptyState({ icon, illustrated, title, titleClassName, description, action, size = 'page', tone = 'neutral', className, ...rest }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'mx-auto flex flex-col items-center justify-center px-6 text-center',
        size === 'page' && 'max-w-md py-16',
        size === 'panel' && 'w-full max-w-none rounded-[8px] bg-[var(--color-surface)] py-10',
        size === 'table' && 'w-full max-w-none py-8',
        className,
      )}
      role="status"
      {...rest}
    >
      {icon ? (
        <div className={cn(
          'inline-flex shrink-0 items-center justify-center rounded-full [&_svg]:shrink-0',
          illustrated && 'relative',
          size === 'page' && 'mb-5 size-12 [&_svg]:size-5',
          size === 'panel' && 'mb-4 size-10 [&_svg]:size-5',
          size === 'table' && 'mb-3 size-8 [&_svg]:size-4',
          tone === 'danger' ? 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]' : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]',
        )}>
          {icon}
          {illustrated && <SceneOrnament surface="empty" />}
        </div>
      ) : null}
      {/* No font-serif: in-app headings follow the Appearance → Font setting
          (globals.css base rule); serif stays opt-in for brand pages only. */}
      <h3 className={cn(
        'max-w-md font-semibold tracking-tight text-[var(--color-fg)]',
        size === 'page' ? 'text-lg' : size === 'panel' ? 'text-[15px]' : 'text-sm',
        titleClassName,
      )}>
        {title}
      </h3>
      {description ? (
        <p className="mt-2 max-w-md text-sm text-[var(--color-fg-muted)] leading-relaxed text-pretty">
          {description}
        </p>
      ) : null}
      {action ? (
        <div className={cn(
          'max-w-full [&_button]:min-h-8 [&_button]:text-sm [@media(pointer:coarse)]:[&_button]:min-h-[var(--tap-min)]',
          size === 'page' ? 'mt-6' : 'mt-4',
        )}>
          {action}
        </div>
      ) : null}
    </div>
  )
}
