import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { AdminDetailHeader } from '@/components/admin/admin-detail-header'

describe('AdminDetailHeader', () => {
  it('keeps the back destination in a sticky detail toolbar', () => {
    const html = renderToStaticMarkup(
      createElement(
        MemoryRouter,
        null,
        createElement(AdminDetailHeader, {
          backTo: '/admin/models',
          backLabel: 'Back to models',
        }),
      ),
    )

    expect(html).toContain('sticky top-0')
    expect(html).toContain('href="/admin/models"')
    expect(html).toContain('Back to models')
  })
})
