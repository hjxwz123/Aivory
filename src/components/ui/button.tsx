import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react'
import { Slot } from '@radix-ui/react-slot'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

type Variant = 'primary' | 'secondary' | 'ghost' | 'outline' | 'destructive' | 'link'
type Size = 'xs' | 'sm' | 'md' | 'lg' | 'icon' | 'icon-sm' | 'icon-lg'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
  leadingIcon?: ReactNode
  trailingIcon?: ReactNode
  loading?: boolean
  asChild?: boolean
}

const base = [
  'inline-flex items-center justify-center gap-2',
  'font-medium select-none whitespace-nowrap',
  'rounded-[10px]',
  'interactive',
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--color-bg)]',
  'disabled:opacity-50 disabled:cursor-not-allowed disabled:pointer-events-none',
  'active:translate-y-[0.5px]',
].join(' ')

const variants: Record<Variant, string> = {
  primary:
    'bg-[var(--color-accent)] text-[var(--color-accent-fg)] hover:bg-[var(--color-accent-hover)] shadow-[var(--shadow-xs)]',
  secondary:
    'bg-[var(--color-surface)] text-[var(--color-fg)] border border-[var(--color-border)] hover:bg-[var(--color-bg-muted)]',
  outline:
    'bg-transparent text-[var(--color-fg)] border border-[var(--color-border-strong)] hover:bg-[var(--color-bg-muted)]',
  ghost:
    'bg-transparent text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
  destructive:
    'bg-[var(--color-danger)] text-white hover:opacity-90',
  link:
    'bg-transparent text-[var(--color-accent)] hover:text-[var(--color-accent-hover)] underline-offset-4 hover:underline px-0',
}

const sizes: Record<Size, string> = {
  xs: 'h-7 px-2.5 text-xs',
  sm: 'h-8 px-3 text-sm',
  md: 'h-10 px-4 text-[0.9375rem]',
  lg: 'h-12 px-6 text-base',
  icon: 'h-9 w-9',
  'icon-sm': 'h-7 w-7',
  'icon-lg': 'h-11 w-11',
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'primary', size = 'md', leadingIcon, trailingIcon, loading, asChild, className, children, disabled, type, ...rest },
  ref,
) {
  const { t } = useTranslation('common')
  const Comp = asChild ? Slot : 'button'
  const isLoading = Boolean(loading)
  // For native <button>, default type="button" so we never accidentally submit a form.
  // When using asChild (e.g. wrapping <a>), don't spread `disabled` (anchors don't have it).
  const nativeProps = asChild
    ? { 'aria-disabled': disabled || isLoading ? true : undefined }
    : { type: type ?? 'button', disabled: disabled || isLoading }
  return (
    <Comp
      ref={ref}
      className={cn(base, variants[variant], sizes[size], className)}
      aria-busy={isLoading || undefined}
      {...nativeProps}
      {...rest}
    >
      {isLoading ? (
        <span
          className="inline-block size-3.5 rounded-full border-2 border-current border-r-transparent animate-[spin_700ms_linear_infinite]"
          aria-hidden
        />
      ) : leadingIcon ? (
        <span className="-ml-0.5 inline-flex">{leadingIcon}</span>
      ) : null}
      {children}
      {!isLoading && trailingIcon ? <span className="-mr-0.5 inline-flex">{trailingIcon}</span> : null}
      {isLoading && <span className="sr-only">{t('aria.loading')}</span>}
    </Comp>
  )
})
