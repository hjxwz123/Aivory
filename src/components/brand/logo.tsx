import { cn } from '@/lib/utils'
import wordmarkUrl from '@/assets/brand/aivory-wordmark.svg'

interface LogoMarkProps {
  size?: number
  className?: string
  tone?: 'system' | 'lockup'
}

/** Symmetric compound path shared by every rendering of the brand mark. */
export const AIVORY_MARK_PATH =
  'M16 4.5c-1.05 0-2.01.61-2.45 1.56L4.78 24.5c-.7 1.5.4 3.2 2.05 3.2h18.34c1.65 0 2.75-1.7 2.05-3.2L18.45 6.06A2.7 2.7 0 0 0 16 4.5Zm0 4.55 7.15 15.15H8.85L16 9.05Z'

/**
 * Aivory mark — abstract triangular vessel suggesting attention focusing
 * to a point. Rendered as SVG with a scalable gradient fill.
 */
export function LogoMark({ size = 24, className, tone = 'system' }: LogoMarkProps) {
  const gradientId = tone === 'lockup' ? 'aivory-mark-lockup' : 'aivory-mark'

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label="Aivory"
      focusable="false"
      shapeRendering="geometricPrecision"
      className={cn('block shrink-0 select-none', className)}
    >
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor={tone === 'lockup' ? '#5a7b72' : 'var(--color-accent)'} />
          {tone === 'lockup' && <stop offset="0.5" stopColor="#466b78" />}
          <stop offset="1" stopColor={tone === 'lockup' ? '#2a4548' : 'var(--color-secondary)'} />
        </linearGradient>
      </defs>
      <path
        d={AIVORY_MARK_PATH}
        fill={`url(#${gradientId})`}
        fillRule="evenodd"
        clipRule="evenodd"
      />
      <circle cx="16" cy="20.9" r="1.4" fill={tone === 'lockup' ? '#4f716c' : 'var(--color-accent)'} />
    </svg>
  )
}

interface LogoProps {
  size?: 'sm' | 'md' | 'lg'
  className?: string
}

export function Logo({ size = 'md', className }: LogoProps) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-2 font-serif tracking-tight text-[var(--color-fg)]',
        size === 'sm' && 'text-[15px]',
        size === 'md' && 'text-lg',
        size === 'lg' && 'text-2xl',
        className,
      )}
    >
      <LogoMark size={size === 'sm' ? 18 : size === 'md' ? 22 : 30} />
      <span className="leading-none">Aivory</span>
    </span>
  )
}

export function TracedLogo({ size = 'md', className }: LogoProps) {
  return (
    <span className={cn('inline-flex items-center gap-2', className)}>
      <LogoMark size={size === 'sm' ? 18 : size === 'md' ? 22 : 30} tone="lockup" />
      <img
        src={wordmarkUrl}
        alt=""
        aria-hidden="true"
        draggable={false}
        className={cn(
          'block w-auto shrink-0 select-none dark:brightness-[2] dark:saturate-50',
          size === 'sm' && 'h-4',
          size === 'md' && 'h-[19px]',
          size === 'lg' && 'h-[26px]',
        )}
      />
    </span>
  )
}
