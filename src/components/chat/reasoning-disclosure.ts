export interface ReasoningDisclosureState {
  active: boolean
  expanded: boolean
}

export function createReasoningDisclosure(active: boolean): ReasoningDisclosureState {
  return { active, expanded: active }
}

/**
 * Automatic disclosure changes belong to phase boundaries only. Streaming
 * updates within the same phase must preserve the user's current choice.
 */
export function syncReasoningDisclosure(
  state: ReasoningDisclosureState,
  active: boolean,
): ReasoningDisclosureState {
  if (state.active === active) return state
  return { active, expanded: active }
}

export function toggleReasoningDisclosure(state: ReasoningDisclosureState): ReasoningDisclosureState {
  return { ...state, expanded: !state.expanded }
}
