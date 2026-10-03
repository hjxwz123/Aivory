// @vitest-environment jsdom
import { act, createElement, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CodeBlockFrame } from '@/components/chat/code-block-frame'
import { useSettings } from '@/store/settings'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/ui/tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))

let root: Root
let container: HTMLDivElement

function toggle(index = 0) {
  act(() => container.querySelectorAll<HTMLButtonElement>('[aria-label="code.wrap"]')[index].click())
}

function wrapped() {
  return Array.from(container.querySelectorAll('[data-code-body]')).map((element) => element.getAttribute('data-wrap'))
}

describe('code block wrapping controls', () => {
  beforeEach(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    useSettings.setState((state) => ({ appearance: { ...state.appearance, codeBlockWrap: false }, codeBlockWrapAccountId: 'account-a', codeBlockWrapPending: null }))
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.unstubAllGlobals()
  })

  it('updates inherited blocks while preserving an explicit false override until reset', () => {
    act(() => root.render(createElement('div', null,
      createElement(CodeBlockFrame, { code: 'first', key: 'first' }),
      createElement(CodeBlockFrame, { code: 'second', key: 'second' }),
    )))
    expect(wrapped()).toEqual(['false', 'false'])
    act(() => useSettings.getState().setAppearance({ codeBlockWrap: true }))
    toggle()
    expect(wrapped()).toEqual(['false', 'true'])
    act(() => useSettings.getState().setAppearance({ codeBlockWrap: false }))
    act(() => useSettings.getState().setAppearance({ codeBlockWrap: true }))
    expect(wrapped()).toEqual(['false', 'true'])
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="code.resetWrap"]')!.click())
    expect(wrapped()).toEqual(['true', 'true'])
    expect(container.querySelector('[aria-label="code.resetWrap"]')).toBeNull()
  })

  it('preserves a selection through streaming code and final highlight updates', () => {
    act(() => root.render(createElement(CodeBlockFrame, { code: 'partial', html: 'partial' })))
    toggle()
    act(() => root.render(createElement(CodeBlockFrame, { code: 'partial completed', html: '<span>partial completed</span>' })))
    expect(wrapped()).toEqual(['true'])
    expect(container.querySelector('[data-code-body]')!.textContent).toBe('partial completed')
    expect(container.querySelector('[aria-label="code.wrap"]')!.getAttribute('aria-pressed')).toBe('true')
  })

  it('clears local choices on an account change and a new block lifecycle', () => {
    act(() => root.render(createElement(CodeBlockFrame, { code: 'first', key: 'first' })))
    toggle()
    act(() => useSettings.getState().syncUserSettings({}, 'account-b'))
    expect(wrapped()).toEqual(['false'])
    act(() => useSettings.getState().syncUserSettings({}, 'account-a'))
    expect(wrapped()).toEqual(['false'])
    toggle()
    act(() => root.render(createElement(CodeBlockFrame, { code: 'new message', key: 'new' })))
    expect(wrapped()).toEqual(['false'])
  })

  it('keeps the code original, escapes plain markup and leaves footer output outside wrapping', () => {
    const code = '\t<script>unsafe</script>\n  indented\n'
    act(() => root.render(createElement(CodeBlockFrame, { code, footer: createElement('pre', { 'data-output': true }, 'output') })))
    toggle()
    expect(container.querySelector('[data-code-body]')!.textContent).toBe(code)
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('[data-output]')!.hasAttribute('data-wrap')).toBe(false)
  })
})
