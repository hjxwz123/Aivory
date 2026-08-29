import { describe, expect, it } from 'vitest'
import { buildHtmlPreviewDocument } from '@/lib/html-preview-document'

const TAILWIND_BROWSER = '@tailwindcss/browser@4.1.12'

describe('buildHtmlPreviewDocument', () => {
  it('loads Tailwind for generated utility-class fragments', () => {
    const document = buildHtmlPreviewDocument(
      '<div class="relative flex items-center rounded-2xl bg-white p-6">Preview</div>',
    )

    expect(document).toContain(TAILWIND_BROWSER)
    expect(document).toContain('upgrade-insecure-requests')
    expect(document).toContain('target="_blank"')
  })

  it('inserts preview resources inside an existing document head', () => {
    const document = buildHtmlPreviewDocument(
      '<!doctype html><html><head><title>Preview</title></head><body class="grid gap-4"></body></html>',
    )

    expect(document.indexOf('upgrade-insecure-requests')).toBeGreaterThan(document.indexOf('<head>'))
    expect(document.indexOf('upgrade-insecure-requests')).toBeLessThan(document.indexOf('<title>'))
    expect(document).toContain(TAILWIND_BROWSER)
  })

  it('does not add a second Tailwind runtime', () => {
    const html =
      '<html><head><script src="https://cdn.tailwindcss.com"></script></head><body class="flex items-center"></body></html>'
    const document = buildHtmlPreviewDocument(html)

    expect(document).toContain('cdn.tailwindcss.com')
    expect(document).not.toContain(TAILWIND_BROWSER)
  })

  it('leaves ordinary custom-class previews free of Tailwind resets', () => {
    const document = buildHtmlPreviewDocument(
      '<style>.widget.primary { color: red; }</style><div class="widget primary">Preview</div>',
    )

    expect(document).not.toContain(TAILWIND_BROWSER)
    expect(document).toContain('.widget.primary { color: red; }')
  })

  it('keeps an empty preview empty', () => {
    expect(buildHtmlPreviewDocument('')).toBe('')
  })
})
