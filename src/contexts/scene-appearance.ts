import { createContext } from 'react'
import type { HomeScene } from '@/types/settings'

export const SceneAppearanceContext = createContext<{ scene: HomeScene; dark: boolean }>({
  scene: 'plain',
  dark: false,
})
