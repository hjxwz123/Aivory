package api

import (
	"net/http"
	"sort"
	"strings"

	"aivory/server/internal/store"
)

type selectableToolResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Icon            string `json:"icon"`
	Allowed         bool   `json:"allowed"`
	DefaultSelected bool   `json:"default_selected"`
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
	permissions, permissionErr := catalogPermissions(d, r)
	if permissionErr != nil {
		writeError(w, http.StatusForbidden, errToolGroupPermission)
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
	// Instance-wide availability removes a candidate entirely. Group policy is
	// represented by Allowed below so users can see restricted tools without
	// being able to select or invoke them. save_memory is the exception: when
	// memory is unavailable globally or to this group it must not be exposed.
	if !store.MemoryEnabledGlobal(d.DB) {
		disabled["save_memory"] = true
	}
	if user := authUser(r); user != nil && !store.MemoryEnabledForUser(r.Context(), d.DB, user.ID) {
		disabled["save_memory"] = true
	}
	if !permissions.AllowMemory {
		disabled["save_memory"] = true
	}
	defaultBuiltinTools := make(map[string]bool)
	for _, name := range effectivePublicBuiltinTools(*model, registered, disabled) {
		defaultBuiltinTools[name] = true
	}
	defaultMCPServerIDs := map[string]bool{}
	configuredMCPServerIDs, mcpDefaultsConfigured, mcpDefaultsErr := store.ParseMCPServerIDs(model.MCPServerIDs)
	if mcpDefaultsErr == nil {
		for _, id := range configuredMCPServerIDs {
			defaultMCPServerIDs[id] = true
		}
	}
	for _, name := range registered {
		if disabled[name] {
			continue
		}
		id := "builtin:" + name
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
			ID: id, Name: label, Description: byName[name], Icon: icon,
			Allowed: toolPolicyAllowsID(permissions, id), DefaultSelected: defaultBuiltinTools[name],
		})
	}

	if hosted, parseErr := store.ParseOfficialTools(model.OfficialTools); parseErr == nil {
		for _, definition := range hosted {
			name := strings.TrimSpace(definition.Name)
			if name == "" {
				continue
			}
			id := "hosted:" + name
			description := hostedToolDescriptions[name]
			if description == "" {
				description = "Use " + name + " when it is useful for the request."
			}
			icon := strings.TrimSpace(definition.Icon)
			if icon == "" {
				icon = "Wrench"
			}
			items = append(items, selectableToolResponse{
				ID: id, Name: name, Description: description, Icon: icon,
				Allowed: toolPolicyAllowsID(permissions, id), DefaultSelected: true,
			})
		}
	}

	seenMCP := map[string]bool{}
	for _, definition := range d.Tools.ListMCP(model.ID) {
		if seenMCP[definition.ServerID] {
			continue
		}
		seenMCP[definition.ServerID] = true
		id := "mcp:" + definition.ServerID
		items = append(items, selectableToolResponse{
			ID: id, Name: definition.DisplayName,
			Description: definition.DisplayDescription, Icon: definition.Icon,
			Allowed:         toolPolicyAllowsID(permissions, id),
			DefaultSelected: mcpDefaultsErr == nil && (!mcpDefaultsConfigured || defaultMCPServerIDs[definition.ServerID]),
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
