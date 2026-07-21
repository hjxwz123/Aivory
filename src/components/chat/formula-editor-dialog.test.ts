import { describe, expect, it } from 'vitest'
import type { MathfieldElement } from 'mathlive'
import {
  installFormulaMathfieldAccessibility,
  type FormulaMathfieldAccessibilityState,
} from './formula-editor-accessibility'

class TestElement extends EventTarget {
  readonly attributes = new Map<string, string>()
  readonly children: TestElement[] = []
  readonly selectors = new Map<string, TestElement>()
  ownerDocument!: TestDocument
  shadowRoot: TestElement | null = null
  className = ''
  id = ''
  tabIndex = -1
  textContent = ''

  setAttribute(name: string, value: string) {
    this.attributes.set(name, value)
    if (name === 'id') this.id = value
  }

  getAttribute(name: string) {
    return this.attributes.get(name) ?? null
  }

  removeAttribute(name: string) {
    this.attributes.delete(name)
  }

  append(child: TestElement) {
    this.children.push(child)
  }

  querySelector<T>(selector: string): T | null {
    if (selector === '[data-aivory-formula-error]') {
      return (this.children.find((child) => child.attributes.has('data-aivory-formula-error')) as T) ?? null
    }
    return (this.selectors.get(selector) as T) ?? null
  }
}

class TestDocument {
  defaultView = null

  createElement() {
    const element = new TestElement()
    element.ownerDocument = this
    return element
  }
}

class TestKeyboardEvent extends Event {
  readonly key: string
  readonly repeat: boolean

  constructor(key: string, repeat = false) {
    super('keydown', { bubbles: true, cancelable: true })
    this.key = key
    this.repeat = repeat
  }
}

function createMathfieldFixture() {
  const document = new TestDocument()
  const field = document.createElement()
  const root = document.createElement()
  const sink = document.createElement()
  const menu = document.createElement()
  const keyboard = document.createElement()
  const menuIcon = document.createElement()
  const keyboardIcon = document.createElement()

  field.shadowRoot = root
  root.selectors.set('[part="keyboard-sink"]', sink)
  root.selectors.set('[part="menu-toggle"]', menu)
  root.selectors.set('[part="virtual-keyboard-toggle"]', keyboard)
  menu.selectors.set('svg', menuIcon)
  keyboard.selectors.set('svg', keyboardIcon)

  return { field, root, sink, menu, keyboard, menuIcon, keyboardIcon }
}

describe('formula editor MathLive accessibility', () => {
  it('keeps the shadow input label and validation error synchronized', () => {
    const fixture = createMathfieldFixture()
    let state: FormulaMathfieldAccessibilityState = {
      locale: 'en-US',
      inputLabel: 'Formula',
      menuLabel: 'Open formula menu',
      keyboardLabel: 'Show or hide formula keyboard',
      error: '',
      errorId: 'formula-error',
    }
    const controller = installFormulaMathfieldAccessibility(
      fixture.field as unknown as MathfieldElement,
      () => state,
    )

    expect(fixture.sink.getAttribute('lang')).toBe('en-US')
    expect(fixture.sink.getAttribute('aria-label')).toBe('Formula')
    expect(fixture.sink.getAttribute('aria-invalid')).toBeNull()

    state = {
      ...state,
      locale: 'zh-CN',
      inputLabel: '公式',
      menuLabel: '打开公式菜单',
      keyboardLabel: '显示或隐藏公式键盘',
      error: '请输入公式。',
    }
    controller.sync()

    expect(fixture.sink.getAttribute('lang')).toBe('zh-CN')
    expect(fixture.sink.getAttribute('aria-label')).toBe('公式')
    expect(fixture.sink.getAttribute('aria-invalid')).toBe('true')
    expect(fixture.sink.getAttribute('aria-describedby')).toBe('formula-error-mathlive')
    expect(fixture.sink.getAttribute('aria-errormessage')).toBe('formula-error-mathlive')
    expect(fixture.root.children[0]?.textContent).toBe('请输入公式。')
    expect(fixture.menu.getAttribute('aria-label')).toBe('打开公式菜单')
    expect(fixture.keyboard.getAttribute('aria-label')).toBe('显示或隐藏公式键盘')

    fixture.sink.setAttribute('aria-label', 'math input field')
    fixture.field.dispatchEvent(new Event('focus'))
    expect(fixture.sink.getAttribute('aria-label')).toBe('公式')

    state = { ...state, error: '' }
    controller.sync()
    expect(fixture.sink.getAttribute('aria-invalid')).toBeNull()
    expect(fixture.sink.getAttribute('aria-describedby')).toBeNull()
    expect(fixture.sink.getAttribute('aria-errormessage')).toBeNull()
    expect(fixture.root.children[0]?.textContent).toBe('')
    controller.dispose()
  })

  it('makes both MathLive toggles keyboard-operable and removes listeners on dispose', () => {
    const fixture = createMathfieldFixture()
    let menuActivations = 0
    let keyboardActivations = 0
    fixture.menu.addEventListener('mousedown', () => { menuActivations += 1 })
    fixture.keyboard.addEventListener('mousedown', () => { keyboardActivations += 1 })

    const controller = installFormulaMathfieldAccessibility(
      fixture.field as unknown as MathfieldElement,
      () => ({
        locale: 'en-US',
        inputLabel: 'Formula',
        menuLabel: 'Open formula menu',
        keyboardLabel: 'Show or hide formula keyboard',
        error: '',
        errorId: 'formula-error',
      }),
    )

    expect(fixture.menu.tabIndex).toBe(0)
    expect(fixture.keyboard.tabIndex).toBe(0)
    expect(fixture.menu.getAttribute('aria-label')).toBe('Open formula menu')
    expect(fixture.keyboard.getAttribute('aria-label')).toBe('Show or hide formula keyboard')
    expect(fixture.menuIcon.getAttribute('aria-hidden')).toBe('true')
    expect(fixture.keyboardIcon.getAttribute('aria-hidden')).toBe('true')
    controller.sync()
    controller.sync()

    const enter = new TestKeyboardEvent('Enter')
    const space = new TestKeyboardEvent(' ')
    fixture.menu.dispatchEvent(enter)
    fixture.keyboard.dispatchEvent(space)
    expect(enter.defaultPrevented).toBe(true)
    expect(space.defaultPrevented).toBe(true)
    expect(menuActivations).toBe(1)
    expect(keyboardActivations).toBe(1)

    fixture.keyboard.dispatchEvent(new TestKeyboardEvent(' ', true))
    fixture.menu.dispatchEvent(new TestKeyboardEvent('Escape'))
    expect(menuActivations).toBe(1)
    expect(keyboardActivations).toBe(1)

    controller.dispose()
    fixture.menu.dispatchEvent(new TestKeyboardEvent('Enter'))
    fixture.keyboard.dispatchEvent(new TestKeyboardEvent(' '))
    expect(menuActivations).toBe(1)
    expect(keyboardActivations).toBe(1)
  })
})
