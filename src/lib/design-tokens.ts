/**
 * Aurelia Design Tokens — TypeScript surface for runtime access.
 * Mirrors src/styles/tokens.css. Use this when you need a token value
 * inside JS (e.g. Framer Motion animations, canvas, dynamic styles).
 *
 * Important: never hardcode hex/rgb/oklch inside components. Either use
 * the Tailwind utility classes (which read from @theme tokens) or import
 * a value from here.
 */

export const radius = {
  none: '0',
  xs: '0.25rem',
  sm: '0.375rem',
  md: '0.5rem',
  lg: '0.75rem',
  xl: '1rem',
  '2xl': '1.25rem',
  '3xl': '1.75rem',
  full: '9999px',
} as const

export const space = {
  0: '0',
  px: '1px',
  0.5: '0.125rem',
  1: '0.25rem',
  1.5: '0.375rem',
  2: '0.5rem',
  2.5: '0.625rem',
  3: '0.75rem',
  3.5: '0.875rem',
  4: '1rem',
  5: '1.25rem',
  6: '1.5rem',
  7: '1.75rem',
  8: '2rem',
  10: '2.5rem',
  12: '3rem',
  14: '3.5rem',
  16: '4rem',
  20: '5rem',
  24: '6rem',
  32: '8rem',
} as const

export const duration = {
  instant: 80,
  fast: 140,
  base: 220,
  slow: 320,
  slower: 500,
  slowest: 800,
} as const

export const easing = {
  standard: [0.4, 0, 0.2, 1] as const,
  out: [0.2, 0.8, 0.2, 1] as const,
  in: [0.4, 0, 1, 1] as const,
  bounce: [0.34, 1.56, 0.64, 1] as const,
  elastic: [0.68, -0.6, 0.32, 1.6] as const,
} as const

export const zIndex = {
  base: 0,
  raised: 10,
  sticky: 20,
  overlay: 40,
  drawer: 50,
  dialog: 60,
  popover: 70,
  toast: 80,
  tooltip: 90,
  max: 9999,
} as const

export const layout = {
  sidebarWidth: '17.5rem',
  sidebarWidthCollapsed: '3.5rem',
  topbarHeight: '3.5rem',
  composerMaxWidth: '50rem',
  contentMaxWidth: '76rem',
  proseMaxWidth: '44rem',
  messageMaxWidth: '48rem',
} as const

export const breakpoint = {
  sm: 640,
  md: 768,
  lg: 1024,
  xl: 1280,
  '2xl': 1536,
} as const

/** Resolve a CSS variable at runtime. Use sparingly — prefer Tailwind utilities. */
export function cssVar(name: string): string {
  if (typeof window === 'undefined') return ''
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export type Radius = keyof typeof radius
export type Duration = keyof typeof duration
export type Easing = keyof typeof easing
