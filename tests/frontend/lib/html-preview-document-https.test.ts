// @vitest-environment jsdom
// @vitest-environment-options {"url": "https://app.example/"}
import { describe, expect, it } from 'vitest'
import { buildHtmlPreviewDocument } from '@/lib/html-preview-document'

/**
 * The HTTPS half of the origin-dependent preview head.
 *
 * Over HTTPS the `upgrade-insecure-requests` directive is both safe and wanted:
 * it keeps an artifact's `http://` subresources from being blocked as mixed
 * content. It must therefore still be injected here — while the plain-http case
 * in html-preview-document.test.ts must not get it, because upgrading
 * same-origin requests there kills the injected Tailwind runtime.
 */
describe('buildHtmlPreviewDocument on an HTTPS origin', () => {
  it('upgrades insecure subresources', () => {
    const document = buildHtmlPreviewDocument(
      '<div class="relative flex items-center rounded-2xl bg-white p-6">Preview</div>',
    )

    expect(document).toContain('upgrade-insecure-requests')
    expect(document).toContain('target="_blank"')
    expect(document).toContain('data-aivory-tailwind')
  })

  it('still injects the runtime for artifacts that bring their own Tailwind', () => {
    const document = buildHtmlPreviewDocument(
      '<html><head><script src="https://cdn.tailwindcss.com"></script></head><body class="flex"></body></html>',
    )

    expect(document).toContain('upgrade-insecure-requests')
    expect(document).not.toContain('data-aivory-tailwind')
  })
})
