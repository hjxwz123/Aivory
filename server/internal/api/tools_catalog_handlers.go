package api

import (
	"net/http"
	"sort"
	"strings"

	"aivory/server/internal/store"
)

type selectableToolResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
}

var builtinToolDisplay = map[string]struct{ name, icon string }{
	"aivory_web_search": {"Aivory web search", "Search"},
	"web_fetch":         {"Read web page", "Globe"},
	"python_execute":    {"Python interpreter", "Terminal"},
	"image_generate":    {"Image generation", "Image"},
	"use_skill":         {"Use skill", "BookOpen"},
	"save_memory":       {"Save memory", "Brain"},
}

var hostedToolDescriptions = map[string]string{
	"web_search":       "Search the latest information on the web",
	"code_interpreter": "Run code and analyze files",
	"image_generation": "Generate or edit images",
}

// listSelectableToolsHandler returns one flat, user-safe catalog. It omits all
// source/category fields and every MCP connection detail; ids are opaque wire
// handles used only to enforce the selected subset on the next turn.
func listSelectableToolsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	if modelID == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	model, err := store.GetModel(r.Context(), d.DB, modelID)
	if err != nil || !model.Enabled || model.Kind != "chat" {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	items := []selectableToolResponse{}
	if model.ToolMode == "none" || d.Tools == nil {
		writeJSON(w, http.StatusOK, items)
		return
	}

	definitions := d.Tools.List(model.ID)
	registered := make([]string, 0, len(definitions))
	byName := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		registered = append(registered, definition.Name)
		byName[definition.Name] = definition.Description
	}
	disabled := map[string]bool{}
	if raw, settingErr := store.GetSetting(d.DB, "disabled_tools"); settingErr == nil && len(raw) > 0 {
		if names, _, parseErr := store.ParseBuiltinTools(raw); parseErr == nil {
			for _, name := range names {
				disabled[name] = true
			}
		}
	}
	user := authUser(r)
	if user == nil || !store.MemoryEnabledForUser(r.Context(), d.DB, user.ID) {
		disabled["save_memory"] = true
	}
	for _, name := range effectivePublicBuiltinTools(*model, registered, disabled) {
		display := builtinToolDisplay[name]
		label := display.name
		if label == "" {
			label = name
		}
		icon := display.icon
		if icon == "" {
			icon = "Wrench"
		}
		items = append(items, selectableToolResponse{
			ID: "builtin:" + name, Name: label, Description: byName[name], Icon: icon,
		})
	}

	if hosted, parseErr := store.ParseOfficialTools(model.OfficialTools); parseErr == nil {
		for _, definition := range hosted {
			name := strings.TrimSpace(definition.Name)
			if name == "" {
				continue
			}
			description := hostedToolDescriptions[name]
			if description == "" {
				description = "Use " + name + " when it is useful for the request."
			}
			icon := strings.TrimSpace(definition.Icon)
			if icon == "" {
				icon = "Wrench"
			}
			items = append(items, selectableToolResponse{
				ID: "hosted:" + name, Name: name, Description: description, Icon: icon,
			})
		}
	}

	seenMCP := map[string]bool{}
	for _, definition := range d.Tools.ListMCP(model.ID) {
		if seenMCP[definition.ServerID] {
			continue
		}
		seenMCP[definition.ServerID] = true
		items = append(items, selectableToolResponse{
			ID: "mcp:" + definition.ServerID, Name: definition.DisplayName,
			Description: definition.DisplayDescription, Icon: definition.Icon,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if left == right {
			return items[i].ID < items[j].ID
		}
		return left < right
	})
	writeJSON(w, http.StatusOK, items)
}
