import type { Message, MessageFeedbackInput } from '@/types/chat'

export const MAX_FEEDBACK_COMMENT_LENGTH = 500

/** Match Go's rune-based validation instead of JavaScript's UTF-16 length. */
export function feedbackCommentLength(value: string): number {
  return Array.from(value).length
}

export function truncateFeedbackComment(
  value: string,
  max = MAX_FEEDBACK_COMMENT_LENGTH,
): string {
  return Array.from(value).slice(0, max).join('')
}

type FeedbackMessageIdentity = Pick<Message, 'id' | 'parentId' | 'role'>

/**
 * Editing an assistant invalidates feedback on that reply. Editing a user
 * question invalidates feedback on each direct assistant answer, but not on
 * later turns or unrelated sibling branches.
 */
export function feedbackInvalidationTargetsAfterEdit(
  messages: readonly FeedbackMessageIdentity[],
  editedMessageId: string,
): string[] {
  const edited = messages.find((message) => message.id === editedMessageId)
  if (!edited) return []
  if (edited.role === 'assistant') return [edited.id]
  if (edited.role !== 'user') return []

  return messages
    .filter((message) => message.role === 'assistant' && message.parentId === edited.id)
    .map((message) => message.id)
}

export type MessageFeedbackState = Pick<
  Message,
  'liked' | 'disliked' | 'feedbackReasons' | 'feedbackComment'
>

/**
 * Apply one feedback mutation to local message state. Rating changes away from
 * dislike always clear its optional detail, while a detail-only dislike update
 * can leave an omitted field unchanged.
 */
export function applyMessageFeedback(
  current: MessageFeedbackState,
  input: MessageFeedbackInput,
): MessageFeedbackState {
  const disliked = input.feedback === 'dislike'
  const keepExistingDetail = disliked && current.disliked === true

  return {
    liked: input.feedback === 'like',
    disliked,
    feedbackReasons: disliked
      ? input.reasons ?? (keepExistingDetail ? current.feedbackReasons ?? [] : [])
      : [],
    feedbackComment: disliked
      ? input.comment ?? (keepExistingDetail ? current.feedbackComment ?? '' : '')
      : '',
  }
}
