import type { ApiSharedMessage } from '@/api/types'
import { ModelIcon } from '@/components/chat/model-icon'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { Zap } from 'lucide-react'

interface SharedMessageIdentityProps {
  message: ApiSharedMessage
  userFallback: string
  assistantFallback: string
  fastLabel: string
}

/** Display-only identity header for one message in a frozen public snapshot. */
export function SharedMessageIdentity({
  message,
  userFallback,
  assistantFallback,
  fastLabel,
}: SharedMessageIdentityProps) {
  if (message.role === 'user') {
    const name = message.author_name?.trim() || userFallback
    return (
      <div className="mb-1.5 flex flex-row-reverse items-center gap-2">
        <Avatar size="sm" tone="ink">
          {message.author_avatar ? <AvatarImage src={message.author_avatar} alt={name} /> : null}
          <AvatarFallback>{initials(name)}</AvatarFallback>
        </Avatar>
        <span className="text-[13px] font-medium text-[var(--color-fg-muted)]">{name}</span>
      </div>
    )
  }

  const label = message.fast ? fastLabel : message.model_label?.trim() || assistantFallback
  return (
    <div className="mb-2 flex items-center gap-2">
      {message.fast ? (
        <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
          <Zap size={13} aria-hidden />
        </span>
      ) : (
        <ModelIcon icon={message.model_icon} size={20} />
      )}
      <span className="text-[15px] font-medium text-[var(--color-fg)]">{label}</span>
    </div>
  )
}
