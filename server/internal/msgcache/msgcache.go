package msgcache

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"aurelia/server/internal/cache"
	"aurelia/server/internal/store"
)

const pathTTL = 45 * time.Second

// Bump invalidates cached conversation message paths by moving the version used
// in cache keys. It is deliberately prefix-free so Redis does not need SCAN/DEL
// on the hot mutation path.
func Bump(c cache.Cache, convID string) {
	if c == nil || convID == "" {
		return
	}
	c.Incr(versionKey(convID), 10*time.Minute)
}

// ListMessages returns store.ListMessages with a short Redis/in-memory cache.
// It targets the repeated hot reads for active-path hydration and context
// assembly while keeping staleness bounded by mutation-driven version bumps.
func ListMessages(ctx context.Context, c cache.Cache, db *sql.DB, convID, leafID string) ([]store.Message, error) {
	if c == nil || convID == "" {
		return store.ListMessages(ctx, db, convID, leafID)
	}
	ver := "0"
	if v, ok := c.Get(versionKey(convID)); ok {
		ver = v
	}
	key := "conv:path:" + convID + ":" + leafID + ":" + ver
	if raw, ok := c.Get(key); ok {
		var msgs []store.Message
		if json.Unmarshal([]byte(raw), &msgs) == nil {
			return msgs, nil
		}
	}
	msgs, err := store.ListMessages(ctx, db, convID, leafID)
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(msgs); err == nil {
		c.Set(key, string(b), pathTTL)
	}
	return msgs, nil
}

func versionKey(convID string) string {
	return "conv:ver:" + convID
}
