package api

import (
	"encoding/json"
	"net/http"

	"aivory/server/internal/store"
)

// listBuiltinToolsAdmin exposes the live local-tool registry so the model
// editor cannot drift from tools that the server can actually declare and run.
// Registry.List is deterministic (sorted by name).
func listBuiltinToolsAdmin(d Deps, w http.ResponseWriter, _ *http.Request) {
	type item struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		GloballyEnabled bool   `json:"globally_enabled"`
	}
	disabled := map[string]bool{}
	memoryEnabled := true
	if d.DB != nil {
		if raw, err := store.GetSetting(d.DB, "disabled_tools"); err == nil && len(raw) > 0 {
			var names []string
			if json.Unmarshal(raw, &names) == nil {
				for _, name := range names {
					disabled[name] = true
				}
			}
		}
		memoryEnabled = store.MemoryEnabledGlobal(d.DB)
	}
	items := []item{}
	if d.Tools != nil {
		for _, definition := range d.Tools.List("") {
			globallyEnabled := !disabled[definition.Name]
			if definition.Name == "save_memory" && !memoryEnabled {
				globallyEnabled = false
			}
			items = append(items, item{
				Name:            definition.Name,
				Description:     definition.Description,
				GloballyEnabled: globallyEnabled,
			})
		}
	}
	writeJSON(w, http.StatusOK, items)
}
