import { useEffect, useRef, useState, type HTMLAttributes, type RefObject } from 'react'
import type { Conversation } from '@/types/chat'

type DropTarget = { id: string; position: 'before' | 'after' }

function sameGroup(a: Conversation, b: Conversation) {
  return (a.workspaceId ?? '') === (b.workspaceId ?? '') &&
    (a.projectId ?? '') === (b.projectId ?? '') &&
    (Boolean(a.projectId) || Boolean(a.starred) === Boolean(b.starred))
}

function settled(conversation: Conversation) {
  return !conversation.archived && !conversation.inline &&
    !conversation.messages.some((message) => message.streaming)
}

export function useConversationReorder({ conversations, enabled, scope, scrollRef, onReorder }: {
  conversations: Conversation[]
  enabled: boolean
  scope: string
  scrollRef: RefObject<HTMLDivElement | null>
  onReorder: (id: string, targetId: string, position: 'before' | 'after') => Promise<boolean>
}) {
  const [draggedId, setDraggedId] = useState<string | null>(null)
  const [dropTarget, setDropTarget] = useState<DropTarget | null>(null)
  const [saving, setSaving] = useState(false)
  const sourceRef = useRef<string | null>(null)
  const targetRef = useRef<DropTarget | null>(null)
  const busyRef = useRef(false)
  const touchCleanupRef = useRef<(() => void) | null>(null)
  const suppressClickUntilRef = useRef(0)
  const currentRef = useRef({ conversations, enabled, onReorder })
  currentRef.current = { conversations, enabled, onReorder }

  function reset() {
    touchCleanupRef.current?.()
    touchCleanupRef.current = null
    sourceRef.current = null
    targetRef.current = null
    setDraggedId(null)
    setDropTarget(null)
  }

  useEffect(() => {
    sourceRef.current = null
    targetRef.current = null
    setDraggedId(null)
    setDropTarget(null)
    return () => { touchCleanupRef.current?.() }
  }, [scope, enabled])

  function find(id: string) {
    return currentRef.current.conversations.find((conversation) => conversation.id === id)
  }

  function start(id: string) {
    const source = find(id)
    if (!currentRef.current.enabled || busyRef.current || !source || !settled(source)) return false
    sourceRef.current = id
    setDraggedId(id)
    return true
  }

  function hover(id: string, clientY: number, element: HTMLElement) {
    const source = sourceRef.current ? find(sourceRef.current) : undefined
    const target = find(id)
    if (!source || !target || source.id === target.id || !settled(target) || !sameGroup(source, target)) {
      targetRef.current = null
      setDropTarget(null)
      return false
    }
    const bounds = element.getBoundingClientRect()
    const position = clientY < bounds.top + bounds.height / 2 ? 'before' : 'after'
    if (targetRef.current?.id !== id || targetRef.current.position !== position) {
      targetRef.current = { id, position }
      setDropTarget({ id, position })
    }
    const scroller = scrollRef.current
    if (scroller) {
      const viewport = scroller.getBoundingClientRect()
      if (clientY < viewport.top + 40) scroller.scrollTop -= 12
      else if (clientY > viewport.bottom - 40) scroller.scrollTop += 12
    }
    return true
  }

  async function commit(id: string, target: DropTarget) {
    reset()
    if (busyRef.current || !currentRef.current.enabled) return
    busyRef.current = true
    setSaving(true)
    suppressClickUntilRef.current = Date.now() + 500
    try {
      await currentRef.current.onReorder(id, target.id, target.position)
    } finally {
      busyRef.current = false
      setSaving(false)
    }
  }

  function rowProps(conversation: Conversation): HTMLAttributes<HTMLLIElement> {
    const canDrag = enabled && !saving && settled(conversation)
    return {
      draggable: canDrag,
      onDragStart(event) {
        if ((event.target as HTMLElement).closest('button') || !start(conversation.id)) {
          event.preventDefault()
          return
        }
        event.dataTransfer.effectAllowed = 'move'
        event.dataTransfer.setData('application/x-aivory-conversation', conversation.id)
        event.dataTransfer.setData('text/plain', conversation.title)
      },
      onDragOver(event) {
        if (!hover(conversation.id, event.clientY, event.currentTarget)) return
        event.preventDefault()
        event.stopPropagation()
        event.dataTransfer.dropEffect = 'move'
      },
      onDragLeave(event) {
        if (event.currentTarget.contains(event.relatedTarget as Node | null)) return
        if (targetRef.current?.id === conversation.id) {
          targetRef.current = null
          setDropTarget(null)
        }
      },
      onDrop(event) {
        const id = sourceRef.current
        const target = targetRef.current
        if (!id || !target || target.id !== conversation.id) return
        event.preventDefault()
        event.stopPropagation()
        void commit(id, target)
      },
      onDragEnd() {
        suppressClickUntilRef.current = Date.now() + 500
        reset()
      },
      onClickCapture(event) {
        if (Date.now() < suppressClickUntilRef.current) {
          event.preventDefault()
          event.stopPropagation()
        }
      },
      onKeyDown(event) {
        if (event.key === 'Escape' && sourceRef.current) {
          event.preventDefault()
          reset()
          return
        }
        if (!canDrag || !event.altKey || (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') ||
          !(event.target as HTMLElement).closest('a')) return
        const peers = currentRef.current.conversations.filter((peer) => settled(peer) && sameGroup(conversation, peer))
          .sort((a, b) => b.updatedAt - a.updatedAt || b.id.localeCompare(a.id))
        const index = peers.findIndex((peer) => peer.id === conversation.id)
        const direction = event.key === 'ArrowUp' ? -1 : 1
        const target = peers[index + direction]
        if (!target) return
        event.preventDefault()
        void commit(conversation.id, { id: target.id, position: direction < 0 ? 'before' : 'after' })
      },
      // A short hold starts touch dragging; movement before the hold remains
      // normal drawer scrolling. Native HTML dragging handles mouse input.
      onTouchStart(event) {
        if (!canDrag || event.touches.length !== 1 || (event.target as HTMLElement).closest('button')) return
        touchCleanupRef.current?.()
        const touch = event.touches[0]
        let active = false
        const timer = window.setTimeout(() => { active = start(conversation.id) }, 350)
        const cleanup = () => {
          window.clearTimeout(timer)
          document.removeEventListener('touchmove', move)
          document.removeEventListener('touchend', end)
          document.removeEventListener('touchcancel', cancel)
        }
        const move = (moveEvent: TouchEvent) => {
          if (moveEvent.touches.length !== 1) { cancel(); return }
          const point = moveEvent.touches[0]
          if (!active) {
            if (Math.hypot(point.clientX - touch.clientX, point.clientY - touch.clientY) > 8) {
              cleanup()
              touchCleanupRef.current = null
            }
            return
          }
          moveEvent.preventDefault()
          const row = document.elementFromPoint(point.clientX, point.clientY)?.closest<HTMLElement>('li[data-conversation-id]')
          if (row?.dataset.conversationId) hover(row.dataset.conversationId, point.clientY, row)
          else { targetRef.current = null; setDropTarget(null) }
        }
        const end = () => {
          const id = sourceRef.current
          const target = targetRef.current
          cleanup()
          touchCleanupRef.current = null
          if (active) suppressClickUntilRef.current = Date.now() + 500
          if (active && id && target) void commit(id, target)
          else reset()
        }
        const cancel = () => { cleanup(); touchCleanupRef.current = null; reset() }
        document.addEventListener('touchmove', move, { passive: false })
        document.addEventListener('touchend', end)
        document.addEventListener('touchcancel', cancel)
        touchCleanupRef.current = cleanup
      },
      onContextMenu(event) {
        if (sourceRef.current) event.preventDefault()
      },
    }
  }

  return { draggedId, dropTarget, saving, rowProps }
}

export type ConversationReorderController = ReturnType<typeof useConversationReorder>
