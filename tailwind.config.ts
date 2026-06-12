import type { Config } from 'tailwindcss'

/**
 * Tailwind v4 config. Theme tokens are declared in src/styles/globals.css
 * via the @theme directive. This file only configures content paths
 * (Tailwind v4 still benefits from explicit `content` in some setups)
 * and the dark mode strategy.
 */
const config: Config = {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {},
  },
  plugins: [],
}

export default config
