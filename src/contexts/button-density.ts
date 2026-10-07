import { createContext, useContext } from 'react'

// Settings actions and their portalled dialogs share a compact scale. Other
// surfaces retain the default sizing, and explicit button sizes still apply.
export const ButtonDensityContext = createContext<'default' | 'compact'>('default')

export function useButtonDensity() {
  return useContext(ButtonDensityContext)
}
