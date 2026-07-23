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
 *   - The preview chip uses <ModelIcon> by default. Domain-specific editors can
 *     provide their own preview so symbolic values match their user-facing UI.
 */
import { useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Upload, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'
import { ModelIcon } from '@/components/chat/model-icon'
import { envNum } from '@/lib/env-config'

const ACCEPT = 'image/png,image/jpeg,image/svg+xml,.svg'
const MAX_BYTES = envNum('VITE_AIVORY_MAX_BYTES', 256 * 1024) // mirrors backend admin_uploads.go maxIconBytes

interface IconUploaderProps {
  id?: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
  /** Lets domain-specific editors preview symbolic values with their own renderer. */
  preview?: ReactNode
}

export function IconUploader({ id, value, onChange, placeholder, preview }: IconUploaderProps) {
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
    <div className="flex min-w-0 items-center gap-2">
      <div className="shrink-0 size-9 inline-flex items-center justify-center rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]">
        {preview ?? <ModelIcon icon={value} size={18} />}
      </div>
      <Input
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder ?? '🌟 / https://… / —'}
        className="min-w-0"
        wrapperClassName="min-w-0 flex-1"
        trailingSlot={
          value ? (
            <button
              type="button"
              aria-label={t('admin:icon.clear')}
              title={t('admin:icon.clear')}
              className="-mr-1 inline-flex size-7 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              onClick={() => onChange('')}
            >
              <X size={14} aria-hidden />
            </button>
          ) : undefined
        }
      />
      <Button
        type="button"
        variant="secondary"
        size="icon"
        leadingIcon={<Upload size={13} aria-hidden />}
        loading={uploading}
        aria-label={t('admin:icon.upload')}
        title={t('admin:icon.upload')}
        className="shrink-0"
        onClick={() => fileRef.current?.click()}
      >
        <span className="sr-only">{t('admin:icon.upload')}</span>
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
