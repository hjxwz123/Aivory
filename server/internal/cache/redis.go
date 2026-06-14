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
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	v, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return v, true
}

func (c *redisCache) Set(key, value string, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	_ = c.rdb.Set(ctx, key, value, ttl).Err()
}

func (c *redisCache) Delete(key string) {
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	_ = c.rdb.Del(ctx, key).Err()
}

// Incr atomically increments key. The TTL is applied only when the key is first
// created (result == 1), giving a fixed-window counter — matching the in-memory
// implementation's semantics (the window does not slide on each hit).
func (c *redisCache) Incr(key string, ttl time.Duration) int64 {
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0
	}
	if n == 1 && ttl > 0 {
		_ = c.rdb.Expire(ctx, key, ttl).Err()
	}
	return n
}

// IncrBy atomically adds delta to key (setting the TTL on creation), flooring
// at 0. Used for windowed cost quotas in integer micro-units.
func (c *redisCache) IncrBy(key string, delta int64, ttl time.Duration) int64 {
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
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
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
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
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	_ = c.rdb.Publish(ctx, topic, payload).Err()
}

// Subscribe returns a channel of payloads for topic and an unsubscribe func.
// The returned channel mirrors the in-memory impl: best-effort delivery with a
// small buffer; slow consumers drop messages rather than block the bridge.
func (c *redisCache) Subscribe(topic string) (chan string, func()) {
	ps := c.rdb.Subscribe(c.ctx, topic)
	out := make(chan string, 16)
	var once sync.Once
	done := make(chan struct{})

	go func() {
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
				default: // drop on backpressure
				}
			}
		}
	}()

	unsubscribe := func() {
		once.Do(func() {
			close(done)
			_ = ps.Close()
			close(out)
		})
	}
	return out, unsubscribe
}
