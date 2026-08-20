package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/envcfg"
	"aivory/server/internal/fileguard"
	"aivory/server/internal/generationcfg"
	"aivory/server/internal/msgcache"
	"aivory/server/internal/queue"
	"aivory/server/internal/rag"
	"aivory/server/internal/store"
	"aivory/server/internal/toolnames"
)

// Env-overridable tuning knobs for inline literals used below. Defaults and
// operator-facing semantics are documented in docs/config-reference.md.
var (
	inlineQuoteSourceInjectionCap    = envcfg.Int("AIVORY_LLM_INLINE_QUOTE_SOURCE_INJECTION_CAP", 8000)
	imageModeForcedGenerationCount   = 1
	imagePromptOptimizerOutputTokens = 400
	ragRouterRecentHistoryCount      = 6
	ragRouterRecentHistoryTruncate   = 200
	// Reasoning-capable task models count hidden reasoning against the output
	// budget. Sixty tokens can yield a successful provider response with no
	// visible title at all, so leave enough room for brief reasoning plus the
	// requested <=8-word label.
	titleGenerationOutputTokens     = 256
	attachmentImageInlineBytes      = envcfg.Int64("AIVORY_LLM_ATTACHMENT_IMAGE_INLINE_BYTES", 20*1024*1024)
	sandboxUploadStagingFileSize    = envcfg.Int64("AIVORY_TOOLS_PYTHON_EXECUTE_UPLOAD_STAGING_FILE_SIZE", 40*1024*1024)
	toolRouteTimeout                = envcfg.Dur("AIVORY_LLM_TOOL_ROUTE_TIMEOUT", 5*time.Second)
	toolRouteSchemaTokenThreshold   = envcfg.Int("AIVORY_LLM_TOOL_ROUTE_SCHEMA_TOKEN_THRESHOLD", 512)
	sandboxExecTimeoutClampRangeMax = envcfg.Int("AIVORY_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MAX", 600)
	sandboxExecTimeoutClampRangeMin = envcfg.Int("AIVORY_LLM_SANDBOX_EXEC_TIMEOUT_CLAMP_RANGE_MIN", 10)
	sandboxExecCtxSafetyMargin      = envcfg.Dur("AIVORY_LLM_SANDBOX_EXEC_CTX_SAFETY_MARGIN", 150*time.Second)
	compactionLeaseTTL              = envcfg.Dur("AIVORY_LLM_COMPACTION_LEASE_TTL", 2*time.Hour)
)

// Orchestrator coordinates the per-message flow described in §3.1: load
// conversation + project + KB + memory context, assemble the system prompt
// (§4.8 — six sections in stable order), pick the right provider, drive the
// tool loop (native or §4.13 prompt-mode), stream events to the caller,
// finalise the assistant message, record usage, and trigger the async
// memory extraction worker (§4.16).
type Orchestrator struct {
	db          *sql.DB
	reg         *Registry
	tools       ToolRegistry
	rag         *rag.Service
	cache       cache.Cache
	queue       queue.Queue
	task        *TaskLLM
	memory      *MemoryWorker
	logger      *log.Logger
	uploadDir   string
	artifactDir string
	// onConversationUpdated is installed by the API layer during startup. Async
	// work such as title generation uses it after a durable metadata write so
	// open clients re-fetch the committed conversation row.
	onConversationUpdated func(userID, conversationID string)
	// onCompactionStatus is a separate lifecycle bridge for AUTOMATIC context
	// compaction. It intentionally does not cover explicit /compact requests:
	// those already have a synchronous HTTP result, while background and inline
	// automatic work otherwise gives open clients no indication that it is using
	// the configured summarisation model.
	onCompactionStatus func(userID, conversationID, operationID, status string)
}

// compactionConfigFingerprint identifies the provider request contract captured
// when an asynchronous compaction job is queued. A model/channel ID alone is
// insufficient: administrators can change request parameters, tool schemas,
// endpoint credentials, or the compaction threshold in place while a job waits
// in the queue. Reusing the old history projection after such a change can
// replay an incompatible native exchange or apply an obsolete budget. The
// fingerprint is an internal digest only; secrets are hashed and never logged.
type compactionChannelFingerprint struct {
	ID        string
	Type      string
	APIFormat string
	BaseURL   string
	APIKey    string
	Enabled   bool
	UpdatedAt int64
}

type compactionCandidateFingerprint struct {
	ID       string
	Model    store.Model
	Primary  compactionChannelFingerprint
	Fallback *compactionFallbackFingerprint
}

type compactionFallbackFingerprint struct {
	ID      string
	Exists  bool
	Channel *compactionChannelFingerprint
}

type compactionFingerprintPayload struct {
	Candidates []compactionCandidateFingerprint
	Settings   map[string]string
}

func compactionChannelFingerprintFrom(channel *store.Channel) compactionChannelFingerprint {
	if channel == nil {
		return compactionChannelFingerprint{}
	}
	// API keys are part of the request contract, but must not be retained in the
	// preimage even though the final value is a digest. This keeps accidental
	// debug logging of the payload from disclosing credentials.
	keyDigest := sha256.Sum256([]byte(channel.APIKey))
	return compactionChannelFingerprint{
		ID: channel.ID, Type: channel.Type, APIFormat: channel.APIFormat,
		BaseURL: channel.BaseURL, APIKey: fmt.Sprintf("%x", keyDigest[:]),
		Enabled: channel.Enabled, UpdatedAt: channel.UpdatedAt,
	}
}

func compactionConfigFingerprint(candidates []compactionCandidateFingerprint, settings map[string]string) string {
	if len(candidates) == 0 || settings == nil {
		return ""
	}
	value := compactionFingerprintPayload{
		Candidates: candidates,
		Settings:   settings,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}

func compactionSettingsSnapshot(db *sql.DB) (map[string]string, error) {
	keys := []string{
		"compaction_enabled", "keep_recent_rounds", "compaction_token_trigger", "compaction_token_cap",
		"compaction_retention_percentage", "summary_max_tokens", "summary_target_percent",
		"summary_merge_max_tokens", "compaction_request_max_tokens", "context_compaction_model_id",
		"context_compaction_prompt", "task_model_id", "default_model_id",
	}
	settings := make(map[string]string, len(keys))
	for _, key := range keys {
		raw, err := store.GetSetting(db, key)
		if err != nil {
			// Older databases may legitimately lack a setting that has a code
			// default. Preserve that absence as part of the contract so adding the
			// row later still invalidates a queued job; only an actual read failure
			// makes the fingerprint unusable.
			if errors.Is(err, sql.ErrNoRows) {
				settings[key] = "<missing>"
				continue
			}
			return nil, fmt.Errorf("read compaction setting %q: %w", key, err)
		}
		settings[key] = string(raw)
	}
	return settings, nil
}

func compactionRuntimeFingerprint(ctx context.Context, db *sql.DB, conversationModelID string) string {
	candidateIDs, err := resolveCompactionModelCandidates(ctx, db, conversationModelID)
	if err != nil {
		return ""
	}
	candidates := make([]compactionCandidateFingerprint, 0, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		model, modelErr := store.GetModel(ctx, db, candidateID)
		if modelErr != nil || model == nil {
			return ""
		}
		primary, channelErr := store.GetChannel(ctx, db, model.ChannelID)
		if channelErr != nil || primary == nil {
			return ""
		}
		candidate := compactionCandidateFingerprint{
			ID:      candidateID,
			Model:   *model,
			Primary: compactionChannelFingerprintFrom(primary),
		}
		fallbackID := strings.TrimSpace(model.FallbackChannelID)
		if fallbackID != "" && fallbackID != model.ChannelID {
			fallback, fallbackErr := store.GetChannel(ctx, db, fallbackID)
			candidate.Fallback = &compactionFallbackFingerprint{ID: fallbackID}
			if fallbackErr != nil {
				if !errors.Is(fallbackErr, store.ErrNotFound) {
					return ""
				}
				// The live request ignores a stale fallback_channel_id and continues on
				// the primary channel. Preserve the missing binding as an explicit,
				// stable state. If the channel later appears or the ID is changed, the
				// fingerprint changes and invalidates the queued job.
				fallback = nil
			}
			if fallback != nil {
				fp := compactionChannelFingerprintFrom(fallback)
				candidate.Fallback.Exists = true
				candidate.Fallback.Channel = &fp
			}
		}
		candidates = append(candidates, candidate)
	}
	settings, settingsErr := compactionSettingsSnapshot(db)
	if settingsErr != nil {
		return ""
	}
	return compactionConfigFingerprint(candidates, settings)
}

// ToolRefusalError marks a tool failure that is a policy/quota REFUSAL (content
// moderation, daily image limit, per-model image quota) rather than a transient
// provider error. The image branch (runImageTurn) renders it as a refusal with
// the real message instead of a generic "try again" error. Defined here (not in
// tools) so the orchestrator can errors.As it without an import cycle.
type ToolRefusalError struct{ Message string }

func (e *ToolRefusalError) Error() string { return e.Message }

var (
	// ErrDrawingPermission is returned before a direct image-model turn persists
	// either message when the current user group forbids drawing.
	ErrDrawingPermission = errors.New("drawing_group_permission_required")
	// ErrKnowledgeBasePermission applies the same defense-in-depth boundary to
	// optional and project knowledge bases for non-HTTP and future callers.
	ErrKnowledgeBasePermission = errors.New("knowledge_base_group_permission_required")
)

// ToolRegistry is the subset of the tools package the orchestrator needs.
type ToolRegistry interface {
	List(modelID string) []ToolDef
	Run(ctx context.Context, name string, input []byte, tc *ToolContext) (output string, citations []Citation, err error)
}

type mcpToolRegistry interface {
	ListMCP(modelID string) []MCPToolDef
}

// providerArtifactRegistry is optionally implemented by the concrete tools
// registry. It lets provider-hosted tools persist binary output through the same
// storage path and OnArtifact callback as local tools.
type providerArtifactRegistry interface {
	SaveArtifact(ctx context.Context, tc *ToolContext, name, mime string, data []byte) error
}

// ToolContext is the runtime context passed to tools.
type ToolContext struct {
	UserID    string
	ConvID    string
	MessageID string
	// WorkspaceID attributes tool spend to a workspace (§workspaces). '' = personal.
	WorkspaceID string
	// ModelID is the chat model driving this turn. use_skill + skill-asset staging
	// scope to the skills bound to THIS model (model_skills, §4.17), so a model can
	// only load the skills an admin checked for it — the same set the system-prompt
	// index advertises.
	ModelID     string
	ProjectID   string
	ProjectName string
	DB          *sql.DB
	// WorkspaceAccessCheck revalidates the turn authority immediately before a
	// local tool leaves the process. Nil for personal conversations.
	WorkspaceAccessCheck func(context.Context) error
	// DeepResearch raises the per-turn tool budgets (deep_research.go).
	DeepResearch bool
	// Fast quarters the per-turn tool budgets and withholds python_execute
	// entirely (§fast-mode) — fast turns trade tool depth for speed.
	Fast bool
	// BuiltinTools is the model's resolved local-tool default selection. nil means
	// all registered tools; a non-nil empty map means no local tool is selected by
	// default. Global and user-group ceilings are applied separately.
	BuiltinTools map[string]bool
	// AdminSkillIDs is the user-group ceiling for administrator-managed skills.
	// nil permits every model-bound skill; a non-nil map permits only listed IDs.
	AdminSkillIDs map[string]bool
	// ImageModelID is the user's pre-selected image model (§4.12-B).
	ImageModelID string
	// ImageRequestParams is the already allowlisted request fragment produced
	// from the selected image model's param_controls. It is carried out-of-band
	// instead of in image_generate's public schema so a chat model cannot forge
	// arbitrary provider fields.
	ImageRequestParams map[string]any
	// ImageInputIDs are the current user turn's image attachments. They are bound
	// server-side so image_generate edits the actual upload even though provider
	// tool schemas do not expose internal file ids to the chat model.
	ImageInputIDs []string
	// ImageUserPrompt preserves the exact current-turn instruction for attached-
	// image edits. The chat model may elaborate a generation prompt, but it must
	// not translate or paraphrase source text the user asked to change literally.
	ImageUserPrompt string
	// SkipImageQuota tells image_generate NOT to meter the image model at all
	// (§4.20): set on the drawing-mode path, where the orchestrator already ran the
	// credit-aware checkImageQuota AND charges in runImageTurn, so the tool must not
	// double-gate / double-charge.
	SkipImageQuota bool
	// ImageBilling lets the chat tool-call path run the SAME free→credits→block
	// decision + debit as drawing mode (§4.20). When set (and not SkipImageQuota),
	// image_generate consults it instead of its own hard cap.
	ImageBilling ImageBiller

	// imgMu guards imageCredits — image_generate may run concurrently in a turn.
	imgMu        sync.Mutex
	imageCredits float64
	imageBillSeq uint64
	// OnArtifact lets a tool surface a produced file (sandbox output, image).
	// The orchestrator persists it + streams an "artifact" SSE event.
	OnArtifact func(ArtifactRef)

	// budgetMu guards counts; charged centrally by the runner before each call.
	budgetMu sync.Mutex
	counts   map[string]int
	// citationIndexes is non-nil only when a KB is attached. This preserves the
	// exact legacy tool output for every no-KB conversation.
	citationIndexes *citationIndexAllocator
}

type citationIndexAllocator struct {
	mu       sync.Mutex
	next     int
	assigned map[string]int
}

func (a *citationIndexAllocator) allocate(count int) int {
	if a == nil || count <= 0 {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	offset := a.next
	a.next += count
	return offset
}

func (a *citationIndexAllocator) normalize(citation Citation) Citation {
	if a == nil || citation.GlobalIndex {
		return citation
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := citationIdentity(citation)
	if key != "" {
		if index, ok := a.assigned[key]; ok {
			citation.Index = index
			citation.GlobalIndex = true
			return citation
		}
	}
	a.next++
	citation.Index = a.next
	citation.GlobalIndex = true
	if key != "" {
		if a.assigned == nil {
			a.assigned = map[string]int{}
		}
		a.assigned[key] = citation.Index
	}
	return citation
}

func citationIdentity(citation Citation) string {
	if value := strings.TrimSpace(citation.URL); value != "" {
		return "url:" + value
	}
	if value := strings.TrimSpace(citation.ID); value != "" {
		return "id:" + value
	}
	return ""
}

// AllowsBuiltinTool is the final execution-boundary policy check. Keeping it
// on ToolContext makes native, prompt-mode, Deep Research, and direct internal
// tool paths share the same fail-closed decision.
func (tc *ToolContext) AllowsBuiltinTool(name string) bool {
	if tc == nil || tc.BuiltinTools == nil {
		return true
	}
	return tc.BuiltinTools[name]
}

// AllowsAdminSkill is the final execution and asset-staging boundary for an
// administrator-managed skill. Model bindings and this group ceiling must both
// allow a skill before its instructions or files can be loaded.
func (tc *ToolContext) AllowsAdminSkill(id string) bool {
	if tc == nil || tc.AdminSkillIDs == nil {
		return true
	}
	return tc.AdminSkillIDs[id]
}

// AddImageCredits accumulates the total credits the tool charged for images this
// turn, so the chat finalize can surface them in messages.credits (§4.20).
func (tc *ToolContext) AddImageCredits(total float64) {
	tc.imgMu.Lock()
	tc.imageCredits += total
	tc.imgMu.Unlock()
}

// ImageCreditsTotal returns the credits charged for images this turn.
func (tc *ToolContext) ImageCreditsTotal() float64 {
	tc.imgMu.Lock()
	defer tc.imgMu.Unlock()
	return tc.imageCredits
}

func (tc *ToolContext) NextImageBillingSourceID() string {
	tc.imgMu.Lock()
	defer tc.imgMu.Unlock()
	tc.imageBillSeq++
	return fmt.Sprintf("%s:image:%d", tc.MessageID, tc.imageBillSeq)
}

type ImageBillingReservation struct {
	admission *billingAdmission
}

// ImageBiller meters image generation against the credit system so the chat
// tool-call path mirrors drawing mode (§4.20). Implemented by *Orchestrator.
type ImageBiller interface {
	ReserveImageBilling(ctx context.Context, userID string, model *store.Model, n int, sourceID string) (reservation *ImageBillingReservation, allow bool, message string, err error)
	SettleImageBilling(ctx context.Context, reservation *ImageBillingReservation, images int, costUSD float64) (timed float64, total float64, err error)
	ReleaseImageBilling(ctx context.Context, reservation *ImageBillingReservation) error
}

// perTurnToolLimits caps how many times a single tool may run per message
// (§4.4 — prevents a model from exhausting search/fetch budget). 0 = unlimited.
var perTurnToolLimits = map[string]int{
	toolnames.AivoryWebSearch: envcfg.Int("AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_SEARCH", 16),
	"web_fetch":               envcfg.Int("AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_FETCH", 12),
	"fetch_image":             envcfg.Int("AIVORY_LLM_PER_TURN_TOOL_LIMITS_FETCH_IMAGE", 16),
	"image_generate":          envcfg.Int("AIVORY_LLM_PER_TURN_TOOL_LIMITS_IMAGE_GENERATE", 8),
	"python_execute":          envcfg.Int("AIVORY_LLM_PER_TURN_TOOL_LIMITS_PYTHON_EXECUTE", 16), // §F10: cap sandbox executions/turn (each up to 120s) to bound abuse/DoS
}

// deepResearchToolLimits are the much higher per-turn caps used while the Deep
// Research engine runs — it deliberately fans out many searches + source reads.
var deepResearchToolLimits = map[string]int{
	toolnames.AivoryWebSearch: envcfg.Int("AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_SEARCH", 40),
	"web_fetch":               envcfg.Int("AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_WEB_FETCH", 25),
	"fetch_image":             envcfg.Int("AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_FETCH_IMAGE", 12),
	"image_generate":          envcfg.Int("AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_IMAGE_GENERATE", 4),
	"python_execute":          envcfg.Int("AIVORY_LLM_DEEP_RESEARCH_TOOL_LIMITS_PYTHON_EXECUTE", 8),
}

// per-turn GLOBAL tool-call ceiling (§B4): bounds a single message's total
// tool-driven cost across ALL tools, on top of the per-tool caps — the native
// provider loop (maxIter=12) otherwise lets the model request unbounded tools
// per round. Deep Research deliberately fans out far more.
var (
	maxToolCallsPerTurn     = envcfg.Int("AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN", 48)
	maxToolCallsPerTurnDeep = envcfg.Int("AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN_DEEP", 150)
)

// §fast-mode budgets: each tool's per-turn cap is a QUARTER of normal (min 1 for
// an available tool), python_execute is dropped entirely (never in the map, so
// charge() also blocks it), and the global ceiling is quartered too. Derived from
// perTurnToolLimits so operator env overrides on the base caps still propagate.
var fastToolLimits = func() map[string]int {
	m := make(map[string]int, len(perTurnToolLimits))
	for k, v := range perTurnToolLimits {
		if k == "python_execute" {
			continue // withheld in fast mode
		}
		q := v / 4
		if q < 1 {
			q = 1
		}
		m[k] = q
	}
	return m
}()

var maxToolCallsPerTurnFast = func() int {
	if q := maxToolCallsPerTurn / 4; q >= 1 {
		return q
	}
	return 1
}()

// filterDisabledTools drops any tool named in the global `disabled_tools`
// setting (§B6 partial: a platform-wide tool kill-switch — e.g. turn off
// python_execute or image_generate).
func (o *Orchestrator) filterDisabledTools(defs []ToolDef) []ToolDef {
	deny := o.disabledToolSet()
	if len(deny) == 0 {
		return defs
	}
	out := make([]ToolDef, 0, len(defs))
	for _, d := range defs {
		if !deny[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

// modelBuiltinToolSet resolves a persisted model policy for execution. nil is
// default-all; an explicit [] and malformed non-null data both produce a
// non-nil empty set (deny all).
func modelBuiltinToolSet(raw json.RawMessage) map[string]bool {
	names, configured, err := store.ParseBuiltinTools(raw)
	if err != nil {
		return map[string]bool{}
	}
	if !configured {
		return nil
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	return allowed
}

func filterModelBuiltinTools(defs []ToolDef, allowed map[string]bool) []ToolDef {
	if allowed == nil {
		return defs
	}
	out := make([]ToolDef, 0, len(defs))
	for _, definition := range defs {
		if allowed[definition.Name] {
			out = append(out, definition)
		}
	}
	return out
}

// modelMCPServerIDSet resolves the model's default MCP selection. Missing,
// explicit-empty, and malformed policies all fail closed to an empty set.
func modelMCPServerIDSet(raw json.RawMessage) map[string]bool {
	ids, configured, err := store.ParseMCPServerIDs(raw)
	if err != nil || !configured {
		return map[string]bool{}
	}
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return allowed
}

func filterModelMCPTools(defs []MCPToolDef, allowed map[string]bool) []MCPToolDef {
	if allowed == nil {
		return defs
	}
	out := make([]MCPToolDef, 0, len(defs))
	for _, definition := range defs {
		if allowed[definition.ServerID] {
			out = append(out, definition)
		}
	}
	return out
}

// Allows reports whether the policy admits one prefixed catalog tool id
// ("builtin:…", "hosted:…", "mcp:…"). Public so the API layer can unit-test
// the workspace policy fold end to end.
func (p *ToolAccessPolicy) Allows(id string) bool {
	return toolAccessPolicyAllows(p, id)
}

func toolAccessPolicyAllows(policy *ToolAccessPolicy, id string) bool {
	if policy == nil {
		return true
	}
	for _, denied := range policy.DenyIDs {
		if denied == id {
			return false
		}
	}
	switch policy.Mode {
	case store.ResourceAccessNone:
		return false
	case store.ResourceAccessSelected:
		allowed := false
		for _, candidate := range policy.IDs {
			if candidate == id {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	if !policy.AllowDrawing && (id == "builtin:image_generate" || id == "hosted:image_generation") {
		return false
	}
	if !policy.AllowMemory && id == "builtin:save_memory" {
		return false
	}
	if !policy.AllowSkills && id == "builtin:use_skill" {
		return false
	}
	return true
}

func groupToolAccessPolicy(permissions store.UserGroupPermissions) *ToolAccessPolicy {
	allowSkills := permissions.Skills.Mode == store.ResourceAccessAll ||
		(permissions.Skills.Mode == store.ResourceAccessSelected && len(permissions.Skills.IDs) > 0)
	return &ToolAccessPolicy{
		Mode:         permissions.Tools.Mode,
		IDs:          append([]string(nil), permissions.Tools.IDs...),
		AllowDrawing: permissions.AllowDrawing,
		AllowMemory:  permissions.AllowMemory,
		AllowSkills:  allowSkills,
		SkillMode:    permissions.Skills.Mode,
		SkillIDs:     append([]string(nil), permissions.Skills.IDs...),
	}
}

func intersectResourceAccess(
	leftMode string,
	leftIDs []string,
	rightMode string,
	rightIDs []string,
) (string, []string) {
	if leftMode == "" {
		leftMode = store.ResourceAccessAll
	}
	if rightMode == "" {
		rightMode = store.ResourceAccessAll
	}
	if leftMode == store.ResourceAccessNone || rightMode == store.ResourceAccessNone {
		return store.ResourceAccessNone, nil
	}
	if leftMode == store.ResourceAccessAll {
		return rightMode, append([]string(nil), rightIDs...)
	}
	if rightMode == store.ResourceAccessAll {
		return leftMode, append([]string(nil), leftIDs...)
	}
	rightSet := make(map[string]bool, len(rightIDs))
	for _, id := range rightIDs {
		rightSet[id] = true
	}
	intersection := make([]string, 0, len(leftIDs))
	for _, id := range leftIDs {
		if rightSet[id] {
			intersection = append(intersection, id)
		}
	}
	if len(intersection) == 0 {
		return store.ResourceAccessNone, nil
	}
	return store.ResourceAccessSelected, intersection
}

// intersectToolAccessPolicies treats the API snapshot as an optional extra
// restriction. The database-derived policy always remains the hard ceiling, so
// a direct caller cannot broaden access with a forged or omitted request field.
func intersectToolAccessPolicies(requested, current *ToolAccessPolicy) *ToolAccessPolicy {
	if current == nil {
		return requested
	}
	if requested == nil {
		copy := *current
		copy.IDs = append([]string(nil), current.IDs...)
		copy.SkillIDs = append([]string(nil), current.SkillIDs...)
		copy.DenyIDs = append([]string(nil), current.DenyIDs...)
		return &copy
	}
	mode, ids := intersectResourceAccess(requested.Mode, requested.IDs, current.Mode, current.IDs)
	skillMode, skillIDs := intersectResourceAccess(requested.SkillMode, requested.SkillIDs, current.SkillMode, current.SkillIDs)
	denyIDs := append([]string(nil), requested.DenyIDs...)
	denyIDs = append(denyIDs, current.DenyIDs...)
	return &ToolAccessPolicy{
		Mode:         mode,
		IDs:          ids,
		AllowDrawing: requested.AllowDrawing && current.AllowDrawing,
		AllowMemory:  requested.AllowMemory && current.AllowMemory,
		AllowSkills:  requested.AllowSkills && current.AllowSkills && skillMode != store.ResourceAccessNone,
		SkillMode:    skillMode,
		SkillIDs:     skillIDs,
		DenyIDs:      denyIDs,
	}
}

func skillAccessPolicyAllows(policy *ToolAccessPolicy, id string) bool {
	if policy == nil {
		return true
	}
	switch policy.SkillMode {
	case store.ResourceAccessNone:
		return false
	case store.ResourceAccessSelected:
		for _, candidate := range policy.SkillIDs {
			if candidate == id {
				return true
			}
		}
		return false
	default:
		return policy.AllowSkills
	}
}

func adminSkillIDSet(policy *ToolAccessPolicy) map[string]bool {
	if policy == nil || policy.SkillMode == "" || policy.SkillMode == store.ResourceAccessAll {
		return nil
	}
	allowed := make(map[string]bool, len(policy.SkillIDs))
	if policy.SkillMode == store.ResourceAccessSelected {
		for _, id := range policy.SkillIDs {
			allowed[id] = true
		}
	}
	return allowed
}

func userSkillAccessPolicyAllows(policy *ToolAccessPolicy, skill store.UserSkill) bool {
	if policy == nil || policy.SkillMode == "" || policy.SkillMode == store.ResourceAccessAll {
		return true
	}
	return skill.SourceSkillID != "" && skillAccessPolicyAllows(policy, skill.SourceSkillID)
}

func validateUserSkillAccessPolicy(policy *ToolAccessPolicy, skills []store.UserSkill) error {
	for _, skill := range skills {
		if !userSkillAccessPolicyAllows(policy, skill) {
			return store.ErrInvalidUserSkillSelection
		}
	}
	return nil
}

func filterBuiltinToolsByAccess(defs []ToolDef, policy *ToolAccessPolicy) []ToolDef {
	if policy == nil {
		return defs
	}
	out := make([]ToolDef, 0, len(defs))
	for _, definition := range defs {
		if toolAccessPolicyAllows(policy, "builtin:"+definition.Name) {
			out = append(out, definition)
		}
	}
	return out
}

func filterMCPToolsByAccess(defs []MCPToolDef, policy *ToolAccessPolicy) []MCPToolDef {
	if policy == nil {
		return defs
	}
	out := make([]MCPToolDef, 0, len(defs))
	for _, definition := range defs {
		if toolAccessPolicyAllows(policy, "mcp:"+definition.ServerID) {
			out = append(out, definition)
		}
	}
	return out
}

func filterHostedToolsByAccess(names []string, requests []json.RawMessage, policy *ToolAccessPolicy) ([]string, []json.RawMessage) {
	if policy == nil {
		return names, requests
	}
	outNames := make([]string, 0, len(names))
	outRequests := make([]json.RawMessage, 0, len(requests))
	for index, name := range names {
		if index >= len(requests) || !toolAccessPolicyAllows(policy, "hosted:"+name) {
			continue
		}
		outNames = append(outNames, name)
		outRequests = append(outRequests, requests[index])
	}
	return outNames, outRequests
}

func selectedToolIDSet(ids []string, configured bool) map[string]bool {
	if !configured {
		return nil
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			selected[id] = true
		}
	}
	return selected
}

func filterBuiltinToolsBySelection(defs []ToolDef, selected map[string]bool) []ToolDef {
	if selected == nil {
		return defs
	}
	out := make([]ToolDef, 0, len(defs))
	for _, definition := range defs {
		if selected["builtin:"+definition.Name] {
			out = append(out, definition)
		}
	}
	return out
}

func filterMCPToolsBySelection(defs []MCPToolDef, selected map[string]bool) []MCPToolDef {
	if selected == nil {
		return defs
	}
	out := make([]MCPToolDef, 0, len(defs))
	for _, definition := range defs {
		if selected["mcp:"+definition.ServerID] {
			out = append(out, definition)
		}
	}
	return out
}

func flattenMCPToolDefs(defs []MCPToolDef) []ToolDef {
	out := make([]ToolDef, 0, len(defs))
	for _, definition := range defs {
		out = append(out, definition.ToolDef)
	}
	return out
}

func filterHostedToolsBySelection(names []string, requests []json.RawMessage, selected map[string]bool) ([]string, []json.RawMessage) {
	if selected == nil {
		return names, requests
	}
	outNames := make([]string, 0, len(names))
	outRequests := make([]json.RawMessage, 0, len(requests))
	for index, name := range names {
		if index >= len(requests) || !selected["hosted:"+name] {
			continue
		}
		outNames = append(outNames, name)
		outRequests = append(outRequests, requests[index])
	}
	return outNames, outRequests
}

func (o *Orchestrator) listMCPTools(modelID string) []MCPToolDef {
	registry, ok := o.tools.(mcpToolRegistry)
	if !ok {
		return nil
	}
	return registry.ListMCP(modelID)
}

func toolDefsContain(defs []ToolDef, name string) bool {
	for _, definition := range defs {
		if definition.Name == name {
			return true
		}
	}
	return false
}

// disabledToolSet reads the global `disabled_tools` admin kill-switch as a set.
// nil when unset/unparseable (fail-open, same as filterDisabledTools).
func (o *Orchestrator) disabledToolSet() map[string]bool {
	if o.db == nil {
		return nil
	}
	raw, err := store.GetSetting(o.db, "disabled_tools")
	if err != nil || len(raw) == 0 {
		return nil
	}
	names, _, err := store.ParseBuiltinTools(raw)
	if err != nil || len(names) == 0 {
		return nil
	}
	deny := make(map[string]bool, len(names))
	for _, n := range names {
		deny[n] = true
	}
	return deny
}

// toolCallTimeout returns the per-invocation bound for a tool (§4.3), matching
// what orchToolRunner applies, so non-tool callers (forced web search) can hold
// the same deadline.
func toolCallTimeout(name string) time.Duration {
	if d, ok := toolTimeouts[name]; ok {
		return d
	}
	return toolTimeoutDefault
}

// charge increments the per-turn counters for a tool and returns an error when
// either the per-tool or the global per-turn limit is exceeded.
func (tc *ToolContext) charge(name string) error {
	if !tc.AllowsBuiltinTool(name) {
		return fmt.Errorf("tool %q is not enabled for this model", name)
	}
	limits := perTurnToolLimits
	totalCap := maxToolCallsPerTurn
	switch {
	case tc.DeepResearch:
		limits = deepResearchToolLimits
		totalCap = maxToolCallsPerTurnDeep
	case tc.Fast:
		limits = fastToolLimits
		totalCap = maxToolCallsPerTurnFast
		// Sandbox-backed tools are withheld from fast turns; block them
		// defensively even if one somehow reaches the runner.
		if name == "python_execute" || name == "fetch_image" {
			return errors.New("sandbox tools are unavailable in fast mode")
		}
	}
	tc.budgetMu.Lock()
	defer tc.budgetMu.Unlock()
	if tc.counts == nil {
		tc.counts = map[string]int{}
	}
	tc.counts["__total__"]++
	if tc.counts["__total__"] > totalCap {
		return fmt.Errorf("tool-call limit (%d) reached for this message", totalCap)
	}
	if limit, ok := limits[name]; ok && limit > 0 {
		tc.counts[name]++
		if tc.counts[name] > limit {
			return fmt.Errorf("%s call limit (%d) reached for this message", name, limit)
		}
	}
	return nil
}

// NewOrchestrator constructs the orchestrator.
//
// The queue / task / memory dependencies are optional — callers that only
// want the basic chat loop can pass nil and the orchestrator silently skips
// the async stages.
func NewOrchestrator(
	db *sql.DB,
	reg *Registry,
	tools ToolRegistry,
	ragSvc *rag.Service,
	c cache.Cache,
	q queue.Queue,
	task *TaskLLM,
	memory *MemoryWorker,
	logger *log.Logger,
	storageRoots ...string,
) *Orchestrator {
	uploadDir, artifactDir := "", ""
	if len(storageRoots) > 0 {
		uploadDir = storageRoots[0]
	}
	if len(storageRoots) > 1 {
		artifactDir = storageRoots[1]
	}
	return &Orchestrator{
		db: db, reg: reg, tools: tools, rag: ragSvc,
		cache: c, queue: q, task: task, memory: memory, logger: logger,
		uploadDir: uploadDir, artifactDir: artifactDir,
	}
}

// SetConversationUpdatedHandler installs the API-owned notification bridge for
// background metadata writes. It is configured once during application startup,
// before the server accepts requests.
func (o *Orchestrator) SetConversationUpdatedHandler(fn func(userID, conversationID string)) {
	o.onConversationUpdated = fn
}

// SetCompactionStatusHandler installs the API-owned notification bridge for
// automatic context compaction. Status is one of "started", "completed", or
// "failed". It is configured during startup before the server accepts work.
func (o *Orchestrator) SetCompactionStatusHandler(fn func(userID, conversationID, operationID, status string)) {
	o.onCompactionStatus = fn
}

func (o *Orchestrator) notifyAutomaticCompactionStatus(userID, conversationID, operationID, status string) {
	if o != nil && o.onCompactionStatus != nil {
		o.onCompactionStatus(userID, conversationID, operationID, status)
	}
}

// ManualCompactionResult describes one explicit /compact attempt.
type ManualCompactionResult struct {
	Compacted       bool   `json:"compacted"`
	Reason          string `json:"reason"`
	DroppedMessages int    `json:"dropped_messages"`
	KeptMessages    int    `json:"kept_messages"`
	SummaryTokens   int    `json:"summary_tokens"`
}

type compactionLease struct {
	orchestrator   *Orchestrator
	conversationID string
	token          string
}

func effectiveCompactionLeaseTTL() time.Duration {
	ttl := compactionLeaseTTL
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	if minimum := generationcfg.ProtectedDuration(); ttl < minimum {
		return minimum
	}
	return ttl
}

// tryAcquireCompactionLease uses the relational database as the authoritative
// per-conversation lease. Redis is optional in Aivory, and a process-local
// in-memory cache cannot protect a multi-replica deployment from duplicate
// compaction provider calls or duplicate billing.
func (o *Orchestrator) tryAcquireCompactionLease(ctx context.Context, conversationID string) (*compactionLease, bool, error) {
	if o == nil || o.db == nil {
		return nil, false, errors.New("context compaction database is unavailable")
	}
	token := store.GenID("cmpl")
	acquired, err := store.TryAcquireConversationCompactionLease(
		ctx, o.db, conversationID, token, effectiveCompactionLeaseTTL(),
	)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return &compactionLease{orchestrator: o, conversationID: conversationID, token: token}, true, nil
}

// acquireCompactionLease is retained for tests and lifecycle probes that only
// need a best-effort yes/no answer. Production call sites use the context-aware
// form above so a database failure is never misreported as an active summary.
func (o *Orchestrator) acquireCompactionLease(conversationID string) (*compactionLease, bool) {
	lease, acquired, err := o.tryAcquireCompactionLease(context.Background(), conversationID)
	if err != nil && o != nil && o.logger != nil {
		o.logger.Printf("compaction: acquire lease (conv=%s): %v", conversationID, err)
	}
	return lease, acquired
}

func (lease *compactionLease) Release() {
	if lease == nil || lease.orchestrator == nil || lease.orchestrator.db == nil {
		return
	}
	// A client cancellation must not strand the lease until its long TTL expires.
	// The owner token protects a replacement that acquired the row after expiry.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.ReleaseConversationCompactionLease(ctx, lease.orchestrator.db, lease.conversationID, lease.token); err != nil && lease.orchestrator.logger != nil {
		lease.orchestrator.logger.Printf("compaction: release lease (conv=%s): %v", lease.conversationID, err)
	}
}

// compactionHistoryAtLeaf loads exactly the branch that scheduled an async
// compaction. The normal ListMessages contract repairs a dangling leaf to the
// newest surviving branch so conversations still render after partial deletes;
// that behavior is unsafe for a queued summary job. Validate both before and
// after loading, and require the returned path to terminate at the requested
// leaf so a repair fallback can never be mistaken for the scheduled branch.
func (o *Orchestrator) compactionHistoryAtLeaf(ctx context.Context, conversationID, leafID string) ([]store.Message, bool, error) {
	leaf, err := store.GetMessage(ctx, o.db, leafID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if leaf == nil || leaf.ConversationID != conversationID {
		return nil, false, nil
	}
	history, err := msgcache.ListMessages(ctx, o.cache, o.db, conversationID, leafID)
	if err != nil {
		return nil, false, err
	}
	if len(history) == 0 || history[len(history)-1].ID != leafID {
		return nil, false, nil
	}
	leaf, err = store.GetMessage(ctx, o.db, leafID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if leaf == nil || leaf.ConversationID != conversationID {
		return nil, false, nil
	}
	return history, true, nil
}

// compactionLeafOnActivePath verifies that a queued compaction still belongs to
// the conversation branch the user is currently viewing. Read active_leaf_id on
// both sides of the path reconstruction so a concurrent branch switch cannot be
// mistaken for a stable match. The caller performs this immediately before any
// status notification, task-model call, or standalone compaction billing.
func (o *Orchestrator) compactionLeafOnActivePath(ctx context.Context, conversationID, queuedLeafID string) (bool, error) {
	var activeLeafBefore string
	if err := o.db.QueryRowContext(ctx,
		`SELECT COALESCE(active_leaf_id,'') FROM conversations WHERE id=?`, conversationID,
	).Scan(&activeLeafBefore); err != nil {
		return false, err
	}
	if activeLeafBefore == "" {
		return false, nil
	}
	activePath, current, err := o.compactionHistoryAtLeaf(ctx, conversationID, activeLeafBefore)
	if err != nil || !current {
		return false, err
	}
	var activeLeafAfter string
	if err := o.db.QueryRowContext(ctx,
		`SELECT COALESCE(active_leaf_id,'') FROM conversations WHERE id=?`, conversationID,
	).Scan(&activeLeafAfter); err != nil {
		return false, err
	}
	if activeLeafAfter != activeLeafBefore {
		return false, nil
	}
	for _, message := range activePath {
		if message.ID == queuedLeafID {
			return true, nil
		}
	}
	return false, nil
}

// CompactConversation explicitly advances the active branch's summary while
// reusing the same model routing, persistence and race guards as auto-compaction.
func (o *Orchestrator) CompactConversation(ctx context.Context, userID, conversationID string) (ManualCompactionResult, error) {
	result := ManualCompactionResult{Reason: "nothing_to_compact"}
	if o == nil || o.db == nil || o.task == nil {
		return result, errors.New("context compaction is unavailable")
	}
	// Bound explicit compaction by the same operator-controlled ceiling as a chat
	// generation. The distributed lease is guaranteed to outlive this deadline,
	// so a stalled provider cannot let another replica start a duplicate summary
	// call (and potentially charge it twice) after the lease expires.
	ctx, cancel := context.WithTimeout(ctx, generationcfg.MaxDuration())
	defer cancel()
	enabled := true
	if raw, err := store.GetSetting(o.db, "compaction_enabled"); err == nil {
		_ = json.Unmarshal(raw, &enabled)
	}
	if !enabled {
		result.Reason = "disabled"
		return result, ErrCompactionDisabled
	}
	conv, err := store.GetConversation(ctx, o.db, conversationID, userID)
	if err != nil {
		return result, err
	}
	if conv.UserID != userID {
		// Shared workspace conversations are readable/replyable by members, but a
		// compaction checkpoint changes the context seen by every collaborator.
		// Keep that conversation-wide mutation with its creator.
		return result, store.ErrNotFound
	}
	lease, acquired, leaseErr := o.tryAcquireCompactionLease(ctx, conv.ID)
	if leaseErr != nil {
		result.Reason = "compaction_failed"
		return result, fmt.Errorf("acquire context compaction lease: %w", leaseErr)
	}
	if !acquired {
		result.Reason = "generation_in_progress"
		return result, ErrCompactionInFlight
	}
	defer lease.Release()
	// Refresh mutable branch and summary state after acquiring the lease. A prior
	// compaction or branch switch may have completed between authorization and the
	// lease; attributing that change to this command would misreport success.
	conv, err = store.GetConversation(ctx, o.db, conversationID, userID)
	if err != nil {
		return result, err
	}
	if conv.UserID != userID {
		return result, store.ErrNotFound
	}
	var inFlight int
	streamingCutoff := protectedStreamingCutoffUnix()
	if err := o.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM messages
		  WHERE conversation_id=? AND role='assistant' AND status='streaming' AND created_at>?`,
		conv.ID, streamingCutoff,
	).Scan(&inFlight); err != nil {
		return result, err
	}
	if inFlight > 0 {
		result.Reason = "generation_in_progress"
		return result, ErrCompactionInFlight
	}
	history, leafCurrent, err := o.compactionHistoryAtLeaf(ctx, conv.ID, conv.ActiveLeafID)
	if err != nil {
		return result, err
	}
	if !leafCurrent {
		result.Reason = "conversation_changed"
		return result, ErrCompactionChanged
	}
	beforeBlocks := filterBlocksForPath(loadSummaryBlocksForModel(ctx, o.db, conv.SummaryBlocks, conv.ModelID), history)
	beforeFrontier := summarizedFrontier(beforeBlocks, history)
	operationID := store.GenID("cmp")
	billingCtx := withStandaloneCompactionBilling(ctx, o, operationID)
	keep, blocks, err := CompactConversationNow(billingCtx, o.db, o.task, conv, history, conv.ModelID, userID)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			result.Reason = "timed_out"
		case errors.Is(err, context.Canceled):
			// If the caller itself is still alive, this came from an internal or
			// provider context. The HTTP layer suppresses output only when its own
			// request context has actually ended.
			result.Reason = "compaction_failed"
		case errors.Is(err, ErrCompactionChanged):
			result.Reason = "conversation_changed"
		case errors.Is(err, ErrCompactionPersist):
			result.Reason = "persistence_failed"
		case errors.Is(err, ErrCompactionFailed), errors.Is(err, ErrTaskBillingRecord):
			result.Reason = "compaction_failed"
		}
		return result, err
	}
	afterFrontier := summarizedFrontier(blocks, history)
	result.KeptMessages = len(keep)
	result.DroppedMessages = max(0, afterFrontier-beforeFrontier)
	result.SummaryTokens = summaryTokens(blocks)
	if result.DroppedMessages == 0 {
		return result, nil
	}
	msgcache.Bump(o.cache, conv.ID)
	result.Compacted = true
	result.Reason = "compacted"
	return result, nil
}

// compactionHistoryForRequest applies the same provider/tool visibility rules
// used by the live request before adding per-turn injections such as private
// skills, the persisted summary, and RAG. Keeping this transformation in one
// place is important for asynchronous rebasing: stable non-history overhead
// must be subtracted from the same history representation that was originally
// estimated, rather than from raw database messages.
func compactionHistoryForRequest(
	history []store.Message,
	currentProvider, currentModelID string,
	nativeToolReplay bool,
	allowedTools map[string]bool,
	fastMode, vision bool,
) []UnifiedMessage {
	unified := storeToUnified(history, currentProvider, currentModelID, nativeToolReplay)
	unified = stripRetiredKnowledgeSearchToolBlocks(unified)
	unified = stripDisallowedBuiltinToolBlocks(unified, allowedTools)
	if fastMode {
		unified = stripFastModeCodeBlocks(unified)
	}
	if !vision {
		unified = stripImageBlocks(unified)
	}
	return unified
}

// RunRequest is the input the API hands to Run().
type RunRequest struct {
	UserID         string
	ConversationID string
	ModelID        string
	UserText       string
	Attachments    []Attachment
	ParentID       string
	ParamOverrides map[string]any
	// KnowledgeBaseIDs is the explicit KB selection captured when this turn was
	// submitted. The companion flag distinguishes an intentional empty selection
	// (detach every optional KB for this turn) from an older caller that omitted
	// the field and should continue using the conversation's persisted kb_ids.
	// Project libraries remain implicit and are added below from conv.ProjectID.
	KnowledgeBaseIDs                 []string
	KnowledgeBaseSelectionConfigured bool
	// Branch is true when the user edits a past question into a NEW sibling
	// branch. It stops Run from falling back to the active leaf when ParentID is
	// empty (i.e. editing the ROOT question), so the edit opens a sibling root
	// instead of being appended to the conversation tail (§4.15).
	Branch bool
	// ReuseExistingUserMessage is true when the caller (regenerate) passes the
	// id of an EXISTING user message in ParentID and wants the new assistant
	// turn parented to it directly — no new user sibling is created. §4.15:
	// regenerate forks at the assistant level, not the user level.
	ReuseExistingUserMessage bool
	// Mode selects an alternate turn pipeline. "" = normal chat;
	// ModeDeepResearch runs the multi-round research engine (deep_research.go).
	Mode string
	// Verify enables Verify mode (§verify) for this turn: after the primary
	// answer finalizes, a secondary auditor model fact-checks it. Honoured only
	// when an admin has configured `verify_model_id`; otherwise a no-op.
	Verify bool
	// ToolMode is the per-turn tool policy: auto | disabled | enabled. Empty keeps
	// backwards compatibility with callers that only set NoTools (true maps to
	// disabled; false maps to enabled). Fast and Deep Research force enabled.
	ToolMode string
	// OfficialToolNames is accepted only for compatibility with callers predating
	// the unified tool policy. It is ignored: clients select from the safe unified
	// catalog through SelectedToolIDs and cannot submit hosted request fragments.
	OfficialToolNames []string
	// SelectedToolIDs is a unified service/tool catalog subset. Omitted uses the
	// serving model's administrator defaults; an explicit empty array means none.
	SelectedToolIDs         []string
	SelectedToolsConfigured bool
	// ToolAccessPolicy is the current user's group-level hard ceiling. It remains
	// independent from model defaults and is re-applied during model fallback.
	ToolAccessPolicy *ToolAccessPolicy
	// WorkspaceAccessCheck is supplied by the API for workspace turns. It is
	// called immediately before every local tool execution so a role, visibility,
	// or policy change cannot wait for the next request to take effect.
	WorkspaceAccessCheck func(context.Context) error
	// SelectedUserSkillIDs names private, user-owned Agent Skills explicitly
	// selected for this turn. They are persisted on the user message and injected
	// at user-message authority, never into composeSystemPrompt.
	SelectedUserSkillIDs []string
	// NoTools is the legacy boolean input and the resolved effective state used by
	// the existing no-tool fallbacks later in Run. ToolMode takes precedence when
	// it is non-empty.
	NoTools bool
	// ForceWebSearch runs a NON-tool web search before generation: a task model
	// derives search queries from the conversation, the searcher runs them, and
	// the results are injected into the prompt as <web-search-result> context
	// (§4.4-B). Only meaningful with NoTools (it replaces the tool the model can
	// no longer call); ignored otherwise.
	ForceWebSearch bool
	// ImageStyleID selects an admin-managed image style (§4.20) for an image-mode
	// turn (conversation model kind=image). Its hidden prompt is composed
	// server-side into the final image prompt; ignored for chat models.
	ImageStyleID string
	// OptimizeImagePrompt controls the task-model rewrite for direct image-model
	// turns. Nil preserves the historical default (enabled).
	OptimizeImagePrompt *bool
	// Locale is the user's UI language code (e.g. "en", "zh", "zh-Hant", "ja").
	// It anchors the reply-language instruction so an English question gets an
	// English answer even from a language-biased model (§ reply language).
	Locale string
	// Fast marks a fast-mode turn (§fast-mode). The model is resolved server-side
	// from the admin's single fast model (ModelID is ignored); Verify and
	// ModeDeepResearch are forced off; tools stay ON but each tool's per-turn
	// budget is quartered and python_execute is withheld; and the resolved model
	// is NOT written back onto the conversation. Every user-facing surface masks
	// the real model as "快速".
	Fast bool
}

const (
	// ModeDeepResearch is the RunRequest.Mode value that triggers the Deep
	// Research engine (plan → multi-round web search + source reading → verify).
	ModeDeepResearch = "deep-research"
	// ToolModeAuto asks the configured task model whether this turn needs tools.
	ToolModeAuto = "auto"
	// ToolModeDisabled exposes no tools and activates the server-side fallbacks.
	ToolModeDisabled = "disabled"
	// ToolModeEnabled exposes the resolved model's complete administrator-
	// configured tool collection (local Functions and provider-hosted tools).
	ToolModeEnabled = "enabled"
	// ToolModeOfficial is a legacy wire value. New callers never emit it and the
	// resolver normalizes it to ToolModeEnabled.
	ToolModeOfficial = "official"
)

// ErrInvalidMessageParent means a caller supplied a message parent that no
// longer exists in this conversation (or belongs to another conversation). It
// is deliberately a domain error: stale optimistic ids must never escape as a
// database foreign-key failure from CreateMessage.
var ErrInvalidMessageParent = errors.New("message parent is no longer available in this conversation")

func invalidMessageParentError(parentID string) error {
	if parentID == "" {
		return ErrInvalidMessageParent
	}
	return fmt.Errorf("%w: %q", ErrInvalidMessageParent, parentID)
}

func isForeignKeyConstraintError(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "foreign key constraint") || strings.Contains(low, "23503")
}

func normalizeMessageCreateError(operation, parentID string, err error) error {
	if err == nil {
		return nil
	}
	if parentID != "" && isForeignKeyConstraintError(err) {
		return invalidMessageParentError(parentID)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func resolveRunToolMode(req RunRequest) (string, error) {
	if req.ToolMode == "" {
		if req.NoTools {
			return ToolModeDisabled, nil
		}
		return ToolModeEnabled, nil
	}
	switch req.ToolMode {
	case ToolModeAuto, ToolModeDisabled, ToolModeEnabled:
		return req.ToolMode, nil
	case ToolModeOfficial:
		return ToolModeEnabled, nil
	default:
		return "", fmt.Errorf("invalid tool mode %q", req.ToolMode)
	}
}

// RunResult is what Run returns to the SSE handler after the stream finishes.
type RunResult struct {
	UserMessage      *store.Message
	AssistantMessage *store.Message
}

// streamWithFallback runs provider.Stream behind a time-to-first-BYTE (TTFT)
// watchdog. The timer is armed by doProviderRequest immediately before the
// provider HTTP call, so it measures provider API request -> first response
// byte and excludes RAG retrieval, context assembly, credit preflight and local
// payload construction. If the upstream sends NO BYTES within the
// admin-configured `fallback_ttft_sec`, the connection is cut and the SAME
// assistant message is re-generated with the admin-configured
// `fallback_model_id` — transparently, since the user has only seen
// `message_start` (no text yet).
//
// Gated on the raw first byte, not the first PARSED content event, on purpose:
// a relay/gateway in front of the real model reports its own TTFT the same
// way (time to first byte from the true upstream), and reasoning models can
// go quiet for a long stretch after an early framing byte before any real
// content streams — gating on parsed content made this fire even when the
// upstream (and the relay's own dashboard) considered the request healthy the
// whole time. See the doc comment on providerTTFTWatchdog.
//
// Only triggers before the first byte (never mid-stream → no visible
// restart), and falls back at most once (the fallback runs without a
// watchdog). Disabled — and zero overhead, a plain provider.Stream — unless
// both settings are present.
func (o *Orchestrator) streamWithFallback(
	ctx context.Context,
	provReq UnifiedChatRequest,
	runner ToolRunner,
	provider Provider,
	primaryModelID string,
	onEvent func(SseEvent),
	servedFallbackModel *string,
) (*UnifiedResult, error) {
	ttft := settingInt(o.db, "fallback_ttft_sec")
	fbID := settingStr(o.db, "fallback_model_id")
	if ttft <= 0 || fbID == "" || fbID == primaryModelID {
		return provider.Stream(ctx, provReq, runner, onEvent)
	}

	wdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	watchdog := newProviderTTFTWatchdog(time.Duration(ttft)*time.Second, cancel, &stalled)
	defer watchdog.stop()
	wdCtx = contextWithProviderTTFTWatchdog(wdCtx, watchdog)

	result, err := provider.Stream(wdCtx, provReq, runner, onEvent)
	// Healthy completion, or a real user cancel on the PARENT ctx → return as-is.
	if !stalled.Load() {
		return result, err
	}
	// Watchdog fired. Only switch when the upstream produced nothing — never
	// after partial output (would visibly restart the answer).
	if result != nil && len(result.Blocks) > 0 {
		return result, err
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return result, err // parent already dead (shutdown) — don't bother
	}
	fbReq, fbProvider, fbName, ferr := o.buildFallbackRequest(ctx, provReq, fbID)
	if ferr != nil {
		o.logger.Printf("llm: fallback model %q unavailable, keeping upstream error: %v", fbID, ferr)
		return result, err
	}
	// Record the switch for the usage row (§4.6-C) BEFORE streaming, so it is
	// attributed even when the fallback attempt itself errors.
	if servedFallbackModel != nil {
		*servedFallbackModel = fbName
	}
	o.logger.Printf("llm: upstream model %q produced no output in %ds — switching to fallback %q", primaryModelID, ttft, fbID)
	// Single attempt, no watchdog → no chaining. Streams into the SAME onEvent,
	// so the frontend just keeps filling the existing (empty) message.
	return fbProvider.Stream(ctx, fbReq, toolRunnerForModelRequest(runner, fbID, fbReq.Tools), onEvent)
}

// buildFallbackRequest clones the in-flight request but swaps in the fallback
// model + its provider/channel and re-gates every capability-dependent field.
func (o *Orchestrator) buildFallbackRequest(ctx context.Context, base UnifiedChatRequest, fbID string) (UnifiedChatRequest, Provider, string, error) {
	m, err := store.GetModel(ctx, o.db, fbID)
	if err != nil {
		return base, nil, "", err
	}
	ch, err := store.GetChannel(ctx, o.db, m.ChannelID)
	if err != nil {
		return base, nil, "", err
	}
	prov, err := o.reg.Get(ch.Type)
	if err != nil {
		return base, nil, "", err
	}
	req := base // shallow copy; slices (history/tools/…) are read-only during the stream
	req.Model = ModelInfo{
		ID: m.ID, RequestID: m.RequestID, Provider: ch.Type, Vision: m.Vision,
		BaseURL: ch.BaseURL, APIKey: ch.APIKey, APIFormat: ch.APIFormat,
	}
	req.Stream = m.Stream
	req.ParamControls = m.ParamControls
	req.ExtraParams = nil
	if m.Kind == "chat" {
		req.ExtraParams = m.ExtraParams
	}
	fallbackBuiltinTools := modelBuiltinToolSet(m.BuiltinTools)
	fallbackMCPServers := modelMCPServerIDSet(m.MCPServerIDs)
	globalDisabledTools := o.disabledToolSet()
	fallbackToolMode := m.ToolMode
	if fallbackToolMode == "" {
		fallbackToolMode = "native"
	}
	// A model configured with tool_mode=none is an administrator-level ceiling
	// over the unified collection. Otherwise, a turn whose policy enabled tools is
	// rebuilt from the fallback model's own complete administrator configuration.
	baseToolsEnabled := base.ToolsEnabled || len(base.Tools) > 0 || len(base.OfficialToolRequests) > 0
	req.ToolsEnabled = baseToolsEnabled && fallbackToolMode != "none"
	req.Tools = nil
	req.OfficialToolNames = nil
	req.OfficialToolRequests = nil
	req.ToolModePrompt = false
	if req.ToolsEnabled {
		selectedTools := selectedToolIDSet(base.SelectedToolIDs, base.SelectedToolsConfigured)
		builtinDefs := filterBuiltinToolsByAccess(o.filterDisabledTools(o.tools.List(m.ID)), base.ToolAccessPolicy)
		if !store.MemoryEnabledForUser(ctx, o.db, base.UserID) {
			builtinDefs = filterToolDefsByName(builtinDefs, map[string]bool{"save_memory": true})
		}
		if req.Fast {
			builtinDefs = filterToolDefsByName(builtinDefs, map[string]bool{"python_execute": true, "fetch_image": true})
		}
		if base.SelectedToolsConfigured {
			builtinDefs = filterBuiltinToolsBySelection(builtinDefs, selectedTools)
		} else {
			builtinDefs = filterModelBuiltinTools(builtinDefs, fallbackBuiltinTools)
		}
		mcpDefs := filterMCPToolsByAccess(o.listMCPTools(m.ID), base.ToolAccessPolicy)
		if base.SelectedToolsConfigured {
			mcpDefs = filterMCPToolsBySelection(mcpDefs, selectedTools)
		} else {
			mcpDefs = filterModelMCPTools(mcpDefs, fallbackMCPServers)
		}
		req.Tools = append(builtinDefs, flattenMCPToolDefs(mcpDefs)...)
		req.OfficialToolNames, req.OfficialToolRequests = configuredOfficialToolRequests(m.OfficialTools, req.Fast)
		req.OfficialToolNames, req.OfficialToolRequests = filterHostedToolsByAccess(
			req.OfficialToolNames, req.OfficialToolRequests, base.ToolAccessPolicy,
		)
		if base.SelectedToolsConfigured {
			req.OfficialToolNames, req.OfficialToolRequests = filterHostedToolsBySelection(
				req.OfficialToolNames, req.OfficialToolRequests, selectedTools,
			)
		}
		req.ToolModePrompt = fallbackToolMode == "prompt" && len(req.Tools) > 0
	}
	// The primary history may contain calls that the fallback model does not
	// declare. Keep calls from either configured category and remove the rest.
	req.History = stripDisallowedBuiltinToolBlocks(
		req.History,
		unifiedToolNameSet(req.Tools, req.OfficialToolNames, req.OfficialToolRequests),
	)
	// Native Raw is tied to both the provider family and its wire format. The
	// primary request has already been converted with storeToUnified, so its Raw
	// may be valid for the primary channel even though the TTFT model is a
	// different provider (or OpenAI Chat vs Responses). Do not let a fallback
	// provider attempt to decode another vendor's exchange; the canonical blocks
	// below remain usable and preserve the visible conversation/tool trace.
	// Responses encrypted reasoning is bound to the model that produced it, not
	// merely to the provider/channel and wire format. A TTFT fallback can use a
	// different model on the same OpenAI Responses channel, so its history must
	// also pass the model-identity gate below.
	if !nativeHistoryCompatible(base.Model, ch.Type, ch.APIFormat) ||
		!nativeHistoryModelCompatible(base.Model.ID, m.ID) {
		for index := range req.History {
			if !isPromptToolRawEnvelope(req.History[index].Raw) {
				req.History[index].Raw = nil
			}
		}
	}
	// The base request may have been assembled for a native primary model and
	// therefore still carry provider Raw. A prompt-mode fallback exposes its local
	// Functions only through text, even when it also has hosted tools, so none of
	// that native exchange is legal to replay on the fallback request.
	if req.ToolModePrompt {
		for index := range req.History {
			if !isPromptToolRawEnvelope(req.History[index].Raw) {
				req.History[index].Raw = nil
			}
		}
	}
	if base.SystemPromptOptions != nil {
		fallbackOpts := *base.SystemPromptOptions
		// TTFT fallback is transparent: preserve the primary turn's identity and
		// admin behavior prompt (including the masked Fast label). Only capability-
		// dependent guidance is rebuilt for the model that will actually serve it.
		fallbackOpts.ToolMode = "none"
		if len(req.Tools) > 0 {
			fallbackOpts.ToolMode = fallbackToolMode
		}
		fallbackOpts.ToolNames = toolDefNames(req.Tools)
		fallbackOpts.SkillToolAvailable = toolDefsContain(req.Tools, "use_skill")
		if base.ToolAccessPolicy != nil {
			fallbackOpts.SkillMode = base.ToolAccessPolicy.SkillMode
		}
		if !toolDefsContain(req.Tools, "python_execute") {
			fallbackOpts.SandboxFiles = nil
		}

		// Skills are model-bound. Never carry the primary model's index or full
		// instructions across a TTFT switch, and do not let fallback broaden a
		// primary/global/per-turn policy that excluded use_skill.
		fallbackOpts.Skills = nil
		fallbackOpts.SkillsFull = nil
		selectedTools := selectedToolIDSet(base.SelectedToolIDs, base.SelectedToolsConfigured)
		fallbackAllowsSkills := toolAccessPolicyAllows(base.ToolAccessPolicy, "builtin:use_skill")
		if base.SelectedToolsConfigured {
			fallbackAllowsSkills = fallbackAllowsSkills && selectedTools["builtin:use_skill"]
		} else {
			fallbackAllowsSkills = fallbackAllowsSkills && (fallbackBuiltinTools == nil || fallbackBuiltinTools["use_skill"])
		}
		if fallbackOpts.SkillsAllowed && fallbackAllowsSkills && !globalDisabledTools["use_skill"] {
			fallbackOpts.Skills, fallbackOpts.SkillsFull = loadEnabledModelSkills(ctx, o.db, m.ID, base.ToolAccessPolicy)
		}
		fallbackOpts.SkillsAllowed = fallbackOpts.SkillsAllowed && fallbackAllowsSkills && !globalDisabledTools["use_skill"]

		req.SystemPrompt = composeSystemPrompt(fallbackOpts)
		req.SystemPromptOptions = &fallbackOpts
	}
	// §4.6-C fallback re-gate: the images in `base.History` were inlined against the
	// PRIMARY model's vision capability (resolveAttachments runs once, before any
	// fallback). If the fallback model can't see images, those blocks must not ride
	// to it — a text-only upstream rejects them ("unknown variant `image_url`,
	// expected `text`"). Rebuild the history without image blocks, image
	// attachments, or image-bearing native Raw (base remains untouched).
	if !m.Vision {
		req.History = stripImageBlocks(req.History)
	}
	name := m.Label
	if name == "" {
		name = m.RequestID
	}
	return req, prov, name, nil
}

func toolDefNameSet(definitions []ToolDef) map[string]bool {
	set := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		set[definition.Name] = true
	}
	return set
}

// nativeHistoryCompatible reports whether provider-native assistant Raw can be
// replayed after a TTFT model switch. Raw is not merely JSON: each provider
// expects a different message grammar, and OpenAI has two incompatible
// grammars behind the same provider id (Chat Completions and Responses).
// Unknown primary metadata is treated as incompatible so a fallback never
// guesses at a vendor-specific exchange.
func nativeHistoryCompatible(primary ModelInfo, fallbackProvider, fallbackFormat string) bool {
	primaryProvider := providerIDForChannelType(primary.Provider)
	resolvedFallbackProvider := providerIDForChannelType(fallbackProvider)
	if primaryProvider == "" || resolvedFallbackProvider == "" || primaryProvider != resolvedFallbackProvider {
		return false
	}
	return normalizeNativeAPIFormat(primaryProvider, primary.APIFormat) ==
		normalizeNativeAPIFormat(resolvedFallbackProvider, fallbackFormat)
}

// nativeHistoryModelCompatible reports whether provider-native history may be
// replayed between two model records. Native exchanges are model-scoped even
// when provider and API format match: OpenAI Responses reasoning items carry
// encrypted_content that a different model cannot decrypt. Missing identity is
// fail-closed because replaying an un-attributed encrypted exchange is unsafe.
func nativeHistoryModelCompatible(primaryModelID, fallbackModelID string) bool {
	primaryModelID = strings.TrimSpace(primaryModelID)
	fallbackModelID = strings.TrimSpace(fallbackModelID)
	return primaryModelID != "" && fallbackModelID != "" && primaryModelID == fallbackModelID
}

// nativeRawForPersistedModel keeps a provider-native exchange only when the
// message row identifies the model that actually produced it. TTFT fallback
// replies are intentionally billed/stored under the primary model, so their Raw
// cannot be safely attributed or replayed on a later turn. Canonical blocks
// remain persisted and preserve the visible answer/tool trace.
func nativeRawForPersistedModel(raw json.RawMessage, ttftFallbackModel string) json.RawMessage {
	// Prompt-protocol Raw is provider-neutral application metadata used only by
	// context compaction. It remains correctly attributable even when a TTFT model
	// fallback produced the turn, and provider adapters never replay it upstream.
	if isPromptToolRawEnvelope(raw) {
		return raw
	}
	if strings.TrimSpace(ttftFallbackModel) != "" {
		return nil
	}
	return raw
}

func normalizeNativeAPIFormat(provider, format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	// OpenAI channels historically defaulted to Chat Completions when the column
	// was empty. Treat that legacy value as "chat" for compatibility checks.
	if provider == "openai" && format == "" {
		return "chat"
	}
	return format
}

func unifiedToolNameSet(definitions []ToolDef, hostedNames []string, hostedRequests []json.RawMessage) map[string]bool {
	set := toolDefNameSet(definitions)
	for _, name := range hostedNames {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	// Hosted output items use provider tool types, which need not match the
	// administrator's definition label. Preserve those exact provider names on
	// later turns without aliasing them to Aivory's local Function namespace.
	merged := MergeOfficialToolRequests(nil, hostedRequests)
	if tools, ok := jsonArrayItems(merged["tools"]); ok {
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok {
				continue
			}
			// Anthropic server tools commonly use a versioned `type` alongside
			// the unversioned runtime `name` emitted by server_tool_use. Keep both:
			// the administrator-facing definition label is not required to equal
			// either provider field.
			toolName, _ := tool["name"].(string)
			if toolName = strings.TrimSpace(toolName); toolName != "" {
				set[toolName] = true
			}
			toolType, _ := tool["type"].(string)
			toolType = strings.TrimSpace(toolType)
			if toolType == "" {
				continue
			}
			set[toolType] = true
			switch toolType {
			case "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26":
				set["web_search"] = true
			}
		}
	}
	return set
}

func filterToolDefsByName(definitions []ToolDef, excluded map[string]bool) []ToolDef {
	if len(definitions) == 0 || len(excluded) == 0 {
		return definitions
	}
	filtered := make([]ToolDef, 0, len(definitions))
	for _, definition := range definitions {
		if !excluded[definition.Name] {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func toolDefNames(definitions []ToolDef) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

// resolveFallbackChannel returns the creds + id of a model's backup channel
// (§fallback channel), or (nil, "") when there is none or it's unusable. It is
// honoured only when the configured channel is DISTINCT from the primary, is
// enabled, carries an API key, and matches the primary's provider family +
// api_format. Provider aliases such as anthropic/claude and google/gemini are
// equivalent; a genuinely different wire format would receive the wrong payload.
// An unusable configured fallback is logged and ignored; the turn still runs on
// the primary channel.
func (o *Orchestrator) resolveFallbackChannel(ctx context.Context, model *store.Model, primary *store.Channel) (*ChannelCreds, string) {
	return resolveFallbackChannelForModel(ctx, o.db, o.logger, model, primary)
}

func resolveFallbackChannelForModel(ctx context.Context, db *sql.DB, logger *log.Logger, model *store.Model, primary *store.Channel) (*ChannelCreds, string) {
	fid := strings.TrimSpace(model.FallbackChannelID)
	if fid == "" || fid == model.ChannelID {
		return nil, ""
	}
	fc, err := store.GetChannel(ctx, db, fid)
	if err != nil {
		if logger != nil {
			logger.Printf("llm: model %q fallback channel %q not found — ignoring", model.ID, fid)
		}
		return nil, ""
	}
	sameProvider := providerIDForChannelType(fc.Type) != "" && providerIDForChannelType(fc.Type) == providerIDForChannelType(primary.Type)
	sameFormat := strings.EqualFold(strings.TrimSpace(fc.APIFormat), strings.TrimSpace(primary.APIFormat))
	if !fc.Enabled || !sameProvider || !sameFormat || fc.APIKey == "" {
		if logger != nil {
			logger.Printf("llm: model %q fallback channel %q unusable (enabled=%v type=%q/%q format=%q/%q hasKey=%v) — ignoring",
				model.ID, fid, fc.Enabled, fc.Type, primary.Type, fc.APIFormat, primary.APIFormat, fc.APIKey != "")
		}
		return nil, ""
	}
	return &ChannelCreds{BaseURL: fc.BaseURL, APIKey: fc.APIKey}, fc.ID
}

// truncErr caps a raw provider error for storage on the admin usage row — an
// upstream failure can carry a large response body. Trims on a rune boundary so
// the stored string stays valid UTF-8.
func truncErr(s string) string {
	max := 2000
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) > max {
		r = r[:max]
	}
	return string(r) + "…"
}

func providerRequestMediaStats(req UnifiedChatRequest) string {
	var images, docs int
	var imageBytes, docBytes int
	for _, m := range req.History {
		for _, b := range m.Blocks {
			size := approxBase64Bytes(b.Data)
			switch b.Kind {
			case "image":
				if b.Data != "" {
					images++
					imageBytes += size
				}
			case "document":
				if b.Data != "" {
					docs++
					docBytes += size
				}
			}
		}
	}
	return fmt.Sprintf("images=%d(%s) documents=%d(%s)", images, formatMediaBytes(imageBytes), docs, formatMediaBytes(docBytes))
}

func approxBase64Bytes(s string) int {
	if s == "" {
		return 0
	}
	return len(s) * 3 / 4
}

func formatMediaBytes(n int) string {
	const kb = 1024
	const mb = 1024 * kb
	if n >= mb {
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mb))
	}
	if n >= kb {
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(kb))
	}
	return fmt.Sprintf("%d B", n)
}

// settingInt / settingStr read an admin setting (JSON number or quoted string).
func settingInt(db *sql.DB, key string) int {
	raw, err := store.GetSetting(db, key)
	if err != nil || len(raw) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, e := strconv.Atoi(strings.TrimSpace(s)); e == nil {
			return v
		}
	}
	return 0
}

func settingStr(db *sql.DB, key string) string {
	raw, err := store.GetSetting(db, key)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func settingBool(db *sql.DB, key string, def bool) bool {
	raw, err := store.GetSetting(db, key)
	if err != nil || len(raw) == 0 {
		return def
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	return def
}

// successRequestLoggingEnabled reports whether SUCCESS usage rows should carry
// the sanitized provider-request snapshot (§B5-request-logging): only when the
// admin turned `log_full_requests` on AND `log_errors_only` off. Error rows
// always carry their snapshot regardless (unchanged floor behavior).
func (o *Orchestrator) successRequestLoggingEnabled() bool {
	return settingBool(o.db, "log_full_requests", false) && !settingBool(o.db, "log_errors_only", true)
}

// requestBodyLoggingEnabled is independent from the request-scope selector.
// Turning it off keeps method/URL/headers/error diagnostics while preventing
// prompt and payload bodies from being retained on either success or error rows.
func (o *Orchestrator) requestBodyLoggingEnabled() bool {
	return settingBool(o.db, "log_request_bodies", true)
}

// requestUsageRow is one per-upstream-request slice of a finished turn's usage
// (§B5-per-request usage rows).
type requestUsageRow struct {
	Usage              Usage
	Cost               float64
	Credits            float64
	Method             string
	URL                string
	Header             string
	Body               string
	Fallback           bool
	ChannelAttribution bool
}

// perRequestUsageRows splits one finished turn into successful per-upstream-
// request usage rows. Failed attempts are emitted separately by
// providerFailureUsageLogs and never share the successful turn's bill. Every
// completed provider round (native tool loop iteration, prompt-protocol round,
// deep-research call) carries its own tokens and request snapshot. Billing
// invariants hold exactly:
// row token sums equal the turn totals (any un-attached residual — list
// overflow, TTFT model fallback — folds into the LAST row), row costs sum to
// totalCost, and row credits sum to totalCredits (distributed by cost share so
// analytics retain the exact turn charge). When fewer than two rounds
// carried usage, the turn stays a single row exactly as before. includeReq
// gates the request-snapshot fields per the §B5 admin settings.
func perRequestUsageRows(snaps []providerRequestSnapshot, model *store.Model, total Usage, totalCost, totalCredits float64, includeReq bool) []requestUsageRow {
	withUsage := make([]providerRequestSnapshot, 0, len(snaps))
	for _, s := range snaps {
		if s.HasUsage && strings.TrimSpace(s.Error) == "" {
			withUsage = append(withUsage, s)
		}
	}
	var last providerRequestSnapshot
	if n := len(snaps); n > 0 {
		last = snaps[n-1]
	}
	if len(withUsage) < 2 {
		row := requestUsageRow{Usage: total, Cost: totalCost, Credits: totalCredits}
		if len(snaps) > 0 {
			row.Fallback = last.Fallback
			row.ChannelAttribution = true
		}
		if includeReq {
			row.Method, row.URL, row.Header, row.Body = last.Method, last.URL, last.Header, last.Body
		}
		return []requestUsageRow{row}
	}
	rows := make([]requestUsageRow, len(withUsage))
	summed := Usage{}
	for i, s := range withUsage {
		rows[i] = requestUsageRow{Usage: s.Usage, Fallback: s.Fallback, ChannelAttribution: true}
		if includeReq {
			rows[i].Method, rows[i].URL, rows[i].Header, rows[i].Body = s.Method, s.URL, s.Header, s.Body
		}
		summed.InputTokens += s.Usage.InputTokens
		summed.OutputTokens += s.Usage.OutputTokens
		summed.CacheReadTokens += s.Usage.CacheReadTokens
		summed.CacheWriteTokens += s.Usage.CacheWriteTokens
	}
	// Residual reconciliation: whatever the attaches missed lands on the last
	// row so Σrows == turn totals exactly (never negative per field).
	lastRow := &rows[len(rows)-1]
	lastRow.Usage.InputTokens = maxInt(lastRow.Usage.InputTokens+(total.InputTokens-summed.InputTokens), 0)
	lastRow.Usage.OutputTokens = maxInt(lastRow.Usage.OutputTokens+(total.OutputTokens-summed.OutputTokens), 0)
	lastRow.Usage.CacheReadTokens = maxInt(lastRow.Usage.CacheReadTokens+(total.CacheReadTokens-summed.CacheReadTokens), 0)
	lastRow.Usage.CacheWriteTokens = maxInt(lastRow.Usage.CacheWriteTokens+(total.CacheWriteTokens-summed.CacheWriteTokens), 0)
	// Distribute the turn's EXACT billed totals (totalCost / totalCredits) across
	// rows by each row's own priced weight, with the last row taking the exact
	// remainder. Weighting by the normalized share (not by absolute per-row cost)
	// keeps Σrows == totals even when the attached per-request usages sum to MORE
	// than the turn total — e.g. the stop/cancel path attaches each request's
	// full usage but bills only the partial turn. A naive "totalCost - Σper-row"
	// would clamp a negative remainder to 0 and let Σ exceed the billed amount,
	// silently over-charging cost and inflating the analytics credit total.
	weights := make([]float64, len(rows))
	weightSum := 0.0
	for i := range rows {
		weights[i] = computeCost(*model, rows[i].Usage)
		weightSum += weights[i]
	}
	costAcc, creditAcc := 0.0, 0.0
	for i := range rows[:len(rows)-1] {
		var share float64
		if weightSum > 0 {
			share = weights[i] / weightSum
		} else {
			share = 1.0 / float64(len(rows)) // zero-priced turn: split evenly
		}
		rows[i].Cost = totalCost * share
		costAcc += rows[i].Cost
		if totalCredits > 0 {
			rows[i].Credits = totalCredits * share
			creditAcc += rows[i].Credits
		}
	}
	lastRow.Cost = totalCost - costAcc
	if lastRow.Cost < 0 {
		lastRow.Cost = 0
	}
	if totalCredits > 0 {
		lastRow.Credits = totalCredits - creditAcc
		if lastRow.Credits < 0 {
			lastRow.Credits = 0
		}
	}
	return rows
}

func requestUsageChannel(row requestUsageRow, primaryChannelID, fallbackChannelID string, turnUsedFallback bool) (string, bool) {
	if row.ChannelAttribution {
		if row.Fallback && fallbackChannelID != "" {
			return fallbackChannelID, true
		}
		return primaryChannelID, false
	}
	if turnUsedFallback && fallbackChannelID != "" {
		return fallbackChannelID, true
	}
	return primaryChannelID, false
}

// providerFailureUsageLogs converts failed upstream attempts into zero-cost
// admin usage rows. A recovered primary failure and its successful fallback use
// the same message_id but remain separate records, so observability improves
// without changing billing, quota, or credit accounting.
func providerFailureUsageLogs(snaps []providerRequestSnapshot, base store.UsageLog, primaryChannelID, fallbackChannelID string) []store.UsageLog {
	rows := make([]store.UsageLog, 0, len(snaps))
	for _, snap := range snaps {
		if strings.TrimSpace(snap.Error) == "" {
			continue
		}
		row := base
		row.InputTokens = 0
		row.OutputTokens = 0
		row.CacheReadTokens = 0
		row.CacheWriteTokens = 0
		row.ImagesCount = 0
		row.Cost = 0
		row.Credits = 0
		row.Status = "error"
		row.Error = snap.Error
		row.RequestMethod = snap.Method
		row.RequestURL = snap.URL
		row.RequestHeaders = snap.Header
		row.RequestBody = snap.Body
		row.Fallback = snap.Fallback
		if snap.Fallback {
			row.ChannelID = fallbackChannelID
		} else {
			row.ChannelID = primaryChannelID
		}
		rows = append(rows, row)
	}
	return rows
}

func providerFailureCaptured(snaps []providerRequestSnapshot, err error) bool {
	if err == nil {
		return false
	}
	target := truncErr(err.Error())
	for _, snap := range snaps {
		if snap.Error == target {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Run executes one turn end to end. It blocks while streaming.
// onEvent is invoked on every SSE event so the HTTP handler can flush.
func (o *Orchestrator) Run(ctx context.Context, req RunRequest, onEvent func(SseEvent)) (*RunResult, error) {
	visibleOutput := new(atomic.Bool)
	onEvent = observeProviderVisibleOutput(onEvent, visibleOutput)
	// 1. Load conversation + resolve model.
	conv, err := store.GetConversation(ctx, o.db, req.ConversationID, req.UserID)
	if err != nil {
		return nil, err
	}
	currentPermissions, err := store.UserGroupPermissionsForUser(ctx, o.db, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("resolve current user-group permissions: %w", err)
	}
	req.ToolAccessPolicy = intersectToolAccessPolicies(req.ToolAccessPolicy, groupToolAccessPolicy(currentPermissions))
	if !currentPermissions.AllowKnowledgeBases &&
		(strings.TrimSpace(conv.ProjectID) != "" || len(conversationKnowledgeBaseSelection(conv, req)) > 0) {
		return nil, ErrKnowledgeBasePermission
	}
	turnKBIDs, err := resolveConversationKnowledgeBaseSelection(ctx, o.db, conv, req)
	if err != nil {
		return nil, fmt.Errorf("resolve knowledge-base selection: %w", err)
	}
	// §fast-mode: a fast turn resolves the model server-side from the admin's
	// single fast model and never uses the client's ModelID. If none is configured
	// (or it's disabled), fall back to normal resolution so the turn still runs —
	// the composer gates the toggle on fast_available, so this is a belt-and-
	// suspenders degrade, not a normal path.
	fastMode := false
	modelID := req.ModelID
	if req.Fast {
		if fm, ferr := store.GetFastModel(ctx, o.db); ferr == nil && fm != nil {
			modelID = fm.ID
			fastMode = true
		}
	}
	if modelID == "" {
		modelID = conv.ModelID
	}
	if modelID == "" {
		if raw, err := store.GetSetting(o.db, "default_model_id"); err == nil {
			_ = json.Unmarshal(raw, &modelID)
		}
	}
	if modelID == "" {
		return nil, errors.New("no model configured (set settings.default_model_id)")
	}
	model, err := store.GetModel(ctx, o.db, modelID)
	if err != nil {
		return nil, fmt.Errorf("load model: %w", err)
	}
	if !model.Enabled {
		return nil, errors.New("model is disabled")
	}
	if model.Kind == "image" && req.ToolAccessPolicy != nil && !req.ToolAccessPolicy.AllowDrawing {
		return nil, ErrDrawingPermission
	}
	// §fast-mode locks the turn's shape: no Verify, no Deep Research, and tools
	// stay ON (fast can't disable tools) but run on a quartered budget without
	// python_execute — enforced where tools + budgets are assembled below.
	if fastMode {
		req.Verify = false
		// `modelId` remains on the user's last advanced choice while the UI is in
		// fast mode. Never let its cached controls (for example `thinking`) leak
		// into the hidden fast model, including from non-browser callers.
		req.ParamOverrides = nil
		if req.Mode == ModeDeepResearch {
			req.Mode = ""
		}
	}
	// Deep Research is model-scoped as well as group-scoped. The API handler
	// already strips the mode for users without the group feature; this guard
	// covers the resolved model (including defaults/regenerate) so a client can't
	// force deep research for a model where admins disabled exposure.
	if req.Mode == ModeDeepResearch && !model.ResearchEnabled {
		req.Mode = ""
	}
	turnToolMode, err := resolveRunToolMode(req)
	if err != nil {
		return nil, err
	}
	// These pipelines have fixed tool semantics and never pay for an automatic
	// routing call. req.Fast is included even when no fast model is currently
	// configured, matching the API's defense-in-depth normalization.
	if req.Fast || req.Mode == ModeDeepResearch {
		turnToolMode = ToolModeEnabled
	}
	req.ToolMode = turnToolMode
	// Per-user hosted-tool selections were retired. Ignore the compatibility
	// field for every mode; administrator model configuration is authoritative.
	req.OfficialToolNames = nil
	req.NoTools = turnToolMode == ToolModeDisabled
	if !req.NoTools {
		// Forced web search is the explicit-disabled fallback, not an additional
		// behavior for enabled or automatically routed turns.
		req.ForceWebSearch = false
	}
	channel, err := store.GetChannel(ctx, o.db, model.ChannelID)
	if err != nil {
		return nil, err
	}
	provider, err := o.reg.Get(channel.Type)
	if err != nil {
		return nil, err
	}

	// Resolve private skills before persisting either message. This repeats the
	// API boundary check for non-HTTP callers and prevents a forged/not-owned id
	// from ever reaching history or durable message metadata.
	selectedUserSkills := []store.UserSkill{}
	normalizedSelectedUserSkillIDs := []string{}
	if !req.ReuseExistingUserMessage {
		selectedUserSkills, normalizedSelectedUserSkillIDs, err = store.ResolveUserSkillSelectionScoped(ctx, o.db, req.UserID, conv.WorkspaceID, req.SelectedUserSkillIDs, true)
		if err != nil {
			return nil, err
		}
		if err := validateUserSkillAccessPolicy(req.ToolAccessPolicy, selectedUserSkills); err != nil {
			return nil, err
		}
	}

	parentID := req.ParentID
	if parentID != "" {
		parent, parentErr := store.GetMessage(ctx, o.db, parentID)
		valid := parentErr == nil && parent.ConversationID == conv.ID
		if valid && req.ReuseExistingUserMessage && parent.Role != "user" {
			valid = false
		}
		if parentErr != nil && !errors.Is(parentErr, store.ErrNotFound) {
			return nil, fmt.Errorf("validate message parent: %w", parentErr)
		}
		if !valid {
			// A branch edit names the exact historical node it must fork from. Falling
			// back here would silently graft the edit onto a different branch. A normal
			// append, however, can safely recover to the conversation's committed leaf.
			if req.Branch || req.ReuseExistingUserMessage {
				return nil, invalidMessageParentError(parentID)
			}
			resolvedParent, _, resolveErr := store.ResolveConversationAppendParent(ctx, o.db, conv.ID, conv.ActiveLeafID)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve append parent: %w", resolveErr)
			}
			if o.logger != nil {
				o.logger.Printf("orchestrator: recovered invalid explicit append parent (conv=%s requested_parent=%q active_leaf=%q append_parent=%q)",
					conv.ID, parentID, conv.ActiveLeafID, resolvedParent)
			}
			parentID = resolvedParent
		}
	}
	// Only a normal append falls back to the active leaf. A branch edit with an
	// empty parent (editing the root question) must stay a root sibling (§4.15)
	// rather than being grafted onto the conversation tail.
	if parentID == "" && !req.Branch {
		resolvedParent, repaired, rerr := store.ResolveConversationAppendParent(ctx, o.db, conv.ID, conv.ActiveLeafID)
		if rerr != nil {
			return nil, fmt.Errorf("resolve append parent: %w", rerr)
		}
		parentID = resolvedParent
		if repaired && o.logger != nil {
			o.logger.Printf("orchestrator: recovered invalid active leaf (conv=%s active_leaf=%q append_parent=%q)",
				conv.ID, conv.ActiveLeafID, parentID)
		}
	}

	// 2. Persist user message + assistant placeholder.
	//    §4.15 regenerate fork-at-assistant: when the caller passes
	//    ReuseExistingUserMessage, parentID is the EXISTING user message id.
	//    We skip inserting a new user turn and parent the assistant directly
	//    to that user — producing a sibling reply, not a sibling question.
	var userMsg *store.Message
	assistantParent := ""
	if req.ReuseExistingUserMessage && parentID != "" {
		existing, gerr := store.GetMessage(ctx, o.db, parentID)
		if gerr == nil && existing.Role == "user" && existing.ConversationID == conv.ID {
			userMsg = existing
			assistantParent = existing.ID
			// §4.20: regenerate doesn't resend attachments. The image branch reads
			// req.Attachments directly (reference / edit images), so restore them
			// from the existing user turn — otherwise a re-draw of an edit loses its
			// source image and starts fresh. (The chat path rebuilds from history.)
			if len(req.Attachments) == 0 && len(existing.Attachments) > 2 {
				var atts []Attachment
				if json.Unmarshal(existing.Attachments, &atts) == nil {
					req.Attachments = atts
				}
			}
			// Regeneration reuses the skill selection persisted on the original
			// user turn. Resolve it under the CURRENT caller's ownership: members of
			// a shared conversation must never read another user's private skills.
			var persistedIDs []string
			_ = json.Unmarshal(existing.SelectedUserSkillIDs, &persistedIDs)
			selectedUserSkills, normalizedSelectedUserSkillIDs, err = store.ResolveUserSkillSelectionScoped(ctx, o.db, req.UserID, conv.WorkspaceID, persistedIDs, false)
			if err != nil {
				return nil, err
			}
			if err := validateUserSkillAccessPolicy(req.ToolAccessPolicy, selectedUserSkills); err != nil {
				return nil, err
			}
		}
	}
	if userMsg == nil {
		atts, _ := json.Marshal(req.Attachments)
		selectedIDs, _ := json.Marshal(normalizedSelectedUserSkillIDs)
		userBlocksList := []UnifiedBlock{}
		if strings.TrimSpace(req.UserText) != "" {
			userBlocksList = append(userBlocksList, UnifiedBlock{Kind: "text", Text: req.UserText})
		}
		userBlocks, _ := json.Marshal(userBlocksList)
		created, err := store.CreateMessageForUser(ctx, o.db, store.Message{
			ConversationID: conv.ID, ParentID: parentID, Role: "user",
			Provider: channel.Type, ModelID: model.ID, Fast: fastMode,
			Blocks: userBlocks, Attachments: atts, SelectedUserSkillIDs: selectedIDs,
			AuthorID: req.UserID, // §workspaces: shared conversations attribute each question
		}, req.UserID)
		if err != nil {
			// The parent can be deleted after the validation above but before this
			// transaction wins the race. Preserve the domain contract even in that
			// narrow window; never leak a PostgreSQL/SQLite FK diagnostic to callers.
			return nil, normalizeMessageCreateError("save user message", parentID, err)
		}
		userMsg = created
		assistantParent = created.ID
	}
	// Turn start — used to record per-reply generation time (gen_ms, shown in UI).
	turnStart := time.Now()
	assistantMsg, err := store.CreateMessageForUser(ctx, o.db, store.Message{
		ConversationID: conv.ID, ParentID: assistantParent, Role: "assistant",
		Provider: channel.Type, ModelID: model.ID, Fast: fastMode,
		Blocks: []byte("[]"), Status: "streaming", AuthorID: req.UserID,
	}, req.UserID)
	if err != nil {
		// A concurrent round/conversation deletion can remove assistantParent after
		// the user row was validated or inserted. Apply the same domain mapping as
		// the user insert so this second FK window cannot leak database diagnostics.
		return nil, normalizeMessageCreateError("save assistant placeholder", assistantParent, err)
	}
	msgcache.Bump(o.cache, conv.ID)
	onEvent(SseEvent{Type: "message_start", MessageID: assistantMsg.ID})
	var generationAccessRevoked atomic.Bool
	emitEvent := onEvent
	onEvent = func(event SseEvent) {
		if generationAccessRevoked.Load() {
			return
		}
		emitEvent(event)
	}
	messageTerminal := false
	providerCompleted := false
	finishMessage := func(writeCtx context.Context, p store.MessageFinishPatch) error {
		// Before a provider has successfully returned, an explicit turn cancel wins
		// over quota/moderation/setup exits that happen to race with Stop. Once the
		// provider has completed, its full result wins over a late Stop at the narrow
		// DB-finalization boundary.
		if ctx.Err() != nil && !providerCompleted && p.StopReason != "stopped" {
			p = store.MessageFinishPatch{
				Blocks:     []byte("[]"),
				Citations:  []byte("[]"),
				StopReason: "stopped",
				Status:     "stopped",
				GenMs:      time.Since(turnStart).Milliseconds(),
			}
		}
		// Terminal persistence must outlive the generation signal. A Stop can arrive
		// after the provider has returned a complete result but just before the DB
		// update; writing on the canceled turn context would fail and let the fallback
		// replace a full answer with empty stopped blocks.
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(writeCtx), 10*time.Second)
		defer persistCancel()
		err := store.FinishMessageForUser(persistCtx, o.db, assistantMsg.ID, conv.ID, req.UserID, p)
		if errors.Is(err, store.ErrConversationAccessRevoked) {
			generationAccessRevoked.Store(true)
			// The store committed a scrubbed stopped row before returning the
			// sentinel, so the deferred placeholder repair has nothing left to do.
			messageTerminal = true
		}
		if err == nil {
			msgcache.Bump(o.cache, conv.ID)
			if p.Status != "" && p.Status != "streaming" {
				messageTerminal = true
			}
		}
		return err
	}

	// The assistant placeholder is already visible and persisted. From here on,
	// every exit must settle it, including cancellation during quota/moderation,
	// image setup, history/RAG loading, or another early failure before the
	// provider loop reaches its normal finalize block.
	defer func() {
		if messageTerminal {
			return
		}
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer persistCancel()

		// Image turns own their normal finalization path and do not call the local
		// finishMessage wrapper. Preserve a terminal row they already committed;
		// only repair a placeholder that is still streaming.
		if persisted, err := store.GetMessage(persistCtx, o.db, assistantMsg.ID); err == nil &&
			persisted.Status != "" && persisted.Status != "streaming" {
			return
		}

		patch := store.MessageFinishPatch{
			Blocks:    []byte("[]"),
			Citations: []byte("[]"),
			Status:    "error",
			Error:     "The message could not be processed. Please try again.",
			GenMs:     time.Since(turnStart).Milliseconds(),
		}
		if ctx.Err() != nil {
			patch.Status = "stopped"
			patch.StopReason = "stopped"
			patch.Error = ""
		}
		if err := finishMessage(persistCtx, patch); err != nil && o.logger != nil {
			o.logger.Printf("orchestrator: settle abandoned assistant placeholder (conv=%s msg=%s): %v", conv.ID, assistantMsg.ID, err)
		}
	}()

	// Persist new conversation defaults. §fast-mode: a fast turn must NOT write the
	// resolved fast model onto the conversation (that would both leak its identity
	// to the picker and silently reuse it on the next non-fast turn). Instead only
	// flip the conversation's `fast` marker so reopening restores the 快速 pill; a
	// normal turn writes model/provider and clears `fast`.
	tmpFast := fastMode
	if fastMode {
		_, _ = store.UpdateConversation(ctx, o.db, conv.ID, req.UserID, store.ConversationPatch{
			Fast: &tmpFast,
		})
	} else {
		tmpModelID := model.ID
		tmpProvider := channel.Type
		_, _ = store.UpdateConversation(ctx, o.db, conv.ID, req.UserID, store.ConversationPatch{
			ModelID: &tmpModelID, Provider: &tmpProvider, Fast: &tmpFast,
		})
	}

	// 2b. Per-model group quota (§ user groups): if the user's group can't use
	//     this model, or its window quota is exhausted, persist a refusal and
	//     stop before generating.
	//     §4.20: image-kind models meter against the purpose='image' ledger
	//     (checkImageQuota) so drawing mode follows the SAME free-allotment →
	//     credits flow as chat; the tool's own hard cap is skipped in that path
	//     (tc.SkipImageQuota) so the orchestrator's credit decision governs.
	var msg string
	var ok bool
	var turnBilling *billingAdmission
	var imageRequestParams map[string]any
	imageGenerationCount := imageModeForcedGenerationCount
	if model.Kind == "image" {
		imageRequestParams = MergeParamControls(nil, model.ParamControls, req.ParamOverrides)
		imageGenerationCount = imageGenerationCountFromParams(imageRequestParams)
		turnBilling, msg, err = o.reserveUsageBilling(
			ctx, req.UserID, model, store.QuotaScopeModelImage, float64(imageGenerationCount),
			float64(imageGenerationCount)*model.PricePerImage, 0, "image_turn", assistantMsg.ID+":direct",
		)
		if err != nil {
			return nil, err
		}
		ok = turnBilling != nil
	} else {
		msg, ok, _, _ = o.checkModelQuota(ctx, req.UserID, model)
	}
	if !ok {
		if ctx.Err() != nil {
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks: []byte("[]"), Citations: []byte("[]"), StopReason: "stopped", Status: "stopped",
			})
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "stopped"})
			return &RunResult{UserMessage: userMsg, AssistantMessage: assistantMsg}, nil
		}
		refusalBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: msg}})
		_ = finishMessage(ctx, store.MessageFinishPatch{
			Blocks: refusalBlocks, Citations: []byte("[]"), StopReason: "quota_exceeded", Status: "complete",
		})
		onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: msg})
		onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "quota_exceeded"})
		assistantMsg.Blocks = refusalBlocks
		return &RunResult{UserMessage: userMsg, AssistantMessage: assistantMsg}, nil
	}
	if turnBilling != nil {
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			if releaseErr := o.releaseUsageBilling(releaseCtx, turnBilling); releaseErr != nil && o.logger != nil {
				o.logger.Printf("release turn billing reservation (msg=%s): %v", assistantMsg.ID, releaseErr)
			}
		}()
	}
	if model.Kind == "image" {
		// Direct drawing and normal-chat image_generate must resolve the same image
		// model and the same last user selections. Store control PICKS (not provider
		// fields); MergeParamControls re-validates them when the chat tool uses them.
		_, _ = store.UpdateUserSettings(ctx, o.db, req.UserID, map[string]any{
			"image_model_id": model.ID,
			"image_model_params": map[string]any{
				"model_id": model.ID,
				"params":   req.ParamOverrides,
			},
		})
	}

	// 2c. Content moderation (§ moderation): screen the new user prompt alone
	//     (no history) before any provider call. On block, persist a refusal and
	//     stop — generation never runs.
	selectedUserSkillText := formatSelectedUserSkills(selectedUserSkills)
	moderationText := req.UserText + selectedUserSkillText
	blocked, moderationMessage, moderationErr := o.moderatePrompt(ctx, model, moderationText, req.UserID, conv.ID, assistantMsg.ID)
	if moderationErr != nil {
		return nil, moderationErr
	}
	if blocked {
		msg := moderationMessage
		refusalBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: msg}})
		_ = finishMessage(ctx, store.MessageFinishPatch{
			Blocks: refusalBlocks, Citations: []byte("[]"), StopReason: "content_moderation", Status: "complete",
		})
		onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: msg})
		onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "content_moderation"})
		assistantMsg.Blocks = refusalBlocks
		return &RunResult{UserMessage: userMsg, AssistantMessage: assistantMsg}, nil
	}

	// 2d. §4.20 Image mode: when the conversation model is an image model, this
	//     turn DRAWS instead of chatting. We force-call the existing image_generate
	//     tool (which owns the Gemini/OpenAI generation/edit protocols and
	//     branch-aware continuation, quota and usage logging) and persist its artifacts as the
	//     assistant message. Chat tools (python/sandbox) stay available by
	//     switching back to a chat model in the same conversation.
	if model.Kind == "image" {
		return o.runImageTurn(ctx, conv, model, userMsg, assistantMsg, req, imageRequestParams, imageGenerationCount, turnStart, turnBilling, onEvent)
	}

	// 3. Build context.
	projectName := ""
	projectInstructions := ""
	projectFiles := []ProjectFileSummary{}
	kbIDs := []string{}
	if conv.ProjectID != "" {
		if p, err := store.GetProject(ctx, o.db, conv.ProjectID, req.UserID); err == nil {
			projectName = p.Name
			projectInstructions = p.Instructions
			if p.KBID != "" {
				kbIDs = append(kbIDs, p.KBID)
				docs, _ := store.ListDocuments(ctx, o.db, "kb", p.KBID)
				for _, d := range docs {
					projectFiles = append(projectFiles, ProjectFileSummary{Name: d.Filename, Kind: d.MimeType})
				}
			}
		}
	}
	// Explicit per-turn selections were strictly resolved before this turn wrote
	// messages or reserved billing. Persisted selections retain their historical
	// fail-closed filtering behavior. The project library is authorized by the
	// project lookup above and remains implicit rather than client-selectable.
	kbIDs = append(kbIDs, turnKBIDs...)

	// 4. Load full path history (the RAG router + compaction both need it).
	history, err := msgcache.ListMessages(ctx, o.cache, o.db, conv.ID, userMsg.ID)
	if err != nil {
		return nil, err
	}
	// Resolve staged files before automatic tool routing. The route model receives
	// only a presence bit; exact file names are used solely by deterministic local
	// fast paths and never leave this process.
	sandboxFiles := listSandboxFiles(ctx, o.db, conv.ID, req.UserID, o.uploadDir, o.artifactDir)
	builtinTools := modelBuiltinToolSet(model.BuiltinTools)
	mcpServers := modelMCPServerIDSet(model.MCPServerIDs)
	globalDisabledTools := o.disabledToolSet()
	selectedTools := selectedToolIDSet(req.SelectedToolIDs, req.SelectedToolsConfigured)
	memoryEnabled := store.MemoryEnabledForUser(ctx, o.db, req.UserID)
	// Load bound skill metadata before automatic routing. Exact skill names are
	// checked locally; descriptions and full instructions never enter the route
	// request.
	availableSkillIdx := []SkillIndex{}
	availableSkillFull := []SkillFull{}
	skillsAllowed := !globalDisabledTools["use_skill"] &&
		toolAccessPolicyAllows(req.ToolAccessPolicy, "builtin:use_skill")
	if req.SelectedToolsConfigured {
		skillsAllowed = skillsAllowed && selectedTools["builtin:use_skill"]
	} else {
		skillsAllowed = skillsAllowed && (builtinTools == nil || builtinTools["use_skill"])
	}
	if skillsAllowed {
		availableSkillIdx, availableSkillFull = loadEnabledModelSkills(ctx, o.db, model.ID, req.ToolAccessPolicy)
	}

	// 5. Resolve tools for this model BEFORE composing the system prompt so the
	//    tool-guidance segment (and the §4.13 prompt preamble) match the real,
	//    enabled tool list instead of a hardcoded set.
	// tool_mode=none is the administrator-level deny-all ceiling. Otherwise load
	// both configured categories up front: local Function definitions from the
	// registry and every provider-hosted request fragment in administrator order.
	toolMode := model.ToolMode
	if toolMode == "" {
		toolMode = "native"
	}
	hostedToolNames := []string(nil)
	hostedToolRequests := []json.RawMessage(nil)
	toolDefs := []ToolDef{}
	if toolMode != "none" {
		hostedToolNames, hostedToolRequests = configuredOfficialToolRequests(model.OfficialTools, fastMode)
		hostedToolNames, hostedToolRequests = filterHostedToolsByAccess(hostedToolNames, hostedToolRequests, req.ToolAccessPolicy)
		if req.SelectedToolsConfigured {
			hostedToolNames, hostedToolRequests = filterHostedToolsBySelection(hostedToolNames, hostedToolRequests, selectedTools)
		}
		builtinDefs := filterBuiltinToolsByAccess(o.filterDisabledTools(o.tools.List(model.ID)), req.ToolAccessPolicy)
		if !memoryEnabled {
			builtinDefs = filterToolDefsByName(builtinDefs, map[string]bool{"save_memory": true})
		}
		// §fast-mode withholds python_execute (no sandbox on a fast turn) — drop it
		// from local Functions and the hosted code interpreter from provider tools.
		// Tool budgets are also quartered via ToolContext.Fast (charge()).
		if fastMode {
			builtinDefs = filterToolDefsByName(builtinDefs, map[string]bool{"python_execute": true})
		}
		if req.SelectedToolsConfigured {
			builtinDefs = filterBuiltinToolsBySelection(builtinDefs, selectedTools)
		} else {
			builtinDefs = filterModelBuiltinTools(builtinDefs, builtinTools)
		}
		mcpDefs := filterMCPToolsByAccess(o.listMCPTools(model.ID), req.ToolAccessPolicy)
		if req.SelectedToolsConfigured {
			mcpDefs = filterMCPToolsBySelection(mcpDefs, selectedTools)
		} else {
			mcpDefs = filterModelMCPTools(mcpDefs, mcpServers)
		}
		toolDefs = append(builtinDefs, flattenMCPToolDefs(mcpDefs)...)
	}
	if req.ToolMode == ToolModeAuto {
		// An effective deny-all policy has nothing the classifier could enable.
		// Enter the same no-tools pipeline immediately and avoid a wasted task-model
		// round trip.
		if len(toolDefs) == 0 && len(hostedToolRequests) == 0 {
			req.NoTools = true
		} else {
			req.NoTools = !o.autoTurnNeedsTools(
				ctx, req, history, toolDefs, hostedToolNames, hostedToolRequests,
				sandboxFiles, availableSkillIdx, len(selectedUserSkills) > 0,
				conv.WorkspaceID, assistantMsg.ID,
			)
		}
	}
	// The explicit disabled policy and an auto=false verdict share exactly the
	// same no-tools behavior: no provider/hosted declarations, no tool guidance,
	// no skills, and server-side RAG/search/spreadsheet fallbacks below.
	if req.NoTools {
		toolMode = "none"
		hostedToolNames = nil
		hostedToolRequests = nil
		toolDefs = nil
	}
	toolsEnabled := !req.NoTools && toolMode != "none"
	useHostedTools := len(hostedToolRequests) > 0
	hostedImageEnabled := useHostedTools && channel.Type == "openai" && channel.APIFormat == "responses" &&
		responsesRequestHasToolType(MergeOfficialToolRequests(nil, hostedToolRequests), "image_generation")
	var hostedImageBilling *billingAdmission
	var hostedDailyImageQuota *store.QuotaReservation
	// Automatic routing is resolved before admission so an auto=false turn never
	// reserves image quota or credits for a hosted tool it will not receive.
	if hostedImageEnabled {
		hostedDailyImageQuota, err = o.checkDailyImageLimit(ctx, req.UserID, 1)
		if err != nil {
			if !errors.Is(err, ErrDailyImageLimitReached) {
				if o.logger != nil {
					o.logger.Printf("hosted image daily quota check failed (conv=%s msg=%s): %v", conv.ID, assistantMsg.ID, err)
				}
				const safeErr = "Image generation is temporarily unavailable. Please try again."
				errBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: safeErr}})
				_ = finishMessage(ctx, store.MessageFinishPatch{
					Blocks: errBlocks, Citations: []byte("[]"), Status: "error", Error: safeErr,
				})
				onEvent(SseEvent{Type: "error", MessageID: assistantMsg.ID, Message: safeErr})
				return nil, err
			}
			message := ErrDailyImageLimitReached.Error()
			refusalBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: message}})
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks: refusalBlocks, Citations: []byte("[]"), StopReason: "quota_exceeded", Status: "complete",
			})
			onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: message})
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "quota_exceeded"})
			assistantMsg.Blocks = refusalBlocks
			return &RunResult{UserMessage: userMsg, AssistantMessage: assistantMsg}, nil
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_ = o.releaseUsageBilling(releaseCtx, hostedImageBilling)
			if hostedDailyImageQuota != nil {
				_ = store.ReleaseQuotaReservation(releaseCtx, o.db, hostedDailyImageQuota.ID)
			}
		}()
		hostedImageBilling, msg, err = o.reserveUsageBilling(
			ctx, req.UserID, model, store.QuotaScopeModelImage, 1, model.PricePerImage, 0,
			"hosted_image", assistantMsg.ID+":hosted-image",
		)
		if err != nil {
			return nil, err
		}
		if hostedImageBilling == nil {
			refusalBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: msg}})
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks: refusalBlocks, Citations: []byte("[]"), StopReason: "quota_exceeded", Status: "complete",
			})
			onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: msg})
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "quota_exceeded"})
			assistantMsg.Blocks = refusalBlocks
			return &RunResult{UserMessage: userMsg, AssistantMessage: assistantMsg}, nil
		}
	}
	toolNames := make([]string, 0, len(toolDefs))
	skillToolAvailable := false
	pythonExecuteAvailable := false
	for _, t := range toolDefs {
		toolNames = append(toolNames, t.Name)
		if t.Name == "use_skill" {
			skillToolAvailable = true
		}
		if t.Name == "python_execute" {
			pythonExecuteAvailable = true
		}
	}

	// 6. RAG via the §4.11-B query router (intent-classify + query-rewrite),
	//    not a blind always-on retrieve. The session's rag_mode overrides:
	//    inject = always retrieve without routing; auto = router. Retired or
	//    otherwise unknown values degrade to auto so documents are never hidden.
	ragSnippets := []Citation{}
	ragMode := store.NormalizeConversationRAGMode(conv.RAGMode)
	hasAttachedKnowledgeBase := len(kbIDs) > 0
	// Chat uploads are rejected by the HTTP handler until their document_id is
	// status='ready'. Do not wait-and-skip here: skipping pending docs is exactly
	// what made the model fall back to python-side PDF parsing.
	// §4.11-B: run inline RAG when a KB is bound OR the conversation itself has an
	// ingested upload (chat-attached files are conversation-scoped, not in a KB).
	ragScoped := len(kbIDs) > 0 || (o.rag != nil && store.ConversationHasReadyDocs(ctx, o.db, conv.ID))
	currentDocumentIDs := attachmentDocumentIDs(req.Attachments)
	if o.rag != nil && ragScoped && req.Mode != ModeDeepResearch {
		ragCtx := rag.WithBillingMessageID(ctx, assistantMsg.ID)
		ragCtx = rag.WithBillingWorkspaceID(ragCtx, conv.WorkspaceID)
		var snippets []rag.Snippet
		var decision rag.RouteDecision
		// topK=8 (was 5): a large uploaded doc's relevant section can sit outside a
		// tight top-5 even when correctly ranked; 8 parent sections stay well within
		// the context budget while improving recall on specific-reference questions.
		var ragErr error
		if hasAttachedKnowledgeBase {
			onEvent(SseEvent{Type: "rag", Status: "searching"})
			iterative, iterativeErr := o.rag.RouteAndRetrieveIterative(
				ragCtx,
				req.UserID,
				conv.ID,
				kbIDs,
				req.UserText,
				nil,
				8,
				rag.IterativeRetrievalOptions{
					ForceRetrieve: ragMode == "inject",
					DocumentIDs:   currentDocumentIDs,
					OnProgress: func(progress rag.IterativeRetrievalProgress) {
						onEvent(SseEvent{Type: "rag", Status: string(progress)})
					},
				},
			)
			snippets, decision, ragErr = iterative.Snippets, iterative.Decision, iterativeErr
			status := iterative.Status
			if ragErr != nil {
				status = rag.IterativeRetrievalError
			}
			sourceCount := len(snippets)
			onEvent(SseEvent{Type: "rag", Status: string(status), SourceCount: &sourceCount})
		} else if ragMode == "inject" {
			snippets, ragErr = o.rag.Retrieve(ragCtx, req.UserID, conv.ID, kbIDs, req.UserText, 8)
			decision = rag.RouteDecision{Strategy: "retrieve"}
		} else {
			snippets, decision, ragErr = o.rag.RouteAndRetrieveDocuments(ragCtx, req.UserID, conv.ID, kbIDs, currentDocumentIDs, req.UserText, nil, 8)
		}
		// Never SILENTLY swallow a retrieval failure (e.g. mixed embedding
		// models/dims, embedder down). We still answer without RAG context — the
		// turn shouldn't hard-fail — but the reason is now logged instead of
		// vanishing into a "_", which previously looked like "RAG just found nothing".
		if ragErr != nil {
			if errors.Is(ragErr, rag.ErrBillingRecord) {
				return nil, ragErr
			}
			o.logger.Printf("rag: retrieval failed for conv %s (kbs=%v): %v — answering without knowledge context", conv.ID, kbIDs, ragErr)
		}
		if !hasAttachedKnowledgeBase && decision.Strategy != "none" {
			onEvent(SseEvent{Type: "rag", Status: decision.Strategy, Summary: fmt.Sprintf("%d sources", len(snippets))})
		}
		for _, s := range snippets {
			c := Citation{ID: s.ID, Index: s.Index, Title: s.Title, URL: s.URL, Snippet: s.Snippet, Source: s.Source}
			ragSnippets = append(ragSnippets, c)
			// Existing conversation-only document RAG keeps its immediate citation
			// events. With an attached KB, defer them until the answer is complete so
			// unused candidates never flash in the UI or survive in message storage.
			if !hasAttachedKnowledgeBase || c.Source == "document" {
				cc := c
				onEvent(SseEvent{Type: "citation", Citation: &cc})
			}
		}
	}
	deferredKBCitationsEmitted := false
	emitDeferredKBCitations := func(blocks []UnifiedBlock) {
		if !hasAttachedKnowledgeBase || deferredKBCitationsEmitted {
			return
		}
		deferredKBCitationsEmitted = true
		for _, citation := range resolvedTurnCitations(ragSnippets, nil, blocks, true) {
			if !isKnowledgeBaseCitation(citation) {
				continue
			}
			cc := citation
			onEvent(SseEvent{Type: "citation", Citation: &cc})
		}
	}

	// 7. Active memories (only ACTIVE + QUERY_DEPENDENT, design.md §4.16) — but
	//    only when the user (and global setting) keep memory enabled. With memory
	//    off, no conversation gets memory injected.
	activeMemories := []store.Memory{}
	// §workspaces privacy: personal memories/persona never leak into SHARED
	// conversations — replies there are visible to every member.
	if conv.WorkspaceID == "" && memoryEnabled {
		activeMemories, _ = store.ListMemoriesActive(ctx, o.db, req.UserID)
	}

	// 7b. Personalization (§ user persona): tone traits + custom instructions +
	//     nickname, read from per-user settings and injected into the system
	//     prompt so the assistant adopts the user's preferred style.
	var persona UserPersona
	if conv.WorkspaceID == "" {
		persona = readUserPersona(ctx, o.db, req.UserID)
	}

	// 8. Skills for this model (§4.17). Native models get the slim index plus
	//    the use_skill tool (progressive disclosure); prompt/none models can't
	//    call a tool, so the full instructions are injected inline.
	//    §4.13-B: a "disable tools" turn uses NO skills at all — neither the
	//    use_skill tool (already dropped with tool_mode=none) nor the inline
	//    full-instruction injection prompt/none models would otherwise get — so
	//    the turn stays a plain, tool-and-skill-free answer as the user asked.
	skillIdx := []SkillIndex{}
	skillFull := []SkillFull{}
	if !req.NoTools {
		skillIdx = availableSkillIdx
		skillFull = availableSkillFull
	}

	// 9. Build the current-turn injected message context. Long-context compaction
	//    is planned after the system prompt and provider tool declarations are also
	//    ready, so its trigger can reuse the complete UnifiedChatRequest estimate
	//    instead of maintaining a smaller, drifting accounting path.
	ragContext := formatRAGContext(ragSnippets, req.Locale)
	// §4.4-B forced non-tool web search (a no-tools turn with web search on):
	// server-run search, results injected as a <web-search-result> block that
	// rides the same message-layer injection as RAG. Citations join the turn's
	// source list. Kept OUT of formatRAGContext so they aren't double-wrapped as
	// KB context.
	if req.NoTools && req.ForceWebSearch && (builtinTools == nil || builtinTools[toolnames.AivoryWebSearch]) {
		// Offset the search citations past any KB snippets already collected this
		// turn so the two source sets don't both start at [1].
		searchCtx := withTaskBillingMessageID(ctx, assistantMsg.ID)
		if searchText, searchCites := o.forcedWebSearch(searchCtx, req, conv, history, len(ragSnippets), builtinTools, onEvent); searchText != "" {
			if ragContext != "" {
				ragContext += "\n\n"
			}
			ragContext += searchText
			ragSnippets = append(ragSnippets, searchCites...)
		}
	}
	// Conversation-uploaded data files staged in the sandbox (§4.5) were resolved
	// before automatic tool routing. The no-Python forced read below uses that same
	// authoritative list, and its injected preview counts toward compaction.
	// §4.5-B spreadsheet injection without Python. Spreadsheets (xlsx/csv/…) are
	// normally read through python_execute, but fast mode deliberately withholds
	// that tool and a no-tools/model-disabled turn may not expose it either. In all
	// of those cases parse the sheet IN-PROCESS (stdlib, rag.SpreadsheetPreview)
	// and inject a bounded <uploaded-data-preview> block on the message layer.
	// Text/code files are already RAG-injected and images go to vision, so only
	// spreadsheets need this fallback.
	if shouldInjectSpreadsheetPreview(sandboxFiles, pythonExecuteAvailable) {
		if sheetText := o.previewSpreadsheetFiles(ctx, req.UserID, conv.ID); sheetText != "" {
			if ragContext != "" {
				ragContext += "\n\n"
			}
			ragContext += sheetText
		}
	}
	// §4.13-B / §2.3-C: only a native-mode request may splice the stored native
	// tool exchange (raw) back into history. A prompt-mode request can still carry
	// provider-hosted tools, but its local Functions use the text protocol and are
	// deliberately absent from the provider declaration. Replaying a prior native
	// Function call in that mixed request would therefore make the upstream reject
	// the history even though the hosted declaration itself is valid.
	// Fast mode cannot safely replay a provider-native exchange: Raw may contain
	// python_execute/code_interpreter calls even though this turn no longer
	// declares either tool. Fall back to canonical blocks for every provider, then
	// remove the prohibited code-tool blocks below.
	nativeToolReplay := shouldReplayNativeToolHistory(fastMode, toolMode, len(toolDefs), useHostedTools)
	// Raw provider exchanges are replayable only for the model that produced
	// them. In particular, OpenAI Responses encrypted reasoning cannot be sent
	// to a different model on the same channel; storeToUnified drops those Raw
	// values while retaining canonical text/tool blocks for the new model.
	// Summary routing follows the model that actually serves this turn. In fast
	// mode conversations.model_id intentionally remains the user's advanced model,
	// so using conv.ModelID here would apply the wrong task fallback chain and
	// request-budget calculation.
	compactionConv := *conv
	compactionConv.ModelID = model.ID
	summaryBlocks := filterBlocksForPath(loadSummaryBlocksForModel(ctx, o.db, conv.SummaryBlocks, model.ID), history)
	frontier := summarizedFrontier(summaryBlocks, history)
	if frontier < 0 || frontier > len(history) {
		frontier = 0
	}
	keep := history[frontier:]
	allowedHistoryTools := unifiedToolNameSet(toolDefs, hostedToolNames, hostedToolRequests)
	uHist := compactionHistoryForRequest(
		keep, channel.Type, model.ID, nativeToolReplay, allowedHistoryTools, fastMode, model.Vision,
	)
	// Private skills are user-authored instructions and therefore belong in the
	// message layer. Apply them to the LAST user entry before any provider-specific
	// history conversion; every OpenAI/Anthropic/Gemini serializer sees the same
	// authority-preserving UnifiedMessage sequence.
	uHist = injectSelectedUserSkillsIntoHistory(uHist, selectedUserSkills)

	// 9b. Inject the summary + RAG context into the MESSAGE layer (§4.8/§4.9),
	//     not the system prompt — keeps the system prefix stable + cacheable.
	uHist = injectSummaryIntoHistory(uHist, ApplySummaryBlocks(summaryBlocks))
	uHist = injectRAGIntoHistory(uHist, ragContext)
	o.injectCompactionMedia(ctx, req.UserID, conv.ID, uHist, summaryBlocks, model.Vision)

	// 9c. Resolve file attachments into provider-ready blocks (§4.6): images
	//     become base64 image blocks on their message (vision models see them
	//     inline). Documents (PDF/DOCX/PPTX/…) never become native provider file
	//     blocks; they are parsed by RAG (local text extraction or MinerU OCR),
	//     chunked/retrieved, and injected as text. Sheets/CSVs are surfaced to
	//     python_execute via the sandbox upload path instead.
	//     §4.6 vision gating: strip legacy image blocks/attachments before any
	//     provider resolution. This changes only the request copy; stored history
	//     remains available if the user later switches back to a vision model.
	o.resolveAttachments(ctx, req.UserID, conv.ID, uHist, model, onEvent)
	if hostedImageEnabled {
		o.resolveImageArtifactBlocks(ctx, req.UserID, uHist)
	}

	// 9d. Conversation-scoped files staged into the sandbox (/workspace/uploads)
	//     were resolved above (`sandboxFiles`, before the forced-read fallback).
	//     Listing them in the system prompt lets the model operate on uploaded
	//     data and verified images from an earlier turn via python_execute.
	//     When python_execute is unavailable that guidance is a dead end and sheet
	//     content is injected as <uploaded-data-preview> instead, so suppress the
	//     listing to avoid pointing the model at a tool it can't call.
	sandboxFilesForPrompt := sandboxFiles
	if !pythonExecuteAvailable {
		sandboxFilesForPrompt = nil
	}

	// Inline-thread context (§ text-selection sub-conversations): the model needs
	// the FULL message the excerpt was lifted from, otherwise a one-line quote
	// like "…draws a diagonal line" is hopelessly ambiguous. Load the source
	// message's text and inject it alongside the highlighted excerpt.
	inlineSource := ""
	if conv.InlineQuote != "" && conv.InlineParentID != "" {
		if pm, perr := store.GetMessage(ctx, o.db, conv.InlineParentID); perr == nil && pm != nil {
			var blocks []UnifiedBlock
			_ = json.Unmarshal(pm.Blocks, &blocks)
			var sb strings.Builder
			for _, b := range blocks {
				if b.Kind == "text" && b.Text != "" {
					sb.WriteString(b.Text)
				}
			}
			inlineSource = sb.String()
			if r := []rune(inlineSource); len(r) > inlineQuoteSourceInjectionCap {
				inlineSource = string(r[:inlineQuoteSourceInjectionCap]) + "…"
			}
		}
	}

	// 10. Compose the six-segment system prompt (§4.8).
	// §fast-mode: the identity segment instructs the model to name itself as
	// ModelLabel — so on a fast turn pass the masked "快速"/"Fast" label, never the
	// real fast model's name (which the model would otherwise disclose in its reply
	// text, a channel redactCost can't mask).
	promptModelLabel := model.Label
	if fastMode {
		promptModelLabel = fastModeLabel(req.Locale)
	}
	systemOpts := systemPromptOpts{
		ModelSystem:         model.SystemPrompt,
		ModelLabel:          promptModelLabel,
		Locale:              req.Locale,
		ToolMode:            toolMode,
		ToolNames:           toolNames,
		ProjectName:         projectName,
		ProjectInstructions: projectInstructions,
		Skills:              skillIdx,
		SkillsFull:          skillFull,
		Memories:            activeMemories,
		ProjectFiles:        projectFiles,
		SandboxFiles:        sandboxFilesForPrompt,
		Persona:             persona,
		InlineQuote:         conv.InlineQuote,
		InlineSource:        inlineSource,
		SkillToolAvailable:  skillToolAvailable,
		SkillsAllowed:       skillsAllowed && !req.NoTools,
		SkillMode: func() string {
			if req.ToolAccessPolicy == nil {
				return ""
			}
			return req.ToolAccessPolicy.SkillMode
		}(),
	}
	system := composeSystemPrompt(systemOpts)

	// 10b. Plan compaction from the same request ingredients that will be sent
	// upstream: system prompt, local/MCP tools, hosted tool fragments, resolved
	// attachments, summaries, RAG, private skills, project context, persona and
	// memories. This closes the first-turn gap for large tools and injected context.
	compactionEstimateReq := UnifiedChatRequest{
		SystemPrompt:         system,
		History:              uHist,
		Tools:                toolDefs,
		OfficialToolRequests: hostedToolRequests,
		ParamOverrides:       req.ParamOverrides,
		ParamControls:        model.ParamControls,
		ExtraParams:          model.ExtraParams,
	}
	requestTokens := estimateRequestTokens(compactionEstimateReq)
	renderedHistoryTokens := estimateRequestTokens(UnifiedChatRequest{History: compactionHistoryForRequest(
		keep, channel.Type, model.ID, nativeToolReplay, allowedHistoryTools, fastMode, model.Vision,
	)}) + summaryTokens(summaryBlocks)
	minimumCut := deepestAutomaticCompactionCut(history, frontier)
	minimumRenderedHistoryTokens := estimateRequestTokens(UnifiedChatRequest{History: compactionHistoryForRequest(
		history[minimumCut:], channel.Type, model.ID, nativeToolReplay, allowedHistoryTools, fastMode, model.Vision,
	)}) + summaryTokens(summaryBlocks)
	minimumRequestTokens := RebasedCompactionRequestTokens(
		requestTokens, renderedHistoryTokens, minimumRenderedHistoryTokens,
	)
	keep, summaryBlocks, compactAction := PlanCompactionForRequest(
		o.db, &compactionConv, history, requestTokens, model.CompactionTokenThreshold, minimumRequestTokens,
	)
	if compactAction == compactInline {
		lease, acquired, leaseErr := o.tryAcquireCompactionLease(ctx, conv.ID)
		if leaseErr != nil {
			// Automatic compaction is maintenance work. Keep the user-visible chat
			// turn available if the database briefly cannot admit its lease; no model
			// call or standalone billing is started in this case.
			if o.logger != nil {
				o.logger.Printf("compaction: skip inline pass after lease error (conv=%s): %v", conv.ID, leaseErr)
			}
		} else if acquired {
			operationID := store.GenID("cmp")
			// Inline compaction runs before the main chat admission below. Keep its
			// provider usage on an independent operation so a later insufficient-credit
			// refusal for the chat turn cannot strand or misattribute the summary cost
			// on the assistant message.
			compactCtx := withStandaloneCompactionBilling(ctx, o, operationID)
			beforeFrontier := summarizedFrontier(summaryBlocks, history)
			var cerr error
			func() {
				defer lease.Release()
				o.notifyAutomaticCompactionStatus(req.UserID, conv.ID, operationID, "started")
				k, b, compactErr := MaybeCompactForRequest(
					compactCtx, o.db, o.task, conv, history, requestTokens, renderedHistoryTokens,
					model.CompactionTokenThreshold, model.ID, req.UserID, userMsg.ID,
				)
				cerr = compactErr
				if cerr == nil {
					keep, summaryBlocks = k, b
				}
				status := "failed"
				if cerr == nil && summarizedFrontier(summaryBlocks, history) > beforeFrontier {
					status = "completed"
				}
				// Publish the terminal state before releasing the lease. A newer pass
				// cannot start and publish its own status until this operation is fully
				// identified as complete or failed.
				o.notifyAutomaticCompactionStatus(req.UserID, conv.ID, operationID, status)
			}()
			if errors.Is(cerr, ErrTaskBillingRecord) {
				return nil, cerr
			}
		}
	} else if compactAction == compactAsync && o.queue != nil && o.task != nil {
		convID, userID, leafID := conv.ID, req.UserID, userMsg.ID
		operationID := store.GenID("cmp")
		modelThreshold, compactionModelID := model.CompactionTokenThreshold, model.ID
		modelChannelID := model.ChannelID
		modelVision := model.Vision
		channelType := channel.Type
		currentModelID := model.ID
		persistedModelID := model.ID
		if fastMode {
			persistedModelID = strings.TrimSpace(conv.ModelID)
		}
		configFingerprint := compactionRuntimeFingerprint(ctx, o.db, currentModelID)
		vision := model.Vision
		historyToolAllowlist := cloneBoolMap(allowedHistoryTools)
		plannedBaseHistory := compactionHistoryForRequest(
			keep, channelType, currentModelID, nativeToolReplay, historyToolAllowlist, fastMode, vision,
		)
		plannedRenderedHistoryTokens := estimateRequestTokens(UnifiedChatRequest{History: plannedBaseHistory}) + summaryTokens(summaryBlocks)
		o.queue.Enqueue("compaction.advance", func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, generationcfg.MaxDuration())
			defer cancel()
			lease, acquired, leaseErr := o.tryAcquireCompactionLease(ctx, convID)
			if leaseErr != nil {
				return fmt.Errorf("acquire context compaction lease: %w", leaseErr)
			}
			if !acquired {
				return nil
			}
			defer lease.Release()
			// The queued job belongs to the path that triggered it, not merely to
			// the conversation. ListMessages deliberately repairs dangling leaves
			// to the newest path for normal rendering; using that fallback here
			// would summarise an unrelated sibling after the original leaf was
			// deleted. Validate and re-plan before issuing any status notification
			// or model call: another job may have advanced the frontier while this one
			// waited, making this pass a legitimate no-op rather than a failure.
			histNow, leafCurrent, leafErr := o.compactionHistoryAtLeaf(ctx, convID, leafID)
			if leafErr != nil {
				return leafErr
			}
			if !leafCurrent {
				return nil
			}
			fresh, gerr := store.GetConversation(ctx, o.db, convID, userID)
			if gerr != nil {
				return gerr
			}
			// Keep the persisted picker state separate from the effective model that
			// shaped this request. A fast turn deliberately does not overwrite
			// conversations.model_id with the hidden fast model. Both the picker state
			// and mode must still match the queued snapshot, and a fast job additionally
			// verifies that the administrator's current fast model is unchanged.
			if strings.TrimSpace(fresh.ModelID) != persistedModelID || fresh.Fast != fastMode {
				return nil
			}
			if fastMode {
				freshFastModel, fastErr := store.GetFastModel(ctx, o.db)
				if fastErr != nil || freshFastModel == nil || freshFastModel.ID != currentModelID {
					return nil
				}
			} else if fresh.ModelID != currentModelID {
				return nil
			}
			freshModel, modelErr := store.GetModel(ctx, o.db, currentModelID)
			if modelErr != nil || freshModel == nil || !freshModel.Enabled || freshModel.ChannelID != modelChannelID || freshModel.Vision != modelVision {
				return nil
			}
			freshChannel, channelErr := store.GetChannel(ctx, o.db, freshModel.ChannelID)
			if channelErr != nil || freshChannel == nil || !freshChannel.Enabled || !strings.EqualFold(strings.TrimSpace(freshChannel.Type), strings.TrimSpace(channelType)) {
				return nil
			}
			if current := compactionRuntimeFingerprint(ctx, o.db, currentModelID); current == "" || current != configFingerprint {
				// Any in-place model/channel/compaction-setting change invalidates the
				// queued request contract. A subsequent turn will plan with the new
				// threshold, prompt, parameters, and provider projection.
				return nil
			}
			onActivePath, activePathErr := o.compactionLeafOnActivePath(ctx, convID, leafID)
			if activePathErr != nil {
				return activePathErr
			}
			if !onActivePath {
				return nil
			}
			// The strict branch snapshot above is rebased at execution time.
			// requestTokens remains the authoritative complete-request snapshot that
			// triggered this pass.
			freshBlocks := filterBlocksForPath(loadSummaryBlocksForModel(ctx, o.db, fresh.SummaryBlocks, currentModelID), histNow)
			freshFrontier := summarizedFrontier(freshBlocks, histNow)
			if freshFrontier < 0 || freshFrontier > len(histNow) {
				freshFrontier = 0
			}
			freshBaseHistory := compactionHistoryForRequest(
				histNow[freshFrontier:], channelType, currentModelID, nativeToolReplay,
				historyToolAllowlist, fastMode, vision,
			)
			freshRenderedHistoryTokens := estimateRequestTokens(UnifiedChatRequest{History: freshBaseHistory}) + summaryTokens(freshBlocks)
			freshRequestTokens := RebasedCompactionRequestTokens(
				requestTokens, plannedRenderedHistoryTokens,
				freshRenderedHistoryTokens,
			)
			freshMinimumCut := deepestAutomaticCompactionCut(histNow, freshFrontier)
			freshMinimumHistory := compactionHistoryForRequest(
				histNow[freshMinimumCut:], channelType, currentModelID, nativeToolReplay,
				historyToolAllowlist, fastMode, vision,
			)
			freshMinimumRenderedHistoryTokens := estimateRequestTokens(UnifiedChatRequest{History: freshMinimumHistory}) + summaryTokens(freshBlocks)
			freshMinimumRequestTokens := RebasedCompactionRequestTokens(
				freshRequestTokens, freshRenderedHistoryTokens, freshMinimumRenderedHistoryTokens,
			)
			freshCompactionConv := *fresh
			freshCompactionConv.ModelID = currentModelID
			_, _, freshAction := PlanCompactionForRequest(
				o.db, &freshCompactionConv, histNow, freshRequestTokens, modelThreshold, freshMinimumRequestTokens,
			)
			if freshAction == compactNone {
				return nil
			}
			o.notifyAutomaticCompactionStatus(userID, convID, operationID, "started")
			ctx = withStandaloneCompactionBilling(ctx, o, operationID)
			completed := false
			defer func() {
				if completed {
					o.notifyAutomaticCompactionStatus(userID, convID, operationID, "completed")
				} else {
					o.notifyAutomaticCompactionStatus(userID, convID, operationID, "failed")
				}
			}()
			_, finalBlocks, cerr := MaybeCompactForRequest(
				ctx, o.db, o.task, fresh, histNow, freshRequestTokens,
				freshRenderedHistoryTokens,
				modelThreshold, compactionModelID, userID, leafID,
			)
			completed = cerr == nil && summarizedFrontier(finalBlocks, histNow) > freshFrontier
			return cerr
		})
	}
	// Rebuild only the history-dependent request copy after compaction. All other
	// fields above are stable and already contributed to requestTokens.
	uHist = compactionHistoryForRequest(
		keep, channel.Type, model.ID, nativeToolReplay, allowedHistoryTools, fastMode, model.Vision,
	)
	uHist = injectSelectedUserSkillsIntoHistory(uHist, selectedUserSkills)
	uHist = injectSummaryIntoHistory(uHist, ApplySummaryBlocks(summaryBlocks))
	uHist = injectRAGIntoHistory(uHist, ragContext)
	o.injectCompactionMedia(ctx, req.UserID, conv.ID, uHist, summaryBlocks, model.Vision)
	o.resolveAttachments(ctx, req.UserID, conv.ID, uHist, model, nil)
	if hostedImageEnabled {
		o.resolveImageArtifactBlocks(ctx, req.UserID, uHist)
	}

	// 11. Title generation (§6.3) — fire-and-forget the first time. An image-only
	// chat gets an immediate attachment-name fallback; after the answer completes,
	// the title task upgrades it from the answer's semantic description.
	upgradeImageOnlyTitle := shouldGenerateTitle(conv) && strings.TrimSpace(req.UserText) == "" && len(imageAttachmentIDs(req.Attachments)) > 0
	if shouldGenerateTitle(conv) {
		if upgradeImageOnlyTitle {
			o.persistGeneratedTitle(context.Background(), conv.ID, req.UserID, imageConversationTitle(req.Attachments, req.Locale))
		} else {
			o.scheduleTitle(conv.ID, req.UserID, req.UserText, req.Locale)
		}
	}

	// §fallback channel: resolve the model's backup channel (if any) so a failed
	// request on the primary channel is retried on it — transparently, before the
	// user sees an error. It must be an enabled channel of the SAME type + format
	// as the primary (only the URL + key differ); anything else is ignored with a
	// warning. fallbackFlag is shared into the provider and flipped the first time
	// ANY request this turn (incl. a tool-loop round) is served by the fallback.
	fallbackCreds, fallbackChannelID := o.resolveFallbackChannel(ctx, model, channel)
	var fallbackFlag *atomic.Bool
	if fallbackCreds != nil {
		fallbackFlag = new(atomic.Bool)
	}

	extraParams := json.RawMessage(nil)
	if model.Kind == "chat" {
		extraParams = model.ExtraParams
	}
	provReq := UnifiedChatRequest{
		UserID:              req.UserID,
		ConversationID:      conv.ID,
		MessageID:           assistantMsg.ID,
		ProjectName:         projectName,
		SystemPrompt:        system,
		SystemPromptOptions: &systemOpts,
		History:             uHist,
		Model: ModelInfo{
			ID:        model.ID,
			RequestID: model.RequestID,
			Provider:  channel.Type,
			Vision:    model.Vision,
			BaseURL:   channel.BaseURL,
			APIKey:    channel.APIKey,
			APIFormat: channel.APIFormat,
			Fallback:  fallbackCreds,
		},
		Tools:                   toolDefs,
		OfficialToolNames:       hostedToolNames,
		OfficialToolRequests:    hostedToolRequests,
		SelectedToolIDs:         append([]string(nil), req.SelectedToolIDs...),
		SelectedToolsConfigured: req.SelectedToolsConfigured,
		ToolAccessPolicy:        req.ToolAccessPolicy,
		ToolsEnabled:            toolsEnabled,
		Fast:                    fastMode,
		ToolModePrompt:          toolMode == "prompt" && len(toolDefs) > 0,
		ProjectFiles:            projectFiles,
		RAGSnippets:             ragSnippets,
		ParamOverrides:          req.ParamOverrides,
		ParamControls:           model.ParamControls,
		ExtraParams:             extraParams,
		Stream:                  model.Stream,
		FallbackUsed:            fallbackFlag,
	}

	// Reserve the model allowance or estimated credits against the fully assembled
	// request. A preliminary gate above keeps obvious refusals cheap; this atomic
	// reservation is authoritative under concurrent turns.
	if model.Kind != "image" {
		estimatedTurnCost := estimateTurnUSD(*model, provReq)
		estimatedTurnTokens := estimateTurnTokens(provReq)
		turnBilling, msg, err = o.reserveUsageBilling(
			ctx, req.UserID, model, store.QuotaScopeModelChat, 1, estimatedTurnCost,
			estimatedTurnTokens, "llm_turn", assistantMsg.ID+":chat",
		)
		if err != nil {
			return nil, err
		}
		if turnBilling == nil {
			refusalBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: msg}})
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks: refusalBlocks, Citations: []byte("[]"), StopReason: "insufficient_credits", Status: "complete",
			})
			onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: msg})
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "insufficient_credits"})
			assistantMsg.Blocks = refusalBlocks
			return &RunResult{UserMessage: userMsg, AssistantMessage: assistantMsg}, nil
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			if releaseErr := o.releaseUsageBilling(releaseCtx, turnBilling); releaseErr != nil && o.logger != nil {
				o.logger.Printf("release chat billing reservation (msg=%s): %v", assistantMsg.ID, releaseErr)
			}
		}()
	}

	// Image model the user pre-selected (§4.12-B), read from user settings.
	imageModelID := ""
	if raw, err := store.GetUserSettingKey(ctx, o.db, req.UserID, "image_model_id"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &imageModelID)
	}

	// Artifacts produced by tools during this turn (sandbox files, images).
	// OnArtifact fires from concurrent tool goroutines (runToolsConcurrent), so
	// the append — and every later read of producedArtifacts — is guarded by
	// artMu to avoid a data race / lost artifacts.
	var artMu sync.Mutex
	producedArtifacts := []ArtifactRef{}
	snapshotArtifacts := func() []ArtifactRef {
		artMu.Lock()
		defer artMu.Unlock()
		return append([]ArtifactRef(nil), producedArtifacts...)
	}
	runner := &orchToolRunner{
		orch:    o,
		onEvent: onEvent,
		ctx: &ToolContext{
			UserID:      req.UserID,
			WorkspaceID: conv.WorkspaceID, ConvID: conv.ID, MessageID: assistantMsg.ID, ModelID: model.ID,
			ProjectID: conv.ProjectID, ProjectName: projectName,
			DB: o.db, ImageModelID: imageModelID,
			WorkspaceAccessCheck: req.WorkspaceAccessCheck,
			ImageInputIDs:        imageAttachmentIDs(req.Attachments),
			ImageUserPrompt:      req.UserText,
			// §4.20: meter chat-driven image_generate against the same credit flow.
			ImageBilling: o,
			DeepResearch: req.Mode == ModeDeepResearch,
			Fast:         fastMode, // §fast-mode: quartered tool budgets + no python_execute
			// Bind execution to the exact definitions declared for this turn. This
			// includes model policy, global disabled_tools, fast mode, official/no-
			// tools state, and prevents an unsolicited provider call from bypassing
			// declaration filtering.
			BuiltinTools:  toolDefNameSet(toolDefs),
			AdminSkillIDs: adminSkillIDSet(req.ToolAccessPolicy),
			citationIndexes: func() *citationIndexAllocator {
				if !hasAttachedKnowledgeBase {
					return nil
				}
				return &citationIndexAllocator{next: maxCitationIndex(ragSnippets)}
			}(),

			OnArtifact: func(a ArtifactRef) {
				artMu.Lock()
				producedArtifacts = append(producedArtifacts, a)
				artMu.Unlock()
				onEvent(SseEvent{Type: "artifact", ID: a.ID, URL: a.URL, Title: a.Filename, Summary: a.MimeType})
			},
		},
	}
	// Provider-hosted calls execute upstream, while Function calls still use the
	// local registry. Bind that local execution path to exactly the Function
	// declarations sent in this unified request.
	providerRunner := ToolRunner(toolDefAllowlistRunner{next: runner, allowed: toolDefNameSet(provReq.Tools)})

	// Non-streaming models (§4.3): suppress incremental text deltas and emit
	// the full answer once after generation. Tool / artifact / rag events still
	// flow live so the user sees progress.
	streamToUser := onEvent
	if !model.Stream {
		streamToUser = func(ev SseEvent) {
			if ev.Type == "text_delta" {
				return
			}
			onEvent(ev)
		}
	}
	providerEvents := streamToUser
	if runner.ctx.citationIndexes != nil {
		providerEvents = func(ev SseEvent) {
			if ev.Type == "citation" && ev.Citation != nil {
				citation := runner.ctx.citationIndexes.normalize(*ev.Citation)
				ev.Citation = &citation
			}
			streamToUser(ev)
		}
	}

	reqRecorder := newProviderRequestRecorder(channel.Type)
	// §B5-per-request rows: retain diagnostics on successful requests only when
	// the admin selected all-request logging. Request bodies have a separate
	// privacy boundary that also applies to failures.
	reqRecorder.captureAll = o.successRequestLoggingEnabled()
	reqRecorder.captureBody = o.requestBodyLoggingEnabled()
	providerCtx := contextWithProviderRequestRecorder(ctx, reqRecorder)
	providerCtx = contextWithProviderVisibleOutput(providerCtx, visibleOutput)
	providerCtx = contextWithProviderTextDeltaVisibility(providerCtx, model.Stream)
	var result *UnifiedResult
	hostedImageCount := 0
	// §4.6-C: set to the fallback model's display name when a TTFT timeout switches
	// models mid-turn, so every usage row for the turn can be marked "timeout
	// fallback" in admin (distinct from the same-model backup-channel `fallback`).
	var ttftFallbackModel string
	if req.Mode == ModeDeepResearch {
		// Deep Research: plan → multi-round web search + source reading → verify
		// → comprehensive cited report. Returns the same UnifiedResult shape, so
		// all finalize/persist/usage/done logic below is path-agnostic.
		result, err = o.runDeepResearch(providerCtx, provReq, runner, provider, providerEvents, conv, assistantMsg)
	} else {
		result, err = o.streamWithFallback(providerCtx, provReq, providerRunner, provider, model.ID, providerEvents, &ttftFallbackModel)
	}
	if result != nil && runner.ctx.citationIndexes != nil {
		for i := range result.Citations {
			result.Citations[i] = runner.ctx.citationIndexes.normalize(result.Citations[i])
		}
	}
	providerCompleted = err == nil && result != nil
	// OpenAI Responses executes image_generation upstream, outside the local tool
	// runner. Materialize those bytes now through the registry's shared artifact
	// writer so the UI, persistence, downloads, and later edits see a normal image
	// artifact. Use a detached context because a final image may arrive alongside a
	// stop/deadline and must not be lost after the provider has already produced it.
	if result != nil && len(result.GeneratedImages) > 0 {
		persistedCount, persistErr := o.persistProviderGeneratedImages(context.WithoutCancel(ctx), runner.ctx, result.GeneratedImages)
		hostedImageCount = persistedCount
		if persistErr != nil {
			if err == nil {
				err = persistErr
			} else {
				err = errors.Join(err, persistErr)
			}
		}
		result.GeneratedImages = nil
	}
	// §fallback channel: which channel actually served this turn, for the usage
	// row. If any request was retried on the fallback, the whole turn is marked
	// fallback and attributed to the fallback channel id. Channel attribution
	// deliberately follows MODEL attribution: the separate TTFT model-fallback
	// (streamWithFallback) already books the whole turn against the PRIMARY model
	// and its pricing even when a different model serves it, so we keep channel_id
	// within the primary model's own channels (primary or its fallback) rather than
	// naming the TTFT-fallback model's channel — pairing model X with model Y's
	// channel would be more misleading than this rare, analytics-only edge.
	usedFallback := fallbackFlag != nil && fallbackFlag.Load()
	servedChannelID := model.ChannelID
	if usedFallback {
		servedChannelID = fallbackChannelID
	}
	providerFailureBase := store.UsageLog{
		UserID: req.UserID, WorkspaceID: conv.WorkspaceID, ConversationID: conv.ID,
		MessageID: assistantMsg.ID, ModelID: model.ID, Purpose: "chat",
		Currency: model.Currency, TTFTFallbackModel: ttftFallbackModel,
	}
	logProviderFailures := func(logCtx context.Context) {
		rows := providerFailureUsageLogs(reqRecorder.snapshots(), providerFailureBase, model.ChannelID, fallbackChannelID)
		for _, row := range rows {
			o.logUsage(logCtx, row)
		}
	}
	if hostedImageCount > 0 {
		persistCtx := context.WithoutCancel(ctx)
		imageCost := float64(hostedImageCount) * model.PricePerImage
		if hostedImageBilling != nil {
			hostedImageBilling.KeepReserved = true
		}
		if hostedDailyImageQuota != nil {
			if _, finalizeErr := store.FinalizeQuotaReservation(persistCtx, o.db, hostedDailyImageQuota.ID, float64(hostedImageCount)); finalizeErr != nil {
				err = errors.Join(err, fmt.Errorf("finalize hosted image daily quota: %w", finalizeErr))
			}
		}
		usageRow := store.UsageLog{
			UserID: req.UserID, WorkspaceID: conv.WorkspaceID, ConversationID: conv.ID,
			MessageID: assistantMsg.ID, ModelID: model.ID, Purpose: "image",
			ImagesCount: hostedImageCount, Cost: imageCost,
			Currency: model.Currency, ChannelID: servedChannelID, Fallback: usedFallback,
		}
		if billingErr := store.RecordBillingUsage(persistCtx, o.db, usageRow); billingErr != nil {
			err = errors.Join(err, fmt.Errorf("record hosted image billing: %w", billingErr))
		} else if hostedImageBilling != nil {
			_, total, settleErr := o.SettleImageBilling(persistCtx, &ImageBillingReservation{admission: hostedImageBilling}, hostedImageCount, imageCost)
			if settleErr != nil {
				err = errors.Join(err, fmt.Errorf("settle hosted image billing: %w", settleErr))
			} else {
				usageRow.Credits = total
				runner.ctx.AddImageCredits(total)
			}
		}
		if analyticsErr := store.LogUsageAnalytics(persistCtx, o.db, usageRow); analyticsErr != nil {
			err = errors.Join(err, fmt.Errorf("record hosted image analytics: %w", analyticsErr))
		}
	}
	if err != nil {
		// §6.2 stop-button semantics: when the user (or the kill switch) cancels
		// the context, the provider returns ctx.Err() — preserve whatever the
		// provider streamed so far (artifacts + text + tool rounds it managed to
		// finish before cancel) rather than blanking the message.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The turn context is dead, so DB writes on it would be rejected and
			// the partial reply would be LOST. Persist on a detached context so a
			// stop / kill / timeout still saves what the model produced.
			ctx := context.WithoutCancel(ctx)
			partialBlocks := []UnifiedBlock{}
			if result != nil {
				partialBlocks = append(partialBlocks, result.Blocks...)
			}
			for _, a := range snapshotArtifacts() {
				partialBlocks = append(partialBlocks, UnifiedBlock{
					Kind: "artifact", FileRef: a.ID, Title: a.Filename, URL: a.URL,
					Summary:   a.MimeType, // §4.12 reload: keep mime alongside title
					Artifacts: []ArtifactRef{a},
				})
			}
			partialJSON, _ := json.Marshal(partialBlocks)
			citesJSON := []byte("[]")
			if result != nil {
				allCites := resolvedTurnCitations(ragSnippets, result.Citations, partialBlocks, hasAttachedKnowledgeBase)
				citesJSON, _ = json.Marshal(allCites)
				emitDeferredKBCitations(partialBlocks)
			}
			usage := Usage{}
			var partialRaw json.RawMessage
			if result != nil {
				usage = result.Usage
				partialRaw = nativeRawForPersistedModel(result.Raw, ttftFallbackModel)
			}
			usage, produced := stoppedTurnUsage(usage, provReq, partialBlocks, visibleOutput.Load(), reqRecorder.snapshots())
			// §发出就算: a user-stopped turn is finalized like a completed one — the
			// partial output is billed, burns the window quota, and (past the free
			// allotment) is charged in credits for what was produced. Providers that
			// lose their terminal usage frame on cancel use a conservative estimate;
			// true provider failures and pre-send refusals stay free.
			stopChatCost := computeCost(*model, usage)
			var stopCredits float64
			stopTurnTotal := stopChatCost
			persistStoppedBillingFailure := func(cost float64, cause error) error {
				const safeErr = "Billing settlement failed. Your partial result was saved, but this turn requires administrator review."
				_ = finishMessage(ctx, store.MessageFinishPatch{
					Blocks: partialJSON, Raw: partialRaw, Citations: citesJSON, StopReason: "stopped", Status: "error", Error: safeErr,
					InputTokens: usage.InputTokens, ContextTokens: reqRecorder.maxContextTokens(), OutputTokens: usage.OutputTokens,
					CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens,
					Cost: cost, GenMs: time.Since(turnStart).Milliseconds(),
				})
				onEvent(SseEvent{Type: "error", Message: safeErr})
				return cause
			}
			if produced {
				if turnBilling != nil {
					turnBilling.KeepReserved = true
				}
				sideCosts, billingErr := store.TurnSideBillingCosts(ctx, o.db, assistantMsg.ID)
				if billingErr != nil {
					return nil, persistStoppedBillingFailure(stopTurnTotal, fmt.Errorf("read stopped-turn billing costs: %w", billingErr))
				}
				stopTurnTotal += sideCosts.Total
				stopCreditBase := stopTurnTotal - sideCosts.Image
				if stopCreditBase < 0 {
					stopCreditBase = 0
				}
				for _, rr := range perRequestUsageRows(reqRecorder.snapshots(), model, usage, stopChatCost, 0, o.successRequestLoggingEnabled()) {
					if billingErr := store.RecordBillingUsage(ctx, o.db, store.UsageLog{
						UserID: req.UserID, WorkspaceID: conv.WorkspaceID, ConversationID: conv.ID,
						MessageID: assistantMsg.ID, ModelID: model.ID, Purpose: "chat",
						InputTokens: rr.Usage.InputTokens, OutputTokens: rr.Usage.OutputTokens,
						CacheReadTokens: rr.Usage.CacheReadTokens, CacheWriteTokens: rr.Usage.CacheWriteTokens,
						Cost: rr.Cost, Currency: model.Currency,
					}); billingErr != nil {
						return nil, persistStoppedBillingFailure(stopTurnTotal, fmt.Errorf("record stopped-turn billing: %w", billingErr))
					}
				}
				settleCtx, settleCancel := context.WithTimeout(ctx, 15*time.Second)
				debit, settleErr := o.settleUsageBilling(
					settleCtx, turnBilling, 1, stopCreditBase, usage.InputTokens+usage.OutputTokens,
				)
				settleCancel()
				if settleErr != nil {
					return nil, persistStoppedBillingFailure(stopTurnTotal, fmt.Errorf("settle stopped-turn billing: %w", settleErr))
				}
				stopCredits = debit.Total
			}
			// Image credits (a tool that drew before the stop) are already debited by
			// ImageBilling; fold them into the per-turn total the user sees.
			turnCredits := stopCredits + runner.ctx.ImageCreditsTotal()
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks:           partialJSON,
				Raw:              partialRaw,
				Citations:        citesJSON,
				StopReason:       "stopped",
				InputTokens:      usage.InputTokens,
				ContextTokens:    reqRecorder.maxContextTokens(),
				OutputTokens:     usage.OutputTokens,
				CacheReadTokens:  usage.CacheReadTokens,
				CacheWriteTokens: usage.CacheWriteTokens,
				Cost:             stopTurnTotal,
				Credits:          turnCredits,
				Status:           "stopped",
				GenMs:            time.Since(turnStart).Milliseconds(),
			})
			// Bill + count what the model produced before the stop. The durable usage
			// fact and the window-quota increment preserve reporting and admission
			// state even when diagnostic logs are later pruned (§B3).
			logProviderFailures(ctx)
			if produced {
				// §B5-per-request rows: same split as the success path — tool
				// rounds completed before the stop each keep their own row.
				for _, rr := range perRequestUsageRows(reqRecorder.snapshots(), model, usage, stopChatCost, stopCredits, o.successRequestLoggingEnabled()) {
					requestChannelID, requestFallback := requestUsageChannel(rr, model.ChannelID, fallbackChannelID, usedFallback)
					if analyticsErr := store.LogUsageAnalytics(ctx, o.db, store.UsageLog{
						UserID:            req.UserID,
						WorkspaceID:       conv.WorkspaceID,
						ConversationID:    conv.ID,
						MessageID:         assistantMsg.ID,
						ModelID:           model.ID,
						Purpose:           "chat",
						InputTokens:       rr.Usage.InputTokens,
						OutputTokens:      rr.Usage.OutputTokens,
						CacheReadTokens:   rr.Usage.CacheReadTokens,
						CacheWriteTokens:  rr.Usage.CacheWriteTokens,
						Cost:              rr.Cost,
						Currency:          model.Currency,
						Credits:           rr.Credits,
						ChannelID:         requestChannelID,
						Fallback:          requestFallback,
						TTFTFallbackModel: ttftFallbackModel,
						RequestMethod:     rr.Method,
						RequestURL:        rr.URL,
						RequestHeaders:    rr.Header,
						RequestBody:       rr.Body,
					}); analyticsErr != nil && o.logger != nil {
						o.logger.Printf("usage analytics write failed (msg=%s purpose=chat): %v", assistantMsg.ID, analyticsErr)
					}
				}
			}
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "stopped", Usage: &usage, Credits: turnCredits})
			finalAssistant, _ := store.GetMessage(ctx, o.db, assistantMsg.ID)
			return &RunResult{UserMessage: userMsg, AssistantMessage: finalAssistant}, nil
		}
		// Upstream content-filter block (e.g. a relay returning
		// `sensitive_words_detected`): classify it as content moderation — a
		// rephrase-and-retry refusal shown to the user — rather than a generic,
		// transient "provider returned an error". The raw error is still recorded
		// as an error usage row below for admin diagnostics, exactly like any
		// other upstream failure, so the reason stays visible on /admin/usage.
		if isUpstreamModerationError(err) {
			msg := o.moderationMessage()
			refusalBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: msg}})
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks: refusalBlocks, Citations: []byte("[]"), StopReason: "content_moderation", Status: "complete",
			})
			if o.logger != nil {
				o.logger.Printf("orchestrator: upstream content-filter block classified as moderation (conv=%s msg=%s model=%s): %v",
					conv.ID, assistantMsg.ID, model.ID, err)
			}
			logProviderFailures(ctx)
			if !providerFailureCaptured(reqRecorder.snapshots(), err) {
				reqSnapshot := reqRecorder.snapshot()
				o.logUsage(ctx, store.UsageLog{
					UserID: req.UserID, WorkspaceID: conv.WorkspaceID, ConversationID: conv.ID,
					MessageID: assistantMsg.ID, ModelID: model.ID, Purpose: "chat", Currency: model.Currency,
					ChannelID: servedChannelID, Fallback: usedFallback, TTFTFallbackModel: ttftFallbackModel, Status: "error",
					Error:         truncErr(err.Error()),
					RequestMethod: reqSnapshot.Method, RequestURL: reqSnapshot.URL,
					RequestHeaders: reqSnapshot.Header, RequestBody: reqSnapshot.Body,
				})
			}
			onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: msg})
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "content_moderation"})
			assistantMsg.Blocks = refusalBlocks
			finalAssistant, _ := store.GetMessage(ctx, o.db, assistantMsg.ID)
			if finalAssistant == nil {
				finalAssistant = assistantMsg
			}
			return &RunResult{UserMessage: userMsg, AssistantMessage: finalAssistant}, nil
		}
		// Preserve any artifacts already produced this turn (e.g. a saved .pptx)
		// so a late provider error doesn't blank the message the user was
		// watching — they still get the downloadable file.
		errBlocks := []UnifiedBlock{}
		if result != nil {
			errBlocks = append(errBlocks, result.Blocks...)
		}
		for _, a := range snapshotArtifacts() {
			errBlocks = append(errBlocks, UnifiedBlock{
				Kind: "artifact", FileRef: a.ID, Title: a.Filename, URL: a.URL,
				Summary: a.MimeType, Artifacts: []ArtifactRef{a},
			})
		}
		errBlocksJSON, _ := json.Marshal(errBlocks)
		errUsage := Usage{}
		var errRaw json.RawMessage
		var errResultCitations []Citation
		if result != nil {
			errUsage = result.Usage
			errRaw = nativeRawForPersistedModel(result.Raw, ttftFallbackModel)
			errResultCitations = result.Citations
		}
		errCites := resolvedTurnCitations(ragSnippets, errResultCitations, errBlocks, hasAttachedKnowledgeBase)
		errCitesJSON, _ := json.Marshal(errCites)
		emitDeferredKBCitations(errBlocks)
		// §B5: the raw error may embed upstream response bodies (org/request ids,
		// echoed prompt fragments). Log that error server-side and expose only a
		// generic message; result.Raw contains the partial model exchange, not the
		// provider error body, and remains useful for same-provider history replay.
		if o.logger != nil {
			o.logger.Printf("orchestrator: generation error (conv=%s msg=%s model=%s provider=%s format=%s media=%s): %v",
				conv.ID, assistantMsg.ID, model.ID, channel.Type, channel.APIFormat, providerRequestMediaStats(provReq), err)
		}
		const safeErr = "The model provider returned an error. Please try again in a moment."
		_ = finishMessage(ctx, store.MessageFinishPatch{
			Blocks:           errBlocksJSON,
			Raw:              errRaw,
			Citations:        errCitesJSON,
			StopReason:       "generation_interrupted",
			InputTokens:      errUsage.InputTokens,
			ContextTokens:    reqRecorder.maxContextTokens(),
			OutputTokens:     errUsage.OutputTokens,
			CacheReadTokens:  errUsage.CacheReadTokens,
			CacheWriteTokens: errUsage.CacheWriteTokens,
			Status:           "error",
			Error:            safeErr,
			GenMs:            time.Since(turnStart).Milliseconds(),
		})
		// §usage errors: record the failed request so admin/usage counts it and
		// shows which channel served it (and whether the fallback was used). The
		// persisted message keeps any parsed partial usage for diagnostics, while
		// failed usage rows remain zero-cost and are excluded from quota reseeds
		// (store.UsageInWindow skips status='error').
		logProviderFailures(ctx)
		if !providerFailureCaptured(reqRecorder.snapshots(), err) {
			reqSnapshot := reqRecorder.snapshot()
			o.logUsage(ctx, store.UsageLog{
				UserID:            req.UserID,
				WorkspaceID:       conv.WorkspaceID,
				ConversationID:    conv.ID,
				MessageID:         assistantMsg.ID,
				ModelID:           model.ID,
				Purpose:           "chat",
				Currency:          model.Currency,
				ChannelID:         servedChannelID,
				Fallback:          usedFallback,
				TTFTFallbackModel: ttftFallbackModel,
				Status:            "error",
				// Store the raw upstream failure (status + response body) so an admin can
				// diagnose it on /admin/usage. It's the same detail we log server-side and
				// deliberately withhold from the user (§B5); it's admin-only on the wire.
				Error:          truncErr(err.Error()),
				RequestMethod:  reqSnapshot.Method,
				RequestURL:     reqSnapshot.URL,
				RequestHeaders: reqSnapshot.Header,
				RequestBody:    reqSnapshot.Body,
			})
		}
		onEvent(SseEvent{Type: "error", MessageID: assistantMsg.ID, Message: safeErr, Code: "generation_interrupted"})
		return nil, err
	}

	// 12. Finalise. Append any artifact blocks so they persist on reload.
	for _, a := range snapshotArtifacts() {
		result.Blocks = append(result.Blocks, UnifiedBlock{
			Kind: "artifact", FileRef: a.ID, Title: a.Filename, URL: a.URL,
			Summary:   a.MimeType, // §4.12 reload fidelity: keep mime
			Artifacts: []ArtifactRef{a},
		})
	}
	// Persist the inject-path RAG sources alongside tool citations so reloads
	// render the same source list the user saw live (§4.11-B).
	allCites := resolvedTurnCitations(ragSnippets, result.Citations, result.Blocks, hasAttachedKnowledgeBase)
	blocksJSON, _ := json.Marshal(result.Blocks)
	citesJSON, _ := json.Marshal(allCites)
	emitDeferredKBCitations(result.Blocks)
	// §2.3-C storage: `raw` (the provider-native exchange) only needs to persist
	// for turns that used TOOLS. Its sole reader is same-vendor replay, where it
	// preserves what `blocks` drops: tool-call IDs and tool-scoped thinking/
	// thought signatures (Gemini 400s without thought_signature on functionCall
	// parts; Anthropic needs the thinking signature INSIDE a tool loop). A pure
	// text/thinking answer carries none of that — those requirements are all
	// scoped to tool/function-call parts — and the block→text fallback in
	// historyTo*() reconstructs such a turn identically. So for tool-free turns we
	// drop raw, avoiding a second near-duplicate copy of the answer in the DB.
	// Conservative gate: keep raw if ANY block is not plain text/thinking
	// (tool_call / artifact / research / image / …), so no tool turn ever loses it.
	rawToStore := nativeRawForPersistedModel(result.Raw, ttftFallbackModel)
	turnUsedTools := false
	for _, b := range result.Blocks {
		if b.Kind != "text" && b.Kind != "thinking" {
			turnUsedTools = true
			break
		}
	}
	if !turnUsedTools {
		rawToStore = nil
	}
	chatCost := computeCost(*model, result.Usage)
	if turnBilling != nil {
		turnBilling.KeepReserved = true
	}
	// §verify: when Verify mode is on, a secondary auditor model fact-checks A's
	// finished answer. Runs HERE — after the answer is final but BEFORE
	// tallyTurnSideCosts — so its usage row (purpose='verify', pinned to this
	// message) folds into the turn cost + credit charge below. Fail-open.
	if req.Verify {
		if verifyErr := o.runVerify(ctx, conv, req.UserID, assistantMsg.ID, req.UserText, result, onEvent); verifyErr != nil {
			const safeErr = "Billing settlement failed. Your result was saved, but this turn requires administrator review."
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks: blocksJSON, Raw: rawToStore, Citations: citesJSON, Status: "error", Error: safeErr,
				Cost: chatCost, GenMs: time.Since(turnStart).Milliseconds(),
			})
			onEvent(SseEvent{Type: "error", Message: safeErr})
			return nil, verifyErr
		}
	}
	billingCtx, billingCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer billingCancel()
	// §8 cost rule: messages.cost is the FULL spend the user incurred for this
	// turn — chat + any image_generate calls + any embedding queries inside
	// the loop. The image/embedding rows are still logged separately so
	// admin/usage breakdowns work.
	sideCosts, billingErr := store.TurnSideBillingCosts(billingCtx, o.db, assistantMsg.ID)
	if billingErr != nil {
		const safeErr = "Billing settlement failed. Your result was saved, but this turn requires administrator review."
		_ = finishMessage(ctx, store.MessageFinishPatch{
			Blocks: blocksJSON, Raw: rawToStore, Citations: citesJSON, Status: "error", Error: safeErr,
			Cost: chatCost, GenMs: time.Since(turnStart).Milliseconds(),
		})
		onEvent(SseEvent{Type: "error", Message: safeErr})
		return nil, fmt.Errorf("read turn billing costs: %w", billingErr)
	}
	turnTotal := chatCost + sideCosts.Total
	// §4.20: image_generate already metered its own cost against the image model's
	// quota and charged any image credits via ImageBilling (free→credits→block),
	// so EXCLUDE the image cost from the chat credit base to avoid double-charging.
	// Image cost still counts in messages.cost (full spend) above.
	chatCreditBase := turnTotal - sideCosts.Image
	if chatCreditBase < 0 {
		chatCreditBase = 0
	}
	requestBillingRows := perRequestUsageRows(reqRecorder.snapshots(), model, result.Usage, chatCost, 0, o.successRequestLoggingEnabled())
	for _, rr := range requestBillingRows {
		if billingErr := store.RecordBillingUsage(billingCtx, o.db, store.UsageLog{
			UserID: req.UserID, WorkspaceID: conv.WorkspaceID, ConversationID: conv.ID,
			MessageID: assistantMsg.ID, ModelID: model.ID, Purpose: "chat",
			InputTokens: rr.Usage.InputTokens, OutputTokens: rr.Usage.OutputTokens,
			CacheReadTokens: rr.Usage.CacheReadTokens, CacheWriteTokens: rr.Usage.CacheWriteTokens,
			Cost: rr.Cost, Currency: model.Currency,
		}); billingErr != nil {
			const safeErr = "Billing settlement failed. Your result was saved, but this turn requires administrator review."
			_ = finishMessage(ctx, store.MessageFinishPatch{
				Blocks: blocksJSON, Raw: rawToStore, Citations: citesJSON, Status: "error", Error: safeErr,
				Cost: turnTotal, GenMs: time.Since(turnStart).Milliseconds(),
			})
			onEvent(SseEvent{Type: "error", Message: safeErr})
			return nil, fmt.Errorf("record chat billing: %w", billingErr)
		}
	}
	// §credits: when this turn is past the group's free allotment, convert the
	// chat spend to credits and debit timed-first-then-permanent. Usage rows store
	// the total charge so every credit-paid turn is excluded from free quota.
	var chatCredits float64
	chatDebit, settleErr := o.settleUsageBilling(
		billingCtx, turnBilling, 1, chatCreditBase, result.Usage.InputTokens+result.Usage.OutputTokens,
	)
	if settleErr != nil {
		const safeErr = "Billing settlement failed. Your result was saved, but this turn requires administrator review."
		_ = finishMessage(ctx, store.MessageFinishPatch{
			Blocks: blocksJSON, Raw: rawToStore, Citations: citesJSON, Status: "error", Error: safeErr,
			Cost: turnTotal, GenMs: time.Since(turnStart).Milliseconds(),
		})
		onEvent(SseEvent{Type: "error", Message: safeErr})
		return nil, fmt.Errorf("settle chat billing: %w", settleErr)
	}
	chatCredits = chatDebit.Total
	// Total credits the user sees for this turn = chat credits + image credits the
	// tool charged (ImageBilling), so a chat turn that drew an image shows both.
	turnCredits := chatCredits + runner.ctx.ImageCreditsTotal()
	_ = finishMessage(ctx, store.MessageFinishPatch{
		Blocks:           blocksJSON,
		Raw:              rawToStore,
		Citations:        citesJSON,
		StopReason:       result.StopReason,
		InputTokens:      result.Usage.InputTokens,
		ContextTokens:    reqRecorder.maxContextTokens(),
		OutputTokens:     result.Usage.OutputTokens,
		CacheReadTokens:  result.Usage.CacheReadTokens,
		CacheWriteTokens: result.Usage.CacheWriteTokens,
		Cost:             turnTotal,
		Credits:          turnCredits,
		Status:           "complete",
		GenMs:            time.Since(turnStart).Milliseconds(),
	})
	// §B5-per-request usage rows: a turn that hit the upstream N times (tool
	// loop rounds, prompt-protocol rounds, research calls) books N rows — each
	// with its own tokens/cost and (when full request logging is on) its own
	// request snapshot. Row sums equal the old single-row totals exactly, and
	// they share message_id, so per-turn groupings and quota reseeds hold.
	logProviderFailures(billingCtx)
	for _, rr := range perRequestUsageRows(reqRecorder.snapshots(), model, result.Usage, chatCost, chatCredits, o.successRequestLoggingEnabled()) {
		requestChannelID, requestFallback := requestUsageChannel(rr, model.ChannelID, fallbackChannelID, usedFallback)
		if analyticsErr := store.LogUsageAnalytics(billingCtx, o.db, store.UsageLog{
			UserID:            req.UserID,
			WorkspaceID:       conv.WorkspaceID,
			ConversationID:    conv.ID,
			MessageID:         assistantMsg.ID,
			ModelID:           model.ID,
			Purpose:           "chat",
			InputTokens:       rr.Usage.InputTokens,
			OutputTokens:      rr.Usage.OutputTokens,
			CacheReadTokens:   rr.Usage.CacheReadTokens,
			CacheWriteTokens:  rr.Usage.CacheWriteTokens,
			Cost:              rr.Cost,
			Currency:          model.Currency,
			Credits:           rr.Credits,
			ChannelID:         requestChannelID,
			Fallback:          requestFallback,
			TTFTFallbackModel: ttftFallbackModel,
			RequestMethod:     rr.Method,
			RequestURL:        rr.URL,
			RequestHeaders:    rr.Header,
			RequestBody:       rr.Body,
		}); analyticsErr != nil && o.logger != nil {
			o.logger.Printf("usage analytics write failed (msg=%s purpose=chat): %v", assistantMsg.ID, analyticsErr)
		}
	}
	// Update the fixed-window FREE quota counter for this user+model (§ user
	// groups). Credit-paid turns are skipped inside — they must not burn the
	// remaining free allowance.

	// Non-streaming models: now that generation is complete, emit the full
	// answer as a single text delta.
	if !model.Stream {
		final := ""
		for _, b := range result.Blocks {
			if b.Kind == "text" {
				final += b.Text
			}
		}
		if final != "" {
			onEvent(SseEvent{Type: "text_delta", MessageID: assistantMsg.ID, Text: final})
		}
	}

	// Surface a content-filter / refusal stop reason explicitly (§6.2) so the
	// UI can render it distinctly rather than as an empty message.
	if result.StopReason == "content_filter" || result.StopReason == "refusal" || result.StopReason == "safety" {
		onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: "The model declined to answer (content filtered)."})
	}

	finalAssistant, _ := store.GetMessage(billingCtx, o.db, assistantMsg.ID)
	usage := result.Usage
	onEvent(SseEvent{
		Type: "done", MessageID: assistantMsg.ID,
		StopReason: result.StopReason, Usage: &usage,
		Credits: turnCredits,
	})
	if upgradeImageOnlyTitle {
		if source := imageOnlyTitleSource(renderBlocksAsText(result.Blocks)); source != "" {
			o.scheduleTitleUpgrade(conv.ID, req.UserID, source, req.Locale)
		}
	}

	// 13. Async memory extraction (§4.16) — runs after the user has the reply.
	if o.memory != nil && o.queue != nil && conv.WorkspaceID == "" && memoryEnabled {
		convID := conv.ID
		o.queue.Enqueue("memory.process", func(ctx context.Context) error {
			return o.memory.Process(ctx, convID)
		})
	}

	return &RunResult{UserMessage: userMsg, AssistantMessage: finalAssistant}, nil
}

func conversationKnowledgeBaseSelection(conv *store.Conversation, req RunRequest) []string {
	if req.KnowledgeBaseSelectionConfigured {
		return append([]string(nil), req.KnowledgeBaseIDs...)
	}
	if conv == nil || len(conv.KBIDs) == 0 {
		return nil
	}
	var selected []string
	if json.Unmarshal(conv.KBIDs, &selected) != nil {
		return nil
	}
	return selected
}

func resolveConversationKnowledgeBaseSelection(
	ctx context.Context,
	db *sql.DB,
	conv *store.Conversation,
	req RunRequest,
) ([]string, error) {
	selected := conversationKnowledgeBaseSelection(conv, req)
	if len(selected) == 0 {
		return selected, nil
	}
	if req.KnowledgeBaseSelectionConfigured {
		return store.ResolveOwnedKBIDs(ctx, db, req.UserID, conv.WorkspaceID, selected)
	}
	return store.OwnedKBIDs(ctx, db, req.UserID, conv.WorkspaceID, selected), nil
}

func imageAttachmentIDs(attachments []Attachment) []string {
	ids := make([]string, 0, len(attachments))
	seen := make(map[string]bool, len(attachments))
	for _, attachment := range attachments {
		id := strings.TrimSpace(attachment.ID)
		if id == "" || seen[id] || (attachment.Kind != "image" && !strings.HasPrefix(attachment.MimeType, "image/")) {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func attachmentDocumentIDs(attachments []Attachment) []string {
	ids := make([]string, 0, len(attachments))
	seen := make(map[string]bool, len(attachments))
	for _, attachment := range attachments {
		id := strings.TrimSpace(attachment.DocumentID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// runImageTurn handles a §4.20 image-mode turn: compose the final prompt (style
// hidden prompt + optional text-model optimization), force-call image_generate
// (the tool owns the Gemini/OpenAI generation/edit protocols, branch continuation,
// quota and image usage logging), and persist its artifacts as the assistant
// message. The "image_status" events drive the studio's dedicated generating UI.
func (o *Orchestrator) runImageTurn(
	ctx context.Context,
	conv *store.Conversation,
	model *store.Model,
	userMsg, assistantMsg *store.Message,
	req RunRequest,
	imageRequestParams map[string]any,
	imageGenerationCount int,
	turnStart time.Time,
	billing *billingAdmission,
	onEvent func(SseEvent),
) (*RunResult, error) {
	var generationAccessRevoked atomic.Bool
	emitEvent := onEvent
	onEvent = func(event SseEvent) {
		if generationAccessRevoked.Load() {
			return
		}
		emitEvent(event)
	}
	optimizePrompt := req.OptimizeImagePrompt == nil || *req.OptimizeImagePrompt
	if optimizePrompt {
		onEvent(SseEvent{Type: "image_status", MessageID: assistantMsg.ID, Status: "optimizing"})
	}

	// Style: the composer sends image_style_id on a fresh turn. Regenerate doesn't
	// resend it, so fall back to the last style remembered on the conversation
	// (provider_state) and re-persist it — so a re-draw keeps
	// the original look instead of silently dropping the style.
	styleID := req.ImageStyleID
	if styleID == "" {
		styleID, _ = store.GetConvProviderStateKey(ctx, o.db, conv.ID, "image_style")
	}
	styleHidden := ""
	if styleID != "" {
		if st, err := store.GetImageStyle(ctx, o.db, styleID); err == nil && st.Enabled {
			styleHidden = strings.TrimSpace(st.HiddenPrompt)
			_ = store.SetConvProviderStateKeyForUser(ctx, o.db, conv.ID, assistantMsg.ID, req.UserID, "image_style", styleID)
		}
	}
	finalPrompt := composeImagePrompt(req.UserText, styleHidden)
	if optimizePrompt {
		var optimizeErr error
		finalPrompt, optimizeErr = o.optimizeImagePrompt(ctx, req.UserID, conv.ID, assistantMsg.ID, req.UserText, styleHidden)
		if optimizeErr != nil {
			return nil, optimizeErr
		}
	}

	// Reference images: the user's image attachments become input images (edit /
	// image-to-image). loadInputImages resolves file ids too (§4.20).
	inputImageIDs := imageAttachmentIDs(req.Attachments)

	onEvent(SseEvent{Type: "image_status", MessageID: assistantMsg.ID, Status: "generating"})

	// Force-call image_generate. tc.ImageModelID = the conversation's image model
	// so resolveImageModel uses exactly it.
	imageGenerationCount = ClampImageGenerationCount(imageGenerationCount)
	toolPayload := map[string]any{
		"prompt":       finalPrompt,
		"n":            imageGenerationCount,
		"input_images": inputImageIDs,
	}
	if configuredSize, ok := imageRequestParams["size"].(string); ok && strings.TrimSpace(configuredSize) != "" {
		toolPayload["size"] = strings.TrimSpace(configuredSize)
	}
	toolInput, _ := json.Marshal(toolPayload)
	var mu sync.Mutex
	artifacts := []ArtifactRef{}
	tc := &ToolContext{
		UserID:             req.UserID,
		WorkspaceID:        conv.WorkspaceID,
		ConvID:             conv.ID,
		MessageID:          assistantMsg.ID,
		ModelID:            model.ID,
		ImageModelID:       model.ID,
		ImageRequestParams: imageRequestParams,
		ImageInputIDs:      inputImageIDs,
		ImageUserPrompt:    req.UserText,
		DB:                 o.db,
		// The orchestrator already ran the credit-aware checkImageQuota above, so
		// the tool must not also hard-cap this turn (§4.20).
		SkipImageQuota: true,
		OnArtifact: func(a ArtifactRef) {
			mu.Lock()
			artifacts = append(artifacts, a)
			mu.Unlock()
			onEvent(SseEvent{Type: "artifact", ID: a.ID, URL: a.URL, Title: a.Filename, Summary: a.MimeType})
		},
		counts: map[string]int{},
	}
	output, _, err := o.tools.Run(ctx, "image_generate", toolInput, tc)

	// Persist on a DETACHED context: a stop / kill / maxGenDuration cancels `ctx`
	// mid-generation, and FinishMessage on a cancelled ctx is a no-op — which would
	// strand the assistant message in Status="streaming" (the ImageGenerating tile
	// spins forever). Mirror the chat path's context.WithoutCancel guard.
	persistCtx := context.WithoutCancel(ctx)
	finishMessage := func(p store.MessageFinishPatch) error {
		err := store.FinishMessageForUser(persistCtx, o.db, assistantMsg.ID, conv.ID, req.UserID, p)
		if errors.Is(err, store.ErrConversationAccessRevoked) {
			generationAccessRevoked.Store(true)
		}
		if err == nil {
			msgcache.Bump(o.cache, conv.ID)
		}
		return err
	}

	// Snapshot produced artifacts (non-empty even on a mid-stream stop).
	mu.Lock()
	artBlocks := make([]UnifiedBlock, 0, len(artifacts))
	for _, a := range artifacts {
		artBlocks = append(artBlocks, UnifiedBlock{
			Kind: "artifact", FileRef: a.ID, Title: a.Filename, URL: a.URL,
			Summary: a.MimeType, Artifacts: []ArtifactRef{a},
		})
	}
	mu.Unlock()

	if err != nil && len(artBlocks) == 0 {
		var refusal *ToolRefusalError
		switch {
		case errors.As(err, &refusal):
			// Policy / quota / moderation refusal — show the real message, not a
			// generic "try again" (mirrors the chat refusal path).
			rb, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: refusal.Message}})
			_ = finishMessage(store.MessageFinishPatch{
				Blocks: rb, Citations: []byte("[]"), StopReason: "refusal", Status: "complete",
				GenMs: time.Since(turnStart).Milliseconds(),
			})
			onEvent(SseEvent{Type: "refusal", MessageID: assistantMsg.ID, Message: refusal.Message})
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "refusal"})
			fin, _ := store.GetMessage(persistCtx, o.db, assistantMsg.ID)
			return &RunResult{UserMessage: userMsg, AssistantMessage: fin}, nil
		case ctx.Err() != nil:
			// The PARENT turn ctx is cancelled → user stop or max-duration timeout.
			// Finalize cleanly (no error banner). A per-model image timeout cancels
			// only the CHILD ctx (ctx.Err()==nil) and falls through to the error case.
			empty, _ := json.Marshal([]UnifiedBlock{})
			_ = finishMessage(store.MessageFinishPatch{
				Blocks: empty, Citations: []byte("[]"), StopReason: "stopped", Status: "complete",
				GenMs: time.Since(turnStart).Milliseconds(),
			})
			onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: "stopped"})
			fin, _ := store.GetMessage(persistCtx, o.db, assistantMsg.ID)
			return &RunResult{UserMessage: userMsg, AssistantMessage: fin}, nil
		default:
			if o.logger != nil {
				o.logger.Printf("orchestrator: image generation error (conv=%s msg=%s): %v", conv.ID, assistantMsg.ID, err)
			}
			// A per-model image timeout (child-ctx deadline) gets a clearer message.
			safeErr := "Image generation failed. Please try again."
			if errors.Is(err, context.DeadlineExceeded) {
				safeErr = "Image generation timed out. Please try again."
			}
			errBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: safeErr}})
			_ = finishMessage(store.MessageFinishPatch{
				Blocks: errBlocks, Citations: []byte("[]"), Status: "error", Error: safeErr,
			})
			onEvent(SseEvent{Type: "error", Message: safeErr})
			return nil, err
		}
	}
	if err != nil && len(artBlocks) > 0 && ctx.Err() == nil {
		if billing != nil {
			billing.KeepReserved = true
		}
		blocksJSON, _ := json.Marshal(artBlocks)
		const safeErr = "Billing settlement failed. Your generated image was saved, but this turn requires administrator review."
		_ = finishMessage(store.MessageFinishPatch{
			Blocks: blocksJSON, Citations: []byte("[]"), Status: "error", Error: safeErr,
			GenMs: time.Since(turnStart).Milliseconds(),
		})
		onEvent(SseEvent{Type: "error", Message: safeErr})
		return nil, err
	}

	// At least one image was produced (a late `err` on stop still keeps the image).
	blocks := artBlocks
	if len(blocks) == 0 && strings.TrimSpace(output) != "" {
		blocks = append(blocks, UnifiedBlock{Kind: "text", Text: output})
	}
	blocksJSON, _ := json.Marshal(blocks)
	if billing != nil {
		billing.KeepReserved = true
	}

	// Cost: image_generate logged the image usage row; message.cost = the turn's
	// side costs (image + any prompt-optimization). Credits debited when the
	// group's free image allotment is exhausted (§4.20 — same flow as chat).
	sideCosts, billingErr := store.TurnSideBillingCosts(persistCtx, o.db, assistantMsg.ID)
	if billingErr != nil {
		const safeErr = "Billing settlement failed. Your result was saved, but this turn requires administrator review."
		_ = finishMessage(store.MessageFinishPatch{
			Blocks: blocksJSON, Citations: []byte("[]"), Status: "error", Error: safeErr,
			GenMs: time.Since(turnStart).Milliseconds(),
		})
		onEvent(SseEvent{Type: "error", Message: safeErr})
		return nil, fmt.Errorf("read image-turn billing costs: %w", billingErr)
	}
	turnTotal := sideCosts.Total
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	debit, settleErr := o.settleUsageBilling(settleCtx, billing, float64(len(artBlocks)), turnTotal, 0)
	settleCancel()
	if settleErr != nil {
		const safeErr = "Billing settlement failed. Your result was saved, but this turn requires administrator review."
		_ = finishMessage(store.MessageFinishPatch{
			Blocks: blocksJSON, Citations: []byte("[]"), Status: "error", Error: safeErr,
			Cost: turnTotal, GenMs: time.Since(turnStart).Milliseconds(),
		})
		onEvent(SseEvent{Type: "error", Message: safeErr})
		return nil, fmt.Errorf("settle image-turn billing: %w", settleErr)
	}
	turnCredits := debit.Total
	// The image cost row was written before the orchestrator knew the final credit
	// charge. Add a zero-cost attribution row so the turn is still recognized as
	// credit-paid without perturbing image counts or cost totals.
	if turnCredits > 0 {
		o.logUsage(persistCtx, store.UsageLog{
			UserID:         req.UserID,
			WorkspaceID:    conv.WorkspaceID,
			ConversationID: conv.ID,
			MessageID:      assistantMsg.ID,
			ModelID:        model.ID,
			Purpose:        "image",
			Credits:        turnCredits,
			Currency:       model.Currency,
		})
	}
	stopReason := "stop"
	if err != nil {
		stopReason = "stopped" // image produced, then the stream was cut
	}
	_ = finishMessage(store.MessageFinishPatch{
		Blocks: blocksJSON, Citations: []byte("[]"),
		StopReason: stopReason, Status: "complete",
		Cost: turnTotal, Credits: turnCredits,
		GenMs: time.Since(turnStart).Milliseconds(),
	})

	if shouldGenerateTitle(conv) {
		o.scheduleTitle(conv.ID, req.UserID, req.UserText, req.Locale)
	}

	finalAssistant, _ := store.GetMessage(persistCtx, o.db, assistantMsg.ID)
	onEvent(SseEvent{Type: "done", MessageID: assistantMsg.ID, StopReason: stopReason, Credits: turnCredits})
	return &RunResult{UserMessage: userMsg, AssistantMessage: finalAssistant}, nil
}

// optimizeImagePrompt expands the user's request into a richer prompt and folds
// in the style's hidden prompt — using the admin-set text model
// (settings.image_prompt_model_id). When unset or on error it falls back to a
// deterministic join so generation always proceeds. The hidden prompt is
// composed here and NEVER returned to the client.
func (o *Orchestrator) optimizeImagePrompt(ctx context.Context, userID, convID, msgID, userText, styleHidden string) (string, error) {
	join := composeImagePrompt(userText, styleHidden)
	modelID := settingStr(o.db, "image_prompt_model_id")
	if modelID == "" || o.task == nil {
		return join, nil
	}
	sys := "You rewrite a user's request into a single vivid, concrete image-generation prompt. " +
		"Merge any STYLE DIRECTIVES naturally. Preserve the user's subject and intent. " +
		"Output ONLY the final prompt text — no preamble, no quotes, no markdown."
	ask := "USER REQUEST:\n" + strings.TrimSpace(userText)
	if styleHidden != "" {
		ask += "\n\nSTYLE DIRECTIVES (apply, do not mention):\n" + styleHidden
	}
	out, err := o.task.Run(ctx, TaskKind("task.image_prompt"), ask, RunOpts{
		SystemPrompt: sys, ModelID: modelID,
		UserID: userID, ConversationID: convID, MessageID: msgID,
		MaxOutputTokens: imagePromptOptimizerOutputTokens,
	})
	if err != nil {
		if errors.Is(err, ErrTaskBillingRecord) {
			return "", err
		}
		return join, nil
	}
	if strings.TrimSpace(out) == "" {
		return join, nil
	}
	return strings.TrimSpace(out), nil
}

func composeImagePrompt(userText, styleHidden string) string {
	return strings.TrimSpace(strings.TrimSpace(userText) + "\n" + strings.TrimSpace(styleHidden))
}

// storeToUnified converts stored messages to the unified history shape.
//
// §2.3-C/D: when an assistant message was produced by the SAME provider and
// model we attach its raw native exchange (providers replay it verbatim for full
// fidelity). A different/unknown model or provider is downgraded to canonical
// blocks: tool rounds become one-line summaries and thinking blocks are dropped
// by renderBlocksAsText.
func storeToUnified(msgs []store.Message, currentProvider, currentModelID string, nativeToolReplay bool) []UnifiedMessage {
	currentModelID = strings.TrimSpace(currentModelID)
	// §4.13-B / §2.3-C: raw replay re-sends the provider-native exchange
	// verbatim — including encrypted reasoning, tool_use / tool_result, and
	// functionCall blocks. Filter Raw up front on a defensive copy so the empty-
	// turn dropper below sees the exact block-derived view the provider will see.
	// This handles both no-native-tools turns and provider/model switches; prior
	// tool rounds degrade to their readable text trace via renderBlocksAsText.
	cp := make([]store.Message, len(msgs))
	copy(cp, msgs)
	for i := range cp {
		sameNativeModel := cp[i].Role == "assistant" &&
			cp[i].Provider == currentProvider &&
			currentModelID != "" &&
			strings.TrimSpace(cp[i].ModelID) == currentModelID
		if !isPromptToolRawEnvelope(cp[i].Raw) && (!nativeToolReplay || !sameNativeModel) {
			cp[i].Raw = nil
		}
	}
	msgs = cp
	// §workspaces concurrent turns: a shared conversation is one linear thread, so
	// when B asks while A's answer is still generating, B's question chains directly
	// under A's assistant PLACEHOLDER (status="streaming", empty blocks — streamed
	// text isn't persisted until FinishMessage). Left in the history that placeholder
	// becomes an empty assistant turn, which providers reject (Anthropic disallows
	// empty text content blocks), failing B's whole turn. Drop any in-flight / empty
	// assistant turn TOGETHER with its now-orphaned question — dropping only the
	// answer would leave two consecutive user turns, which providers also reject.
	// Purely a per-call transient: the stored messages are untouched, so once A
	// finishes its real answer is used normally on the next turn.
	drop := make([]bool, len(msgs))
	for i, m := range msgs {
		if m.Role == "assistant" && (m.Status == "streaming" || assistantRendersEmpty(m)) {
			drop[i] = true
			if i > 0 && msgs[i-1].Role == "user" {
				drop[i-1] = true
			}
		}
	}
	out := []UnifiedMessage{}
	for i, m := range msgs {
		if drop[i] {
			continue
		}
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		um := UnifiedMessage{Role: m.Role, Blocks: blocks}
		var atts []Attachment
		if len(m.Attachments) > 2 {
			_ = json.Unmarshal(m.Attachments, &atts)
			um.Attachments = atts
		}
		if m.Role == "assistant" && len(m.Raw) > 2 {
			um.Raw = m.Raw
		}
		out = append(out, um)
	}
	return out
}

func shouldReplayNativeToolHistory(fast bool, toolMode string, localToolCount int, hostedTools bool) bool {
	return !fast && toolMode == "native" && (localToolCount > 0 || hostedTools)
}

const fastModeCodeHistoryPlaceholder = "[A previous code-analysis step was omitted in Fast mode.]"

const unsupportedToolHistoryPlaceholder = "[A previous tool step was omitted because this model does not support that tool.]"

// stripRetiredKnowledgeSearchToolBlocks removes persisted calls from the former
// model-driven document-search path. Automatic server-side RAG now owns all
// knowledge retrieval, including turns using provider-hosted Official tools.
func stripRetiredKnowledgeSearchToolBlocks(history []UnifiedMessage) []UnifiedMessage {
	const retiredName = "search_knowledge_base"
	allowed := map[string]bool{}
	found := false
	for _, message := range history {
		for _, block := range message.Blocks {
			name := strings.TrimSpace(block.ToolName)
			if name == "" {
				continue
			}
			if name == retiredName {
				found = true
				continue
			}
			allowed[name] = true
		}
	}
	if !found {
		return history
	}
	return stripDisallowedBuiltinToolBlocks(history, allowed)
}

// stripDisallowedBuiltinToolBlocks removes historical calls that are outside a
// configured model allowlist. Provider-native Raw is dropped on an affected
// message because its vendor-specific call/output items cannot be safely
// filtered here; unaffected messages retain Raw for reasoning/tool continuity.
// Canonical blocks retain only allowed calls and paired outputs. Stored messages
// remain untouched.
func stripDisallowedBuiltinToolBlocks(history []UnifiedMessage, allowed map[string]bool) []UnifiedMessage {
	if allowed == nil {
		return history
	}
	deniedIDs := map[string]bool{}
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.Kind == "tool_call" && !allowed[strings.TrimSpace(block.ToolName)] && block.ToolID != "" {
				deniedIDs[block.ToolID] = true
			}
		}
	}
	out := make([]UnifiedMessage, len(history))
	for index, message := range history {
		filtered := message
		if message.Blocks != nil {
			filtered.Blocks = make([]UnifiedBlock, 0, len(message.Blocks))
		}
		affected := false
		for _, block := range message.Blocks {
			nameDenied := strings.TrimSpace(block.ToolName) != "" && !allowed[strings.TrimSpace(block.ToolName)]
			linkedOutput := block.Kind == "tool_output" && block.ToolID != "" && deniedIDs[block.ToolID]
			if (block.Kind == "tool_call" || block.Kind == "tool_output") && (nameDenied || linkedOutput) {
				affected = true
				continue
			}
			filtered.Blocks = append(filtered.Blocks, cloneUnifiedBlock(block))
		}
		if affected && strings.TrimSpace(renderBlocksAsText(filtered.Blocks)) == "" {
			filtered.Blocks = append(filtered.Blocks, UnifiedBlock{Kind: "text", Text: unsupportedToolHistoryPlaceholder})
		}
		if affected {
			if isPromptToolRawEnvelope(message.Raw) {
				filtered.Raw = filterPromptToolRawEnvelope(message.Raw, func(name string) bool {
					return allowed[strings.TrimSpace(name)]
				})
			} else {
				filtered.Raw = nil
			}
		} else {
			filtered.Raw = append(json.RawMessage(nil), message.Raw...)
		}
		filtered.Attachments = append([]Attachment(nil), message.Attachments...)
		out[index] = filtered
	}
	return out
}

// stripFastModeCodeBlocks removes canonical history for code tools that fast
// mode does not offer. Raw replay is disabled by the caller before conversion,
// so this provider-neutral block filter covers OpenAI Chat/Responses, Anthropic,
// Gemini, and any fallback request built from the resulting unified history.
func stripFastModeCodeBlocks(history []UnifiedMessage) []UnifiedMessage {
	deniedNames := map[string]bool{
		"python_execute":   true,
		"code_interpreter": true,
	}
	deniedIDs := map[string]bool{}
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.Kind == "tool_call" && deniedNames[strings.ToLower(strings.TrimSpace(block.ToolName))] && block.ToolID != "" {
				deniedIDs[block.ToolID] = true
			}
		}
	}

	out := make([]UnifiedMessage, len(history))
	for i, message := range history {
		filtered := message
		if isPromptToolRawEnvelope(message.Raw) {
			filtered.Raw = filterPromptToolRawEnvelope(message.Raw, func(name string) bool {
				return !deniedNames[strings.ToLower(strings.TrimSpace(name))]
			})
		} else {
			filtered.Raw = nil
		}
		if message.Blocks != nil {
			filtered.Blocks = make([]UnifiedBlock, 0, len(message.Blocks))
		}
		affected := false
		for _, block := range message.Blocks {
			nameDenied := deniedNames[strings.ToLower(strings.TrimSpace(block.ToolName))]
			linkedOutput := block.Kind == "tool_output" && block.ToolID != "" && deniedIDs[block.ToolID]
			if (block.Kind == "tool_call" || block.Kind == "tool_output") && (nameDenied || linkedOutput) {
				affected = true
				continue
			}
			filtered.Blocks = append(filtered.Blocks, cloneUnifiedBlock(block))
		}
		if affected && strings.TrimSpace(renderBlocksAsText(filtered.Blocks)) == "" {
			filtered.Blocks = append(filtered.Blocks, UnifiedBlock{Kind: "text", Text: fastModeCodeHistoryPlaceholder})
		}
		filtered.Attachments = append([]Attachment(nil), message.Attachments...)
		out[i] = filtered
	}
	return out
}

// assistantRendersEmpty reports whether a stored assistant turn would collapse to
// empty provider content (no text, no tool trace, no media, no same-vendor raw
// replay). The provider APIs reject empty content, so such a turn must be dropped
// from the prompt rather than sent. This is exactly the state of a still-streaming
// placeholder (its text isn't persisted until FinishMessage, so mid-generation its
// blocks are []) and of a stopped-before-any-output turn.
func assistantRendersEmpty(m store.Message) bool {
	if len(m.Raw) > 2 {
		return false // raw carries the full native exchange verbatim
	}
	var blocks []UnifiedBlock
	if json.Unmarshal(m.Blocks, &blocks) != nil {
		return false // unparseable — keep it rather than risk dropping real content
	}
	for _, b := range blocks {
		switch b.Kind {
		case "image", "document", "artifact":
			return false // becomes a non-empty media block downstream
		}
	}
	return strings.TrimSpace(renderBlocksAsText(blocks)) == ""
}

// resolveAttachments loads image attachments from disk and appends them as
// base64 image blocks to their messages so vision-capable providers can see
// them (§4.6). Errors are silent — a missing file never blocks the turn.
//
// §4.6 vision gating: if the resolved model is not vision-capable, image
// attachments are SKIPPED with a visible note appended to the user turn so the
// user sees "this model can't read images, pick a vision-capable one".
//
// Documents are deliberately NOT attached as native provider file/document
// blocks. Every LLM API request uses the RAG text path for PDFs/DOCX/PPTX/etc.:
// upload -> parse/OCR -> chunks -> retrieval/full-text injection. This keeps
// provider wire formats simple and avoids gateway-specific file-block failures.
func (o *Orchestrator) resolveAttachments(ctx context.Context, userID, convID string, hist []UnifiedMessage, model *store.Model, onEvent func(SseEvent)) {
	visionCapable := model != nil && model.Vision
	notedNonVision := false
	notedPDFRAGOnly := false
	notedOversizeImage := false
	for i := range hist {
		// §4.6-C role gate: attachments belong to the user turn that uploaded them.
		// A non-user row can only carry an image attachment via a copy path (share
		// import / fork / branch), and inlining it would emit an image content part
		// on an assistant/model role — which every provider rejects (OpenAI: "unknown
		// variant `image_url`, expected `text`"; Anthropic: assistant content must be
		// text/tool_use; Gemini: media not allowed on a model turn). Resolve
		// attachments on user turns only; the provider serializers gate again in depth.
		if hist[i].Role != "user" {
			continue
		}
		for _, a := range hist[i].Attachments {
			f, err := store.GetFile(ctx, o.db, a.ID, userID)
			if err != nil || f.ConversationID != convID {
				continue
			}

			data, imageMIME, imageState := readVerifiedProviderImage(f, o.uploadDir)
			switch imageState {
			case verifiedAttachmentImage:
				if !visionCapable {
					// This path covers legacy history whose stored message metadata did
					// not say image. Current requests are rejected before SSE by the API.
					if !notedNonVision && onEvent != nil {
						onEvent(SseEvent{Type: "rag", Status: "warning", Summary: "model does not support images; attached images were skipped"})
						notedNonVision = true
					}
					hist[i].Blocks = append(hist[i].Blocks, UnifiedBlock{
						Kind: "text",
						Text: "[image attachment skipped — current model lacks vision capability]",
					})
					continue
				}
				hist[i].Blocks = append(hist[i].Blocks, UnifiedBlock{
					Kind: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: imageMIME, Title: f.Filename,
				})
				continue
			case rejectedOversizeAttachmentImage:
				if !notedOversizeImage && onEvent != nil {
					onEvent(SseEvent{Type: "rag", Status: "warning", Summary: "image attachment exceeded the provider inline limit and was skipped"})
					notedOversizeImage = true
				}
				continue
			}

			// Documents use the RAG text path. Use only server-owned metadata here;
			// a forged client kind can neither turn a PDF into an image nor suppress
			// its document handling.
			if storedFileIsPDF(f) {
				if !store.ConversationDocReady(ctx, o.db, convID, f.Filename) && !notedPDFRAGOnly && onEvent != nil {
					onEvent(SseEvent{Type: "rag", Status: "warning", Summary: "PDF attachment is still indexing; documents are read through RAG text, not native file blocks"})
					notedPDFRAGOnly = true
				}
				hist[i].Blocks = append(hist[i].Blocks, UnifiedBlock{
					Kind: "text",
					Text: fmt.Sprintf("[PDF attachment %q is read through the indexed RAG text path; do not expect a native PDF/file block in the provider request.]", f.Filename),
				})
				continue
			}
		}
	}
}

// resolveImageArtifactBlocks hydrates generated-image artifact blocks only for a
// provider path that explicitly needs them as model input (currently OpenAI's
// hosted Responses image tool). The database remains metadata-only; base64 lives
// only in this request copy and is ownership-checked and byte-sniffed.
func (o *Orchestrator) resolveImageArtifactBlocks(ctx context.Context, userID string, hist []UnifiedMessage) {
	for i := range hist {
		if hist[i].Role != "assistant" {
			continue
		}
		for j := range hist[i].Blocks {
			block := &hist[i].Blocks[j]
			if block.Kind != "artifact" || block.Data != "" {
				continue
			}
			artifactID := strings.TrimSpace(block.FileRef)
			if artifactID == "" && len(block.Artifacts) > 0 {
				artifactID = strings.TrimSpace(block.Artifacts[0].ID)
			}
			if artifactID == "" {
				continue
			}
			artifact, err := store.GetArtifact(ctx, o.db, artifactID, userID)
			if err != nil || artifact == nil || !reusableProviderImageArtifact(artifact.Source) ||
				artifact.SizeBytes <= 0 || artifact.SizeBytes > attachmentImageInlineBytes {
				continue
			}
			safePath, err := resolveLLMStoragePath(artifact.StoragePath, o.artifactDir)
			if err != nil {
				continue
			}
			file, err := os.Open(safePath)
			if err != nil {
				continue
			}
			data, readErr := io.ReadAll(io.LimitReader(file, attachmentImageInlineBytes+1))
			_ = file.Close()
			if readErr != nil || int64(len(data)) > attachmentImageInlineBytes {
				continue
			}
			mimeType := providerImageMIMEFromBytes(data)
			if mimeType == "" {
				continue
			}
			block.Data = base64.StdEncoding.EncodeToString(data)
			block.MimeType = mimeType
		}
	}
}

func reusableProviderImageArtifact(source string) bool {
	switch strings.TrimSpace(source) {
	case "", store.ArtifactSourceImageGenerate, store.ArtifactSourceHostedImageGeneration:
		// Empty source keeps image artifacts produced before source attribution was
		// introduced. Python and unrelated downloadable artifacts are never selected
		// implicitly as an image-generation edit source.
		return true
	default:
		return false
	}
}

func (o *Orchestrator) persistProviderGeneratedImages(ctx context.Context, tc *ToolContext, images []GeneratedImage) (int, error) {
	registry, ok := o.tools.(providerArtifactRegistry)
	if !ok {
		return 0, errors.New("provider image artifact storage is unavailable")
	}
	persisted := 0
	for i, image := range images {
		mimeType := providerImageMIMEFromBytes(image.Data)
		if mimeType == "" {
			return persisted, fmt.Errorf("provider generated image %d has an unsupported format", i+1)
		}
		name := fmt.Sprintf("image_%d%s", i+1, providerImageExtension(mimeType))
		if err := registry.SaveArtifact(ctx, tc, name, mimeType, image.Data); err != nil {
			return persisted, fmt.Errorf("persist provider generated image %d: %w", i+1, err)
		}
		persisted++
	}
	return persisted, nil
}

func providerImageExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

type verifiedAttachmentImageState uint8

const (
	notAttachmentImage verifiedAttachmentImageState = iota
	verifiedAttachmentImage
	rejectedOversizeAttachmentImage
)

// readVerifiedProviderImage reads at most one bounded file and classifies it
// from bytes, not attachment or database claims. The size check happens both
// before and during the read because legacy metadata may be stale.
func readVerifiedProviderImage(file *store.File, roots ...string) ([]byte, string, verifiedAttachmentImageState) {
	if file == nil || attachmentImageInlineBytes <= 0 {
		return nil, "", notAttachmentImage
	}
	metadataImage := strings.EqualFold(strings.TrimSpace(file.Kind), "image") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.MimeType)), "image/") ||
		providerImageFilename(file.Filename)
	if file.SizeBytes > attachmentImageInlineBytes {
		if metadataImage {
			return nil, "", rejectedOversizeAttachmentImage
		}
		return nil, "", notAttachmentImage
	}
	safePath, err := resolveLLMStoragePath(file.StoragePath, roots...)
	if err != nil {
		return nil, "", notAttachmentImage
	}
	f, err := os.Open(safePath)
	if err != nil {
		return nil, "", notAttachmentImage
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, attachmentImageInlineBytes+1))
	if err != nil {
		return nil, "", notAttachmentImage
	}
	if int64(len(data)) > attachmentImageInlineBytes {
		return nil, "", rejectedOversizeAttachmentImage
	}
	mimeType := providerImageMIMEFromBytes(data)
	if mimeType == "" {
		return nil, "", notAttachmentImage
	}
	return data, mimeType, verifiedAttachmentImage
}

func storedFileIsPDF(file *store.File) bool {
	if file == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(file.Kind), "pdf") ||
		strings.EqualFold(strings.TrimSpace(strings.SplitN(file.MimeType, ";", 2)[0]), "application/pdf") ||
		strings.EqualFold(filepath.Ext(file.Filename), ".pdf")
}

func providerImageFilename(filename string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(filename))) {
	case ".png", ".apng", ".jpg", ".jpeg", ".jpe", ".jfif", ".gif", ".webp", ".bmp",
		".tif", ".tiff", ".heic", ".heif", ".avif", ".ico", ".cur", ".jxl", ".psd", ".svg":
		return true
	default:
		return false
	}
}

func providerImageMIMEFromBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	if detected := strings.ToLower(http.DetectContentType(head)); strings.HasPrefix(detected, "image/") {
		return detected
	}
	if len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) {
		brands := head[8:]
		if len(brands) > 64 {
			brands = brands[:64]
		}
		for _, brand := range []string{"avif", "avis"} {
			if bytes.Contains(brands, []byte(brand)) {
				return "image/avif"
			}
		}
		for _, brand := range []string{"heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1"} {
			if bytes.Contains(brands, []byte(brand)) {
				return "image/heif"
			}
		}
	}
	if bytes.HasPrefix(head, []byte("8BPS")) {
		return "image/vnd.adobe.photoshop"
	}
	if bytes.HasPrefix(head, []byte{0xff, 0x0a}) || bytes.HasPrefix(head, []byte("\x00\x00\x00\x0cJXL \r\n\x87\n")) {
		return "image/jxl"
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(head, []byte{0xef, 0xbb, 0xbf}))
	if bytes.Contains(bytes.ToLower(trimmed), []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}

// renderBlocksAsText flattens a block list to plain text for history rebuild:
// text blocks verbatim; tool rounds compressed to a one-line summary (§2.3-D
// cross-vendor downgrade, e.g. "[已执行 python_execute，输出：均值=5.5]");
// thinking blocks are never replayed as visible text.
func renderBlocksAsText(blocks []UnifiedBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		switch blk.Kind {
		case "text":
			if blk.Text != "" {
				b.WriteString(blk.Text)
				b.WriteString("\n")
			}
		case "tool_call":
			fmt.Fprintf(&b, "[已执行 %s，输出：%s]\n", blk.ToolName, blk.Summary)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// SkillIndex is a slim view used for system prompt composition.
type SkillIndex struct {
	Name string
	When string
}

// SkillFull carries a skill's full instructions, injected inline for
// prompt/none tool-mode models that can't call use_skill (§4.17).
type SkillFull struct {
	Name         string
	Instructions string
}

func loadEnabledModelSkills(ctx context.Context, db *sql.DB, modelID string, policy *ToolAccessPolicy) ([]SkillIndex, []SkillFull) {
	indexes := []SkillIndex{}
	full := []SkillFull{}
	skillIDs, _ := store.SkillsForModel(ctx, db, modelID)
	for _, skillID := range skillIDs {
		if !skillAccessPolicyAllows(policy, skillID) {
			continue
		}
		skill, err := store.GetSkill(ctx, db, skillID)
		if err != nil || !skill.Enabled {
			continue
		}
		indexes = append(indexes, SkillIndex{Name: skill.Name, When: skill.Description})
		full = append(full, SkillFull{Name: skill.Name, Instructions: skill.Instructions})
	}
	return indexes, full
}

type systemPromptOpts struct {
	ModelSystem string
	// ModelLabel is the admin-configured display name of the model. It drives the
	// built-in identity line so the assistant identifies as this name (§ identity).
	ModelLabel string
	// Locale is the user's UI language code; anchors the reply-language line so
	// replies follow the user's message language (defaulting to this on ambiguity).
	Locale              string
	ToolMode            string   // native | prompt | none
	ToolNames           []string // names of the tools actually enabled for this model
	ProjectName         string
	ProjectInstructions string
	Skills              []SkillIndex
	SkillsFull          []SkillFull
	Memories            []store.Memory
	ProjectFiles        []ProjectFileSummary
	// SandboxFiles are supported conversation data files and verified uploaded
	// images staged at /workspace/uploads. Listed only when python_execute is
	// enabled.
	SandboxFiles []ProjectFileSummary
	// Persona is the user's personalization (tone traits + custom instructions
	// + nickname). Empty fields render nothing.
	Persona UserPersona
	// InlineQuote is the excerpt a text-selection sub-conversation is anchored to.
	// When non-empty the assistant is told to focus on explaining/discussing it.
	InlineQuote string
	// InlineSource is the FULL text of the message the excerpt was lifted from,
	// injected so a short ambiguous quote has the context it needs.
	InlineSource string
	// SkillToolAvailable is true only when the use_skill tool is actually exposed
	// to the model this turn. When false (official/hosted tools, none mode, or
	// use_skill disabled), skills are inlined in full so they still take effect
	// instead of pointing the model at a tool it can't call.
	SkillToolAvailable bool
	// SkillsAllowed captures the primary request ceiling separately from whether
	// use_skill is natively declared. It remains true for tool_mode=none models
	// that inline skills, but false for a per-turn disable or model/global policy.
	// TTFT fallback uses it to avoid broadening the original request policy.
	SkillsAllowed bool
	// SkillMode carries the effective administrator/per-turn skill policy into
	// prompt composition. Selected policies may expose database skills while
	// still excluding code-defined skills without catalog IDs.
	SkillMode string
}

func skillModeAllowsBuiltinDocGen(mode string) bool {
	return mode == "" || mode == store.ResourceAccessAll
}

// UserPersona is the per-user personalization read from settings.
type UserPersona struct {
	Traits   []string `json:"traits"`   // stable trait keys (concise, friendly, …)
	Custom   string   `json:"custom"`   // free-form custom instructions
	Nickname string   `json:"nickname"` // what to call the user
}

func (p UserPersona) empty() bool {
	return len(p.Traits) == 0 && strings.TrimSpace(p.Custom) == "" && strings.TrimSpace(p.Nickname) == ""
}

// personaTraitPhrases maps the UI's trait keys to a short instruction phrase.
// Unknown keys fall through to the raw key so a future preset still reads okay.
var personaTraitPhrases = map[string]string{
	"concise":      "concise and to the point",
	"detailed":     "thorough and detailed",
	"friendly":     "warm and friendly",
	"professional": "professional",
	"encouraging":  "encouraging and supportive",
	"direct":       "direct and straight-shooting",
	"witty":        "witty, with light humor",
	"socratic":     "Socratic — guide with questions",
	"genz":         "casual, Gen-Z tone",
	"formal":       "formal",
}

// readUserPersona loads the persona from per-user settings keys persona_traits
// / persona_custom / persona_nickname. Missing keys yield empty fields.
func readUserPersona(ctx context.Context, db *sql.DB, userID string) UserPersona {
	var p UserPersona
	if raw, err := store.GetUserSettingKey(ctx, db, userID, "persona_traits"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &p.Traits)
	}
	if raw, err := store.GetUserSettingKey(ctx, db, userID, "persona_custom"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &p.Custom)
	}
	if raw, err := store.GetUserSettingKey(ctx, db, userID, "persona_nickname"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &p.Nickname)
	}
	return p
}

// recentHistoryStrings returns up to n trailing "role: text" strings from the
// message path, used to give the RAG query router conversational context.
func recentHistoryStrings(msgs []store.Message, n int) []string {
	out := []string{}
	start := 0
	if len(msgs) > n {
		start = len(msgs) - n
	}
	for _, m := range msgs[start:] {
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		text := strings.Builder{}
		for _, b := range blocks {
			if b.Kind == "text" {
				text.WriteString(b.Text)
				text.WriteString(" ")
			}
		}
		t := strings.TrimSpace(text.String())
		if t == "" {
			continue
		}
		if len([]rune(t)) > ragRouterRecentHistoryTruncate {
			t = string([]rune(t)[:ragRouterRecentHistoryTruncate])
		}
		out = append(out, m.Role+": "+t)
	}
	return out
}

// (§4.8-L10N) The former replyLanguageDirective was removed: the whole system
// prompt now renders in the user's language (see composeSystemPrompt +
// prompt_l10n.go), so a separate "reply in X" line is redundant. Title
// generation keeps its own directive below because a task-model call has no
// localized system prompt.

// titleLanguageDirective returns a "write the title in this language" instruction
// WRITTEN IN the user's selected UI language. Empty for unknown/blank locales.
func titleLanguageDirective(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "en", "en-us", "en-gb":
		return "Write the title in English."
	case "zh", "zh-cn", "zh-hans", "zh-sg":
		return "请用简体中文写这个标题。"
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo":
		return "請用繁體中文寫這個標題。"
	case "ja", "ja-jp":
		return "タイトルは日本語で書いてください。"
	case "fr", "fr-fr", "fr-ca":
		return "Rédige le titre en français."
	default:
		return ""
	}
}

// composeSystemPrompt implements the §4.8 six-segment composition in stable
// order. Stable = cache-friendly (§4.9).
func composeSystemPrompt(o systemPromptOpts) string {
	var b strings.Builder
	// §4.8-L10N: the WHOLE authored prompt renders in the user's UI language via
	// `l` (English is the default/fallback). Because the prompt itself is in the
	// target language, a separate "always reply in X" directive is no longer
	// needed — and was removed. Only tool NAMES, boundary tags, markdown/paths,
	// and admin/user DATA stay language-neutral (see prompt_l10n.go).
	l := promptL10nFor(o.Locale)
	// ① built-in identity (§ identity): the assistant identifies as the model's
	// admin-configured display NAME — never a hardcoded product name. So a model
	// labelled "GPT 5.5" answers "who are you?" with "I am GPT 5.5", regardless of
	// the actual upstream provider.
	label := strings.TrimSpace(o.ModelLabel)
	if label == "" {
		label = "an AI assistant"
	}
	fmt.Fprintf(&b, l.identity, label, label)

	// ② model-level system prompt (admin-customised behaviour/persona), or the
	// localized default style line when the admin hasn't set one.
	if s := strings.TrimSpace(o.ModelSystem); s != "" {
		b.WriteString("\n\n")
		b.WriteString(s)
	} else {
		b.WriteString(l.defaultStyle)
	}

	// ①.1 ground the model in real time. Without this it falls back to its
	// training-era date, so "today" / "latest" — and the queries it hands to
	// a web-search tool — silently target the wrong year. Server-local time; operators
	// set TZ to their zone. English keeps the weekday; other locales use the ISO
	// date to avoid an English weekday inside a localized sentence.
	now := time.Now()
	dateStr := now.Format("2006-01-02")
	if promptLocaleKey(o.Locale) == "en" {
		dateStr = now.Format("Monday, 2006-01-02")
	}
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf(l.dateGrounding, dateStr))

	// ①.5 user personalization — tone traits + custom instructions + nickname.
	// Placed high so the assistant adopts the user's preferred style.
	if !o.Persona.empty() {
		b.WriteString("\n\n")
		b.WriteString(l.personaHeader)
		var phrases []string
		for _, key := range o.Persona.Traits {
			if ph, ok := personaTraitPhrases[key]; ok {
				phrases = append(phrases, ph)
			} else if k := strings.TrimSpace(key); k != "" {
				phrases = append(phrases, k)
			}
		}
		if len(phrases) > 0 {
			fmt.Fprintf(&b, l.personaTone, strings.Join(phrases, "; "))
		}
		if n := strings.TrimSpace(o.Persona.Nickname); n != "" {
			fmt.Fprintf(&b, l.personaAddress, n)
		}
		if c := strings.TrimSpace(o.Persona.Custom); c != "" {
			b.WriteString(c)
			b.WriteString("\n")
		}
	}

	// §4.11.7 prompt-injection defense — added inline so the rule travels with
	// the stable system prefix (cacheable). Without this, a poisoned document
	// in retrieval can hijack the model with "Ignore previous instructions…".
	b.WriteString("\n\n")
	b.WriteString(l.trustHeader)
	b.WriteString(l.trustBody)

	// ② tool guidance — only mention tools actually enabled for this model.
	has := map[string]bool{}
	for _, n := range o.ToolNames {
		has[n] = true
	}
	if o.ToolMode != "none" && len(o.ToolNames) > 0 {
		if o.ToolMode == "native" {
			// Native function-calling: each enabled tool already ships its NAME +
			// DESCRIPTION + input schema in the request's `tools` array (the
			// descriptions are in fact more detailed than a one-line hint), so
			// per-tool "use X for Y" guidance here would just duplicate them.
			// Emit ONLY the cross-cutting steering the schema doesn't carry:
			// cite web/KB results inline, and retry weak tool results. use_skill
			// usage is already mandated by the "## Skills available" section.
			b.WriteString("\n\n")
			b.WriteString(l.toolHeader)
			if has[toolnames.AivoryWebSearch] {
				b.WriteString(l.toolCite)
			}
			b.WriteString(l.toolMultiRound)
		} else {
			// Prompt mode (§4.13): the model has NO tool schema — it learns each
			// tool ONLY from this section, so list every enabled one. use_skill is
			// excluded (prompt/none mode inlines skills in segment ③).
			guidance := []struct{ name, line string }{
				{toolnames.AivoryWebSearch, l.toolWebSearch},
				{"fetch_image", l.toolFetchImage},
				{"python_execute", l.toolPython},
				{"image_generate", l.toolImage},
				{"save_memory", l.toolSaveMemory},
			}
			wrote := false
			for _, g := range guidance {
				if has[g.name] {
					if !wrote {
						b.WriteString("\n\n")
						b.WriteString(l.toolHeader)
						wrote = true
					}
					b.WriteString(g.line)
				}
			}
			if wrote {
				b.WriteString(l.toolMultiRound)
			}
		}

		// §4.5.1 "quality watershed": when the user asks for a downloadable
		// document (PDF / PPT / DOCX / XLSX), the model MUST follow the DocGen
		// recipes rather than improvise. Without them, the output looks like
		// LaTeX from 1995. With them, it looks like an editorial deck.
		// Progressive disclosure (§4.17): a model that can call use_skill loads
		// them on demand via the built-in document-generation entry in the
		// skills index below — inlining ~800 tokens on every turn that never
		// produces a document is dead weight. Models that can't call use_skill
		// still get them inline.
		if has["python_execute"] {
			if o.SkillsAllowed && !o.SkillToolAvailable && skillModeAllowsBuiltinDocGen(o.SkillMode) {
				b.WriteString("\n")
				b.WriteString(DocGenRecipes)
			}

			// Conversation-uploaded data files persist in the sandbox across turns
			// — list them so the model can act on a file uploaded earlier.
			if len(o.SandboxFiles) > 0 {
				b.WriteString(l.sandboxHeader)
				for _, f := range o.SandboxFiles {
					fmt.Fprintf(&b, "- /workspace/uploads/%s (%s)\n", f.Name, f.Kind)
				}
				b.WriteString(l.sandboxBody)
			}
		}
	}

	// ③ skills (§4.17). When use_skill is actually exposed → slim index +
	// progressive disclosure (the model loads a skill on demand). When it is not
	// (official/hosted tools, none mode, or use_skill disabled) → inline full
	// instructions so the skill still takes effect instead of pointing the model
	// at a tool it can't call.
	// The built-in document-generation skill (§4.5.1) joins the index when the
	// model can run python_execute; an admin skill with the same name shadows
	// it (mirrored in useSkillTool's lookup order).
	skillIdx := []SkillIndex(nil)
	if o.SkillsAllowed {
		skillIdx = o.Skills
	}
	if o.SkillsAllowed && o.SkillToolAvailable && o.ToolMode != "none" && has["python_execute"] && skillModeAllowsBuiltinDocGen(o.SkillMode) {
		shadowed := false
		for _, s := range o.Skills {
			if strings.EqualFold(s.Name, DocGenSkillName) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			skillIdx = append(append([]SkillIndex{}, o.Skills...), SkillIndex{Name: DocGenSkillName, When: DocGenWhen})
		}
	}
	if o.SkillsAllowed && o.SkillToolAvailable && len(skillIdx) > 0 {
		b.WriteString(l.skillsAvailHeader)
		b.WriteString(l.skillsAvailBody)
		for _, s := range skillIdx {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.When)
		}
	} else if o.SkillsAllowed && len(o.SkillsFull) > 0 {
		b.WriteString(l.skillsInlineHeader)
		b.WriteString(l.skillsInlineBody)
		for _, s := range o.SkillsFull {
			fmt.Fprintf(&b, "\n### %s\n%s\n", s.Name, s.Instructions)
		}
	}

	// ④ project instructions
	if o.ProjectInstructions != "" {
		fmt.Fprintf(&b, l.projectHeader, o.ProjectName)
		b.WriteString(o.ProjectInstructions)
		b.WriteString("\n")
	}

	// ⑤ current memories (only ACTIVE + QUERY_DEPENDENT, §4.16). The
	// [CURRENT]/[CONTEXT-DEPENDENT] markers stay language-neutral (the rules line
	// references them literally).
	if len(o.Memories) > 0 {
		b.WriteString(l.memoryHeader)
		for _, m := range o.Memories {
			label := "[CURRENT]"
			if m.Status == "QUERY_DEPENDENT" {
				label = "[CONTEXT-DEPENDENT]"
			}
			fmt.Fprintf(&b, "%s %s\n", label, m.MemoryText)
		}
		b.WriteString(l.memoryRules)
	}

	// ⑥ available documents
	if len(o.ProjectFiles) > 0 {
		b.WriteString(l.documentsHeader)
		for _, f := range o.ProjectFiles {
			fmt.Fprintf(&b, "- %s\n", f.Name)
		}
	}

	// ⑦ inline-thread excerpt (§ text-selection sub-conversations). The user
	// highlighted a passage from a previous answer and started a side thread to
	// ask about it; keep answers tightly scoped to this excerpt. Wrapped in a
	// trust boundary like other injected content.
	if strings.TrimSpace(o.InlineQuote) != "" {
		b.WriteString(l.excerptHeader)
		b.WriteString(l.excerptBody)
		b.WriteString("<excerpt>\n")
		b.WriteString(o.InlineQuote)
		b.WriteString("\n</excerpt>\n")
		if strings.TrimSpace(o.InlineSource) != "" {
			b.WriteString("<source-message>\n")
			b.WriteString(o.InlineSource)
			b.WriteString("\n</source-message>\n")
		}
	}

	// NOTE: the long-context summary (§4.7) and RAG snippets (§4.11-B) are
	// deliberately NOT part of the system prompt — they belong to the message
	// layer (injected by the orchestrator) so the system prefix stays stable
	// and cacheable (§4.9). See injectSummaryIntoHistory / injectRAGIntoHistory.
	return b.String()
}

// formatRAGContext renders retrieved snippets as a text block to append to the
// current user turn (closest to the question → best recall).
//
// §4.11.7 prompt-injection protection: wrap context with explicit boundary
// tags. Combined with the system-prompt declaration that <context>…</context>
// is reference material (NOT instructions), this neutralizes prompt-injected
// "ignore the user" patterns embedded in retrieved documents.
func formatRAGContext(snips []Citation, locale string) string {
	if len(snips) == 0 {
		return ""
	}
	b := strings.Builder{}
	b.WriteString("\n\n<context-from-knowledge-base>\n")
	b.WriteString(promptL10nFor(locale).ragIntro)
	for i, c := range snips {
		index := c.Index
		if index <= 0 {
			index = i + 1
		}
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", index, c.Title, c.Snippet)
	}
	b.WriteString("</context-from-knowledge-base>\n")
	return b.String()
}

// resolvedTurnCitations preserves the historical append-and-renumber behavior
// unless this turn actually attached a knowledge base. In that branch, the
// answer model has already seen stable [n] labels, so unused KB sources are
// removed without closing numbering gaps; web/tool sources are retained.
func resolvedTurnCitations(ragCitations, providerCitations []Citation, blocks []UnifiedBlock, pruneUnusedKB bool) []Citation {
	if !pruneUnusedKB {
		out := append(append([]Citation{}, ragCitations...), providerCitations...)
		for i := range out {
			out[i].Index = i + 1
		}
		return out
	}

	normalizedRAG := append([]Citation(nil), ragCitations...)
	maxIndex := 0
	for i := range normalizedRAG {
		if normalizedRAG[i].Index <= 0 {
			normalizedRAG[i].Index = i + 1
		}
		if normalizedRAG[i].Index > maxIndex {
			maxIndex = normalizedRAG[i].Index
		}
	}
	out := append([]Citation(nil), normalizedRAG...)
	for _, citation := range providerCitations {
		if citation.GlobalIndex && citation.Index > 0 {
			if citation.Index > maxIndex {
				maxIndex = citation.Index
			}
		} else {
			maxIndex++
			citation.Index = maxIndex
		}
		out = append(out, citation)
	}

	used := citationMarkersInBlocks(blocks)
	kept := out[:0]
	for _, citation := range out {
		if isKnowledgeBaseCitation(citation) {
			if _, ok := used[citation.Index]; !ok {
				continue
			}
		}
		kept = append(kept, citation)
	}
	return kept
}

func maxCitationIndex(citations []Citation) int {
	maxIndex := 0
	for _, citation := range citations {
		if citation.Index > maxIndex {
			maxIndex = citation.Index
		}
	}
	return maxIndex
}

func isKnowledgeBaseCitation(citation Citation) bool {
	return citation.Source == "kb"
}

func citationMarkersInBlocks(blocks []UnifiedBlock) map[int]struct{} {
	used := map[int]struct{}{}
	for _, block := range blocks {
		if block.Kind != "text" || block.Text == "" {
			continue
		}
		for index := range citationMarkersOutsideCode(block.Text) {
			used[index] = struct{}{}
		}
	}
	return used
}

// citationMarkersOutsideCode mirrors the frontend citation renderer's most
// important boundary: markers inside inline/fenced code are examples, not
// evidence references. Escaped markers are likewise left literal.
func citationMarkersOutsideCode(text string) map[int]struct{} {
	used := map[int]struct{}{}
	for i := 0; i < len(text); {
		if text[i] == '`' {
			runEnd := i + 1
			for runEnd < len(text) && text[runEnd] == '`' {
				runEnd++
			}
			delimiter := text[i:runEnd]
			if closeAt := strings.Index(text[runEnd:], delimiter); closeAt >= 0 {
				i = runEnd + closeAt + len(delimiter)
				continue
			}
			// An unmatched code delimiter makes the remainder literal markdown.
			break
		}
		if text[i] != '[' || (i > 0 && (text[i-1] == '\\' || text[i-1] == '!')) {
			i++
			continue
		}
		end := i + 1
		for end < len(text) && end-i <= 3 && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		if end == i+1 || end >= len(text) || text[end] != ']' {
			i++
			continue
		}
		// A normal Markdown link label such as [1](https://example.test) is not
		// transformed into a citation marker by the frontend either.
		if end+1 < len(text) && text[end+1] == '(' {
			i = end + 1
			continue
		}
		index, err := strconv.Atoi(text[i+1 : end])
		if err == nil && index > 0 {
			used[index] = struct{}{}
		}
		i = end + 1
	}
	return used
}

// injectSummaryIntoHistory prepends the rolled-up summary to the FIRST user
// message so it sits in the message layer between system and recent turns
// (§4.8) without breaking role alternation (important for Gemini).
func injectSummaryIntoHistory(msgs []UnifiedMessage, text string) []UnifiedMessage {
	if strings.TrimSpace(text) == "" {
		return msgs
	}
	for i := range msgs {
		if msgs[i].Role == "user" {
			msgs[i].Blocks = append([]UnifiedBlock{{Kind: "text", Text: text}}, msgs[i].Blocks...)
			return msgs
		}
	}
	return msgs
}

const (
	toolRouteMaxOutputTokens = 2
	toolRouteInputRuneCap    = 240
	toolRouteInputHeadRunes  = 150
	toolRouteCustomNameCap   = 24
)

// configuredOfficialToolRequests returns every administrator-configured hosted
// tool in configuration order. The historical name mirrors the persisted
// `official_tools` field; there is deliberately no user-supplied selection.
func configuredOfficialToolRequests(raw json.RawMessage, fast bool) ([]string, []json.RawMessage) {
	definitions, err := store.ParseOfficialTools(raw)
	if err != nil {
		return nil, nil
	}
	names := make([]string, 0, len(definitions))
	requests := make([]json.RawMessage, 0, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		isHostedCodeTool := responsesRequestHasToolType(
			MergeOfficialToolRequests(nil, []json.RawMessage{definition.Request}),
			"code_interpreter",
		)
		if name == "" || (fast && (name == "code_interpreter" || isHostedCodeTool)) {
			continue
		}
		names = append(names, name)
		requests = append(requests, append(json.RawMessage(nil), definition.Request...))
	}
	return names, requests
}

// autoTurnNeedsTools resolves cheap, deterministic positive signals first. If
// the real provider declarations are small, sending them directly to the main
// model is cheaper and faster than adding another network round trip. Only the
// remaining ambiguous turns reach the dedicated route model, with no history,
// file names, schemas, request fragments, skill descriptions, or instructions.
// Failures are fail-open because the main model can still decline every tool.
func (o *Orchestrator) autoTurnNeedsTools(
	ctx context.Context,
	req RunRequest,
	history []store.Message,
	localTools []ToolDef,
	hostedNames []string,
	hostedRequests []json.RawMessage,
	files []ProjectFileSummary,
	skills []SkillIndex,
	selectedUserSkill bool,
	workspaceID, messageID string,
) bool {
	capabilities, capabilitySet := toolRouteCapabilities(localTools, hostedNames, hostedRequests)
	attachmentKinds := toolRouteAttachmentKinds(req.Attachments)
	if selectedUserSkill ||
		(toolRouteCurrentAttachmentNeedsFileTool(req.Attachments) && (capabilitySet["code"] || capabilitySet["file"])) ||
		(capabilitySet["web"] && toolRouteInputHasURL(req.UserText)) ||
		((capabilitySet["code"] || capabilitySet["file"]) && toolRouteMentionsFile(req.UserText, files)) ||
		(capabilitySet["skill"] && toolRouteMentionsSkill(req.UserText, skills)) ||
		(toolRouteIsContinuation(req.UserText) && toolRoutePreviousAssistantUsedTool(history)) {
		return true
	}

	if toolRouteSchemaTokenThreshold > 0 &&
		estimateToolDeclarationTokens(localTools, hostedRequests) <= toolRouteSchemaTokenThreshold {
		return true
	}

	if o.task == nil {
		if o.logger != nil {
			o.logger.Printf("tool route: dedicated model unavailable, enabling tools (conv=%s)", req.ConversationID)
		}
		return true
	}

	prompt := formatToolRoutePrompt(capabilities, attachmentKinds, len(files) > 0, req.UserText)
	routeCtx, cancel := context.WithTimeout(ctx, toolRouteTimeout)
	defer cancel()
	decision, err := o.task.Run(routeCtx, TaskToolRoute, prompt, RunOpts{
		UserID:          req.UserID,
		ConversationID:  req.ConversationID,
		MessageID:       messageID,
		WorkspaceID:     workspaceID,
		MaxOutputTokens: toolRouteMaxOutputTokens,
	})
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("tool route: decision failed, enabling tools (conv=%s): %v", req.ConversationID, err)
		}
		return true
	}
	decision = strings.TrimSpace(decision)
	if strings.HasPrefix(decision, "0") {
		return false
	}
	if !strings.HasPrefix(decision, "1") && o.logger != nil {
		o.logger.Printf("tool route: invalid decision %q, enabling tools (conv=%s)", truncate(decision, 80), req.ConversationID)
	}
	return true
}

func estimateToolDeclarationTokens(localTools []ToolDef, hostedRequests []json.RawMessage) int {
	tokens := 0
	if len(localTools) > 0 {
		if raw, err := json.Marshal(localTools); err == nil {
			tokens += estimateToolJSONTokens(raw)
		}
	}
	if len(hostedRequests) > 0 {
		if raw, err := json.Marshal(MergeOfficialToolRequests(nil, hostedRequests)); err == nil {
			tokens += estimateToolJSONTokens(raw)
		}
	}
	return tokens
}

func estimateToolJSONTokens(raw []byte) int {
	byBytes := (len(raw) + 3) / 4
	if byContent := estimateTokens(string(raw)); byContent > byBytes {
		return byContent
	}
	return byBytes
}

func toolRouteCapabilities(localTools []ToolDef, hostedNames []string, hostedRequests []json.RawMessage) ([]string, map[string]bool) {
	set := map[string]bool{}
	custom := []string{}
	addCustom := func(name string) {
		capability := toolRouteCustomCapability(name)
		if !set[capability] {
			set[capability] = true
			custom = append(custom, capability)
		}
	}
	addKnown := func(name string) bool {
		capabilities := toolRouteKnownCapabilities(name)
		for _, capability := range capabilities {
			set[capability] = true
		}
		return len(capabilities) > 0
	}

	for _, tool := range localTools {
		if name := strings.TrimSpace(tool.Name); name != "" && !addKnown(name) {
			addCustom(name)
		}
	}
	for index, name := range hostedNames {
		known := addKnown(name)
		if index < len(hostedRequests) {
			for requestName := range unifiedToolNameSet(nil, nil, []json.RawMessage{hostedRequests[index]}) {
				known = addKnown(requestName) || known
			}
		}
		if !known {
			addCustom(name)
		}
	}

	ordered := make([]string, 0, len(set))
	for _, capability := range []string{"web", "code", "file", "image", "memory", "skill"} {
		if set[capability] {
			ordered = append(ordered, capability)
		}
	}
	ordered = append(ordered, custom...)
	return ordered, set
}

func toolRouteKnownCapabilities(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case name == toolnames.AivoryWebSearch,
		name == "web_fetch",
		name == "fetch_image",
		name == "web_search",
		strings.HasPrefix(name, "web_search_"),
		name == "google_search",
		name == "googlesearch",
		name == "url_context",
		name == "computer_use":
		return []string{"web"}
	case name == "python_execute", name == "code_interpreter", name == "shell", name == "bash":
		return []string{"code", "file"}
	case name == "file_search", strings.HasPrefix(name, "file_search_"):
		return []string{"file"}
	case name == "image_generate", name == "image_generation":
		return []string{"image"}
	case name == "save_memory":
		return []string{"memory"}
	case name == "use_skill", strings.HasPrefix(name, "skill:"):
		return []string{"skill"}
	default:
		return nil
	}
}

func toolRouteCustomCapability(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
			if b.Len() >= toolRouteCustomNameCap {
				break
			}
		}
	}
	if b.Len() == 0 {
		b.WriteString("tool")
	}
	return "custom:" + b.String()
}

func toolRouteAttachmentKinds(attachments []Attachment) []string {
	set := map[string]bool{}
	for _, attachment := range attachments {
		kind := strings.ToLower(strings.TrimSpace(attachment.Kind))
		switch {
		case attachmentIsImage(attachment):
			set["image"] = true
		case kind == "sheet" || isSandboxSpreadsheetFilename(attachment.Filename):
			set["sheet"] = true
		case kind == "code":
			set["code"] = true
		case kind == "pdf":
			set["pdf"] = true
		case kind == "doc":
			set["doc"] = true
		case kind == "text":
			set["text"] = true
		default:
			set["file"] = true
		}
	}
	ordered := []string{}
	for _, kind := range []string{"sheet", "code", "pdf", "doc", "text", "image", "file"} {
		if set[kind] {
			ordered = append(ordered, kind)
		}
	}
	return ordered
}

func toolRouteCurrentAttachmentNeedsFileTool(attachments []Attachment) bool {
	for _, attachment := range attachments {
		kind := strings.ToLower(strings.TrimSpace(attachment.Kind))
		if kind == "sheet" || kind == "code" || isSandboxSpreadsheetFilename(attachment.Filename) {
			return true
		}
		switch strings.ToLower(filepath.Ext(strings.TrimSpace(attachment.Filename))) {
		case ".parquet", ".arrow", ".feather", ".jsonl", ".ndjson", ".sqlite", ".sqlite3", ".db", ".sql":
			return true
		}
	}
	return false
}

func toolRouteInputHasURL(input string) bool {
	input = strings.ToLower(input)
	return strings.Contains(input, "https://") || strings.Contains(input, "http://")
}

func toolRouteMentionsFile(input string, files []ProjectFileSummary) bool {
	input = strings.ToLower(input)
	for _, file := range files {
		name := strings.ToLower(strings.TrimSpace(file.Name))
		if name != "" && strings.Contains(input, name) {
			return true
		}
	}
	return false
}

func toolRouteMentionsSkill(input string, skills []SkillIndex) bool {
	input = strings.ToLower(input)
	for _, skill := range skills {
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if name != "" && strings.Contains(input, name) {
			return true
		}
	}
	return false
}

func toolRoutePreviousAssistantUsedTool(history []store.Message) bool {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Role != "assistant" {
			continue
		}
		var blocks []UnifiedBlock
		if json.Unmarshal(message.Blocks, &blocks) != nil {
			return false
		}
		for _, block := range blocks {
			if block.Kind == "tool_call" || block.Kind == "tool_output" {
				return true
			}
		}
		return false
	}
	return false
}

func toolRouteIsContinuation(input string) bool {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" || len([]rune(input)) > 160 {
		return false
	}
	for _, prefix := range []string{
		"continue", "go on", "keep going", "proceed", "do it", "run it", "try again", "retry", "again", "next", "more", "use that",
		"继续", "接着", "往下", "下一步", "再试", "重试", "再来", "就按这个", "按这个", "用这个", "然后呢", "再查", "再运行",
		"続け", "再試行", "poursuis", "continuer", "encore",
	} {
		if strings.HasPrefix(input, prefix) {
			return true
		}
	}
	return false
}

func formatToolRoutePrompt(capabilities, attachmentKinds []string, hasFiles bool, input string) string {
	if len(capabilities) == 0 {
		capabilities = []string{"none"}
	}
	if len(attachmentKinds) == 0 {
		attachmentKinds = []string{"none"}
	}
	files := "0"
	if hasFiles {
		files = "1"
	}
	return "CAP=" + strings.Join(capabilities, ",") +
		"\nATT=" + strings.Join(attachmentKinds, ",") +
		"\nFILES=" + files +
		"\nINPUT=" + truncateToolRouteInput(input)
}

func truncateToolRouteInput(input string) string {
	input = strings.Join(strings.Fields(strings.TrimSpace(input)), " ")
	runes := []rune(input)
	if len(runes) <= toolRouteInputRuneCap {
		return input
	}
	tailRunes := toolRouteInputRuneCap - toolRouteInputHeadRunes
	return string(runes[:toolRouteInputHeadRunes]) + " ... " + string(runes[len(runes)-tailRunes:])
}

// forcedSearchHistoryTurns caps how many recent messages feed the search-query
// task model (keep the prompt small; the latest question dominates intent).
const forcedSearchHistoryTurns = 6

// deriveSearchQueries asks the task model for a few web-search queries that
// would answer the user's latest message given recent context. Falls back to
// the raw user text on any failure so a search still runs.
func (o *Orchestrator) deriveSearchQueries(ctx context.Context, req RunRequest, history []store.Message) []string {
	var b strings.Builder
	start := 0
	if len(history) > forcedSearchHistoryTurns {
		start = len(history) - forcedSearchHistoryTurns
	}
	for _, m := range history[start:] {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		if t := strings.TrimSpace(renderBlocksAsText(blocks)); t != "" {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, truncate(t, 600))
		}
	}
	fmt.Fprintf(&b, "user (latest): %s\n", strings.TrimSpace(req.UserText))

	var out struct {
		Queries []string `json:"queries"`
	}
	if o.task != nil {
		err := o.task.RunJSON(ctx, TaskSearchQueries, b.String(), &out, RunOpts{
			UserID: req.UserID, ConversationID: req.ConversationID,
		})
		if err == nil {
			cleaned := make([]string, 0, len(out.Queries))
			for _, q := range out.Queries {
				if q = strings.TrimSpace(q); q != "" {
					cleaned = append(cleaned, q)
				}
				if len(cleaned) >= forcedSearchQueryCap {
					break
				}
			}
			if len(cleaned) > 0 {
				return cleaned
			}
		}
	}
	if u := strings.TrimSpace(req.UserText); u != "" {
		return []string{u}
	}
	return nil
}

// forcedWebSearch runs a NON-tool web search for a no-tools + web-search turn
// (§4.4-B): a task model turns the conversation into queries, the configured
// searcher runs them, progress streams as aivory_web_search rounds,
// and the results become a <web-search-result> block for prompt injection.
// Returns (contextText, citations); ("", nil) when search is unconfigured or
// yields nothing. Best-effort — a failure never blocks the turn.
func (o *Orchestrator) forcedWebSearch(ctx context.Context, req RunRequest, conv *store.Conversation, history []store.Message, baseIndex int, allowedTools map[string]bool, onEvent func(SseEvent)) (string, []Citation) {
	// Respect the admin platform kill-switch: if aivory_web_search is globally
	// disabled, the forced-search path must not run it either (it would
	// otherwise be a back door around `disabled_tools`).
	if o.disabledToolSet()[toolnames.AivoryWebSearch] {
		return "", nil
	}
	if allowedTools != nil && !allowedTools[toolnames.AivoryWebSearch] {
		return "", nil
	}
	queries := o.deriveSearchQueries(ctx, req, history)
	if len(queries) == 0 {
		return "", nil
	}
	searchTimeout := toolCallTimeout(toolnames.AivoryWebSearch)
	tc := &ToolContext{UserID: req.UserID, ConvID: req.ConversationID, WorkspaceID: conv.WorkspaceID, ModelID: req.ModelID, BuiltinTools: allowedTools}
	var cites []Citation
	var b strings.Builder
	for i, q := range queries {
		id := fmt.Sprintf("fws_%d", i+1)
		input, _ := json.Marshal(map[string]any{"query": q})
		onEvent(SseEvent{Type: "tool_start", Name: toolnames.AivoryWebSearch, ID: id, Input: input})
		// Bound each search with the same per-call timeout orchToolRunner applies
		// (§4.3) so a stalled search backend can't hang the turn pre-first-token.
		sctx, cancel := context.WithTimeout(ctx, searchTimeout)
		out, qcites, err := o.tools.Run(sctx, toolnames.AivoryWebSearch, input, tc)
		cancel()
		if err != nil {
			onEvent(SseEvent{Type: "tool_result", Name: toolnames.AivoryWebSearch, ID: id, Summary: "search failed", Status: "error"})
			continue
		}
		// The searcher returns this exact sentence when no backend is configured
		// (settings + env both empty). Injecting that placeholder would only add
		// noise — stop and let the model answer from training knowledge.
		if strings.HasPrefix(out, "Search not yet configured") {
			onEvent(SseEvent{Type: "tool_result", Name: toolnames.AivoryWebSearch, ID: id, Summary: "search not configured", Status: "error"})
			return "", nil
		}
		onEvent(SseEvent{Type: "tool_result", Name: toolnames.AivoryWebSearch, ID: id, Summary: truncate(out, 400), Status: "complete"})
		// The searcher numbers its inline [n] markers 1..k locally (per query),
		// but the citation RECORDS are renumbered globally with an offset so the
		// KB + web source lists never collide. Remap the injected text's markers
		// to the same offset numbering, or the model's [n] references point at
		// the wrong source.
		offset := baseIndex + len(cites)
		for j := range qcites {
			c := qcites[j]
			c.Index = offset + j + 1
			cites = append(cites, c)
			onEvent(SseEvent{Type: "citation", Citation: &c})
		}
		fmt.Fprintf(&b, "Query: %s\n%s\n\n", q, remapCitationMarkers(strings.TrimSpace(out), len(qcites), offset))
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", nil
	}
	return "<web-search-result>\n" + strings.TrimSpace(b.String()) + "\n</web-search-result>", cites
}

// listSandboxFiles returns the conversation's sandbox-staged files (the same
// kinds tools.pythonExecuteTool stages: sheet/text/code/image). Shared by the
// system-prompt listing and the no-tools forced read. Filename detection keeps
// older rows usable when their stored kind predates the sheet classifier.
func listSandboxFiles(ctx context.Context, db *sql.DB, convID, userID string, roots ...string) []ProjectFileSummary {
	out := []ProjectFileSummary{}
	convFiles, err := store.ListFilesByConversation(ctx, db, convID, userID)
	if err == nil {
		for _, f := range convFiles {
			metadataImage := strings.EqualFold(strings.TrimSpace(f.Kind), "image") ||
				strings.HasPrefix(strings.ToLower(strings.TrimSpace(strings.SplitN(f.MimeType, ";", 2)[0])), "image/")
			verifiedImage := storedSandboxFileLooksLikeImage(f, roots...)
			if metadataImage {
				if verifiedImage && f.SizeBytes >= 0 && f.SizeBytes <= sandboxUploadStagingFileSize {
					out = append(out, ProjectFileSummary{Name: f.Filename, Kind: "image"})
				}
				continue
			}
			// A legacy text/data row containing image bytes is deliberately not
			// advertised or staged. Image access requires explicit server-owned image
			// metadata plus a matching byte signature.
			if verifiedImage {
				continue
			}
			if isSandboxSpreadsheetFilename(f.Filename) {
				out = append(out, ProjectFileSummary{Name: f.Filename, Kind: "sheet"})
				continue
			}
			switch f.Kind {
			case "sheet", "text", "code":
				out = append(out, ProjectFileSummary{Name: f.Filename, Kind: f.Kind})
			}
		}
	}
	if artifacts, err := store.ListImageArtifactsByConversation(ctx, db, convID, userID); err == nil {
		for _, artifact := range artifacts {
			if !reusableGeneratedImageSource(artifact.Source) || !storedSandboxArtifactLooksLikeImage(artifact, roots...) {
				continue
			}
			name := filepath.Base(strings.TrimSpace(artifact.Filename))
			if name == "" || name == "." || name == "/" {
				name = "image"
			}
			out = append(out, ProjectFileSummary{Name: "generated-" + name, Kind: "image"})
		}
	}
	return out
}

func reusableGeneratedImageSource(source string) bool {
	switch strings.TrimSpace(source) {
	case "", store.ArtifactSourceImageGenerate, store.ArtifactSourceHostedImageGeneration:
		return true
	default:
		return false
	}
}

func storedSandboxArtifactLooksLikeImage(artifact store.Artifact, roots ...string) bool {
	if artifact.SizeBytes < 0 || artifact.SizeBytes > sandboxUploadStagingFileSize {
		return false
	}
	safePath, err := resolveLLMStoragePath(artifact.StoragePath, roots...)
	if err != nil {
		return false
	}
	f, err := os.Open(safePath)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, sandboxUploadStagingFileSize+1))
	return err == nil && int64(len(data)) <= sandboxUploadStagingFileSize && providerImageMIMEFromBytes(data) != ""
}

func storedSandboxFileLooksLikeImage(file store.File, roots ...string) bool {
	safePath, err := resolveLLMStoragePath(file.StoragePath, roots...)
	if err != nil {
		return false
	}
	f, err := os.Open(safePath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 0 || info.Size() > sandboxUploadStagingFileSize {
		return false
	}
	head, err := io.ReadAll(io.LimitReader(f, 4096))
	return err == nil && providerImageMIMEFromBytes(head) != ""
}

func isSandboxSpreadsheetFilename(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".csv", ".tsv", ".xlsx", ".xls", ".xlsm":
		return true
	}
	return false
}

// sandboxFilesHaveSheet reports whether any staged file is a spreadsheet — the
// only sandbox kind with no message-layer fallback (text/code are RAG-injected,
// images go to vision), so it's the only one the forced read must cover.
func sandboxFilesHaveSheet(files []ProjectFileSummary) bool {
	for _, f := range files {
		if f.Kind == "sheet" || isSandboxSpreadsheetFilename(f.Name) {
			return true
		}
	}
	return false
}

// shouldInjectSpreadsheetPreview selects the server-side spreadsheet path when
// the current turn cannot read staged uploads with python_execute. Fast mode and
// explicit no-tools turns therefore share the same parsing/injection behavior.
func shouldInjectSpreadsheetPreview(files []ProjectFileSummary, pythonExecuteAvailable bool) bool {
	return !pythonExecuteAvailable && sandboxFilesHaveSheet(files)
}

// Spreadsheet preview bounds for the in-process read used without Python.
const (
	// spreadsheetPreviewInjectionCap bounds the injected preview so a wide or long
	// sheet can't blow the context budget (runes).
	spreadsheetPreviewInjectionCap = 8000
	spreadsheetPreviewRows         = 30
	spreadsheetPreviewCols         = 40
)

// previewSpreadsheetFiles parses the conversation's uploaded spreadsheets
// IN-PROCESS (stdlib rag.SpreadsheetPreview — no code sandbox, no python_execute)
// and returns a bounded <uploaded-data-preview> block. It replaces the sandbox
// read whenever python_execute is unavailable, including fast and no-tools turns,
// so xlsx/csv are parsed server-side and injected as prompt context. Returns ""
// when there are no sheets or none could be parsed (each failure is logged and
// skipped, so one bad file doesn't sink the rest).
func (o *Orchestrator) previewSpreadsheetFiles(ctx context.Context, userID, convID string) string {
	files, err := store.ListFilesByConversation(ctx, o.db, convID, userID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, f := range files {
		if f.Kind != "sheet" && !isSandboxSpreadsheetFilename(f.Filename) {
			continue
		}
		safePath, err := resolveLLMStoragePath(f.StoragePath, o.uploadDir)
		if err != nil {
			continue
		}
		text, perr := rag.SpreadsheetPreview(safePath, f.Filename, spreadsheetPreviewRows, spreadsheetPreviewCols)
		if perr != nil || strings.TrimSpace(text) == "" {
			if o.logger != nil {
				o.logger.Printf("spreadsheet preview skipped file=%q: %v", f.Filename, perr)
			}
			continue
		}
		b.WriteString(strings.TrimRight(text, "\n"))
		b.WriteString("\n\n")
	}
	preview := strings.TrimSpace(b.String())
	if preview == "" {
		return ""
	}
	if r := []rune(preview); len(r) > spreadsheetPreviewInjectionCap {
		preview = string(r[:spreadsheetPreviewInjectionCap]) + "\n…(truncated)"
	}
	return "<uploaded-data-preview>\n" + preview + "\n</uploaded-data-preview>"
}

// resolveLLMStoragePath keeps legacy unit fixtures usable when no deployment
// roots are supplied, while the production constructor always passes both
// configured roots. Remote/object-storage URIs are not accepted by these local
// readers and therefore fail closed.
func resolveLLMStoragePath(path string, roots ...string) (string, error) {
	if len(roots) == 0 || strings.TrimSpace(roots[0]) == "" {
		return path, nil
	}
	return fileguard.ResolveExisting(path, roots...)
}

// remapCitationMarkers rewrites a searcher's local inline citation markers
// `[1]..[maxLocal]` to `[offset+1]..[offset+maxLocal]` so injected search text
// references the globally-renumbered citation records. Markers outside
// 1..maxLocal (incidental bracketed numbers in snippets) are left untouched.
// Single pass, no double-remapping (each match consumed once).
func remapCitationMarkers(text string, maxLocal, offset int) string {
	if maxLocal <= 0 || offset <= 0 || !strings.Contains(text, "[") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '[' {
			b.WriteByte(text[i])
			i++
			continue
		}
		j := i + 1
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		// A valid marker is "[<digits>]" with at least one digit.
		if j > i+1 && j < len(text) && text[j] == ']' {
			n, _ := strconv.Atoi(text[i+1 : j])
			if n >= 1 && n <= maxLocal {
				fmt.Fprintf(&b, "[%d]", offset+n)
				i = j + 1
				continue
			}
		}
		b.WriteByte('[')
		i++
	}
	return b.String()
}

// injectRAGIntoHistory appends retrieved context to the LAST user message.
func injectRAGIntoHistory(msgs []UnifiedMessage, text string) []UnifiedMessage {
	if strings.TrimSpace(text) == "" {
		return msgs
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			msgs[i].Blocks = append(msgs[i].Blocks, UnifiedBlock{Kind: "text", Text: text})
			return msgs
		}
	}
	return msgs
}

// formatSelectedUserSkills renders private skill content as an explicit part of
// the user's request. The store has already enforced the five-skill/64 KiB
// instruction-body limits. No part of this text is included in the system
// prompt or administrator skill index.
func formatSelectedUserSkills(skills []store.UserSkill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<user-selected-skills>\n")
	b.WriteString("Apply the following private skills as user-provided instructions for this request.\n")
	for _, skill := range skills {
		b.WriteString("\n<user-selected-skill name=\"")
		b.WriteString(skill.Name)
		b.WriteString("\">\nDescription: ")
		b.WriteString(skill.Description)
		b.WriteString("\n\n")
		b.WriteString(skill.Instructions)
		b.WriteString("\n</user-selected-skill>\n")
	}
	b.WriteString("</user-selected-skills>\n")
	return b.String()
}

// injectSelectedUserSkillsIntoHistory appends selected skill instructions to
// the last user turn. It intentionally mirrors RAG's last-user placement, but
// runs first so later provider-neutral context additions preserve their order.
func injectSelectedUserSkillsIntoHistory(msgs []UnifiedMessage, skills []store.UserSkill) []UnifiedMessage {
	text := formatSelectedUserSkills(skills)
	if text == "" {
		return msgs
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			msgs[i].Blocks = append(msgs[i].Blocks, UnifiedBlock{Kind: "text", Text: text})
			return msgs
		}
	}
	return msgs
}

func computeCost(m store.Model, u Usage) float64 {
	cost := 0.0
	if m.Kind == "image" {
		// For mock image generation, OutputTokens is repurposed as image count.
		return float64(u.OutputTokens) * m.PricePerImage
	}
	cost += float64(u.InputTokens) / 1_000_000 * m.PriceInput
	cost += float64(u.OutputTokens) / 1_000_000 * m.PriceOutput
	cost += float64(u.CacheReadTokens) / 1_000_000 * m.PriceCacheRead
	cost += float64(u.CacheWriteTokens) / 1_000_000 * m.PriceCacheWrite
	return cost
}

// shouldGenerateTitle is true when the conversation still has its default title.
func shouldGenerateTitle(c *store.Conversation) bool {
	t := strings.TrimSpace(c.Title)
	return t == "" || t == "新对话" || t == "New conversation"
}

// scheduleTitle fires a TaskLLM call in the background to generate a real title.
func (o *Orchestrator) scheduleTitle(convID, userID, userText, locale string) {
	// First, set a deterministic clip so the sidebar updates immediately even
	// when no task model/queue is configured.
	first := clipTitle(userText)
	if first != "" {
		o.persistGeneratedTitle(context.Background(), convID, userID, first)
	}
	o.enqueueTitleTask(convID, userID, userText, locale)
}

// scheduleTitleUpgrade keeps the already-persisted image filename fallback
// visible until the title task has produced a better semantic label.
func (o *Orchestrator) scheduleTitleUpgrade(convID, userID, sourceText, locale string) {
	o.enqueueTitleTask(convID, userID, sourceText, locale)
}

func (o *Orchestrator) enqueueTitleTask(convID, userID, sourceText, locale string) {
	if o.queue == nil || o.task == nil || strings.TrimSpace(sourceText) == "" {
		return
	}
	// Force the title language to the user's UI language. The task model is a
	// separate, often language-biased model that ignores a soft "follow the user"
	// hint, so we append an authoritative directive WRITTEN IN the target language
	// (strongest signal); fall back to matching the message when locale is unknown.
	sys := defaultSystem(TaskTitle, false)
	if dir := titleLanguageDirective(locale); dir != "" {
		sys += " " + dir
	} else {
		sys += " Write the title in the same language as the user's message."
	}
	o.queue.Enqueue("title.generate", func(ctx context.Context) error {
		text, err := o.task.Run(ctx, TaskTitle, sourceText, RunOpts{
			UserID:          userID,
			ConversationID:  convID,
			MaxOutputTokens: titleGenerationOutputTokens,
			SystemPrompt:    sys,
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			if o.logger != nil {
				o.logger.Printf("title generation: task returned no visible text (conv=%s user=%s)", convID, userID)
			}
			return nil
		}
		title := cleanTitle(text)
		if title == "" {
			if o.logger != nil {
				o.logger.Printf("title generation: task output was empty after cleanup (conv=%s user=%s)", convID, userID)
			}
			return nil
		}
		o.persistGeneratedTitle(ctx, convID, userID, title)
		return nil
	})
}

func imageConversationTitle(attachments []Attachment, locale string) string {
	for _, attachment := range attachments {
		if attachment.Kind != "image" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.MimeType)), "image/") {
			continue
		}
		base := filepath.Base(strings.TrimSpace(attachment.Filename))
		name := strings.TrimSuffix(base, filepath.Ext(base))
		if title := clipTitle(name); title != "" {
			return title
		}
	}
	switch promptLocaleKey(locale) {
	case "zh":
		return "图片对话"
	case "zh-Hant":
		return "圖片對話"
	case "ja":
		return "画像の会話"
	case "fr":
		return "Conversation sur une image"
	default:
		return "Image conversation"
	}
}

func imageOnlyTitleSource(answer string) string {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return ""
	}
	runes := []rune(answer)
	if len(runes) > 1200 {
		runes = runes[:1200]
	}
	return "The user sent an image without accompanying text. Label the image topic described in this assistant response:\n" + string(runes)
}

// persistGeneratedTitle couples the asynchronous title write to the realtime
// notification that follows it. Usage logging alone does not prove that a task
// produced a usable, persisted title, so failed writes reach the server log.
func (o *Orchestrator) persistGeneratedTitle(ctx context.Context, convID, userID, title string) bool {
	if _, err := store.UpdateConversation(ctx, o.db, convID, userID, store.ConversationPatch{Title: &title}); err != nil {
		if o.logger != nil {
			o.logger.Printf("title generation: update conversation %s: %v", convID, err)
		}
		return false
	}
	if o.onConversationUpdated != nil {
		o.onConversationUpdated(userID, convID)
	}
	return true
}

func clipTitle(s string) string {
	s = strings.Join(strings.Fields(titleMathContentToPlainText(s)), " ")
	if s == "" {
		return ""
	}
	rs := []rune(s)
	if len(rs) > 28 {
		rs = rs[:28]
	}
	return string(rs)
}

func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'.。．＂")
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	// §6.3: keep titles short. CJK is dense (≤24 runes is plenty); a Western title
	// (≈8 words) needs more room, so clamp higher and back off to a word boundary
	// rather than cutting mid-word.
	limit := 24
	if !hasCJK(s) {
		limit = 56
	}
	rs := []rune(s)
	if len(rs) > limit {
		cut := strings.TrimSpace(string(rs[:limit]))
		if !hasCJK(s) {
			if idx := strings.LastIndexByte(cut, ' '); idx > limit/2 {
				cut = cut[:idx]
			}
		}
		return strings.TrimSpace(cut)
	}
	return strings.TrimSpace(s)
}

// hasCJK reports whether s contains a CJK ideograph, kana, or hangul — used to
// pick a tighter title clamp for dense CJK vs a roomier one for Western text.
func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x3040 && r <= 0x30ff) || // hiragana + katakana
			(r >= 0x3400 && r <= 0x4dbf) || // CJK ext A
			(r >= 0x4e00 && r <= 0x9fff) || // CJK unified
			(r >= 0xf900 && r <= 0xfaff) || // CJK compatibility
			(r >= 0xac00 && r <= 0xd7a3) { // hangul syllables
			return true
		}
	}
	return false
}

// orchToolRunner adapts the tool registry's Run signature to the provider's
// expectation (no ToolContext parameter), threading the orchestrator's
// captured tool context through.
type orchToolRunner struct {
	orch    *Orchestrator
	ctx     *ToolContext
	onEvent func(SseEvent)
}

// toolDefAllowlistRunner binds execution to the exact definitions sent in the
// current provider request. It blocks unsolicited or stale calls even when a
// broader persisted model policy would otherwise allow the tool.
type toolDefAllowlistRunner struct {
	next    ToolRunner
	allowed map[string]bool
}

func (r toolDefAllowlistRunner) Run(ctx context.Context, name string, input []byte) (string, []Citation, error) {
	if !r.allowed[name] {
		return "", nil, fmt.Errorf("tool %q is not enabled for the current model request", name)
	}
	return r.next.Run(ctx, name, input)
}

// toolRunnerForModelRequest retargets the concrete orchestrator runner during a
// TTFT model switch. In particular, use_skill must query bindings for the
// fallback model, not the primary model whose context built the first request.
func toolRunnerForModelRequest(runner ToolRunner, modelID string, definitions []ToolDef) ToolRunner {
	base := runner
	if restricted, ok := runner.(toolDefAllowlistRunner); ok {
		base = restricted.next
	}
	if current, ok := base.(*orchToolRunner); ok && current.ctx != nil {
		source := current.ctx
		var params map[string]any
		if source.ImageRequestParams != nil {
			params = make(map[string]any, len(source.ImageRequestParams))
			for key, value := range source.ImageRequestParams {
				params[key] = value
			}
		}
		fallbackContext := &ToolContext{
			UserID:               source.UserID,
			ConvID:               source.ConvID,
			MessageID:            source.MessageID,
			WorkspaceID:          source.WorkspaceID,
			ModelID:              modelID,
			ProjectID:            source.ProjectID,
			ProjectName:          source.ProjectName,
			DB:                   source.DB,
			WorkspaceAccessCheck: source.WorkspaceAccessCheck,
			DeepResearch:         source.DeepResearch,
			Fast:                 source.Fast,
			BuiltinTools:         toolDefNameSet(definitions),
			AdminSkillIDs:        cloneBoolMap(source.AdminSkillIDs),
			ImageModelID:         source.ImageModelID,
			ImageRequestParams:   params,
			ImageInputIDs:        append([]string(nil), source.ImageInputIDs...),
			ImageUserPrompt:      source.ImageUserPrompt,
			SkipImageQuota:       source.SkipImageQuota,
			ImageBilling:         source.ImageBilling,
			OnArtifact:           source.OnArtifact,
			counts:               map[string]int{},
			citationIndexes:      source.citationIndexes,
		}
		base = &orchToolRunner{orch: current.orch, ctx: fallbackContext, onEvent: current.onEvent}
	}
	return toolDefAllowlistRunner{next: base, allowed: toolDefNameSet(definitions)}
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// toolTimeouts bounds a single tool invocation per tool type (§4.3: search
// 10s / sandbox 120s / image 60s) so one slow tool can't stall the turn.
var toolTimeouts = map[string]time.Duration{
	toolnames.AivoryWebSearch: envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS", 10*time.Second),
	"web_fetch":               envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS_2", 15*time.Second),
	"fetch_image":             envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS_FETCH_IMAGE", 45*time.Second),
	"python_execute":          120 * time.Second,
	"image_generate":          envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS_3", 600*time.Second), // slow third-party image gateways need a wide window
}

var toolTimeoutDefault = envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUT_DEFAULT", 100*time.Second)

// sandboxExecCtxTimeout sizes the per-call ctx for python_execute to the
// admin-configured sandbox exec cap (settings.sandbox_exec_timeout_sec, default
// 120s, clamped [10,600]) PLUS margin, so the ctx outlasts the sandbox HTTP
// client timeout (exec + ~120s overhead) and never cancels a valid long run
// early. Mirrors the clamp in tools.settingsSandbox.execTimeout (kept here
// rather than imported to avoid an llm→tools import cycle via ToolContext).
func sandboxExecCtxTimeout(db *sql.DB) time.Duration {
	secs := 120
	if db != nil {
		if raw, err := store.GetSetting(db, "sandbox_exec_timeout_sec"); err == nil {
			n := 0
			if json.Unmarshal(raw, &n) != nil {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					n, _ = strconv.Atoi(strings.TrimSpace(s))
				}
			}
			if n > sandboxExecTimeoutClampRangeMax {
				secs = sandboxExecTimeoutClampRangeMax
			} else if n >= sandboxExecTimeoutClampRangeMin {
				secs = n
			} else if n > 0 {
				secs = sandboxExecTimeoutClampRangeMin
			}
		}
	}
	return time.Duration(secs)*time.Second + sandboxExecCtxSafetyMargin
}

func (r *orchToolRunner) Run(ctx context.Context, name string, input []byte) (string, []Citation, error) {
	if r.ctx == nil {
		return "", nil, errors.New("tool context unavailable")
	}
	if r.ctx.WorkspaceAccessCheck != nil {
		if err := r.ctx.WorkspaceAccessCheck(ctx); err != nil {
			return "", nil, fmt.Errorf("workspace access revoked before tool execution: %w", err)
		}
	}
	if err := r.ctx.charge(name); err != nil {
		return "", nil, err
	}
	timeout, ok := toolTimeouts[name]
	if !ok {
		timeout = toolTimeoutDefault
	}
	if name == "python_execute" {
		// The sandbox exec cap is admin-configurable (sandbox_exec_timeout_sec,
		// up to 600s); a static 120s ctx here would silently cancel a longer-but-
		// valid run before the sidecar/client deadline. Size the ctx to the
		// configured cap + margin so raising the setting actually takes effect.
		timeout = sandboxExecCtxTimeout(r.orch.db)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, cites, err := r.orch.tools.Run(ctx, name, input, r.ctx)
	if err != nil && r.orch.logger != nil {
		r.orch.logger.Printf("tool execution failed (conv=%s msg=%s tool=%s): %v", r.ctx.ConvID, r.ctx.MessageID, name, err)
	}
	if err == nil && len(cites) > 0 && r.ctx != nil && r.ctx.citationIndexes != nil {
		offset := r.ctx.citationIndexes.allocate(len(cites))
		out = remapCitationMarkers(out, len(cites), offset)
		for i := range cites {
			cites[i].Index = offset + i + 1
			cites[i].GlobalIndex = true
		}
	}
	// Stream tool-sourced citations live (§6.2) from this single choke point so
	// every provider (native + prompt mode) gets them without per-provider code.
	if r.onEvent != nil {
		for _, c := range cites {
			cc := c
			r.onEvent(SseEvent{Type: "citation", Citation: &cc})
		}
	}
	return out, cites, err
}
