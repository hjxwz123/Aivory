import { SlidersHorizontal, UserRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip } from '@/components/ui/tooltip'
import type { FileTypeFilter } from '@/lib/file-preview-kind'

const FILE_TYPES: FileTypeFilter[] = ['all', 'pdf', 'document', 'presentation', 'spreadsheet', 'image', 'text', 'other']

interface OwnerSearchConfig {
  value: string
  onChange: (value: string) => void
  label: string
  placeholder: string
}

interface FileFiltersPopoverProps {
  fileType: FileTypeFilter
  onFileTypeChange: (value: FileTypeFilter) => void
  origin: string
  onOriginChange: (value: string) => void
  sort: string
  order: 'desc' | 'asc'
  onSortChange: (sort: string, order: 'desc' | 'asc') => void
  activeCount: number
  onReset: () => void
  ownerSearch?: OwnerSearchConfig
}

export function FileFiltersPopover({
  fileType,
  onFileTypeChange,
  origin,
  onOriginChange,
  sort,
  order,
  onSortChange,
  activeCount,
  onReset,
  ownerSearch,
}: FileFiltersPopoverProps) {
  const { t } = useTranslation(['files', 'common'])
  const title = t('files:filters.title')

  return (
    <Popover>
      <Tooltip content={title} side="bottom">
        <PopoverTrigger asChild>
          <Button
            variant={activeCount > 0 ? 'secondary' : 'ghost'}
            size="icon"
            className="relative shrink-0 [@media(pointer:coarse)]:size-11"
            aria-label={activeCount > 0 ? `${title}: ${activeCount}` : title}
          >
            <SlidersHorizontal size={16} aria-hidden />
            {activeCount > 0 ? (
              <span
                className="absolute -right-1 -top-1 inline-flex min-w-4 items-center justify-center rounded-full bg-[var(--color-accent)] px-1 text-[0.625rem] font-semibold leading-4 text-[var(--color-accent-fg)]"
                aria-hidden
              >
                {activeCount}
              </span>
            ) : null}
          </Button>
        </PopoverTrigger>
      </Tooltip>

      <PopoverContent align="end" collisionPadding={12} className="w-[min(19rem,calc(100vw-1.5rem))] p-3">
        <div className="mb-2 flex min-h-7 items-center justify-between gap-3 px-0.5">
          <span className="text-xs font-medium text-[var(--color-fg)]">{title}</span>
          {activeCount > 0 ? (
            <Button variant="ghost" size="xs" className="-mr-1" onClick={onReset}>
              {t('common:actions.clear')}
            </Button>
          ) : null}
        </div>

        <div className="space-y-2">
          {ownerSearch ? (
            <Input
              value={ownerSearch.value}
              onChange={(event) => ownerSearch.onChange(event.target.value)}
              leadingIcon={<UserRound size={14} aria-hidden />}
              placeholder={ownerSearch.placeholder}
              aria-label={ownerSearch.label}
              wrapperClassName="h-9 w-full"
              className="text-sm"
            />
          ) : null}

          <div className="grid grid-cols-2 gap-2">
            <Select value={fileType} onValueChange={(value) => onFileTypeChange(value as FileTypeFilter)}>
              <SelectTrigger
                aria-label={t('files:filters.typeLabel')}
                className="h-9 min-w-0 px-3 [&>span:first-child]:truncate"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FILE_TYPES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`files:types.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            <Select value={origin} onValueChange={onOriginChange}>
              <SelectTrigger
                aria-label={t('files:filters.originLabel')}
                className="h-9 min-w-0 px-3 [&>span:first-child]:truncate"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('files:origin.all')}</SelectItem>
                <SelectItem value="conversation">{t('files:origin.conversation')}</SelectItem>
                <SelectItem value="kb">{t('files:origin.kb')}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <Select
            value={`${sort}-${order}`}
            onValueChange={(value) => {
              const [nextSort, nextOrder] = value.split('-') as [string, 'desc' | 'asc']
              onSortChange(nextSort, nextOrder)
            }}
          >
            <SelectTrigger aria-label={t('files:filters.sortLabel')} className="h-9 px-3">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="created_at-desc">{t('files:sort.newest')}</SelectItem>
              <SelectItem value="created_at-asc">{t('files:sort.oldest')}</SelectItem>
              <SelectItem value="size_bytes-desc">{t('files:sort.largest')}</SelectItem>
              <SelectItem value="size_bytes-asc">{t('files:sort.smallest')}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </PopoverContent>
    </Popover>
  )
}
