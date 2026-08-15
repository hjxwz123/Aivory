import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  IMAGE_UPLOAD_PASSTHROUGH_BYTES,
  prepareImageForUpload,
} from '@/lib/image-upload'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('prepareImageForUpload', () => {
  it('keeps images through 3 MiB byte-for-byte unchanged', async () => {
    const input = new File(
      [new Uint8Array(IMAGE_UPLOAD_PASSTHROUGH_BYTES)],
      'original.png',
      { type: 'image/png' },
    )

    await expect(prepareImageForUpload(input)).resolves.toBe(input)
  })

  it('compresses an oversized image recognized by extension when MIME is empty', async () => {
    const close = vi.fn()
    vi.stubGlobal('createImageBitmap', vi.fn(async () => ({
      width: 4000,
      height: 3000,
      close,
    })))

    const context = {
      imageSmoothingEnabled: false,
      imageSmoothingQuality: 'low',
      fillStyle: '',
      fillRect: vi.fn(),
      drawImage: vi.fn(),
    }
    const canvas = {
      width: 0,
      height: 0,
      getContext: vi.fn(() => context),
      toBlob: vi.fn((callback: BlobCallback, mimeType: string) => {
        callback(new Blob([new Uint8Array(1024)], { type: mimeType }))
      }),
    }
    vi.stubGlobal('document', {
      createElement: vi.fn(() => canvas),
    })

    const input = new File(
      [new Uint8Array(IMAGE_UPLOAD_PASSTHROUGH_BYTES + 1)],
      'phone-photo.heic',
      { type: '' },
    )
    const output = await prepareImageForUpload(input)

    expect(output).not.toBe(input)
    expect(output.size).toBeLessThan(IMAGE_UPLOAD_PASSTHROUGH_BYTES)
    expect(output.type).toBe('image/webp')
    expect(output.name).toBe('phone-photo.webp')
    expect(close).toHaveBeenCalledOnce()
  })
})
