import { resolveLucideIconName } from '@/lib/lucide-icons'

export type PaymentMethodIconValue =
  | { kind: 'image'; src: string }
  | { kind: 'lucide'; name: string }
  | { kind: 'fallback' }

/** Keep uploaded paths and legacy Lucide values compatible without rendering unsafe URLs. */
export function resolvePaymentMethodIcon(icon?: string): PaymentMethodIconValue {
  const value = (icon ?? '').trim()
  if (!value) return { kind: 'fallback' }

  if (value.startsWith('/api/icons/')) {
    return { kind: 'image', src: value }
  }

  try {
    const url = new URL(value)
    if (url.protocol === 'http:' || url.protocol === 'https:') {
      return { kind: 'image', src: value }
    }
  } catch {
    // Symbolic values are resolved below.
  }

  const name = resolveLucideIconName(value)
  return name ? { kind: 'lucide', name } : { kind: 'fallback' }
}
