import { describe, expect, it } from 'vitest'
import { isChatShellPath } from '@/lib/app-paths'

describe('chat shell route classification', () => {
  it.each(['/', '/chat', '/chat/conversation-1', '/projects', '/files', '/skills', '/kb/item-1', '/subscription'])(
    'loads chat data for %s',
    (pathname) => {
      expect(isChatShellPath(pathname)).toBe(true)
    },
  )

  it.each(['/admin', '/admin/overview', '/login', '/privacy', '/share/conversation-1', '/welcome'])(
    'does not load chat data for %s',
    (pathname) => {
      expect(isChatShellPath(pathname)).toBe(false)
    },
  )
})
