import { Wrench } from 'lucide-react'
import { ModelIcon } from '@/components/chat/model-icon'
import { LucideGlyph } from '@/components/ui/lucide-icon'
import { resolveLucideIconName } from '@/lib/lucide-icons'
import { cn } from '@/lib/utils'

interface OfficialToolIconProps {
  icon?: string
  name?: string
  size?: number
  className?: string
}

function semanticIcon(value: string) {
  switch (value.trim().toLowerCase().replace(/[\s_-]+/g, '')) {
    case 'search':
    case 'web':
    case 'websearch':
      return 'Search'
    case 'terminal':
    case 'code':
    case 'codeinterpreter':
      return 'SquareTerminal'
    case 'image':
    case 'imagegeneration':
      return 'Image'
    case 'tool':
    case 'tools':
    case 'wrench':
      return 'Wrench'
    default:
      return null
  }
}

/** Render built-in symbolic names as Lucide icons and preserve custom emoji/URLs. */
export function OfficialToolIcon({ icon, name, size = 16, className }: OfficialToolIconProps) {
  const configured = icon?.trim() ?? ''
  const configuredLucide = resolveLucideIconName(configured)
  // Picker output is canonical PascalCase and must render exactly as selected.
  // Lowercase legacy aliases keep their historical semantic mapping.
  const selectedLucide = configuredLucide === configured ? configuredLucide : null
  const iconName = selectedLucide
    ?? semanticIcon(configured)
    ?? configuredLucide
    ?? (!configured ? semanticIcon(name ?? '') ?? resolveLucideIconName(name) : null)
  if (iconName) {
    return <LucideGlyph name={iconName} size={size} aria-hidden className={cn('shrink-0', className)} />
  }
  if (configured) return <ModelIcon icon={configured} size={size} className={className} />
  return <Wrench size={size} aria-hidden className={cn('shrink-0', className)} />
}
