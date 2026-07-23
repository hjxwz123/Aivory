export interface ChatRouteKeys {
  /** Coarse key used only for the section-change animation. */
  section: string
  /** Exact content identity used to reset the Suspense boundary on navigation. */
  content: string
}

/**
 * Keep chat-thread navigation visually quiet while still giving every target
 * route its own Suspense boundary. A fresh boundary is important with React
 * transition updates: without it, React keeps the previous page visible until
 * the next lazy module resolves, which makes a completed Link click look inert.
 */
export function chatRouteKeys(pathname: string): ChatRouteKeys {
  const section = pathname === '/' || pathname.startsWith('/chat')
    ? 'chat'
    : pathname.split('/')[1] || 'chat'

  return {
    section,
    // The caller intentionally passes pathname only. Query-only changes
    // (message search jumps, filters, draw mode) don't load another route chunk
    // and must not remount a page's local UI state.
    content: pathname,
  }
}
