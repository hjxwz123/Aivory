import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import styles from './reply-pending.module.css'

/** A small, continuous rhythm shared by waiting replies and live reasoning. */
export function ReplyActivityMark({ small = false }: { small?: boolean }) {
  return (
    <span className={cn(styles.activity, small && styles.small)} aria-hidden="true" data-reply-activity>
      <span />
    </span>
  )
}

/** Visual waiting feedback; the description is available to screen readers. */
export function ReplyPending() {
  const { t } = useTranslation('chat')

  return (
    <div
      role="status"
      aria-live="polite"
      aria-atomic="true"
      className={styles.root}
      data-reply-pending
    >
      <ReplyActivityMark />
      <span className="sr-only">{t('replyPending.preparing')}</span>
    </div>
  )
}
