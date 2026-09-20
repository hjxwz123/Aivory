import { Briefcase } from 'lucide-react'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'

export function WorkspaceIcon({ icon, size = 20 }: { icon?: string; size?: number }) {
  return (
    <Avatar className="shrink-0 rounded-[6px]" style={{ width: size, height: size }}>
      {icon ? <AvatarImage src={icon} alt="" className="object-contain" /> : null}
      <AvatarFallback className="rounded-[6px] bg-transparent text-[var(--color-fg-muted)]">
        <Briefcase size={size} aria-hidden />
      </AvatarFallback>
    </Avatar>
  )
}
