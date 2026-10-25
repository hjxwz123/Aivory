import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { Check } from 'lucide-react'
import { ReplyWaitAnimation } from '@/components/chat/reply-wait-animation'
import { DEFAULT_REPLY_ANIMATION, REPLY_ANIMATION_PRESETS, type ReplyAnimationPref } from '@/types/settings'
import styles from './reply-animation-picker.module.css'

export function ReplyAnimationPicker({ value, onChange, disabled = false }: {
  value: ReplyAnimationPref
  onChange: (value: ReplyAnimationPref) => void
  disabled?: boolean
}) {
  const { t } = useTranslation('settings')
  const name = useId()
  return (
    <fieldset className={styles.picker} disabled={disabled} aria-busy={disabled} data-reply-animation-picker>
      <legend className="sr-only">{t('appearance.replyAnimation.title')}</legend>
      {REPLY_ANIMATION_PRESETS.map((variant) => (
        <label key={variant} className={styles.option}>
          <input
            className="sr-only"
            type="radio"
            name={name}
            value={variant}
            checked={value === variant}
            onChange={() => onChange(variant)}
          />
          <span className={styles.tile}>
            <span className={styles.selection} aria-hidden="true"><Check size={12} /></span>
            <span className={styles.preview}><ReplyWaitAnimation variant={variant} /></span>
            <span className={styles.caption}>
              <span>{t(`appearance.replyAnimation.options.${variant}`)}</span>
              {variant === DEFAULT_REPLY_ANIMATION && <span className={styles.defaultLabel}>{t('appearance.replyAnimation.default')}</span>}
            </span>
          </span>
        </label>
      ))}
    </fieldset>
  )
}
