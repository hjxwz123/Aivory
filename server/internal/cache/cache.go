// Package cache is the small in-memory shim that stands in for Redis in
// development. The same interface is satisfied by a future Redis driver.
package cache

import (
	"sync"
	"time"
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
}

type memoryEntry struct {
	value string
	exp   int64 // unix nanos; 0 = no expiry
}

// memory is a goroutine-safe, in-process implementation. Tuned to be simple,
// not fast — we expect single-digit ops/sec from the dev profile.
type memory struct {
	mu     sync.RWMutex
	store  map[string]memoryEntry
	subsMu sync.Mutex
	subs   map[string][]chan string
}

// NewMemory constructs a fresh in-memory cache.
func NewMemory() Cache {
	return &memory{
		store: map[string]memoryEntry{},
		subs:  map[string][]chan string{},
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
	m.subsMu.Lock()
	subs := append([]chan string{}, m.subs[topic]...)
	m.subsMu.Unlock()
	for _, c := range subs {
		select {
		case c <- payload:
		default:
		}
	}
}

func (m *memory) Subscribe(topic string) (chan string, func()) {
	ch := make(chan string, 16)
	m.subsMu.Lock()
	m.subs[topic] = append(m.subs[topic], ch)
	m.subsMu.Unlock()
	return ch, func() {
		m.subsMu.Lock()
		subs := m.subs[topic]
		for i, c := range subs {
			if c == ch {
				m.subs[topic] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		m.subsMu.Unlock()
		close(ch)
	}
}
