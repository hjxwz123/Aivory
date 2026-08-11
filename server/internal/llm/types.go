// Package llm holds the Provider adapter interface and the chat orchestrator
// described in design.md §4.3.
//
// The shape here is deliberately the same as design.md: providers consume the
// `UnifiedChatRequest` and emit `SseEvent`s, the orchestrator drives the loop,
// tool execution is decoupled, and the storage layer never sees provider-
// specific shapes.
package llm

import (
	"encoding/json"
	"sync/atomic"
)

// UnifiedBlock is the canonical message-block shape stored in DB (§2.3-C).
type UnifiedBlock struct {
	Kind     string          `json:"kind"` // text | thinking | tool_call | tool_output | citation | image | document | artifact
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	ToolID   string          `json:"tool_id,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Summary  string          `json:"summary,omitempty"`
	URL      string          `json:"url,omitempty"`
	Title    string          `json:"title,omitempty"`
	FileRef  string          `json:"file_ref,omitempty"`
	// Data carries base64 payloads for image blocks built from user
	// attachments (§4.6); MimeType qualifies it. Document attachments are
	// parsed/OCRed by RAG and injected as text, not sent as provider file blocks.
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	// Artifacts lists files this block references (§2.3-C ArtifactRef).
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
}

// Attachment is a user-side file reference attached to a message.
type Attachment struct {
	ID         string `json:"id"`
	DocumentID string `json:"document_id,omitempty"`
	Filename   string `json:"filename"`
	MimeType   string `json:"mime_type"`
	Kind       string `json:"kind"`
	URL        string `json:"url"`
}

// Citation is the cross-source citation type used by web search tools and RAG.
type Citation struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"` // web | kb
}

// SystemPart lets the orchestrator pass extra structured context (project
// instructions, memory snippet, file index) without hardcoding strings.
type SystemPart struct {
	Label string
	Text  string
}

// UnifiedChatRequest is what the orchestrator hands to a Provider.
type UnifiedChatRequest struct {
	UserID         string
	ConversationID string
	// MessageID is the assistant message being filled.
	MessageID    string
	ProjectName  string
	SystemPrompt string // pre-built (§4.8)
	// SystemPromptOptions retains the structured inputs used to build
	// SystemPrompt. Providers ignore it; TTFT model fallback uses a cloned copy to
	// recompose tool/skill guidance for the fallback model's exact capabilities.
	SystemPromptOptions *systemPromptOpts
	// SystemExtras carries optional debug-only inspection.
	SystemExtras []SystemPart
	History      []UnifiedMessage
	Model        ModelInfo
	Tools        []ToolDef
	// OfficialToolNames and OfficialToolRequests are the selected subset of the
	// resolved model's administrator-configured provider-hosted tools, retained in
	// administrator order. The historical field names match `official_tools`; no
	// client-supplied request fragment reaches this collection.
	OfficialToolNames    []string
	OfficialToolRequests []json.RawMessage
	// SelectedToolIDs preserves the user's per-model candidate subset across a
	// transparent TTFT fallback. SelectedToolsConfigured distinguishes omitted
	// (all available tools) from an explicit empty selection.
	SelectedToolIDs         []string
	SelectedToolsConfigured bool
	// ToolsEnabled records the resolved turn policy independently of whether the
	// primary model happens to configure any tools. TTFT fallback uses it to rebuild
	// the fallback model's complete administrator-configured tool collection.
	ToolsEnabled bool
	// Fast preserves the fixed fast-mode safety restrictions when rebuilding a
	// request for a TTFT fallback model.
	Fast bool
	// ToolModePrompt is true when §4.13 prompt-injection mode is on.
	ToolModePrompt bool
	ProjectFiles   []ProjectFileSummary
	RAGSnippets    []Citation
	// ParamOverrides carries user-selected param_controls values.
	ParamOverrides map[string]any
	// ParamControls is the raw model.param_controls JSON, used by providers
	// (and the deep-merge helper) to know which keys are whitelisted and how
	// each value maps to upstream parameters (§2.3-G).
	ParamControls json.RawMessage
	// ExtraParams is the admin-only JSON object of provider request defaults for
	// this model. Providers apply it below selected param controls and native
	// request fields, so it cannot replace model, messages, stream, or tools.
	ExtraParams json.RawMessage
	Stream      bool
	// MaxOutputTokens overrides the provider's default max_tokens cap.
	// Used by TaskLLM for short internal calls.
	MaxOutputTokens int
	// FallbackUsed, when non-nil, is set by the provider the first time it serves
	// ANY request in this turn — including a tool-loop round — through
	// Model.Fallback. Once set, later rounds stay on that channel. The
	// orchestrator reads it after Stream to flag the turn's usage row as fallback
	// (§fallback channel). nil = untracked.
	FallbackUsed *atomic.Bool
}

// ModelInfo is the slim subset of store.Model the provider needs.
type ModelInfo struct {
	ID        string
	RequestID string
	Provider  string
	Vision    bool
	BaseURL   string
	APIKey    string
	APIFormat string
	// Fallback, when non-nil, is the backup endpoint retried when a complete
	// primary round fails before user-visible output is committed (request
	// construction, transport, non-200 status, SSE
	// error, malformed protocol, empty body, or interrupted body). Same provider
	// family + format as the primary — only URL and key differ (§fallback
	// channel). nil = no fallback.
	Fallback *ChannelCreds
}

// ChannelCreds is the alternate endpoint used for the fallback retry. Only the
// base URL + API key differ from the primary channel.
type ChannelCreds struct {
	BaseURL string
	APIKey  string
}

// UnifiedMessage is the chronological message used as conversation history.
type UnifiedMessage struct {
	Role        string         `json:"role"` // user | assistant | system | tool
	Blocks      []UnifiedBlock `json:"blocks"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	// Raw carries the provider-native exchange recorded when this assistant
	// message was generated (§2.3-C). Set ONLY when the stored provider matches
	// the current channel — providers replay it verbatim for full fidelity.
	Raw json.RawMessage `json:"-"`
}

// ProjectFileSummary is the file-index entry surfaced to the model.
type ProjectFileSummary struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Excerpt string `json:"excerpt,omitempty"`
}

// ToolDef is the tool descriptor exposed to providers.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// MCPToolDef is one executable Function discovered from an administrator-
// configured MCP service. ServerID groups multiple remote methods behind the
// single service-level item presented in the user tool picker.
type MCPToolDef struct {
	ToolDef
	ServerID           string
	DisplayName        string
	DisplayDescription string
	Icon               string
}

// SseEvent is the on-the-wire shape per §6.2. Always lowercase, snake_case.
type SseEvent struct {
	Type      string `json:"type"`
	MessageID string `json:"message_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Name      string `json:"name,omitempty"`
	ID        string `json:"id,omitempty"`
	// PartialJson streams incremental tool-input JSON fragments (§6.2
	// tool_input.partialJson).
	PartialJson string          `json:"partial_json,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	URL         string          `json:"url,omitempty"`
	Title       string          `json:"title,omitempty"`
	Citation    *Citation       `json:"citation,omitempty"`
	StopReason  string          `json:"stop_reason,omitempty"`
	Usage       *Usage          `json:"usage,omitempty"`
	Message     string          `json:"message,omitempty"`
	Code        string          `json:"code,omitempty"`
	ToolID      string          `json:"tool_id,omitempty"`
	Status      string          `json:"status,omitempty"`
	// Credits charged for this turn (emitted on the `done` event so the UI can
	// show "credits used"). 0 = free / credits disabled.
	Credits float64 `json:"credits,omitempty"`
	// Verdict + Finding carry Verify-mode (§verify) audit results: `verify_done`
	// sends the overall verdict ("clean"|"issues"); `verify_finding` sends one
	// finding at a time so the UI builds the report live; `verify_started` carries
	// neither (just the message_id).
	Verdict string         `json:"verdict,omitempty"`
	Finding *VerifyFinding `json:"finding,omitempty"`
}

// VerifyFinding is one issue the auditor model (Verify mode, §verify) flagged in
// the primary answer: a verbatim quote of the offending sentence + the problem,
// with a severity. Shared by the SSE wire, the persisted message.verify JSON,
// and the verifyReport.
type VerifyFinding struct {
	Severity string `json:"severity"` // "error" | "warning" | "note"
	Quote    string `json:"quote"`
	Issue    string `json:"issue"`
}

// ArtifactRef is a file a tool produced (sandbox output, generated image). The
// orchestrator persists it (artifacts table) and streams an "artifact" event.
type ArtifactRef struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

// Usage tracks token + cache counts.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// GeneratedImage is provider-produced binary image output that has not yet
// been persisted as an application artifact. Local image_generate calls save
// their own artifacts; hosted provider tools (OpenAI Responses image_generation)
// return these to the orchestrator so both paths share the same durable output
// and SSE contract without embedding multi-megabyte base64 payloads in blocks.
type GeneratedImage struct {
	Data     []byte
	MimeType string
	SourceID string
}

// UnifiedResult is what the provider returns after the loop terminates.
type UnifiedResult struct {
	Blocks          []UnifiedBlock
	Raw             json.RawMessage
	StopReason      string
	Usage           Usage
	Citations       []Citation
	GeneratedImages []GeneratedImage
}
