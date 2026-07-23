import { describe, expect, it } from 'vitest'
import type { ApiModel } from '@/api/types'
import {
  filterOfficialToolNames,
  officialToolsForModel,
  resolveDefaultOfficialToolNames,
  sanitizeOfficialToolNames,
} from './official-tools'

function modelWithOfficialTools(officialTools: unknown): Pick<ApiModel, 'official_tools'> {
  return { official_tools: officialTools as ApiModel['official_tools'] }
}

describe('official tool selections', () => {
  it('sanitizes persisted and account-setting arrays without changing user order', () => {
    const raw = [' image_generation ', 'web_search', 'image_generation', '', 42, null]

    expect(sanitizeOfficialToolNames(raw)).toEqual(['image_generation', 'web_search'])
    expect(resolveDefaultOfficialToolNames({ official_tool_names_default: raw })).toEqual([
      'image_generation',
      'web_search',
    ])
  })

  it('reads public definitions and remains compatible with the legacy string array', () => {
    expect(
      officialToolsForModel(
        modelWithOfficialTools([
          { name: 'web_search', icon: 'search', request: { tools: [{ type: 'web_search' }] } },
          'code_interpreter',
          { name: 'web_search', icon: 'duplicate' },
          { name: '', icon: 'invalid' },
        ]),
      ),
    ).toEqual([
      { name: 'web_search', icon: 'search' },
      { name: 'code_interpreter', icon: '' },
    ])
  })

  it('drops stale names and restores administrator order after a model switch', () => {
    const model = modelWithOfficialTools([
      { name: 'web_search', icon: 'search' },
      { name: 'code_interpreter', icon: 'terminal' },
      { name: 'image_generation', icon: 'image' },
    ])

    expect(
      filterOfficialToolNames(model, [
        'image_generation',
        'removed_tool',
        'web_search',
        'image_generation',
      ]),
    ).toEqual(['web_search', 'image_generation'])
    expect(filterOfficialToolNames(model, undefined)).toEqual([])
  })
})
