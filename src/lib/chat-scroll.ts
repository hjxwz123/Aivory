export const CHAT_BOTTOM_THRESHOLD = 80
export const CHAT_OVERFLOW_THRESHOLD = 200

export interface ChatScrollGeometry {
  scrollHeight: number
  scrollTop: number
  clientHeight: number
}

export interface ChatScrollState {
  atBottom: boolean
  scrolled: boolean
  showJump: boolean
  distanceFromBottom: number
}

export function chatScrollState(
  geometry: ChatScrollGeometry,
  bottomThreshold = CHAT_BOTTOM_THRESHOLD,
): ChatScrollState {
  const distanceFromBottom = Math.max(
    0,
    geometry.scrollHeight - geometry.scrollTop - geometry.clientHeight,
  )
  const atBottom = distanceFromBottom < bottomThreshold

  return {
    atBottom,
    scrolled: geometry.scrollTop > 2,
    showJump:
      !atBottom &&
      geometry.scrollHeight - geometry.clientHeight > CHAT_OVERFLOW_THRESHOLD,
    distanceFromBottom,
  }
}

/**
 * Reconcile a chat scroller after its viewport or transcript changes size.
 * Assigning scrollHeight lets the browser clamp scrollTop to the exact maximum.
 */
export function reconcileChatScroll<T extends ChatScrollGeometry>(
  target: T,
  wasFollowing: boolean,
): ChatScrollState {
  if (wasFollowing) target.scrollTop = target.scrollHeight
  return chatScrollState(target)
}
