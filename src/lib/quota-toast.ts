/**
 * Shared "storage quota exceeded" toast (§ user files page): every upload
 * entry point shows the same message with a one-click jump to /files, so
 * users always know WHERE to free up space.
 */
import i18n from '@/i18n'
import { toast } from '@/hooks/use-toast'

export function toastStorageQuotaFull(navigate: (to: string) => void) {
  toast.custom({
    title: i18n.t('chat:composer.storageQuotaFull', {
      defaultValue: 'Storage is full — free up space in Files and try again.',
    }),
    variant: 'danger',
    duration: 8000,
    action: {
      label: i18n.t('chat:composer.storageQuotaAction', { defaultValue: 'Clean up space' }),
      onClick: () => navigate('/files'),
    },
  })
}
