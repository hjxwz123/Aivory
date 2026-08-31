// Package tools is the unified self-built tool layer (§4.2). Every tool
// implements Tool; the registry exposes a `List + Run` interface to the
// orchestrator. The orchestrator does not know what the tools are — it just
// hands them inputs and gets back text + citations.
package tools

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/mcp"
	"aivory/server/internal/netsafe"
	"aivory/server/internal/sandbox"
	"aivory/server/internal/store"
)

// Tool is the contract every self-built tool implements.
type Tool interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Execute(ctx context.Context, input []byte, tc *llm.ToolContext) (text string, citations []llm.Citation, err error)
}

// Registry is the global, model-aware tool registry.
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	cfg     config.Config
	db      *sql.DB
	logger  *log.Logger
	sandbox sandbox.Service
	// MCP definitions are discovered administratively and persisted in the store.
	// The synthetic Function name is mapped back to its trusted service + remote
	// method here; configured URL/headers never enter model-controlled input.
	mcpBindings map[string]mcpBinding
	mcpClients  map[string]cachedMCPClient
	// mcpGenerations changes whenever a server is invalidated. It closes the
	// small construction race where an endpoint mutation can otherwise let an
	// old client get written back into the cache after InvalidateMCPServer.
	mcpGenerations map[string]uint64
	// User MCP endpoints use a distinct dial-time policy. Keeping the client on
	// the registry also lets package tests inject a loopback transport without
	// weakening production discovery or execution.
	userMCPHTTPClient *http.Client
}

type mcpBinding struct {
	ServerID   string
	RemoteName string
	UserOwned  bool
	// RuntimeFingerprint binds a model-visible Function declaration to the
	// exact endpoint, credentials, protocol generation, and tools snapshot that
	// produced it. The database row is re-read before execution and must still
	// match, which also closes the stale-binding window across application
	// processes where the in-memory invalidation callback is not delivered.
	RuntimeFingerprint string
	// WorkspaceID is the scope captured when the model-facing definition was
	// built. Empty means the owner's personal library; it must never be inferred
	// from the caller's current conversation at execution time.
	WorkspaceID string
	// OwnerID identifies the user row that created a user-owned MCP. It is used
	// as an additional stale-binding guard for legacy/imported rows.
	OwnerID string
}

type cachedMCPClient struct {
	fingerprint string
	client      *mcp.Client
	serverID    string
	userOwned   bool
	userID      string
}

// Sandbox exposes the settings-wrapped sandbox backend so admin endpoints can
// inspect / clear a conversation's workspace.
func (r *Registry) Sandbox() sandbox.Service { return r.sandbox }

// NewRegistry builds the default registry with the built-in tools.
func NewRegistry(db *sql.DB, cfg config.Config, logger *log.Logger) *Registry {
	r := &Registry{
		tools: map[string]Tool{}, cfg: cfg, db: db, logger: logger,
		mcpBindings:       map[string]mcpBinding{},
		mcpClients:        map[string]cachedMCPClient{},
		mcpGenerations:    map[string]uint64{},
		userMCPHTTPClient: netsafe.UserMCPAllowedClient(mcp.DefaultTimeout),
	}
	// Sandbox config comes from admin settings (sandbox_base_url /
	// sandbox_api_key), re-read per call, with env as the fallback default.
	sb := newSettingsSandbox(db, cfg.SandboxBaseURL, cfg.SandboxAPIKey)
	r.sandbox = sb
	r.Register(&webSearchTool{cfg: cfg, searcher: newSettingsSearcher(db, cfg.SearchProvider, cfg.SearchAPIKey, cfg.SearchBaseURL)})
	r.Register(&webFetchTool{})
	r.Register(&fetchImageTool{sandbox: sb, logger: logger})
	r.Register(&pythonExecuteTool{sandbox: sb, uploadDir: cfg.UploadDir, artifactDir: cfg.ArtifactDir, logger: logger})
	r.Register(&imageGenerateTool{db: db, uploadDir: cfg.UploadDir, artifactDir: cfg.ArtifactDir, logger: logger})
	r.Register(&useSkillTool{db: db})
	r.Register(&saveMemoryTool{db: db})
	return r
}

// Register adds or replaces a tool.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	r.tools[t.Name()] = t
	r.mu.Unlock()
}

// List returns every currently usable registered tool definition. The
// orchestrator applies the loaded model's allowlist and global kill-switch.
// Tools with unavailable runtime dependencies are withheld so models cannot
// select an operation that cannot execute. The list is sorted for deterministic
// serialization.
func (r *Registry) List(_ string) []llm.ToolDef {
	return r.listRegistered(false)
}

// ListRegistered includes tools whose runtime dependency is unavailable. It is
// used by the admin capability page so an unavailable tool remains visible and
// can explain what must be configured.
func (r *Registry) ListRegistered() []llm.ToolDef {
	return r.listRegistered(true)
}

// ImageGenerationConfigured reports whether the local image_generate tool has
// an enabled image model to execute with. A nil database is treated as unknown
// rather than unavailable so isolated registries used by tests and maintenance
// callers retain their explicitly registered tools.
func (r *Registry) ImageGenerationConfigured() bool {
	return r.imageGenerationConfigured(context.Background())
}

func (r *Registry) imageGenerationConfigured(ctx context.Context) bool {
	if r == nil || r.db == nil {
		return true
	}
	models, err := store.ListModels(ctx, r.db, "image", true)
	return err == nil && len(models) > 0
}

func (r *Registry) listRegistered(includeUnavailable bool) []llm.ToolDef {
	sandboxEnabled := r.sandbox != nil && r.sandbox.Enabled()
	imageGenerationEnabled := r.ImageGenerationConfigured()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []llm.ToolDef{}
	for _, t := range r.tools {
		if !includeUnavailable && t.Name() == "python_execute" && !sandboxEnabled {
			continue
		}
		if !includeUnavailable && t.Name() == "image_generate" && !imageGenerationEnabled {
			continue
		}
		out = append(out, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type mcpServerSnapshot struct {
	ID              string
	WorkspaceID     string
	OwnerID         string
	Name            string
	Icon            string
	Description     string
	Headers         map[string]string
	Enabled         bool
	DiscoveredTools json.RawMessage
	ProtocolVersion string
	LastSyncedAt    int64
	URL             string
	UserOwned       bool
	OwnerExempt     bool
}

type mcpBindingSource struct {
	serverID    string
	userOwned   bool
	workspaceID string
	ownerID     string
}

// ListMCP returns persisted tools/list snapshots for every enabled administrator
// MCP service plus the caller's personal and active-workspace user services.
// Discovery is explicit, so chat turns never block on remote schema discovery.
// Bindings are merged under one lock: concurrent turns for different users must
// not replace each other's private Function routes.
func (r *Registry) ListMCP(_ string, userID string, workspaceID string) []llm.MCPToolDef {
	if r.db == nil {
		return []llm.MCPToolDef{}
	}
	ctx := context.Background()
	userID = strings.TrimSpace(userID)
	workspaceID = strings.TrimSpace(workspaceID)
	var workspacePolicy *store.WorkspacePolicy
	// A workspace-scoped declaration must honor the same capability boundary as
	// execution. This is presentation-only (Run re-checks the policy), but it
	// prevents stale or forbidden MCP functions from entering a model request in
	// the first place. Calls with no workspace scope are used by the admin tool
	// catalog to enumerate official services and intentionally stay unscoped.
	if workspaceID != "" {
		if userID == "" {
			return []llm.MCPToolDef{}
		}
		policy, policyErr := store.GetWorkspacePolicy(ctx, r.db, workspaceID)
		if policyErr != nil {
			if r.logger != nil {
				r.logger.Printf("resolve workspace MCP policy (workspace=%q): %v", workspaceID, policyErr)
			}
			return []llm.MCPToolDef{}
		}
		member, memberErr := store.GetWorkspaceForMember(ctx, r.db, workspaceID, userID)
		if memberErr != nil || member == nil {
			if r.logger != nil && memberErr != nil {
				r.logger.Printf("resolve workspace MCP membership (workspace=%q user=%q): %v", workspaceID, userID, memberErr)
			}
			return []llm.MCPToolDef{}
		}
		if !policy.AllowToolCalling || !policy.AllowMCP || !member.CanUseMCP {
			return []llm.MCPToolDef{}
		}
		workspacePolicy = &policy
	}
	adminServers, err := store.ListMCPServers(ctx, r.db, false)
	if err != nil {
		if r.logger != nil {
			r.logger.Printf("list enabled MCP services: %v", err)
		}
		return []llm.MCPToolDef{}
	}
	servers := make([]mcpServerSnapshot, 0, len(adminServers))
	for _, server := range adminServers {
		if workspacePolicy != nil && workspacePolicy.ToolDeniedByPolicy("mcp:"+server.ID) {
			// The model-facing registry must apply the same per-server workspace
			// allowlist as the HTTP catalog. Runtime checks still repeat this gate,
			// but omitting denied definitions avoids predictable provider/tool-call
			// failures and prevents a stale selection from being advertised.
			continue
		}
		servers = append(servers, mcpServerSnapshot{
			ID: server.ID, Name: server.Name, Icon: server.Icon, Description: server.Description,
			URL: server.URL, Headers: server.Headers,
			Enabled: server.Enabled, DiscoveredTools: server.DiscoveredTools,
			ProtocolVersion: server.ProtocolVersion, LastSyncedAt: server.LastSyncedAt,
		})
	}

	if userID != "" {
		scopes := []string{""}
		if workspaceID != "" {
			scopes = append(scopes, workspaceID)
		}
		for _, scope := range scopes {
			userServers, listErr := store.ListUserMCPServersScoped(ctx, r.db, userID, scope)
			if listErr != nil {
				if r.logger != nil {
					r.logger.Printf("list scoped user MCP services (user=%q workspace=%q): %v", userID, scope, listErr)
				}
				continue
			}
			for _, server := range userServers {
				servers = append(servers, mcpServerSnapshot{
					ID: server.ID, WorkspaceID: server.WorkspaceID, OwnerID: server.UserID,
					Name: server.Name, Icon: server.Icon, Description: server.Description,
					URL: server.URL, Headers: server.Headers,
					Enabled: server.Enabled, DiscoveredTools: server.DiscoveredTools,
					ProtocolVersion: server.ProtocolVersion, LastSyncedAt: server.LastSyncedAt,
					UserOwned: true, OwnerExempt: server.UserID == userID,
				})
			}
		}
	}

	return r.mergeMCPDefinitions(servers)
}

func (r *Registry) mergeMCPDefinitions(servers []mcpServerSnapshot) []llm.MCPToolDef {
	r.mu.Lock()
	defer r.mu.Unlock()

	refreshed := make(map[mcpBindingSource]struct{}, len(servers))
	for _, server := range servers {
		refreshed[mcpBindingSource{
			serverID: server.ID, userOwned: server.UserOwned,
			workspaceID: server.WorkspaceID, ownerID: server.OwnerID,
		}] = struct{}{}
	}
	for functionName, binding := range r.mcpBindings {
		if _, ok := refreshed[mcpBindingSource{
			serverID: binding.ServerID, userOwned: binding.UserOwned,
			workspaceID: binding.WorkspaceID, ownerID: binding.OwnerID,
		}]; ok {
			delete(r.mcpBindings, functionName)
		}
	}

	out := []llm.MCPToolDef{}
	usedFunctionNames := make(map[string]struct{})
	for name := range r.tools {
		usedFunctionNames[name] = struct{}{}
	}
	for name := range r.mcpBindings {
		usedFunctionNames[name] = struct{}{}
	}
	// The same database id can legally exist in the administrator and
	// user-owned tables. Include the complete source identity in the de-dup key;
	// using only server id + remote name would silently drop one tenant's tool
	// when an imported/legacy row happens to reuse an administrator id.
	seenRemoteTools := make(map[string]struct{})
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		var remoteTools []mcp.Tool
		if err := json.Unmarshal(server.DiscoveredTools, &remoteTools); err != nil {
			if r.logger != nil {
				r.logger.Printf("decode MCP tool snapshot for %q: %v", server.ID, err)
			}
			continue
		}
		// Snapshots can come from an older database/backup that predates API-side
		// redaction. Sanitize again before schema text reaches an LLM provider.
		remoteTools = redactMCPToolSnapshotMetadata(remoteTools, server.Headers)
		for _, remote := range remoteTools {
			remote.Name = strings.TrimSpace(remote.Name)
			if remote.Name == "" || !validMCPInputSchema(remote.InputSchema) {
				continue
			}
			remoteKey := fmt.Sprintf("%t\x00%s\x00%s\x00%s\x00%s",
				server.UserOwned, server.ID, server.WorkspaceID, server.OwnerID, remote.Name)
			if _, exists := seenRemoteTools[remoteKey]; exists {
				continue
			}
			seenRemoteTools[remoteKey] = struct{}{}
			// Function names are model-visible. A hostile endpoint can put a
			// configured request-header value in its remote name, so derive the
			// readable portion from a redacted copy while retaining the original
			// protocol name in the binding used by tools/call.
			functionName, ok := reserveMCPFunctionName(
				mcpFunctionDeclarationIdentity(server), redactMCPText(remote.Name, server.Headers), usedFunctionNames,
			)
			if !ok {
				if r.logger != nil {
					r.logger.Printf("allocate Function name for MCP tool %q on %q", remote.Name, server.ID)
				}
				continue
			}
			usedFunctionNames[functionName] = struct{}{}
			description := strings.TrimSpace(remote.Description)
			if description == "" {
				description = strings.TrimSpace(remote.Title)
			}
			if description == "" {
				description = "Use " + server.Name + " for this operation."
			}
			out = append(out, llm.MCPToolDef{
				ToolDef: llm.ToolDef{
					Name: functionName, Description: description,
					InputSchema: append(json.RawMessage(nil), remote.InputSchema...),
				},
				ServerID: server.ID, DisplayName: server.Name,
				DisplayDescription: server.Description, Icon: server.Icon,
				UserOwned: server.UserOwned, OwnerExempt: server.OwnerExempt,
			})
			r.mcpBindings[functionName] = mcpBinding{
				ServerID: server.ID, RemoteName: remote.Name, UserOwned: server.UserOwned,
				WorkspaceID: server.WorkspaceID, OwnerID: server.OwnerID,
				RuntimeFingerprint: mcpSnapshotRuntimeFingerprint(server),
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServerID == out[j].ServerID {
			return out[i].Name < out[j].Name
		}
		return out[i].DisplayName < out[j].DisplayName
	})
	return out
}

func mcpSnapshotRuntimeFingerprint(server mcpServerSnapshot) string {
	return mcpRuntimeFingerprint(&store.MCPServer{
		ID: server.ID, URL: server.URL, Headers: server.Headers, Enabled: server.Enabled,
		DiscoveredTools: server.DiscoveredTools, ProtocolVersion: server.ProtocolVersion,
		LastSyncedAt: server.LastSyncedAt,
	})
}

// mcpFunctionDeclarationIdentity is hashed into the provider-visible Function
// name. The runtime fingerprint makes every declaration immutable: after an
// endpoint, credential, protocol, or snapshot change, a refresh gets a new name
// and the previous in-flight name is removed instead of being rebound to the
// new configuration. Neither the source identity nor fingerprint leaves the
// process directly.
func mcpFunctionDeclarationIdentity(server mcpServerSnapshot) string {
	sourceIdentity := mcpFunctionSourceIdentity(server)
	runtimeFingerprint := mcpSnapshotRuntimeFingerprint(server)
	return fmt.Sprintf("%d:%s:%d:%s",
		len(sourceIdentity), sourceIdentity,
		len(runtimeFingerprint), runtimeFingerprint,
	)
}

// mcpFunctionSourceIdentity distinguishes trusted database sources before the
// declaration fingerprint is added. Administrator and user MCP tables can
// legally contain the same server id, and restored rows may reuse ids across
// historical user/workspace scopes.
func mcpFunctionSourceIdentity(server mcpServerSnapshot) string {
	if !server.UserOwned {
		return server.ID
	}
	return fmt.Sprintf("usermcp:%d:%s:%d:%s:%d:%s",
		len(server.ID), server.ID,
		len(server.WorkspaceID), server.WorkspaceID,
		len(server.OwnerID), server.OwnerID,
	)
}

// InvalidateMCPServer removes every synthetic Function binding and cached MCP
// session for a service. Mutation handlers call it after endpoint, credential,
// enabled-state, snapshot, or deletion changes so an in-flight stale declaration
// fails closed before any remote request.
func (r *Registry) InvalidateMCPServer(serverID string) {
	if r == nil || strings.TrimSpace(serverID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Increment before removing the cache entries. A concurrent mcpClient call
	// that started with the previous generation will observe the mismatch before
	// it can publish its newly constructed client.
	if r.mcpGenerations == nil {
		r.mcpGenerations = make(map[string]uint64)
	}
	generation := r.mcpGenerations[serverID]
	if generation == ^uint64(0) {
		// Wraparound is extraordinarily unlikely, but retaining a non-zero epoch
		// avoids accidentally accepting a client from generation zero after an
		// unbounded sequence of invalidations.
		generation = 1
	} else {
		generation++
	}
	r.mcpGenerations[serverID] = generation
	for functionName, binding := range r.mcpBindings {
		if binding.ServerID == serverID {
			delete(r.mcpBindings, functionName)
		}
	}
	for cacheKey, cached := range r.mcpClients {
		if cached.serverID == serverID {
			delete(r.mcpClients, cacheKey)
		}
	}
}

// mcpFunctionName stays inside the conservative provider intersection:
// [A-Za-z0-9_-], <=64 bytes. A digest makes duplicate remote names across MCP
// services collision-safe without exposing database ids to the model.
func mcpFunctionName(serverID, remoteName string) string {
	return mcpFunctionNameAttempt(serverID, remoteName, 0)
}

func mcpFunctionNameAttempt(serverID, remoteName string, attempt int) string {
	digestInput := serverID + "\x00" + remoteName
	if attempt > 0 {
		digestInput += fmt.Sprintf("\x00%d", attempt)
	}
	digest := sha256.Sum256([]byte(digestInput))
	suffix := fmt.Sprintf("%x", digest[:6])
	var cleaned strings.Builder
	for _, char := range remoteName {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '-' {
			cleaned.WriteRune(char)
		} else {
			cleaned.WriteByte('_')
		}
	}
	base := strings.Trim(cleaned.String(), "_-")
	if base == "" {
		base = "tool"
	}
	const prefix = "mcp_"
	maxBase := 64 - len(prefix) - 1 - len(suffix)
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return prefix + base + "_" + suffix
}

func reserveMCPFunctionName(serverID, remoteName string, used map[string]struct{}) (string, bool) {
	const maxAttempts = 1024
	for attempt := 0; attempt < maxAttempts; attempt++ {
		candidate := mcpFunctionNameAttempt(serverID, remoteName, attempt)
		if _, exists := used[candidate]; !exists {
			return candidate, true
		}
	}
	return "", false
}

func validMCPInputSchema(raw json.RawMessage) bool {
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		return false
	}
	typeJSON, hasType := schema["type"]
	if !hasType {
		return true
	}
	var schemaType string
	return json.Unmarshal(typeJSON, &schemaType) == nil && schemaType == "object"
}

// Run executes a tool by name.
func (r *Registry) Run(ctx context.Context, name string, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	// A nil context has no authenticated user, workspace, model declaration, or
	// budget. Treat it as an invalid execution request instead of allowing a
	// direct caller to accidentally run a private MCP endpoint (or panic while
	// checking the model's tool map).
	if tc == nil {
		return "", nil, errors.New("tool context unavailable")
	}
	// context.WithTimeout (used by MCP transports) panics when given a nil
	// parent. Fail closed here so direct registry callers cannot turn a malformed
	// execution request into a process-level panic or bypass cancellation.
	if ctx == nil {
		return "", nil, errors.New("tool execution context unavailable")
	}
	// Normal chat turns are checked once by orchToolRunner, but direct image
	// turns call the registry directly. Keep this final callback at the registry
	// boundary so every caller revalidates conversation membership/visibility
	// immediately before execution. The callback is a pure authorization check;
	// callers may safely invoke it more than once when they have an outer guard.
	if tc.WorkspaceAccessCheck != nil {
		if err := tc.WorkspaceAccessCheck(ctx); err != nil {
			return "", nil, fmt.Errorf("workspace access revoked before tool execution: %w", err)
		}
	}
	r.mu.RLock()
	t, ok := r.tools[name]
	binding, mcpOK := r.mcpBindings[name]
	r.mu.RUnlock()
	if !ok && !mcpOK {
		return "", nil, errors.New("unknown tool: " + name)
	}
	// BuiltinTools is the exact Function declaration allowlist for this model
	// request. It includes flattened MCP functions as well as local builtins, so
	// retain the check for both categories and reject stale/unsolicited calls
	// before any policy or remote lookup runs.
	if !tc.AllowsBuiltinTool(name) {
		return "", nil, errors.New("tool is not enabled for this model: " + name)
	}
	if err := r.checkCurrentToolAccess(ctx, name, binding, mcpOK, tc); err != nil {
		return "", nil, err
	}
	if ok {
		return t.Execute(ctx, input, tc)
	}
	return r.runMCP(ctx, binding, input, tc)
}

// checkCurrentToolAccess is the last authorization boundary before a local or
// MCP tool executes. A turn's declarations are intentionally a snapshot, but a
// global switch, group policy, drawing permission, or memory setting may be
// revoked while the model is deciding which tool to call. Re-read those values
// here so a stale declaration can never outlive the current server policy.
func (r *Registry) checkCurrentToolAccess(
	ctx context.Context,
	name string,
	binding mcpBinding,
	isMCP bool,
	tc *llm.ToolContext,
) error {
	if tc == nil {
		return errors.New("tool context unavailable")
	}
	if !isMCP && name == "python_execute" && (r.sandbox == nil || !r.sandbox.Enabled()) {
		return errors.New("python_execute is unavailable: sandbox is not configured")
	}
	if !isMCP && name == "image_generate" && !r.imageGenerationConfigured(ctx) {
		return errors.New("image_generate is unavailable: no image model is configured")
	}
	if r.db == nil {
		return nil
	}
	// Workspace capability switches are checked for both local and MCP tools
	// before any owner exemption is considered. runMCP repeats this check after
	// loading the authoritative row to close a policy-change race.
	if err := r.checkWorkspaceToolAccess(ctx, name, binding, isMCP, tc); err != nil {
		return err
	}
	if isMCP {
		// The scoped server row is needed to determine whether this caller created
		// the service. loadRuntimeMCPServer applies the group policy after it reads
		// that row, so a teammate-owned workspace MCP cannot inherit the owner's
		// exemption.
		return nil
	}
	if strings.TrimSpace(tc.UserID) == "" {
		// Personal/internal callers may use the registry without a database user;
		// workspace-scoped calls were already rejected above because they need an
		// authenticated member. Preserve the historical behavior for built-ins.
		return nil
	}
	permissions, err := store.UserGroupPermissionsForUser(ctx, r.db, tc.UserID)
	if err != nil {
		return fmt.Errorf("resolve current tool permission: %w", err)
	}
	// The dedicated image-model pipeline invokes image_generate internally. It
	// is a drawing operation, not a user-selectable model tool, so do not make it
	// depend on the member's ordinary tool-selection list. Its independent
	// drawing capability is still checked below (and workspace policy is checked
	// by checkWorkspaceToolAccess).
	directDrawing := !isMCP && name == "image_generate" && tc.DirectImageTurn
	// MCP calls have a separate group-policy check below because the requester
	// may be the owner of a user MCP service. Keep the owner exemption in one
	// place; applying the generic `mcp:` check here first would reject an
	// owner's `usermcp:` call before checkMCPGroupAccess can grant it.
	toolID := "builtin:" + name
	if !isMCP && !directDrawing && !store.ResourcePolicyAllows(permissions.Tools, toolID) {
		return errors.New("tool is no longer allowed for this user: " + name)
	}
	if isMCP {
		// runMCP performs the authoritative enabled-state lookup immediately
		// after this check and before making any network request.
		return nil
	}
	if raw, settingErr := store.GetSetting(r.db, "disabled_tools"); settingErr == nil && len(raw) > 0 {
		if disabled, _, parseErr := store.ParseBuiltinTools(raw); parseErr == nil {
			for _, disabledName := range disabled {
				if disabledName == name {
					return errors.New("tool is disabled by the administrator: " + name)
				}
			}
		}
	}
	switch name {
	case "image_generate":
		if !permissions.AllowDrawing {
			return errors.New("drawing is no longer allowed for this user")
		}
	case "save_memory":
		if !store.MemoryEnabledForUser(ctx, r.db, tc.UserID) {
			return errors.New("memory is disabled")
		}
	case "use_skill":
		if permissions.Skills.Mode == store.ResourceAccessNone {
			return errors.New("skills are no longer allowed for this user")
		}
	}
	return nil
}

// checkWorkspaceToolAccess is the workspace-wide execution gate. It is kept in
// the tools package so direct registry callers receive the same protection as
// HTTP turns, and it deliberately knows nothing about the member's group tool
// selection or the user-MCP owner exemption.
func (r *Registry) checkWorkspaceToolAccess(
	ctx context.Context,
	name string,
	binding mcpBinding,
	isMCP bool,
	tc *llm.ToolContext,
) error {
	if r == nil || r.db == nil || tc == nil || strings.TrimSpace(tc.WorkspaceID) == "" {
		return nil
	}
	if strings.TrimSpace(tc.UserID) == "" {
		return errors.New("workspace tool execution requires an authenticated user")
	}
	policy, err := store.GetWorkspacePolicy(ctx, r.db, tc.WorkspaceID)
	if err != nil {
		return fmt.Errorf("resolve workspace tool policy: %w", err)
	}
	directDrawing := !isMCP && name == "image_generate" && tc.DirectImageTurn
	if !policy.AllowToolCalling && !directDrawing {
		return errors.New("tool calling is disabled for this workspace")
	}
	if directDrawing && !policy.AllowDrawing {
		return errors.New("drawing is disabled for this workspace")
	}
	if isMCP {
		if !policy.AllowMCP {
			return errors.New("MCP is disabled for this workspace")
		}
		// The legacy per-server allowlist applies to administrator MCP services.
		// User-owned services are governed by the explicit workspace MCP switch
		// and member capability; an administrator cannot enumerate a member's
		// private server ids in this list.
		if !binding.UserOwned && policy.ToolDeniedByPolicy("mcp:"+binding.ServerID) {
			return errors.New("MCP service is not allowed in this workspace")
		}
	} else if !directDrawing {
		if policy.ToolDeniedByPolicy("builtin:" + name) {
			return errors.New("tool is not allowed in this workspace: " + name)
		}
		if name == "use_skill" && !policy.AllowSkills {
			return errors.New("skills are disabled for this workspace")
		}
	}
	member, err := store.GetWorkspaceForMember(ctx, r.db, tc.WorkspaceID, tc.UserID)
	if err != nil {
		return fmt.Errorf("resolve workspace member tool permission: %w", err)
	}
	if isMCP && !member.CanUseMCP {
		return errors.New("MCP use is not allowed for this workspace member")
	}
	if !isMCP && name == "use_skill" && !member.CanUseSkills {
		return errors.New("skill use is not allowed for this workspace member")
	}
	return nil
}

// checkMCPGroupAccess applies the member's group tool policy after the
// authoritative MCP row has been loaded. A creator may use their own user MCP
// even when the group selected/none list excludes it, but this exemption is
// intentionally limited to that group list and never reaches workspace gates.
func (r *Registry) checkMCPGroupAccess(
	ctx context.Context,
	binding mcpBinding,
	ownerID string,
	tc *llm.ToolContext,
) error {
	if r == nil || r.db == nil || tc == nil || strings.TrimSpace(tc.UserID) == "" {
		return nil
	}
	permissions, err := store.UserGroupPermissionsForUser(ctx, r.db, tc.UserID)
	if err != nil {
		return fmt.Errorf("resolve current user MCP permission: %w", err)
	}
	if binding.UserOwned && strings.TrimSpace(ownerID) == strings.TrimSpace(tc.UserID) {
		return nil
	}
	id := "mcp:" + binding.ServerID
	if binding.UserOwned {
		id = "usermcp:" + binding.ServerID
	}
	if !store.ResourcePolicyAllows(permissions.Tools, id) {
		return errors.New("user MCP service is no longer allowed for this user")
	}
	return nil
}

var errMCPServerChanged = errors.New("MCP service configuration changed; retry the tool call")

func (r *Registry) runMCP(ctx context.Context, binding mcpBinding, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	if r.db == nil {
		return "", nil, errors.New("MCP tools are unavailable")
	}
	server, callerUserID, err := r.loadRuntimeMCPServer(ctx, binding, tc)
	if err != nil {
		return "", nil, err
	}
	if !server.Enabled {
		return "", nil, errors.New("MCP service is disabled")
	}
	if !mcpSnapshotContainsTool(server.DiscoveredTools, binding.RemoteName) {
		return "", nil, errors.New("MCP tool is no longer available")
	}
	client, generation, err := r.mcpClient(server, binding, callerUserID, tc.WorkspaceID)
	if err != nil {
		return "", nil, redactMCPError(err, server.Headers)
	}
	// The row and policy were read before the client lookup. Read them once more
	// immediately before the network call so a metadata/snapshot update that did
	// not reach the in-process invalidator (for example, another process or a
	// maintenance job) cannot route a stale binding to a newly configured endpoint.
	if err := r.verifyMCPClientState(ctx, binding, tc, server, callerUserID, generation); err != nil {
		r.invalidateMCPClient(binding, callerUserID, tc.WorkspaceID, client)
		return "", nil, err
	}
	result, err := client.CallTool(ctx, binding.RemoteName, json.RawMessage(input))
	if err != nil {
		// A cached client may hold a legacy MCP session id that the remote has
		// discarded (or it may have observed a transient transport failure). Do
		// not poison every later call with that stale state; remove only the exact
		// client instance used by this request so a concurrent replacement wins.
		r.invalidateMCPClient(binding, callerUserID, tc.WorkspaceID, client)
		return "", nil, redactMCPError(err, server.Headers)
	}
	text := strings.TrimSpace(redactMCPText(result.TextContent(), server.Headers))
	if result.IsError {
		if text == "" {
			text = "remote tool reported an error"
		}
		return "", nil, errors.New(text)
	}

	citations := []llm.Citation{}
	for _, content := range result.Content {
		resourceURL := strings.TrimSpace(content.URI)
		title := strings.TrimSpace(redactMCPText(content.Name, server.Headers))
		snippet := strings.TrimSpace(redactMCPText(content.Text, server.Headers))
		if content.Resource != nil {
			resourceURL = strings.TrimSpace(content.Resource.URI)
			if snippet == "" {
				snippet = strings.TrimSpace(redactMCPText(content.Resource.Text, server.Headers))
			}
		}
		if redactMCPText(resourceURL, server.Headers) != resourceURL {
			continue
		}
		parsed, parseErr := url.Parse(resourceURL)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			continue
		}
		if title == "" {
			title = parsed.Host
		}
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("mcp_%d", len(citations)+1), Index: len(citations) + 1,
			Title: title, URL: resourceURL, Snippet: snippet, Source: "web",
		})
	}
	if text == "" {
		return "", nil, errors.New("MCP tool returned no supported text or structured content")
	}
	return text, citations, nil
}

func (r *Registry) verifyMCPClientState(
	ctx context.Context,
	binding mcpBinding,
	tc *llm.ToolContext,
	expected *store.MCPServer,
	expectedCallerID string,
	expectedGeneration uint64,
) error {
	if r == nil || expected == nil {
		return errMCPServerChanged
	}
	// The declaration carries the endpoint/snapshot fingerprint captured by
	// ListMCP. Reject a row that was changed by another process before even
	// comparing the in-memory generation; otherwise a stale Function could be
	// transparently rebound to a newly configured endpoint with the same id.
	if !mcpBindingMatchesRuntime(binding, expected) {
		return errMCPServerChanged
	}
	if r.mcpServerGeneration(expected.ID) != expectedGeneration {
		return errMCPServerChanged
	}
	current, currentCallerID, err := r.loadRuntimeMCPServer(ctx, binding, tc)
	if err != nil {
		return err
	}
	if currentCallerID != expectedCallerID || !sameMCPRuntimeState(expected, current) {
		return errMCPServerChanged
	}
	// Check the epoch again after the database read. Invalidation can happen
	// while the query is in flight; accepting that result would reintroduce the
	// very client this generation guard is intended to retire.
	if r.mcpServerGeneration(expected.ID) != expectedGeneration {
		return errMCPServerChanged
	}
	return nil
}

func sameMCPRuntimeState(left, right *store.MCPServer) bool {
	if left == nil || right == nil {
		return false
	}
	return left.ID == right.ID && left.URL == right.URL &&
		left.Enabled == right.Enabled &&
		left.ProtocolVersion == right.ProtocolVersion &&
		left.LastSyncedAt == right.LastSyncedAt &&
		mcpStringMapsEqual(left.Headers, right.Headers) &&
		mcpJSONValuesEqual(left.DiscoveredTools, right.DiscoveredTools)
}

func mcpStringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func mcpJSONValuesEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(strings.NewReader(string(left)))
	rightDecoder := json.NewDecoder(strings.NewReader(string(right)))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftErr := leftDecoder.Decode(&leftValue); leftErr != nil {
		return strings.TrimSpace(string(left)) == strings.TrimSpace(string(right))
	}
	if rightErr := rightDecoder.Decode(&rightValue); rightErr != nil {
		return strings.TrimSpace(string(left)) == strings.TrimSpace(string(right))
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func (r *Registry) loadRuntimeMCPServer(
	ctx context.Context,
	binding mcpBinding,
	tc *llm.ToolContext,
) (*store.MCPServer, string, error) {
	if !binding.UserOwned {
		server, err := store.GetMCPServer(ctx, r.db, binding.ServerID)
		if err != nil {
			return nil, "", fmt.Errorf("load MCP service: %w", err)
		}
		if err := r.checkWorkspaceToolAccess(ctx, "", binding, true, tc); err != nil {
			return nil, "", err
		}
		if err := r.checkMCPGroupAccess(ctx, binding, "", tc); err != nil {
			return nil, "", err
		}
		return server, "", nil
	}
	if tc == nil || strings.TrimSpace(tc.UserID) == "" {
		return nil, "", errors.New("user MCP service requires an authenticated tool context")
	}
	// Resolve exactly the scope captured by ListMCP. Falling back from a
	// workspace row to a personal row (or vice versa) would let a stale function
	// declaration cross tenant/workspace boundaries when ids are reused by a
	// restore or migration.
	bindingWorkspaceID := strings.TrimSpace(binding.WorkspaceID)
	if bindingWorkspaceID != "" && strings.TrimSpace(tc.WorkspaceID) != bindingWorkspaceID {
		return nil, "", errors.New("user MCP service is unavailable for this workspace")
	}
	scoped, err := store.GetUserMCPServerScoped(ctx, r.db, binding.ServerID, tc.UserID, bindingWorkspaceID)
	if errors.Is(err, store.ErrNotFound) {
		if bindingWorkspaceID != "" {
			return nil, "", errors.New("user MCP service is unavailable for this workspace")
		}
		return nil, "", errors.New("user MCP service is unavailable for this user or workspace")
	}
	if err != nil {
		return nil, "", fmt.Errorf("load user MCP service: %w", err)
	}
	if scoped == nil {
		return nil, "", errors.New("user MCP service is unavailable for this user or workspace")
	}
	if strings.TrimSpace(binding.OwnerID) == "" ||
		strings.TrimSpace(scoped.UserID) != strings.TrimSpace(binding.OwnerID) {
		return nil, "", errors.New("user MCP service is unavailable for this user or workspace")
	}
	if strings.TrimSpace(scoped.WorkspaceID) != bindingWorkspaceID {
		return nil, "", errors.New("user MCP service is unavailable for this workspace")
	}
	if err := r.checkWorkspaceToolAccess(ctx, "", binding, true, tc); err != nil {
		return nil, "", err
	}
	if err := r.checkMCPGroupAccess(ctx, binding, scoped.UserID, tc); err != nil {
		return nil, "", err
	}
	runtimeServer := &store.MCPServer{
		ID: scoped.ID, Name: scoped.Name, Icon: scoped.Icon, Description: scoped.Description,
		URL: scoped.URL, Headers: scoped.Headers, Enabled: scoped.Enabled,
		DiscoveredTools: scoped.DiscoveredTools, ProtocolVersion: scoped.ProtocolVersion,
		LastError: scoped.LastError, LastSyncedAt: scoped.LastSyncedAt,
		CreatedAt: scoped.CreatedAt, UpdatedAt: scoped.UpdatedAt,
	}
	return runtimeServer, tc.UserID, nil
}

func mcpBindingMatchesRuntime(binding mcpBinding, server *store.MCPServer) bool {
	// A zero fingerprint exists only in package-local legacy tests that inject a
	// binding directly. Every declaration produced by ListMCP has a fingerprint.
	return binding.RuntimeFingerprint == "" || binding.RuntimeFingerprint == mcpRuntimeFingerprint(server)
}

func mcpRuntimeFingerprint(server *store.MCPServer) string {
	if server == nil {
		return ""
	}
	headerJSON, _ := json.Marshal(server.Headers)
	digest := sha256.New()
	for _, value := range []string{
		server.ID,
		server.URL,
		string(headerJSON),
		fmt.Sprintf("%t", server.Enabled),
		server.ProtocolVersion,
		fmt.Sprintf("%d", server.LastSyncedAt),
		string(server.DiscoveredTools),
	} {
		_, _ = digest.Write([]byte(fmt.Sprintf("%d:", len(value))))
		_, _ = digest.Write([]byte(value))
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func mcpSnapshotContainsTool(raw json.RawMessage, remoteName string) bool {
	var tools []mcp.Tool
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	remoteName = strings.TrimSpace(remoteName)
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == remoteName && validMCPInputSchema(tool.InputSchema) {
			return true
		}
	}
	return false
}

const mcpRuntimeSecretMask = "[redacted]"

// redactMCPToolSnapshotMetadata protects the model-facing side of the
// tools/list contract. API discovery sanitizes fresh snapshots, but a restored
// or legacy row may still contain a configured request-header value in a title,
// description, JSON Schema, annotation, icon URL, or extension metadata.
// Name remains untouched because it is the protocol routing key stored in the
// binding; all other metadata is recursively sanitized.
func redactMCPToolSnapshotMetadata(tools []mcp.Tool, headers map[string]string) []mcp.Tool {
	if len(tools) == 0 || len(headers) == 0 {
		return tools
	}
	redacted := append([]mcp.Tool(nil), tools...)
	for index := range redacted {
		tool := &redacted[index]
		tool.Title = redactMCPText(tool.Title, headers)
		tool.Description = redactMCPText(tool.Description, headers)
		var safe bool
		if tool.InputSchema, safe = redactMCPJSONSnapshot(tool.InputSchema, headers); !safe {
			return nil
		}
		if tool.OutputSchema, safe = redactMCPJSONSnapshot(tool.OutputSchema, headers); !safe {
			return nil
		}
		if tool.Annotations, safe = redactMCPJSONSnapshot(tool.Annotations, headers); !safe {
			return nil
		}
		if tool.Meta, safe = redactMCPJSONSnapshot(tool.Meta, headers); !safe {
			return nil
		}
		if len(tool.Icons) > 0 {
			tool.Icons = append([]mcp.Icon(nil), tool.Icons...)
		}
		for iconIndex := range tool.Icons {
			tool.Icons[iconIndex].Source = redactMCPText(tool.Icons[iconIndex].Source, headers)
			tool.Icons[iconIndex].MimeType = redactMCPText(tool.Icons[iconIndex].MimeType, headers)
			tool.Icons[iconIndex].Sizes = append([]string(nil), tool.Icons[iconIndex].Sizes...)
			for sizeIndex := range tool.Icons[iconIndex].Sizes {
				tool.Icons[iconIndex].Sizes[sizeIndex] = redactMCPText(tool.Icons[iconIndex].Sizes[sizeIndex], headers)
			}
		}
	}
	return redacted
}

// redactMCPJSONSnapshot sanitizes both JSON object keys and string values. A
// false result means two distinct keys collapsed to the same redacted key (or
// the document could not be represented safely), so the caller drops the
// complete server snapshot rather than exposing or ambiguously overwriting it.
func redactMCPJSONSnapshot(raw json.RawMessage, headers map[string]string) (json.RawMessage, bool) {
	if len(raw) == 0 || len(headers) == 0 {
		return raw, true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	var redact func(any) (any, bool, bool)
	redact = func(input any) (any, bool, bool) {
		switch typed := input.(type) {
		case string:
			redacted := redactMCPText(typed, headers)
			return redacted, redacted != typed, true
		case []any:
			changed := false
			for index := range typed {
				redactedItem, itemChanged, safe := redact(typed[index])
				if !safe {
					return nil, false, false
				}
				typed[index] = redactedItem
				changed = changed || itemChanged
			}
			return typed, changed, true
		case map[string]any:
			changed := false
			redactedMap := make(map[string]any, len(typed))
			for key, item := range typed {
				redactedKey := redactMCPText(key, headers)
				if _, exists := redactedMap[redactedKey]; exists {
					return nil, false, false
				}
				redactedItem, itemChanged, safe := redact(item)
				if !safe {
					return nil, false, false
				}
				redactedMap[redactedKey] = redactedItem
				changed = changed || redactedKey != key || itemChanged
			}
			return redactedMap, changed, true
		}
		return input, false, true
	}
	redacted, changed, safe := redact(value)
	if !safe {
		return nil, false
	}
	if !changed {
		return raw, true
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func redactMCPText(value string, headers map[string]string) string {
	if value == "" || len(headers) == 0 {
		return value
	}
	secrets := make([]string, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for _, secret := range headers {
		if secret == "" || secret == mcpRuntimeSecretMask {
			continue
		}
		if _, exists := seen[secret]; !exists {
			seen[secret] = struct{}{}
			secrets = append(secrets, secret)
		}
	}
	if len(secrets) == 0 {
		return value
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	replacements := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		replacements = append(replacements, secret, mcpRuntimeSecretMask)
	}
	return strings.NewReplacer(replacements...).Replace(value)
}

func redactMCPError(err error, headers map[string]string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactMCPText(err.Error(), headers))
}

func (r *Registry) mcpServerGeneration(serverID string) uint64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mcpGenerations[serverID]
}

func (r *Registry) mcpClient(
	server *store.MCPServer,
	binding mcpBinding,
	userID string,
	executionWorkspaceID string,
) (*mcp.Client, uint64, error) {
	if server == nil {
		return nil, 0, errors.New("MCP service is unavailable")
	}
	userOwned := binding.UserOwned
	headerJSON, _ := json.Marshal(server.Headers)
	// Include discovery generation/protocol in addition to endpoint credentials.
	// A failed or externally-applied sync can change the negotiated protocol or
	// retire a remote session without changing URL/headers; reusing that client
	// would send the next call with stale MCP state.
	fingerprint := fmt.Sprintf("%s\n%s\n%s\n%d", server.URL, string(headerJSON), server.ProtocolVersion, server.LastSyncedAt)
	cacheKey := "admin:" + server.ID
	if userOwned {
		// A workspace server may be shared by multiple members. Isolate legacy
		// MCP session ids per caller and workspace scope so remote session state
		// cannot cross users or accidentally follow a stale binding into another
		// workspace.
		cacheKey = mcpUserCacheKey(server.ID, binding.WorkspaceID, executionWorkspaceID, userID)
	}
	r.mu.RLock()
	generation := r.mcpGenerations[server.ID]
	cached, ok := r.mcpClients[cacheKey]
	r.mu.RUnlock()
	if ok && cached.fingerprint == fingerprint {
		return cached.client, generation, nil
	}
	config := mcp.Config{URL: server.URL, Headers: server.Headers}
	if userOwned {
		config.HTTPClient = r.userMCPHTTPClient
		if config.HTTPClient == nil {
			config.HTTPClient = netsafe.UserMCPAllowedClient(mcp.DefaultTimeout)
		}
	}
	client, err := mcp.NewClient(config)
	if err != nil {
		return nil, generation, err
	}
	r.mu.Lock()
	// An invalidation may have happened while NewClient was constructing the
	// transport. Never publish a client from the retired generation; the caller
	// will re-read the authoritative row and retry on a subsequent turn.
	if currentGeneration := r.mcpGenerations[server.ID]; currentGeneration != generation {
		r.mu.Unlock()
		return nil, generation, errMCPServerChanged
	}
	if existing, exists := r.mcpClients[cacheKey]; exists && existing.fingerprint == fingerprint {
		client = existing.client
	} else {
		r.mcpClients[cacheKey] = cachedMCPClient{
			fingerprint: fingerprint, client: client, serverID: server.ID,
			userOwned: userOwned, userID: userID,
		}
	}
	r.mu.Unlock()
	return client, generation, nil
}

func (r *Registry) invalidateMCPClient(
	binding mcpBinding,
	userID string,
	executionWorkspaceID string,
	target *mcp.Client,
) {
	if r == nil || target == nil {
		return
	}
	cacheKey := "admin:" + binding.ServerID
	if binding.UserOwned {
		cacheKey = mcpUserCacheKey(binding.ServerID, binding.WorkspaceID, executionWorkspaceID, userID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, ok := r.mcpClients[cacheKey]; ok && cached.client == target {
		delete(r.mcpClients, cacheKey)
	}
}

func mcpUserCacheKey(serverID, resourceWorkspaceID, executionWorkspaceID, userID string) string {
	// Length-prefix each component so arbitrary ids cannot create ambiguous cache
	// keys (for example, a user id containing a colon). Resource scope prevents
	// restored ids from crossing personal/workspace rows; execution scope keeps a
	// personal MCP's remote session state from crossing workspace boundaries.
	return fmt.Sprintf("user:%d:%s:%d:%s:%d:%s:%d:%s",
		len(serverID), serverID,
		len(resourceWorkspaceID), resourceWorkspaceID,
		len(executionWorkspaceID), executionWorkspaceID,
		len(userID), userID,
	)
}

// SaveArtifact lets provider-hosted tools use the same durable artifact path as
// local tools without introducing an llm -> tools import cycle.
func (r *Registry) SaveArtifact(ctx context.Context, tc *llm.ToolContext, name, mime string, data []byte) error {
	_, err := saveArtifact(ctx, tc, r.cfg.ArtifactDir, name, mime, store.ArtifactSourceHostedImageGeneration, data)
	return err
}
