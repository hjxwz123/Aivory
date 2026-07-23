import { describe, expect, it } from 'vitest'
import { chatRouteKeys } from './chat-route'

describe('chatRouteKeys', () => {
  it('keeps home and chat threads in one animation section', () => {
    expect(chatRouteKeys('/').section).toBe('chat')
    expect(chatRouteKeys('/chat').section).toBe('chat')
    expect(chatRouteKeys('/chat/c_123').section).toBe('chat')
  })

  it('gives list and detail destinations distinct content identities', () => {
    expect(chatRouteKeys('/projects').content).not.toBe(chatRouteKeys('/projects/p_123').content)
    expect(chatRouteKeys('/kb').content).not.toBe(chatRouteKeys('/kb/kb_123').content)
    expect(chatRouteKeys('/files').content).toBe('/files')
  })

  it('does not remount page content for query-only state changes', () => {
    const drawLocation = new URL('https://aivory.test/?mode=draw')
    const messageJumpLocation = new URL('https://aivory.test/chat/c_123?m=m_1&j=2')

    expect(chatRouteKeys(drawLocation.pathname).content).toBe('/')
    expect(chatRouteKeys(messageJumpLocation.pathname).content).toBe('/chat/c_123')
  })
})
