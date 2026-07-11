import { createCssVariablesTheme } from 'shiki/core'

export const SHIKI_THEME = 'aivory-css-vars'

export const aivoryShikiTheme = createCssVariablesTheme({
  name: SHIKI_THEME,
  variablePrefix: '--shiki-',
  fontStyle: true,
  variableDefaults: {
    foreground: 'currentColor',
    background: 'transparent',
    'token-comment': 'currentColor',
    'token-string': 'currentColor',
    'token-keyword': 'currentColor',
    'token-function': 'currentColor',
    'token-constant': 'currentColor',
    'token-string-expression': 'currentColor',
    'token-punctuation': 'currentColor',
    'token-link': 'currentColor',
    'token-deleted': 'currentColor',
    'token-inserted': 'currentColor',
  },
})
