import ChatHome from '@/pages/chat/ChatHome'
import ChatThread from '@/pages/chat/ChatThread'
import PrivateChat from '@/pages/chat/PrivateChat'

interface ChatRouteProps {
  page: 'home' | 'thread' | 'private'
}

/**
 * Home, thread and private mode intentionally share one lazy route module.
 * Once the home is visible, the other two are already loaded, so neither a
 * first send nor entering private mode can flash the content-panel loading
 * fallback while switching routes.
 */
export default function ChatRoute({ page }: ChatRouteProps) {
  if (page === 'thread') return <ChatThread />
  if (page === 'private') return <PrivateChat />
  return <ChatHome />
}
