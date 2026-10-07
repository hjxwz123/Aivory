import { createContext, useContext } from 'react'

// Low-line controls are the product default, including portalled menus and
// editors. A surface can explicitly opt into outlines when its task needs them.
export const QuietSurfaceContext = createContext(true)

export function useQuietSurface() {
  return useContext(QuietSurfaceContext)
}
