import { useEffect, useState } from 'react'
import { Brush, Clock3, ImageIcon, SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import styles from './image-generating.module.css'

interface ImageGeneratingProps {
  /** Drawing phase, driving the status label. */
  phase: 'preparing' | 'optimizing' | 'generating'
  startedAt?: number
  className?: string
}

export function ImageGenerating({ phase, startedAt, className }: ImageGeneratingProps) {
  const { t } = useTranslation('chat')
  const labels = {
    preparing: t('image.preparing', { defaultValue: 'Preparing your image…' }),
    optimizing: t('image.optimizing', { defaultValue: 'Refining your prompt…' }),
    generating: t('image.generating', { defaultValue: 'Painting your image…' }),
  }
  const PhaseIcon = phase === 'optimizing' ? SlidersHorizontal : Brush

  return (
    <div className={cn(styles.root, className)} data-image-generating={phase}>
      <div className={styles.preview} aria-hidden="true">
        <div className={styles.canvasMark}>
          <span className={styles.corner} />
          <span className={styles.corner} />
          <span className={styles.corner} />
          <span className={styles.corner} />
          <ImageIcon size={44} strokeWidth={1.25} className={styles.imageIcon} />
          <PhaseIcon size={20} strokeWidth={1.5} className={styles.phaseIcon} />
        </div>
        <div className={styles.sweep} />
        <div className={styles.activity}>
          <span /><span /><span />
        </div>
      </div>
      <div className={styles.caption}>
        <span role="status" aria-live="polite" aria-atomic="true" className={styles.label}>
          {labels[phase]}
        </span>
        {startedAt !== undefined && Number.isFinite(startedAt) && startedAt > 0 ? <ElapsedTime startedAt={startedAt} /> : null}
      </div>
    </div>
  )
}

function ElapsedTime({ startedAt }: { startedAt: number }) {
  const { t } = useTranslation('chat')
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  const seconds = Math.max(0, Math.floor((now - startedAt) / 1000))
  const duration = `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`

  return (
    <span role="timer" aria-live="off" aria-label={t('image.elapsed', { duration })} className={styles.elapsed}>
      <Clock3 size={13} aria-hidden />
      {duration}
    </span>
  )
}
