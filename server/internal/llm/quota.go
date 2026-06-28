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
		// Restricted model, but THIS group has no free-allotment row → it gets no
		// free uses, yet it can still PAY with credits. This MUST match the model
		// picker's `uses_credits` badge (modelUsesCredits returns true for exactly
		// this case), or the user is shown "pay with credits" and then blocked when
		// they actually send. creditDecision still blocks when credits are disabled
		// (credits_per_usd=0) or the user can't cover the cost, so non-credit
		// deployments stay hard-locked exactly as before.
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

// checkImageQuota is the image-model analogue of checkModelQuota (§4.20). It
// reads the SHARED purpose='image' ledger (ImageUsageInWindow) so drawing-mode
// and chat tool-call generations on the same model draw from one pool, and it
// follows the SAME free-allotment → credits → block flow as chat: within the
// group's free image allotment is free; past it, charge credits (timed then
// permanent) when the user can cover it; otherwise block. Counts images for a
// count-limit, summed cost for a cost-limit. Admins are exempt.
func (o *Orchestrator) checkImageQuota(ctx context.Context, userID string, model *store.Model, n int) (string, bool, bool) {
	if n <= 0 {
		n = 1
	}
	if u, err := store.FindUserByID(ctx, o.db, userID); err == nil && u.Role == "admin" {
		return "", true, false
	}
	has, err := store.ModelHasAnyQuota(ctx, o.db, model.ID)
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("imagequota: ModelHasAnyQuota(%s) failed, allowing (fail-open): %v", model.ID, err)
		}
		return "", true, false
	}
	if !has {
		return "", true, false // no quota rows → free + unlimited
	}
	groupID := o.userGroupID(ctx, userID)
	q, err := store.GetModelQuota(ctx, o.db, model.ID, groupID)
	if err != nil {
		// Restricted model with no row for this group → not available to them.
		return o.quotaMessage(), false, false
	}
	if q.LimitValue <= 0 {
		return "", true, false // granted unlimited free
	}
	start, _ := quotaWindow(q.PeriodSeconds)
	cost, images, _ := store.ImageUsageInWindow(ctx, o.db, userID, model.ID, start)
	// Pre-project this request (n images) so the n-th image that crosses the free
	// allotment is what flips to credits.
	withinFree := true
	if q.LimitType == "count" {
		withinFree = images+n <= int(q.LimitValue+0.5)
	} else {
		withinFree = cost+float64(n)*model.PricePerImage <= q.LimitValue
	}
	if withinFree {
		return "", true, false // free use within the group's per-cycle allotment
	}
	// Free image allotment exhausted → pay with credits (shared with chat credits).
	return o.creditDecision(ctx, userID, groupID)
}

// CheckImageCredits / ChargeImageCredits implement the ImageBiller interface so
// the image_generate tool (chat tool-call path) runs the SAME free→credits→block
// decision + debit as drawing mode (§4.20). CheckImageCredits returns whether to
// allow the n images and whether they cost credits; ChargeImageCredits debits.
func (o *Orchestrator) CheckImageCredits(ctx context.Context, userID string, model *store.Model, n int) (bool, bool, string) {
	msg, ok, payCredits := o.checkImageQuota(ctx, userID, model, n)
	return ok, payCredits, msg
}

func (o *Orchestrator) ChargeImageCredits(ctx context.Context, userID string, costUSD float64) (float64, float64) {
	return o.chargeTurnCredits(ctx, userID, costUSD)
}

// creditsPerUSD reads the global USD→credit conversion rate (§ credits). 0 = the
// credit system is disabled platform-wide.
func (o *Orchestrator) creditsPerUSD() float64 {
	if raw, err := store.GetSetting(o.db, "credits_per_usd"); err == nil && len(raw) > 0 {
		var v float64
		if json.Unmarshal(raw, &v) == nil {
			return v
		}
	}
	return 0
}

// creditDecision checks whether the user can cover a credit-charged turn from
// their timed + permanent balance. Returns (msg, ok, useCredits).
func (o *Orchestrator) creditDecision(ctx context.Context, userID, groupID string) (string, bool, bool) {
	g, err := store.GetUserGroup(ctx, o.db, groupID)
	if err != nil || g == nil || o.creditsPerUSD() <= 0 {
		// Credits disabled (no global rate) → nothing to charge against.
		return o.quotaMessage(), false, false
	}
	// A group credit allowance is honoured even with no explicit refresh period:
	// the admin form defaults the period to 0, and silently ignoring a configured
	// allowance ("100 credits") is a footgun. With period<=0 it uses the default
	// window (creditWindow → 7 days); set a period to change the refresh cadence.
	timedRemaining := 0.0
	if g.CreditAllowance > 0 {
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
// (timed, total): the timed portion — which the caller records in
// usage_logs.credits so the timed window can be reseeded across restarts — and
// the total credits charged this turn (timed + permanent), which the caller
// surfaces to the user as "credits used".
func (o *Orchestrator) chargeTurnCredits(ctx context.Context, userID string, usdCost float64) (float64, float64) {
	if usdCost <= 0 {
		return 0, 0
	}
	g, err := store.GetUserGroup(ctx, o.db, o.userGroupID(ctx, userID))
	ratio := o.creditsPerUSD()
	if err != nil || g == nil || ratio <= 0 {
		return 0, 0
	}
	credits := usdCost * ratio
	// Spend the group allowance (timed) first, then the permanent balance. Mirror
	// creditDecision: a configured allowance counts even with no explicit period
	// (default window via creditWindow). Read/charge MUST agree or the decision and
	// the debit drift apart.
	var remaining float64
	var start int64
	var ttl time.Duration
	if g.CreditAllowance > 0 {
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
		if err := store.SpendPermanentCredits(ctx, o.db, userID, fromPermanent); err != nil && o.logger != nil {
			o.logger.Printf("credit debit (permanent, user=%s, amount=%.4f) failed: %v", userID, fromPermanent, err)
		}
	}
	return fromTimed, credits
}

// logUsage writes a usage_logs row and LOGS (rather than silently swallows) a
// failure. usage_logs.credits is the only durable record of timed-credit
// consumption — it reseeds the window across restarts (CreditsUsedInWindow) — so
// a silent write failure would refund the user on the next restart. Make it
// observable (§ credit accounting).
func (o *Orchestrator) logUsage(ctx context.Context, log store.UsageLog) {
	if err := store.LogUsage(ctx, o.db, log); err != nil && o.logger != nil {
		o.logger.Printf("usage log write failed (msg=%s purpose=%s): %v", log.MessageID, log.Purpose, err)
	}
}

// estimateRequestTokens approximates the INPUT token footprint of the assembled
// upstream request — system prompt + tool defs + the full history (which already
// contains the injected RAG/summary/attachments). Heuristic (CJK-aware via
// estimateTokens), no tokenizer; base64 image/document payloads aren't text-
// tokenised so they're counted at a flat per-block allowance. Used by the
// §credits pre-flight gate.
func estimateRequestTokens(req UnifiedChatRequest) int {
	t := estimateTokens(req.SystemPrompt)
	if len(req.Tools) > 0 {
		if b, err := json.Marshal(req.Tools); err == nil {
			t += estimateTokens(string(b))
		}
	}
	for _, m := range req.History {
		if len(m.Raw) > 2 {
			t += estimateTokens(string(m.Raw))
			continue
		}
		for _, b := range m.Blocks {
			switch b.Kind {
			case "image", "document":
				t += 1024 // base64 isn't text-tokenised; rough flat allowance
			default:
				t += estimateTokens(b.Text) + estimateTokens(b.Summary)
				if len(b.Input) > 0 {
					t += estimateTokens(string(b.Input))
				}
			}
		}
	}
	return t
}

// availableCredits returns the user's spendable credits right now (timed-window
// remaining + permanent balance), mirroring creditDecision's read.
func (o *Orchestrator) availableCredits(ctx context.Context, userID, groupID string) float64 {
	timed := 0.0
	if g, err := store.GetUserGroup(ctx, o.db, groupID); err == nil && g != nil && g.CreditAllowance > 0 {
		start, ttl := creditWindow(g.CreditPeriodSeconds)
		timed = g.CreditAllowance - o.readTimedCreditsUsed(ctx, userID, start, ttl)
		if timed < 0 {
			timed = 0
		}
	}
	perm, _ := store.PermanentCredits(ctx, o.db, userID)
	return timed + perm
}

// preflightCredit estimates, BEFORE generating, whether a credit-charged turn is
// affordable (§credits pre-flight). Estimated cost = computeCost(estimated input
// tokens of the REAL request + a fixed 2k output reserve) × credits_per_usd;
// refuse if it exceeds the user's balance. Returns (refusalMessage, ok); ok=true
// means proceed. No-op when credits are off or the admin disabled the check.
func (o *Orchestrator) preflightCredit(ctx context.Context, userID string, model *store.Model, req UnifiedChatRequest) (string, bool) {
	if o.creditsPerUSD() <= 0 {
		return "", true
	}
	enabled := true
	if raw, err := store.GetSetting(o.db, "credit_preflight_enabled"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &enabled)
	}
	if !enabled {
		return "", true
	}
	const outputReserve = 2000 // input + a fixed 2k output reserve (admin choice)
	estIn := estimateRequestTokens(req)
	need := computeCost(*model, Usage{InputTokens: estIn, OutputTokens: outputReserve}) * o.creditsPerUSD()
	have := o.availableCredits(ctx, userID, o.userGroupID(ctx, userID))
	if need > have {
		return fmt.Sprintf("This message is estimated to need about %.1f credits (≈%d input tokens) but your balance is %.1f. Reduce the context (fewer referenced files / shorter conversation) or top up, then try again.", need, estIn, have), false
	}
	return "", true
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
