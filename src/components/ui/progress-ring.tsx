import { cn } from '@/lib/utils'

interface ProgressRingProps {
  value: number
  size?: number
  strokeWidth?: number
  showValue?: boolean
  className?: string
  trackClassName?: string
  indicatorClassName?: string
  label?: string
}

export function ProgressRing({
  value,
  size = 32,
  strokeWidth = 3,
  showValue = false,
  className,
  trackClassName,
  indicatorClassName,
  label,
}: ProgressRingProps) {
  const pct = Math.max(0, Math.min(100, Math.round(Number.isFinite(value) ? value : 0)))
  const radius = (size - strokeWidth) / 2
  const circumference = 2 * Math.PI * radius
  const dashOffset = circumference - (pct / 100) * circumference

  return (
    <span
      className={cn('relative inline-grid shrink-0 place-items-center', className)}
      style={{ width: size, height: size }}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct}
      aria-label={label}
    >
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        className="-rotate-90"
        aria-hidden
      >
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          strokeWidth={strokeWidth}
          className={cn('stroke-current opacity-20', trackClassName)}
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          strokeLinecap="round"
          strokeWidth={strokeWidth}
          strokeDasharray={circumference}
          strokeDashoffset={dashOffset}
          className={cn('stroke-current transition-[stroke-dashoffset] duration-150 ease-out', indicatorClassName)}
        />
      </svg>
      {showValue ? (
        <span className="absolute inset-0 grid place-items-center text-[9px] font-semibold leading-none tabular-nums">
          {pct}
        </span>
      ) : null}
    </span>
  )
}
