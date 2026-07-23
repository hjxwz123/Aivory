import type { LucideProps } from 'lucide-react'
import { resolveLucideIcon } from '@/lib/lucide-icons'

interface LucideGlyphProps extends Omit<LucideProps, 'ref'> {
  name?: string
}

/** Render a Lucide icon name defensively; unknown names render nothing. */
export function LucideGlyph({ name, ...props }: LucideGlyphProps) {
  const Icon = resolveLucideIcon(name)
  if (!Icon) return null
  return <Icon {...props} />
}
