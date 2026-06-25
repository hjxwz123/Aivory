import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { GitBranch, X, ZoomIn, ZoomOut, GripHorizontal, Maximize2, User } from 'lucide-react'
import { cn } from '@/lib/utils'
import { conversationsApi } from '@/api'
import { useConversations, toLocalMessage } from '@/store/conversations'
import { useModels } from '@/store/models'
import { ModelIcon } from './model-icon'
import type { Conversation, Message } from '@/types/chat'

interface ConversationOutlineProps {
  conversation: Conversation
  scrollContainerRef: RefObject<HTMLDivElement | null>
  onClose: () => void
}

const MIN_W = 260
const MAX_W = 760
const MIN_H = 240
const MAX_H = 820
const STEP = 0.1
const ZOOM_MIN = 0.3
const ZOOM_MAX = 1.5

// Node-graph geometry (canvas px, before zoom). A turn = two stacked nodes.
const NODE_W = 184
const NODE_H = 72
const COL_W = 212 // horizontal slot per leaf
const ROW_H = 132 // vertical slot per depth

// A node in the FULL message tree (user + assistant), top-down.
interface TNode {
  msg: Message
  children: TNode[]
}

function buildMessageTree(all: Message[]): TNode[] {
  const byId = new Map<string, TNode>(all.map((m) => [m.id, { msg: m, children: [] }]))
  const roots: TNode[] = []
  // Sort so siblings (same parent) keep chronological left→right order.
  const sorted = [...all].sort((a, b) => (a.createdAt ?? 0) - (b.createdAt ?? 0))
  for (const m of sorted) {
    const node = byId.get(m.id)!
    const parent = m.parentId ? byId.get(m.parentId) : undefined
    if (parent) parent.children.push(node)
    else roots.push(node)
  }
  return roots
}

interface LaidNode {
  msg: Message
  depth: number
  cx: number // center x (px)
  cy: number // center y (px)
}
interface Edge {
  id: string
  px: number
  py: number
  cx: number
  cy: number
  active: boolean
}

// Tidy top-down layout: leaves take sequential x slots, a parent centers over its
// children (first child left, the rest spread right). Deterministic — no library.
function layoutTree(roots: TNode[], activeIds: Set<string>): { nodes: LaidNode[]; edges: Edge[]; width: number; height: number } {
  const nodes: LaidNode[] = []
  const edges: Edge[] = []
  let nextLeaf = 0
  let maxDepth = 0

  const place = (node: TNode, depth: number): { cx: number; cy: number; cxUnits: number } => {
    maxDepth = Math.max(maxDepth, depth)
    const cy = depth * ROW_H + ROW_H / 2
    let cxUnits: number
    if (node.children.length === 0) {
      cxUnits = nextLeaf
      nextLeaf += 1
    } else {
      const kids = node.children.map((c) => place(c, depth + 1))
      cxUnits = (kids[0].cxUnits + kids[kids.length - 1].cxUnits) / 2
      // edges parent → each child
      const px = cxUnits * COL_W + COL_W / 2
      for (let i = 0; i < node.children.length; i++) {
        const child = node.children[i]
        const k = kids[i]
        edges.push({
          id: `${node.msg.id}->${child.msg.id}`,
          px,
          py: cy + NODE_H / 2,
          cx: k.cx,
          cy: k.cy - NODE_H / 2,
          active: activeIds.has(node.msg.id) && activeIds.has(child.msg.id),
        })
      }
    }
    const cx = cxUnits * COL_W + COL_W / 2
    nodes.push({ msg: node.msg, depth, cx, cy })
    return { cx, cy, cxUnits }
  }

  roots.forEach((r) => place(r, 0))
  const width = Math.max(1, nextLeaf) * COL_W
  const height = (maxDepth + 1) * ROW_H
  return { nodes, edges, width, height }
}

export function ConversationOutline({ conversation, scrollContainerRef, onClose }: ConversationOutlineProps) {
  const { t } = useTranslation('chat')
  const setActiveLeaf = useConversations((s) => s.setActiveLeaf)
  const getModel = useModels((s) => s.getById)

  const [pos, setPos] = useState(() => ({
    x: Math.max(16, (typeof window !== 'undefined' ? window.innerWidth : 1280) - 452),
    y: 56,
  }))
  const [size, setSize] = useState({ w: 440, h: 480 })
  const [zoom, setZoom] = useState(0.8)

  const posRef = useRef(pos)
  const sizeRef = useRef(size)
  posRef.current = pos
  sizeRef.current = size

  const dragRef = useRef<{ mx: number; my: number; px: number; py: number } | null>(null)
  const resizeRef = useRef<{ mx: number; my: number; w: number; h: number } | null>(null)
  const bodyRef = useRef<HTMLDivElement>(null)
  const didFit = useRef(false)

  // Full branch tree (all branches). ?mode=tree gives parent_id edges straight
  // from the server; refetch only when the tree shape can change (gated on "not
  // streaming" so we don't refetch per token).
  const convId = conversation.id
  const settledCount = conversation.messages.filter((m) => !m.streaming).length
  const [treeMsgs, setTreeMsgs] = useState<Message[] | null>(null)
  useEffect(() => {
    let alive = true
    conversationsApi
      .messages(convId, 'tree')
      .then((rows) => alive && setTreeMsgs(rows.map(toLocalMessage)))
      .catch(() => alive && setTreeMsgs(null))
    return () => {
      alive = false
    }
  }, [convId, settledCount])

  const activeIds = useMemo(() => new Set(conversation.messages.map((m) => m.id)), [conversation.messages])
  const activeLeafId = useMemo(() => {
    const path = conversation.messages
    return path.length ? path[path.length - 1].id : undefined
  }, [conversation.messages])

  const { nodes, edges, width, height } = useMemo(
    () => layoutTree(buildMessageTree(treeMsgs ?? conversation.messages), activeIds),
    [treeMsgs, conversation.messages, activeIds],
  )

  // Fit the whole graph into the panel — on first load and on demand.
  function fit() {
    const el = bodyRef.current
    const bw = el ? el.clientWidth : sizeRef.current.w - 8
    const bh = el ? el.clientHeight : sizeRef.current.h - 48
    const z = Math.min((bw - 24) / width, (bh - 24) / height, 1)
    setZoom(Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, z)))
  }
  useEffect(() => {
    if (didFit.current || nodes.length === 0) return
    didFit.current = true
    // Defer so the body has measured its size.
    requestAnimationFrame(fit)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes.length, width, height])

  function scrollToMessage(msgId: string) {
    const container = scrollContainerRef.current
    if (!container) return
    const el = container.querySelector<HTMLElement>(`[data-message-id="${msgId}"]`)
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
    else container.scrollTo({ top: 0, behavior: 'smooth' })
  }

  // Click a node: jump if it's on the active path, otherwise switch the branch to
  // it (R4-safe — never interrupts a streaming sibling) then scroll.
  function onNodeClick(msg: Message) {
    if (activeIds.has(msg.id)) {
      scrollToMessage(msg.id)
      return
    }
    void setActiveLeaf(convId, msg.id).then(() => window.setTimeout(() => scrollToMessage(msg.id), 120))
  }

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
        setSize({
          w: Math.max(MIN_W, Math.min(MAX_W, resizeRef.current.w + dx)),
          h: Math.max(MIN_H, Math.min(MAX_H, resizeRef.current.h + dy)),
        })
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

  const canZoomOut = zoom > ZOOM_MIN + 0.001
  const canZoomIn = zoom < ZOOM_MAX - 0.001

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
          {t('outline.title', { defaultValue: 'Conversation tree' })}
          {nodes.length > 0 ? (
            <span className="ml-1.5 text-[var(--color-fg-subtle)] font-normal">· {nodes.length}</span>
          ) : null}
        </span>
        <div className="flex items-center gap-0.5 shrink-0">
          <button
            type="button"
            onClick={fit}
            aria-label={t('outline.fit', { defaultValue: 'Fit view' })}
            className="inline-flex items-center justify-center size-6 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <Maximize2 size={11} aria-hidden />
          </button>
          <button
            type="button"
            onClick={() => setZoom((z) => parseFloat(Math.max(ZOOM_MIN, z - STEP).toFixed(3)))}
            disabled={!canZoomOut}
            aria-label={t('outline.zoomOut', { defaultValue: 'Zoom out' })}
            className="inline-flex items-center justify-center size-6 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-35 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <ZoomOut size={11} aria-hidden />
          </button>
          <button
            type="button"
            onClick={() => setZoom((z) => parseFloat(Math.min(ZOOM_MAX, z + STEP).toFixed(3)))}
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

      {/* Graph canvas — pannable (scroll), zoom via the header buttons. */}
      <div ref={bodyRef} className="flex-1 min-h-0 overflow-auto scrollbar-thin bg-[var(--color-bg)]">
        {nodes.length === 0 ? (
          <div className="px-4 py-4 text-[12px] text-[var(--color-fg-subtle)]">
            {t('outline.empty', { defaultValue: 'No messages yet.' })}
          </div>
        ) : (
          <div style={{ width: width * zoom, height: height * zoom }}>
            <div style={{ width, height, transform: `scale(${zoom})`, transformOrigin: '0 0' }} className="relative">
              {/* Edges */}
              <svg width={width} height={height} className="absolute inset-0 pointer-events-none overflow-visible">
                {edges.map((e) => {
                  const midY = e.py + (e.cy - e.py) / 2
                  return (
                    <path
                      key={e.id}
                      d={`M ${e.px} ${e.py} V ${midY} H ${e.cx} V ${e.cy}`}
                      fill="none"
                      stroke={e.active ? 'var(--color-fg-subtle)' : 'var(--color-border-strong)'}
                      strokeWidth={1.5}
                      strokeDasharray={e.active ? undefined : '4 4'}
                    />
                  )
                })}
              </svg>
              {/* Nodes */}
              {nodes.map((n) => {
                const isUser = n.msg.role === 'user'
                const model = !isUser && n.msg.modelId ? getModel(n.msg.modelId) : undefined
                const active = activeIds.has(n.msg.id)
                const current = n.msg.id === activeLeafId
                return (
                  <button
                    key={n.msg.id}
                    type="button"
                    onClick={() => onNodeClick(n.msg)}
                    aria-current={current ? 'true' : undefined}
                    style={{ left: n.cx - NODE_W / 2, top: n.cy - NODE_H / 2, width: NODE_W, height: NODE_H }}
                    className={cn(
                      'absolute flex flex-col gap-1 rounded-[10px] border px-2.5 py-2 text-left overflow-hidden transition-colors',
                      active
                        ? 'border-[var(--color-border-strong)] bg-[var(--color-surface)]'
                        : 'border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] opacity-80',
                      current && 'ring-1 ring-[var(--color-accent)] border-[var(--color-accent)]',
                      'hover:border-[var(--color-fg-subtle)] hover:opacity-100 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    )}
                  >
                    <span className="flex items-center gap-1.5 shrink-0">
                      {isUser ? (
                        <span className="inline-flex size-[15px] items-center justify-center rounded-full bg-[var(--color-bg-subtle)] text-[var(--color-fg-muted)]">
                          <User size={10} aria-hidden />
                        </span>
                      ) : (
                        <ModelIcon icon={model?.icon} size={15} />
                      )}
                      <span className="truncate text-[11px] font-medium text-[var(--color-fg)]">
                        {isUser
                          ? t('common.you', { ns: 'common', defaultValue: 'You' })
                          : model?.label || n.msg.modelLabel || t('assistant')}
                      </span>
                    </span>
                    <span
                      className="text-[11px] leading-snug text-[var(--color-fg-muted)]"
                      style={{ display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}
                    >
                      {n.msg.content || t('outline.emptyMessage', { defaultValue: '(empty)' })}
                    </span>
                  </button>
                )
              })}
            </div>
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
