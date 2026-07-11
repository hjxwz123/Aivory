package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/sandbox"
)

func TestPutFileDirectUsesS3CompatibleEndpointWithoutSidecar(t *testing.T) {
	var putPath, deletePath, putBody, putContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			putPath = r.URL.Path
			putContentType = r.Header.Get("content-type")
			b, _ := io.ReadAll(r.Body)
			putBody = string(b)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	p := filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(p, []byte("pdf-bytes"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	c := New("", "", &sandbox.StorageConfig{
		Provider:    "s3",
		Prefix:      "workspaces/",
		S3Bucket:    "bucket",
		S3Region:    "us-east-1",
		S3Endpoint:  srv.URL,
		S3AccessKey: "ak",
		S3SecretKey: "sk",
	})
	if c.Enabled() {
		t.Fatal("sidecar path should be disabled when BaseURL is empty")
	}
	if !DirectUploadSupported(c.Storage) {
		t.Fatal("direct S3 upload should be supported")
	}

	res, err := c.PutFileDirect(context.Background(), "mineru/u1/scan.pdf", p, "application/pdf", 3600)
	if err != nil {
		t.Fatalf("PutFileDirect: %v", err)
	}
	if res.Key != "workspaces/mineru/u1/scan.pdf" {
		t.Fatalf("key = %q", res.Key)
	}
	if !strings.Contains(res.URL, srv.URL) || !strings.Contains(res.URL, "/bucket/workspaces/mineru/u1/scan.pdf") {
		t.Fatalf("presigned URL does not target test S3 endpoint/path: %s", res.URL)
	}
	if putPath != "/bucket/workspaces/mineru/u1/scan.pdf" {
		t.Fatalf("put path = %q", putPath)
	}
	if putBody != "pdf-bytes" {
		t.Fatalf("put body = %q", putBody)
	}
	if !strings.HasPrefix(putContentType, "application/pdf") {
		t.Fatalf("content-type = %q", putContentType)
	}

	if err := c.DeleteDirect(context.Background(), res.Key); err != nil {
		t.Fatalf("DeleteDirect: %v", err)
	}
	if deletePath != "/bucket/workspaces/mineru/u1/scan.pdf" {
		t.Fatalf("delete path = %q", deletePath)
	}
}

func TestNormalizeAliyunOSSEndpointKeepsNativeOSSHost(t *testing.T) {
	got := normalizeAliyunOSSEndpoint("oss-cn-beijing.aliyuncs.com/")
	want := "https://oss-cn-beijing.aliyuncs.com"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestNormalizeAliyunOSSEndpointAcceptsLegacyS3Host(t *testing.T) {
	got := normalizeAliyunOSSEndpoint("https://s3.oss-cn-beijing.aliyuncs.com")
	want := "https://oss-cn-beijing.aliyuncs.com"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestAliyunOSSBucketUsesNativeSDKConfig(t *testing.T) {
	c := New("", "", &sandbox.StorageConfig{
		Provider:           "aliyun_oss",
		Prefix:             "workspaces/",
		OSSBucket:          "bucket-name",
		OSSEndpoint:        "https://s3.oss-cn-beijing.aliyuncs.com",
		OSSAccessKeyID:     "ak",
		OSSAccessKeySecret: "sk",
	})
	if c.Enabled() {
		t.Fatal("sidecar path should be disabled when BaseURL is empty")
	}
	if !DirectUploadSupported(c.Storage) {
		t.Fatal("direct Aliyun OSS upload should be supported")
	}
	bucket, err := c.aliyunOSSBucket()
	if err != nil {
		t.Fatalf("aliyunOSSBucket: %v", err)
	}
	if got, want := bucket.Client.Config.Endpoint, "https://oss-cn-beijing.aliyuncs.com"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}
