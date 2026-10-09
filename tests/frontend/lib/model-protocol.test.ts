import { describe, expect, it } from 'vitest'
import type { ApiChannel, ApiModel } from '@/api/types'
import { effectiveModelProtocol, modelProtocolLabel, protocolForKind } from '@/lib/model-protocol'
import { availablePolicyModels, availableVisionModels } from '@/lib/admin-model-policy'

describe('model request protocols', () => {
  const generic = { id: 'generic', enabled: true, has_api_key: true, type: 'typesafe', api_format: '' } as ApiChannel
  it('uses model protocols even when legacy channel metadata differs', () => {
    expect(effectiveModelProtocol({ kind: 'chat', protocol: 'openai.responses' }, generic)).toBe('openai.responses')
    expect(modelProtocolLabel({ kind: 'chat', protocol: 'anthropic.messages' }, generic)).toBe('Anthropic · Messages')
    expect(effectiveModelProtocol({ kind: 'image', protocol: 'openai.images' }, generic)).toBe('openai.images')
    expect(effectiveModelProtocol({ kind: 'embedding', protocol: 'openai.embeddings' }, generic)).toBe('openai.embeddings')
  })
  it('preserves compatible formats and resets incompatible formats on kind changes', () => {
    expect(protocolForKind('image', 'gemini.generateContent')).toBe('gemini.generateContent')
    expect(protocolForKind('embedding', 'openai.responses')).toBe('openai.embeddings')
    expect(protocolForKind('decision', 'openai.chat')).toBe('typesafe.decisions')
  })
  it('offers chat and decision models on the same generic channel to their supported policies', () => {
    const models = [
      { id: 'chat', channel_id: generic.id, kind: 'chat', protocol: 'openai.responses', enabled: true, vision: true },
      { id: 'decision', channel_id: generic.id, kind: 'decision', protocol: 'typesafe.decisions', enabled: true },
      { id: 'embedding', channel_id: generic.id, kind: 'embedding', protocol: 'openai.embeddings', enabled: true },
    ] as ApiModel[]
    expect(availablePolicyModels(models, [generic]).map((model) => model.id)).toEqual(['chat'])
    expect(availablePolicyModels(models, [generic], 'tool_route_model_id').map((model) => model.id)).toEqual(['chat', 'decision'])
    expect(availableVisionModels(models, [generic]).map((model) => model.id)).toEqual(['chat'])
  })
})
