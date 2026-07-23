import { lazy, type ComponentType, type LazyExoticComponent } from 'react'

export type PreloadableLazy<T extends ComponentType> = LazyExoticComponent<T> & {
  preload: () => Promise<{ default: T }>
}

/**
 * React.lazy caches after its first render. This wrapper exposes the same
 * memoized import promise so a likely destination can begin loading before it
 * is rendered, without issuing duplicate chunk requests.
 */
export function lazyWithPreload<T extends ComponentType>(
  importer: () => Promise<{ default: T }>,
): PreloadableLazy<T> {
  let pending: Promise<{ default: T }> | undefined
  const load = () => {
    pending ??= importer()
    return pending
  }
  return Object.assign(lazy(load), { preload: load })
}
