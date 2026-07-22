import { useCallback, useEffect, useRef, useState } from 'react'

export function normalizeEmailRetryAfter(value: unknown, fallback = 0): number {
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) return Math.max(0, Math.ceil(fallback))
  return Math.ceil(parsed)
}

export function emailRetryAfterFromBody(body: unknown, fallback = 0): number {
  if (!body || typeof body !== 'object' || !('retry_after' in body)) {
    return normalizeEmailRetryAfter(undefined, fallback)
  }
  return normalizeEmailRetryAfter((body as { retry_after?: unknown }).retry_after, fallback)
}

export function remainingEmailCooldown(deadlineMs: number, nowMs = Date.now()): number {
  if (!Number.isFinite(deadlineMs) || deadlineMs <= nowMs) return 0
  return Math.ceil((deadlineMs - nowMs) / 1000)
}

export function useEmailCooldown(initialSeconds = 0) {
  const initial = normalizeEmailRetryAfter(initialSeconds)
  const initialDeadline = initial > 0 ? Date.now() + initial * 1000 : 0
  const [deadline, setDeadline] = useState(initialDeadline)
  const [remaining, setRemaining] = useState(initial)
  const previousInitial = useRef(initial)

  const start = useCallback((seconds: unknown) => {
    const next = normalizeEmailRetryAfter(seconds)
    setRemaining(next)
    setDeadline(next > 0 ? Date.now() + next * 1000 : 0)
  }, [])

  useEffect(() => {
    if (initial !== previousInitial.current) {
      previousInitial.current = initial
      start(initial)
    }
  }, [initial, start])

  useEffect(() => {
    if (deadline <= 0) return
    const update = () => {
      const next = remainingEmailCooldown(deadline)
      setRemaining(next)
      if (next === 0) setDeadline(0)
    }
    update()
    const timer = window.setInterval(update, 250)
    return () => window.clearInterval(timer)
  }, [deadline])

  return { remaining, start }
}
