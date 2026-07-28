package llm

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
)

// Provider is the adapter contract per §2.3-A. Each implementation translates
// the unified request into its vendor format, executes the streaming loop and
// emits canonical SseEvents through the onEvent callback.
type Provider interface {
	// ID is one of "anthropic" | "openai" | "google".
	ID() string

	// Stream runs one model turn. The provider drives the multi-turn tool
	// loop internally — the orchestrator picks the model + provider but the
	// provider owns the per-vendor work.
	Stream(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error)
}

// ToolRunner is what the provider calls when it wants to execute a tool.
// The orchestrator implements this so the tool registry / RAG service / etc.
// can be shared across providers.
type ToolRunner interface {
	Run(ctx context.Context, name string, input []byte) (output string, citations []Citation, err error)
}

// Registry holds the live set of providers indexed by ID.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	logger    *log.Logger
}

// NewRegistry builds the default registry of real providers. The orchestrator
// picks one based on the model's channel type; a channel must carry real
// credentials to function.
func NewRegistry(logger *log.Logger) *Registry {
	r := &Registry{
		providers: map[string]Provider{},
		logger:    logger,
	}
	r.Register(&AnthropicProvider{logger: logger})
	r.Register(&OpenAIProvider{logger: logger})
	r.Register(&GoogleProvider{logger: logger})
	return r
}

// Register adds a provider.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	r.providers[p.ID()] = p
	r.mu.Unlock()
}

// ErrUnknownProvider is returned when the channel type doesn't map.
var ErrUnknownProvider = errors.New("unknown provider")

// Get returns the provider for the channel type. The mapping is:
//   - "anthropic" / "claude" → anthropic
//   - "openai" → openai
//   - "google" / "gemini" → google
func (r *Registry) Get(channelType string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if providerID := providerIDForChannelType(channelType); providerID != "" {
		return r.providers[providerID], nil
	}
	return nil, ErrUnknownProvider
}

func providerIDForChannelType(channelType string) string {
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "anthropic", "claude":
		return "anthropic"
	case "openai":
		return "openai"
	case "google", "gemini":
		return "google"
	default:
		return ""
	}
}
