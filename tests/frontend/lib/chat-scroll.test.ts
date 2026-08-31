import { describe, expect, it } from 'vitest'
import { chatScrollState, reconcileChatScroll } from '@/lib/chat-scroll'

describe('chat scroll geometry', () => {
  it('treats a small tail distance as bottom-following', () => {
    expect(chatScrollState({ scrollHeight: 4_000, scrollTop: 3_140, clientHeight: 800 })).toEqual({
      atBottom: true,
      scrolled: true,
      showJump: false,
      distanceFromBottom: 60,
    })
  })

  it('shows the jump control when a long transcript is genuinely away from the tail', () => {
    expect(chatScrollState({ scrollHeight: 6_000, scrollTop: 3_500, clientHeight: 900 })).toEqual({
      atBottom: false,
      scrolled: true,
      showJump: true,
      distanceFromBottom: 1_600,
    })
  })

  it('pins resize-driven reflows only while the user was following', () => {
    const following = { scrollHeight: 5_200, scrollTop: 3_900, clientHeight: 900 }
    expect(reconcileChatScroll(following, true).atBottom).toBe(true)
    expect(following.scrollTop).toBe(5_200)

    const readingOlder = { scrollHeight: 5_200, scrollTop: 3_100, clientHeight: 900 }
    expect(reconcileChatScroll(readingOlder, false).showJump).toBe(true)
    expect(readingOlder.scrollTop).toBe(3_100)
  })

  it('normalizes elastic overscroll instead of reporting a negative distance', () => {
    expect(chatScrollState({ scrollHeight: 1_000, scrollTop: 250, clientHeight: 800 })).toMatchObject({
      atBottom: true,
      showJump: false,
      distanceFromBottom: 0,
    })
  })
})
