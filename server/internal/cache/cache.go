// Package cache is the small in-memory shim that stands in for Redis in
// development. The same interface is satisfied by a future Redis driver.
package cache

import (
	"sync"
	"time"

	"aurelia/server/internal/envcfg"
)

// In-memory cache tuning knobs — overridable via env (see
// docs/config-reference.md); defaults preserve the previous hardcoded values.
var (
	memoryPubSubSubscriberChannelBuffer = envcfg.Int("AURELIA_CACHE_MEMORY_PUB_SUB_SUBSCRIBER_CHANNEL_BUFFER", 16)
	memoryStreamEventRetentionCap       = envcfg.Int("AURELIA_CACHE_MEMORY_STREAM_EVENT_RETENTION_CAP", 50000)
	memoryStreamReadPageLimit           = envcfg.Int("AURELIA_CACHE_MEMORY_STREAMREAD_PAGE_LIMIT", 100)
)

// Cache is the minimal surface we use across the codebase: KV with TTL and a
// pub-sub for kill signals and config invalidation.
type Cache interface {
	Get(key string) (string, bool)
	Set(key, value string, ttl time.Duration)
	Delete(key string)
	Incr(key string, ttl time.Duration) int64
	// IncrBy atomically adds delta to a key (creating it with the TTL if absent),
	// flooring at 0. Returns the new value. Used for non-negative accumulators
	// like windowed cost quotas (stored in integer micro-units).
	IncrBy(key string, delta int64, ttl time.Duration) int64
	// Decr atomically decrements a key, flooring at 0. Returns the new value.
	Decr(key string) int64
	Publish(topic string, payload string)
	Subscribe(topic string) (chan string, func())
	StreamAppend(key, value string, ttl time.Duration) (string, bool)
	StreamRead(key, afterID string, limit int) ([]StreamEvent, bool)
}

type memoryEntry struct {
	value string
	exp   int64 // unix nanos; 0 = no expiry
}

// StreamEvent is one durable-enough event in an append-only cache stream. Redis
// backs this with XADD/XRANGE; the in-memory implementation mirrors the same
// contract for local development and tests.
type StreamEvent struct {
	ID    string
	Value string
}

// memory is a goroutine-safe, in-process implementation. Tuned to be simple,
// not fast — we expect single-digit ops/sec from the dev profile.
type memory struct {
	mu        sync.RWMutex
	store     map[string]memoryEntry
	subsMu    sync.Mutex
	subs      map[string][]chan string
	streams   map[string]memoryStream
	streamSeq int64
}

type memoryStream struct {
	events []StreamEvent
	exp    int64 // unix nanos; 0 = no expiry
}

// NewMemory constructs a fresh in-memory cache.
func NewMemory() Cache {
	return &memory{
		store:   map[string]memoryEntry{},
		subs:    map[string][]chan string{},
		streams: map[string]memoryStream{},
	}
}

func (m *memory) Get(key string) (string, bool) {
	m.mu.RLock()
	e, ok := m.store[key]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	if e.exp > 0 && time.Now().UnixNano() > e.exp {
		m.mu.Lock()
		delete(m.store, key)
		m.mu.Unlock()
		return "", false
	}
	return e.value, true
}

func (m *memory) Set(key, value string, ttl time.Duration) {
	exp := int64(0)
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixNano()
	}
	m.mu.Lock()
	m.store[key] = memoryEntry{value: value, exp: exp}
	m.mu.Unlock()
}

func (m *memory) Delete(key string) {
	m.mu.Lock()
	delete(m.store, key)
	m.mu.Unlock()
}

func (m *memory) Incr(key string, ttl time.Duration) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.store[key]
	if !ok || (e.exp > 0 && time.Now().UnixNano() > e.exp) {
		exp := int64(0)
		if ttl > 0 {
			exp = time.Now().Add(ttl).UnixNano()
		}
		m.store[key] = memoryEntry{value: "1", exp: exp}
		return 1
	}
	n := parseInt(e.value)
	n++
	e.value = formatInt(n)
	m.store[key] = e
	return n
}

func (m *memory) IncrBy(key string, delta int64, ttl time.Duration) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.store[key]
	if !ok || (e.exp > 0 && time.Now().UnixNano() > e.exp) {
		v := delta
		if v < 0 {
			v = 0
		}
		exp := int64(0)
		if ttl > 0 {
			exp = time.Now().Add(ttl).UnixNano()
		}
		m.store[key] = memoryEntry{value: formatInt(v), exp: exp}
		return v
	}
	n := parseInt(e.value) + delta
	if n < 0 {
		n = 0
	}
	e.value = formatInt(n)
	m.store[key] = e
	return n
}

func (m *memory) Decr(key string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.store[key]
	if !ok || (e.exp > 0 && time.Now().UnixNano() > e.exp) {
		return 0
	}
	n := parseInt(e.value)
	if n > 0 {
		n--
	}
	e.value = formatInt(n)
	m.store[key] = e
	return n
}

func parseInt(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

func (m *memory) Publish(topic, payload string) {
	// Hold subsMu for the WHOLE non-blocking send loop. Sends never block (the
	// select has a default), so this is cheap — and it makes unsubscribe's removal
	// + close(ch) (also under subsMu) mutually exclusive with the send, so a send
	// can never hit a channel that unsubscribe just closed (send-on-closed panic).
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for _, c := range m.subs[topic] {
		select {
		case c <- payload:
		default:
		}
	}
}

func (m *memory) Subscribe(topic string) (chan string, func()) {
	ch := make(chan string, memoryPubSubSubscriberChannelBuffer)
	m.subsMu.Lock()
	m.subs[topic] = append(m.subs[topic], ch)
	m.subsMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			m.subsMu.Lock()
			defer m.subsMu.Unlock()
			subs := m.subs[topic]
			for i, c := range subs {
				if c == ch {
					m.subs[topic] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
			// Close under the lock: Publish holds the same lock across its send loop,
			// so no send can be in flight on ch here.
			close(ch)
		})
	}
}

func (m *memory) StreamAppend(key, value string, ttl time.Duration) (string, bool) {
	exp := int64(0)
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixNano()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamSeq++
	id := formatInt(time.Now().UnixMilli()) + "-" + formatInt(m.streamSeq)
	s := m.streams[key]
	if s.exp > 0 && time.Now().UnixNano() > s.exp {
		s = memoryStream{}
	}
	s.events = append(s.events, StreamEvent{ID: id, Value: value})
	// Keep a generous cap for dev. Production Redis uses MAXLEN too; either way,
	// generation streams are for short-lived reconnect/replay, not archival.
	if len(s.events) > memoryStreamEventRetentionCap {
		s.events = append([]StreamEvent(nil), s.events[len(s.events)-memoryStreamEventRetentionCap:]...)
	}
	s.exp = exp
	m.streams[key] = s
	return id, true
}

func (m *memory) StreamRead(key, afterID string, limit int) ([]StreamEvent, bool) {
	if limit <= 0 {
		limit = memoryStreamReadPageLimit
	}
	m.mu.RLock()
	s, ok := m.streams[key]
	m.mu.RUnlock()
	if !ok {
		return nil, true
	}
	if s.exp > 0 && time.Now().UnixNano() > s.exp {
		m.mu.Lock()
		delete(m.streams, key)
		m.mu.Unlock()
		return nil, true
	}
	start := 0
	if afterID != "" {
		start = len(s.events)
		for i, ev := range s.events {
			if ev.ID == afterID {
				start = i + 1
				break
			}
		}
	}
	if start >= len(s.events) {
		return []StreamEvent{}, true
	}
	end := start + limit
	if end > len(s.events) {
		end = len(s.events)
	}
	out := append([]StreamEvent(nil), s.events[start:end]...)
	return out, true
}
