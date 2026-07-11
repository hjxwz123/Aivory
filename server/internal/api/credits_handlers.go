package api

import (
	"aivory/server/internal/store"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"
)

// creditWindowPeriodFallbackSeconds is the default timed-window length in
// seconds (604800 = 7 days) used when no explicit period is supplied.
var creditWindowPeriodFallbackSeconds int64 = 604800

// Credit balance (§ credits). The timed pool refreshes every cycle (unused
// voided); the permanent pool is bought / admin-set and never expires. The
// timed-window consumption mirrors the orchestrator's accounting: a per-window
// cache key in micro-units, seeded from usage_logs.credits when cold, so this
// read agrees with what the deduction path writes.

func creditMicros(c float64) int64 { return int64(math.Round(c * 1e6)) }

// creditWindowUsed returns the timed credits consumed in the current window plus
// the window start, matching internal/llm/quota.go's key + seeding.
func creditWindowUsed(ctx context.Context, d Deps, userID string, periodSeconds int) (used float64, windowStart int64) {
	p := int64(periodSeconds)
	if p <= 0 {
		p = creditWindowPeriodFallbackSeconds
	}
	windowStart = (time.Now().Unix() / p) * p
	key := "credit:v1:" + userID + ":" + strconv.FormatInt(windowStart, 10)
	if d.Cache != nil {
		if v, ok := d.Cache.Get(key); ok {
			micros, _ := strconv.ParseInt(v, 10, 64)
			return float64(micros) / 1e6, windowStart
		}
	}
	used, _ = store.CreditsUsedInWindow(ctx, d.DB, userID, windowStart)
	if d.Cache != nil {
		d.Cache.Set(key, strconv.FormatInt(creditMicros(used), 10), time.Duration(p)*time.Second)
	}
	return used, windowStart
}

// meCreditsHandler reports the signed-in user's credit balance for the
// subscription page: the timed pool (remaining / allowance + next refresh) and
// the separate permanent pool, plus the top-up link.
func meCreditsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	g, err := store.GetUserGroup(r.Context(), d.DB, groupOrDefault(u.GroupID))
	if err != nil || g == nil {
		writeJSON(w, 200, map[string]any{"enabled": false, "permanent": u.CreditsPermanent})
		return
	}
	used, windowStart := creditWindowUsed(r.Context(), d, u.ID, g.CreditPeriodSeconds)
	remaining := g.CreditAllowance - used
	if remaining < 0 {
		remaining = 0
	}
	period := g.CreditPeriodSeconds
	resetsAt := int64(0)
	if period > 0 {
		resetsAt = windowStart + int64(period)
	}
	writeJSON(w, 200, map[string]any{
		"enabled": globalCreditsPerUSD(d) > 0,
		"timed": map[string]any{
			"remaining":      remaining,
			"allowance":      g.CreditAllowance,
			"period_seconds": period,
			"resets_at":      resetsAt,
		},
		"permanent":     u.CreditsPermanent,
		"buy_url":       globalSettingStr(d, "credit_buy_url"),
		"group_buy_url": globalSettingStr(d, "group_buy_url"),
	})
}

// globalSettingStr reads a string-valued global setting (§ credits purchase
// links). Empty when unset.
func globalSettingStr(d Deps, key string) string {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func groupOrDefault(id string) string {
	if id == "" {
		return store.DefaultGroupID
	}
	return id
}

// globalCreditsPerUSD reads the platform-wide USD→credit rate (§ credits). 0 =
// credits disabled.
func globalCreditsPerUSD(d Deps) float64 {
	raw, err := store.GetSetting(d.DB, "credits_per_usd")
	if err != nil || len(raw) == 0 {
		return 0
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil {
		return 0
	}
	return v
}
