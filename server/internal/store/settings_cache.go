package store

import (
	"encoding/json"
	"sync"
	"time"
)

// §2.4 settings cache. GetSetting is read on nearly every request (sandbox /
// search config, quota message, model resolution, upload policy…); a short-TTL
// process-local cache keeps those reads off the database. Writes invalidate the
// touched key immediately on this instance; cross-instance invalidation is
// driven by the "cfg:invalidate" Pub/Sub subscriber wired in main() so an admin
// change on one node propagates to the others.
const settingsCacheTTL = 15 * time.Second

type settingEntry struct {
	val     json.RawMessage
	missing bool // negative cache: the key has no row
	exp     int64
}

var (
	settingsMu    sync.RWMutex
	settingsStore = map[string]settingEntry{}
)

func settingsCacheGet(key string) (val json.RawMessage, missing bool, ok bool) {
	settingsMu.RLock()
	e, found := settingsStore[key]
	settingsMu.RUnlock()
	if !found || time.Now().UnixNano() > e.exp {
		return nil, false, false
	}
	return e.val, e.missing, true
}

func settingsCachePut(key string, val json.RawMessage, missing bool) {
	settingsMu.Lock()
	settingsStore[key] = settingEntry{val: val, missing: missing, exp: time.Now().Add(settingsCacheTTL).UnixNano()}
	settingsMu.Unlock()
}

func invalidateSettingKey(key string) {
	settingsMu.Lock()
	delete(settingsStore, key)
	settingsMu.Unlock()
}

// InvalidateConfig clears the whole settings cache — called by the
// "cfg:invalidate" Pub/Sub subscriber when any admin config write happens
// (settings/channels/models), so stale config can't outlive a change.
// (Channel/model rows are read via single indexed queries and are intentionally
// not object-cached, so there's nothing else to clear here yet.)
func InvalidateConfig() {
	settingsMu.Lock()
	settingsStore = map[string]settingEntry{}
	settingsMu.Unlock()
}
