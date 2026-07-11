// Package tools is the unified self-built tool layer (§4.2). Every tool
// implements Tool; the registry exposes a `List + Run` interface to the
// orchestrator. The orchestrator does not know what the tools are — it just
// hands them inputs and gets back text + citations.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"sync"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/rag"
	"aivory/server/internal/sandbox"
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
	rag     *rag.Service
	logger  *log.Logger
	sandbox sandbox.Service
}

// Sandbox exposes the settings-wrapped sandbox backend so admin endpoints can
// inspect / clear a conversation's workspace.
func (r *Registry) Sandbox() sandbox.Service { return r.sandbox }

// NewRegistry builds the default registry with the built-in tools.
func NewRegistry(db *sql.DB, ragSvc *rag.Service, cfg config.Config, logger *log.Logger) *Registry {
	r := &Registry{tools: map[string]Tool{}, cfg: cfg, db: db, rag: ragSvc, logger: logger}
	// Sandbox config comes from admin settings (sandbox_base_url /
	// sandbox_api_key), re-read per call, with env as the fallback default.
	sb := newSettingsSandbox(db, cfg.SandboxBaseURL, cfg.SandboxAPIKey)
	r.sandbox = sb
	r.Register(&webSearchTool{cfg: cfg, searcher: newSettingsSearcher(db, cfg.SearchProvider, cfg.SearchAPIKey, cfg.SearchBaseURL)})
	r.Register(&webFetchTool{})
	r.Register(&fetchImageTool{sandbox: sb, logger: logger})
	r.Register(&pythonExecuteTool{sandbox: sb, artifactDir: cfg.ArtifactDir, logger: logger})
	r.Register(&imageGenerateTool{db: db, artifactDir: cfg.ArtifactDir})
	r.Register(&searchKnowledgeBaseTool{rag: ragSvc})
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

// List returns the tool definitions visible to a model. The list is sorted by
// name so serialization is deterministic across requests — unstable ordering
// busts the upstream prompt-prefix cache (pitfall D5, §4.9).
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

// Run executes a tool by name.
func (r *Registry) Run(ctx context.Context, name string, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", nil, errors.New("unknown tool: " + name)
	}
	return t.Execute(ctx, input, tc)
}
