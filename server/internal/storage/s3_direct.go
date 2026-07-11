package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/sandbox"

	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const defaultStoragePrefix = "workspaces/"

// Direct-upload / OSS tunables (env-overridable; defaults preserve prior
// hardcoded behavior).
var (
	s3DirectUploadMinClientTimeout   = envcfg.Dur("AURELIA_STORAGE_S3_DIRECT_UPLOAD_MIN_CLIENT_TIMEOUT", 20*time.Minute)
	directS3OSSUploadHTTPClientTTL   = envcfg.Dur("AURELIA_STORAGE_DIRECT_S3_OSS_UPLOAD_HTTP_CLIENT", 20*time.Minute)
	aliyunOSSConnectTimeoutSec       = envcfg.Int64("AURELIA_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_CONNECT", 30)
	aliyunOSSReadWriteTimeoutSec     = envcfg.Int64("AURELIA_STORAGE_ALIYUN_OSS_CLIENT_CONNECT_READ_TIMEOUTS_RW", 300)
	presignURLTTLSeconds             = int(envcfg.Dur("AURELIA_STORAGE_PRESIGN_URL_TTL", 3600*time.Second) / time.Second)
	presignURLTTLClampCeilingSeconds = int(envcfg.Dur("AURELIA_STORAGE_PRESIGN_URL_TTL_CLAMP_CEILING", 86400*time.Second) / time.Second)
)

type directS3Config struct {
	provider     string
	bucket       string
	region       string
	endpoint     string
	accessKey    string
	secretKey    string
	usePathStyle bool
}

// DirectUploadSupported reports whether the storage block can be used by the
// Go backend itself for MinerU source uploads. It deliberately does NOT change
// Client.Enabled(), which remains the sandbox-sidecar readiness check used by
// workspace archiving and the legacy /storage endpoints.
func DirectUploadSupported(sc *sandbox.StorageConfig) bool {
	if sc == nil || !sc.Effective() {
		return false
	}
	return sc.Provider == "s3" || sc.Provider == "aliyun_oss"
}

// PutFileDirect uploads a local file directly from the Go backend to S3 or
// Aliyun OSS, then returns a presigned GET URL.
// This is used only by MinerU parsing so large scanned PDFs don't detour
// through the sandbox sidecar before OCR.
func (c *Client) PutFileDirect(ctx context.Context, key, filePath, contentType string, ttlSeconds int) (*PutResult, error) {
	if c == nil || !DirectUploadSupported(c.Storage) {
		return nil, fmt.Errorf("storage: direct S3/OSS upload is not configured")
	}
	if c.Storage.Provider == "aliyun_oss" {
		return c.putDirectAliyunOSS(ctx, key, filePath, contentType, ttlSeconds)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return c.putDirectS3(ctx, key, f, info.Size(), contentType, ttlSeconds)
}

// DeleteDirect removes an object uploaded by PutFileDirect. Kept separate from
// Delete so normal storage cleanup keeps using the existing sidecar path.
func (c *Client) DeleteDirect(ctx context.Context, key string) error {
	if c == nil || !DirectUploadSupported(c.Storage) || strings.TrimSpace(key) == "" {
		return nil
	}
	if c.Storage.Provider == "aliyun_oss" {
		return c.deleteDirectAliyunOSS(ctx, key)
	}
	return c.deleteDirectS3(ctx, key)
}

func (c *Client) putDirectS3(ctx context.Context, key string, body io.Reader, contentLength int64, contentType string, ttlSeconds int) (*PutResult, error) {
	cfg, fullKey, err := c.directS3Config(key)
	if err != nil {
		return nil, err
	}
	client, err := c.newDirectS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(cfg.bucket),
		Key:           aws.String(fullKey),
		Body:          body,
		ContentLength: aws.Int64(contentLength),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return nil, fmt.Errorf("direct %s put object: %w", cfg.provider, err)
	}
	ttl := storageTTL(ttlSeconds)
	presigned, err := s3.NewPresignClient(client).PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(cfg.bucket),
		Key:    aws.String(fullKey),
	}, s3.WithPresignExpires(time.Duration(ttl)*time.Second))
	if err != nil {
		return nil, fmt.Errorf("direct %s presign get: %w", cfg.provider, err)
	}
	return &PutResult{
		Provider:  cfg.provider,
		Key:       fullKey,
		URL:       presigned.URL,
		ExpiresIn: ttl,
	}, nil
}

func (c *Client) deleteDirectS3(ctx context.Context, key string) error {
	cfg, fullKey, err := c.directS3ConfigForExistingKey(key)
	if err != nil {
		return err
	}
	client, err := c.newDirectS3Client(ctx, cfg)
	if err != nil {
		return err
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(cfg.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return fmt.Errorf("direct %s delete object: %w", cfg.provider, err)
	}
	return nil
}

func (c *Client) newDirectS3Client(ctx context.Context, cfg directS3Config) (*s3.Client, error) {
	httpClient := c.client
	if httpClient == nil || httpClient.Timeout < s3DirectUploadMinClientTimeout {
		httpClient = &http.Client{Timeout: directS3OSSUploadHTTPClientTTL}
	}
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.region),
		config.WithHTTPClient(httpClient),
		// S3-compatible backends frequently don't support the newer optional
		// checksum headers/trailers AWS SDKs enable by default. Only send them
		// when the modeled operation requires them.
		config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		config.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	}
	if cfg.accessKey != "" || cfg.secretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.accessKey, cfg.secretKey, ""),
		))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.usePathStyle
		if cfg.endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.endpoint)
		}
	}), nil
}

func (c *Client) directS3Config(key string) (directS3Config, string, error) {
	fullKey, err := c.fullObjectKey(key, false)
	if err != nil {
		return directS3Config{}, "", err
	}
	cfg, err := directS3ConfigFromStorage(c.Storage)
	if err != nil {
		return directS3Config{}, "", err
	}
	return cfg, fullKey, nil
}

func (c *Client) directS3ConfigForExistingKey(key string) (directS3Config, string, error) {
	fullKey, err := c.fullObjectKey(key, true)
	if err != nil {
		return directS3Config{}, "", err
	}
	cfg, err := directS3ConfigFromStorage(c.Storage)
	if err != nil {
		return directS3Config{}, "", err
	}
	return cfg, fullKey, nil
}

func directS3ConfigFromStorage(sc *sandbox.StorageConfig) (directS3Config, error) {
	if sc == nil {
		return directS3Config{}, fmt.Errorf("storage: missing config")
	}
	switch sc.Provider {
	case "s3":
		region := strings.TrimSpace(sc.S3Region)
		if region == "" {
			region = "us-east-1"
		}
		return directS3Config{
			provider:     "s3",
			bucket:       strings.TrimSpace(sc.S3Bucket),
			region:       region,
			endpoint:     normalizeEndpoint(sc.S3Endpoint),
			accessKey:    strings.TrimSpace(sc.S3AccessKey),
			secretKey:    strings.TrimSpace(sc.S3SecretKey),
			usePathStyle: strings.TrimSpace(sc.S3Endpoint) != "",
		}, nil
	default:
		return directS3Config{}, fmt.Errorf("storage: provider %q does not support direct S3 upload", sc.Provider)
	}
}

func (c *Client) putDirectAliyunOSS(ctx context.Context, key, filePath, contentType string, ttlSeconds int) (*PutResult, error) {
	fullKey, err := c.fullObjectKey(key, false)
	if err != nil {
		return nil, err
	}
	bucket, err := c.aliyunOSSBucket()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := bucket.PutObjectFromFile(fullKey, filePath, aliyunoss.ContentType(contentType)); err != nil {
		return nil, fmt.Errorf("direct aliyun_oss put object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ttl := storageTTL(ttlSeconds)
	signedURL, err := bucket.SignURL(fullKey, aliyunoss.HTTPGet, int64(ttl))
	if err != nil {
		return nil, fmt.Errorf("direct aliyun_oss sign get: %w", err)
	}
	return &PutResult{
		Provider:  "aliyun_oss",
		Key:       fullKey,
		URL:       signedURL,
		ExpiresIn: ttl,
	}, nil
}

func (c *Client) deleteDirectAliyunOSS(ctx context.Context, key string) error {
	fullKey, err := c.fullObjectKey(key, true)
	if err != nil {
		return err
	}
	bucket, err := c.aliyunOSSBucket()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := bucket.DeleteObject(fullKey); err != nil {
		return fmt.Errorf("direct aliyun_oss delete object: %w", err)
	}
	return ctx.Err()
}

func (c *Client) aliyunOSSBucket() (*aliyunoss.Bucket, error) {
	if c == nil || c.Storage == nil {
		return nil, fmt.Errorf("storage: missing config")
	}
	sc := c.Storage
	endpoint := normalizeAliyunOSSEndpoint(sc.OSSEndpoint)
	bucketName := strings.TrimSpace(sc.OSSBucket)
	accessKeyID := strings.TrimSpace(sc.OSSAccessKeyID)
	accessKeySecret := strings.TrimSpace(sc.OSSAccessKeySecret)
	if endpoint == "" || bucketName == "" || accessKeyID == "" || accessKeySecret == "" {
		return nil, fmt.Errorf("storage: incomplete aliyun_oss config")
	}
	client, err := aliyunoss.New(endpoint, accessKeyID, accessKeySecret, aliyunoss.Timeout(aliyunOSSConnectTimeoutSec, aliyunOSSReadWriteTimeoutSec))
	if err != nil {
		return nil, fmt.Errorf("direct aliyun_oss client: %w", err)
	}
	bucket, err := client.Bucket(bucketName)
	if err != nil {
		return nil, fmt.Errorf("direct aliyun_oss bucket: %w", err)
	}
	return bucket, nil
}

func (c *Client) fullObjectKey(key string, existing bool) (string, error) {
	key = strings.TrimSpace(key)
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.Contains(key, "\x00") || strings.Contains(key, "..") {
		return "", fmt.Errorf("storage: invalid object key")
	}
	prefix := storagePrefix(c.Storage)
	if existing && (key == strings.Trim(prefix, "/") || strings.HasPrefix(key, strings.Trim(prefix, "/")+"/")) {
		return key, nil
	}
	if prefix == "" {
		return key, nil
	}
	return prefix + key, nil
}

func storagePrefix(sc *sandbox.StorageConfig) string {
	prefix := defaultStoragePrefix
	if sc != nil && strings.TrimSpace(sc.Prefix) != "" {
		prefix = strings.TrimSpace(sc.Prefix)
	}
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

func storageTTL(seconds int) int {
	if seconds <= 0 {
		return presignURLTTLSeconds
	}
	if seconds > presignURLTTLClampCeilingSeconds {
		return presignURLTTLClampCeilingSeconds
	}
	return seconds
}

func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	return strings.TrimRight(endpoint, "/")
}

func normalizeAliyunOSSEndpoint(endpoint string) string {
	endpoint = normalizeEndpoint(endpoint)
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return endpoint
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(host, "s3.oss-") {
		host = strings.TrimPrefix(host, "s3.")
		if port := u.Port(); port != "" {
			u.Host = host + ":" + port
		} else {
			u.Host = host
		}
		return strings.TrimRight(u.String(), "/")
	}
	return endpoint
}
