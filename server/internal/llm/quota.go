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

// checkModelQuota decides whether the user may use the model and how it's paid
// (§ credits). Returns (message, ok, useCredits):
//   - ok=false: blocked, `message` is the over-limit / top-up prompt.
//   - ok=true, useCredits=false: covered by the model's per-group FREE allotment.
//   - ok=true, useCredits=true: free allotment is exhausted (or the group has
//     none), so this turn is charged in credits (timed first, then permanent).
//
// There is no "locked" outcome anymore — a model the group has no free uses for
// simply falls back to credits (§ remove user-side lock).
func (o *Orchestrator) checkModelQuota(ctx context.Context, userID string, model *store.Model) (string, bool, bool) {
	// Admins are exempt from all usage quotas (§ admin).
	if u, err := store.FindUserByID(ctx, o.db, userID); err == nil && u.Role == "admin" {
		return "", true, false
	}
	has, err := store.ModelHasAnyQuota(ctx, o.db, model.ID)
	if err != nil {
		// §B11: fail OPEN on a DB error (availability over enforcement) but log it.
		if o.logger != nil {
			o.logger.Printf("quota: ModelHasAnyQuota(%s) failed, allowing (fail-open): %v", model.ID, err)
		}
		return "", true, false
	}
	if !has {
		return "", true, false // no quota rows → open to everyone, free + unlimited
	}
	groupID := o.userGroupID(ctx, userID)
	q, err := store.GetModelQuota(ctx, o.db, model.ID, groupID)
	if err != nil {
		// Group has no free grant for this model → pay with credits.
		return o.creditDecision(ctx, userID, groupID)
	}
	if q.LimitValue <= 0 {
		return "", true, false // granted unlimited free
	}
	start, ttl := quotaWindow(q.PeriodSeconds)
	cost, count := o.readQuota(ctx, userID, model.ID, q, start, ttl)
	withinFree := true
	if q.LimitType == "count" {
		withinFree = count < int(q.LimitValue+0.5)
	} else {
		withinFree = cost < q.LimitValue
	}
	if withinFree {
		return "", true, false // free use within the group's per-cycle allotment
	}
	// Free allotment exhausted → pay with credits.
	return o.creditDecision(ctx, userID, groupID)
}

// creditDecision checks whether the user can cover a credit-charged turn from
// their timed + permanent balance. Returns (msg, ok, useCredits).
func (o *Orchestrator) creditDecision(ctx context.Context, userID, groupID string) (string, bool, bool) {
	g, err := store.GetUserGroup(ctx, o.db, groupID)
	if err != nil || g == nil || g.CreditsPerUSD <= 0 {
		// Credits not configured for this group → nothing to charge against.
		return o.quotaMessage(), false, false
	}
	// Timed credits only apply when an explicit refresh period is set; otherwise
	// the group is permanent-credits-only.
	timedRemaining := 0.0
	if g.CreditPeriodSeconds > 0 && g.CreditAllowance > 0 {
		start, ttl := creditWindow(g.CreditPeriodSeconds)
		timedRemaining = g.CreditAllowance - o.readTimedCreditsUsed(ctx, userID, start, ttl)
		if timedRemaining < 0 {
			timedRemaining = 0
		}
	}
	perm, _ := store.PermanentCredits(ctx, o.db, userID)
	if timedRemaining+perm > 0 {
		return "", true, true
	}
	return o.quotaMessage(), false, false
}

// chargeTurnCredits debits a credit-charged turn: timed credits first (so the
// expiring balance is spent before the permanent one), then permanent. Returns
// the timed portion charged, which the caller records in usage_logs.credits so
// the timed window can be reseeded across restarts.
func (o *Orchestrator) chargeTurnCredits(ctx context.Context, userID string, usdCost float64) float64 {
	if usdCost <= 0 {
		return 0
	}
	g, err := store.GetUserGroup(ctx, o.db, o.userGroupID(ctx, userID))
	if err != nil || g == nil || g.CreditsPerUSD <= 0 {
		return 0
	}
	credits := usdCost * g.CreditsPerUSD
	// Timed credits only exist with an explicit refresh period; otherwise charge
	// entirely against the permanent balance.
	var remaining float64
	var start int64
	var ttl time.Duration
	if g.CreditPeriodSeconds > 0 && g.CreditAllowance > 0 {
		start, ttl = creditWindow(g.CreditPeriodSeconds)
		remaining = g.CreditAllowance - o.readTimedCreditsUsed(ctx, userID, start, ttl)
		if remaining < 0 {
			remaining = 0
		}
	}
	fromTimed := credits
	if fromTimed > remaining {
		fromTimed = remaining
	}
	if fromTimed > 0 && o.cache != nil {
		o.cache.IncrBy(creditKey(userID, start), costToMicros(fromTimed), ttl)
	}
	if fromPermanent := credits - fromTimed; fromPermanent > 0 {
		_ = store.AddPermanentCredits(ctx, o.db, userID, -fromPermanent)
	}
	return fromTimed
}

// creditWindow computes the fixed-window start + ttl for the credit refresh cycle.
func creditWindow(periodSeconds int) (int64, time.Duration) {
	p := int64(periodSeconds)
	if p <= 0 {
		p = 604800
	}
	now := time.Now().Unix()
	return (now / p) * p, time.Duration(p) * time.Second
}

func creditKey(userID string, windowStart int64) string {
	return fmt.Sprintf("credit:v1:%s:%d", userID, windowStart)
}

// readTimedCreditsUsed returns the timed credits consumed in the current window,
// preferring the cache (micro-units) and seeding from usage_logs when cold.
func (o *Orchestrator) readTimedCreditsUsed(ctx context.Context, userID string, start int64, ttl time.Duration) float64 {
	key := creditKey(userID, start)
	if o.cache != nil {
		if v, ok := o.cache.Get(key); ok {
			micros, _ := strconv.ParseInt(v, 10, 64)
			return microsToCost(micros)
		}
	}
	used, _ := store.CreditsUsedInWindow(ctx, o.db, userID, start)
	if o.cache != nil {
		o.cache.Set(key, strconv.FormatInt(costToMicros(used), 10), ttl)
	}
	return used
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
