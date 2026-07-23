import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { ApiSharedMessage } from '@/api/types'
import { SharedMessageIdentity } from './shared-message-identity'

function message(role: ApiSharedMessage['role'], patch: Partial<ApiSharedMessage> = {}): ApiSharedMessage {
  return {
    role,
    blocks: [],
    citations: [],
    created_at: 1,
    ...patch,
  }
}

function renderIdentity(value: ApiSharedMessage): string {
  return renderToStaticMarkup(
    createElement(SharedMessageIdentity, {
      message: value,
      userFallback: 'User',
      assistantFallback: 'Assistant',
      fastLabel: 'Fast',
    }),
  )
}

describe('SharedMessageIdentity', () => {
  it('renders the snapshotted user name and avatar', () => {
    const html = renderIdentity(
      message('user', {
        author_name: 'Ada Lovelace',
        author_avatar: 'https://cdn.example.test/ada.png',
      }),
    )

    expect(html).toContain('Ada Lovelace')
    // Radix intentionally renders the fallback during SSR, then swaps in the
    // image after it loads in the browser. The avatar URL itself is covered by
    // the public API snapshot test; here we assert the stable fallback too.
    expect(html).toContain('AL')
  })

  it('renders the snapshotted assistant model name and icon', () => {
    const html = renderIdentity(message('assistant', { model_label: 'GPT Test', model_icon: 'GT' }))

    expect(html).toContain('GPT Test')
    expect(html).toContain('GT')
    expect(html).not.toContain('Assistant')
  })

  it('falls back for legacy snapshots and keeps fast model identity hidden', () => {
    expect(renderIdentity(message('user'))).toContain('User')
    expect(renderIdentity(message('assistant'))).toContain('Assistant')

    const fast = renderIdentity(
      message('assistant', { fast: true, model_label: 'Private Model', model_icon: 'PM' }),
    )
    expect(fast).toContain('Fast')
    expect(fast).not.toContain('Private Model')
    expect(fast).not.toContain('PM')
  })
})
