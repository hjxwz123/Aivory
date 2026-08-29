import tailwindBrowserUrl from '@tailwindcss/browser?url'

const PREVIEW_RESOURCE_HEAD =
  '<meta http-equiv="Content-Security-Policy" content="upgrade-insecure-requests">' +
  '<base target="_blank" rel="noopener noreferrer">'

const TAILWIND_RUNTIME = `<script data-aivory-tailwind src="${tailwindBrowserUrl}"></script>`
const GOOGLE_FONTS_STYLESHEET_HOST = /(?:https?:)?\/\/fonts\.googleapis\.com(?=\/)/gi
const GOOGLE_FONTS_FILE_HOST = /(?:https?:)?\/\/fonts\.gstatic\.com(?=\/)/gi

const CLASS_ATTRIBUTE = /\bclass\s*=\s*(["'])(.*?)\1/gis
const EXISTING_TAILWIND = /data-aivory-tailwind|cdn\.tailwindcss\.com|@tailwindcss\/browser|text\/tailwindcss|tailwind(?:css)?(?:\.config|[./-](?:min\.)?css)/i
const STANDALONE_UTILITY = /^(?:block|inline|inline-block|hidden|flex|inline-flex|grid|inline-grid|table|contents|flow-root|container|relative|absolute|fixed|sticky|grow|grow-0|shrink|shrink-0|truncate|italic|antialiased|uppercase|lowercase|capitalize|underline|overline|line-through|no-underline|transition|transform|appearance-none|sr-only|not-sr-only)$/
const PREFIXED_UTILITY = /^(?:-?(?:m[trblxy]?|p[trblxy]?|inset(?:-[xy])?|top|right|bottom|left|gap(?:-[xy])?|space-[xy]|w|min-w|max-w|h|min-h|max-h|size|z|order|basis|translate-[xy]|rotate|scale|skew-[xy])-|(?:items|justify|content|self|place|grid|col|row|auto-cols|auto-rows|text|font|leading|tracking|whitespace|break|bg|from|via|to|border|rounded|shadow|ring|divide|outline|opacity|overflow|overscroll|object|fill|stroke|transition|duration|delay|ease|cursor|select|resize|pointer-events)-)/

function utilityName(token: string): string {
  let bracketDepth = 0
  let variantEnd = -1

  for (let index = 0; index < token.length; index += 1) {
    if (token[index] === '[') bracketDepth += 1
    else if (token[index] === ']') bracketDepth = Math.max(0, bracketDepth - 1)
    else if (token[index] === ':' && bracketDepth === 0) variantEnd = index
  }

  return token.slice(variantEnd + 1).replace(/^!/, '')
}

function usesTailwindUtilities(html: string): boolean {
  if (EXISTING_TAILWIND.test(html)) return false

  let matches = 0
  for (const attribute of html.matchAll(CLASS_ATTRIBUTE)) {
    for (const token of attribute[2].trim().split(/\s+/)) {
      const utility = utilityName(token)
      if (STANDALONE_UTILITY.test(utility) || PREFIXED_UTILITY.test(utility)) {
        matches += 1
        if (matches >= 2) return true
      }
    }
  }
  return false
}

function rewriteRestrictedResources(html: string): string {
  return html
    .replace(GOOGLE_FONTS_STYLESHEET_HOST, 'https://fonts.loli.net')
    .replace(GOOGLE_FONTS_FILE_HOST, 'https://gstatic.loli.net')
}

/** Build an isolated preview document and support generated Tailwind fragments. */
export function buildHtmlPreviewDocument(html: string): string {
  if (!html) return html

  const previewHtml = rewriteRestrictedResources(html)
  const previewHead = PREVIEW_RESOURCE_HEAD + (usesTailwindUtilities(previewHtml) ? TAILWIND_RUNTIME : '')
  const headOpen = /<head[^>]*>/i
  if (headOpen.test(previewHtml)) return previewHtml.replace(headOpen, (match) => match + previewHead)

  const htmlOpen = /<html[^>]*>/i
  if (htmlOpen.test(previewHtml)) return previewHtml.replace(htmlOpen, (match) => `${match}<head>${previewHead}</head>`)

  return previewHead + previewHtml
}
