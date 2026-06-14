/// <reference types="vite/client" />

declare module '*.css' {}
declare module '*.svg' {
  const src: string
  export default src
}
// Self-hosted @fontsource packages are CSS-only (no type declarations); these
// are side-effect imports that register @font-face rules.
declare module '@fontsource-variable/*'
