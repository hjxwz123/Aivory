// Package store — domain models and DTOs. Keep these flat so they map cleanly
// to both the SQLite schema and the JSON payloads sent over the API.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

// User is the row + profile shape returned to the frontend.
type User struct {
	ID       string          `json:"id"`
	Email    string          `json:"email"`
	Name     string          `json:"name"`
	Role     string          `json:"role"`
	Status   string          `json:"status"`
	TokenVer int             `json:"-"`
	Settings json.RawMessage `json:"settings"`
	GroupID  string          `json:"group_id"`
	// GroupExpiresAt is the unix seconds at which the current group_id
	// downgrades back to PreviousGroupID (or ug_free if empty). 0 = no expiry
	// (permanent membership, set by admin or by a redeem code with duration=0).
	GroupExpiresAt  int64  `json:"group_expires_at"`
	PreviousGroupID string `json:"previous_group_id"`
	// TotpSecret is never serialized to clients. TotpEnabled is exposed so the
	// account page can show the 2FA state (§ 2FA login).
	TotpSecret  string `json:"-"`
	TotpEnabled bool   `json:"totp_enabled"`
	// HasPassword is false for accounts created via OAuth that have never
	// chosen a password of their own. The client uses this to force a
	// set-password step (§ third-party login has no password).
	HasPassword bool `json:"has_password"`
	// PasswordChangedAt is the unix seconds of the user's last password change
	// (change / reset / first OAuth set-password). 0 = never changed since the
	// account was created — the account page shows a neutral message instead of
	// a fabricated time.
	PasswordChangedAt int64 `json:"password_changed_at"`
	// LastSeenAt is the unix seconds of the user's last authenticated activity,
	// updated (throttled) by the auth middleware. Drives the admin online status.
	LastSeenAt int64 `json:"last_seen_at"`
	// CreditsPermanent is the user's non-expiring credit balance (§ credits) —
	// bought via top-up or set by an admin. Debited only after timed credits run
	// out; never reset by the refresh cycle.
	CreditsPermanent float64 `json:"credits_permanent"`
	SortOrder        int     `json:"sort_order"`
	CreatedAt        int64   `json:"created_at"`
	// Features is the transient list of capability flags from the user's group
	// (e.g. "research"). Populated only on the /api/me response so the client can
	// gate features; never persisted on the users table.
	Features []string `json:"features,omitempty"`
	// GroupName is the transient display name of the user's membership group (the
	// "tier" label shown in the sidebar). Populated alongside Features on the
	// auth/me responses; never persisted on the users table.
	GroupName string `json:"group_name,omitempty"`
	// MemoryAvailable mirrors the GLOBAL admin `memory_enabled` master switch so the
	// client can show/hide the per-user memory toggle (when off, no one can enable
	// memory). Transient — populated alongside the group fields on auth/me; never
	// persisted on the users table.
	MemoryAvailable bool `json:"memory_available"`
	// Permissions is the normalized capability policy inherited from the user's
	// membership group. It is populated only on auth/me responses.
	Permissions UserGroupPermissions `json:"permissions"`
}

// UserGroup is a membership tier (§ user groups). Features is a JSON array of
// strings. MonthlyPriceAmountMinor and YearlyPriceAmountMinor use the
// deployment-wide settlement currency; SettlementCurrency is attached by the
// API and is not persisted per group.
type UserGroup struct {
	ID                      string          `json:"id"`
	Name                    string          `json:"name"`
	Description             string          `json:"description"`
	Features                json.RawMessage `json:"features"`
	MonthlyPriceAmountMinor int64           `json:"monthly_price_amount_minor"`
	YearlyPriceAmountMinor  int64           `json:"yearly_price_amount_minor"`
	SettlementCurrency      string          `json:"settlement_currency,omitempty"`
	IsDefault               bool            `json:"is_default"`
	SortOrder               int             `json:"sort_order"`
	// MaxProjects / MaxKBs cap how many projects / knowledge bases a member may
	// create (§ user groups). 0 = unlimited.
	MaxProjects int `json:"max_projects"`
	MaxKBs      int `json:"max_kbs"`
	// IsPublic controls whether the tier is listed on the public subscription
	// page (admins always see every group). Default true.
	IsPublic bool `json:"is_public"`
	// IsPurchasable controls whether members may start or resume payment for the
	// tier. A tier may remain public while purchases are temporarily paused.
	// Default true so existing tiers keep their current behavior.
	IsPurchasable bool `json:"is_purchasable"`
	// MaxStorageMB caps the total size of a member's non-image uploads
	// (files + KB documents), in MB. 0 = unlimited (§ user files page).
	MaxStorageMB int `json:"max_storage_mb"`
	// MaxWorkspaces caps how many workspaces a member may OWN (§workspaces).
	// 0 = unlimited; whether the group may create workspaces AT ALL is the
	// 'workspaces' feature flag inside Features (mirrors the research flag).
	MaxWorkspaces int `json:"max_workspaces"`
	// Credit system (§ credits). CreditAllowance is the timed-credit budget granted
	// each CreditPeriodSeconds cycle (unused voided on refresh). The USD→credit
	// rate is a global setting, not a per-group field.
	CreditAllowance     float64 `json:"credit_allowance"`
	CreditPeriodSeconds int     `json:"credit_period_seconds"`
	// Permissions is a JSON policy for group-scoped product capabilities and
	// resource allowlists. Empty/legacy values normalize to the permissive policy
	// so existing deployments retain their behavior after migration.
	Permissions UserGroupPermissions `json:"permissions"`
	CreatedAt   int64                `json:"created_at"`
	UpdatedAt   int64                `json:"updated_at"`
}

// ModelGroupQuota caps a group's usage of one model within a fixed window.
type ModelGroupQuota struct {
	ModelID       string  `json:"model_id"`
	GroupID       string  `json:"group_id"`
	PeriodSeconds int     `json:"period_seconds"`
	LimitType     string  `json:"limit_type"` // cost | count
	LimitValue    float64 `json:"limit_value"`
}

// Channel matches design.md §2.3-B.
type Channel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	APIFormat string `json:"api_format"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"-"`
	HasAPIKey bool   `json:"has_api_key"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sort_order"`
	UpdatedAt int64  `json:"updated_at"`
}

// Model mirrors design.md §2.3-B. Prices are per 1M tokens (chat/embedding)
// or per image.
type Model struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channel_id"`
	Kind        string `json:"kind"`
	RequestID   string `json:"request_id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	// FallbackChannelID is the backup channel retried when a request on the
	// primary channel fails (§fallback channel). '' = no fallback. The fallback
	// channel is expected to match the primary's type/format — only the endpoint
	// URL and API key differ.
	FallbackChannelID string `json:"fallback_channel_id"`
	Enabled           bool   `json:"enabled"`
	SortOrder         int    `json:"sort_order"`
	ToolMode          string `json:"tool_mode"`
	Vision            bool   `json:"vision"`
	Stream            bool   `json:"stream"`
	ResearchEnabled   bool   `json:"research_enabled"`
	// ResearchEnabledSet is an internal create-path marker: JSON booleans cannot
	// distinguish omitted from explicit false once decoded into Model.
	ResearchEnabledSet bool `json:"-"`
	// Fast marks THE fast model (§fast-mode). At most one model is fast at a time
	// (SetFastModel enforces it). A fast model is hidden from the advanced picker
	// and has Deep Research forced off.
	Fast          bool            `json:"fast"`
	SystemPrompt  string          `json:"system_prompt"`
	ParamControls json.RawMessage `json:"param_controls"`
	// ExtraParams is an admin-only JSON object merged into this model's upstream
	// request body. It is intentionally omitted from the public model response.
	// Native request fields and user-selected param controls take precedence.
	ExtraParams json.RawMessage `json:"extra_params"`
	// OfficialTools is a JSON array of provider-hosted tool definitions. Each
	// entry carries name/icon/request; legacy string arrays remain readable.
	// Empty means this model exposes no provider-hosted tools.
	OfficialTools json.RawMessage `json:"official_tools"`
	// BuiltinTools controls which locally-executed tools are selected by default.
	// nil/null selects all registered tools; an explicit [] selects none. Global
	// and group policy still cap availability when chat orchestration consumes it.
	BuiltinTools json.RawMessage `json:"builtin_tools"`
	// Tags is a JSON array of model_tags ids assigned to this model — used by the
	// model picker's tag filter (§ model tags). Empty = untagged.
	Tags json.RawMessage `json:"tags"`
	// Skills lists the skill ids bound to this model (model_skills join, §4.17).
	// NOT a column — populated on demand (admin model list) so the editor can show
	// current bindings. Omitted from JSON when not loaded.
	Skills []string `json:"skills,omitempty"`
	// ModerationEnabled screens each user prompt before generation (§ moderation).
	// ModerationMode picks the screen: "keyword" (match the admin keyword list)
	// or "model" (ask the configured moderation model for an ALLOW/BLOCK verdict).
	ModerationEnabled bool    `json:"moderation_enabled"`
	ModerationMode    string  `json:"moderation_mode"`
	PriceInput        float64 `json:"price_input"`
	PriceOutput       float64 `json:"price_output"`
	PriceCacheRead    float64 `json:"price_cache_read"`
	PriceCacheWrite   float64 `json:"price_cache_write"`
	PricePerImage     float64 `json:"price_per_image"`
	Currency          string  `json:"currency"`
	Dim               int     `json:"dim"`
	// CompactionTokenThreshold overrides the global automatic-compaction trigger
	// for this model, subject to the global cap. 0 uses the global default.
	CompactionTokenThreshold int `json:"compaction_token_threshold"`
	// ImageTimeoutSec caps a single image generation/edit request (§4.20). 0 =
	// use the default (no per-model cap; bounded only by the turn context).
	// Only meaningful for kind=image models.
	ImageTimeoutSec int   `json:"image_timeout_sec"`
	UpdatedAt       int64 `json:"updated_at"`
}

var ErrInvalidModelBilling = errors.New("invalid model billing configuration")

// ValidateModelBilling keeps the cost engine's unit explicit. The application
// has no FX-rate snapshot for provider usage, so accepting another currency and
// later multiplying the number as USD would silently misbill users.
func ValidateModelBilling(m *Model) error {
	if m == nil {
		return ErrInvalidModelBilling
	}
	prices := []float64{m.PriceInput, m.PriceOutput, m.PriceCacheRead, m.PriceCacheWrite, m.PricePerImage}
	for _, price := range prices {
		if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
			return ErrInvalidModelBilling
		}
	}
	currency := strings.ToUpper(strings.TrimSpace(m.Currency))
	if currency == "" {
		currency = "USD"
	}
	if currency != "USD" {
		return ErrInvalidModelBilling
	}
	m.Currency = currency
	return nil
}

// OAuthProvider is an admin-configured social/OAuth login method. The
// client_secret is never serialised (mirrors Channel.APIKey); HasSecret tells
// the admin UI whether a secret is on file without leaking it.
type OAuthProvider struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // google | github | apple | oidc | oauth2
	Name         string `json:"name"`
	Icon         string `json:"icon"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"-"`
	HasSecret    bool   `json:"has_secret"`
	IssuerURL    string `json:"issuer_url"`
	JWKSURL      string `json:"jwks_url"`
	AuthURL      string `json:"auth_url"`
	TokenURL     string `json:"token_url"`
	UserInfoURL  string `json:"userinfo_url"`
	Scopes       string `json:"scopes"`
	TeamID       string `json:"team_id"`
	KeyID        string `json:"key_id"`
	// SubjectNamespace is an internal trust-domain generation marker. It is
	// never exposed to clients; callbacks compare it with the effective config.
	SubjectNamespace string `json:"-"`
	Enabled          bool   `json:"enabled"`
	SortOrder        int    `json:"sort_order"`
	UpdatedAt        int64  `json:"updated_at"`
}

// MCPServer is an administrator-managed Streamable HTTP MCP endpoint.
// Headers may contain bearer tokens or other credentials and are deliberately
// excluded from JSON; API handlers must construct a masked response DTO.
// DiscoveredTools is the latest successful tools/list response snapshot.
type MCPServer struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Icon            string            `json:"icon"`
	Description     string            `json:"description"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"-"`
	Enabled         bool              `json:"enabled"`
	DiscoveredTools json.RawMessage   `json:"discovered_tools"`
	ProtocolVersion string            `json:"protocol_version"`
	LastError       string            `json:"last_error"`
	LastSyncedAt    int64             `json:"last_synced_at"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
}

// OAuthIdentity is one third-party identity bound to a local user (an
// oauth_identities row joined with its provider row), returned to the account
// page's "identity sources" section (§ identity linking). ClientSecret and the
// provider subject are safe to expose here — the subject is the provider's own
// public account id, not a credential.
type OAuthIdentity struct {
	ProviderID   string `json:"provider_id"`
	Subject      string `json:"subject"`
	Email        string `json:"email"`
	CreatedAt    int64  `json:"created_at"`
	ProviderName string `json:"provider_name"`
	ProviderKind string `json:"provider_kind"`
	ProviderIcon string `json:"provider_icon"`
	// ProviderEnabled is false when the admin has since disabled the provider:
	// the binding still shows (so it can be removed) but can't be used to log in.
	ProviderEnabled bool `json:"provider_enabled"`
}

// Skill is the §4.17 record. Assets carry references to template files.
type Skill struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	DisplayDescription string          `json:"display_description"`
	Icon               string          `json:"icon"`
	Instructions       string          `json:"instructions"`
	Assets             json.RawMessage `json:"assets"`
	Enabled            bool            `json:"enabled"`
	SortOrder          int             `json:"sort_order"`
	UpdatedAt          int64           `json:"updated_at"`
}

// Prompt is an administrator-managed prompt template published in the shared
// catalog. Content is only returned by administrator endpoints; the user-facing
// catalog exposes display metadata and copies content into a user-owned row.
type Prompt struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon,omitempty"`
	Content     string `json:"content"`
	Enabled     bool   `json:"enabled"`
	SortOrder   int    `json:"sort_order"`
	UpdatedAt   int64  `json:"updated_at"`
}

// UserSkill is a private, instruction-only Agent Skill. Unlike administrator
// skills it has no assets, storage paths, or sandbox staging surface.
type UserSkill struct {
	ID                 string `json:"id"`
	UserID             string `json:"-"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	DisplayDescription string `json:"display_description,omitempty"`
	Icon               string `json:"icon"`
	Instructions       string `json:"instructions"`
	SourceSkillID      string `json:"source_skill_id,omitempty"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

// UserPrompt is a private prompt template. A catalog copy is independent from
// its administrator source and remains after that source is deleted.
type UserPrompt struct {
	ID             string `json:"id"`
	UserID         string `json:"-"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Content        string `json:"content"`
	SourcePromptID string `json:"source_prompt_id,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// Project — §4.14.
type Project struct {
	ID                 string `json:"id"`
	UserID             string `json:"user_id"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	Instructions       string `json:"instructions"`
	Accent             string `json:"accent"`
	Emoji              string `json:"emoji"`
	Pinned             bool   `json:"pinned"`
	KBID               string `json:"kb_id"`
	KBEmbeddingModelID string `json:"kb_embedding_model_id,omitempty"`
	KBEmbeddingDim     int    `json:"kb_embedding_dim,omitempty"`
	AutoAddUploads     bool   `json:"auto_add_uploads"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
	// '' = personal; set = shared with the workspace's members (§workspaces).
	WorkspaceID string `json:"workspace_id"`
}

// Conversation — §5 conversations row. kb_ids/summary_blocks/provider_state
// are kept as raw JSON to round-trip through SQLite cleanly.
type Conversation struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Provider  string `json:"provider"`
	ModelID   string `json:"model_id"`
	// Fast marks the conversation as running in fast mode (§fast-mode): the model
	// is resolved server-side from the admin's fast model and never named to the
	// user. ModelID keeps the user's advanced pick (used when they switch back to
	// 进阶), but a fast turn ignores it and does NOT write the fast model onto it.
	Fast          bool            `json:"fast"`
	KBIDs         json.RawMessage `json:"kb_ids"`
	RAGMode       string          `json:"rag_mode"`
	SummaryBlocks json.RawMessage `json:"summary_blocks"`
	ActiveLeafID  string          `json:"active_leaf_id"`
	ProviderState json.RawMessage `json:"provider_state"`
	Pinned        bool            `json:"pinned"`
	Archived      bool            `json:"archived"`
	Starred       bool            `json:"starred"`
	CreatedAt     int64           `json:"created_at"`
	UpdatedAt     int64           `json:"updated_at"`
	// Inline-thread linkage (§ text-selection sub-conversations). When set, this
	// conversation is a sub-conversation anchored to a quoted excerpt of a
	// message in another conversation; it is hidden from the normal list and its
	// quote is injected as system context. Empty for ordinary conversations.
	InlineSourceConv string `json:"inline_source_conv"`
	InlineParentID   string `json:"inline_parent_id"`
	InlineQuote      string `json:"inline_quote"`
	// Workspace linkage (§workspaces). '' = personal conversation; user_id stays
	// the creator when set. Workspace conversations are creator-private unless
	// IsPublic is enabled explicitly.
	WorkspaceID string `json:"workspace_id"`
	IsPublic    bool   `json:"is_public"`
	// Enriched for workspace listings (not columns): creator display identity so
	// the sidebar can label who started each shared conversation.
	CreatorName   string `json:"creator_name,omitempty"`
	CreatorAvatar string `json:"creator_avatar,omitempty"`
}

// Message — flat record over §5 messages. blocks/raw/attachments/citations are
// JSON-encoded so the handler layer can decode/encode without a per-shape DAO.
type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	ParentID       string `json:"parent_id"`
	Role           string `json:"role"`
	Provider       string `json:"provider"`
	ModelID        string `json:"model_id"`
	ModelLabel     string `json:"model_label"`
	// Fast marks a turn that ran in fast mode (§fast-mode). The row keeps the REAL
	// model_id/model_label/provider (for billing + admin drill-down); the user
	// boundary (redactCost) blanks that identity and the client renders "快速".
	Fast        bool            `json:"fast"`
	Blocks      json.RawMessage `json:"blocks"`
	Raw         json.RawMessage `json:"raw,omitempty"`
	StopReason  string          `json:"stop_reason"`
	Attachments json.RawMessage `json:"attachments"`
	Citations   json.RawMessage `json:"citations"`
	InputTokens int             `json:"input_tokens"`
	// ContextTokens is the final successful upstream request's prompt footprint,
	// including cache-read tokens. 0 means the provider did not report usage.
	ContextTokens    int     `json:"context_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	Cost             float64 `json:"cost"`
	Currency         string  `json:"currency"`
	// Credits charged to the user for this turn (0 = free / credits disabled).
	// Unlike Cost (USD spend, admin-only), credits ARE the user-facing currency,
	// so this is surfaced to the user and not redacted.
	Credits         float64  `json:"credits"`
	Status          string   `json:"status"`
	Error           string   `json:"error"`
	Feedback        string   `json:"feedback"`         // "" | "like" | "dislike" (§ message feedback)
	FeedbackReasons []string `json:"feedback_reasons"` // current authenticated user's optional dislike reasons
	FeedbackComment string   `json:"feedback_comment"` // current authenticated user's optional note
	// GenMs is the wall-clock time the assistant turn took to generate (ms).
	GenMs int64 `json:"gen_ms"`
	// Verify holds the secondary-auditor result (Verify mode, §verify) for this
	// assistant turn: JSON {verdict,findings:[{severity,quote,issue}],...}.
	// Omitted from the wire when the turn was never audited.
	Verify    json.RawMessage `json:"verify,omitempty"`
	CreatedAt int64           `json:"created_at"`
	// AuthorID records WHO wrote a user turn (§workspaces — shared conversations
	// attribute each question). '' on legacy rows and assistant turns: the
	// conversation creator is the implied author.
	AuthorID string `json:"author_id,omitempty"`
	// SelectedUserSkillIDs records the private skills applied to this user turn.
	// The instruction bodies are never elevated into the system prompt; the
	// orchestrator resolves these ids under the message author's ownership and
	// appends their content to the last user-role history entry.
	SelectedUserSkillIDs json.RawMessage `json:"-"`
}

// KnowledgeBase — §5 knowledge_bases row.
type KnowledgeBase struct {
	ID               string `json:"id"`
	UserID           string `json:"user_id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	EmbeddingModelID string `json:"embedding_model_id"`
	EmbeddingDim     int    `json:"embedding_dim"`
	ProjectID        string `json:"project_id"`
	CreatedAt        int64  `json:"created_at"`
	// '' = personal; set = shared with the workspace's members (§workspaces).
	WorkspaceID string `json:"workspace_id"`
	// AccessRole is transient and user-relative: owner, read, write, or workspace.
	// It is populated by user-facing list/get queries and never persisted.
	AccessRole       string `json:"access_role,omitempty"`
	OwnerName        string `json:"owner_name,omitempty"`
	CanShare         bool   `json:"can_share"`
	CanUpload        bool   `json:"can_upload"`
	CanDelete        bool   `json:"can_delete"`
	CanDeleteContent bool   `json:"can_delete_content"`
	CanManageMembers bool   `json:"can_manage_members"`
}

// KnowledgeBaseShare grants another user read-only or collaborative access to
// one personal knowledge base. Workspace knowledge bases never use this table.
type KnowledgeBaseShare struct {
	KBID      string `json:"kb_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"` // read | write
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// KnowledgeBaseUploader is the identity-only shape used by document uploader
// filters. It deliberately does not pretend to carry a share role or share
// timestamps.
type KnowledgeBaseUploader struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// WorkspaceKnowledgeBaseMemberPermission is a per-library override shown only
// to that workspace KB's creator or the workspace owner. The two Total fields
// are read-only upper bounds from workspace_members.
type WorkspaceKnowledgeBaseMemberPermission struct {
	KBID                    string `json:"kb_id"`
	UserID                  string `json:"user_id"`
	Role                    string `json:"role"`
	Name                    string `json:"name"`
	Email                   string `json:"email"`
	AvatarURL               string `json:"avatar_url,omitempty"`
	CanAddFiles             bool   `json:"can_add_files"`
	CanDeleteContent        bool   `json:"can_delete_content"`
	TotalCanAddKBFiles      bool   `json:"total_can_add_kb_files"`
	TotalCanDeleteKBContent bool   `json:"total_can_delete_kb_content"`
	Locked                  bool   `json:"locked"`
}

// Document — §5 documents row. status: pending|parsing|embedding|ready|failed.
type Document struct {
	ID               string `json:"id"`
	KBID             string `json:"kb_id"`
	ConversationID   string `json:"conversation_id"`
	Filename         string `json:"filename"`
	MimeType         string `json:"mime_type"`
	SizeBytes        int64  `json:"size_bytes"`
	Status           string `json:"status"`
	Error            string `json:"error"`
	ChunkCount       int    `json:"chunk_count"`
	StoragePath      string `json:"-"`
	UploadedByUserID string `json:"uploaded_by_user_id"`
	UploadedByName   string `json:"uploaded_by_name"`
	UploadedByEmail  string `json:"uploaded_by_email,omitempty"`
	CanDelete        bool   `json:"can_delete"`
	CreatedAt        int64  `json:"created_at"`
}

// Memory — §4.16 row.
type Memory struct {
	ID               string   `json:"id"`
	UserID           string   `json:"user_id"`
	MemoryText       string   `json:"memory_text"`
	MemoryType       string   `json:"memory_type"`
	Slot             string   `json:"slot"`
	Value            string   `json:"value"`
	Status           string   `json:"status"`
	Confidence       float64  `json:"confidence"`
	SourceMessageIDs []string `json:"source_message_ids"`
	Supersedes       []string `json:"supersedes"`
	SupersededBy     []string `json:"superseded_by"`
	AffectedDomains  []string `json:"affected_domains"`
	Reason           string   `json:"reason"`
	ValidFrom        int64    `json:"valid_from"`
	ValidUntil       int64    `json:"valid_until"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
}

// UsageLog — §8.3 row.
type UsageLog struct {
	ID               int64   `json:"id"`
	UserID           string  `json:"user_id"`
	ConversationID   string  `json:"conversation_id"`
	MessageID        string  `json:"message_id"`
	ModelID          string  `json:"model_id"`
	Purpose          string  `json:"purpose"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	ImagesCount      int     `json:"images_count"`
	Cost             float64 `json:"cost"`
	Currency         string  `json:"currency"`
	// Credits charged for this row (§ credits). 0 = free (within the model's
	// per-group free count) or credits disabled.
	Credits   float64 `json:"credits"`
	CreatedAt int64   `json:"created_at"`
	// WorkspaceID attributes spend to a workspace conversation (§workspaces).
	// '' = personal. The PAYER stays user_id (members burn their OWN quota).
	WorkspaceID string `json:"workspace_id,omitempty"`
	// ChannelID records which channel actually served the request (§fallback
	// channel). Fallback is true when the model's backup channel was used because
	// the primary failed. Status is "ok" | "error"; error requests are logged too
	// so the admin usage page can count failures.
	ChannelID string `json:"channel_id,omitempty"`
	Fallback  bool   `json:"fallback,omitempty"`
	Status    string `json:"status,omitempty"`
	// TTFTFallbackModel is the display name of the model a TTFT timeout-fallback
	// switched to for this row (§4.6-C). '' = no model fallback. Distinct from
	// Fallback (that is the same-model backup-channel retry).
	TTFTFallbackModel string `json:"ttft_fallback_model,omitempty"`
	// Error is the upstream failure detail for status='error' rows (admin-only;
	// may embed provider response bodies, so it is never returned to end users).
	Error          string `json:"-"`
	RequestMethod  string `json:"-"`
	RequestURL     string `json:"-"`
	RequestHeaders string `json:"-"`
	RequestBody    string `json:"-"`
}

// File — uploaded file metadata.
type File struct {
	ID             string `json:"id"`
	UserID         string `json:"user_id"`
	ConversationID string `json:"conversation_id"`
	Filename       string `json:"filename"`
	MimeType       string `json:"mime_type"`
	SizeBytes      int64  `json:"size_bytes"`
	Kind           string `json:"kind"`
	// Draft is true for a composer upload that has not yet been committed to a
	// user message. Conversation-file drawer uploads are immediately committed.
	Draft       bool   `json:"draft"`
	StoragePath string `json:"-"`
	// URL is filled by the handler (not the DB) so the frontend can render
	// thumbnails / download links without keeping the blob URL alive.
	URL string `json:"url,omitempty"`
	// DocumentID is filled by the handler (not the DB) when the upload also
	// created a conversation-scoped RAG document, so the client can poll that
	// document's ingest status before sending its first question (§ chat uploads).
	DocumentID string `json:"document_id,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

// Helper: read settings value as JSON. Backed by a short-TTL process-local
// cache (§2.4) — this is one of the hottest reads in the server.
func GetSetting(db *sql.DB, key string) (json.RawMessage, error) {
	if val, missing, ok := settingsCacheGet(key); ok {
		if missing {
			return nil, sql.ErrNoRows
		}
		return val, nil
	}
	var raw string
	err := db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			settingsCachePut(key, nil, true) // negative cache absent keys
		}
		return nil, err
	}
	settingsCachePut(key, json.RawMessage(raw), false)
	return json.RawMessage(raw), nil
}

// SetSetting writes the JSON-encoded value (overwrites). If the key did not
// exist before, the row is created. Invalidates the cache entry on this
// instance (other instances clear via the cfg:invalidate Pub/Sub).
func SetSetting(db *sql.DB, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, string(b), time.Now().Unix())
	if err == nil {
		invalidateSettingKey(key)
	}
	return err
}
