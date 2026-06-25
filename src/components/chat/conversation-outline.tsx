import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { GitBranch, X, ZoomIn, ZoomOut, GripHorizontal } from 'lucide-react'
import { cn } from '@/lib/utils'
import { conversationsApi } from '@/api'
import { useConversations, toLocalMessage } from '@/store/conversations'
import type { Conversation, Message } from '@/types/chat'

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

// A node in the question-tree: one user turn, plus its branch children (the next
// user turns reachable from any of its answers). Edit-branches (sibling
// questions) and the questions that follow each regenerated answer all surface
// as children, so the vertical layout reads as the real conversation tree.
interface QNode {
  msg: Message
  children: QNode[]
}

// Build the user-question tree from the FULL message set (all branches).
// A user message U's tree-parent is its grandparent user: U.parentId is an
// assistant A, and A.parentId is the question that produced A. Roots are the
// user messages with no parent (the original question + any root edit-siblings).
function buildQuestionTree(all: Message[]): QNode[] {
  const byId = new Map(all.map((m) => [m.id, m]))
  const users = all
    .filter((m) => m.role === 'user')
    .sort((a, b) => (a.createdAt ?? 0) - (b.createdAt ?? 0))
  const nodes = new Map<string, QNode>(users.map((u) => [u.id, { msg: u, children: [] }]))
  const roots: QNode[] = []
  for (const u of users) {
    const parentAssistant = u.parentId ? byId.get(u.parentId) : undefined
    const parentUserId = parentAssistant?.parentId
    const parentNode = parentUserId ? nodes.get(parentUserId) : undefined
    if (parentNode) parentNode.children.push(nodes.get(u.id)!)
    else roots.push(nodes.get(u.id)!)
  }
  return roots
}

function countNodes(roots: QNode[]): number {
  let n = 0
  const walk = (list: QNode[]) => {
    for (const node of list) {
      n++
      walk(node.children)
    }
  }
  walk(roots)
  return n
}

// A flat row carries its node plus the indent DEPTH. Depth only increases when
// passing THROUGH a branch point (a question with >1 children), so a linear
// conversation stays a flat vertical list and only real forks indent — a
// git-graph-style vertical tree rather than an ever-deepening staircase.
interface FlatRow {
  node: QNode
  depth: number
  /** True when this row is the last child in its sibling group (rail corner). */
  last: boolean
}

function flattenTree(roots: QNode[]): FlatRow[] {
  const out: FlatRow[] = []
  const walk = (list: QNode[], depth: number) => {
    list.forEach((node, i) => {
      out.push({ node, depth, last: i === list.length - 1 })
      const childDepth = depth + (node.children.length > 1 ? 1 : 0)
      walk(node.children, childDepth)
    })
  }
  // Multiple roots = the very first question was edited into siblings; indent
  // them so the fork is visible, otherwise start the spine flush-left.
  walk(roots, roots.length > 1 ? 1 : 0)
  return out
}

export function ConversationOutline({ conversation, scrollContainerRef, onClose }: ConversationOutlineProps) {
  const { t } = useTranslation('chat')
  const setActiveLeaf = useConversations((s) => s.setActiveLeaf)

  const [pos, setPos] = useState(() => ({
    x: Math.max(16, (typeof window !== 'undefined' ? window.innerWidth : 1280) - 308),
    y: 56,
  }))
  const [size, setSize] = useState({ w: 300, h: 380 })
  const [zoom, setZoom] = useState(1)

  // Sync refs so mouse handlers always see the latest pos/size without needing them in deps.
  const posRef = useRef(pos)
  const sizeRef = useRef(size)
  posRef.current = pos
  sizeRef.current = size

  const dragRef = useRef<{ mx: number; my: number; px: number; py: number } | null>(null)
  const resizeRef = useRef<{ mx: number; my: number; w: number; h: number } | null>(null)

  // The full branch tree (all branches, not just the active path). Fetched via
  // ?mode=tree so edges (parent_id) + sibling groups come straight from the
  // server. Falls back to the active path if the fetch fails. Refetched when the
  // tree shape can have changed (a turn was added/edited/regenerated) — gated on
  // "not streaming" so we don't refetch on every token.
  const convId = conversation.id
  const settledCount = conversation.messages.filter((m) => !m.streaming).length
  const [treeMsgs, setTreeMsgs] = useState<Message[] | null>(null)
  useEffect(() => {
    let alive = true
    conversationsApi
      .messages(convId, 'tree')
      .then((rows) => {
        if (alive) setTreeMsgs(rows.map(toLocalMessage))
      })
      .catch(() => {
        if (alive) setTreeMsgs(null)
      })
    return () => {
      alive = false
    }
  }, [convId, settledCount])

  // The set of message ids on the CURRENT active path — drives the highlight.
  const activeIds = useMemo(
    () => new Set(conversation.messages.map((m) => m.id)),
    [conversation.messages],
  )
  const roots = useMemo(
    () => buildQuestionTree(treeMsgs ?? conversation.messages),
    [treeMsgs, conversation.messages],
  )
  const rows = useMemo(() => flattenTree(roots), [roots])
  const total = useMemo(() => countNodes(roots), [roots])
  // The single accent (CLAUDE.md §2.4) marks where you currently are: the last
  // user turn on the active path. Other active-path nodes read as plain fg.
  const activeLeafId = useMemo(() => {
    const us = conversation.messages.filter((m) => m.role === 'user')
    return us.length ? us[us.length - 1].id : undefined
  }, [conversation.messages])

  function scrollToMessage(msgId: string) {
    const container = scrollContainerRef.current
    if (!container) return
    const el = container.querySelector<HTMLElement>(`[data-message-id="${msgId}"]`)
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
    else container.scrollTo({ top: 0, behavior: 'smooth' })
  }

  // Click a node: if it's already on the active path, jump to it; otherwise
  // switch the active branch to it (R4-safe — never interrupts a streaming
  // sibling) and then scroll once the new path has rendered.
  function onNodeClick(node: QNode) {
    if (activeIds.has(node.msg.id)) {
      scrollToMessage(node.msg.id)
      return
    }
    void setActiveLeaf(convId, node.msg.id).then(() => {
      window.setTimeout(() => scrollToMessage(node.msg.id), 120)
    })
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
          {t('outline.title', { defaultValue: 'Conversation tree' })}
          {total > 0 ? (
            <span className="ml-1.5 text-[var(--color-fg-subtle)] font-normal">· {total}</span>
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

      {/* Vertical branch tree */}
      <div className="flex-1 min-h-0 overflow-auto scrollbar-thin py-1.5">
        {total === 0 ? (
          <div className="px-4 py-4 text-[12px] text-[var(--color-fg-subtle)]">
            {t('outline.empty', { defaultValue: 'No messages yet.' })}
          </div>
        ) : (
          <div className="flex flex-col pr-2" style={{ fontSize: `${basePx}px` }}>
            {rows.map((row) => (
              <OutlineRow
                key={row.node.msg.id}
                row={row}
                active={activeIds.has(row.node.msg.id)}
                current={row.node.msg.id === activeLeafId}
                onClick={onNodeClick}
                emptyLabel={t('outline.emptyMessage', { defaultValue: '(empty)' })}
              />
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

// OutlineRow — one question in the vertical tree. Indentation encodes branch
// depth (it only grows at a fork), with a left rail + corner connector drawn for
// indented rows so forks read as a tree. The current active leaf carries the lone
// clay accent dot (CLAUDE.md §2.4); other active-path nodes read as plain fg,
// off-path nodes stay muted. A branch point (a question with >1 child) shows a
// small fork count.
function OutlineRow({
  row,
  active,
  current,
  onClick,
  emptyLabel,
}: {
  row: FlatRow
  active: boolean
  current: boolean
  onClick: (node: QNode) => void
  emptyLabel: string
}) {
  const { node, depth, last } = row
  const branchPoint = node.children.length > 1
  return (
    <button
      type="button"
      onClick={() => onClick(node)}
      aria-current={current ? 'true' : undefined}
      style={{ paddingLeft: `${0.5 + depth * 1.1}em` }}
      className={cn(
        'group relative flex w-full items-start gap-[0.5em] py-[0.5em] pr-1 text-left transition-colors rounded-[6px]',
        'hover:bg-[var(--color-bg-muted)] active:bg-[var(--color-bg-muted)]/80',
        'focus-visible:outline-none focus-visible:bg-[var(--color-bg-muted)]',
      )}
    >
      {/* Fork rail — a vertical guide line to the left of each indented (branched)
          row; stacked rows form the branch spine. Token-driven 1px border; only
          the em-based position is inline (it tracks the dynamic depth). */}
      {depth > 0 ? (
        <span
          aria-hidden
          className={cn(
            'absolute top-0 border-l border-[var(--color-divider)]',
            last ? 'h-[0.85em]' : 'bottom-0',
          )}
          style={{ left: `${0.5 + (depth - 1) * 1.1 + 0.3}em` }}
        />
      ) : null}
      <span
        aria-hidden
        className={cn(
          'mt-[0.32em] size-[0.55em] shrink-0 rounded-full border transition-colors',
          current
            ? 'bg-[var(--color-accent)] border-[var(--color-accent)]'
            : active
              ? 'bg-[var(--color-fg-muted)] border-[var(--color-fg-muted)]'
              : 'bg-[var(--color-surface)] border-[var(--color-border-strong)] group-hover:border-[var(--color-fg-subtle)]',
        )}
      />
      <span className="flex-1 min-w-0">
        <span
          className={cn(
            'block text-[1em] leading-[1.4] transition-colors',
            active
              ? 'text-[var(--color-fg)] font-medium'
              : 'text-[var(--color-fg-muted)] group-hover:text-[var(--color-fg)]',
          )}
          style={{
            display: '-webkit-box',
            WebkitLineClamp: 2,
            WebkitBoxOrient: 'vertical',
            overflow: 'hidden',
          }}
        >
          {node.msg.content || emptyLabel}
        </span>
        {branchPoint ? (
          <span className="mt-[0.2em] inline-flex items-center gap-[0.3em] text-[0.8em] text-[var(--color-fg-subtle)]">
            <GitBranch size={9} aria-hidden />
            {node.children.length}
          </span>
        ) : null}
      </span>
    </button>
  )
}
