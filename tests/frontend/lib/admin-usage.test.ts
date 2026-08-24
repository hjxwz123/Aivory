import { describe, expect, it } from 'vitest'
import { usageUserLabel } from '@/lib/admin-usage'

describe('admin usage user label', () => {
  it('shows the nickname without exposing the email address', () => {
    expect(usageUserLabel({ user_name: 'Aivory User', user_id: 'u_123' })).toBe('Aivory User')
  })

  it('falls back to the stable user id when the nickname is empty', () => {
    expect(usageUserLabel({ user_name: '   ', user_id: 'u_123' })).toBe('u_123')
  })
})
