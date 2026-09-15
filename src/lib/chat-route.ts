export interface ChatRouteKeys {
  /** Coarse key used only for the section-change animation. */
  section: string
  /** Exact content identity used to reset the Suspense boundary on navigation. */
  content: string
}

export interface ChatRouteAccess {
  domainLocked: boolean
  canUsePrivateChat: boolean
}

/** Resolve chat-shell restrictions without coupling private chat to the domain lock. */
export function chatRouteAccessRedirect(pathname: string, access: ChatRouteAccess): '/' | null {
  if (access.domainLocked && pathname === '/files') return '/'
  if (!access.canUsePrivateChat && pathname === '/private-chat') return '/'
  return null
}

/**
 * Keep chat-thread navigation visually quiet while still giving every target
 * route its own Suspense boundary. A fresh boundary is important with React
 * transition updates: without it, React keeps the previous page visible until
 * the next lazy module resolves, which makes a completed Link click look inert.
 */
export function chatRouteKeys(pathname: string): ChatRouteKeys {
  const section = pathname === '/' || pathname === '/private-chat' || pathname.startsWith('/chat')
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
