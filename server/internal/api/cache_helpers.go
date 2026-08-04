package api

import (
	"context"
	"encoding/json"
	"time"

	"aivory/server/internal/store"
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
		if json.Unmarshal([]byte(raw), &u) == nil && u.ID == userID {
			return &u, nil
		}
		d.Cache.Delete(key)
	}
	u, err := store.FindUserByID(ctx, d.DB, userID)
	if err != nil {
		return nil, err
	}
	// Keep the generation captured before the DB read. If a password reset,
	// ban, or role change invalidates this user while the query is in flight,
	// writing to the old generation cannot repopulate the new cache namespace.
	cacheAuthUserAtKey(d, key, u)
	return u, nil
}

func cacheAuthUser(d Deps, u *store.User) {
	if d.Cache == nil || u == nil {
		return
	}
	cacheAuthUserAtKey(d, authUserCacheKey(d, u.ID), u)
}

func cacheAuthUserAtKey(d Deps, key string, u *store.User) {
	if d.Cache == nil || key == "" || u == nil {
		return
	}
	if b, err := json.Marshal(u); err == nil {
		d.Cache.Set(key, string(b), authUserTTL(*u))
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
	oldKey := authUserCacheKey(d, userID)
	// Advancing a persistent per-user generation closes the delete/refill race:
	// an in-flight reader may still populate oldKey, but no later request will
	// consult that generation. The counter intentionally has no TTL to avoid an
	// ABA collision with an older cache entry that has not expired yet.
	d.Cache.Incr(authUserCacheGenerationKey(userID), 0)
	d.Cache.Delete(oldKey)
}

func authUserCacheKey(d Deps, userID string) string {
	epoch := "0"
	userGeneration := "0"
	if d.Cache != nil {
		if v, ok := d.Cache.Get("auth:epoch"); ok {
			epoch = v
		}
		if v, ok := d.Cache.Get(authUserCacheGenerationKey(userID)); ok {
			userGeneration = v
		}
	}
	return "auth:user:" + epoch + ":" + userGeneration + ":" + userID
}

func authUserCacheGenerationKey(userID string) string {
	return "auth:user-generation:" + userID
}

func bumpAuthCacheEpoch(d Deps) {
	if d.Cache == nil {
		return
	}
	d.Cache.Incr("auth:epoch", 0)
}
