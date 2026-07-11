package api

import (
	"context"
	"encoding/json"
	"time"

	"aurelia/server/internal/store"
)

var (
	authUserCacheTTL             = 5 * time.Minute
	authUserCacheTTLGroupExpired = time.Second
)

func cachedAuthUser(ctx context.Context, d Deps, userID string) (*store.User, error) {
	if d.Cache == nil {
		return store.FindUserByID(ctx, d.DB, userID)
	}
	key := authUserCacheKey(d, userID)
	if raw, ok := d.Cache.Get(key); ok {
		var u store.User
		if json.Unmarshal([]byte(raw), &u) == nil {
			return &u, nil
		}
		d.Cache.Delete(key)
	}
	u, err := store.FindUserByID(ctx, d.DB, userID)
	if err != nil {
		return nil, err
	}
	cacheAuthUser(d, u)
	return u, nil
}

func refreshCachedAuthUser(ctx context.Context, d Deps, userID string) (*store.User, error) {
	if d.Cache != nil {
		d.Cache.Delete(authUserCacheKey(d, userID))
	}
	u, err := store.FindUserByID(ctx, d.DB, userID)
	if err != nil {
		return nil, err
	}
	cacheAuthUser(d, u)
	return u, nil
}

func cacheAuthUser(d Deps, u *store.User) {
	if d.Cache == nil || u == nil {
		return
	}
	if b, err := json.Marshal(u); err == nil {
		d.Cache.Set(authUserCacheKey(d, u.ID), string(b), authUserTTL(*u))
	}
}

func authUserTTL(u store.User) time.Duration {
	ttl := authUserCacheTTL
	if u.GroupExpiresAt > 0 {
		until := time.Until(time.Unix(u.GroupExpiresAt, 0))
		if until <= 0 {
			return authUserCacheTTLGroupExpired
		}
		if until < ttl {
			ttl = until
		}
	}
	return ttl
}

func invalidateAuthUser(d Deps, userID string) {
	if d.Cache == nil || userID == "" {
		return
	}
	d.Cache.Delete(authUserCacheKey(d, userID))
}

func authUserCacheKey(d Deps, userID string) string {
	epoch := "0"
	if d.Cache != nil {
		if v, ok := d.Cache.Get("auth:epoch"); ok {
			epoch = v
		}
	}
	return "auth:user:" + epoch + ":" + userID
}

func bumpAuthCacheEpoch(d Deps) {
	if d.Cache == nil {
		return
	}
	d.Cache.Incr("auth:epoch", 0)
}
