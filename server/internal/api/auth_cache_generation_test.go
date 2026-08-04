package api

import (
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

type blockingAuthCache struct {
	cache.Cache
	setStarted chan struct{}
	releaseSet chan struct{}
	once       sync.Once
}

func (c *blockingAuthCache) Set(key, value string, ttl time.Duration) {
	shouldBlock := false
	if strings.HasPrefix(key, "auth:user:") {
		c.once.Do(func() {
			shouldBlock = true
			close(c.setStarted)
		})
	}
	if shouldBlock {
		<-c.releaseSet
	}
	c.Cache.Set(key, value, ttl)
}

func TestAuthCacheInvalidationCannotBeUndoneByInflightRefill(t *testing.T) {
	d := newAuthSecurityDeps(t, "auth-cache-generation.db")
	user, err := store.CreateUser(t.Context(), d.DB, "cache-race@example.test", "Cache Race", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	base := d.Cache
	blocking := &blockingAuthCache{
		Cache: base, setStarted: make(chan struct{}), releaseSet: make(chan struct{}),
	}
	d.Cache = blocking
	oldKey := authUserCacheKey(d, user.ID)

	loaded := make(chan *store.User, 1)
	errs := make(chan error, 1)
	go func() {
		got, loadErr := cachedAuthUser(t.Context(), d, user.ID)
		loaded <- got
		errs <- loadErr
	}()

	select {
	case <-blocking.setStarted:
	case <-time.After(time.Second):
		t.Fatal("auth cache refill did not reach the blocked Set")
	}
	if err := store.BumpTokenVersion(t.Context(), d.DB, user.ID); err != nil {
		t.Fatalf("bump token version: %v", err)
	}
	invalidateAuthUser(d, user.ID)
	newKey := authUserCacheKey(d, user.ID)
	if newKey == oldKey {
		t.Fatalf("cache generation did not advance: %q", newKey)
	}
	close(blocking.releaseSet)

	if err := <-errs; err != nil {
		t.Fatalf("in-flight cache load: %v", err)
	}
	if got := <-loaded; got == nil || got.TokenVer != user.TokenVer {
		t.Fatalf("in-flight load = %+v, want pre-invalidation token version %d", got, user.TokenVer)
	}
	if _, ok := base.Get(oldKey); !ok {
		t.Fatal("test did not reproduce the stale refill into the old generation")
	}

	fresh, err := cachedAuthUser(t.Context(), d, user.ID)
	if err != nil {
		t.Fatalf("load current cache generation: %v", err)
	}
	if fresh.TokenVer != user.TokenVer+1 {
		t.Fatalf("current generation token version = %d, want %d", fresh.TokenVer, user.TokenVer+1)
	}
}
