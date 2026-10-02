/**
 * Play the one-shot "text landed here" ring on a composer shell (see
 * `.composer-fill-pulse` in globals.css). Re-triggers cleanly when called again
 * before the previous ring finished.
 */
export function pulseComposerShell(element: HTMLElement | null): void {
  if (!element) return
  element.classList.remove('composer-fill-pulse')
  // Force a style flush so re-adding the class restarts the animation.
  void element.offsetWidth
  element.classList.add('composer-fill-pulse')
  // Child animations (e.g. the send button's swap) bubble animationend too;
  // only the shell's own ring may clear the class.
  const onEnd = (event: AnimationEvent) => {
    if (event.target !== element || event.animationName !== 'composer-fill-pulse') return
    element.classList.remove('composer-fill-pulse')
    element.removeEventListener('animationend', onEnd)
  }
  element.addEventListener('animationend', onEnd)
}
