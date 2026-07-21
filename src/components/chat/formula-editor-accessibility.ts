import type { MathfieldElement } from 'mathlive'

export interface FormulaMathfieldAccessibilityState {
  locale: string
  inputLabel: string
  menuLabel: string
  keyboardLabel: string
  error: string
  errorId: string
}

function dispatchMathLiveToggleActivation(toggle: HTMLElement): void {
  const view = toggle.ownerDocument.defaultView as (Window & typeof globalThis & {
    PointerEvent?: typeof PointerEvent
  }) | null
  const PointerEventConstructor = view?.PointerEvent
  const init = { bubbles: true, cancelable: true, composed: true }
  const activationEvent = PointerEventConstructor
    ? new PointerEventConstructor('pointerdown', init)
    : view
      ? new view.MouseEvent('mousedown', init)
      : new Event('mousedown', init)
  toggle.dispatchEvent(activationEvent)
}

export function installFormulaMathfieldAccessibility(
  mathfield: MathfieldElement,
  getState: () => FormulaMathfieldAccessibilityState,
) {
  const configuredToggles = new Map<HTMLElement, EventListener>()
  let focusFrame: number | undefined

  const configureToggle = (part: 'menu-toggle' | 'virtual-keyboard-toggle', label: string) => {
    const toggle = mathfield.shadowRoot?.querySelector<HTMLElement>(`[part="${part}"]`)
    if (!toggle) return
    toggle.tabIndex = 0
    toggle.setAttribute('aria-label', label)
    toggle.querySelector('svg')?.setAttribute('aria-hidden', 'true')
    if (configuredToggles.has(toggle)) return

    const onKeyDown: EventListener = (event) => {
      const keyboardEvent = event as KeyboardEvent
      if (!['Enter', ' ', 'Spacebar'].includes(keyboardEvent.key)) return
      event.preventDefault()
      event.stopPropagation()
      if (!keyboardEvent.repeat) dispatchMathLiveToggleActivation(toggle)
    }
    toggle.addEventListener('keydown', onKeyDown)
    configuredToggles.set(toggle, onKeyDown)
  }

  const sync = () => {
    const root = mathfield.shadowRoot
    const sink = root?.querySelector<HTMLElement>('[part="keyboard-sink"]')
    if (!root || !sink) return
    const state = getState()
    let errorMirror = root.querySelector<HTMLElement>('[data-aivory-formula-error]')
    if (!errorMirror) {
      errorMirror = mathfield.ownerDocument.createElement('span')
      errorMirror.className = 'ML__sr-only'
      errorMirror.setAttribute('data-aivory-formula-error', '')
      root.append(errorMirror)
    }

    const shadowErrorId = `${state.errorId}-mathlive`
    errorMirror.id = shadowErrorId
    errorMirror.textContent = state.error
    sink.setAttribute('lang', state.locale)
    sink.setAttribute('aria-label', state.inputLabel)
    if (state.error) {
      sink.setAttribute('aria-invalid', 'true')
      sink.setAttribute('aria-describedby', shadowErrorId)
      sink.setAttribute('aria-errormessage', shadowErrorId)
    } else {
      sink.removeAttribute('aria-invalid')
      sink.removeAttribute('aria-describedby')
      sink.removeAttribute('aria-errormessage')
    }

    configureToggle('menu-toggle', state.menuLabel)
    configureToggle('virtual-keyboard-toggle', state.keyboardLabel)
  }

  const syncAfterFocus: EventListener = () => {
    sync()
    const view = mathfield.ownerDocument.defaultView
    if (!view) return
    if (focusFrame !== undefined) view.cancelAnimationFrame(focusFrame)
    focusFrame = view.requestAnimationFrame(sync)
  }

  mathfield.addEventListener('focus', syncAfterFocus, true)
  sync()

  return {
    sync,
    dispose: () => {
      mathfield.removeEventListener('focus', syncAfterFocus, true)
      const view = mathfield.ownerDocument.defaultView
      if (view && focusFrame !== undefined) view.cancelAnimationFrame(focusFrame)
      configuredToggles.forEach((listener, toggle) => {
        toggle.removeEventListener('keydown', listener)
      })
      configuredToggles.clear()
    },
  }
}
