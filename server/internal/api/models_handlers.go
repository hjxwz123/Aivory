package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"aurelia/server/internal/store"
)

// creditMultiplier is the relative credit rate shown in the picker: the model's
// (input + output price) / 5 (so a $5 combined price = ×1.0), one decimal.
func creditMultiplier(m store.Model) float64 {
	v := (m.PriceInput + m.PriceOutput) / 5.0
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
		p = 604800
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
	writeJSON(w, 200, modelsResponse(d, r, models))
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
	type item struct {
		ID            string          `json:"id"`
		Label         string          `json:"label"`
		Description   string          `json:"description"`
		Icon          string          `json:"icon"`
		Kind          string          `json:"kind"`
		Vision        bool            `json:"vision"`
		Stream        bool            `json:"stream"`
		ToolMode      string          `json:"tool_mode"`
		ParamControls json.RawMessage `json:"param_controls"`
		ChannelID     string          `json:"channel_id"`
		SortOrder     int             `json:"sort_order"`
		Currency      string          `json:"currency"`
		Tags          json.RawMessage `json:"tags"`
		// UsesCredits is true when this model has NO free allotment left for the
		// caller's group (none configured, or the per-cycle count is used up) —
		// the picker shows the credit multiplier instead of a lock (§ credits).
		UsesCredits bool `json:"uses_credits"`
		// Multiplier is the relative credit rate shown next to the name: the model's
		// (input price + output price) / 5, where 5 = ×1.0. One decimal.
		Multiplier float64 `json:"multiplier"`
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

	items := []item{}
	for _, m := range models {
		tags := m.Tags
		if tags == nil {
			tags = json.RawMessage("[]")
		}
		items = append(items, item{
			ID: m.ID, Label: m.Label, Description: m.Description, Icon: m.Icon,
			Kind: m.Kind, Vision: m.Vision, Stream: m.Stream, ToolMode: m.ToolMode,
			ParamControls: m.ParamControls, ChannelID: m.ChannelID, SortOrder: m.SortOrder,
			Currency:    m.Currency,
			Tags:        tags,
			UsesCredits: !isAdmin && modelUsesCredits(r.Context(), d, userID, m, restricted[m.ID], grants),
			Multiplier:  creditMultiplier(m),
		})
	}
	defaultID := ""
	if raw, err := store.GetSetting(d.DB, "default_model_id"); err == nil {
		_ = json.Unmarshal(raw, &defaultID)
	}
	return map[string]any{
		"models":     items,
		"default_id": defaultID,
	}
}
