import ChatHome from '@/pages/chat/ChatHome'
import ChatThread from '@/pages/chat/ChatThread'

interface ChatRouteProps {
  page: 'home' | 'thread'
}

/**
 * Home and thread intentionally share one lazy route module. Once the home is
 * visible, the thread component is already loaded, so a first send cannot flash
 * the content-panel loading fallback while switching to the optimistic route.
 */
export default function ChatRoute({ page }: ChatRouteProps) {
  return page === 'thread' ? <ChatThread /> : <ChatHome />
}
