import { modKey } from './utils'

export interface Shortcut {
  id: string
  name: string
  combo: string
  display: string[]
  scope: 'global' | 'composer' | 'sidebar'
}

export function buildShortcuts(): Shortcut[] {
  const mod = modKey()
  return [
    { id: 'cmd-k', name: 'Open command menu', combo: 'mod+k', display: [mod, 'K'], scope: 'global' },
    { id: 'send', name: 'Send message', combo: 'mod+enter', display: [mod, 'Enter'], scope: 'composer' },
    { id: 'newline', name: 'New line', combo: 'shift+enter', display: ['Shift', 'Enter'], scope: 'composer' },
    { id: 'sidebar', name: 'Toggle sidebar', combo: 'mod+b', display: [mod, 'B'], scope: 'global' },
    { id: 'esc', name: 'Close any overlay', combo: 'escape', display: ['Esc'], scope: 'global' },
    { id: 'settings', name: 'Open settings', combo: 'mod+,', display: [mod, ','], scope: 'global' },
    { id: 'new-chat', name: 'New chat', combo: 'mod+shift+o', display: [mod, 'Shift', 'O'], scope: 'global' },
    { id: 'shortcuts', name: 'Show keyboard shortcuts', combo: 'mod+/', display: [mod, '/'], scope: 'global' },
    { id: 'focus-composer', name: 'Focus composer', combo: '/', display: ['/'], scope: 'global' },
  ]
}
