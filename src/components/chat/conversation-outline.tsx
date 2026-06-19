import { useEffect, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { GitBranch, X, ZoomIn, ZoomOut, GripHorizontal } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { Conversation } from '@/types/chat'

interface ConversationOutlineProps {
  conversation: Conversation
  scrollContainerRef: RefObject<HTMLDivElement | null>
  onClose: () => void
}

const MIN_W = 220
const MAX_W = 520
const MIN_H = 180
const MAX_H = 640
const STEP = 0.125

export function ConversationOutline({ conversation, scrollContainerRef, onClose }: ConversationOutlineProps) {
  const { t } = useTranslation('chat')

  const [pos, setPos] = useState(() => ({
    x: Math.max(16, (typeof window !== 'undefined' ? window.innerWidth : 1280) - 308),
    y: 56,
  }))
  const [size, setSize] = useState({ w: 284, h: 360 })
  const [zoom, setZoom] = useState(1)

  // Sync refs so mouse handlers always see the latest pos/size without needing them in deps.
  const posRef = useRef(pos)
  const sizeRef = useRef(size)
  posRef.current = pos
  sizeRef.current = size

  const dragRef = useRef<{ mx: number; my: number; px: number; py: number } | null>(null)
  const resizeRef = useRef<{ mx: number; my: number; w: number; h: number } | null>(null)

  const userMessages = conversation.messages.filter((m) => m.role === 'user')

  function scrollToMessage(msgId: string) {
    const container = scrollContainerRef.current
    if (!container) return
    const el = container.querySelector<HTMLElement>(`[data-message-id="${msgId}"]`)
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' })
    } else {
      // Message not yet rendered by the lazy window — scroll to top to reveal it.
      container.scrollTo({ top: 0, behavior: 'smooth' })
    }
  }

  // Global mouse move / up — mounted once, reads from refs to avoid stale closure.
  useEffect(() => {
    function onMove(e: MouseEvent) {
      if (dragRef.current) {
        const dx = e.clientX - dragRef.current.mx
        const dy = e.clientY - dragRef.current.my
        const newX = Math.max(0, Math.min(window.innerWidth - sizeRef.current.w, dragRef.current.px + dx))
        const newY = Math.max(0, Math.min(window.innerHeight - 60, dragRef.current.py + dy))
        setPos({ x: newX, y: newY })
      }
      if (resizeRef.current) {
        const dx = e.clientX - resizeRef.current.mx
        const dy = e.clientY - resizeRef.current.my
        const newW = Math.max(MIN_W, Math.min(MAX_W, resizeRef.current.w + dx))
        const newH = Math.max(MIN_H, Math.min(MAX_H, resizeRef.current.h + dy))
        setSize({ w: newW, h: newH })
      }
    }
    function onUp() {
      dragRef.current = null
      resizeRef.current = null
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    return () => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
    }
  }, [])

  function onDragDown(e: React.MouseEvent) {
    e.preventDefault()
    dragRef.current = { mx: e.clientX, my: e.clientY, px: posRef.current.x, py: posRef.current.y }
  }

  function onResizeDown(e: React.MouseEvent) {
    e.preventDefault()
    e.stopPropagation()
    resizeRef.current = { mx: e.clientX, my: e.clientY, w: sizeRef.current.w, h: sizeRef.current.h }
  }

  const canZoomOut = zoom > 0.75 + 0.001
  const canZoomIn = zoom < 1.5 - 0.001

  // Base font size in px at zoom 1.0 is 12.5px.
  const basePx = Math.round(zoom * 12.5 * 10) / 10

  return (
    <div
      style={{ left: pos.x, top: pos.y, width: size.w, height: size.h }}
      className="fixed z-[200] flex flex-col rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)] select-none overflow-hidden"
    >
      {/* Header / drag handle */}
      <div
        onMouseDown={onDragDown}
        className="flex items-center gap-2 px-3 py-2 border-b border-[var(--color-divider)] cursor-grab active:cursor-grabbing shrink-0 bg-[var(--color-surface)]"
      >
        <GitBranch size={13} aria-hidden className="text-[var(--color-fg-muted)] shrink-0" />
        <span className="flex-1 min-w-0 truncate text-[12.5px] font-medium text-[var(--color-fg)]">
          {t('outline.title', { defaultValue: 'Conversation outline' })}
          {userMessages.length > 0 ? (
            <span className="ml-1.5 text-[var(--color-fg-subtle)] font-normal">
              · {userMessages.length}
            </span>
          ) : null}
        </span>
        <div className="flex items-center gap-0.5 shrink-0">
          <button
            type="button"
            onClick={() => setZoom((z) => parseFloat(Math.max(0.75, z - STEP).toFixed(3)))}
            disabled={!canZoomOut}
            aria-label={t('outline.zoomOut', { defaultValue: 'Zoom out' })}
            className="inline-flex items-center justify-center size-6 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-35 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <ZoomOut size={11} aria-hidden />
          </button>
          <button
            type="button"
            onClick={() => setZoom((z) => parseFloat(Math.min(1.5, z + STEP).toFixed(3)))}
            disabled={!canZoomIn}
            aria-label={t('outline.zoomIn', { defaultValue: 'Zoom in' })}
            className="inline-flex items-center justify-center size-6 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-35 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <ZoomIn size={11} aria-hidden />
          </button>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('outline.close', { defaultValue: 'Close outline' })}
            className="inline-flex items-center justify-center size-6 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <X size={11} aria-hidden />
          </button>
        </div>
      </div>

      {/* Message list */}
      <div className="flex-1 min-h-0 overflow-y-auto scrollbar-thin py-1">
        {userMessages.length === 0 ? (
          <div className="px-4 py-4 text-[12px] text-[var(--color-fg-subtle)]">
            {t('outline.empty', { defaultValue: 'No messages yet.' })}
          </div>
        ) : (
          <div className="flex flex-col" style={{ fontSize: `${basePx}px` }}>
            {userMessages.map((m, i) => (
              <button
                key={m.id}
                type="button"
                onClick={() => scrollToMessage(m.id)}
                className={cn(
                  'group flex items-start gap-[0.6em] px-3 py-[0.55em] text-left w-full',
                  'hover:bg-[var(--color-bg-muted)] active:bg-[var(--color-bg-muted)]/80',
                  'transition-colors focus-visible:outline-none focus-visible:bg-[var(--color-bg-muted)]',
                )}
              >
                <span className="shrink-0 font-mono text-[0.82em] text-[var(--color-fg-subtle)] tabular-nums w-[1.8em] text-right leading-[1.5] mt-[0.1em]">
                  {i + 1}
                </span>
                <span className="flex-1 min-w-0">
                  <span
                    className="block text-[1em] leading-[1.4] text-[var(--color-fg)] group-hover:text-[var(--color-accent)] transition-colors"
                    style={{
                      display: '-webkit-box',
                      WebkitLineClamp: 2,
                      WebkitBoxOrient: 'vertical',
                      overflow: 'hidden',
                    }}
                  >
                    {m.content || t('outline.emptyMessage', { defaultValue: '(empty)' })}
                  </span>
                  {(m.branchCount ?? 1) > 1 ? (
                    <span className="mt-[0.25em] flex items-center gap-[0.35em] text-[0.82em] text-[var(--color-fg-subtle)]">
                      <GitBranch size={9} aria-hidden />
                      {t('outline.branches', {
                        defaultValue: '{{count}} branches',
                        count: m.branchCount,
                      })}
                    </span>
                  ) : null}
                </span>
              </button>
            ))}
          </div>
        )}
      </div>

      {/* Resize handle — bottom-right corner */}
      <div
        onMouseDown={onResizeDown}
        aria-hidden
        className="absolute bottom-0 right-0 size-5 cursor-nwse-resize flex items-center justify-center opacity-40 hover:opacity-80 transition-opacity"
      >
        <GripHorizontal size={10} className="rotate-45 text-[var(--color-fg-subtle)]" />
      </div>
    </div>
  )
}
