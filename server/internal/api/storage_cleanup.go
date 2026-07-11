package api

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/sandbox"
	"aurelia/server/internal/storage"
	"aurelia/server/internal/store"
)

var objectStorageDeleteTimeout = envcfg.Dur("AURELIA_API_OBJECT_STORAGE_DELETE_TIMEOUT_CLEANUP", 30*time.Second)

func cleanupStoragePaths(ctx context.Context, d Deps, paths []string, label string) {
	if len(paths) == 0 {
		return
	}
	seen := map[string]struct{}{}
	obj := objectStorageClient(d)
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		ref, err := store.StoragePathReferenced(ctx, d.DB, p)
		if err != nil {
			logStorageCleanup(d, "%s: check storage refs for %q: %v", label, p, err)
			continue
		}
		if ref {
			continue
		}
		if looksLocalStoragePath(p) {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				logStorageCleanup(d, "%s: remove local storage %q: %v", label, p, err)
			}
		}
		if key, ok := objectStorageKey(p, obj); ok {
			dctx, cancel := context.WithTimeout(context.Background(), objectStorageDeleteTimeout)
			if err := obj.Delete(dctx, key); err != nil {
				logStorageCleanup(d, "%s: delete object storage %q: %v", label, key, err)
			}
			cancel()
		}
	}
}

func cleanupRAGDocument(ctx context.Context, d Deps, docID, label string) {
	docID = strings.TrimSpace(docID)
	if d.RAG == nil || docID == "" {
		return
	}
	if err := d.RAG.OnDocumentDeleted(ctx, docID); err != nil {
		logStorageCleanup(d, "%s: drop vectors for document %s: %v", label, docID, err)
	}
}

func cleanupRAGKB(ctx context.Context, d Deps, kbID, label string) {
	kbID = strings.TrimSpace(kbID)
	if d.RAG == nil || kbID == "" {
		return
	}
	if err := d.RAG.OnKBDeleted(ctx, kbID); err != nil {
		logStorageCleanup(d, "%s: drop vectors for kb %s: %v", label, kbID, err)
	}
}

func cleanupRAGConversation(ctx context.Context, d Deps, convID, label string) {
	convID = strings.TrimSpace(convID)
	if d.RAG == nil || convID == "" {
		return
	}
	if err := d.RAG.OnConversationDeleted(ctx, convID); err != nil {
		logStorageCleanup(d, "%s: drop vectors for conversation %s: %v", label, convID, err)
	}
}

func objectStorageClient(d Deps) *storage.Client {
	provider := settingString(d, "storage_provider", "")
	if provider != "s3" && provider != "aliyun_oss" && provider != "local" {
		return nil
	}
	cfg := &sandbox.StorageConfig{
		Provider: provider,
		Prefix:   settingString(d, "storage_prefix", "workspaces/"),
	}
	switch provider {
	case "s3":
		cfg.S3Bucket = settingString(d, "storage_s3_bucket", "")
		cfg.S3Region = settingString(d, "storage_s3_region", "")
		cfg.S3Endpoint = settingString(d, "storage_s3_endpoint", "")
		cfg.S3AccessKey = settingString(d, "storage_s3_access_key", "")
		cfg.S3SecretKey = settingString(d, "storage_s3_secret_key", "")
	case "aliyun_oss":
		cfg.OSSBucket = settingString(d, "storage_aliyun_bucket", "")
		cfg.OSSEndpoint = settingString(d, "storage_aliyun_endpoint", "")
		cfg.OSSAccessKeyID = settingString(d, "storage_aliyun_access_key_id", "")
		cfg.OSSAccessKeySecret = settingString(d, "storage_aliyun_access_key_secret", "")
	}
	if !cfg.Effective() {
		return nil
	}
	base := settingString(d, "sandbox_base_url", d.Config.SandboxBaseURL)
	key := settingString(d, "sandbox_api_key", d.Config.SandboxAPIKey)
	return storage.New(base, key, cfg)
}

func objectStorageKey(p string, c *storage.Client) (string, bool) {
	if c == nil || !c.Enabled() {
		return "", false
	}
	switch {
	case strings.HasPrefix(p, "storage://"):
		key := strings.TrimPrefix(p, "storage://")
		return strings.TrimLeft(key, "/"), strings.TrimSpace(key) != ""
	case strings.HasPrefix(p, "s3://") || strings.HasPrefix(p, "oss://") || strings.HasPrefix(p, "aliyun_oss://"):
		u, err := url.Parse(p)
		if err != nil {
			return "", false
		}
		key := strings.TrimLeft(u.Path, "/")
		return key, key != ""
	default:
		prefix := strings.Trim(c.Storage.Prefix, "/")
		if prefix != "" && (p == prefix || strings.HasPrefix(p, prefix+"/")) {
			return p, true
		}
		return "", false
	}
}

func looksLocalStoragePath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || strings.Contains(p, "://") {
		return false
	}
	if strings.HasPrefix(p, "s3:") || strings.HasPrefix(p, "oss:") || strings.HasPrefix(p, "aliyun_oss:") || strings.HasPrefix(p, "storage:") {
		return false
	}
	return filepath.IsAbs(p) || strings.HasPrefix(p, ".") || strings.Contains(p, string(filepath.Separator))
}

func logStorageCleanup(d Deps, format string, args ...any) {
	if d.Logger != nil {
		d.Logger.Printf(format, args...)
	}
}
