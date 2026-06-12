import * as AvatarPrimitive from '@radix-ui/react-avatar'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { cn } from '@/lib/utils'

type Size = 'xs' | 'sm' | 'md' | 'lg' | 'xl'

const sizeClass: Record<Size, string> = {
  xs: 'size-5 text-[10px]',
  sm: 'size-6 text-[11px]',
  md: 'size-8 text-xs',
  lg: 'size-10 text-sm',
  xl: 'size-14 text-base',
}

export const Avatar = forwardRef<
  ElementRef<typeof AvatarPrimitive.Root>,
  ComponentPropsWithoutRef<typeof AvatarPrimitive.Root> & { size?: Size; tone?: 'clay' | 'sage' | 'ink' }
>(function Avatar({ className, size = 'md', tone = 'clay', ...rest }, ref) {
  return (
    <AvatarPrimitive.Root
      ref={ref}
      className={cn(
        'relative inline-flex items-center justify-center rounded-full overflow-hidden shrink-0',
        'font-medium select-none',
        tone === 'clay' && 'bg-[var(--color-accent-soft)] text-[var(--color-accent)]',
        tone === 'sage' && 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]',
        tone === 'ink' && 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]',
        sizeClass[size],
        className,
      )}
      {...rest}
    />
  )
})

export const AvatarImage = forwardRef<
  ElementRef<typeof AvatarPrimitive.Image>,
  ComponentPropsWithoutRef<typeof AvatarPrimitive.Image>
>(function AvatarImage({ className, ...rest }, ref) {
  return <AvatarPrimitive.Image ref={ref} className={cn('h-full w-full object-cover', className)} {...rest} />
})

export const AvatarFallback = forwardRef<
  ElementRef<typeof AvatarPrimitive.Fallback>,
  ComponentPropsWithoutRef<typeof AvatarPrimitive.Fallback>
>(function AvatarFallback({ className, ...rest }, ref) {
  return <AvatarPrimitive.Fallback ref={ref} className={cn('flex h-full w-full items-center justify-center', className)} {...rest} />
})
