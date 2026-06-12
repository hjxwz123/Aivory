import { cn } from '@/lib/utils'

interface LogoMarkProps {
  size?: number
  className?: string
}

/**
 * Aurelia mark — abstract triangular vessel suggesting attention focusing
 * to a point. Rendered as SVG, single accent fill, scales to any size.
 */
export function LogoMark({ size = 24, className }: LogoMarkProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label="Aurelia"
      className={cn('inline-block', className)}
    >
      <defs>
        <linearGradient id="aurelia-mark" x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="var(--color-accent)" />
          <stop offset="1" stopColor="var(--color-secondary)" />
        </linearGradient>
      </defs>
      <path
        d="M16 4.5c-1.05 0-2.02.6-2.47 1.55L4.34 24.6c-.74 1.55.4 3.4 2.13 3.4h19.06c1.74 0 2.87-1.85 2.13-3.4L18.47 6.05A2.72 2.72 0 0 0 16 4.5Zm0 4.3 9.8 20.6H6.2L16 8.8Z"
        fill="url(#aurelia-mark)"
      />
      <circle cx="16" cy="22.2" r="1.4" fill="var(--color-accent)" />
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
      <span className="leading-none">Aurelia</span>
    </span>
  )
}
