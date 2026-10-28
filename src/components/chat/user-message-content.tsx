import { useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown } from 'lucide-react'
import styles from './user-message-content.module.css'

/** Limit long questions by their rendered height, including Markdown and math. */
export function UserMessageContent({ text, children }: { text: string; children: ReactNode }) {
  const { t } = useTranslation('chat')
  const id = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const [collapsible, setCollapsible] = useState(false)
  const [disclosure, setDisclosure] = useState({ text, expanded: false })
  const expanded = disclosure.text === text && disclosure.expanded

  useLayoutEffect(() => {
    const root = rootRef.current
    const content = contentRef.current
    if (!root || !content) return
    const measure = () => {
      const style = getComputedStyle(root)
      const lines = Number(style.getPropertyValue('--user-message-preview-lines')) || 8
      const limit = parseFloat(style.lineHeight) * lines
      setCollapsible(content.getBoundingClientRect().height > limit + 1)
    }
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    observer?.observe(content)
    window.addEventListener('resize', measure)
    return () => {
      observer?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [text])

  return <div ref={rootRef} className={styles.root} data-user-message-content>
    <div
      id={id}
      className={styles.viewport}
      data-user-message-viewport
      data-collapsed={collapsible && !expanded}
      onFocusCapture={() => { if (collapsible && !expanded) setDisclosure({ text, expanded: true }) }}
    >
      <div ref={contentRef} className={styles.content}>{children}</div>
    </div>
    {collapsible ? <button
      type="button"
      aria-expanded={expanded}
      aria-controls={id}
      className={styles.toggle}
      data-user-message-toggle
      onClick={() => setDisclosure({ text, expanded: !expanded })}
    >
      <span>{expanded ? t('message.collapse') : t('message.expandAll')}</span>
      <ChevronDown size={14} aria-hidden className={styles.chevron} />
    </button> : null}
  </div>
}
