import { describe, expect, it } from 'vitest'
import { matchesAdminSearch, mergeVisibleAdminOrder } from '@/lib/admin-list-filter'

describe('admin list search and filtered ordering', () => {
  it('matches case-insensitive tokens across fields and normalizes full-width text', () => {
    expect(matchesAdminSearch('  ＧＰＴ production ', ['GPT-5', 'OpenAI Production'])).toBe(true)
    expect(matchesAdminSearch('gpt disabled', ['GPT-5', null, undefined, 'Production'])).toBe(false)
    expect(matchesAdminSearch(' ', [])).toBe(true)
  })

  it('keeps hidden records in place and sends a complete order when sorting a subset', () => {
    const all = ['a', 'hidden-1', 'b', 'hidden-2', 'c'].map((id) => ({ id }))
    const reordered = mergeVisibleAdminOrder(all, [all[4], all[0], all[2]])
    expect(reordered.map((row) => row.id)).toEqual(['c', 'hidden-1', 'a', 'hidden-2', 'b'])
    expect(reordered).toHaveLength(all.length)
    expect(all.map((row) => row.id)).toEqual(['a', 'hidden-1', 'b', 'hidden-2', 'c'])
  })

  it('preserves current row data during rollback and ignores missing or duplicate IDs', () => {
    const current = [{ id: 'a', enabled: false }, { id: 'hidden', enabled: true }, { id: 'b', enabled: true }]
    const stale = [{ id: 'b', enabled: false }, { id: 'deleted', enabled: true }, { id: 'b', enabled: false }, { id: 'a', enabled: true }]
    const result = mergeVisibleAdminOrder(current, stale)
    expect(result).toEqual([current[2], current[1], current[0]])
    expect(result[2].enabled).toBe(false)
    expect(mergeVisibleAdminOrder(current, [])).toEqual(current)
  })
})
