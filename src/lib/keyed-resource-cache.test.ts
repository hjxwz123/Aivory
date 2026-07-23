import { describe, expect, it, vi } from 'vitest'
import { createKeyedResourceCache, resolveOwnedResourceView } from './keyed-resource-cache'

describe('createKeyedResourceCache', () => {
  it('coalesces requests and serves cached values until a refresh', async () => {
    const cache = createKeyedResourceCache<number>()
    const loader = vi.fn()
      .mockResolvedValueOnce(1)
      .mockResolvedValueOnce(2)

    const first = cache.load('u_1', loader)
    const duplicate = cache.load('u_1', loader)
    expect(first).toBe(duplicate)
    await expect(first).resolves.toBe(1)
    await expect(cache.load('u_1', loader)).resolves.toBe(1)
    await expect(cache.load('u_1', loader, true)).resolves.toBe(2)
    expect(loader).toHaveBeenCalledTimes(2)
  })

  it('clears values when the authenticated owner changes', async () => {
    const cache = createKeyedResourceCache<string[]>()
    cache.set('u_1', ['private'])
    cache.clear()

    expect(cache.peek('u_1')).toBeUndefined()
  })

  it('does not repopulate values from a request issued before clear', async () => {
    const cache = createKeyedResourceCache<string>()
    let resolveRequest: ((value: string) => void) | undefined
    const pending = cache.load('u_1', () => new Promise((resolve) => { resolveRequest = resolve }))

    cache.clear()
    resolveRequest?.('private')
    await pending

    expect(cache.peek('u_1')).toBeUndefined()
  })
})

describe('resolveOwnedResourceView', () => {
  it('never exposes previous-owner rows while the next user has no cache', () => {
    expect(resolveOwnedResourceView({
      resourceUserId: 'u_old',
      userId: 'u_new',
      value: ['old-private-row'],
      cached: undefined,
      empty: [],
      loading: false,
    })).toEqual({ value: [], loading: true })
  })

  it('adopts the current user cache synchronously before the effect runs', () => {
    expect(resolveOwnedResourceView({
      resourceUserId: 'u_old',
      userId: 'u_new',
      value: ['old-private-row'],
      cached: ['new-row'],
      empty: [],
      loading: false,
    })).toEqual({ value: ['new-row'], loading: false })
  })
})
