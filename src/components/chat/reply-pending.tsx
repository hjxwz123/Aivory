import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useSettings } from '@/store/settings'
import { ReplyWaitAnimation } from './reply-wait-animation'
import styles from './reply-pending.module.css'

/** A compact spinner for live reasoning; pre-answer artwork stays customizable. */
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
  const variant = useSettings((state) => state.appearance.replyAnimation)

  return (
    <div
      role="status"
      aria-live="polite"
      aria-atomic="true"
      className={styles.root}
      data-reply-pending
    >
      <ReplyWaitAnimation variant={variant} />
      <span className="sr-only">{t('replyPending.preparing')}</span>
    </div>
  )
}
