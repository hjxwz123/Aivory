import type { ApiChannel, ApiModel, ApiModelProtocol } from '@/api/types'

export const MODEL_PROTOCOLS: { value: ApiModelProtocol; label: string; kinds: ApiModel['kind'][] }[] = [
  { value: 'openai.chat', label: 'OpenAI · Chat Completions', kinds: ['chat'] },
  { value: 'openai.responses', label: 'OpenAI · Responses', kinds: ['chat'] },
  { value: 'anthropic.messages', label: 'Anthropic · Messages', kinds: ['chat'] },
  { value: 'gemini.generateContent', label: 'Gemini · generateContent', kinds: ['chat', 'image'] },
  { value: 'openai.images', label: 'OpenAI · Images', kinds: ['image'] },
  { value: 'openai.embeddings', label: 'OpenAI · Embeddings', kinds: ['embedding'] },
  { value: 'dashscope.embeddings', label: 'DashScope · Embeddings', kinds: ['embedding'] },
  { value: 'typesafe.decisions', label: 'TypeSafe · Decisions', kinds: ['decision'] },
  { value: 'openrouter.decisions', label: 'OpenRouter · Decisions', kinds: ['decision'] },
]

export function isDecisionProtocol(protocol: ApiModelProtocol): boolean {
  return protocol === 'typesafe.decisions' || protocol === 'openrouter.decisions'
}

export function protocolForKind(kind: ApiModel['kind'], current?: ApiModelProtocol): ApiModelProtocol {
  return MODEL_PROTOCOLS.find((option) => option.value === current && option.kinds.includes(kind))?.value
    ?? MODEL_PROTOCOLS.find((option) => option.kinds.includes(kind))!.value
}

export function effectiveModelProtocol(model: Pick<ApiModel, 'kind' | 'protocol'>, channel?: ApiChannel): ApiModelProtocol {
  if (model.protocol) return model.protocol
  if (model.kind === 'decision') return 'typesafe.decisions'
  if (model.kind === 'embedding') return channel?.base_url?.replace(/\/+$/, '').endsWith('/api/v1') ? 'dashscope.embeddings' : 'openai.embeddings'
  if (channel?.type === 'gemini' || channel?.type === 'google') return 'gemini.generateContent'
  if (model.kind === 'image') return 'openai.images'
  if (channel?.type === 'anthropic' || channel?.type === 'claude') return 'anthropic.messages'
  if (channel?.type === 'typesafe') return 'typesafe.decisions'
  return channel?.api_format === 'responses' ? 'openai.responses' : 'openai.chat'
}

export function modelProtocolLabel(model: Pick<ApiModel, 'kind' | 'protocol'>, channel?: ApiChannel): string {
  const protocol = effectiveModelProtocol(model, channel)
  return MODEL_PROTOCOLS.find((option) => option.value === protocol)?.label ?? protocol
}
