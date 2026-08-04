// Redis-backed implementation of the Cache interface used in production. The
// same surface (KV+TTL, atomic Incr, pub/sub) is served by NewMemory in dev.
//
// Pub/sub here is cross-process: a "stop"/"kill" signal published on one API
// instance reaches subscribers on every instance, which is exactly what the
// multi-replica deployment needs for §8.1 realtime bans and §11.5 stop-stream.
package cache

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisOpTimeout bounds every per-operation Redis command.
var redisOpTimeout = 3 * time.Second

var incrWithTTLScript = redis.NewScript(`
local n = redis.call("INCR", KEYS[1])
if n == 1 and tonumber(ARGV[1]) > 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return n
`)

var takeScript = redis.NewScript(`
local value = redis.call("GET", KEYS[1])
if not value then
  return false
end
redis.call("DEL", KEYS[1])
return value
`)

var compareAndDeleteScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

var streamAppendIfAllowedScript = redis.NewScript(`
for index = 2, #KEYS do
  if redis.call("EXISTS", KEYS[index]) == 1 then
    return {0}
  end
end
local id = redis.call("XADD", KEYS[1], "MAXLEN", "~", 50000, "*", "data", ARGV[1])
if tonumber(ARGV[2]) > 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return {1, id}
`)

var streamReadIfAllowedScript = redis.NewScript(`
for index = 2, #KEYS do
  if redis.call("EXISTS", KEYS[index]) == 1 then
    return {0}
  end
end
local rows = redis.call("XRANGE", KEYS[1], ARGV[1], "+", "COUNT", ARGV[2])
local result = {1}
for _, row in ipairs(rows) do
  local data = ""
  for field = 1, #row[2], 2 do
    if row[2][field] == "data" then
      data = row[2][field + 1]
      break
    end
  end
  if data ~= "" then
    table.insert(result, row[1])
    table.insert(result, data)
  end
end
return result
`)

var streamRevokeScript = redis.NewScript(`
if tonumber(ARGV[1]) > 0 then
  redis.call("SET", KEYS[2], "1", "PX", ARGV[1])
else
  redis.call("SET", KEYS[2], "1")
end
redis.call("DEL", KEYS[1])
return 1
`)

type redisCache struct {
	rdb *redis.Client
	ctx context.Context
}

// NewRedis dials the Redis server at url (redis://… or rediss://…) and returns
// a Cache. It pings once so a misconfiguration fails fast at startup.
func NewRedis(url string) (Cache, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return &redisCache{rdb: rdb, ctx: context.Background()}, nil
}

func (c *redisCache) Get(key string) (string, bool) {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	v, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return v, true
}

func (c *redisCache) Take(key string) (string, bool) {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	v, err := takeScript.Run(ctx, c.rdb, []string{key}).Text()
	if err != nil {
		return "", false
	}
	return v, true
}

func (c *redisCache) Set(key, value string, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	_ = c.rdb.Set(ctx, key, value, ttl).Err()
}

func (c *redisCache) SetNX(key, value string, ttl time.Duration) bool {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	ok, err := c.rdb.SetNX(ctx, key, value, ttl).Result()
	return err == nil && ok
}

func (c *redisCache) CompareAndDelete(key, expected string) bool {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	n, err := compareAndDeleteScript.Run(ctx, c.rdb, []string{key}, expected).Int64()
	return err == nil && n == 1
}

func (c *redisCache) Delete(key string) {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	_ = c.rdb.Del(ctx, key).Err()
}

func (c *redisCache) TTL(key string) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	ttl, err := c.rdb.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 {
		return 0, false
	}
	return ttl, true
}

// Incr atomically increments key. The TTL is applied only when the key is first
// created (result == 1), giving a fixed-window counter — matching the in-memory
// implementation's semantics (the window does not slide on each hit).
func (c *redisCache) Incr(key string, ttl time.Duration) int64 {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	n, err := incrWithTTLScript.Run(ctx, c.rdb, []string{key}, ttl.Milliseconds()).Int64()
	if err != nil {
		return 0
	}
	return n
}

// IncrBy atomically adds delta to key (setting the TTL on creation), flooring
// at 0. Used for windowed cost quotas in integer micro-units.
func (c *redisCache) IncrBy(key string, delta int64, ttl time.Duration) int64 {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	n, err := c.rdb.IncrBy(ctx, key, delta).Result()
	if err != nil {
		return 0
	}
	if n == delta && ttl > 0 {
		_ = c.rdb.Expire(ctx, key, ttl).Err()
	}
	if n < 0 {
		_ = c.rdb.Set(ctx, key, 0, ttl).Err()
		return 0
	}
	return n
}

// Decr atomically decrements key, flooring at 0.
func (c *redisCache) Decr(key string) int64 {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	n, err := c.rdb.Decr(ctx, key).Result()
	if err != nil {
		return 0
	}
	// Floor at zero — avoid negative counter on race.
	if n < 0 {
		_ = c.rdb.Set(ctx, key, "0", redis.KeepTTL).Err()
		return 0
	}
	return n
}

func (c *redisCache) Publish(topic, payload string) {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	_ = c.rdb.Publish(ctx, topic, payload).Err()
}

// Subscribe returns a channel of payloads for topic and an unsubscribe func.
// The returned channel mirrors the in-memory impl: best-effort delivery with a
// small buffer; slow consumers drop messages rather than block the bridge.
func (c *redisCache) Subscribe(topic string) (chan string, func()) {
	ps := c.rdb.Subscribe(c.ctx, topic)
	// Wait for Redis to acknowledge SUBSCRIBE before callers perform their
	// post-subscribe marker check. Without this handshake, Set+Publish can land
	// after Get reports no marker but before the server has activated the channel,
	// losing a stop signal in the exact first-token race the marker is meant to
	// close. The in-memory cache is synchronous and needs no equivalent step.
	ackCtx, ackCancel := context.WithTimeout(c.ctx, redisOpTimeout)
	_, ackErr := ps.Receive(ackCtx)
	ackCancel()
	if ackErr != nil {
		_ = ps.Close()
		out := make(chan string)
		close(out)
		return out, func() {}
	}
	out := make(chan string, 16)
	var once sync.Once
	done := make(chan struct{})

	go func() {
		// The bridge goroutine is the SOLE sender on `out`, so it also owns closing
		// it. Closing `out` from unsubscribe (a different goroutine) while this one
		// is mid-send panics with "send on closed channel" and, being detached from
		// any request, escapes recoverMiddleware and crashes the whole replica.
		defer close(out)
		ch := ps.Channel()
		for {
			select {
			case <-done:
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- msg.Payload:
				case <-done: // tearing down — stop sending
					return
				default: // drop on backpressure
				}
			}
		}
	}()

	unsubscribe := func() {
		once.Do(func() {
			close(done)
			_ = ps.Close()
			// Do NOT close(out) here — the bridge goroutine closes it on return
			// (it is the only sender), which avoids the send-on-closed-channel race.
		})
	}
	return out, unsubscribe
}

func (c *redisCache) StreamAppend(key, value string, ttl time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	id, err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		Values: map[string]any{"data": value},
		MaxLen: 50000,
		Approx: true,
	}).Result()
	if err != nil {
		return "", false
	}
	if ttl > 0 {
		_ = c.rdb.Expire(ctx, key, ttl).Err()
	}
	return id, true
}

func (c *redisCache) StreamRead(key, afterID string, limit int) ([]StreamEvent, bool) {
	if limit <= 0 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	start := "-"
	if afterID != "" {
		start = "(" + afterID
	}
	rows, err := c.rdb.XRangeN(ctx, key, start, "+", int64(limit)).Result()
	if err != nil {
		return nil, false
	}
	out := make([]StreamEvent, 0, len(rows))
	for _, row := range rows {
		v, _ := row.Values["data"].(string)
		if v == "" {
			continue
		}
		out = append(out, StreamEvent{ID: row.ID, Value: v})
	}
	return out, true
}

func (c *redisCache) StreamAppendIfAllowed(key string, denyKeys []string, value string, ttl time.Duration) (string, bool, bool) {
	keys := make([]string, 0, 1+len(denyKeys))
	keys = append(keys, key)
	for _, denyKey := range denyKeys {
		if denyKey != "" {
			keys = append(keys, denyKey)
		}
	}
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	raw, err := streamAppendIfAllowedScript.Run(ctx, c.rdb, keys, value, ttl.Milliseconds()).Result()
	if err != nil {
		return "", false, false
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return "", false, false
	}
	if redisScriptInt(values[0]) == 0 {
		return "", false, true
	}
	if len(values) != 2 {
		return "", false, false
	}
	id, ok := redisScriptString(values[1])
	if !ok || id == "" {
		return "", false, false
	}
	return id, true, true
}

func (c *redisCache) StreamReadIfAllowed(key string, denyKeys []string, afterID string, limit int) ([]StreamEvent, bool, bool) {
	if limit <= 0 {
		limit = 100
	}
	keys := make([]string, 0, 1+len(denyKeys))
	keys = append(keys, key)
	for _, denyKey := range denyKeys {
		if denyKey != "" {
			keys = append(keys, denyKey)
		}
	}
	start := "-"
	if afterID != "" {
		start = "(" + afterID
	}
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	raw, err := streamReadIfAllowedScript.Run(ctx, c.rdb, keys, start, limit).Result()
	if err != nil {
		return nil, false, false
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, false, false
	}
	if redisScriptInt(values[0]) == 0 {
		return nil, false, true
	}
	if (len(values)-1)%2 != 0 {
		return nil, false, false
	}
	events := make([]StreamEvent, 0, (len(values)-1)/2)
	for index := 1; index < len(values); index += 2 {
		id, idOK := redisScriptString(values[index])
		value, valueOK := redisScriptString(values[index+1])
		if !idOK || !valueOK {
			return nil, false, false
		}
		events = append(events, StreamEvent{ID: id, Value: value})
	}
	return events, true, true
}

func (c *redisCache) StreamRevoke(key, tombstoneKey string, ttl time.Duration) bool {
	ctx, cancel := context.WithTimeout(c.ctx, redisOpTimeout)
	defer cancel()
	result, err := streamRevokeScript.Run(ctx, c.rdb, []string{key, tombstoneKey}, ttl.Milliseconds()).Int64()
	return err == nil && result == 1
}

func redisScriptInt(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		return 0
	}
}

func redisScriptString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}
