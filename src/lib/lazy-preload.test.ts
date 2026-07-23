import type { ComponentType } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { lazyWithPreload } from './lazy-preload'

describe('lazyWithPreload', () => {
  it('shares one import promise across repeated preloads', async () => {
    const Page = (() => null) as ComponentType
    const importer = vi.fn(async () => ({ default: Page }))
    const LazyPage = lazyWithPreload(importer)

    const first = LazyPage.preload()
    const second = LazyPage.preload()

    expect(first).toBe(second)
    await expect(first).resolves.toEqual({ default: Page })
    expect(importer).toHaveBeenCalledTimes(1)
  })
})
