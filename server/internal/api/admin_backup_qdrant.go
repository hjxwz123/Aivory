package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aurelia/server/internal/envcfg"
)

const (
	qdrantArchiveVersion   = 1
	qdrantCollectionPrefix = "aurelia_c"
	qdrantZipManifest      = "qdrant/manifest.json"
	qdrantZipCollectionDir = "qdrant/collections/"
)

var (
	qdrantArchiveRequestTimeout      = envcfg.Dur("AURELIA_API_QDRANT_ARCHIVE_REQUEST_TIMEOUT", 5*time.Minute)
	qdrantErrorBodyReadCap           = int64(1 << 20)
	qdrantExportScrollPageSize       = envcfg.Int("AURELIA_API_QDRANT_EXPORT_SCROLL_PAGE_SIZE", 256)
	qdrantImportUpsertFlushBatchSize = envcfg.Int("AURELIA_API_QDRANT_IMPORT_UPSERT_FLUSH_BATCH_SIZE", 128)
)

type qdrantArchiveManifest struct {
	Format      string                    `json:"format"`
	Version     int                       `json:"version"`
	CreatedAt   int64                     `json:"created_at"`
	Collections []qdrantCollectionArchive `json:"collections"`
}

type qdrantCollectionArchive struct {
	Name   string `json:"name"`
	Dim    int    `json:"dim"`
	Entry  string `json:"entry"`
	Points int64  `json:"points"`
}

type qdrantDumpPoint struct {
	ID      json.RawMessage `json:"id"`
	Vector  json.RawMessage `json:"vector"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type qdrantArchiveClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newQdrantArchiveClient(d Deps) *qdrantArchiveClient {
	base := strings.TrimRight(strings.TrimSpace(d.Config.QdrantURL), "/")
	if base == "" {
		return nil
	}
	return &qdrantArchiveClient{
		baseURL: base,
		apiKey:  d.Config.QdrantAPIKey,
		http:    &http.Client{Timeout: qdrantArchiveRequestTimeout},
	}
}

func (c *qdrantArchiveClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, qdrantErrorBodyReadCap))
		return fmt.Errorf("qdrant %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode qdrant %s %s: %w", method, path, err)
		}
	}
	return nil
}

func (c *qdrantArchiveClient) listAureliaCollections(ctx context.Context) ([]string, error) {
	var out struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, "/collections", nil, &out); err != nil {
		return nil, err
	}
	names := []string{}
	for _, coll := range out.Result.Collections {
		if validQdrantCollectionName(coll.Name) {
			names = append(names, coll.Name)
		}
	}
	return names, nil
}

func exportQdrantToZip(ctx context.Context, d Deps, zw *zip.Writer) (int64, error) {
	client := newQdrantArchiveClient(d)
	if client == nil {
		return 0, nil
	}
	names, err := client.listAureliaCollections(ctx)
	if err != nil {
		return 0, err
	}
	manifest := qdrantArchiveManifest{
		Format:    "aurelia-qdrant",
		Version:   qdrantArchiveVersion,
		CreatedAt: time.Now().Unix(),
	}
	var total int64
	for _, name := range names {
		dim, err := dimFromQdrantCollection(name)
		if err != nil {
			return total, err
		}
		entry := qdrantZipCollectionDir + name + ".jsonl"
		fw, err := zw.Create(entry)
		if err != nil {
			return total, err
		}
		n, err := client.exportCollection(ctx, name, fw)
		if err != nil {
			return total, fmt.Errorf("%s: %w", name, err)
		}
		total += n
		manifest.Collections = append(manifest.Collections, qdrantCollectionArchive{
			Name:   name,
			Dim:    dim,
			Entry:  entry,
			Points: n,
		})
	}
	mw, err := zw.Create(qdrantZipManifest)
	if err != nil {
		return total, err
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		return total, err
	}
	return total, nil
}

func (c *qdrantArchiveClient) exportCollection(ctx context.Context, name string, w io.Writer) (int64, error) {
	enc := json.NewEncoder(w)
	var total int64
	var offset json.RawMessage
	for {
		body := map[string]any{
			"limit":        qdrantExportScrollPageSize,
			"with_payload": true,
			"with_vector":  true,
		}
		if len(offset) > 0 && string(offset) != "null" {
			body["offset"] = offset
		}
		var out struct {
			Result struct {
				Points []qdrantDumpPoint `json:"points"`
				Next   json.RawMessage   `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := c.do(ctx, http.MethodPost, "/collections/"+url.PathEscape(name)+"/points/scroll", body, &out); err != nil {
			return total, err
		}
		for _, p := range out.Result.Points {
			if len(p.ID) == 0 || len(p.Vector) == 0 || string(p.Vector) == "null" {
				continue
			}
			if len(p.Payload) == 0 {
				p.Payload = json.RawMessage(`{}`)
			}
			if err := enc.Encode(p); err != nil {
				return total, err
			}
			total++
		}
		if len(out.Result.Next) == 0 || string(out.Result.Next) == "null" {
			break
		}
		offset = out.Result.Next
	}
	return total, nil
}

func restoreQdrantFromZip(ctx context.Context, d Deps, zr *zip.Reader) (int64, string) {
	client := newQdrantArchiveClient(d)
	if client == nil {
		if findZipFile(zr, qdrantZipManifest) != nil {
			return 0, "archive contains Qdrant vectors, but QDRANT_URL is not configured"
		}
		return 0, ""
	}
	entry := findZipFile(zr, qdrantZipManifest)
	if entry == nil {
		if err := client.deleteAureliaCollections(ctx); err != nil {
			return 0, err.Error()
		}
		return 0, ""
	}
	rc, err := entry.Open()
	if err != nil {
		return 0, err.Error()
	}
	var man qdrantArchiveManifest
	err = json.NewDecoder(rc).Decode(&man)
	_ = rc.Close()
	if err != nil {
		return 0, fmt.Sprintf("invalid qdrant manifest: %v", err)
	}
	if man.Format != "aurelia-qdrant" {
		return 0, "invalid qdrant manifest format"
	}
	if man.Version > qdrantArchiveVersion {
		return 0, fmt.Sprintf("qdrant archive v%d is newer than this server supports (v%d)", man.Version, qdrantArchiveVersion)
	}
	if err := client.deleteAureliaCollections(ctx); err != nil {
		return 0, err.Error()
	}
	var total int64
	for _, coll := range man.Collections {
		if !validQdrantCollectionName(coll.Name) {
			return total, fmt.Sprintf("invalid qdrant collection name %q", coll.Name)
		}
		if coll.Dim <= 0 {
			return total, fmt.Sprintf("invalid qdrant dimension for %q", coll.Name)
		}
		entry := findZipFile(zr, coll.Entry)
		if entry == nil {
			return total, fmt.Sprintf("missing qdrant collection entry %q", coll.Entry)
		}
		if err := client.ensureCollection(ctx, coll.Name, coll.Dim); err != nil {
			return total, err.Error()
		}
		n, err := client.importCollection(ctx, coll.Name, entry)
		if err != nil {
			return total, err.Error()
		}
		total += n
	}
	return total, ""
}

func (c *qdrantArchiveClient) deleteAureliaCollections(ctx context.Context) error {
	names, err := c.listAureliaCollections(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := c.do(ctx, http.MethodDelete, "/collections/"+url.PathEscape(name), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *qdrantArchiveClient) ensureCollection(ctx context.Context, name string, dim int) error {
	body := map[string]any{
		"vectors": map[string]any{"size": dim, "distance": "Cosine"},
	}
	if err := c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(name), body, nil); err != nil {
		return err
	}
	for _, field := range []string{"kb_id", "conversation_id", "document_id"} {
		_ = c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(name)+"/index?wait=true",
			map[string]any{"field_name": field, "field_schema": "keyword"}, nil)
	}
	_ = c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(name)+"/index?wait=true",
		map[string]any{
			"field_name": "content",
			"field_schema": map[string]any{
				"type":          "text",
				"tokenizer":     "multilingual",
				"min_token_len": 1,
				"lowercase":     true,
			},
		}, nil)
	return nil
}

func (c *qdrantArchiveClient) importCollection(ctx context.Context, name string, entry *zip.File) (int64, error) {
	rc, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	batch := make([]qdrantDumpPoint, 0, qdrantImportUpsertFlushBatchSize)
	var total int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		body := struct {
			Points []qdrantDumpPoint `json:"points"`
		}{Points: batch}
		if err := c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(name)+"/points?wait=true", body, nil); err != nil {
			return err
		}
		total += int64(len(batch))
		batch = batch[:0]
		return nil
	}
	for {
		var p qdrantDumpPoint
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return total, err
		}
		if len(p.ID) == 0 || len(p.Vector) == 0 || string(p.Vector) == "null" {
			continue
		}
		if len(p.Payload) == 0 {
			p.Payload = json.RawMessage(`{}`)
		}
		batch = append(batch, p)
		if len(batch) >= qdrantImportUpsertFlushBatchSize {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

func validQdrantCollectionName(name string) bool {
	if !strings.HasPrefix(name, qdrantCollectionPrefix) {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func dimFromQdrantCollection(name string) (int, error) {
	if !validQdrantCollectionName(name) {
		return 0, fmt.Errorf("invalid qdrant collection name %q", name)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, qdrantCollectionPrefix))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid qdrant collection dimension in %q", name)
	}
	return n, nil
}
