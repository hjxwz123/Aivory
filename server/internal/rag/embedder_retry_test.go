package rag

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func configureDashScopeRetryTest(t *testing.T, attemptTimeout, headerTimeout time.Duration) *httpEmbedder {
	t.Helper()
	t.Setenv("AIVORY_RAG_MAX_ATTEMPTS", "2")
	oldClient, oldTimeout := dashScopeEmbedHTTPClient, dashScopeEmbedAttemptTimeout
	oldBase, oldCap, oldJitter := embeddingRetryDelayBase, embeddingRetryDelayCap, embeddingRetryJitter
	client := newEmbeddingHTTPClient(false)
	client.Transport.(*http.Transport).ResponseHeaderTimeout = headerTimeout
	dashScopeEmbedHTTPClient, dashScopeEmbedAttemptTimeout = client, attemptTimeout
	embeddingRetryDelayBase, embeddingRetryDelayCap, embeddingRetryJitter = 0, 0, 0
	t.Cleanup(func() {
		client.CloseIdleConnections()
		dashScopeEmbedHTTPClient, dashScopeEmbedAttemptTimeout = oldClient, oldTimeout
		embeddingRetryDelayBase, embeddingRetryDelayCap, embeddingRetryJitter = oldBase, oldCap, oldJitter
	})
	return &httpEmbedder{baseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"}
}

func TestDashScopeResponseHeaderTimeoutRetries(t *testing.T) {
	for _, recoverOnRetry := range []bool{true, false} {
		name := "exhausted"
		if recoverOnRetry {
			name = "recovered"
		}
		t.Run(name, func(t *testing.T) {
			e := configureDashScopeRetryTest(t, time.Second, 20*time.Millisecond)
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if hits.Add(1) > 1 && recoverOnRetry {
					_, _ = io.WriteString(w, `{"data":[{"embedding":[1,2]}]}`)
					return
				}
				<-r.Context().Done()
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			parsed, err := e.postEmbeddings(ctx, srv.URL, []byte(`{"input":"hello"}`))
			if recoverOnRetry {
				if err != nil || len(parsed.Data) != 1 {
					t.Fatalf("retry did not recover: data=%v err=%v", parsed.Data, err)
				}
			} else {
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Fatalf("err = %v, want underlying network timeout", err)
				}
				if !strings.Contains(err.Error(), "after 2 attempts:") || strings.Contains(err.Error(), "did not return a response within") {
					t.Fatalf("misleading header timeout diagnostic: %v", err)
				}
			}
			if got := hits.Load(); got != 2 {
				t.Fatalf("request hits = %d, want 2", got)
			}
		})
	}
}

func TestDashScopeAttemptTimeoutStartsAfterProviderSlot(t *testing.T) {
	const attemptTimeout = 50 * time.Millisecond
	e := configureDashScopeRetryTest(t, attemptTimeout, time.Second)
	oldSlots := dashScopeEmbeddingSlots
	dashScopeEmbeddingSlots = make(chan struct{}, 1)
	t.Cleanup(func() { dashScopeEmbeddingSlots = oldSlots })
	release, err := e.acquireProviderSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"embedding":[1,2]}]}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.postEmbeddings(ctx, srv.URL, []byte(`{"input":"hello"}`))
		done <- err
	}()
	// Spend more than the attempt budget in the local queue. The provider must
	// still get a fresh budget once its slot becomes available.
	timer := time.NewTimer(3 * attemptTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		t.Fatalf("request finished before its provider slot was available: %v", err)
	case <-timer.C:
	}
	release()
	release = nil
	if err := <-done; err != nil {
		t.Fatalf("local queue consumed the provider request budget: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("request hits = %d, want 1", got)
	}
}
