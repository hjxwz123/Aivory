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
	"net/url"
	"sort"
	"strings"
	"sync"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/mcp"
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
}

type mcpBinding struct {
	ServerID   string
	RemoteName string
}

type cachedMCPClient struct {
	fingerprint string
	client      *mcp.Client
}

// Sandbox exposes the settings-wrapped sandbox backend so admin endpoints can
// inspect / clear a conversation's workspace.
func (r *Registry) Sandbox() sandbox.Service { return r.sandbox }

// NewRegistry builds the default registry with the built-in tools.
func NewRegistry(db *sql.DB, cfg config.Config, logger *log.Logger) *Registry {
	r := &Registry{
		tools: map[string]Tool{}, cfg: cfg, db: db, logger: logger,
		mcpBindings: map[string]mcpBinding{}, mcpClients: map[string]cachedMCPClient{},
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

// List returns every registered tool definition. The orchestrator applies the
// loaded model's allowlist and global kill-switch; keeping storage access out of
// the registry avoids duplicate queries and also lets this list drive the admin
// capability endpoint. The list is sorted for deterministic serialization.
func (r *Registry) List(_ string) []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []llm.ToolDef{}
	for _, t := range r.tools {
		out = append(out, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListMCP returns the persisted tools/list snapshot for every enabled MCP
// service. Discovery is an explicit admin action, so chat turns never block on
// remote schema discovery and a transient service outage cannot reshape a turn.
func (r *Registry) ListMCP(_ string) []llm.MCPToolDef {
	if r.db == nil {
		return []llm.MCPToolDef{}
	}
	servers, err := store.ListMCPServers(context.Background(), r.db, true)
	if err != nil {
		if r.logger != nil {
			r.logger.Printf("list enabled MCP services: %v", err)
		}
		return []llm.MCPToolDef{}
	}

	out := []llm.MCPToolDef{}
	bindings := make(map[string]mcpBinding)
	usedFunctionNames := make(map[string]struct{})
	r.mu.RLock()
	for name := range r.tools {
		usedFunctionNames[name] = struct{}{}
	}
	r.mu.RUnlock()
	seenRemoteTools := make(map[string]struct{})
	for _, server := range servers {
		var remoteTools []mcp.Tool
		if err := json.Unmarshal(server.DiscoveredTools, &remoteTools); err != nil {
			if r.logger != nil {
				r.logger.Printf("decode MCP tool snapshot for %q: %v", server.ID, err)
			}
			continue
		}
		for _, remote := range remoteTools {
			remote.Name = strings.TrimSpace(remote.Name)
			if remote.Name == "" || !validMCPInputSchema(remote.InputSchema) {
				continue
			}
			remoteKey := server.ID + "\x00" + remote.Name
			if _, exists := seenRemoteTools[remoteKey]; exists {
				continue
			}
			seenRemoteTools[remoteKey] = struct{}{}
			functionName, ok := reserveMCPFunctionName(server.ID, remote.Name, usedFunctionNames)
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
			})
			bindings[functionName] = mcpBinding{ServerID: server.ID, RemoteName: remote.Name}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServerID == out[j].ServerID {
			return out[i].Name < out[j].Name
		}
		return out[i].DisplayName < out[j].DisplayName
	})
	r.mu.Lock()
	r.mcpBindings = bindings
	r.mu.Unlock()
	return out
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
	if !tc.AllowsBuiltinTool(name) {
		return "", nil, errors.New("tool is not enabled for this model: " + name)
	}
	r.mu.RLock()
	t, ok := r.tools[name]
	binding, mcpOK := r.mcpBindings[name]
	r.mu.RUnlock()
	if !ok && !mcpOK {
		return "", nil, errors.New("unknown tool: " + name)
	}
	if err := r.checkCurrentToolAccess(ctx, name, binding, mcpOK, tc); err != nil {
		return "", nil, err
	}
	if ok {
		return t.Execute(ctx, input, tc)
	}
	return r.runMCP(ctx, binding, input)
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
	if r.db == nil || tc == nil || strings.TrimSpace(tc.UserID) == "" {
		return nil
	}
	permissions, err := store.UserGroupPermissionsForUser(ctx, r.db, tc.UserID)
	if err != nil {
		return fmt.Errorf("resolve current tool permission: %w", err)
	}
	toolID := "builtin:" + name
	if isMCP {
		toolID = "mcp:" + binding.ServerID
	}
	if !store.ResourcePolicyAllows(permissions.Tools, toolID) {
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

func (r *Registry) runMCP(ctx context.Context, binding mcpBinding, input []byte) (string, []llm.Citation, error) {
	if r.db == nil {
		return "", nil, errors.New("MCP tools are unavailable")
	}
	server, err := store.GetMCPServer(ctx, r.db, binding.ServerID)
	if err != nil {
		return "", nil, fmt.Errorf("load MCP service: %w", err)
	}
	if !server.Enabled {
		return "", nil, errors.New("MCP service is disabled")
	}
	client, err := r.mcpClient(server)
	if err != nil {
		return "", nil, redactMCPError(err, server.Headers)
	}
	result, err := client.CallTool(ctx, binding.RemoteName, json.RawMessage(input))
	if err != nil {
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

const mcpRuntimeSecretMask = "[redacted]"

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

func (r *Registry) mcpClient(server *store.MCPServer) (*mcp.Client, error) {
	headerJSON, _ := json.Marshal(server.Headers)
	fingerprint := server.URL + "\n" + string(headerJSON)
	r.mu.RLock()
	cached, ok := r.mcpClients[server.ID]
	r.mu.RUnlock()
	if ok && cached.fingerprint == fingerprint {
		return cached.client, nil
	}
	client, err := mcp.NewClient(mcp.Config{URL: server.URL, Headers: server.Headers})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if existing, exists := r.mcpClients[server.ID]; exists && existing.fingerprint == fingerprint {
		client = existing.client
	} else {
		r.mcpClients[server.ID] = cachedMCPClient{fingerprint: fingerprint, client: client}
	}
	r.mu.Unlock()
	return client, nil
}

// SaveArtifact lets provider-hosted tools use the same durable artifact path as
// local tools without introducing an llm -> tools import cycle.
func (r *Registry) SaveArtifact(ctx context.Context, tc *llm.ToolContext, name, mime string, data []byte) error {
	_, err := saveArtifact(ctx, tc, r.cfg.ArtifactDir, name, mime, store.ArtifactSourceHostedImageGeneration, data)
	return err
}
