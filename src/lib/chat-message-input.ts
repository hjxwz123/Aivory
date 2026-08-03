import type { Attachment } from '@/types/chat'
import { mathContentToPlainText } from '@/lib/math-content'

export function hasImageAttachment(attachments: readonly Attachment[] | undefined): boolean {
  return attachments?.some((attachment) => attachment.kind === 'image') ?? false
}

export function hasSendableMessageContent(
  text: string,
  attachments: readonly Attachment[] | undefined,
  imageMode: boolean,
): boolean {
  return text.trim().length > 0 || (!imageMode && hasImageAttachment(attachments))
}

export function initialConversationTitle(
  text: string,
  attachments: readonly Attachment[] | undefined,
  imageFallback: string,
): string {
  const readableText = mathContentToPlainText(text).replace(/\s+/g, ' ').trim()
  if (readableText) return readableText.slice(0, 60).trim()

  const image = attachments?.find((attachment) => attachment.kind === 'image')
  const basename = image?.name.split(/[\\/]/).pop()?.trim() ?? ''
  const extensionIndex = basename.lastIndexOf('.')
  const filename = (extensionIndex >= 0 ? basename.slice(0, extensionIndex) : basename)
    .replace(/\s+/g, ' ')
    .trim()

  return Array.from(filename || imageFallback.trim() || 'Image conversation').slice(0, 60).join('')
}
