export interface KeyedResourceCache<T> {
  peek: (key: string) => T | undefined
  load: (key: string, loader: () => Promise<T>, refresh?: boolean) => Promise<T>
  set: (key: string, value: T) => void
  clear: () => void
}

export interface OwnedResourceView<T> {
  value: T
  loading: boolean
}

/**
 * A component may render once with state owned by the previous auth user before
 * its userId effect adopts the next snapshot. Resolve that render from the
 * CURRENT user's cache (or an empty loading value), never from stale local
 * state, so private rows cannot flash across an account switch.
 */
export function resolveOwnedResourceView<T>(input: {
  resourceUserId: string
  userId: string
  value: T
  cached: T | undefined
  empty: T
  loading: boolean
}): OwnedResourceView<T> {
  if (input.resourceUserId === input.userId) {
    return { value: input.value, loading: input.loading }
  }
  return {
    value: input.cached ?? input.empty,
    loading: Boolean(input.userId && input.cached === undefined),
  }
}

/**
 * Small in-memory stale-while-revalidate cache for dialog-owned resources.
 * It coalesces StrictMode/remount requests and lets a reopened settings page
 * paint its last authoritative rows immediately while refreshing in place.
 */
export function createKeyedResourceCache<T>(): KeyedResourceCache<T> {
  const values = new Map<string, T>()
  const inflight = new Map<string, Promise<T>>()
  let generation = 0

  return {
    peek(key) {
      return values.get(key)
    },
    load(key, loader, refresh = false) {
      if (!refresh && values.has(key)) return Promise.resolve(values.get(key) as T)
      const pending = inflight.get(key)
      if (pending) return pending
      const requestGeneration = generation
      const request = loader()
        .then((value) => {
          // A logout/account change clears the cache. Never let an older
          // in-flight response repopulate data owned by the previous session.
          if (requestGeneration === generation) values.set(key, value)
          return value
        })
        .finally(() => {
          if (inflight.get(key) === request) inflight.delete(key)
        })
      inflight.set(key, request)
      return request
    },
    set(key, value) {
      values.set(key, value)
    },
    clear() {
      generation += 1
      values.clear()
      inflight.clear()
    },
  }
}
