import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { IconUploader } from '@/components/admin/icon-uploader'
import { Textarea } from '@/components/ui/textarea'
import { WorkspaceIcon } from './workspace-icon'

export interface WorkspaceProfileDraft { icon_url: string; description: string }

export function WorkspaceProfileFields({ value, onChange, disabled, upload, onUploadingChange }: {
  value: WorkspaceProfileDraft
  onChange: (value: WorkspaceProfileDraft) => void
  disabled?: boolean
  upload?: (file: File) => Promise<{ url: string }>
  onUploadingChange?: (uploading: boolean) => void
}) {
  const { t } = useTranslation('settings')
  const id = useId()
  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <label htmlFor={`${id}-icon`} className="text-sm font-medium">{t('work.icon')}</label>
        <IconUploader id={`${id}-icon`} value={value.icon_url} disabled={disabled} upload={upload}
          onUploadingChange={onUploadingChange} placeholder="https://…"
          preview={<WorkspaceIcon icon={value.icon_url} size={20} />}
          onChange={(icon_url) => onChange({ ...value, icon_url })} />
      </div>
      <div className="space-y-2">
        <label htmlFor={`${id}-description`} className="text-sm font-medium">{t('work.description')}</label>
        <Textarea id={`${id}-description`} rows={4} maxLength={4000} disabled={disabled}
          value={value.description} placeholder={t('work.descriptionPlaceholder')}
          onChange={(event) => onChange({ ...value, description: event.target.value })} />
      </div>
    </div>
  )
}
