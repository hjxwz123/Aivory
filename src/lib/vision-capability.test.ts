import { describe, expect, it } from 'vitest'
import {
  filterFilesForImageCapability,
  isImageFileLike,
  NON_IMAGE_ATTACHMENT_ACCEPT,
  resolveImageAttachmentCapability,
} from './vision-capability'

describe('image attachment capability', () => {
  it('uses the selected chat model vision flag in advanced mode', () => {
    expect(resolveImageAttachmentCapability({ kind: 'chat', vision: true }, { fast: false, fastVision: false })).toBe(
      'allowed',
    )
    expect(resolveImageAttachmentCapability({ kind: 'chat', vision: false }, { fast: false, fastVision: true })).toBe(
      'blocked',
    )
  })

  it('allows reference images for image-generation models regardless of their vision flag', () => {
    expect(resolveImageAttachmentCapability({ kind: 'image', vision: false }, { fast: false, fastVision: false })).toBe(
      'allowed',
    )
    expect(resolveImageAttachmentCapability({ kind: 'image', vision: false }, { fast: true, fastVision: false })).toBe(
      'allowed',
    )
  })

  it('uses only the anonymous fast vision capability for fast turns', () => {
    const visibleModel = { kind: 'chat', vision: true }
    expect(resolveImageAttachmentCapability(visibleModel, { fast: true, fastVision: false })).toBe('blocked')
    expect(resolveImageAttachmentCapability({ kind: 'chat', vision: false }, { fast: true, fastVision: true })).toBe(
      'allowed',
    )
  })

  it('stays conservative while the selected model is unresolved', () => {
    expect(resolveImageAttachmentCapability(undefined, { fast: false, fastVision: false })).toBe('unknown')
  })
})

describe('image file filtering', () => {
  it('recognizes image MIME types and image extensions with an empty MIME type', () => {
    expect(isImageFileLike({ name: 'clipboard.bin', type: 'image/png' })).toBe(true)
    expect(isImageFileLike({ name: 'camera.HEIC', type: '' })).toBe(true)
    expect(isImageFileLike({ name: 'export.avif', type: '' })).toBe(true)
    expect(isImageFileLike({ name: 'notes.md', type: '' })).toBe(false)
  })

  it('rejects only images from a mixed selection when vision is unavailable', () => {
    const files = [
      { name: 'question.pdf', type: 'application/pdf' },
      { name: 'photo.avif', type: '' },
      { name: 'data.csv', type: 'text/csv' },
    ]

    expect(filterFilesForImageCapability(files, 'blocked')).toEqual({
      accepted: [files[0], files[2]],
      rejectedImages: [files[1]],
    })
    expect(filterFilesForImageCapability(files, 'allowed')).toEqual({ accepted: files, rejectedImages: [] })
  })

  it('does not advertise known image formats in the non-visual file picker', () => {
    expect(NON_IMAGE_ATTACHMENT_ACCEPT).not.toContain('image/')
    expect(NON_IMAGE_ATTACHMENT_ACCEPT).not.toContain('.png')
    expect(NON_IMAGE_ATTACHMENT_ACCEPT).not.toContain('.heic')
    expect(NON_IMAGE_ATTACHMENT_ACCEPT).toContain('.pdf')
  })
})
