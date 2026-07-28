import { useState } from 'react'
import { CreditCard } from 'lucide-react'

import { LucideGlyph } from '@/components/ui/lucide-icon'
import { resolvePaymentMethodIcon } from '@/lib/payment-method-icons'
import { cn } from '@/lib/utils'

interface PaymentMethodIconProps {
  icon?: string
  size?: number
  className?: string
}

export function PaymentMethodIcon({ icon, size = 16, className }: PaymentMethodIconProps) {
  const resolved = resolvePaymentMethodIcon(icon)
  const [failedSource, setFailedSource] = useState('')

  if (resolved.kind === 'image' && failedSource !== resolved.src) {
    return (
      <img
        src={resolved.src}
        alt=""
        width={size}
        height={size}
        aria-hidden
        loading="lazy"
        decoding="async"
        referrerPolicy="no-referrer"
        className={cn('shrink-0 rounded-[4px] object-contain', className)}
        style={{ width: size, height: size }}
        onError={() => setFailedSource(resolved.src)}
      />
    )
  }

  if (resolved.kind === 'lucide') {
    return <LucideGlyph name={resolved.name} size={size} aria-hidden className={cn('shrink-0', className)} />
  }

  return <CreditCard size={size} aria-hidden className={cn('shrink-0', className)} />
}
