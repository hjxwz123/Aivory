import { describe, expect, it } from 'vitest'
import {
  createReasoningDisclosure,
  syncReasoningDisclosure,
  toggleReasoningDisclosure,
} from './reasoning-disclosure'

describe('reasoning disclosure state', () => {
  it('starts completed reasoning collapsed and supports repeated toggles', () => {
    let state = createReasoningDisclosure(false)

    expect(state.expanded).toBe(false)
    state = toggleReasoningDisclosure(state)
    expect(state.expanded).toBe(true)
    state = toggleReasoningDisclosure(state)
    expect(state.expanded).toBe(false)
    state = toggleReasoningDisclosure(state)
    expect(state.expanded).toBe(true)
  })

  it('starts an active reasoning phase expanded', () => {
    expect(createReasoningDisclosure(true)).toEqual({ active: true, expanded: true })
  })

  it('preserves a manual choice across updates in the same streaming phase', () => {
    let state = createReasoningDisclosure(true)
    state = toggleReasoningDisclosure(state)

    const afterStreamingUpdate = syncReasoningDisclosure(state, true)

    expect(afterStreamingUpdate).toBe(state)
    expect(afterStreamingUpdate.expanded).toBe(false)
  })

  it('collapses on settle and auto-expands for a later reasoning phase', () => {
    let state = createReasoningDisclosure(true)
    state = syncReasoningDisclosure(state, false)
    expect(state).toEqual({ active: false, expanded: false })

    state = toggleReasoningDisclosure(state)
    expect(state.expanded).toBe(true)

    state = syncReasoningDisclosure(state, true)
    expect(state).toEqual({ active: true, expanded: true })
  })
})
