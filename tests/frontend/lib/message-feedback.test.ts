import { describe, expect, it } from 'vitest'
import {
  applyMessageFeedback,
  feedbackCommentLength,
  feedbackInvalidationTargetsAfterEdit,
  truncateFeedbackComment,
} from '@/lib/message-feedback'

describe('feedback comment length', () => {
  it('counts and truncates Unicode code points the same way as Go runes', () => {
    expect('A😀B'.length).toBe(4)
    expect(feedbackCommentLength('A😀B')).toBe(3)
    expect(truncateFeedbackComment('A😀B', 2)).toBe('A😀')
  })
})

describe('feedback invalidation after an in-place edit', () => {
  const messages = [
    { id: 'user_1', parentId: '', role: 'user' as const },
    { id: 'assistant_1a', parentId: 'user_1', role: 'assistant' as const },
    { id: 'assistant_1b', parentId: 'user_1', role: 'assistant' as const },
    { id: 'user_2', parentId: 'assistant_1a', role: 'user' as const },
    { id: 'assistant_2', parentId: 'user_2', role: 'assistant' as const },
  ]

  it('targets only the edited assistant reply', () => {
    expect(feedbackInvalidationTargetsAfterEdit(messages, 'assistant_1a')).toEqual(['assistant_1a'])
  })

  it('targets every direct assistant answer to an edited user question', () => {
    expect(feedbackInvalidationTargetsAfterEdit(messages, 'user_1')).toEqual([
      'assistant_1a',
      'assistant_1b',
    ])
  })

  it('does not target later turns or unknown messages', () => {
    expect(feedbackInvalidationTargetsAfterEdit(messages, 'missing')).toEqual([])
  })
})

describe('applyMessageFeedback', () => {
  it('makes ratings mutually exclusive and clears dislike detail when liked', () => {
    expect(
      applyMessageFeedback(
        {
          liked: false,
          disliked: true,
          feedbackReasons: ['incorrect_fact'],
          feedbackComment: 'The date is wrong.',
        },
        { feedback: 'like' },
      ),
    ).toEqual({
      liked: true,
      disliked: false,
      feedbackReasons: [],
      feedbackComment: '',
    })
  })

  it('starts a new dislike without stale detail from another rating', () => {
    expect(
      applyMessageFeedback(
        {
          liked: true,
          disliked: false,
          feedbackReasons: ['outdated'],
          feedbackComment: 'Stale detail',
        },
        { feedback: 'dislike' },
      ),
    ).toEqual({
      liked: false,
      disliked: true,
      feedbackReasons: [],
      feedbackComment: '',
    })
  })

  it('updates structured dislike detail and preserves an omitted field', () => {
    expect(
      applyMessageFeedback(
        {
          liked: false,
          disliked: true,
          feedbackReasons: ['not_answered'],
          feedbackComment: 'Please address the second question.',
        },
        { feedback: 'dislike', reasons: ['incomplete', 'poor_format'] },
      ),
    ).toEqual({
      liked: false,
      disliked: true,
      feedbackReasons: ['incomplete', 'poor_format'],
      feedbackComment: 'Please address the second question.',
    })
  })

  it('clears all feedback detail when the rating is removed', () => {
    expect(
      applyMessageFeedback(
        {
          liked: false,
          disliked: true,
          feedbackReasons: ['unsafe'],
          feedbackComment: 'Unsafe recommendation',
        },
        { feedback: '', reasons: [], comment: '' },
      ),
    ).toEqual({
      liked: false,
      disliked: false,
      feedbackReasons: [],
      feedbackComment: '',
    })
  })
})
