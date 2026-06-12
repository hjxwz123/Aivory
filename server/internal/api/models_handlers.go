package api

import (
	"encoding/json"
	"net/http"

	"aurelia/server/internal/store"
)

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
	}
	items := []item{}
	for _, m := range models {
		items = append(items, item{
			ID: m.ID, Label: m.Label, Description: m.Description, Icon: m.Icon,
			Kind: m.Kind, Vision: m.Vision, Stream: m.Stream, ToolMode: m.ToolMode,
			ParamControls: m.ParamControls, ChannelID: m.ChannelID, SortOrder: m.SortOrder,
			Currency: m.Currency,
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
