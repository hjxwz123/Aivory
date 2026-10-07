import { useEffect, useRef, useState, type ReactNode } from 'react'
import { motion, useReducedMotion } from 'framer-motion'
import { ArrowDown, ArrowUp, GripVertical } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Tooltip } from '@/components/ui/tooltip'
import { AdminTable, type AdminTableColumn } from './AdminTable'
import { duration, easing, zIndex } from '@/lib/design-tokens'
import { cn } from '@/lib/utils'

interface SortableItem {
  id: string
}

interface DragState {
  id: string
  pointerId: number
  startX: number
  startY: number
  x: number
  y: number
  left: number
  top: number
  width: number
  height: number
}

interface AdminSortableListProps<T extends SortableItem> {
  items: T[]
  onItemsChange: (items: T[]) => void
  onOrderCommit?: (next: T[], prev: T[]) => void
  renderItem?: (item: T, index: number) => ReactNode
  rowClassName?: string
  columns?: AdminTableColumn<T>[]
  tableLabel?: string
  emptyMessage?: ReactNode
  dragHandleLabel: string
  moveUpLabel: string
  moveDownLabel: string
  listClassName?: string
  /** Keep touch reordering compact on narrow screens; drag remains available. */
  mobileDragOnly?: boolean
}

/**
 * Shared admin sort list. Uses pointer capture instead of native HTML5 drag so
 * the lifted row follows the cursor while the real list keeps an empty slot.
 */
export function AdminSortableList<T extends SortableItem>({
  items,
  onItemsChange,
  onOrderCommit,
  renderItem,
  rowClassName,
  columns,
  tableLabel,
  emptyMessage,
  dragHandleLabel,
  moveUpLabel,
  moveDownLabel,
  listClassName,
  mobileDragOnly = false,
}: AdminSortableListProps<T>) {
  const { t } = useTranslation('admin')
  const reduceMotion = useReducedMotion()
  const itemsRef = useRef(items)
  const dragStartItems = useRef<T[] | null>(null)
  const rowRefs = useRef(new Map<string, HTMLElement>())
  const dragRef = useRef<DragState | null>(null)
  const [drag, setDrag] = useState<DragState | null>(null)

  useEffect(() => {
    itemsRef.current = items
  }, [items])

  useEffect(() => {
    if (!drag) return undefined

    function handlePointerMove(e: PointerEvent) {
      if (e.pointerType === 'mouse' && e.buttons === 0) {
        finishDrag(e.pointerId)
        return
      }
      updateDragFromPointer(e.pointerId, e.clientX, e.clientY)
      e.preventDefault()
    }

    function handlePointerEnd(e: PointerEvent) {
      finishDrag(e.pointerId)
    }

    window.addEventListener('pointermove', handlePointerMove, { passive: false })
    window.addEventListener('pointerup', handlePointerEnd)
    window.addEventListener('pointercancel', handlePointerEnd)
    window.addEventListener('mouseup', cancelDrag)
    window.addEventListener('blur', cancelDrag)

    return () => {
      window.removeEventListener('pointermove', handlePointerMove)
      window.removeEventListener('pointerup', handlePointerEnd)
      window.removeEventListener('pointercancel', handlePointerEnd)
      window.removeEventListener('mouseup', cancelDrag)
      window.removeEventListener('blur', cancelDrag)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [drag?.pointerId])

  function setDragState(next: DragState | null) {
    dragRef.current = next
    setDrag(next)
  }

  function setRowRef(id: string, node: HTMLElement | null) {
    if (node) rowRefs.current.set(id, node)
    else rowRefs.current.delete(id)
  }

  function moveItem(from: number, to: number) {
    if (from === to || from < 0 || to < 0 || to >= itemsRef.current.length) return
    const next = [...itemsRef.current]
    const [item] = next.splice(from, 1)
    next.splice(to, 0, item)
    itemsRef.current = next
    onItemsChange(next)
  }

  function indexFromClientY(clientY: number, draggingId: string): number {
    const current = itemsRef.current
    let target = current.length - 1
    for (let i = 0; i < current.length; i += 1) {
      const id = current[i].id
      const row = rowRefs.current.get(id)
      if (!row) continue
      const rect = row.getBoundingClientRect()
      const midpoint = rect.top + rect.height / 2
      if (clientY < midpoint) {
        target = i
        break
      }
    }
    const draggingIndex = current.findIndex((item) => item.id === draggingId)
    if (draggingIndex < 0) return target
    return Math.max(0, Math.min(current.length - 1, target))
  }

  function startDrag(e: React.PointerEvent<HTMLButtonElement>, item: T) {
    if (e.pointerType === 'mouse' && e.button !== 0) return
    const row = rowRefs.current.get(item.id)
    if (!row) return
    const rect = row.getBoundingClientRect()
    e.preventDefault()
    e.stopPropagation()
    try {
      e.currentTarget.setPointerCapture(e.pointerId)
    } catch {
      // The window-level pointer listeners below still complete the drag.
    }
    dragStartItems.current = itemsRef.current
    setDragState({
      id: item.id,
      pointerId: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      x: e.clientX,
      y: e.clientY,
      left: rect.left,
      top: rect.top,
      width: rect.width,
      height: rect.height,
    })
  }

  function updateDrag(e: React.PointerEvent<HTMLButtonElement>) {
    if (!dragRef.current || dragRef.current.pointerId !== e.pointerId) return
    if (e.pointerType === 'mouse' && e.buttons === 0) {
      finishDrag(e.pointerId)
      return
    }
    e.preventDefault()
    updateDragFromPointer(e.pointerId, e.clientX, e.clientY)
  }

  function updateDragFromPointer(pointerId: number, clientX: number, clientY: number) {
    const active = dragRef.current
    if (!active || active.pointerId !== pointerId) return
    const nextDrag = { ...active, x: clientX, y: clientY }
    setDragState(nextDrag)
    const from = itemsRef.current.findIndex((item) => item.id === active.id)
    const to = indexFromClientY(clientY, active.id)
    moveItem(from, to)
  }

  function finishDrag(pointerId: number) {
    const active = dragRef.current
    if (!active || active.pointerId !== pointerId) return
    const prev = dragStartItems.current
    const next = itemsRef.current
    dragStartItems.current = null
    setDragState(null)
    if (prev && prev.some((item, i) => item.id !== next[i]?.id)) {
      onOrderCommit?.(next, prev)
    }
  }

  function moveBy(index: number, dir: -1 | 1) {
    const to = index + dir
    if (to < 0 || to >= items.length) return
    const prev = items
    const next = [...items]
    const [item] = next.splice(index, 1)
    next.splice(to, 0, item)
    itemsRef.current = next
    onItemsChange(next)
    onOrderCommit?.(next, prev)
  }

  function orderControls(item: T, index: number, overlay = false) {
    return (
      <OrderControls
        index={index}
        count={itemsRef.current.length}
        overlay={overlay}
        dragHandleLabel={dragHandleLabel}
        moveUpLabel={moveUpLabel}
        moveDownLabel={moveDownLabel}
        mobileDragOnly={mobileDragOnly}
        compact={Boolean(columns)}
        onMoveBy={moveBy}
        onPointerDown={(e) => startDrag(e, item)}
        onPointerMove={updateDrag}
        onPointerUp={(e) => finishDrag(e.pointerId)}
        onPointerCancel={(e) => finishDrag(e.pointerId)}
        onLostPointerCapture={(e) => {
          if (e.pointerType !== 'mouse' || e.buttons === 0) {
            finishDrag(e.pointerId)
          }
        }}
      />
    )
  }

  function rowContent(item: T, index: number, overlay = false) {
    return (
      <>
        {orderControls(item, index, overlay)}
        {renderItem?.(item, index)}
      </>
    )
  }

  function cancelDrag() {
    const active = dragRef.current
    if (!active) return
    finishDrag(active.pointerId)
  }

  const draggedItem = drag ? itemsRef.current.find((item) => item.id === drag.id) : null
  const draggedIndex = draggedItem ? itemsRef.current.findIndex((item) => item.id === draggedItem.id) : -1

  if (columns) {
    const tableColumns = (overlay = false): AdminTableColumn<T>[] => [
      { id: 'order', header: t('common.order'), width: 76, render: (item, index) => orderControls(item, index, overlay) },
      ...columns,
    ]

    return (
      <>
        <AdminTable
          items={items}
          columns={tableColumns()}
          rowKey={(item) => item.id}
          label={tableLabel}
          emptyMessage={emptyMessage}
          className={cn(drag && 'select-none', listClassName)}
          renderRow={(item, _index, cells) => (
            <motion.tr
              key={item.id}
              ref={(node) => setRowRef(item.id, node)}
              layout={reduceMotion ? false : 'position'}
              transition={{ duration: duration.fast / 1000, ease: easing.out }}
              data-sortable-dragging={drag?.id === item.id ? 'true' : undefined}
              style={{ opacity: drag?.id === item.id ? 0 : 1 }}
            >
              {cells}
            </motion.tr>
          )}
        />
        {drag && draggedItem ? (
          <div
            aria-hidden
            inert
            className="admin-table-drag-overlay pointer-events-none fixed overflow-hidden rounded-[8px] border border-[var(--color-border-strong)] bg-[var(--color-surface)]"
            style={{
              left: drag.left,
              top: drag.top,
              width: drag.width,
              height: drag.height,
              transform: `translate3d(${drag.x - drag.startX}px, ${drag.y - drag.startY}px, 0)`,
              zIndex: zIndex.popover,
            }}
          >
            <AdminTable<T> items={[draggedItem]} columns={tableColumns(true).map((column) => ({ ...column, render: (item) => column.render(item, draggedIndex) }))} rowKey={(item) => item.id} framed={false} hideHeader />
          </div>
        ) : null}
      </>
    )
  }

  return (
    <>
      <ul
        className={cn(
          'flex min-w-0 max-w-full flex-col overflow-hidden rounded-[8px] bg-[var(--color-surface)] text-[13px]',
          drag && 'select-none',
          listClassName,
        )}
      >
        {items.map((item, index) => {
          const isDragging = drag?.id === item.id
          return (
            <motion.li
              key={item.id}
              ref={(node) => setRowRef(item.id, node)}
              layout={reduceMotion ? false : 'position'}
              transition={{ duration: duration.fast / 1000, ease: easing.out }}
              className={cn(
                'min-w-0 max-w-full hover:bg-[var(--color-bg-muted)]',
                rowClassName,
                isDragging ? 'opacity-0' : 'opacity-100',
              )}
              data-sortable-dragging={isDragging ? 'true' : undefined}
            >
              {rowContent(item, index)}
            </motion.li>
          )
        })}
      </ul>

      {drag && draggedItem ? (
        <div
          aria-hidden
          inert
          className={cn(
            'min-w-0 max-w-full',
            rowClassName,
            'pointer-events-none fixed rounded-[12px] border border-[var(--color-border-strong)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)]',
          )}
          style={{
            left: drag.left,
            top: drag.top,
            width: drag.width,
            height: drag.height,
            transform: `translate3d(${drag.x - drag.startX}px, ${drag.y - drag.startY}px, 0)`,
            zIndex: zIndex.popover,
          }}
        >
          {rowContent(draggedItem, draggedIndex, true)}
        </div>
      ) : null}
    </>
  )
}

interface OrderControlsProps {
  index: number
  count: number
  overlay: boolean
  dragHandleLabel: string
  moveUpLabel: string
  moveDownLabel: string
  mobileDragOnly: boolean
  compact: boolean
  onMoveBy: (index: number, dir: -1 | 1) => void
  onPointerDown: (e: React.PointerEvent<HTMLButtonElement>) => void
  onPointerMove: (e: React.PointerEvent<HTMLButtonElement>) => void
  onPointerUp: (e: React.PointerEvent<HTMLButtonElement>) => void
  onPointerCancel: (e: React.PointerEvent<HTMLButtonElement>) => void
  onLostPointerCapture: (e: React.PointerEvent<HTMLButtonElement>) => void
}

function OrderControls({
  index,
  count,
  overlay,
  dragHandleLabel,
  moveUpLabel,
  moveDownLabel,
  mobileDragOnly,
  compact,
  onMoveBy,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  onPointerCancel,
  onLostPointerCapture,
}: OrderControlsProps) {
  if (compact) {
    const buttonClass = 'inline-flex items-center justify-center rounded text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:pointer-events-none disabled:opacity-25 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]'
    return (
      <div className="admin-table-order-controls">
        {overlay ? <span className="inline-flex size-7 items-center justify-center"><GripVertical size={14} aria-hidden /></span> : (
          <Tooltip content={dragHandleLabel}>
            <button
              type="button"
              aria-label={dragHandleLabel}
              className={cn(buttonClass, 'size-7 touch-none cursor-grab active:cursor-grabbing')}
              onPointerDown={onPointerDown}
              onPointerMove={onPointerMove}
              onPointerUp={onPointerUp}
              onPointerCancel={onPointerCancel}
              onLostPointerCapture={onLostPointerCapture}
            ><GripVertical size={14} aria-hidden /></button>
          </Tooltip>
        )}
        <div className="flex flex-col">
          {([-1, 1] as const).map((dir) => {
            const Icon = dir === -1 ? ArrowUp : ArrowDown
            const label = dir === -1 ? moveUpLabel : moveDownLabel
            return overlay ? <span key={dir} className="inline-flex size-5 items-center justify-center"><Icon size={12} aria-hidden /></span> : (
              <Tooltip key={dir} content={label}>
                <button
                  type="button"
                  aria-label={label}
                  disabled={dir === -1 ? index === 0 : index === count - 1}
                  onClick={() => onMoveBy(index, dir)}
                  className={cn(buttonClass, 'size-5')}
                ><Icon size={12} aria-hidden /></button>
              </Tooltip>
            )
          })}
        </div>
      </div>
    )
  }
  if (overlay) {
    return (
      <>
        <span
          className={cn(
            'inline-flex size-7 items-center justify-center rounded text-[var(--color-fg-muted)]',
            mobileDragOnly && 'max-md:size-11',
          )}
        >
          <GripVertical size={15} strokeWidth={1.5} aria-hidden />
        </span>
        <div className={cn('flex flex-col gap-0.5', mobileDragOnly && 'max-md:hidden')}>
          <span className="inline-flex size-6 items-center justify-center rounded text-[var(--color-fg-subtle)]">
            <ArrowUp size={14} strokeWidth={1.5} aria-hidden />
          </span>
          <span className="inline-flex size-6 items-center justify-center rounded text-[var(--color-fg-subtle)]">
            <ArrowDown size={14} strokeWidth={1.5} aria-hidden />
          </span>
        </div>
      </>
    )
  }

  return (
    <>
      <button
        type="button"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
        onLostPointerCapture={onLostPointerCapture}
        aria-label={dragHandleLabel}
        className={cn(
          'inline-flex size-7 touch-none items-center justify-center rounded text-[var(--color-fg-faint)] cursor-grab active:cursor-grabbing hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg-muted)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          mobileDragOnly && 'max-md:size-11',
        )}
      >
        <GripVertical size={15} strokeWidth={1.5} aria-hidden />
      </button>
      <div className={cn('flex flex-col gap-0.5', mobileDragOnly && 'max-md:hidden')}>
        <button
          type="button"
          className="inline-flex size-6 items-center justify-center rounded text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:pointer-events-none disabled:opacity-30 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          disabled={index === 0}
          onClick={() => onMoveBy(index, -1)}
          aria-label={moveUpLabel}
        >
          <ArrowUp size={14} strokeWidth={1.5} aria-hidden />
        </button>
        <button
          type="button"
          className="inline-flex size-6 items-center justify-center rounded text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:pointer-events-none disabled:opacity-30 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          disabled={index === count - 1}
          onClick={() => onMoveBy(index, 1)}
          aria-label={moveDownLabel}
        >
          <ArrowDown size={14} strokeWidth={1.5} aria-hidden />
        </button>
      </div>
    </>
  )
}
