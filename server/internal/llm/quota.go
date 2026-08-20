package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

const defaultImageDocumentFlatTokenAllowance = 1024

// ErrDailyImageLimitReached distinguishes an expected, user-visible quota
// refusal from storage/configuration failures in the same admission check.
var ErrDailyImageLimitReached = errors.New("daily image limit reached")

var (
	dailyImageLimitResetWindow      = envcfg.Dur("AIVORY_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW", 24*time.Hour)
	imageDocumentFlatTokenAllowance = envcfg.Int("AIVORY_LLM_IMAGE_DOCUMENT_FLAT_TOKEN_ALLOWANCE", defaultImageDocumentFlatTokenAllowance)
)

func effectiveImageDocumentFlatTokenAllowance() int {
	if imageDocumentFlatTokenAllowance <= 0 {
		return defaultImageDocumentFlatTokenAllowance
	}
	return imageDocumentFlatTokenAllowance
}

// Per-model, per-group usage quotas (§ user groups). A quota row grants that
// group a free allowance. A missing row, including a model with no rows at all,
// means the call is paid with credits. quota_ledger is the authoritative,
// concurrency-safe source for consumption within a membership-anchored cycle.

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
	u, err := store.FindUserByID(ctx, o.db, userID)
	if err != nil {
		return o.quotaMessage(), false, false, -1
	}
	if u.Role == "admin" {
		return "", true, false, -1
	}
	q, err := store.GetModelQuota(ctx, o.db, model.ID, u.GroupID)
	if errors.Is(err, store.ErrNotFound) {
		// No free allowance for this group. This also covers the all-toggles-off
		// state, where the model has no quota rows at all.
		msg, ok, useCredits := o.creditDecision(ctx, userID)
		return msg, ok, useCredits, -1
	}
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("quota: GetModelQuota(%s,%s) failed: %v", model.ID, u.GroupID, err)
		}
		return o.quotaMessage(), false, false, -1
	}
	if q.LimitValue <= 0 {
		return "", true, false, -1 // granted unlimited free
	}
	scope, err := store.GetUserQuotaScope(ctx, o.db, userID)
	if err != nil {
		return o.quotaMessage(), false, false, -1
	}
	start, _ := store.CreditCycleStart(scope.Anchor, q.PeriodSeconds, time.Now().Unix())
	used, err := store.ModelQuotaUsage(ctx, o.db, userID, model.ID, scope.GroupID, store.QuotaScopeModelChat, scope.Anchor, start)
	if err != nil {
		return o.quotaMessage(), false, false, -1
	}
	withinFree := true
	remaining := -1.0
	if q.LimitType == "count" {
		withinFree = used < q.LimitValue
	} else {
		withinFree = used < q.LimitValue
		remaining = q.LimitValue - used // > 0 whenever withinFree holds
	}
	if withinFree {
		return "", true, false, remaining // free use within the group's per-cycle allotment
	}
	// Free allotment exhausted → pay with credits.
	msg, ok, useCredits := o.creditDecision(ctx, userID)
	return msg, ok, useCredits, -1
}

// checkImageQuota is the image-model analogue of checkModelQuota (§4.20). It
// reads the shared quota ledger so drawing-mode and chat tool-call generations
// on the same model draw from one pool, and it
// follows the SAME free-allotment → credits → block flow as chat: within the
// group's free image allotment is free; past it, charge credits (timed then
// permanent) when the user can cover it; otherwise block. Counts images for a
// count-limit, summed cost for a cost-limit. Admins are exempt.
func (o *Orchestrator) checkImageQuota(ctx context.Context, userID string, model *store.Model, n int) (string, bool, bool) {
	n = ClampImageGenerationCount(n)
	u, err := store.FindUserByID(ctx, o.db, userID)
	if err != nil {
		return o.quotaMessage(), false, false
	}
	if u.Role == "admin" {
		return "", true, false
	}
	q, err := store.GetModelQuota(ctx, o.db, model.ID, u.GroupID)
	if errors.Is(err, store.ErrNotFound) {
		return o.checkPaidImageQuota(ctx, userID, model, n)
	}
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("imagequota: GetModelQuota(%s,%s) failed: %v", model.ID, u.GroupID, err)
		}
		return o.quotaMessage(), false, false
	}
	if q.LimitValue <= 0 {
		return "", true, false // granted unlimited free
	}
	scope, err := store.GetUserQuotaScope(ctx, o.db, userID)
	if err != nil {
		return o.quotaMessage(), false, false
	}
	start, _ := store.CreditCycleStart(scope.Anchor, q.PeriodSeconds, time.Now().Unix())
	used, err := store.ModelQuotaUsage(ctx, o.db, userID, model.ID, scope.GroupID, store.QuotaScopeModelImage, scope.Anchor, start)
	if err != nil {
		return o.quotaMessage(), false, false
	}
	// Pre-project this request (n images) so the n-th image that crosses the free
	// allotment is what flips to credits.
	withinFree := true
	if q.LimitType == "count" {
		withinFree = used+float64(n) <= q.LimitValue
	} else {
		withinFree = used+float64(n)*model.PricePerImage <= q.LimitValue
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

type billingAdmission struct {
	UserID         string
	Quota          *store.QuotaReservation
	DailyTokens    *store.QuotaReservation
	PayCredits     bool
	CreditReserved bool
	CreditSourceID string
	SourceType     string
	SourceID       string
	KeepReserved   bool
}

func (o *Orchestrator) validatedCreditsPerUSD() (float64, error) {
	raw, err := store.GetSetting(o.db, "credits_per_usd")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	var ratio float64
	if json.Unmarshal(raw, &ratio) != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
		return 0, store.ErrInvalidCreditConfig
	}
	if micros, err := store.CreditsToMicros(ratio); err != nil || ratio > 0 && micros == 0 {
		return 0, store.ErrInvalidCreditConfig
	}
	return ratio, nil
}

func (o *Orchestrator) reserveUsageBilling(ctx context.Context, userID string, model *store.Model, scopeType string, countValue, costUSD float64, estimatedTokens int, sourceType, sourceID string) (*billingAdmission, string, error) {
	user, err := store.FindUserByID(ctx, o.db, userID)
	if err != nil {
		return nil, "", err
	}
	if user.Role == "admin" {
		return &billingAdmission{UserID: userID}, "", nil
	}
	admission := &billingAdmission{UserID: userID, SourceType: sourceType, SourceID: sourceID}
	if scopeType == store.QuotaScopeModelChat && estimatedTokens > 0 {
		daily, allowed, reserveErr := store.ReserveDailyTokenQuota(ctx, o.db, userID, estimatedTokens)
		if reserveErr != nil {
			if errors.Is(reserveErr, store.ErrDailyTokenQuotaExceeded) {
				return nil, reserveErr.Error(), nil
			}
			return nil, "", reserveErr
		}
		if !allowed {
			return nil, store.ErrDailyTokenQuotaExceeded.Error(), nil
		}
		admission.DailyTokens = daily
	}
	releaseDaily := true
	defer func() {
		if releaseDaily && admission.DailyTokens != nil {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_ = store.ReleaseQuotaReservation(releaseCtx, o.db, admission.DailyTokens.ID)
		}
	}()
	q, err := store.GetModelQuota(ctx, o.db, model.ID, user.GroupID)
	if err == nil && q.LimitValue > 0 {
		requested := costUSD
		if q.LimitType == "count" {
			requested = countValue
		}
		reservation, allowed, reserveErr := store.ReserveModelQuota(
			ctx, o.db, userID, model.ID, scopeType, *q, requested, q.LimitType == "cost",
		)
		if reserveErr != nil {
			return nil, "", reserveErr
		}
		if allowed {
			admission.Quota = reservation
			if q.LimitType == "cost" && costUSD > reservation.ReservedValue {
				ratio, ratioErr := o.validatedCreditsPerUSD()
				if ratioErr != nil || ratio <= 0 {
					_ = store.ReleaseQuotaReservation(ctx, o.db, reservation.ID)
					if ratioErr != nil {
						return nil, "", ratioErr
					}
					return nil, o.quotaMessage(), nil
				}
				admission.CreditSourceID = sourceID + ":quota-overage"
				if _, reserveErr := store.ReserveCredits(
					ctx, o.db, userID, (costUSD-reservation.ReservedValue)*ratio,
					sourceType, admission.CreditSourceID, 24*time.Hour,
				); reserveErr != nil {
					if releaseErr := store.ReleaseQuotaReservation(ctx, o.db, reservation.ID); releaseErr != nil {
						return nil, "", errors.Join(reserveErr, releaseErr)
					}
					if errors.Is(reserveErr, store.ErrInsufficientCredits) {
						return nil, o.quotaMessage(), nil
					}
					return nil, "", reserveErr
				}
				admission.CreditReserved = true
			}
			releaseDaily = false
			return admission, "", nil
		}
	} else if err == nil {
		releaseDaily = false
		return admission, "", nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, "", err
	}

	ratio, err := o.validatedCreditsPerUSD()
	if err != nil {
		return nil, "", err
	}
	if ratio <= 0 {
		return nil, o.quotaMessage(), nil
	}
	admission.PayCredits = true
	admission.CreditSourceID = sourceID
	credits := costUSD * ratio
	if credits <= 0 {
		releaseDaily = false
		return admission, "", nil
	}
	if _, err := store.ReserveCredits(ctx, o.db, userID, credits, sourceType, sourceID, 24*time.Hour); err != nil {
		if errors.Is(err, store.ErrInsufficientCredits) {
			return nil, o.quotaMessage(), nil
		}
		return nil, "", err
	}
	admission.CreditReserved = true
	releaseDaily = false
	return admission, "", nil
}

func (o *Orchestrator) settleUsageBilling(ctx context.Context, admission *billingAdmission, actualCount, actualCostUSD float64, actualTokens int) (store.CreditDebit, error) {
	if admission == nil {
		return store.CreditDebit{}, nil
	}
	admission.KeepReserved = true
	if admission.DailyTokens != nil {
		if _, err := store.FinalizeQuotaReservation(ctx, o.db, admission.DailyTokens.ID, float64(actualTokens)); err != nil {
			return store.CreditDebit{}, err
		}
	}
	if admission.Quota != nil {
		actual := actualCostUSD
		if admission.Quota.LimitType == "count" {
			actual = actualCount
		}
		overageUSD, err := store.FinalizeQuotaReservation(ctx, o.db, admission.Quota.ID, actual)
		if err != nil {
			return store.CreditDebit{}, err
		}
		if overageUSD <= 0 {
			if admission.CreditReserved {
				if err := store.ReleaseCreditReservation(ctx, o.db, admission.SourceType, admission.CreditSourceID); err != nil {
					return store.CreditDebit{}, err
				}
			}
			return store.CreditDebit{}, nil
		}
		ratio, err := o.validatedCreditsPerUSD()
		if err != nil || ratio <= 0 {
			if err == nil {
				err = store.ErrInsufficientCredits
			}
			return store.CreditDebit{}, err
		}
		overageSourceID := admission.CreditSourceID
		if overageSourceID == "" {
			overageSourceID = admission.SourceID + ":quota-overage"
		}
		if !admission.CreditReserved {
			if _, err := store.ReserveCredits(ctx, o.db, admission.Quota.UserID, overageUSD*ratio, admission.SourceType, overageSourceID, 24*time.Hour); err != nil {
				return store.CreditDebit{}, err
			}
		}
		return store.SettleCreditReservation(ctx, o.db, admission.SourceType, overageSourceID, overageUSD*ratio)
	}
	if !admission.PayCredits {
		return store.CreditDebit{}, nil
	}
	ratio, err := o.validatedCreditsPerUSD()
	if err != nil {
		return store.CreditDebit{}, err
	}
	actualCredits := actualCostUSD * ratio
	if !admission.CreditReserved {
		if actualCredits <= 0 {
			return store.CreditDebit{}, nil
		}
		if _, err := store.ReserveCredits(ctx, o.db, admission.UserID, actualCredits, admission.SourceType, admission.SourceID, 24*time.Hour); err != nil {
			return store.CreditDebit{}, err
		}
	}
	return store.SettleCreditReservation(ctx, o.db, admission.SourceType, admission.SourceID, actualCredits)
}

func (o *Orchestrator) releaseUsageBilling(ctx context.Context, admission *billingAdmission) error {
	if admission == nil || admission.KeepReserved {
		return nil
	}
	var out error
	if admission.Quota != nil {
		out = errors.Join(out, store.ReleaseQuotaReservation(ctx, o.db, admission.Quota.ID))
	}
	if admission.DailyTokens != nil {
		out = errors.Join(out, store.ReleaseQuotaReservation(ctx, o.db, admission.DailyTokens.ID))
	}
	if admission.CreditReserved {
		out = errors.Join(out, store.ReleaseCreditReservation(ctx, o.db, admission.SourceType, admission.CreditSourceID))
	}
	return out
}

// CheckImageCredits / ChargeImageCredits implement the ImageBiller interface so
// the image_generate tool (chat tool-call path) runs the SAME free→credits→block
// decision + debit as drawing mode (§4.20). CheckImageCredits returns whether to
// allow the n images and whether they cost credits; ChargeImageCredits debits.
func (o *Orchestrator) CheckImageCredits(ctx context.Context, userID string, model *store.Model, n int) (bool, bool, string) {
	msg, ok, payCredits := o.checkImageQuota(ctx, userID, model, n)
	return ok, payCredits, msg
}

func (o *Orchestrator) ReserveImageBilling(ctx context.Context, userID string, model *store.Model, n int, sourceID string) (*ImageBillingReservation, bool, string, error) {
	n = ClampImageGenerationCount(n)
	admission, message, err := o.reserveUsageBilling(
		ctx, userID, model, store.QuotaScopeModelImage, float64(n), float64(n)*model.PricePerImage,
		0, "image", sourceID,
	)
	if err != nil {
		return nil, false, "", err
	}
	if admission == nil {
		return nil, false, message, nil
	}
	return &ImageBillingReservation{admission: admission}, true, "", nil
}

func (o *Orchestrator) SettleImageBilling(ctx context.Context, reservation *ImageBillingReservation, images int, costUSD float64) (float64, float64, error) {
	if reservation == nil {
		return 0, 0, nil
	}
	debit, err := o.settleUsageBilling(ctx, reservation.admission, float64(images), costUSD, 0)
	return debit.Timed, debit.Total, err
}

func (o *Orchestrator) ReleaseImageBilling(ctx context.Context, reservation *ImageBillingReservation) error {
	if reservation == nil {
		return nil
	}
	return o.releaseUsageBilling(ctx, reservation.admission)
}

// checkDailyImageLimit mirrors image_generate's deployment-wide daily boundary
// for provider-hosted image tools, which never enter the local tool executor.
func (o *Orchestrator) checkDailyImageLimit(ctx context.Context, userID string, n int) (*store.QuotaReservation, error) {
	limit := 30
	if raw, err := store.GetSetting(o.db, "daily_image_limit"); err == nil {
		if json.Unmarshal(raw, &limit) != nil || limit < 0 {
			return nil, store.ErrInvalidCreditConfig
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	n = ClampImageGenerationCount(n)
	dayStart := time.Now().Truncate(dailyImageLimitResetWindow).Unix()
	reservation, allowed, err := store.ReserveFixedQuota(
		ctx, o.db, userID, store.QuotaScopeDailyImage, n, limit, dayStart,
		dayStart+int64(dailyImageLimitResetWindow/time.Second),
	)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrDailyImageLimitReached
	}
	return reservation, nil
}

func (o *Orchestrator) ChargeImageCredits(ctx context.Context, userID string, costUSD float64) (float64, float64, error) {
	return o.chargeTurnCredits(ctx, userID, costUSD)
}

// creditsPerUSD reads the global USD→credit conversion rate (§ credits). 0 = the
// credit system is disabled platform-wide.
func (o *Orchestrator) creditsPerUSD() float64 {
	ratio, err := o.validatedCreditsPerUSD()
	if err != nil {
		return 0
	}
	return ratio
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
func (o *Orchestrator) chargeTurnCredits(ctx context.Context, userID string, usdCost float64) (float64, float64, error) {
	if usdCost <= 0 {
		return 0, 0, nil
	}
	ratio := o.creditsPerUSD()
	if ratio <= 0 {
		return 0, 0, store.ErrInvalidCreditConfig
	}
	credits := usdCost * ratio
	debit, err := store.DebitCredits(ctx, o.db, userID, credits, "llm", "")
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("credit debit failed (user=%s, amount=%.4f): %v", userID, credits, err)
		}
		return 0, 0, err
	}
	return debit.Timed, debit.Total, nil
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
	// Admin defaults and the user's selected parameter-control fragments are
	// serialized into the upstream request alongside the native provider body.
	// Most deployments keep these tiny, but both fields intentionally accept
	// arbitrary JSON objects, so omitting them can hide a large prompt-adjacent
	// payload from context-compaction and credit preflight decisions.
	if params := MergeRequestParams(nil, req.ExtraParams, req.ParamControls, req.ParamOverrides); len(params) > 0 {
		if b, err := json.Marshal(params); err == nil {
			t += estimateTokens(string(b))
		}
	}
	if len(req.OfficialToolRequests) > 0 {
		// Official request fragments are part of the real upstream body and may
		// carry large provider tool schemas. Count the final merged shape so the
		// credit/free-quota preflight cannot be bypassed by moving a schema from
		// the platform tool list into an admin-configured hosted tool.
		if b, err := json.Marshal(MergeOfficialToolRequests(nil, req.OfficialToolRequests)); err == nil {
			t += estimateTokens(string(b))
		}
	}
	if len(req.Tools) > 0 {
		if b, err := json.Marshal(req.Tools); err == nil {
			t += estimateTokens(string(b))
		}
	}
	for _, m := range req.History {
		t += effectiveMessageStructuralOverhead()
		if len(m.Raw) > 2 {
			t += estimateTokens(string(m.Raw))
			continue
		}
		for _, b := range m.Blocks {
			switch b.Kind {
			case "image", "document":
				t += effectiveImageDocumentFlatTokenAllowance() // base64 isn't text-tokenised; rough flat allowance
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
	outputReserve := outputTokenReserve(req) // input + a fixed output reserve (admin choice)
	estIn := estimateRequestTokens(req)
	need := computeCost(*model, Usage{InputTokens: estIn, OutputTokens: outputReserve}) * o.creditsPerUSD()
	have := o.availableCredits(ctx, userID)
	if need > have {
		return fmt.Sprintf("This message is estimated to need about %.1f credits (≈%d input tokens) but your balance is %.1f. Reduce the context (fewer referenced files / shorter conversation) or top up, then try again.", need, estIn, have), false
	}
	return "", true
}

// estimateTurnUSD estimates a turn's upstream USD cost before sending: the
// assembled request's estimated input tokens plus the same fixed output
// reserve the credit pre-flight uses.
func estimateTurnUSD(model store.Model, req UnifiedChatRequest) float64 {
	return computeCost(model, Usage{InputTokens: estimateRequestTokens(req), OutputTokens: outputTokenReserve(req)})
}

func outputTokenReserve(req UnifiedChatRequest) int {
	outputReserve := req.MaxOutputTokens
	if outputReserve <= 0 {
		outputReserve = envcfg.Int("AIVORY_LLM_OUTPUT_RESERVE", 2000)
	}
	if outputReserve <= 0 {
		outputReserve = 2000
	}
	return outputReserve
}

func estimateTurnTokens(req UnifiedChatRequest) int {
	outputReserve := outputTokenReserve(req)
	input := estimateRequestTokens(req)
	maxInt := int(^uint(0) >> 1)
	if input < 0 {
		return maxInt
	}
	if input > maxInt-outputReserve {
		return maxInt
	}
	return input + outputReserve
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
