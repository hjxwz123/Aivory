package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

var dailyImageLimitResetWindow = envcfg.Dur("AIVORY_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW", 24*time.Hour)

// Window cost is accumulated in integer micro-units so it can use the cache's
// atomic IncrBy (§B3) — a float Get/modify/Set loses concurrent increments.
func costToMicros(c float64) int64 { return int64(math.Round(c * 1e6)) }
func microsToCost(m int64) float64 { return float64(m) / 1e6 }

// Per-model, per-group usage quotas (§ user groups). A quota row grants that
// group a free allowance. A missing row, including a model with no rows at all,
// means the call is paid with credits. The window count/cost lives in the cache
// (O(1) per request), seeded from usage_logs when the cache is cold, so the
// check stays cheap at scale.

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
//
// The fourth return is the REMAINING free allowance in USD when the turn was
// admitted under a finite cost-type allotment, or -1 when no such ceiling
// applies (admin, unlimited, count-type, already paying credits). The
// orchestrator re-checks it against the assembled request's estimated cost
// (§ free-allowance overshoot): the gate here runs before the prompt exists,
// so on its own a $2 request would ride on $1 of remaining allowance.
func (o *Orchestrator) checkModelQuota(ctx context.Context, userID string, model *store.Model) (string, bool, bool, float64) {
	// Admins are exempt from all usage quotas (§ admin).
	if u, err := store.FindUserByID(ctx, o.db, userID); err == nil && u.Role == "admin" {
		return "", true, false, -1
	}
	groupID := o.userGroupID(ctx, userID)
	q, err := store.GetModelQuota(ctx, o.db, model.ID, groupID)
	if errors.Is(err, store.ErrNotFound) {
		// No free allowance for this group. This also covers the all-toggles-off
		// state, where the model has no quota rows at all.
		msg, ok, useCredits := o.creditDecision(ctx, userID)
		return msg, ok, useCredits, -1
	}
	if err != nil {
		// §B11: fail OPEN on an unexpected DB error (availability over enforcement)
		// but do not confuse it with the intentional no-free-allowance state above.
		if o.logger != nil {
			o.logger.Printf("quota: GetModelQuota(%s,%s) failed, allowing (fail-open): %v", model.ID, groupID, err)
		}
		return "", true, false, -1
	}
	if q.LimitValue <= 0 {
		return "", true, false, -1 // granted unlimited free
	}
	start, ttl := quotaWindow(q.PeriodSeconds)
	cost, count := o.readQuota(ctx, userID, model.ID, q, start, ttl)
	withinFree := true
	remaining := -1.0
	if q.LimitType == "count" {
		withinFree = count < int(q.LimitValue+0.5)
	} else {
		withinFree = cost < q.LimitValue
		remaining = q.LimitValue - cost // > 0 whenever withinFree holds
	}
	if withinFree {
		return "", true, false, remaining // free use within the group's per-cycle allotment
	}
	// Free allotment exhausted → pay with credits.
	msg, ok, useCredits := o.creditDecision(ctx, userID)
	return msg, ok, useCredits, -1
}

// checkImageQuota is the image-model analogue of checkModelQuota (§4.20). It
// reads the SHARED purpose='image' ledger (ImageUsageInWindow) so drawing-mode
// and chat tool-call generations on the same model draw from one pool, and it
// follows the SAME free-allotment → credits → block flow as chat: within the
// group's free image allotment is free; past it, charge credits (timed then
// permanent) when the user can cover it; otherwise block. Counts images for a
// count-limit, summed cost for a cost-limit. Admins are exempt.
func (o *Orchestrator) checkImageQuota(ctx context.Context, userID string, model *store.Model, n int) (string, bool, bool) {
	n = ClampImageGenerationCount(n)
	if u, err := store.FindUserByID(ctx, o.db, userID); err == nil && u.Role == "admin" {
		return "", true, false
	}
	groupID := o.userGroupID(ctx, userID)
	q, err := store.GetModelQuota(ctx, o.db, model.ID, groupID)
	if errors.Is(err, store.ErrNotFound) {
		return o.checkPaidImageQuota(ctx, userID, model, n)
	}
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("imagequota: GetModelQuota(%s,%s) failed, allowing (fail-open): %v", model.ID, groupID, err)
		}
		return "", true, false
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
	// Unlike chat, image cost is exact before the request starts, so require the
	// balance to cover the whole clamped batch instead of merely being positive.
	return o.checkPaidImageQuota(ctx, userID, model, n)
}

func (o *Orchestrator) checkPaidImageQuota(ctx context.Context, userID string, model *store.Model, n int) (string, bool, bool) {
	msg, ok, useCredits := o.creditDecision(ctx, userID)
	if !ok || !useCredits {
		return msg, ok, useCredits
	}
	requiredCredits := float64(n) * model.PricePerImage * o.creditsPerUSD()
	if requiredCredits > o.availableCredits(ctx, userID) {
		return o.quotaMessage(), false, false
	}
	return "", true, true
}

// CheckImageCredits / ChargeImageCredits implement the ImageBiller interface so
// the image_generate tool (chat tool-call path) runs the SAME free→credits→block
// decision + debit as drawing mode (§4.20). CheckImageCredits returns whether to
// allow the n images and whether they cost credits; ChargeImageCredits debits.
func (o *Orchestrator) CheckImageCredits(ctx context.Context, userID string, model *store.Model, n int) (bool, bool, string) {
	msg, ok, payCredits := o.checkImageQuota(ctx, userID, model, n)
	return ok, payCredits, msg
}

// checkDailyImageLimit mirrors image_generate's deployment-wide daily boundary
// for provider-hosted image tools, which never enter the local tool executor.
func (o *Orchestrator) checkDailyImageLimit(ctx context.Context, userID string, n int) error {
	limit := 30
	if raw, err := store.GetSetting(o.db, "daily_image_limit"); err == nil {
		_ = json.Unmarshal(raw, &limit)
	}
	if limit <= 0 {
		return nil
	}
	n = ClampImageGenerationCount(n)
	dayStart := time.Now().Truncate(dailyImageLimitResetWindow).Unix()
	var used int
	if err := o.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(images_count),0) FROM usage_logs WHERE user_id=? AND purpose='image' AND created_at>=?`,
		userID, dayStart).Scan(&used); err != nil {
		return err
	}
	if used+n > limit {
		return fmt.Errorf("daily image limit reached (%d/%d)", used, limit)
	}
	return nil
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
func (o *Orchestrator) creditDecision(ctx context.Context, userID string) (string, bool, bool) {
	if o.creditsPerUSD() <= 0 {
		// Credits disabled (no global rate) → nothing to charge against.
		return o.quotaMessage(), false, false
	}
	balance, err := store.GetCreditBalance(ctx, o.db, userID)
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("credit balance read failed (user=%s): %v", userID, err)
		}
		return o.quotaMessage(), false, false
	}
	if balance.Available > 0 {
		return "", true, true
	}
	return o.quotaMessage(), false, false
}

// chargeTurnCredits debits a credit-charged turn atomically: timed credits first,
// then permanent. The ledger remains authoritative even if analytics usage logs
// are deleted.
func (o *Orchestrator) chargeTurnCredits(ctx context.Context, userID string, usdCost float64) (float64, float64) {
	if usdCost <= 0 {
		return 0, 0
	}
	ratio := o.creditsPerUSD()
	if ratio <= 0 {
		return 0, 0
	}
	credits := usdCost * ratio
	debit, err := store.DebitCredits(ctx, o.db, userID, credits, "llm", "")
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("credit debit failed (user=%s, amount=%.4f): %v", userID, credits, err)
		}
		return 0, 0
	}
	return debit.Timed, debit.Total
}

// logUsage writes an analytics row. Credit debits are already durable in the
// independent billing ledger, so deleting or failing to write this row cannot
// refund a user.
func (o *Orchestrator) logUsage(ctx context.Context, log store.UsageLog) {
	if err := store.LogUsage(ctx, o.db, log); err != nil && o.logger != nil {
		o.logger.Printf("usage log write failed (msg=%s purpose=%s): %v", log.MessageID, log.Purpose, err)
	}
}

// estimateRequestTokens approximates the INPUT token footprint of the assembled
// upstream request — system prompt + tool defs + the full history (which already
// contains the injected RAG/summary/attachments). Heuristic (CJK-aware via
// estimateTokens), no tokenizer; base64 image payloads aren't text-tokenised so
// they're counted at a flat per-block allowance. Documents use the RAG text path.
// Used by the §credits pre-flight gate.
func estimateRequestTokens(req UnifiedChatRequest) int {
	t := estimateTokens(req.SystemPrompt)
	if officialToolModeEnabled(req) && len(req.OfficialToolRequests) > 0 {
		// Official request fragments are part of the real upstream body and may
		// carry large provider tool schemas. Count the final merged shape so the
		// credit/free-quota preflight cannot be bypassed by moving a schema from
		// the platform tool list into an admin-configured hosted tool.
		if b, err := json.Marshal(MergeOfficialToolRequests(nil, req.OfficialToolRequests)); err == nil {
			t += estimateTokens(string(b))
		}
	} else if len(req.Tools) > 0 {
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
				t += envcfg.Int("AIVORY_LLM_IMAGE_DOCUMENT_FLAT_TOKEN_ALLOWANCE", 1024) // base64 isn't text-tokenised; rough flat allowance
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
func (o *Orchestrator) availableCredits(ctx context.Context, userID string) float64 {
	balance, err := store.GetCreditBalance(ctx, o.db, userID)
	if err != nil {
		return 0
	}
	return balance.Available
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
	outputReserve := envcfg.Int("AIVORY_LLM_OUTPUT_RESERVE", 2000) // input + a fixed 2k output reserve (admin choice)
	estIn := estimateRequestTokens(req)
	need := computeCost(*model, Usage{InputTokens: estIn, OutputTokens: outputReserve}) * o.creditsPerUSD()
	have := o.availableCredits(ctx, userID)
	if need > have {
		return fmt.Sprintf("This message is estimated to need about %.1f credits (≈%d input tokens) but your balance is %.1f. Reduce the context (fewer referenced files / shorter conversation) or top up, then try again.", need, estIn, have), false
	}
	return "", true
}

// freeQuotaOvershootGracePct: a turn admitted under the free allotment flips to
// credits only when its estimated cost exceeds the REMAINING allowance by this
// percentage (default 120 = 20% grace). The estimate (bytes/4 + CJK) is
// approximate; the grace keeps borderline turns free instead of prematurely
// charging credits, while still stopping a $2 request from riding on $0.01.
var freeQuotaOvershootGracePct = envcfg.Int("AIVORY_LLM_FREE_QUOTA_OVERSHOOT_GRACE_PCT", 120)

// estimateTurnUSD estimates a turn's upstream USD cost before sending: the
// assembled request's estimated input tokens plus the same fixed output
// reserve the credit pre-flight uses.
func estimateTurnUSD(model store.Model, req UnifiedChatRequest) float64 {
	outputReserve := envcfg.Int("AIVORY_LLM_OUTPUT_RESERVE", 2000)
	return computeCost(model, Usage{InputTokens: estimateRequestTokens(req), OutputTokens: outputReserve})
}

// freeQuotaOvershoot reports whether an estimated turn cost blows past the
// remaining free allowance (§ free-allowance overshoot). remaining < 0 means
// no finite cost-type allowance applies, so there is nothing to overshoot.
func freeQuotaOvershoot(estUSD, remainingUSD float64) bool {
	return remainingUSD >= 0 && estUSD*100 > remainingUSD*float64(freeQuotaOvershootGracePct)
}

// recordQuotaUsage updates the free-window counter after a successful turn.
// Credit-paid turns are skipped — the window measures the FREE allotment, and
// store.UsageInWindow (the cold-cache seed) excludes credits>0 rows the same
// way, so a paid turn never burns the user's remaining free allowance.
func (o *Orchestrator) recordQuotaUsage(ctx context.Context, userID string, model *store.Model, turnCost float64, paidWithCredits bool) {
	if o.cache == nil || paidWithCredits {
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
