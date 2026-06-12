// Package sandbox is the single integration point for the Python execution
// environment described in design.md §4.5. The product does NOT embed its own
// runtime; it proxies to a ready-made sandbox service (OpenTerminal / E2B /
// any HTTP-exposed runner). Swap the backend by pointing SANDBOX_BASE_URL at a
// different service — nothing else in the codebase changes.
//
// Protocol (kept deliberately tiny so any runner can implement it):
//
//	POST {BaseURL}/sessions            -> {"session_id":"..."}
//	POST {BaseURL}/exec  {session_id, code, timeout_ms}
//	     -> {"stdout":"...","stderr":"...","exit_code":0,
//	         "files":[{"name":"plot.png","mime_type":"image/png","data_base64":"..."}]}
//
// `files` are the artifacts written under /workspace/outputs during the run.
// The Authorization: Bearer <SANDBOX_API_KEY> header is attached when set.
package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// File is an artifact produced by a run (a file under /workspace/outputs).
type File struct {
	Name     string
	MimeType string
	Data     []byte
}

// Result is the outcome of one Exec call.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Files    []File
}

// StorageConfig is the per-call override the Go side forwards on /sessions
// and /sessions/:delete so the sandbox sidecar can archive/restore /workspace
// to whichever bucket the admin configured live (no sidecar restart). Empty
// Provider means "no archive" — workspaces reaped = gone, which matches the
// original sidecar default. Provider is one of "s3" | "aliyun_oss" | "".
type StorageConfig struct {
	Provider string `json:"provider,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	// S3
	S3Bucket    string `json:"s3_bucket,omitempty"`
	S3Region    string `json:"s3_region,omitempty"`
	S3Endpoint  string `json:"s3_endpoint,omitempty"`
	S3AccessKey string `json:"s3_access_key,omitempty"`
	S3SecretKey string `json:"s3_secret_key,omitempty"`
	// Aliyun OSS
	OSSBucket          string `json:"oss_bucket,omitempty"`
	OSSEndpoint        string `json:"oss_endpoint,omitempty"`
	OSSAccessKeyID     string `json:"oss_access_key_id,omitempty"`
	OSSAccessKeySecret string `json:"oss_access_key_secret,omitempty"`
}

// Effective reports whether a usable storage backend is configured. An empty
// provider, an unknown provider, or missing creds collapses to false.
func (c *StorageConfig) Effective() bool {
	if c == nil {
		return false
	}
	switch c.Provider {
	case "s3":
		return c.S3Bucket != ""
	case "aliyun_oss":
		return c.OSSBucket != "" && c.OSSEndpoint != "" &&
			c.OSSAccessKeyID != "" && c.OSSAccessKeySecret != ""
	}
	return false
}

// Service is the contract the python_execute tool depends on.
type Service interface {
	// Enabled reports whether a real backend is configured. When false the
	// tool falls back to its safe-mode evaluator so dev stays usable.
	Enabled() bool
	// NewSession provisions a fresh sandbox with a persistent /workspace and
	// returns its id. The id is stored on the conversation (provider_state)
	// so subsequent calls reuse the same filesystem (§4.5).
	NewSession(ctx context.Context) (string, error)
	// Exec runs code in the given session and returns stdout/stderr + any
	// files written to /workspace/outputs.
	Exec(ctx context.Context, sessionID, code string) (*Result, error)
	// PutFile writes a file into the session workspace (e.g.
	// /workspace/uploads/data.csv) so python_execute can read it (§4.5).
	PutFile(ctx context.Context, sessionID, path string, data []byte) error
	// GetFile reads a file out of the session workspace. Used when the
	// orchestrator wants to surface a sandbox-produced file that isn't a
	// declared artifact (e.g. an intermediate dataset the user asks about).
	GetFile(ctx context.Context, sessionID, path string) ([]byte, error)
	// Release tears down a session deliberately (the user closed the
	// conversation, or compaction archived its workspace). Idempotent.
	Release(ctx context.Context, sessionID string) error
}

// HTTPSandbox talks to an external runner over the tiny JSON protocol above.
type HTTPSandbox struct {
	BaseURL string
	APIKey  string
	// Storage is the per-call archive/restore config. The settings-wrapped
	// sandbox (see internal/tools/sandbox_settings.go) re-reads the admin
	// settings each call and bakes the resolved values in here, so a change
	// in the admin UI applies on the very next /sessions request without
	// restarting the sidecar.
	Storage *StorageConfig
	client  *http.Client
}

// New returns a Service. When baseURL is empty the returned service reports
// Enabled()=false and all calls error, so callers fall back to safe-mode.
func New(baseURL, apiKey string) Service {
	return &HTTPSandbox{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		client:  &http.Client{Timeout: 150 * time.Second}, // > 120s exec ceiling
	}
}

// NewWithStorage is identical to New but also carries the admin-configured
// archive/restore bucket. Used by the settings-wrapped backend so storage
// follows the admin UI live.
func NewWithStorage(baseURL, apiKey string, storage *StorageConfig) Service {
	return &HTTPSandbox{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Storage: storage,
		client:  &http.Client{Timeout: 150 * time.Second},
	}
}

// Enabled reports whether a backend URL is configured.
func (s *HTTPSandbox) Enabled() bool { return s.BaseURL != "" }

func (s *HTTPSandbox) do(ctx context.Context, path string, payload any, out any) error {
	return s.doMethod(ctx, "POST", path, payload, out)
}

func (s *HTTPSandbox) doMethod(ctx context.Context, method, path string, payload any, out any) error {
	if !s.Enabled() {
		return fmt.Errorf("sandbox: no SANDBOX_BASE_URL configured")
	}
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, method, s.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if s.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+s.APIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sandbox %d: %s", resp.StatusCode, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// NewSession provisions a fresh sandbox session.
func (s *HTTPSandbox) NewSession(ctx context.Context) (string, error) {
	var res struct {
		SessionID string `json:"session_id"`
	}
	payload := map[string]any{}
	if s.Storage != nil && s.Storage.Effective() {
		payload["storage"] = s.Storage
	}
	if err := s.do(ctx, "/sessions", payload, &res); err != nil {
		return "", err
	}
	if res.SessionID == "" {
		return "", fmt.Errorf("sandbox: empty session_id")
	}
	return res.SessionID, nil
}

// PutFile writes a file into the session workspace via POST {BaseURL}/files
// with {session_id, path, data_base64}.
func (s *HTTPSandbox) PutFile(ctx context.Context, sessionID, path string, data []byte) error {
	return s.do(ctx, "/files", map[string]any{
		"session_id":  sessionID,
		"path":        path,
		"data_base64": base64.StdEncoding.EncodeToString(data),
	}, nil)
}

// Exec runs code in the session.
func (s *HTTPSandbox) Exec(ctx context.Context, sessionID, code string) (*Result, error) {
	var res struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
		Files    []struct {
			Name       string `json:"name"`
			MimeType   string `json:"mime_type"`
			DataBase64 string `json:"data_base64"`
		} `json:"files"`
	}
	// §4.5 安全基线: 单次执行超时 120s。
	payload := map[string]any{"session_id": sessionID, "code": code, "timeout_ms": 120000}
	if err := s.do(ctx, "/exec", payload, &res); err != nil {
		return nil, err
	}
	out := &Result{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode}
	for _, f := range res.Files {
		data, err := base64.StdEncoding.DecodeString(f.DataBase64)
		if err != nil {
			continue
		}
		out.Files = append(out.Files, File{Name: f.Name, MimeType: f.MimeType, Data: data})
	}
	return out, nil
}

// GetFile fetches `path` from the session workspace. Used to surface a
// sandbox file that wasn't returned as a declared artifact. The sidecar
// exposes `POST /files/get {session_id, path}` returning {data_base64}.
func (s *HTTPSandbox) GetFile(ctx context.Context, sessionID, path string) ([]byte, error) {
	var res struct {
		DataBase64 string `json:"data_base64"`
	}
	if err := s.do(ctx, "/files/get", map[string]any{
		"session_id": sessionID,
		"path":       path,
	}, &res); err != nil {
		return nil, err
	}
	if res.DataBase64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(res.DataBase64)
}

// Release deletes a session. Idempotent.
func (s *HTTPSandbox) Release(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	payload := map[string]any{}
	if s.Storage != nil && s.Storage.Effective() {
		payload["storage"] = s.Storage
	}
	return s.doMethod(ctx, "DELETE", "/sessions/"+sessionID, payload, nil)
}
