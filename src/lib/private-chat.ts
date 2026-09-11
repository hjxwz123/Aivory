export const PRIVATE_IMAGE_BYTES = 5 * 1024 * 1024
export const PRIVATE_REQUEST_BYTES = 32 * 1024 * 1024
export const PRIVATE_IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif'] as const

export interface PrivateImage {
  data: string
  mime_type: string
}

export interface PrivateMessage {
  role: 'user' | 'assistant'
  text: string
  images?: PrivateImage[]
}

export function privateImageURL(image: PrivateImage): string {
  return `data:${image.mime_type};base64,${image.data}`
}

export function readPrivateImage(file: File): Promise<PrivateImage> {
  if (!PRIVATE_IMAGE_TYPES.some((type) => type === file.type)) {
    return Promise.reject(new Error('private_image_invalid'))
  }
  if (file.size === 0 || file.size > PRIVATE_IMAGE_BYTES) {
    return Promise.reject(new Error('private_image_limit'))
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('private_image_invalid'))
    reader.onabort = () => reject(new Error('private_image_invalid'))
    reader.onload = () => {
      const result = reader.result
      if (typeof result !== 'string' || !result.startsWith(`data:${file.type};base64,`)) {
        reject(new Error('private_image_invalid'))
        return
      }
      resolve({ mime_type: file.type, data: result.slice(result.indexOf(',') + 1) })
    }
    reader.readAsDataURL(file)
  })
}

export function validatePrivateHistory(messages: PrivateMessage[], modelId: string, vision: boolean): void {
  if (!modelId) throw new Error('private_model_unavailable')
  if (!messages.length || messages.length > 127 || messages.at(-1)?.role !== 'user') {
    throw new Error('private_history_limit')
  }
  let imageCount = 0
  let textBytes = 0
  for (const [index, message] of messages.entries()) {
    if (message.role !== (index % 2 === 0 ? 'user' : 'assistant') || (!message.text.trim() && !message.images?.length)) {
      throw new Error('private_invalid_request')
    }
    textBytes += new TextEncoder().encode(message.text).length
    imageCount += message.images?.length ?? 0
    if (message.images?.length && (!vision || message.role !== 'user')) {
      throw new Error('private_images_not_supported')
    }
  }
  if (imageCount > 16) throw new Error('private_image_limit')
  if (textBytes > 1024 * 1024 || new TextEncoder().encode(JSON.stringify({ model_id: modelId, messages })).length > PRIVATE_REQUEST_BYTES) {
    throw new Error('private_history_limit')
  }
}
