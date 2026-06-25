// Package store — domain models and DTOs. Keep these flat so they map cleanly
// to both the SQLite schema and the JSON payloads sent over the API.
package store

import (
	"database/sql"
	"encoding/json"
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
}

// UserGroup is a membership tier (§ user groups). Features is a JSON array of
// strings; prices are display-only.
type UserGroup struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Features    json.RawMessage `json:"features"`
	PriceUSD    float64         `json:"price_usd"`
	PriceCNY    float64         `json:"price_cny"`
	IsDefault   bool            `json:"is_default"`
	SortOrder   int             `json:"sort_order"`
	// MaxProjects / MaxKBs cap how many projects / knowledge bases a member may
	// create (§ user groups). 0 = unlimited.
	MaxProjects int `json:"max_projects"`
	MaxKBs      int `json:"max_kbs"`
	// Credit system (§ credits). CreditAllowance is the timed-credit budget granted
	// each CreditPeriodSeconds cycle (unused voided on refresh). The USD→credit
	// rate and both purchase links are global settings, not per-group fields.
	CreditAllowance     float64 `json:"credit_allowance"`
	CreditPeriodSeconds int     `json:"credit_period_seconds"`
	CreatedAt           int64   `json:"created_at"`
	UpdatedAt           int64   `json:"updated_at"`
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
	ID            string          `json:"id"`
	ChannelID     string          `json:"channel_id"`
	Kind          string          `json:"kind"`
	RequestID     string          `json:"request_id"`
	Label         string          `json:"label"`
	Description   string          `json:"description"`
	Icon          string          `json:"icon"`
	Enabled       bool            `json:"enabled"`
	SortOrder     int             `json:"sort_order"`
	ToolMode      string          `json:"tool_mode"`
	Vision        bool            `json:"vision"`
	Stream        bool            `json:"stream"`
	SystemPrompt  string          `json:"system_prompt"`
	ParamControls json.RawMessage `json:"param_controls"`
	// OfficialTools lists OpenAI Responses hosted tools to enable (e.g.
	// "web_search"). Empty = use the system's self-built tools (§2.3-B). Only
	// meaningful for an openai channel with api_format=responses.
	OfficialTools json.RawMessage `json:"official_tools"`
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
	UpdatedAt         int64   `json:"updated_at"`
}

// OAuthProvider is an admin-configured social/OAuth login method. The
// client_secret is never serialised (mirrors Channel.APIKey); HasSecret tells
// the admin UI whether a secret is on file without leaking it.
type OAuthProvider struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // google | github | apple | oidc
	Name         string `json:"name"`
	Icon         string `json:"icon"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"-"`
	HasSecret    bool   `json:"has_secret"`
	AuthURL      string `json:"auth_url"`
	TokenURL     string `json:"token_url"`
	UserInfoURL  string `json:"userinfo_url"`
	Scopes       string `json:"scopes"`
	TeamID       string `json:"team_id"`
	KeyID        string `json:"key_id"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sort_order"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Skill is the §4.17 record. Assets carry references to template files.
type Skill struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Icon         string          `json:"icon"`
	Instructions string          `json:"instructions"`
	Assets       json.RawMessage `json:"assets"`
	Enabled      bool            `json:"enabled"`
	SortOrder    int             `json:"sort_order"`
	UpdatedAt    int64           `json:"updated_at"`
}

// Project — §4.14.
type Project struct {
	ID             string `json:"id"`
	UserID         string `json:"user_id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Instructions   string `json:"instructions"`
	Accent         string `json:"accent"`
	Emoji          string `json:"emoji"`
	Pinned         bool   `json:"pinned"`
	KBID           string `json:"kb_id"`
	AutoAddUploads bool   `json:"auto_add_uploads"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// Conversation — §5 conversations row. kb_ids/summary_blocks/provider_state
// are kept as raw JSON to round-trip through SQLite cleanly.
type Conversation struct {
	ID            string          `json:"id"`
	UserID        string          `json:"user_id"`
	ProjectID     string          `json:"project_id"`
	Title         string          `json:"title"`
	Provider      string          `json:"provider"`
	ModelID       string          `json:"model_id"`
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
}

// Message — flat record over §5 messages. blocks/raw/attachments/citations are
// JSON-encoded so the handler layer can decode/encode without a per-shape DAO.
type Message struct {
	ID               string          `json:"id"`
	ConversationID   string          `json:"conversation_id"`
	ParentID         string          `json:"parent_id"`
	Role             string          `json:"role"`
	Provider         string          `json:"provider"`
	ModelID          string          `json:"model_id"`
	ModelLabel       string          `json:"model_label"`
	Blocks           json.RawMessage `json:"blocks"`
	Raw              json.RawMessage `json:"raw,omitempty"`
	StopReason       string          `json:"stop_reason"`
	Attachments      json.RawMessage `json:"attachments"`
	Citations        json.RawMessage `json:"citations"`
	InputTokens      int             `json:"input_tokens"`
	OutputTokens     int             `json:"output_tokens"`
	CacheReadTokens  int             `json:"cache_read_tokens"`
	CacheWriteTokens int             `json:"cache_write_tokens"`
	Cost             float64         `json:"cost"`
	Currency         string          `json:"currency"`
	// Credits charged to the user for this turn (0 = free / credits disabled).
	// Unlike Cost (USD spend, admin-only), credits ARE the user-facing currency,
	// so this is surfaced to the user and not redacted.
	Credits  float64 `json:"credits"`
	Status   string  `json:"status"`
	Error    string  `json:"error"`
	Feedback string  `json:"feedback"` // "" | "like" | "dislike" (§ message feedback)
	// GenMs is the wall-clock time the assistant turn took to generate (ms).
	GenMs     int64 `json:"gen_ms"`
	CreatedAt int64 `json:"created_at"`
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
}

// Document — §5 documents row. status: pending|parsing|embedding|ready|failed.
type Document struct {
	ID             string `json:"id"`
	KBID           string `json:"kb_id"`
	ConversationID string `json:"conversation_id"`
	Filename       string `json:"filename"`
	MimeType       string `json:"mime_type"`
	SizeBytes      int64  `json:"size_bytes"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	ChunkCount     int    `json:"chunk_count"`
	StoragePath    string `json:"-"`
	CreatedAt      int64  `json:"created_at"`
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
	StoragePath    string `json:"-"`
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
