import { ChevronDown, Sparkles } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useModels } from '@/store/models'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

interface ModelPickerProps {
  value: string
  onChange: (id: string) => void
  className?: string
}

/**
 * ModelPicker — driven by the backend model registry. Falls back to the local
 * mock model bundle when the registry hasn't loaded yet, so the picker is
 * always populated.
 */
export function ModelPicker({ value, onChange, className }: ModelPickerProps) {
  const models = useModels((s) => s.models)
  const current = models.find((m) => m.id === value) ?? models[0]
  const { t } = useTranslation('chat')

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className={cn(
          'inline-flex items-center gap-1.5 h-8 px-2.5 rounded-[8px]',
          'text-[13px] font-medium text-[var(--color-fg-muted)]',
          'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          'max-w-[180px]',
          className,
        )}
        aria-label={t('modelPicker.label', { name: current?.label ?? 'Model' })}
      >
        <Sparkles size={13} className="text-[var(--color-secondary)] shrink-0" aria-hidden />
        <span className="truncate">{current?.label ?? 'Model'}</span>
        <ChevronDown size={13} aria-hidden />
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="end" className="w-[320px]">
        <DropdownMenuLabel>{t('modelPicker.section')}</DropdownMenuLabel>
        {models.map((m) => {
          const active = m.id === value
          return (
            <DropdownMenuItem
              key={m.id}
              onSelect={() => onChange(m.id)}
              className="items-start py-2.5"
            >
              <div className="flex flex-col gap-1 flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-[var(--color-fg)] truncate">{m.label}</span>
                  {active && (
                    <span
                      className="ml-auto size-1.5 rounded-full bg-[var(--color-accent)] shrink-0"
                      aria-label={t('modelPicker.current')}
                    />
                  )}
                </div>
                {m.description ? (
                  <span className="text-[11.5px] text-[var(--color-fg-muted)] leading-snug">
                    {m.description}
                  </span>
                ) : null}
              </div>
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
