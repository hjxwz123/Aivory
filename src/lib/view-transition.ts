interface ViewTransitionHandle {
  finished: Promise<void>
  updateCallbackDone: Promise<void>
}

type DocumentWithViewTransition = Document & {
  startViewTransition?: (update: () => void) => ViewTransitionHandle
}

/**
 * Apply a synchronous DOM update inside a View Transition tagged with `kind`.
 * The tag lands on <html data-view-transition> for the transition's lifetime,
 * so CSS can name only the elements that should morph for that change.
 *
 * Resolves once the update has been applied. Browsers without View
 * Transitions, reduced-motion users and hidden tabs get the plain update
 * immediately — the same state change, without the animation.
 */
export function runViewTransition(kind: string, update: () => void): Promise<void> {
  const doc = typeof document === 'undefined' ? undefined : (document as DocumentWithViewTransition)
  const reducedMotion = typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches
  if (!doc || typeof doc.startViewTransition !== 'function' || reducedMotion || doc.visibilityState === 'hidden') {
    update()
    return Promise.resolve()
  }

  const root = doc.documentElement
  root.dataset.viewTransition = kind
  let transition: ViewTransitionHandle
  try {
    transition = doc.startViewTransition(update)
  } catch {
    delete root.dataset.viewTransition
    update()
    return Promise.resolve()
  }
  void transition.finished
    .catch(() => undefined)
    .finally(() => {
      if (root.dataset.viewTransition === kind) delete root.dataset.viewTransition
    })
  return transition.updateCallbackDone.catch(() => undefined)
}
