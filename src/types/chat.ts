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

/**
 * One ordered step in the assistant's reasoning trace (§7.1-4): either a run of
 * extended-thinking text or a single tool round. Kept in the exact order they
 * occurred so the UI can interleave "think → search → think → run" faithfully.
 */
export interface ReasoningThinking {
  kind: 'thinking'
  id: string
  text: string
}
export interface ReasoningTool {
  kind: 'tool'
  id: string
  tool: ToolCall
}
/**
 * Narration the model writes BEFORE a tool call ("Let me search the news…").
 * It belongs in the reasoning trace, not the final answer, so the answer stays
 * the clean last-round text. Captured by flushing in-progress answer text into
 * the trace when a tool starts (§4.3).
 */
export interface ReasoningNarration {
  kind: 'narration'
  id: string
  text: string
}
export type ReasoningItem = ReasoningThinking | ReasoningTool | ReasoningNarration

export interface Citation {
  id: string
  index: number
  title: string
  url: string
  domain: string
  snippet?: string
  /** Origin of the citation: 'web' for an external page, 'kb' for one of the
   *  user's own indexed documents (rendered as a non-link document chip). */
  source?: 'web' | 'kb'
}

/** Live (and persisted) state of a Deep Research turn (§ deep-research mode). */
export interface ResearchTask {
  id: string
  question: string
  status: 'pending' | 'researching' | 'partial' | 'done'
  round?: number
}
export interface ResearchSource {
  id: string
  url: string
  title: string
  domain: string
  status: 'found' | 'read' | 'kept' | 'failed'
  verdict?: string
}
export interface ResearchState {
  title: string
  tasks: ResearchTask[]
  sources: ResearchSource[]
  rounds?: number
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
  /** Ordered, interleaved reasoning trace — thinking runs + tool rounds in the
   *  exact order they happened (§7.1-4), so the UI can render them woven
   *  together instead of "all thinking, then all tools". */
  reasoning?: ReasoningItem[]
  /** Deep Research progress/state (plan, sources) for the research panel. */
  research?: ResearchState
  /** Turn mode — 'deep-research' renders the research panel + badge. */
  mode?: 'default' | 'deep-research' | 'canvas'
  /** Files produced by tools during this turn (downloadable). */
  artifacts?: ArtifactRef[]
  /** Set when the model declined to answer (content filter). */
  refused?: boolean
  /** Set when the turn failed (API/provider/stream error). Drives a red
   *  "reply failed — retry" banner + a retry (regenerate) button. */
  error?: string
  /** Set when the prompt was blocked by content moderation (§ moderation) —
   *  rendered as a prominent red notice asking the user to re-edit. */
  moderation?: boolean
  /** Set when the turn was refused because the user's group ran out of quota for
   *  this model (§ user groups) — rendered as a "quota reached → upgrade" card. */
  quotaExceeded?: boolean
  /** Model that generated this assistant message (§7.2-6 “由 … 生成”). */
  modelId?: string
  /** Snapshot of the model's display name at message creation time. Used as fallback when the model has been deleted. */
  modelLabel?: string
  /** When the user is editing a previously sent message. */
  editing?: boolean
  /** Reactions. */
  liked?: boolean
  disliked?: boolean
  /** Citations attached to this assistant turn. */
  citations?: Citation[]
  /** RAG retrieval lifecycle event surfaced live during streaming so the UI
   *  can render "📚 retrieved 4 sources from KB" or "Injected full document". */
  ragInjection?: { strategy: string; summary: string; at: number }
  /** Cost the user spent on this assistant turn (chat + tools + images). §8.3 */
  cost?: number
  /** Credits charged for this assistant turn (user-facing; undefined = none). */
  credits?: number
  /** Wall-clock generation time for this assistant turn, in ms. */
  genMs?: number
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
  /** Set when this is a text-selection sub-conversation anchored to a quoted
   *  excerpt of a message in another conversation. Such conversations are hidden
   *  from the sidebar / search and rendered in the inline-thread drawer. */
  inline?: { sourceConvId: string; messageId: string; quote: string }
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
