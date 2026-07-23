package api

import "net/http"

// listBuiltinToolsAdmin exposes the live local-tool registry so the model
// editor cannot drift from tools that the server can actually declare and run.
// Registry.List is deterministic (sorted by name).
func listBuiltinToolsAdmin(d Deps, w http.ResponseWriter, _ *http.Request) {
	type item struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	items := []item{}
	if d.Tools != nil {
		for _, definition := range d.Tools.List("") {
			items = append(items, item{Name: definition.Name, Description: definition.Description})
		}
	}
	writeJSON(w, http.StatusOK, items)
}
