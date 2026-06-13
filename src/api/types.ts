/**
 * Wire-format types shared between the Go backend and the frontend. Keep
 * field names snake_case to match the backend JSON tags directly — frontend
 * code uses helpers in `lib/format.ts` to convert to display strings when
 * needed.
 */

export interface ApiError {
  error: string
}

export interface ApiUser {
  id: string
  email: string
  name: string
  role: 'user' | 'admin'
  status: 'active' | 'banned' | 'disabled'
  settings: Record<string, unknown>
  created_at: number
}

export interface ApiAuthResponse {
  user: ApiUser
  access_token: string
  expires_at: number
}

export interface ApiChannel {
  id: string
  name: string
  type: 'openai' | 'claude' | 'gemini' | 'mock'
  api_format: 'chat' | 'responses' | ''
  base_url: string
  has_api_key: boolean
  enabled: boolean
  sort_order: number
  updated_at: number
}

export interface ApiModel {
  id: string
  channel_id: string
  kind: 'chat' | 'image' | 'embedding'
  request_id: string
  label: string
  description: string
  icon: string
  enabled: boolean
  sort_order: number
  tool_mode: 'native' | 'prompt' | 'none'
  vision: boolean
  stream: boolean
  system_prompt: string
  param_controls: unknown
  /** OpenAI Responses hosted tools to enable; empty/absent = use system tools (§2.3-B). */
  official_tools?: string[]
  price_input: number
  price_output: number
  price_cache_read: number
  price_cache_write: number
  price_per_image: number
  currency: string
  dim: number
  updated_at: number
}

export interface ApiSkill {
  id: string
  name: string
  description: string
  icon: string
  instructions: string
  assets: unknown
  enabled: boolean
  sort_order: number
  updated_at: number
}

export interface ApiProject {
  id: string
  user_id: string
  name: string
  description: string
  instructions: string
  accent: 'violet' | 'sage' | 'amber' | 'rose' | 'slate' | 'teal'
  emoji: string
  pinned: boolean
  kb_id: string
  auto_add_uploads: boolean
  created_at: number
  updated_at: number
}

export interface ApiKnowledgeBase {
  id: string
  user_id: string
  name: string
  description: string
  embedding_model_id: string
  embedding_dim: number
  project_id: string
  created_at: number
}

export interface ApiDocument {
  id: string
  kb_id: string
  conversation_id: string
  filename: string
  mime_type: string
  size_bytes: number
  status: 'pending' | 'parsing' | 'embedding' | 'ready' | 'failed'
  error: string
  chunk_count: number
  created_at: number
}

export interface ApiConversation {
  id: string
  user_id: string
  project_id: string
  title: string
  provider: string
  model_id: string
  kb_ids: string[]
  rag_mode: 'auto' | 'inject' | 'tool'
  summary_blocks: unknown[]
  active_leaf_id: string
  provider_state: Record<string, unknown>
  pinned: boolean
  archived: boolean
  starred: boolean
  created_at: number
  updated_at: number
}

export type ApiBlockKind =
  | 'text'
  | 'thinking'
  | 'tool_call'
  | 'tool_output'
  | 'citation'
  | 'image'
  | 'document'
  | 'artifact'

export interface ApiBlock {
  kind: ApiBlockKind
  text?: string
  tool_name?: string
  tool_id?: string
  input?: unknown
  summary?: string
  url?: string
  title?: string
  file_ref?: string
}

export interface ApiAttachment {
  id: string
  filename: string
  mime_type: string
  kind: string
  url: string
}

export interface ApiCitation {
  id: string
  index: number
  title: string
  url: string
  snippet: string
  source: 'web' | 'kb'
}

export interface ApiMessage {
  id: string
  conversation_id: string
  parent_id: string
  role: 'user' | 'assistant' | 'system'
  provider: string
  model_id: string
  blocks: ApiBlock[]
  attachments: ApiAttachment[]
  citations: ApiCitation[]
  stop_reason: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost: number
  currency: string
  status: 'streaming' | 'complete' | 'error'
  error: string
  created_at: number
  /** Sibling navigation (only on the path response). */
  branch_index?: number
  branch_count?: number
  siblings?: string[]
}

export interface ApiMemory {
  id: string
  user_id: string
  memory_text: string
  memory_type: string
  slot: string
  value: string
  status: 'ACTIVE' | 'STALE' | 'UNKNOWN_CURRENT' | 'HISTORICAL_ONLY' | 'QUERY_DEPENDENT'
  confidence: number
  source_message_ids: string[]
  supersedes: string[]
  superseded_by: string[]
  affected_domains: string[]
  reason: string
  valid_from: number
  valid_until: number
  created_at: number
  updated_at: number
}

export interface ApiUsageReportRow {
  user_id: string
  user_email: string
  model_id: string
  purpose: string
  input_tokens: number
  output_tokens: number
  calls: number
  cost: number
  currency: string
}

/** SSE event shapes — matches §6.2. */
export type ApiSseEvent =
  | { type: 'message_start'; message_id: string }
  | { type: 'thinking_delta'; text: string }
  | { type: 'text_delta'; text: string }
  | { type: 'tool_start'; name: string; id?: string; input?: unknown }
  | { type: 'tool_input'; name?: string; id?: string; partial_json?: string; input?: unknown }
  | { type: 'tool_result'; name: string; id?: string; summary: string; status?: 'complete' | 'error' }
  | { type: 'citation'; citation: ApiCitation }
  | { type: 'artifact'; id?: string; url?: string; title?: string; summary?: string }
  | { type: 'rag'; status?: string; summary?: string }
  | { type: 'refusal'; message_id?: string; message?: string }
  | { type: 'error'; message: string }
  | { type: 'done'; stop_reason?: string; usage?: { input_tokens: number; output_tokens: number } }
