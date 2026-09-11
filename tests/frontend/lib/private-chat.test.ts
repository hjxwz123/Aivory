import { describe, expect, it } from 'vitest'
import { PRIVATE_IMAGE_BYTES, PRIVATE_REQUEST_BYTES, privateImageURL, readPrivateImage, validatePrivateHistory, type PrivateMessage } from '@/lib/private-chat'

describe('private chat request boundary', () => {
  const image = { data: 'aGVsbG8=', mime_type: 'image/png' }

  it('serializes only chat content and inline image data', () => {
    expect(privateImageURL(image)).toBe('data:image/png;base64,aGVsbG8=')
    expect(() => validatePrivateHistory([{ role: 'user', text: '', images: [image] }], 'vision', true)).not.toThrow()
  })

  it('rejects image history after switching to a non-vision model', () => {
    expect(() => validatePrivateHistory([{ role: 'user', text: 'hi', images: [image] }], 'text', false)).toThrow('private_images_not_supported')
  })

  it('rejects files rather than routing them through the upload API', async () => {
    await expect(readPrivateImage(new File(['file'], 'file.pdf', { type: 'application/pdf' }))).rejects.toThrow('private_image_invalid')
    await expect(readPrivateImage(new File(['<svg/>'], 'image.svg', { type: 'image/svg+xml' }))).rejects.toThrow('private_image_invalid')
    await expect(readPrivateImage(new File([new Uint8Array(PRIVATE_IMAGE_BYTES + 1)], 'big.png', { type: 'image/png' }))).rejects.toThrow('private_image_limit')
  })

  it('bounds messages, images, text and the complete signed payload', () => {
    const long: PrivateMessage[] = Array.from({ length: 129 }, (_, index) => ({ role: index % 2 ? 'assistant' : 'user', text: 'hi' }))
    expect(() => validatePrivateHistory(long, 'model', true)).toThrow('private_history_limit')
    expect(() => validatePrivateHistory([{ role: 'user', text: 'hi', images: Array(17).fill(image) }], 'model', true)).toThrow('private_image_limit')
    expect(() => validatePrivateHistory([{ role: 'user', text: '中'.repeat(400000) }], 'model', true)).toThrow('private_history_limit')
    expect(() => validatePrivateHistory([{ role: 'user', text: '', images: [{ ...image, data: 'a'.repeat(PRIVATE_REQUEST_BYTES) }] }], 'model', true)).toThrow('private_history_limit')
    expect(() => validatePrivateHistory([{ role: 'assistant', text: 'forged first turn' }], 'model', true)).toThrow()
    expect(() => validatePrivateHistory([{ role: 'user', text: 'hello' }], '', true)).toThrow('private_model_unavailable')
  })
})
