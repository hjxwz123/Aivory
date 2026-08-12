import { describe, expect, it } from 'vitest'
import {
  knowledgeBaseSelectionContext,
  knowledgeBasesHaveCompatibleEmbeddings,
} from '@/lib/knowledge-base-selection'

describe('knowledge-base embedding compatibility', () => {
  it('requires both the configured model and stored vector dimension to match', () => {
    const base = { embedding_model_id: 'embed-v1', embedding_dim: 1024 }

    expect(knowledgeBasesHaveCompatibleEmbeddings(base, { ...base })).toBe(true)
    expect(
      knowledgeBasesHaveCompatibleEmbeddings(base, {
        embedding_model_id: 'embed-v2',
        embedding_dim: 1024,
      }),
    ).toBe(false)
    expect(
      knowledgeBasesHaveCompatibleEmbeddings(base, {
        embedding_model_id: 'embed-v1',
        embedding_dim: 768,
      }),
    ).toBe(false)
  })

  it('normalizes harmless whitespace in model ids', () => {
    expect(
      knowledgeBasesHaveCompatibleEmbeddings(
        { embedding_model_id: ' embed-v1 ', embedding_dim: 1024 },
        { embedding_model_id: 'embed-v1', embedding_dim: 1024 },
      ),
    ).toBe(true)
  })

  it('uses the project KB as a locked anchor and removes it from user options', () => {
    const projectKB = { id: 'kb-project', embedding_model_id: 'embed-v1', embedding_dim: 1024 }
    const selectedKB = { id: 'kb-selected', embedding_model_id: 'embed-v1', embedding_dim: 1024 }
    const otherKB = { id: 'kb-other', embedding_model_id: 'embed-v2', embedding_dim: 768 }

    expect(
      knowledgeBaseSelectionContext(
        [projectKB, selectedKB, otherKB],
        ['kb-project', 'kb-selected', 'kb-selected'],
        'kb-project',
      ),
    ).toEqual({
      options: [selectedKB, otherKB],
      anchors: [projectKB, selectedKB],
      selectedIds: ['kb-selected'],
    })
  })

  it('builds a compatibility-only project anchor when the project KB is hidden', () => {
    const ordinaryKB = {
      id: 'kb-ordinary',
      embedding_model_id: 'embed-v2',
      embedding_dim: 768,
    }
    const context = knowledgeBaseSelectionContext(
      [ordinaryKB],
      ['kb-project'],
      'kb-project',
      { embedding_model_id: 'embed-v1', embedding_dim: 1024 },
    )

    expect(context.options).toEqual([ordinaryKB])
    expect(context.selectedIds).toEqual([])
    expect(context.anchors).toEqual([
      { id: 'kb-project', embedding_model_id: 'embed-v1', embedding_dim: 1024 },
    ])
    expect(
      context.anchors.some(
        (anchor) => !knowledgeBasesHaveCompatibleEmbeddings(anchor, ordinaryKB),
      ),
    ).toBe(true)
  })

  it('does not synthesize a locked anchor from an incomplete signature', () => {
    const ordinaryKB = {
      id: 'kb-ordinary',
      embedding_model_id: 'embed-v2',
      embedding_dim: 768,
    }

    expect(
      knowledgeBaseSelectionContext(
        [ordinaryKB],
        [],
        'kb-project',
        { embedding_model_id: '', embedding_dim: 0 },
      ).anchors,
    ).toEqual([])
  })
})
