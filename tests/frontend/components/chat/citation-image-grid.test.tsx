// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CitationList } from '@/components/chat/citation'
import type { Citation } from '@/types/chat'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, options?: { title?: string }) => options?.title ? `${key}: ${options.title}` : key }),
}))
vi.mock('@/components/chat/image-lightbox', () => ({
  ImageLightbox: ({ src, sourceUrl, onOpenChange }: { src: string; sourceUrl?: string; onOpenChange: (open: boolean) => void }) =>
    createElement('div', { role: 'dialog' },
      createElement('img', { src, alt: 'original' }),
      createElement('a', { href: sourceUrl }, 'source'),
      createElement('button', { onClick: () => onOpenChange(false) }, 'close'),
    ),
}))

const imageCitation: Citation = {
  id: 'image-1', index: 1, title: 'Kyoto temple', url: 'https://source.test/gallery', domain: 'source.test', source: 'web',
  imageUrl: 'https://images.test/original.jpg', thumbnailUrl: 'https://images.test/thumbnail.jpg',
}
let root: Root
let container: HTMLDivElement
function render(citations: Citation[]) {
  act(() => root.render(createElement(CitationList, { citations })))
}

describe('image search citations', () => {
  beforeEach(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.unstubAllGlobals()
  })

  it('shows image results before the collapsed source list and opens the original', () => {
    render([imageCitation])
    expect(container.querySelector('[aria-expanded]')?.getAttribute('aria-expanded')).toBe('false')
    const gallery = container.querySelector('[data-image-search-gallery]')!
    const img = gallery.querySelector('img')!
    expect(img.src).toBe(imageCitation.thumbnailUrl)
    expect(img.getAttribute('loading')).toBe('lazy')
    expect(img.getAttribute('referrerpolicy')).toBe('no-referrer')
    expect(gallery.querySelector('a')?.href).toBe(imageCitation.url)
    act(() => gallery.querySelector('button')!.click())
    expect(container.querySelector('[role="dialog"] img')?.getAttribute('src')).toBe(imageCitation.imageUrl)
    expect(container.querySelector('[role="dialog"] a')?.getAttribute('href')).toBe(imageCitation.url)
    act(() => container.querySelector<HTMLButtonElement>('[role="dialog"] button')!.click())
    expect(container.querySelector('[role="dialog"]')).toBeNull()
  })

  it('falls back from a broken thumbnail to the original, then retains the source on failure', () => {
    render([imageCitation])
    const gallery = container.querySelector('[data-image-search-gallery]')!
    const img = gallery.querySelector('img')!
    act(() => img.dispatchEvent(new Event('error')))
    expect(img.src).toBe(imageCitation.imageUrl)
    act(() => img.dispatchEvent(new Event('error')))
    expect(gallery.querySelector('img')).toBeNull()
    expect(gallery.querySelector('[role="status"]')?.textContent).toBe('sources.imageUnavailable')
    expect(gallery.querySelector('button')?.disabled).toBe(true)
    expect(gallery.querySelector('a')?.href).toBe(imageCitation.url)
  })

  it('keeps legacy webpage results unchanged and excludes unsafe or document images', () => {
    for (const citation of [
      { ...imageCitation, imageUrl: undefined, thumbnailUrl: undefined },
      { ...imageCitation, imageUrl: 'javascript:alert(1)', thumbnailUrl: 'data:image/png,unsafe' },
      { ...imageCitation, imageUrl: 'https://user:password@images.test/one.jpg', thumbnailUrl: undefined },
      { ...imageCitation, source: 'kb' as const, url: 'kbdoc://doc-1' },
    ]) {
      render([citation])
      expect(container.querySelector('[data-image-search-gallery]')).toBeNull()
      expect(container.querySelector('img')).toBeNull()
    }
  })

  it('supports thumbnail-only results and deduplicates images while preserving different photos on one page', () => {
    render([
      imageCitation,
      { ...imageCitation, id: 'duplicate', index: 2 },
      { ...imageCitation, id: 'image-2', index: 3, imageUrl: 'https://images.test/two.jpg', thumbnailUrl: undefined },
      { ...imageCitation, id: 'image-3', index: 4, imageUrl: undefined, thumbnailUrl: 'https://images.test/preview.jpg' },
    ])
    const gallery = container.querySelector('[data-image-search-gallery]')!
    expect(gallery.querySelectorAll('img')).toHaveLength(3)
    expect(gallery.querySelectorAll('a')).toHaveLength(3)
  })

  it('retains ordinary result images without showing a gallery until requested', () => {
    render([{ ...imageCitation, imageDisplay: false }])
    expect(container.querySelector('[data-image-search-gallery]')).toBeNull()
    expect(container.querySelector('[aria-expanded]')).not.toBeNull()
    render([{ ...imageCitation, imageDisplay: true }])
    expect(container.querySelector('[data-image-search-gallery] img')?.getAttribute('src')).toBe(imageCitation.thumbnailUrl)
  })
})
