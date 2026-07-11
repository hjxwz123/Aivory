import { useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { GitBranch, X, ZoomIn, ZoomOut, GripHorizontal, Maximize2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'

import { conversationsApi } from '@/api'
import { useConversations, toLocalMessage } from '@/store/conversations'
import { useModels } from '@/store/models'
import { useAuth } from '@/store/auth'
import { Avatar, AvatarImage, AvatarFallback } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
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

// Tree self-heal refetch backoff (ms), scaled by retry count.
const treeRefetchBackoffMs = 200

// Node-graph geometry (canvas px, before zoom). A turn = two stacked nodes.
const NODE_W = 200
const NODE_H = 78
const COL_W = 236 // horizontal slot per leaf
const ROW_H = 152 // vertical slot per depth
const GRID = 16 // dotted-canvas dot spacing (px, at 1:1 zoom)

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
  // Phone: dock as a bottom sheet (full-width, backdrop) instead of a tiny
  // draggable desktop window (§ mobile redesign).
  const isPhone = useMediaQuery(mediaQuery.phone)
  const setActiveLeaf = useConversations((s) => s.setActiveLeaf)
  const getModel = useModels((s) => s.getById)
  // The active line's user nodes are labelled with the current account (name +
  // avatar), matching the chat bubbles rather than a generic "You".
  const user = useAuth((s) => s.user)
  const youLabel = t('common.you', { ns: 'common', defaultValue: 'You' })
  const displayName = user?.name || user?.email?.split('@')[0] || youLabel
  const avatarUrl = (user?.settings as Record<string, unknown> | undefined)?.avatar_url as string | undefined

  // Open as a roomy panel centered on screen (then the user can drag it aside).
  const [size, setSize] = useState(() => {
    const vw = typeof window !== 'undefined' ? window.innerWidth : 1280
    const vh = typeof window !== 'undefined' ? window.innerHeight : 800
    return {
      w: Math.round(Math.min(MAX_W, Math.max(520, vw * 0.52))),
      h: Math.round(Math.min(MAX_H, Math.max(560, vh * 0.72))),
    }
  })
  const [pos, setPos] = useState(() => {
    const vw = typeof window !== 'undefined' ? window.innerWidth : 1280
    const vh = typeof window !== 'undefined' ? window.innerHeight : 800
    const w = Math.round(Math.min(MAX_W, Math.max(520, vw * 0.52)))
    const h = Math.round(Math.min(MAX_H, Math.max(560, vh * 0.72)))
    return { x: Math.max(16, Math.round((vw - w) / 2)), y: Math.max(16, Math.round((vh - h) / 2)) }
  })
  // Open enlarged (1:1) — never shrink-to-fit on open, or a big tree collapses
  // into unreadable lines. The "fit" button is there when the user wants it.
  const [zoom, setZoom] = useState(1)

  const posRef = useRef(pos)
  const sizeRef = useRef(size)
  const zoomRef = useRef(zoom)
  posRef.current = pos
  sizeRef.current = size
  zoomRef.current = zoom

  const dragRef = useRef<{ mx: number; my: number; px: number; py: number } | null>(null)
  const resizeRef = useRef<{ mx: number; my: number; w: number; h: number } | null>(null)
  const bodyRef = useRef<HTMLDivElement>(null)
  const didFit = useRef(false)
  // Set by a cursor-anchored wheel zoom; consumed after re-render to keep the
  // point under the pointer stationary (see the wheel + layout effects below).
  const pendingZoomRef = useRef<{ wx: number; wy: number; vx: number; vy: number } | null>(null)

  // Full branch tree (all branches). ?mode=tree gives parent_id edges straight
  // from the server; refetch when the tree SHAPE can change. We key on a signature
  // of the settled active-path messages (ids + branch counts), NOT just their
  // count: a RETRY swaps the active leaf for a NEW sibling, which keeps the path
  // length identical — so a count-based trigger would never refetch and the
  // outline would show a stale single branch (§ branch tree). Gated on "not
  // streaming" so we don't refetch per token.
  const convId = conversation.id
  const treeSig = useMemo(
    () =>
      conversation.messages
        .filter((m) => !m.streaming)
        .map((m) => `${m.id}:${m.branchCount ?? 1}`)
        .join('|'),
    [conversation.messages],
  )
  const [treeMsgs, setTreeMsgs] = useState<Message[] | null>(null)
  // Bumped to force a tree refetch when the fetched tree looks INCOMPLETE (see
  // the self-heal effect below) — e.g. a fetch that raced ahead of a
  // just-persisted retry sibling, or one that failed/returned partial data.
  const [refetchNonce, setRefetchNonce] = useState(0)
  const retriesRef = useRef(0)
  // A structural change (new/switched branch) earns a fresh self-heal budget.
  useEffect(() => {
    retriesRef.current = 0
  }, [convId, treeSig])
  useEffect(() => {
    let alive = true
    conversationsApi
      .messages(convId, 'tree')
      .then((rows) => alive && setTreeMsgs(rows.map(toLocalMessage)))
      // Keep the last good tree on a failed/partial fetch — nulling it would
      // collapse the view to the single active-path branch (§ branch tree).
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [convId, treeSig, refetchNonce])

  // The active path always ends at the current leaf, and its `< n/m >` switcher
  // knows the TRUE sibling count (DB-backed branch_count) even when a fresh retry
  // sibling hasn't landed in the tree fetch yet. If any settled active-path node
  // declares more same-parent siblings than the fetched tree holds, the tree is
  // stale/incomplete — refetch (a few times, backing off) so the sibling branches
  // actually render instead of a lone spine (the reported bug). Keyed on treeMsgs
  // (a fresh array each fetch) so a still-incomplete refetch schedules the next
  // retry rather than stalling on an unchanged boolean.
  useEffect(() => {
    if (retriesRef.current >= 4) return
    const present = new Map<string, number>()
    for (const m of treeMsgs ?? []) {
      const key = `${m.parentId ?? ''}|${m.role}`
      present.set(key, (present.get(key) ?? 0) + 1)
    }
    const incomplete = conversation.messages.some((m) => {
      if (m.streaming || !m.branchCount || m.branchCount <= 1) return false
      const key = `${m.parentId ?? ''}|${m.role}`
      return (present.get(key) ?? 0) < m.branchCount
    })
    if (!incomplete) return
    retriesRef.current += 1
    const timer = setTimeout(() => setRefetchNonce((n) => n + 1), treeRefetchBackoffMs * retriesRef.current)
    return () => clearTimeout(timer)
  }, [treeMsgs, conversation.messages])

  const activeIds = useMemo(() => new Set(conversation.messages.map((m) => m.id)), [conversation.messages])
  const activeLeafId = useMemo(() => {
    const path = conversation.messages
    return path.length ? path[path.length - 1].id : undefined
  }, [conversation.messages])

  // The fetched tree carries the full parent_id edges (all branches); fall back
  // to the active path only until the first fetch resolves. The self-heal above
  // keeps `treeMsgs` complete once it exists, so this no longer strands siblings.
  const { nodes, edges, width, height } = useMemo(
    () => layoutTree(buildMessageTree(treeMsgs ?? conversation.messages), activeIds),
    [treeMsgs, conversation.messages, activeIds],
  )

  // Trackpad / mouse wheel. A pinch (trackpad) or ⌘/Ctrl+wheel zooms centered on
  // the cursor; a plain two-finger swipe falls through to native scroll (pan). A
  // non-passive native listener is required — React's onWheel is passive, so
  // preventDefault (needed to stop the browser page-zoom) is ignored there.
  useEffect(() => {
    const el = bodyRef.current
    if (!el) return
    function onWheel(e: WheelEvent) {
      if (!(e.ctrlKey || e.metaKey)) return // plain wheel → let the canvas scroll (pan)
      e.preventDefault()
      const container = bodyRef.current
      if (!container) return
      const z = zoomRef.current
      const nz = Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, z * Math.exp(-e.deltaY * 0.0016)))
      if (Math.abs(nz - z) < 0.0005) return
      const rect = container.getBoundingClientRect()
      const vx = e.clientX - rect.left
      const vy = e.clientY - rect.top
      // Undo the margin-auto centering offset so we resolve the true world point.
      const offX = Math.max(0, (container.clientWidth - width * z) / 2)
      const offY = Math.max(0, (container.clientHeight - height * z) / 2)
      pendingZoomRef.current = {
        wx: (container.scrollLeft + vx - offX) / z,
        wy: (container.scrollTop + vy - offY) / z,
        vx,
        vy,
      }
      setZoom(parseFloat(nz.toFixed(3)))
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [width, height])

  // After a cursor-anchored zoom re-renders (the canvas resizes), re-scroll so the
  // point that was under the pointer stays there — the natural pinch-zoom feel.
  useLayoutEffect(() => {
    const p = pendingZoomRef.current
    const el = bodyRef.current
    if (!p || !el) return
    pendingZoomRef.current = null
    const offX = Math.max(0, (el.clientWidth - width * zoom) / 2)
    const offY = Math.max(0, (el.clientHeight - height * zoom) / 2)
    el.scrollLeft = p.wx * zoom + offX - p.vx
    el.scrollTop = p.wy * zoom + offY - p.vy
  }, [zoom, width, height])

  // Fit the whole graph into the panel — on demand (the "fit" button). Once it
  // fits, the margin-auto centering keeps the fitted graph centered.
  function fit() {
    const el = bodyRef.current
    const bw = el ? el.clientWidth : sizeRef.current.w - 8
    const bh = el ? el.clientHeight : sizeRef.current.h - 48
    const z = Math.min((bw - 32) / width, (bh - 32) / height, 1)
    setZoom(Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, z)))
  }

  // Scroll the canvas so a node sits in the middle of the viewport. offX/offY
  // mirror the margin-auto offset when the graph is smaller than the viewport;
  // the browser clamps the scroll to valid bounds.
  function centerOnNode(id?: string) {
    const el = bodyRef.current
    if (!el) return
    const z = zoomRef.current
    const target = (id ? nodes.find((n) => n.msg.id === id) : undefined) ?? nodes[nodes.length - 1]
    if (!target) return
    const offX = Math.max(0, (el.clientWidth - width * z) / 2)
    const offY = Math.max(0, (el.clientHeight - height * z) / 2)
    el.scrollLeft = offX + target.cx * z - el.clientWidth / 2
    el.scrollTop = offY + target.cy * z - el.clientHeight / 2
  }

  // On open: fit the WHOLE tree into view so every branch is visible at once (a
  // mind-map, not just the active spine) — then center on the current leaf. Fit
  // floors at ZOOM_MIN, so a huge tree stays navigable via zoom/pan rather than
  // shrinking to threads.
  //
  // Wait for the async branch tree (`treeMsgs`) to land before latching: on mount
  // it's null, so `nodes` is laid out from the active-path fallback — a single
  // spine — and fitting to THAT then latching would strand the sibling branches
  // off-screen (the reported "tree still wrong"). If the fetch never resolves the
  // fallback spine is all there is, so don't block forever on it.
  useEffect(() => {
    if (didFit.current || treeMsgs === null || nodes.length === 0) return
    didFit.current = true
    requestAnimationFrame(() => {
      fit()
      requestAnimationFrame(() => centerOnNode(activeLeafId))
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes.length, treeMsgs])

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
    <>
      {isPhone ? (
        <div
          className="fixed inset-0 z-[199] bg-[var(--color-overlay)] backdrop-blur-[2px] animate-[fade-in_200ms_var(--ease-out)]"
          onClick={onClose}
          aria-hidden
        />
      ) : null}
      <div
        style={
          isPhone
            ? { left: 0, bottom: 0, width: '100%', height: '80dvh' }
            : { left: pos.x, top: pos.y, width: size.w, height: size.h }
        }
        className={cn(
          'fixed z-[200] flex flex-col border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)] select-none overflow-hidden',
          isPhone
            ? 'rounded-t-[20px] pb-[var(--safe-bottom)] animate-[sheet-in-b_280ms_var(--ease-out)]'
            : 'rounded-[12px]',
        )}
      >
      {/* Header — a drag handle on desktop; a static title bar on phone */}
      <div
        onMouseDown={isPhone ? undefined : onDragDown}
        className={cn(
          'flex items-center gap-2 px-3 py-2 border-b border-[var(--color-divider)] shrink-0 bg-[var(--color-surface)]',
          isPhone ? '' : 'cursor-grab active:cursor-grabbing',
        )}
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
            className="inline-flex items-center justify-center size-6 max-sm:size-9 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <Maximize2 size={11} aria-hidden />
          </button>
          <button
            type="button"
            onClick={() => setZoom((z) => parseFloat(Math.max(ZOOM_MIN, z - STEP).toFixed(3)))}
            disabled={!canZoomOut}
            aria-label={t('outline.zoomOut', { defaultValue: 'Zoom out' })}
            className="inline-flex items-center justify-center size-6 max-sm:size-9 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-35 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <ZoomOut size={11} aria-hidden />
          </button>
          <button
            type="button"
            onClick={() => setZoom((z) => parseFloat(Math.min(ZOOM_MAX, z + STEP).toFixed(3)))}
            disabled={!canZoomIn}
            aria-label={t('outline.zoomIn', { defaultValue: 'Zoom in' })}
            className="inline-flex items-center justify-center size-6 max-sm:size-9 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-35 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <ZoomIn size={11} aria-hidden />
          </button>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('outline.close', { defaultValue: 'Close outline' })}
            className="inline-flex items-center justify-center size-6 max-sm:size-9 rounded-[5px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
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
          <div
            className="flex"
            style={{
              width: width * zoom,
              height: height * zoom,
              minWidth: '100%',
              minHeight: '100%',
              // Dotted graph-paper canvas — scales & pans with the content.
              backgroundImage:
                'radial-gradient(circle, color-mix(in oklch, var(--color-fg) 9%, transparent) 1px, transparent 1px)',
              backgroundSize: `${GRID * zoom}px ${GRID * zoom}px`,
            }}
          >
            <div style={{ width: width * zoom, height: height * zoom, margin: 'auto' }}>
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
                      'absolute flex flex-col gap-1.5 rounded-[12px] border px-3 py-2.5 text-left overflow-hidden transition-colors',
                      active
                        ? 'border-[var(--color-border-strong)] bg-[var(--color-surface)]'
                        : 'border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] opacity-80',
                      current && 'ring-1 ring-[var(--color-accent)] border-[var(--color-accent)]',
                      'hover:border-[var(--color-fg-subtle)] hover:opacity-100 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    )}
                  >
                    <span className="flex items-center gap-2 shrink-0">
                      {isUser ? (
                        // §workspaces: a shared conversation's question nodes carry
                        // their AUTHOR — show that identity, not the viewer's.
                        <Avatar size="xs">
                          {(n.msg.authorAvatar ?? avatarUrl) ? (
                            <AvatarImage
                              src={n.msg.authorAvatar || avatarUrl || ''}
                              alt={n.msg.authorName || displayName}
                            />
                          ) : null}
                          <AvatarFallback>{initials(n.msg.authorName || displayName)}</AvatarFallback>
                        </Avatar>
                      ) : (
                        <ModelIcon icon={model?.icon} size={17} />
                      )}
                      <span className="truncate text-[12px] font-semibold text-[var(--color-fg)]">
                        {isUser ? n.msg.authorName || displayName : model?.label || n.msg.modelLabel || t('assistant')}
                      </span>
                    </span>
                    <span
                      className="text-[11.5px] leading-snug text-[var(--color-fg-muted)]"
                      style={{ display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}
                    >
                      {n.msg.content || t('outline.emptyMessage', { defaultValue: '(empty)' })}
                    </span>
                  </button>
                )
              })}
            </div>
            </div>
          </div>
        )}
      </div>

      {/* Resize handle — desktop only (the phone sheet is fixed height) */}
      {!isPhone && (
        <div
          onMouseDown={onResizeDown}
          aria-hidden
          className="absolute bottom-0 right-0 size-5 cursor-nwse-resize flex items-center justify-center opacity-40 hover:opacity-80 transition-opacity"
        >
          <GripHorizontal size={10} className="rotate-45 text-[var(--color-fg-subtle)]" />
        </div>
      )}
      </div>
    </>
  )
}
