// Package sse contains the small HTTP/SSE helper used by every streaming
// endpoint. Keeps content-type, headers and flush logic in one place so the
// handlers stay focused on business logic.
package sse

import (
	"encoding/json"
	"net/http"
	"sync"
)

// Writer wraps an http.ResponseWriter and exposes a simple `Send(event)`
// method. Send/Ping are safe to call concurrently: the orchestrator emits
// events from concurrent tool goroutines (runToolsConcurrent) while a separate
// keep-alive goroutine calls Ping, and the underlying http.ResponseWriter is
// NOT concurrency-safe — so every write is serialised through `mu`.
type Writer struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
}

// New creates a Writer for the request. Returns nil when the underlying
// ResponseWriter does not support flushing (no SSE possible).
func New(w http.ResponseWriter) *Writer {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache, no-transform")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("x-accel-buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &Writer{w: w, flusher: f}
}

// Send marshals payload as JSON and writes one SSE event. The optional event
// name appears as the "event:" line.
func (s *Writer) Send(payload any, eventName string) error {
	if s == nil {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if eventName != "" {
		_, _ = s.w.Write([]byte("event: " + eventName + "\n"))
	}
	_, _ = s.w.Write([]byte("data: "))
	_, _ = s.w.Write(body)
	_, _ = s.w.Write([]byte("\n\n"))
	s.flusher.Flush()
	return nil
}

// Ping writes a comment line to keep proxies from timing out.
func (s *Writer) Ping() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write([]byte(": ping\n\n"))
	s.flusher.Flush()
}
