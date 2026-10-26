import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Filter, RotateCcw, Search, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip } from '@/components/ui/tooltip'
import { useMobileLayout } from '@/hooks/use-mobile-layout'
import { Dialog, DialogBody, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

interface AdminListToolbarProps {
  search: string
  onSearchChange: (value: string) => void
  placeholder: string
  filters?: ReactNode
  activeFilterCount?: number
  onResetFilters?: () => void
  actions?: ReactNode
}

export function AdminListToolbar({ search, onSearchChange, placeholder, filters, activeFilterCount = 0, onResetFilters, actions }: AdminListToolbarProps) {
  const { t } = useTranslation('admin')
  const mobile = useMobileLayout()
  const [filtersOpen, setFiltersOpen] = useState(false)
  const searchInput = (wrapperClassName: string) => <Input
    type="search"
    value={search}
    onChange={(event) => onSearchChange(event.target.value)}
    placeholder={placeholder}
    aria-label={placeholder}
    leadingIcon={<Search size={15} aria-hidden />}
    wrapperClassName={wrapperClassName}
    className="min-w-0 text-[13px] [&::-webkit-search-cancel-button]:appearance-none"
    trailingSlot={search ? (
      <Tooltip content={t('listToolbar.clearSearch')}>
        <Button size="icon-sm" variant="ghost" aria-label={t('listToolbar.clearSearch')} onClick={() => onSearchChange('')}>
          <X size={14} aria-hidden />
        </Button>
      </Tooltip>
    ) : null}
  />
  if (mobile) return <>
    <div data-admin-list-toolbar role="search" aria-label={placeholder} className="mt-5 grid min-w-0 gap-2">
      {searchInput('h-11 w-full min-w-0 rounded-[8px] px-3')}
      <div className="flex min-w-0 items-center gap-2">
        {filters ? <Button variant="secondary" size="sm" className="h-11 shrink-0" aria-haspopup="dialog" aria-expanded={filtersOpen} onClick={() => setFiltersOpen(true)}>
          <Filter size={15} aria-hidden />{t('listToolbar.filter')}
          {activeFilterCount > 0 ? <span className="ml-1 inline-flex min-w-5 items-center justify-center rounded-full bg-[var(--color-accent)] px-1.5 text-xs text-white">{activeFilterCount}</span> : null}
        </Button> : null}
        {actions ? <div data-admin-list-actions className="ml-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2 [&_button]:min-h-11 [&_button]:max-w-full [&_button]:rounded-[8px] [&_button[data-button-size=sm]]:px-3 [&_button[data-button-size=sm]]:text-[13px]">{actions}</div> : null}
      </div>
    </div>
    {filters ? <Dialog open={filtersOpen} onOpenChange={setFiltersOpen}>
      <DialogContent mobilePresentation="sheet" size="md" aria-describedby={undefined}>
        <DialogHeader><DialogTitle>{t('listToolbar.filter')}</DialogTitle></DialogHeader>
        <DialogBody className="grid content-start gap-4 py-3">
          {filters}
        </DialogBody>
        <DialogFooter>
          {onResetFilters ? <Button className="mr-auto" variant="ghost" disabled={!activeFilterCount} onClick={onResetFilters}>{t('listToolbar.resetFilters')}</Button> : null}
          <Button variant="secondary" onClick={() => setFiltersOpen(false)}>{t('common:done')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog> : null}
  </>
  return (
    <div data-admin-list-toolbar role="search" aria-label={placeholder} className="mt-5 max-w-full overflow-x-auto pb-1">
      <div className="flex w-max min-w-full items-center gap-2">
        {searchInput('h-9 w-60 min-w-60 max-w-md flex-1 rounded-[8px] px-3 max-sm:h-11')}
        {filters ? (
          <div data-admin-list-filters className="flex shrink-0 items-center gap-2">
            {filters}
            {onResetFilters ? (
              <Tooltip content={t('listToolbar.resetFilters')}>
                <Button variant="ghost" size="icon" className="shrink-0 rounded-[8px] max-sm:size-11" aria-label={t('listToolbar.resetFilters')} disabled={!activeFilterCount} onClick={onResetFilters}>
                  <RotateCcw size={15} aria-hidden />
                </Button>
              </Tooltip>
            ) : null}
          </div>
        ) : null}
        {actions ? <div data-admin-list-actions className="ml-auto flex shrink-0 items-center gap-2 pl-2 [&_button]:h-9 [&_button]:shrink-0 [&_button]:rounded-[8px] [&_button[data-button-size=sm]]:text-[13px] max-sm:[&_button]:h-11">{actions}</div> : null}
      </div>
    </div>
  )
}

export function AdminListFilter({ label, value, onValueChange, options }: {
  label: string
  value: string
  onValueChange: (value: string) => void
  options: { value: string; label: string }[]
}) {
  return (
    <Select value={value} onValueChange={onValueChange}>
      <SelectTrigger aria-label={label} title={`${label}: ${options.find((option) => option.value === value)?.label ?? value}`} className="h-9 w-32 shrink-0 rounded-[8px] text-[13px] max-sm:h-11 [&>span]:min-w-0 [&>span]:truncate [&>span]:text-left [&>svg]:shrink-0"><SelectValue /></SelectTrigger>
      <SelectContent>{options.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}</SelectContent>
    </Select>
  )
}
