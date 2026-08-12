import { describe, expect, it } from 'vitest'
import { knowledgeBaseSelectionContext } from '@/lib/knowledge-base-selection'

describe('knowledge-base selection context', () => {
  it('removes the project library from user options and explicit selections', () => {
    const projectKB = { id: 'kb-project' }
    const selectedKB = { id: 'kb-selected' }
    const otherKB = { id: 'kb-other' }

    expect(
      knowledgeBaseSelectionContext(
        [projectKB, selectedKB, otherKB],
        ['kb-project', 'kb-selected', 'kb-selected'],
        'kb-project',
      ),
    ).toEqual({
      options: [selectedKB, otherKB],
      selectedIds: ['kb-selected'],
    })
  })

  it('keeps ordinary selections when the project library is not listed', () => {
    const ordinaryKB = { id: 'kb-ordinary' }
    const context = knowledgeBaseSelectionContext([ordinaryKB], ['kb-project', 'kb-ordinary'], 'kb-project')

    expect(context.options).toEqual([ordinaryKB])
    expect(context.selectedIds).toEqual(['kb-ordinary'])
  })
})
