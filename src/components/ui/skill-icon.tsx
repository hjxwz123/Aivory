import { Sparkles, type LucideProps } from 'lucide-react'
import { LucideGlyph } from '@/components/ui/lucide-icon'
import { resolveLucideIconName } from '@/lib/lucide-icons'

interface SkillIconProps extends Omit<LucideProps, 'name' | 'ref'> {
  name?: string
}

/** Render an administrator-selected skill icon with a stable default. */
export function SkillIcon({ name, ...props }: SkillIconProps) {
  const resolved = resolveLucideIconName(name)
  return resolved ? <LucideGlyph name={resolved} {...props} /> : <Sparkles {...props} />
}
