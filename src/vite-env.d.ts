/// <reference types="vite/client" />

// §23 version probe — the build id baked in by vite.config.ts `define`.
declare const __APP_VERSION__: string

declare module '*.css' {}
declare module '*.svg' {
  const src: string
  export default src
}
// Self-hosted @fontsource packages are CSS-only (no type declarations); these
// are side-effect imports that register @font-face rules.
declare module '@fontsource-variable/*'
