import { describe, expect, it } from 'vitest'
import { partitionConversationNavigation } from '@/lib/conversation-navigation'

describe('partitionConversationNavigation', () => {
  it('keeps ordinary conversations in global history and groups project conversations', () => {
    const rows = [
      { id: 'global-new' },
      { id: 'project-a-new', projectId: 'project-a' },
      { id: 'project-b', projectId: 'project-b' },
      { id: 'project-a-old', projectId: 'project-a' },
      { id: 'global-old' },
    ]

    const result = partitionConversationNavigation(rows)

    expect(result.ordinary.map((row) => row.id)).toEqual(['global-new', 'global-old'])
    expect(result.byProject.get('project-a')?.map((row) => row.id)).toEqual([
      'project-a-new',
      'project-a-old',
    ])
    expect(result.byProject.get('project-b')?.map((row) => row.id)).toEqual(['project-b'])
  })

  it('never falls an unknown project id back into ordinary history', () => {
    const result = partitionConversationNavigation([
      { id: 'orphaned-project-chat', projectId: 'deleted-project' },
    ])

    expect(result.ordinary).toEqual([])
    expect(result.byProject.get('deleted-project')).toHaveLength(1)
  })
})
