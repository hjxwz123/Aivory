// Package mcp implements the MCP Streamable HTTP client transport used by
// administrator-configured tool servers.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// TransportMode identifies the protocol flow negotiated with an MCP server.
type TransportMode string

const (
	ModeModern TransportMode = "modern"
	ModeLegacy TransportMode = "legacy"
)

// ServerInfo is the identity returned by MCP discovery or initialization.
type ServerInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Version     string `json:"version,omitempty"`
	WebsiteURL  string `json:"websiteUrl,omitempty"`
	Description string `json:"description,omitempty"`
	Icons       []Icon `json:"icons,omitempty"`
}

// Icon describes an optional remote icon. Administrators still control the
// icon displayed for an MCP service in Aivory.
type Icon struct {
	Source   string   `json:"src"`
	MimeType string   `json:"mimeType,omitempty"`
	Sizes    []string `json:"sizes,omitempty"`
}

// Discovery describes the MCP endpoint and protocol selected by the client.
// Capabilities is retained as JSON because MCP capability objects are
// intentionally extensible.
type Discovery struct {
	Mode            TransportMode   `json:"mode"`
	ProtocolVersion string          `json:"protocolVersion"`
	ServerInfo      ServerInfo      `json:"serverInfo"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	Instructions    string          `json:"instructions,omitempty"`
}

// Tool is a remotely exposed MCP tool. JSON Schema values are retained as raw
// JSON so callers can pass them to an LLM provider without lossy conversion.
type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	Meta         json.RawMessage `json:"_meta,omitempty"`
	Icons        []Icon          `json:"icons,omitempty"`
}

// Content is one item returned from tools/call. Text and embedded textual
// resources can be normalized with CallResult.TextContent.
type Content struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Data        string            `json:"data,omitempty"`
	MimeType    string            `json:"mimeType,omitempty"`
	URI         string            `json:"uri,omitempty"`
	Name        string            `json:"name,omitempty"`
	Resource    *ResourceContents `json:"resource,omitempty"`
	Annotations json.RawMessage   `json:"annotations,omitempty"`
	Meta        json.RawMessage   `json:"_meta,omitempty"`
}

// ResourceContents contains an embedded resource returned by a tool. Blob is
// preserved for callers that explicitly support binary data; TextContent does
// not decode or expose it.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// CallResult is the result of tools/call. An MCP-level tool failure is returned
// with IsError=true and a nil Go error; transport and JSON-RPC failures are Go
// errors.
type CallResult struct {
	Content           []Content       `json:"content,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
	Meta              json.RawMessage `json:"_meta,omitempty"`
}

// TextContent returns textual content in response order. If a server only
// returns structuredContent, its compact JSON representation is returned.
func (r CallResult) TextContent() string {
	parts := make([]string, 0, len(r.Content))
	for _, item := range r.Content {
		switch {
		case item.Type == "text" && item.Text != "":
			parts = append(parts, item.Text)
		case item.Type == "resource" && item.Resource != nil && item.Resource.Text != "":
			parts = append(parts, item.Resource.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}

	structured := bytes.TrimSpace(r.StructuredContent)
	if len(structured) == 0 || bytes.Equal(structured, []byte("null")) {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, structured); err != nil {
		return string(structured)
	}
	return compact.String()
}

// RPCError is an error object returned by a JSON-RPC MCP endpoint.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "MCP JSON-RPC error"
	}
	return fmt.Sprintf("MCP JSON-RPC error %d: %s", e.Code, e.Message)
}

// HTTPError reports a non-success response without exposing configured request
// headers. Body contains a short, sanitized response excerpt for diagnostics.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("MCP HTTP error: %s", e.Status)
	}
	return fmt.Sprintf("MCP HTTP error: %s: %s", e.Status, e.Body)
}

// ResponseTooLargeError indicates that a response exceeded Config.MaxResponseBytes.
type ResponseTooLargeError struct {
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("MCP response exceeds %d bytes", e.Limit)
}
