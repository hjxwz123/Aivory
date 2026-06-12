import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

interface KbdProps extends HTMLAttributes<HTMLElement> {
  size?: 'sm' | 'md'
}

export function Kbd({ size = 'sm', className, children, ...rest }: KbdProps) {
  return (
    <kbd
      className={cn(
        'inline-flex items-center justify-center align-middle',
        'rounded-[5px] border border-[var(--color-kbd-border)] bg-[var(--color-kbd-bg)]',
        'font-mono font-medium text-[var(--color-fg-muted)] tracking-tight',
        'shadow-[inset_0_-1px_0_0_var(--color-border)]',
        size === 'sm' && 'h-[18px] min-w-[18px] px-1 text-[10px]',
        size === 'md' && 'h-6 min-w-[24px] px-1.5 text-[11px]',
        className,
      )}
      {...rest}
    >
      {children}
    </kbd>
  )
}

interface KeyboardShortcutProps {
  combo: string[]
  className?: string
  size?: 'sm' | 'md'
}

export function KeyboardShortcut({ combo, size = 'sm', className }: KeyboardShortcutProps) {
  return (
    <span className={cn('inline-flex items-center gap-1', className)}>
      {combo.map((k, i) => (
        <Kbd key={`${k}-${i}`} size={size}>
          {k}
        </Kbd>
      ))}
    </span>
  )
}
