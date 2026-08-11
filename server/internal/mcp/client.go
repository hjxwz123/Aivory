package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// ModernProtocolVersion is the stateless server/discover protocol attempted
	// before the client falls back to the session-capable initialize flow.
	ModernProtocolVersion = "2026-07-28"

	DefaultTimeout          = 30 * time.Second
	DefaultMaxResponseBytes = int64(8 << 20)
	maxListPages            = 100
)

var legacyProtocolVersions = []string{"2025-11-25", "2025-03-26", "2024-11-05"}

// Config contains trusted, administrator-controlled connection settings.
// HTTPClient is injectable for transport policy and tests. Request headers are
// copied at construction and cannot be changed by tool-call arguments.
type Config struct {
	URL              string
	Headers          map[string]string
	Timeout          time.Duration
	MaxResponseBytes int64
	HTTPClient       *http.Client
}

// Client is safe for concurrent use.
type Client struct {
	endpoint         string
	headers          http.Header
	timeout          time.Duration
	maxResponseBytes int64
	httpClient       *http.Client
	nextID           atomic.Int64

	discoverMu sync.Mutex
	stateMu    sync.RWMutex
	state      clientState
}

type clientState struct {
	discovery Discovery
	sessionID string
}

// NewClient validates and copies the endpoint configuration.
func NewClient(cfg Config) (*Client, error) {
	endpoint, err := validateEndpoint(cfg.URL)
	if err != nil {
		return nil, err
	}

	headers := make(http.Header, len(cfg.Headers))
	for name, value := range cfg.Headers {
		if !validHeaderName(name) {
			return nil, fmt.Errorf("invalid MCP header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("invalid MCP header value for %q", name)
		}
		if reservedHeader(name) {
			return nil, fmt.Errorf("MCP header %q is managed by the client", name)
		}
		headers.Set(name, value)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	return &Client{
		endpoint:         endpoint,
		headers:          headers,
		timeout:          timeout,
		maxResponseBytes: maxBytes,
		httpClient:       httpClient,
	}, nil
}

// Discover negotiates with the endpoint. It first attempts the modern,
// stateless server/discover method and then falls back to the legacy initialize
// handshake when discovery is unsupported.
func (c *Client) Discover(ctx context.Context) (Discovery, error) {
	c.discoverMu.Lock()
	defer c.discoverMu.Unlock()
	return c.discoverLocked(ctx)
}

func (c *Client) discoverLocked(ctx context.Context) (Discovery, error) {
	modern, modernErr := c.discoverModern(ctx)
	if modernErr == nil {
		c.setState(clientState{discovery: modern})
		return modern, nil
	}
	if !shouldTryLegacy(modernErr) {
		return Discovery{}, modernErr
	}

	legacy, sessionID, legacyErr := c.discoverLegacy(ctx)
	if legacyErr == nil {
		c.setState(clientState{discovery: legacy, sessionID: sessionID})
		return legacy, nil
	}
	return Discovery{}, errors.Join(
		fmt.Errorf("modern MCP discovery failed: %w", modernErr),
		fmt.Errorf("legacy MCP initialization failed: %w", legacyErr),
	)
}

func (c *Client) discoverModern(ctx context.Context) (Discovery, error) {
	params := map[string]any{
		"_meta": map[string]any{"protocolVersion": ModernProtocolVersion},
	}
	result, _, err := c.rpcCall(ctx, "server/discover", params, callOptions{
		protocolVersion: ModernProtocolVersion,
		modern:          true,
	})
	if err != nil {
		return Discovery{}, err
	}
	var payload discoveryPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return Discovery{}, fmt.Errorf("decode server/discover result: %w", err)
	}
	if payload.ProtocolVersion == "" {
		payload.ProtocolVersion = ModernProtocolVersion
	}
	return payload.discovery(ModeModern), nil
}

func (c *Client) discoverLegacy(ctx context.Context) (Discovery, string, error) {
	var attemptErrors []error
	for _, version := range legacyProtocolVersions {
		params := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]string{
				"name":    "aivory",
				"version": "1",
			},
		}
		result, meta, err := c.rpcCall(ctx, "initialize", params, callOptions{protocolVersion: version})
		if err != nil {
			if !shouldRetryLegacyVersion(err) {
				return Discovery{}, "", err
			}
			attemptErrors = append(attemptErrors, fmt.Errorf("initialize %s: %w", version, err))
			continue
		}

		var payload discoveryPayload
		if err := json.Unmarshal(result, &payload); err != nil {
			attemptErrors = append(attemptErrors, fmt.Errorf("decode initialize %s result: %w", version, err))
			continue
		}
		if payload.ProtocolVersion == "" {
			payload.ProtocolVersion = version
		}
		if err := c.rpcNotify(ctx, "notifications/initialized", map[string]any{}, callOptions{
			protocolVersion: payload.ProtocolVersion,
			sessionID:       meta.sessionID,
		}); err != nil {
			return Discovery{}, "", fmt.Errorf("send notifications/initialized: %w", err)
		}
		return payload.discovery(ModeLegacy), meta.sessionID, nil
	}
	return Discovery{}, "", errors.Join(attemptErrors...)
}

// ListTools returns all tools exposed by the endpoint, following MCP cursors up
// to a defensive page limit.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	state, err := c.ensureDiscovered(ctx)
	if err != nil {
		return nil, err
	}

	tools := make([]Tool, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := 0; page < maxListPages; page++ {
		params := make(map[string]any)
		if cursor != "" {
			params["cursor"] = cursor
		}
		addModernMeta(params, state.discovery)

		result, _, err := c.rpcCall(ctx, "tools/list", params, c.optionsForState(state))
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		var payload struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &payload); err != nil {
			return nil, fmt.Errorf("decode tools/list result: %w", err)
		}
		tools = append(tools, payload.Tools...)
		if payload.NextCursor == "" {
			return tools, nil
		}
		if _, exists := seenCursors[payload.NextCursor]; exists {
			return nil, fmt.Errorf("MCP tools/list repeated cursor %q", payload.NextCursor)
		}
		seenCursors[payload.NextCursor] = struct{}{}
		cursor = payload.NextCursor
	}
	return nil, fmt.Errorf("MCP tools/list exceeded %d pages", maxListPages)
}

// CallTool invokes one tool with JSON object arguments. The endpoint and all
// request headers remain fixed by Config.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*CallResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("MCP tool name is required")
	}
	arguments, err := normalizeArguments(arguments)
	if err != nil {
		return nil, err
	}
	state, err := c.ensureDiscovered(ctx)
	if err != nil {
		return nil, err
	}

	params := map[string]any{
		"name":      name,
		"arguments": arguments,
	}
	addModernMeta(params, state.discovery)
	options := c.optionsForState(state)
	options.toolName = name
	result, _, err := c.rpcCall(ctx, "tools/call", params, options)
	if err != nil {
		return nil, fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	var payload CallResult
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("decode tools/call result for %q: %w", name, err)
	}
	return &payload, nil
}

func (c *Client) ensureDiscovered(ctx context.Context) (clientState, error) {
	if state := c.getState(); state.discovery.Mode != "" {
		return state, nil
	}

	c.discoverMu.Lock()
	defer c.discoverMu.Unlock()
	if state := c.getState(); state.discovery.Mode != "" {
		return state, nil
	}
	if _, err := c.discoverLocked(ctx); err != nil {
		return clientState{}, err
	}
	return c.getState(), nil
}

func (c *Client) getState() clientState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

func (c *Client) setState(state clientState) {
	c.stateMu.Lock()
	c.state = state
	c.stateMu.Unlock()
}

func (c *Client) optionsForState(state clientState) callOptions {
	return callOptions{
		protocolVersion: state.discovery.ProtocolVersion,
		sessionID:       state.sessionID,
		modern:          state.discovery.Mode == ModeModern,
	}
}

type discoveryPayload struct {
	ProtocolVersion string          `json:"protocolVersion"`
	ServerInfo      ServerInfo      `json:"serverInfo"`
	Capabilities    json.RawMessage `json:"capabilities"`
	Instructions    string          `json:"instructions"`
}

func (p discoveryPayload) discovery(mode TransportMode) Discovery {
	return Discovery{
		Mode:            mode,
		ProtocolVersion: p.ProtocolVersion,
		ServerInfo:      p.ServerInfo,
		Capabilities:    p.Capabilities,
		Instructions:    p.Instructions,
	}
}

func addModernMeta(params map[string]any, discovery Discovery) {
	if discovery.Mode == ModeModern {
		params["_meta"] = map[string]any{"protocolVersion": discovery.ProtocolVersion}
	}
}

func normalizeArguments(arguments json.RawMessage) (json.RawMessage, error) {
	arguments = bytes.TrimSpace(arguments)
	if len(arguments) == 0 || bytes.Equal(arguments, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(arguments) {
		return nil, errors.New("MCP tool arguments are not valid JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &object); err != nil || object == nil {
		return nil, errors.New("MCP tool arguments must be a JSON object")
	}
	return arguments, nil
}

func validateEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("MCP URL is required")
	}
	if strings.Contains(raw, "#") {
		return "", errors.New("MCP URL must not contain a fragment")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", fmt.Errorf("invalid MCP URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("MCP URL must be an absolute http or https URL")
	}
	if u.User != nil {
		return "", errors.New("MCP URL must not contain user information")
	}
	return u.String(), nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", r) &&
			!(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

func reservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "accept", "content-type", "content-length", "host", "transfer-encoding",
		"mcp-protocol-version", "mcp-session-id", "mcp-method", "mcp-name":
		return true
	default:
		return false
	}
}

func isAuthenticationError(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) &&
		(httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden)
}

func shouldTryLegacy(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		isAuthenticationError(err) {
		return false
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == -32600 || rpcErr.Code == -32601 || rpcErr.Code == -32602
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed,
			http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			return true
		}
	}
	return false
}

func shouldRetryLegacyVersion(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		isAuthenticationError(err) {
		return false
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == -32602
	}
	var httpErr *HTTPError
	return errors.As(err, &httpErr) &&
		(httpErr.StatusCode == http.StatusBadRequest || httpErr.StatusCode == http.StatusUnprocessableEntity)
}
