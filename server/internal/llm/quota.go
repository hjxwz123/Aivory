package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"aurelia/server/internal/store"
)

// Window cost is accumulated in integer micro-units so it can use the cache's
// atomic IncrBy (§B3) — a float Get/modify/Set loses concurrent increments.
func costToMicros(c float64) int64 { return int64(math.Round(c * 1e6)) }
func microsToCost(m int64) float64 { return float64(m) / 1e6 }

// Per-model, per-group usage quotas (§ user groups). A model with no quota rows
// is open to everyone. Otherwise a user's group must have a row (else the model
// is locked for them), and usage is capped within a fixed window. The window
// count/cost lives in the cache (O(1) per request), seeded from usage_logs when
// the cache is cold, so the check stays cheap at scale.

// quotaWindow computes the fixed-window start + ttl for a period.
func quotaWindow(periodSeconds int) (start int64, ttl time.Duration) {
	p := int64(periodSeconds)
	if p <= 0 {
		p = 604800 // 7 days
	}
	now := time.Now().Unix()
	return (now / p) * p, time.Duration(p) * time.Second
}

// checkModelQuota returns ("", true) when the user may use the model, or
// (message, false) with the upgrade/over-limit prompt when blocked.
func (o *Orchestrator) checkModelQuota(ctx context.Context, userID string, model *store.Model) (string, bool) {
	// Admins are exempt from all usage quotas (§ admin) — they can always test
	// any model regardless of group limits.
	if u, err := store.FindUserByID(ctx, o.db, userID); err == nil && u.Role == "admin" {
		return "", true
	}
	has, err := store.ModelHasAnyQuota(ctx, o.db, model.ID)
	if err != nil {
		// §B11: fail OPEN on a DB error (availability over enforcement) but log it
		// — a silent fail-open hides both outages and a deliberately-induced bypass.
		if o.logger != nil {
			o.logger.Printf("quota: ModelHasAnyQuota(%s) failed, allowing (fail-open): %v", model.ID, err)
		}
		return "", true
	}
	if !has {
		return "", true // no quota rows → open to everyone, unlimited
	}
	groupID := o.userGroupID(ctx, userID)
	q, err := store.GetModelQuota(ctx, o.db, model.ID, groupID)
	if err != nil {
		return o.quotaMessage(), false // group not granted → locked
	}
	if q.LimitValue <= 0 {
		return "", true // granted, unlimited
	}
	start, ttl := quotaWindow(q.PeriodSeconds)
	cost, count := o.readQuota(ctx, userID, model.ID, q, start, ttl)
	if q.LimitType == "count" {
		if count >= int(q.LimitValue+0.5) {
			return o.quotaMessage(), false
		}
	} else if cost >= q.LimitValue {
		return o.quotaMessage(), false
	}
	return "", true
}

// recordQuotaUsage updates the window counter after a successful turn.
func (o *Orchestrator) recordQuotaUsage(ctx context.Context, userID string, model *store.Model, turnCost float64) {
	if o.cache == nil {
		return
	}
	has, err := store.ModelHasAnyQuota(ctx, o.db, model.ID)
	if err != nil || !has {
		return
	}
	q, err := store.GetModelQuota(ctx, o.db, model.ID, o.userGroupID(ctx, userID))
	if err != nil || q.LimitValue <= 0 {
		return
	}
	start, ttl := quotaWindow(q.PeriodSeconds)
	key := quotaKey(userID, model.ID, start)
	if q.LimitType == "count" {
		o.cache.Incr(key, ttl)
		return
	}
	// §B3: atomic add in micro-units (no Get→add→Set race under concurrent turns).
	o.cache.IncrBy(key, costToMicros(turnCost), ttl)
}

// readQuota returns the current window cost/count, preferring the cache and
// falling back to a usage_logs aggregate (which it then seeds into the cache).
func (o *Orchestrator) readQuota(ctx context.Context, userID, modelID string, q *store.ModelGroupQuota, start int64, ttl time.Duration) (float64, int) {
	key := quotaKey(userID, modelID, start)
	if o.cache != nil {
		if v, ok := o.cache.Get(key); ok {
			if q.LimitType == "count" {
				n, _ := strconv.Atoi(v)
				return 0, n
			}
			micros, _ := strconv.ParseInt(v, 10, 64)
			return microsToCost(micros), 0
		}
	}
	cost, count, _ := store.UsageInWindow(ctx, o.db, userID, modelID, start)
	if o.cache != nil {
		if q.LimitType == "count" {
			o.cache.Set(key, strconv.Itoa(count), ttl)
		} else {
			// Seed the cold cache with the authoritative usage_logs total, in micro-units.
			o.cache.Set(key, strconv.FormatInt(costToMicros(cost), 10), ttl)
		}
	}
	return cost, count
}

func quotaKey(userID, modelID string, windowStart int64) string {
	// v2: cost is now stored in integer micro-units (§B3); the version prefix
	// prevents reading a stale pre-upgrade float string as an int.
	return fmt.Sprintf("quota:v2:%s:%s:%d", userID, modelID, windowStart)
}

// userGroupID resolves the user's group, defaulting to the free tier.
func (o *Orchestrator) userGroupID(ctx context.Context, userID string) string {
	if u, err := store.FindUserByID(ctx, o.db, userID); err == nil && u.GroupID != "" {
		return u.GroupID
	}
	return store.DefaultGroupID
}

// quotaMessage is the admin-configurable prompt shown when a model is locked for
// a group or its quota is exhausted.
func (o *Orchestrator) quotaMessage() string {
	if raw, err := store.GetSetting(o.db, "quota_exceeded_message"); err == nil {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	return "You've reached your plan's usage limit for this model. Please wait for your quota to reset, or upgrade your plan to continue."
}
