package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aivory/server/internal/sandbox"
)

func TestPrivateStoreLocalRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := NewPrivateStore(&sandbox.StorageConfig{
		Provider: "local",
		Prefix:   "tenant/",
	}, root)
	if !store.Enabled() {
		t.Fatal("local private store should be enabled")
	}

	data := []byte("avatar-bytes")
	res, err := store.Put(context.Background(), "avatars/u1.png", data, "image/png")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if res.Provider != "local" || res.Key != "tenant/avatars/u1.png" || res.URL != "" {
		t.Fatalf("put result = %+v", res)
	}
	path := filepath.Join(root, "tenant", "avatars", "u1.png")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("stored bytes = %q", got)
	}

	got, err = store.Get(context.Background(), "avatars/u1.png", int64(len(data)))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("get bytes = %q", got)
	}
	if _, err := store.Get(context.Background(), "avatars/u1.png", 3); err == nil {
		t.Fatal("Get should enforce maxBytes")
	}
	if _, err := store.Put(context.Background(), "../escape.png", data, "image/png"); err == nil {
		t.Fatal("Put should reject path traversal")
	}

	if err := store.Delete(context.Background(), res.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(context.Background(), "avatars/u1.png", 100); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Get after delete error = %v", err)
	}
}

func TestPrivateStoreS3RoundTripWithoutSidecar(t *testing.T) {
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		switch r.Method {
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			objects[key] = data
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[key]
			if !ok {
				w.Header().Set("content-type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
				return
			}
			w.Header().Set("content-length", strconv.Itoa(len(data)))
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	store := NewPrivateStore(&sandbox.StorageConfig{
		Provider:    "s3",
		Prefix:      "workspaces/",
		S3Bucket:    "bucket",
		S3Region:    "us-east-1",
		S3Endpoint:  srv.URL,
		S3AccessKey: "ak",
		S3SecretKey: "sk",
	}, "")
	if !store.Enabled() {
		t.Fatal("S3 private store should be enabled without a sidecar URL")
	}

	data := []byte("direct-s3-avatar")
	res, err := store.Put(context.Background(), "avatars/u1.png", data, "image/png")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if res.Key != "workspaces/avatars/u1.png" || res.URL != "" {
		t.Fatalf("put result = %+v", res)
	}
	got, err := store.Get(context.Background(), "avatars/u1.png", 1024)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("get bytes = %q", got)
	}
	if err := store.Delete(context.Background(), res.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(context.Background(), "avatars/u1.png", 1024); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Get after delete error = %v", err)
	}
}

func TestPrivateStoreAliyunOSSDoesNotRequireSidecar(t *testing.T) {
	store := NewPrivateStore(&sandbox.StorageConfig{
		Provider:           "aliyun_oss",
		Prefix:             "workspaces/",
		OSSBucket:          "bucket",
		OSSEndpoint:        "oss-cn-beijing.aliyuncs.com",
		OSSAccessKeyID:     "ak",
		OSSAccessKeySecret: "sk",
	}, "")
	if !store.Enabled() {
		t.Fatal("Aliyun OSS private store should be enabled without a sidecar URL")
	}
}
