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
//	POST {BaseURL}/files/reset-inputs {session_id} -> {"ok":true}
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

	"aivory/server/internal/envcfg"
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
// to whichever backend the admin configured live (no sidecar restart). Empty
// Provider means "no archive" — workspaces reaped = gone, which matches the
// original sidecar default. Provider is one of "s3" | "aliyun_oss" | "local" | "".
// For "local" the sidecar writes tarballs to its SANDBOX_LOCAL_STORAGE_DIR
// (an operator-mounted volume), so no bucket/creds fields are needed here.
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
	case "local":
		// The archive dir lives sidecar-side (SANDBOX_LOCAL_STORAGE_DIR); Go
		// can't see it, so it always forwards and lets the sidecar gate on
		// whether the dir is actually mounted (a missing mount → no-op).
		return true
	}
	return false
}

// Service is the contract the python_execute tool depends on.
type Service interface {
	// Enabled reports whether a real backend is configured. When false the
	// tool falls back to its safe-mode evaluator so dev stays usable.
	Enabled() bool
	// NewSession provisions a fresh sandbox session and returns its id (stored on
	// the conversation's provider_state). archiveKey is a STABLE key (the
	// conversation id) under which /workspace is archived and restored, so the
	// filesystem survives session recycle even though each session gets a fresh
	// id (§4.5-C G2). Pass "" to key the archive by the session id (no
	// cross-recycle persistence).
	NewSession(ctx context.Context, archiveKey string) (string, error)
	// Exec runs code in the given session and returns stdout/stderr + any
	// files written to /workspace/outputs.
	Exec(ctx context.Context, sessionID, code string) (*Result, error)
	// PutFile writes a file into the session workspace (e.g.
	// /workspace/uploads/data.csv) so python_execute can read it (§4.5).
	PutFile(ctx context.Context, sessionID, path string, data []byte) error
	// ResetInputs removes every previously staged input under
	// /workspace/uploads and /workspace/skills, then recreates those directories.
	// It intentionally leaves /workspace/outputs and all other generated workspace
	// state alone. Callers use this before staging the current non-image inputs so
	// an older persistent session/archive cannot retain legacy image copies.
	ResetInputs(ctx context.Context, sessionID string) error
	// GetFile reads a file out of the session workspace. Used when the
	// orchestrator wants to surface a sandbox-produced file that isn't a
	// declared artifact (e.g. an intermediate dataset the user asks about).
	GetFile(ctx context.Context, sessionID, path string) ([]byte, error)
	// ListFiles lists every file under /workspace for a session (admin sandbox
	// inspector). Read-only.
	ListFiles(ctx context.Context, sessionID string) ([]SandboxFile, error)
	// Release tears down a session deliberately (the user closed the
	// conversation, or compaction archived its workspace). Idempotent.
	// It ARCHIVES /workspace first, so a later session for the same conversation
	// restores it (§4.5-C G2).
	Release(ctx context.Context, sessionID string) error
	// ReleaseDiscard tears down a session WITHOUT archiving, and also deletes the
	// existing archive under archiveKey (the conversation id) — a real purge for
	// the admin "clear sandbox" control, which stable-key restore would otherwise
	// silently undo. Idempotent. archiveKey "" only skips the archive.
	ReleaseDiscard(ctx context.Context, sessionID, archiveKey string) error
	// PruneArchives deletes archived workspace tarballs older than maxAge from
	// the configured object store, returning how many were removed. A no-op
	// returning (0, nil) when no storage backend is configured or maxAge<=0 —
	// archived workspaces otherwise accumulate one-per-session forever (§4.5).
	PruneArchives(ctx context.Context, maxAge time.Duration) (int, error)
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
	// ExecTimeout is the per-exec wall-clock cap forwarded to the sidecar
	// (timeout_ms). The HTTP client timeout is derived from it (+overhead) so a
	// long-but-valid run can't trip "context deadline exceeded" before the
	// sidecar answers. 0 → defaultExecTimeout.
	ExecTimeout time.Duration
	// IdleTTL is the admin-tunable idle-recycle window forwarded on /sessions
	// (idle_ttl_sec). 0 → the sidecar's own default (SANDBOX_IDLE_TTL_SECONDS).
	// Bound at session-create; already-live sessions keep their create-time TTL.
	IdleTTL time.Duration
	client  *http.Client
}

const (
	// defaultExecTimeout is the per-exec cap when the admin hasn't set one.
	defaultExecTimeout = 120 * time.Second
)

// maxSandboxRespBytes caps the decoded sidecar response so a buggy/compromised
// sidecar can't OOM the API process. Generous: > the 50MB artifact total.
var maxSandboxRespBytes = envcfg.Int64("AIVORY_SANDBOX_MAX_SANDBOX_RESP_BYTES", 256<<20)

// execClientOverhead is added on top of the exec cap to size the HTTP
// client timeout: the sidecar still has to write the cell, snapshot, kill a
// runaway, and base64 any output files AFTER the code's own deadline. The
// sidecar now BOUNDS artifact collection (SANDBOX_MAX_COLLECT_SECONDS, default
// 60s) so its post-deadline work is a known quantity; this 120s comfortably
// covers that budget plus the fixed control calls, so the client outlasts the
// sidecar's worst-case single-exec time instead of cutting it off mid-collection.
var execClientOverhead = envcfg.Dur("AIVORY_SANDBOX_EXEC_CLIENT_OVERHEAD", 120*time.Second)

// sandboxErrorBodyReadCap bounds how much of a 4xx/5xx sidecar error body we read.
var sandboxErrorBodyReadCap = envcfg.Int64("AIVORY_SANDBOX_SANDBOX_ERROR_BODY_READ_CAP", 64<<10)

// Options configures an HTTPSandbox. The settings-wrapped backend
// (internal/tools/sandbox_settings.go) fills these from admin settings on every
// call, so URL / key / storage / exec timeout all follow the admin UI live.
type Options struct {
	BaseURL     string
	APIKey      string
	Storage     *StorageConfig
	ExecTimeout time.Duration // 0 → defaultExecTimeout
	IdleTTL     time.Duration // 0 → sidecar default (SANDBOX_IDLE_TTL_SECONDS)
}

// NewWithOptions builds a sandbox Service, sizing the HTTP client timeout to the
// exec cap + overhead so the client never deadlines before the sidecar.
func NewWithOptions(o Options) Service {
	exec := o.ExecTimeout
	if exec <= 0 {
		exec = defaultExecTimeout
	}
	return &HTTPSandbox{
		BaseURL:     strings.TrimRight(o.BaseURL, "/"),
		APIKey:      o.APIKey,
		Storage:     o.Storage,
		ExecTimeout: exec,
		IdleTTL:     o.IdleTTL,
		client:      &http.Client{Timeout: exec + execClientOverhead},
	}
}

// New returns a Service. When baseURL is empty the returned service reports
// Enabled()=false and all calls error, so callers fall back to safe-mode.
func New(baseURL, apiKey string) Service {
	return NewWithOptions(Options{BaseURL: baseURL, APIKey: apiKey})
}

// NewWithStorage is identical to New but also carries the admin-configured
// archive/restore bucket. Used by the settings-wrapped backend so storage
// follows the admin UI live.
func NewWithStorage(baseURL, apiKey string, storage *StorageConfig) Service {
	return NewWithOptions(Options{BaseURL: baseURL, APIKey: apiKey, Storage: storage})
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, sandboxErrorBodyReadCap)) // error bodies are small; cap anyway
		return fmt.Errorf("sandbox %d: %s", resp.StatusCode, string(b))
	}
	if out == nil {
		return nil
	}
	// Cap the decoded body: the largest legit response is an Exec/GetFile with
	// base64 artifacts (≤50MB total → ~67MB base64), so 256MB is generous while
	// still preventing a buggy/compromised sidecar from OOMing the API process.
	return json.NewDecoder(io.LimitReader(resp.Body, maxSandboxRespBytes)).Decode(out)
}

// NewSession provisions a fresh sandbox session.
func (s *HTTPSandbox) NewSession(ctx context.Context, archiveKey string) (string, error) {
	var res struct {
		SessionID string `json:"session_id"`
	}
	payload := map[string]any{}
	if s.Storage != nil && s.Storage.Effective() {
		payload["storage"] = s.Storage
	}
	if s.IdleTTL > 0 {
		payload["idle_ttl_sec"] = int(s.IdleTTL.Seconds())
	}
	if archiveKey != "" {
		payload["archive_key"] = archiveKey
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

// ResetInputs clears the two server-managed input namespaces. The sidecar owns
// the exact paths so callers cannot turn this into a general-purpose deletion
// primitive.
func (s *HTTPSandbox) ResetInputs(ctx context.Context, sessionID string) error {
	return s.do(ctx, "/files/reset-inputs", map[string]any{
		"session_id": sessionID,
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
	// §4.5 安全基线: 单次执行超时 = 管理员配置(默认 120s),由 sidecar 再按其硬上限钳制。
	timeoutMs := int(defaultExecTimeout.Milliseconds())
	if s.ExecTimeout > 0 {
		timeoutMs = int(s.ExecTimeout.Milliseconds())
	}
	payload := map[string]any{"session_id": sessionID, "code": code, "timeout_ms": timeoutMs}
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

// SandboxFile is one file in a session workspace (path relative to /workspace).
type SandboxFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ListFiles lists every file under /workspace for a session via the sidecar's
// `POST /files/list {session_id}` → {files:[{path,size}]}.
func (s *HTTPSandbox) ListFiles(ctx context.Context, sessionID string) ([]SandboxFile, error) {
	var res struct {
		Files []SandboxFile `json:"files"`
	}
	if err := s.do(ctx, "/files/list", map[string]any{"session_id": sessionID}, &res); err != nil {
		return nil, err
	}
	return res.Files, nil
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

// ReleaseDiscard deletes a session without archiving /workspace, and deletes any
// existing archive keyed by archiveKey so a stable-key restore (§4.5-C G2)
// can't resurrect the "cleared" workspace. Idempotent.
func (s *HTTPSandbox) ReleaseDiscard(ctx context.Context, sessionID, archiveKey string) error {
	if sessionID == "" {
		return nil
	}
	payload := map[string]any{"discard": true}
	if s.Storage != nil && s.Storage.Effective() {
		payload["storage"] = s.Storage
	}
	if archiveKey != "" {
		payload["archive_key"] = archiveKey
	}
	return s.doMethod(ctx, "DELETE", "/sessions/"+sessionID, payload, nil)
}

// PruneArchives asks the sidecar to delete archived workspaces older than
// maxAge. No-op when no object store is configured (nothing is ever archived)
// or maxAge<=0 (the admin disabled the sweep).
func (s *HTTPSandbox) PruneArchives(ctx context.Context, maxAge time.Duration) (int, error) {
	if s.Storage == nil || !s.Storage.Effective() {
		return 0, nil
	}
	secs := int(maxAge.Seconds())
	if secs <= 0 {
		return 0, nil
	}
	payload := map[string]any{"storage": s.Storage, "max_age_seconds": secs}
	var res struct {
		Deleted int `json:"deleted"`
	}
	if err := s.do(ctx, "/storage/gc", payload, &res); err != nil {
		return 0, err
	}
	return res.Deleted, nil
}
