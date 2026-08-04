// Package cache is the small in-memory shim that stands in for Redis in
// development. The same interface is satisfied by a future Redis driver.
package cache

import (
	"sync"
	"time"
)

// In-memory cache tuning knobs — overridable via env (see
// docs/config-reference.md); defaults preserve the previous hardcoded values.
var (
	memoryPubSubSubscriberChannelBuffer = 16
	memoryStreamEventRetentionCap       = 50000
	memoryStreamReadPageLimit           = 100
)

// Cache is the minimal surface we use across the codebase: KV with TTL and a
// pub-sub for kill signals and config invalidation.
type Cache interface {
	Get(key string) (string, bool)
	// Take atomically returns and deletes a value. It is the only safe primitive
	// for one-time credentials that must not be consumed by two concurrent requests.
	Take(key string) (string, bool)
	Set(key, value string, ttl time.Duration)
	// SetNX atomically stores a value only when the key does not already exist.
	SetNX(key, value string, ttl time.Duration) bool
	// CompareAndDelete atomically deletes key only when its current value matches.
	// This lets a caller validate a credential before consuming exactly that value.
	CompareAndDelete(key, expected string) bool
	Delete(key string)
	// TTL returns the remaining lifetime of an expiring key. The boolean is
	// false when the key is missing, expired, or has no expiry.
	TTL(key string) (time.Duration, bool)
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
	// StreamAppendIfAllowed atomically checks every deny key and appends only
	// when none exists. allowed=false, ok=true is an intentional revocation;
	// ok=false is a cache failure. Generation streams use this distinction to
	// fail closed after access revocation without treating an outage as a revoke.
	StreamAppendIfAllowed(key string, denyKeys []string, value string, ttl time.Duration) (id string, allowed, ok bool)
	// StreamReadIfAllowed provides the matching atomic read boundary. A revoke
	// cannot land between checking its tombstone and reading already-buffered data.
	StreamReadIfAllowed(key string, denyKeys []string, afterID string, limit int) (events []StreamEvent, allowed, ok bool)
	// StreamRevoke atomically creates a tombstone and deletes all buffered events.
	StreamRevoke(key, tombstoneKey string, ttl time.Duration) bool
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

func (m *memory) Take(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.store[key]
	if !ok {
		return "", false
	}
	delete(m.store, key)
	if e.exp > 0 && time.Now().UnixNano() > e.exp {
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

func (m *memory) SetNX(key, value string, ttl time.Duration) bool {
	now := time.Now()
	exp := int64(0)
	if ttl > 0 {
		exp = now.Add(ttl).UnixNano()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.store[key]; ok && (current.exp == 0 || now.UnixNano() <= current.exp) {
		return false
	}
	m.store[key] = memoryEntry{value: value, exp: exp}
	return true
}

func (m *memory) CompareAndDelete(key, expected string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.store[key]
	if !ok {
		return false
	}
	if e.exp > 0 && time.Now().UnixNano() > e.exp {
		delete(m.store, key)
		return false
	}
	if e.value != expected {
		return false
	}
	delete(m.store, key)
	return true
}

func (m *memory) Delete(key string) {
	m.mu.Lock()
	delete(m.store, key)
	m.mu.Unlock()
}

func (m *memory) TTL(key string) (time.Duration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.store[key]
	if !ok || e.exp <= 0 {
		return 0, false
	}
	remaining := time.Until(time.Unix(0, e.exp))
	if remaining <= 0 {
		delete(m.store, key)
		return 0, false
	}
	return remaining, true
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

func (m *memory) StreamAppendIfAllowed(key string, denyKeys []string, value string, ttl time.Duration) (string, bool, bool) {
	now := time.Now()
	exp := int64(0)
	if ttl > 0 {
		exp = now.Add(ttl).UnixNano()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, denyKey := range denyKeys {
		if denyKey == "" {
			continue
		}
		if entry, exists := m.store[denyKey]; exists {
			if entry.exp == 0 || now.UnixNano() <= entry.exp {
				return "", false, true
			}
			delete(m.store, denyKey)
		}
	}
	m.streamSeq++
	id := formatInt(now.UnixMilli()) + "-" + formatInt(m.streamSeq)
	stream := m.streams[key]
	if stream.exp > 0 && now.UnixNano() > stream.exp {
		stream = memoryStream{}
	}
	stream.events = append(stream.events, StreamEvent{ID: id, Value: value})
	if len(stream.events) > memoryStreamEventRetentionCap {
		stream.events = append([]StreamEvent(nil), stream.events[len(stream.events)-memoryStreamEventRetentionCap:]...)
	}
	stream.exp = exp
	m.streams[key] = stream
	return id, true, true
}

func (m *memory) StreamReadIfAllowed(key string, denyKeys []string, afterID string, limit int) ([]StreamEvent, bool, bool) {
	if limit <= 0 {
		limit = memoryStreamReadPageLimit
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, denyKey := range denyKeys {
		if denyKey == "" {
			continue
		}
		if entry, exists := m.store[denyKey]; exists {
			if entry.exp == 0 || now.UnixNano() <= entry.exp {
				return nil, false, true
			}
			delete(m.store, denyKey)
		}
	}
	stream, exists := m.streams[key]
	if !exists {
		return nil, true, true
	}
	if stream.exp > 0 && now.UnixNano() > stream.exp {
		delete(m.streams, key)
		return nil, true, true
	}
	start := 0
	if afterID != "" {
		start = len(stream.events)
		for index, event := range stream.events {
			if event.ID == afterID {
				start = index + 1
				break
			}
		}
	}
	if start >= len(stream.events) {
		return []StreamEvent{}, true, true
	}
	end := start + limit
	if end > len(stream.events) {
		end = len(stream.events)
	}
	return append([]StreamEvent(nil), stream.events[start:end]...), true, true
}

func (m *memory) StreamRevoke(key, tombstoneKey string, ttl time.Duration) bool {
	exp := int64(0)
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixNano()
	}
	m.mu.Lock()
	m.store[tombstoneKey] = memoryEntry{value: "1", exp: exp}
	delete(m.streams, key)
	m.mu.Unlock()
	return true
}
