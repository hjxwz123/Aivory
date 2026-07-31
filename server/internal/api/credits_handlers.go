package api

import (
	"aivory/server/internal/store"
	"encoding/json"
	"net/http"
	"strings"
)

type timedCreditsSnapshot struct {
	Remaining     float64 `json:"remaining"`
	Allowance     float64 `json:"allowance"`
	PeriodSeconds int     `json:"period_seconds"`
	ResetsAt      int64   `json:"resets_at"`
}

func timedCreditsFromBalance(balance store.CreditBalance) timedCreditsSnapshot {
	return timedCreditsSnapshot{
		Remaining:     balance.TimedRemaining,
		Allowance:     balance.Allowance,
		PeriodSeconds: balance.PeriodSeconds,
		ResetsAt:      balance.ResetsAt,
	}
}

// meCreditsHandler reports the signed-in user's credit balance for the
// subscription page: the timed pool (remaining / allowance + next refresh) and
// the separate permanent pool.
func meCreditsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	currency := globalSettlementCurrency(d)
	balance, err := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"enabled":             globalCreditsPerUSD(d) > 0,
		"timed":               timedCreditsFromBalance(balance),
		"permanent":           balance.Permanent,
		"available":           balance.Available,
		"settlement_currency": currency,
	})
}

const defaultSettlementCurrency = store.DefaultSettlementCurrency

func validSettlementCurrency(code string) bool {
	if len(code) != 3 {
		return false
	}
	for _, ch := range code {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}

// globalSettlementCurrency is deliberately code-based rather than a hardcoded
// currency list so self-hosters can use any ISO-4217 code supported by their
// payment provider. Invalid or missing restored settings fail safely to USD.
func globalSettlementCurrency(d Deps) string {
	code := strings.ToUpper(strings.TrimSpace(globalSettingStr(d, "settlement_currency")))
	if !validSettlementCurrency(code) {
		return defaultSettlementCurrency
	}
	return code
}

// globalSettingStr reads a string-valued global setting. Empty when unset.
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
