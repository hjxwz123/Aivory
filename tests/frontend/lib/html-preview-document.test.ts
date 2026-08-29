import { describe, expect, it } from 'vitest'
import { buildHtmlPreviewDocument } from '@/lib/html-preview-document'

describe('buildHtmlPreviewDocument', () => {
  it('loads the bundled Tailwind runtime for generated utility-class fragments', () => {
    const document = buildHtmlPreviewDocument(
      '<div class="relative flex items-center rounded-2xl bg-white p-6">Preview</div>',
    )

    expect(document).toContain('data-aivory-tailwind')
    expect(document).not.toContain('cdn.jsdelivr.net')
    expect(document).toContain('upgrade-insecure-requests')
    expect(document).toContain('target="_blank"')
  })

  it('inserts preview resources inside an existing document head', () => {
    const document = buildHtmlPreviewDocument(
      '<!doctype html><html><head><title>Preview</title></head><body class="grid gap-4"></body></html>',
    )

    expect(document.indexOf('upgrade-insecure-requests')).toBeGreaterThan(document.indexOf('<head>'))
    expect(document.indexOf('upgrade-insecure-requests')).toBeLessThan(document.indexOf('<title>'))
    expect(document).toContain('data-aivory-tailwind')
  })

  it('does not add a second Tailwind runtime', () => {
    const html =
      '<html><head><script src="https://cdn.tailwindcss.com"></script></head><body class="flex items-center"></body></html>'
    const document = buildHtmlPreviewDocument(html)

    expect(document).toContain('cdn.tailwindcss.com')
    expect(document).not.toContain('data-aivory-tailwind')
  })

  it('leaves ordinary custom-class previews free of Tailwind resets', () => {
    const document = buildHtmlPreviewDocument(
      '<style>.widget.primary { color: red; }</style><div class="widget primary">Preview</div>',
    )

    expect(document).not.toContain('data-aivory-tailwind')
    expect(document).toContain('.widget.primary { color: red; }')
  })

  it('does not duplicate its bundled runtime when rebuilding a preview document', () => {
    const initial = buildHtmlPreviewDocument('<main class="grid gap-4">Preview</main>')
    const rebuilt = buildHtmlPreviewDocument(initial)

    expect(rebuilt.match(/data-aivory-tailwind/g)).toHaveLength(1)
  })

  it('rewrites Google Fonts stylesheets and font files to reachable mirrors', () => {
    const document = buildHtmlPreviewDocument(`
      <link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter:wght@400;700&display=swap">
      <style>
        @font-face { src: url('//fonts.gstatic.com/s/inter/v20/inter.woff2') format('woff2'); }
      </style>
    `)

    expect(document).toContain(
      'https://fonts.loli.net/css2?family=Inter:wght@400;700&display=swap',
    )
    expect(document).toContain('https://gstatic.loli.net/s/inter/v20/inter.woff2')
    expect(document).not.toContain('fonts.googleapis.com')
    expect(document).not.toContain('fonts.gstatic.com')
  })

  it('upgrades HTTP Google Fonts references while preserving their paths', () => {
    const document = buildHtmlPreviewDocument(
      '<style>@import url(http://fonts.googleapis.com/css?family=Roboto);</style>',
    )

    expect(document).toContain(
      '@import url(https://fonts.loli.net/css?family=Roboto)',
    )
  })

  it('keeps an empty preview empty', () => {
    expect(buildHtmlPreviewDocument('')).toBe('')
  })
})
