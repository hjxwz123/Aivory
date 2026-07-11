/**
 * IconUploader — text field + image upload affordance used by the admin
 * model editor (both the create dialog and the per-model settings page).
 *
 * The value stored in `model.icon` is one of:
 *   - empty string             → falls back to the default Sparkles icon in
 *                                the model picker
 *   - emoji character(s)       → rendered as plain text
 *   - URL (starts with http or /)
 *                              → rendered as <img>
 *
 * Behaviour:
 *   - Typing in the text input is the manual path — user can paste an emoji,
 *     a remote URL like https://example.com/foo.png, or the URL we returned
 *     from a prior upload.
 *   - Clicking the upload button opens a file picker for png/jpg/jpeg. The
 *     file is validated client-side (type + size 256 KiB) before being POSTed
 *     to /api/admin/icons — the server still re-validates everything. On
 *     success we paste the returned URL into the field.
 *   - The preview chip on the left re-uses <ModelIcon> so what the admin
 *     sees here matches what users see in the model picker.
 */
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Upload, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { ModelIcon } from '@/components/chat/model-icon'
import { envNum } from '@/lib/env-config'

const ACCEPT = 'image/png,image/jpeg,image/svg+xml,.svg'
const MAX_BYTES = envNum('VITE_AURELIA_MAX_BYTES', 256 * 1024) // mirrors backend admin_uploads.go maxIconBytes

interface IconUploaderProps {
  id?: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
}

export function IconUploader({ id, value, onChange, placeholder }: IconUploaderProps) {
  const { t } = useTranslation(['admin', 'common'])
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement | null>(null)

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0]
    e.target.value = '' // allow re-picking the same file
    if (!f) return
    const okType =
      ['image/png', 'image/jpeg', 'image/svg+xml'].includes(f.type) || /\.svg$/i.test(f.name)
    if (!okType) {
      toast.error(t('admin:icon.errBadType'))
      return
    }
    if (f.size > MAX_BYTES) {
      toast.error(t('admin:icon.errTooLarge'))
      return
    }
    setUploading(true)
    try {
      const { url } = await adminApi.uploadIcon(f)
      onChange(url)
      toast.success(t('admin:icon.uploaded'))
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t('admin:common.failed'))
    } finally {
      setUploading(false)
    }
  }

  return (
    <div className="flex items-center gap-2">
      <div className="shrink-0 size-9 inline-flex items-center justify-center rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]">
        <ModelIcon icon={value} size={18} />
      </div>
      <div className="flex-1 min-w-0">
        <Input
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder ?? '🌟 / https://… / —'}
        />
      </div>
      {value ? (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label={t('admin:icon.clear')}
          onClick={() => onChange('')}
        >
          <X size={14} aria-hidden />
        </Button>
      ) : null}
      <Button
        type="button"
        variant="secondary"
        size="sm"
        leadingIcon={<Upload size={13} aria-hidden />}
        loading={uploading}
        onClick={() => fileRef.current?.click()}
      >
        {t('admin:icon.upload')}
      </Button>
      <input
        ref={fileRef}
        type="file"
        accept={ACCEPT}
        className="hidden"
        onChange={(e) => void onPick(e)}
      />
    </div>
  )
}
