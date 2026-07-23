package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// creditMultiplierDivisor is env-overridable (§ config-reference) and keeps the
// original 5.0 divisor as a float so the price arithmetic still compiles; it
// falls back to that default when AIVORY_API_CREDIT_MULTIPLIER is unset.
// modelFreeAllotmentQuotaWindowFallback is a hardcoded int64 second-count (not a
// time.Duration) to match the PeriodSeconds math it feeds.
var (
	creditMultiplierDivisor               = envcfg.F64("AIVORY_API_CREDIT_MULTIPLIER", 5.0)
	modelFreeAllotmentQuotaWindowFallback = int64(604800)
)

// imageCreditCost is the per-image cost in CREDITS for an image model:
// price_per_image × the global USD→credit rate (e.g. $0.2/image × 100 = 20). The
// picker shows it after the name when the model is credit-charged. 0 for chat
// models, when the model is free, or when the credit system is off (rate 0).
func imageCreditCost(m store.Model, ratePerUSD float64) float64 {
	if m.Kind != "image" || m.PricePerImage <= 0 || ratePerUSD <= 0 {
		return 0
	}
	return math.Round(m.PricePerImage*ratePerUSD*100) / 100
}

// creditMultiplier is the relative credit rate shown in the picker: the model's
// (input + output price) / 5 (so a $5 combined price = ×1.0), one decimal.
func creditMultiplier(m store.Model) float64 {
	v := (m.PriceInput + m.PriceOutput) / creditMultiplierDivisor
	return math.Round(v*10) / 10
}

// modelUsesCredits reports whether the model would be CREDIT-charged for this
// user's group right now: it has a quota (restricted) but the group has no free
// grant, or the per-cycle free allotment is used up (§ credits). Models with no
// quota rows are free + unlimited → false.
func modelUsesCredits(ctx context.Context, d Deps, userID string, m store.Model, restricted bool, grants map[string]store.ModelGroupQuota) bool {
	if !restricted {
		return false
	}
	q, granted := grants[m.ID]
	if !granted {
		return true // group has no free grant → credits
	}
	if q.LimitValue <= 0 {
		return false // granted unlimited free
	}
	p := int64(q.PeriodSeconds)
	if p <= 0 {
		p = modelFreeAllotmentQuotaWindowFallback
	}
	start := (time.Now().Unix() / p) * p
	cost, count, _ := store.UsageInWindow(ctx, d.DB, userID, m.ID, start)
	if q.LimitType == "count" {
		return count >= int(q.LimitValue+0.5)
	}
	return cost >= q.LimitValue
}

// listModelsHandler returns chat models visible to all signed-in users.
func listModelsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	models, err := store.ListModels(r.Context(), d.DB, "chat", true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// §fast-mode: the fast model is resolved server-side and never named to the
	// user, so drop it from the advanced ("进阶") picker. `fast_available` tells the
	// composer whether to offer the 快速 option at all. `fast_vision` exposes only
	// its image-input capability, never the hidden model's identity.
	fastAvailable := false
	fastVision := false
	filtered := models[:0]
	for _, m := range models {
		if m.Fast {
			fastAvailable = true
			fastVision = modelSupportsImageInput(&m)
			continue
		}
		filtered = append(filtered, m)
	}
	resp := modelsResponse(d, r, filtered)
	resp["fast_available"] = fastAvailable
	resp["fast_vision"] = fastAvailable && fastVision
	writeJSON(w, 200, resp)
}

// listImageModelsHandler returns enabled image models.
func listImageModelsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	models, err := store.ListModels(r.Context(), d.DB, "image", true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, modelsResponse(d, r, models))
}

// listEmbeddingModelsHandler returns enabled embedding models for KB creation.
func listEmbeddingModelsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	models, err := store.ListModels(r.Context(), d.DB, "embedding", true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, modelsResponse(d, r, models))
}

// listSkillsPublicHandler returns enabled skills (read-only listing for
// surfacing in the composer / picker; admin endpoint is /api/admin/skills).
func listSkillsPublicHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	skills, err := store.ListSkills(r.Context(), d.DB, true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, skills)
}

// modelsResponse hides upstream credentials and only exports user-safe model
// fields. The default model id from settings is also returned so the
// frontend's model picker can default to it.
func modelsResponse(d Deps, r *http.Request, models []store.Model) map[string]any {
	type officialToolItem struct {
		Name string `json:"name"`
		Icon string `json:"icon"`
	}
	type item struct {
		ID              string          `json:"id"`
		Label           string          `json:"label"`
		Description     string          `json:"description"`
		Icon            string          `json:"icon"`
		Kind            string          `json:"kind"`
		Enabled         bool            `json:"enabled"`
		Vision          bool            `json:"vision"`
		Stream          bool            `json:"stream"`
		ResearchEnabled bool            `json:"research_enabled"`
		ToolMode        string          `json:"tool_mode"`
		// BuiltinTools is the resolved, user-safe capability set. Unlike the
		// nullable admin policy, this always contains only live registry tools that
		// survive both the model allowlist and the global disabled_tools switch.
		BuiltinTools    []string        `json:"builtin_tools"`
		ParamControls   json.RawMessage `json:"param_controls"`
		ChannelID       string          `json:"channel_id"`
		SortOrder       int             `json:"sort_order"`
		Currency        string          `json:"currency"`
		Tags            json.RawMessage `json:"tags"`
		// OfficialTools intentionally omits each definition's upstream request
		// JSON. Users need names/icons for the per-turn picker, while provider wire
		// configuration remains admin-only.
		OfficialTools []officialToolItem `json:"official_tools"`
		// UsesCredits is true when this model has NO free allotment left for the
		// caller's group (none configured, or the per-cycle count is used up) —
		// the picker shows the credit multiplier instead of a lock (§ credits).
		UsesCredits bool `json:"uses_credits"`
		// Multiplier is the relative credit rate shown next to the name: the model's
		// (input price + output price) / 5, where 5 = ×1.0. One decimal.
		Multiplier float64 `json:"multiplier"`
		// CreditsPerImage is an IMAGE model's per-image cost in credits
		// (price_per_image × credits_per_usd). The picker shows "N credits" after the
		// name when the model is credit-charged; 0 for chat / free / credits-off.
		CreditsPerImage float64 `json:"credits_per_image"`
	}

	// Resolve per-model free-allotment state for the caller's group. Restricted =
	// the model has any quota row; grants = the group's quotas (with limits).
	restricted, _ := store.RestrictedModelIDs(r.Context(), d.DB)
	caller := authUser(r)
	isAdmin := caller != nil && caller.Role == "admin"
	groupID := store.DefaultGroupID
	userID := ""
	if caller != nil {
		userID = caller.ID
		if caller.GroupID != "" {
			groupID = caller.GroupID
		}
	}
	grants, _ := store.QuotasForGroup(r.Context(), d.DB, groupID)

	// Global USD→credit rate, read once. 0 (default / unset) disables the credit
	// system, so image models show no per-image credit cost.
	creditsPerUSD := 0.0
	if raw, err := store.GetSetting(d.DB, "credits_per_usd"); err == nil {
		_ = json.Unmarshal(raw, &creditsPerUSD)
	}

	registeredBuiltinTools := []string{}
	if d.Tools != nil {
		for _, definition := range d.Tools.List("") {
			registeredBuiltinTools = append(registeredBuiltinTools, definition.Name)
		}
	}
	disabledBuiltinTools := map[string]bool{}
	if raw, err := store.GetSetting(d.DB, "disabled_tools"); err == nil && len(raw) > 0 {
		var names []string
		if json.Unmarshal(raw, &names) == nil {
			for _, name := range names {
				disabledBuiltinTools[name] = true
			}
		}
	}

	items := []item{}
	for _, m := range models {
		tags := m.Tags
		if tags == nil {
			tags = json.RawMessage("[]")
		}
		usesCredits := !isAdmin && modelUsesCredits(r.Context(), d, userID, m, restricted[m.ID], grants)
		creditsPerImage := 0.0
		if usesCredits {
			creditsPerImage = imageCreditCost(m, creditsPerUSD)
		}
		officialTools := []officialToolItem{}
		if definitions, err := store.ParseOfficialTools(m.OfficialTools); err == nil {
			for _, definition := range definitions {
				officialTools = append(officialTools, officialToolItem{Name: definition.Name, Icon: definition.Icon})
			}
		}
		items = append(items, item{
			ID: m.ID, Label: m.Label, Description: m.Description, Icon: m.Icon,
			Kind: m.Kind, Enabled: m.Enabled, Vision: m.Vision, Stream: m.Stream, ResearchEnabled: m.ResearchEnabled, ToolMode: m.ToolMode,
			BuiltinTools: effectivePublicBuiltinTools(m, registeredBuiltinTools, disabledBuiltinTools),
			ParamControls: m.ParamControls, ChannelID: m.ChannelID, SortOrder: m.SortOrder,
			Currency:        m.Currency,
			Tags:            tags,
			OfficialTools:   officialTools,
			UsesCredits:     usesCredits,
			Multiplier:      creditMultiplier(m),
			CreditsPerImage: creditsPerImage,
		})
	}
	defaultID := ""
	if raw, err := store.GetSetting(d.DB, "default_model_id"); err == nil {
		_ = json.Unmarshal(raw, &defaultID)
	}
	// §verify: whether an auditor model is configured, so the composer can show
	// the Verify toggle only when the feature is actually usable.
	verifyAvailable := false
	if raw, err := store.GetSetting(d.DB, "verify_model_id"); err == nil {
		var id string
		if json.Unmarshal(raw, &id) == nil && strings.TrimSpace(id) != "" {
			verifyAvailable = true
		}
	}
	return map[string]any{
		"models":           items,
		"default_id":       defaultID,
		"verify_available": verifyAvailable,
	}
}

// effectivePublicBuiltinTools resolves the nullable persisted policy into an
// exact public capability list. Iterating the registry list (rather than the
// configured names) also drops stale/unknown names and preserves deterministic
// registry order. Invalid non-null policies fail closed, matching orchestration.
func effectivePublicBuiltinTools(m store.Model, registered []string, disabled map[string]bool) []string {
	resolved := []string{}
	if m.Kind != "chat" || m.ToolMode == "none" {
		return resolved
	}
	configuredNames, configured, err := store.ParseBuiltinTools(m.BuiltinTools)
	if err != nil {
		return resolved
	}
	configuredSet := make(map[string]bool, len(configuredNames))
	for _, name := range configuredNames {
		configuredSet[name] = true
	}
	for _, name := range registered {
		if disabled[name] || (configured && !configuredSet[name]) {
			continue
		}
		resolved = append(resolved, name)
	}
	return resolved
}
