// Package storage is the thin Go-side client for the sandbox sidecar's
// /storage/put and /storage/delete endpoints (design.md §4.5 + §4.11-C).
//
// Why a separate package: the sandbox sidecar is the only process that links
// boto3 / oss2. The Go server has no AWS or Aliyun SDK in its dep graph; it
// just POSTs JSON to the sidecar and treats the bucket as opaque. This keeps
// the Go binary slim and the cloud-SDK choice swappable behind a stable HTTP
// contract.
//
// The RAG ingest pipeline uses Put to stash a document, hands the returned
// presigned URL to MinerU's /api/v4/extract/task, then Deletes after the zip
// download completes (best-effort cleanup; a missed delete is OK because the
// presigned URL expires).
//
// Storage credentials are forwarded on every call, same shape and live-reload
// semantics as the sandbox's existing flow. Empty Provider → caller should
// treat upload as unavailable.
package storage

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

	"aurelia/server/internal/sandbox"
)

// Client is the sidecar-backed object-storage client. BaseURL points at the
// same sandbox sidecar; APIKey gates it. Storage carries the admin-configured
// bucket creds — these are re-resolved by callers each operation so admin
// changes take effect without restarting either process.
type Client struct {
	BaseURL string
	APIKey  string
	Storage *sandbox.StorageConfig
	client  *http.Client
}

// New returns a Client; nil BaseURL or no effective storage → Enabled()=false.
func New(baseURL, apiKey string, storage *sandbox.StorageConfig) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Storage: storage,
		// MinerU PDF uploads can hit 200 MB; give the round-trip enough head-room.
		client: &http.Client{Timeout: 5 * time.Minute},
	}
}

// Enabled reports whether the client has both a sidecar URL and an effective
// storage config. Callers SHOULD check this — the RAG pipeline falls back to
// the no-MinerU placeholder when storage isn't wired.
func (c *Client) Enabled() bool {
	return c != nil && c.BaseURL != "" && c.Storage != nil && c.Storage.Effective()
}

// PutResult is what /storage/put returns.
type PutResult struct {
	Provider  string `json:"provider"`
	Key       string `json:"key"`
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
}

// Put uploads bytes to the bucket under `key` (joined under the admin prefix
// by the sidecar) and returns a presigned GET URL with ttlSeconds expiry.
// Pass 0 to use the sidecar default (1 hour). Cap is 24 hours.
func (c *Client) Put(ctx context.Context, key string, data []byte, contentType string, ttlSeconds int) (*PutResult, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("storage: not enabled (sidecar or storage config missing)")
	}
	payload := map[string]any{
		"key":          key,
		"data_base64":  base64.StdEncoding.EncodeToString(data),
		"content_type": contentType,
		"storage":      c.Storage,
	}
	if ttlSeconds > 0 {
		payload["expires_in"] = ttlSeconds
	}
	var res PutResult
	if err := c.do(ctx, "/storage/put", payload, &res); err != nil {
		return nil, err
	}
	if res.URL == "" {
		return nil, fmt.Errorf("storage: empty presigned URL")
	}
	return &res, nil
}

// Delete drops an object. Idempotent — the sidecar swallows not-found errors,
// so callers don't need to distinguish "wasn't there" from "now isn't there".
func (c *Client) Delete(ctx context.Context, key string) error {
	if !c.Enabled() || key == "" {
		return nil
	}
	return c.do(ctx, "/storage/delete", map[string]any{
		"key":     key,
		"storage": c.Storage,
	}, nil)
}

func (c *Client) do(ctx context.Context, path string, payload any, out any) error {
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("storage %d: %s", resp.StatusCode, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
