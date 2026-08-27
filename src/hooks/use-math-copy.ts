import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent } from 'react'
import { useTranslation } from 'react-i18next'
import type { MathCopyLabels } from '@/lib/markdown'
import { copyText } from '@/lib/utils'
import { toast } from '@/hooks/use-toast'

const FEEDBACK_DURATION_MS = 1500

function selectionIntersects(trigger: HTMLElement): boolean {
  if (typeof window === 'undefined') return false
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return false
  try {
    return selection.getRangeAt(0).intersectsNode(trigger)
  } catch {
    return false
  }
}

function resetTrigger(trigger: HTMLElement | null) {
  if (!trigger) return
  trigger.removeAttribute('data-copied')
  const label = trigger.dataset.copyLabel
  if (label) {
    trigger.setAttribute('aria-label', label)
    trigger.setAttribute('title', label)
  }
}

export function useMathCopy() {
  const { t } = useTranslation('chat')
  const labels = useMemo<MathCopyLabels>(
    () => ({
      copy: t('actions.copyLatex', { defaultValue: 'Copy LaTeX' }),
      copied: t('actions.latexCopied', { defaultValue: 'LaTeX copied' }),
    }),
    [t],
  )
  const failedLabel = t('actions.copyLatexFailed', { defaultValue: 'Couldn’t copy the LaTeX' })
  const [announcement, setAnnouncement] = useState('')
  const activeTrigger = useRef<HTMLElement | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const request = useRef(0)

  const clearFeedback = useCallback(() => {
    if (timer.current) clearTimeout(timer.current)
    timer.current = null
    resetTrigger(activeTrigger.current)
    activeTrigger.current = null
    setAnnouncement('')
  }, [])

  useEffect(
    () => () => {
      request.current += 1
      if (timer.current) clearTimeout(timer.current)
      resetTrigger(activeTrigger.current)
    },
    [],
  )

  const handleMathCopy = useCallback(
    (event: MouseEvent<HTMLElement>) => {
      if (!(event.target instanceof Element)) return
      const trigger = event.target.closest<HTMLElement>('[data-math-copy="true"]')
      if (!trigger || !event.currentTarget.contains(trigger) || selectionIntersects(trigger)) return

      const latex = trigger.dataset.latex?.trim()
      if (!latex) return
      const requestID = ++request.current

      void copyText(latex).then((copied) => {
        if (requestID !== request.current) return
        if (!copied) {
          clearFeedback()
          setAnnouncement(failedLabel)
          toast.error(failedLabel)
          timer.current = setTimeout(clearFeedback, FEEDBACK_DURATION_MS)
          return
        }

        clearFeedback()
        activeTrigger.current = trigger
        trigger.dataset.copied = 'true'
        trigger.setAttribute('aria-label', labels.copied)
        trigger.setAttribute('title', labels.copied)
        setAnnouncement(labels.copied)
        timer.current = setTimeout(clearFeedback, FEEDBACK_DURATION_MS)
      })
    },
    [clearFeedback, failedLabel, labels.copied],
  )

  return { announcement, handleMathCopy, labels }
}
