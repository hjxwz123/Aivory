// @vitest-environment jsdom
import { act, createElement, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AdminTable, type AdminTableColumn } from '@/components/admin/AdminTable'
import { AdminSortableList } from '@/components/admin/AdminSortableList'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/ui/tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))
vi.mock('framer-motion', async (importOriginal) => ({
  ...await importOriginal<typeof import('framer-motion')>(),
  useReducedMotion: () => true,
}))

type Item = { id: string; name: string }
const initial: Item[] = [{ id: 'a', name: 'Alpha' }, { id: 'b', name: 'Beta' }, { id: 'c', name: 'Gamma' }]
const columns: AdminTableColumn<Item>[] = [
  { id: 'name', header: 'Name', width: 260, render: (item) => item.name },
  { id: 'actions', header: 'Actions', width: 100, align: 'right', render: (item) => createElement('button', { type: 'button' }, item.id) },
]
let root: Root
let container: HTMLDivElement

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

function mountSortable(commit = vi.fn()) {
  function Harness() {
    const [items, setItems] = useState(initial)
    return createElement(AdminSortableList<Item>, {
      items, onItemsChange: setItems, onOrderCommit: commit, columns,
      tableLabel: 'Inventory', dragHandleLabel: 'Drag', moveUpLabel: 'Up', moveDownLabel: 'Down',
    })
  }
  act(() => root.render(createElement(Harness)))
  return commit
}

function rowNames() {
  return [...container.querySelectorAll('tbody tr')].map((row) => row.querySelector('[data-column="name"]')?.textContent)
}

function pointer(target: EventTarget, type: string, y: number, buttons = 1) {
  const event = new Event(type, { bubbles: true, cancelable: true })
  Object.assign(event, { pointerId: 1, pointerType: 'mouse', clientX: 20, clientY: y, button: 0, buttons })
  act(() => target.dispatchEvent(event))
}

describe('shared admin tables', () => {
  it('renders matching semantic headers and cells, with an accessible table name', () => {
    mountSortable()
    const table = container.querySelector('table')!
    expect(table.getAttribute('aria-label')).toBe('Inventory')
    expect([...table.querySelectorAll('th')].map((th) => th.textContent)).toEqual(['common.order', 'Name', 'Actions'])
    expect([...table.querySelectorAll('th')].every((th) => th.scope === 'col')).toBe(true)
    expect([...table.querySelectorAll('tbody tr')].every((row) => row.children.length === 3)).toBe(true)
    expect(table.querySelector('td[data-column="actions"]')?.getAttribute('data-align')).toBe('right')
  })

  it('moves rows by keyboard-accessible buttons and commits the previous and next order', () => {
    const commit = mountSortable()
    const first = container.querySelector('tbody tr')!
    expect(first.querySelector<HTMLButtonElement>('[aria-label="Up"]')!.disabled).toBe(true)
    act(() => first.querySelector<HTMLButtonElement>('[aria-label="Down"]')!.click())
    expect(rowNames()).toEqual(['Beta', 'Alpha', 'Gamma'])
    expect(commit).toHaveBeenCalledTimes(1)
    expect(commit.mock.calls[0].map((items: Item[]) => items.map((item) => item.id))).toEqual([['b', 'a', 'c'], ['a', 'b', 'c']])
    expect(container.querySelector('tbody tr:last-child [aria-label="Down"]')?.hasAttribute('disabled')).toBe(true)
  })

  it('keeps a semantic drag overlay and commits pointer reordering once', () => {
    const commit = mountSortable()
    container.querySelectorAll('tbody tr').forEach((row, index) => {
      row.getBoundingClientRect = () => ({ top: 40 + index * 60, left: 0, width: 436, height: 60, bottom: 100 + index * 60, right: 436, x: 0, y: 40 + index * 60, toJSON: () => ({}) })
    })
    pointer(container.querySelector('[aria-label="Drag"]')!, 'pointerdown', 70)
    expect(container.querySelector('.admin-table-drag-overlay table tbody tr')).not.toBeNull()
    pointer(window, 'pointermove', 170)
    pointer(window, 'pointerup', 170, 0)
    expect(rowNames()).toEqual(['Beta', 'Gamma', 'Alpha'])
    expect(container.querySelector('.admin-table-drag-overlay')).toBeNull()
    expect(commit).toHaveBeenCalledTimes(1)
  })

  it('keeps headers and spans all columns for an empty result', () => {
    act(() => root.render(createElement(AdminTable<Item>, { items: [], columns, rowKey: (item) => item.id, emptyMessage: 'No matches' })))
    expect(container.querySelectorAll('thead th')).toHaveLength(2)
    expect(container.querySelector('tbody td')?.getAttribute('colspan')).toBe('2')
    expect(container.querySelector('tbody')?.textContent).toBe('No matches')
  })

  it('preserves the list layout used by editable parameter and tool forms', () => {
    act(() => root.render(createElement(AdminSortableList<Item>, {
      items: initial, onItemsChange: vi.fn(), renderItem: (item) => createElement('input', { defaultValue: item.name }),
      rowClassName: 'grid', dragHandleLabel: 'Drag', moveUpLabel: 'Up', moveDownLabel: 'Down',
    })))
    expect(container.querySelector('table')).toBeNull()
    expect(container.querySelectorAll('ul > li input')).toHaveLength(3)
  })
})
