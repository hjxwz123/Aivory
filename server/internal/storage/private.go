package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aivory/server/internal/sandbox"

	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var ErrObjectNotFound = errors.New("storage: object not found")

// PrivateStore reads and writes application-owned private objects directly
// from the Go API. It deliberately has no sandbox URL or API key dependency.
type PrivateStore struct {
	client   *Client
	localDir string
}

func NewPrivateStore(storage *sandbox.StorageConfig, localDir string) *PrivateStore {
	return &PrivateStore{
		client:   New("", "", storage),
		localDir: strings.TrimSpace(localDir),
	}
}

func (s *PrivateStore) Enabled() bool {
	if s == nil || s.client == nil || s.client.Storage == nil || !s.client.Storage.Effective() {
		return false
	}
	if s.client.Storage.Provider == "local" {
		return s.localDir != ""
	}
	return DirectUploadSupported(s.client.Storage)
}

func (s *PrivateStore) Put(ctx context.Context, key string, data []byte, contentType string) (*PutResult, error) {
	if !s.Enabled() {
		return nil, fmt.Errorf("storage: direct private storage is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch s.client.Storage.Provider {
	case "s3":
		return s.putS3(ctx, key, data, contentType)
	case "aliyun_oss":
		return s.putAliyunOSS(ctx, key, data, contentType)
	case "local":
		return s.putLocal(ctx, key, data)
	default:
		return nil, fmt.Errorf("storage: unsupported direct provider %q", s.client.Storage.Provider)
	}
}

func (s *PrivateStore) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if !s.Enabled() {
		return nil, fmt.Errorf("storage: direct private storage is not configured")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("storage: invalid private object boundary")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch s.client.Storage.Provider {
	case "s3":
		return s.getS3(ctx, key, maxBytes)
	case "aliyun_oss":
		return s.getAliyunOSS(ctx, key, maxBytes)
	case "local":
		return s.getLocal(ctx, key, maxBytes)
	default:
		return nil, fmt.Errorf("storage: unsupported direct provider %q", s.client.Storage.Provider)
	}
}

func (s *PrivateStore) Delete(ctx context.Context, key string) error {
	if !s.Enabled() || strings.TrimSpace(key) == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch s.client.Storage.Provider {
	case "s3":
		return s.client.deleteDirectS3(ctx, key)
	case "aliyun_oss":
		return s.client.deleteDirectAliyunOSS(ctx, key)
	case "local":
		return s.deleteLocal(key)
	default:
		return nil
	}
}

func (s *PrivateStore) putS3(ctx context.Context, key string, data []byte, contentType string) (*PutResult, error) {
	cfg, fullKey, err := s.client.directS3Config(key)
	if err != nil {
		return nil, err
	}
	client, err := s.client.newDirectS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(cfg.bucket),
		Key:           aws.String(fullKey),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return nil, fmt.Errorf("direct %s put object: %w", cfg.provider, err)
	}
	return &PutResult{Provider: cfg.provider, Key: fullKey}, nil
}

func (s *PrivateStore) getS3(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	cfg, fullKey, err := s.client.directS3ConfigForExistingKey(key)
	if err != nil {
		return nil, err
	}
	client, err := s.client.newDirectS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	res, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(cfg.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("direct %s get object: %w", cfg.provider, err)
	}
	defer res.Body.Close()
	return readBoundedObject(res.Body, maxBytes)
}

func isS3NotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	code := strings.ToLower(apiErr.ErrorCode())
	return code == "nosuchkey" || code == "notfound" || code == "404"
}

func (s *PrivateStore) putAliyunOSS(ctx context.Context, key string, data []byte, contentType string) (*PutResult, error) {
	fullKey, err := s.client.fullObjectKey(key, false)
	if err != nil {
		return nil, err
	}
	bucket, err := s.client.aliyunOSSBucket()
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := bucket.PutObject(fullKey, bytes.NewReader(data), aliyunoss.ContentType(contentType)); err != nil {
		return nil, fmt.Errorf("direct aliyun_oss put object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &PutResult{Provider: "aliyun_oss", Key: fullKey}, nil
}

func (s *PrivateStore) getAliyunOSS(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	fullKey, err := s.client.fullObjectKey(key, true)
	if err != nil {
		return nil, err
	}
	bucket, err := s.client.aliyunOSSBucket()
	if err != nil {
		return nil, err
	}
	body, err := bucket.GetObject(fullKey)
	if err != nil {
		if isAliyunOSSNotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("direct aliyun_oss get object: %w", err)
	}
	defer body.Close()
	data, err := readBoundedObject(body, maxBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func isAliyunOSSNotFound(err error) bool {
	var serviceErr aliyunoss.ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr.StatusCode == 404 || strings.EqualFold(serviceErr.Code, "NoSuchKey")
	}
	var serviceErrPtr *aliyunoss.ServiceError
	if errors.As(err, &serviceErrPtr) && serviceErrPtr != nil {
		return serviceErrPtr.StatusCode == 404 || strings.EqualFold(serviceErrPtr.Code, "NoSuchKey")
	}
	var statusErr aliyunoss.UnexpectedStatusCodeError
	return errors.As(err, &statusErr) && statusErr.Got() == 404
}

func (s *PrivateStore) putLocal(ctx context.Context, key string, data []byte) (*PutResult, error) {
	fullKey, path, err := s.localObjectPath(key, false)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create local object directory: %w", err)
	}
	path, err = s.checkedLocalObjectPath(fullKey, false)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".aivory-object-*")
	if err != nil {
		return nil, fmt.Errorf("storage: create local object: %w", err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return nil, fmt.Errorf("storage: write local object: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return nil, fmt.Errorf("storage: chmod local object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("storage: close local object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("storage: publish local object: %w", err)
	}
	keep = true
	return &PutResult{Provider: "local", Key: fullKey}, nil
}

func (s *PrivateStore) getLocal(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	_, path, err := s.localObjectPath(key, true)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: open local object: %w", err)
	}
	defer f.Close()
	data, err := readBoundedObject(f, maxBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *PrivateStore) deleteLocal(key string) error {
	_, path, err := s.localObjectPath(key, true)
	if errors.Is(err, ErrObjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage: delete local object: %w", err)
	}
	return nil
}

func (s *PrivateStore) localObjectPath(key string, existing bool) (string, string, error) {
	fullKey, err := s.client.fullObjectKey(key, existing)
	if err != nil {
		return "", "", err
	}
	path, err := s.checkedLocalObjectPath(fullKey, existing)
	return fullKey, path, err
}

func (s *PrivateStore) checkedLocalObjectPath(fullKey string, existing bool) (string, error) {
	root, err := filepath.Abs(s.localDir)
	if err != nil {
		return "", fmt.Errorf("storage: resolve local storage root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("storage: create local storage root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("storage: resolve local storage root: %w", err)
	}
	target := filepath.Join(root, filepath.FromSlash(fullKey))
	if target == root || !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", fmt.Errorf("storage: object key escapes local storage root")
	}
	if existing {
		resolved, err := filepath.EvalSymlinks(target)
		if os.IsNotExist(err) {
			return "", ErrObjectNotFound
		}
		if err != nil {
			return "", fmt.Errorf("storage: resolve local object: %w", err)
		}
		if resolved == root || !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return "", fmt.Errorf("storage: local object escapes storage root")
		}
		return resolved, nil
	}
	parent := filepath.Dir(target)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if os.IsNotExist(err) {
		return target, nil
	}
	if err != nil {
		return "", fmt.Errorf("storage: resolve local object directory: %w", err)
	}
	if resolvedParent != root && !strings.HasPrefix(resolvedParent, root+string(filepath.Separator)) {
		return "", fmt.Errorf("storage: local object directory escapes storage root")
	}
	return filepath.Join(resolvedParent, filepath.Base(target)), nil
}

func readBoundedObject(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("storage: read private object: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("storage: private object exceeds %d bytes", maxBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("storage: private object is empty")
	}
	return data, nil
}
