import { Eye, EyeOff } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Tooltip } from '@/components/ui/tooltip'

export function PrivateModeToggle({ active, onClick }: { active: boolean; onClick: () => void }) {
  const { t } = useTranslation('chat')
  const label = t(active ? 'private.exit' : 'private.enter')
  const Icon = active ? EyeOff : Eye

  return (
    <Tooltip content={label}>
      <button
        type="button"
        aria-label={label}
        aria-pressed={active}
        onClick={onClick}
        className="inline-flex size-11 shrink-0 items-center justify-center rounded-full bg-transparent text-[var(--color-fg-muted)] transition-colors hover:text-[var(--color-fg)] aria-pressed:text-[var(--color-fg)] focus-visible:outline-2 focus-visible:outline-offset-[-6px] focus-visible:outline-[var(--color-fg-muted)]"
      >
        <Icon size={20} strokeWidth={active ? 2 : 1.6} aria-hidden />
      </button>
    </Tooltip>
  )
}
