/**
 * Core chat types — shared by mock runtime, store, and UI.
 * Designed to be backend-agnostic; a future real adapter can populate
 * these directly without UI changes.
 */

export type MessageRole = 'user' | 'assistant' | 'system'

export interface ToolCall {
  id: string
  /** Symbolic tool name, e.g. "web_search", "code_interpreter" */
  name: string
  /** Free-form display label, e.g. "Searching the web" */
  label: string
  /** Tool-specific input arguments */
  input?: Record<string, unknown>
  /** Streaming or final output */
  output?: string
  status: 'running' | 'complete' | 'error'
  startedAt: number
  endedAt?: number
}

export interface Citation {
  id: string
  index: number
  title: string
  url: string
  domain: string
  snippet?: string
}

export interface Attachment {
  id: string
  /** Display name */
  name: string
  /** Mime-type bucket: image, pdf, doc, sheet, code, other */
  kind: 'image' | 'pdf' | 'doc' | 'sheet' | 'code' | 'other'
  /** Approximate size in bytes; just for display */
  size: number
  /** Optional preview URL (data URL for mock) */
  previewUrl?: string
}

/** A file a tool produced (sandbox output, generated image), §4.5/§4.12. */
export interface ArtifactRef {
  id: string
  filename: string
  url: string
  mimeType: string
}

export interface Message {
  id: string
  /** Parent id in the conversation tree (§4.15). Empty for root. Needed by the
   *  composer so "edit a past question" opens a sibling branch under the same
   *  parent instead of appending to the active leaf. */
  parentId?: string
  role: MessageRole
  /** Rendered content. For user this is plain text; for assistant this is markdown. */
  content: string
  createdAt: number
  /** True while the model is producing tokens. */
  streaming?: boolean
  /** Extended-thinking text (collapsible), accumulated from thinking_delta. */
  thinking?: string
  /** Files produced by tools during this turn (downloadable). */
  artifacts?: ArtifactRef[]
  /** Set when the model declined to answer (content filter). */
  refused?: boolean
  /** Model that generated this assistant message (§7.2-6 “由 … 生成”). */
  modelId?: string
  /** When the user is editing a previously sent message. */
  editing?: boolean
  /** Reactions. */
  liked?: boolean
  disliked?: boolean
  /** Tool calls that ran during this assistant turn. */
  toolCalls?: ToolCall[]
  /** Citations attached to this assistant turn. */
  citations?: Citation[]
  /** RAG retrieval lifecycle event surfaced live during streaming so the UI
   *  can render "📚 retrieved 4 sources from KB" or "Injected full document". */
  ragInjection?: { strategy: string; summary: string; at: number }
  /** Cost the user spent on this assistant turn (chat + tools + images). §8.3 */
  cost?: number
  /** Currency code for `cost`, e.g. "USD" or "CNY". */
  currency?: string
  /** Attachments on a user turn. */
  attachments?: Attachment[]
  /** Branch index when message has alternates. */
  branchIndex?: number
  /** Total branches for this position. */
  branchCount?: number
  /** Sibling message ids at this branch position (parents share). */
  siblings?: string[]
}

export interface Conversation {
  id: string
  title: string
  createdAt: number
  updatedAt: number
  modelId: string
  pinned?: boolean
  starred?: boolean
  archived?: boolean
  /** Project this conversation belongs to. Free chats leave this undefined. */
  projectId?: string
  /** Last param_controls selection, remembered so regenerate reuses it (§2.3-G). */
  lastParams?: Record<string, unknown>
  /** Knowledge bases bound to this conversation (§7.2-7 composer 📚 selector). */
  kbIds?: string[]
  messages: Message[]
}

/**
 * Compact project context passed into the runtime so the assistant can
 * "see" the project's instructions + file index without the UI having to
 * resend the whole project payload every turn.
 */
export interface ProjectContext {
  id: string
  name: string
  instructions: string
  files: Array<{ id: string; name: string; kind: string; excerpt?: string }>
}

export interface SendMessageInput {
  conversationId: string
  text: string
  modelId: string
  attachments?: Attachment[]
  /** Conversation history shape; passed by store. */
  history: Message[]
  /** Optional armed mode such as "deep-research" or "canvas". */
  mode?: 'default' | 'deep-research' | 'canvas'
  /** Resolved project context for project-scoped conversations. */
  project?: ProjectContext
  /** Abort signal for stopping a stream. */
  signal?: AbortSignal
}

export type MessageChunk =
  | { type: 'text'; delta: string }
  | { type: 'tool_call'; toolCall: ToolCall }
  | { type: 'tool_update'; id: string; output?: string; status?: ToolCall['status'] }
  | { type: 'citation'; citation: Citation }
  | { type: 'done' }
  | { type: 'error'; message: string }
