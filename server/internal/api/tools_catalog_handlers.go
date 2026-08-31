package api

import (
	"errors"
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
	"fetch_image":       {"Download image", "Download"},
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
	// Resolve the workspace capability once for the whole catalog request. The
	// group policy below controls whether a row is selectable; workspace policy
	// controls whether a capability is exposed at all. Keeping the two checks
	// separate is important for the owner exemption on user MCP rows.
	var workspacePolicy *store.WorkspacePolicy
	var workspaceMember *store.Workspace
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if workspaceID != "" {
		user := authUser(r)
		if user == nil {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		member, memberErr := store.GetWorkspaceForMember(r.Context(), d.DB, workspaceID, user.ID)
		if errors.Is(memberErr, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		if memberErr != nil {
			writeError(w, http.StatusInternalServerError, memberErr)
			return
		}
		policy, policyErr := store.GetWorkspacePolicy(r.Context(), d.DB, workspaceID)
		if policyErr != nil {
			writeError(w, http.StatusInternalServerError, policyErr)
			return
		}
		workspaceMember, workspacePolicy = member, &policy
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
		if !workspaceCatalogToolAllowed(id, workspacePolicy, workspaceMember) {
			continue
		}
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
			if !workspaceCatalogToolAllowed(id, workspacePolicy, workspaceMember) {
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
				ID: id, Name: name, Description: description, Icon: icon,
				Allowed: toolPolicyAllowsID(permissions, id), DefaultSelected: true,
			})
		}
	}

	seenMCP := map[string]bool{}
	for _, definition := range d.Tools.ListMCP(model.ID, "", "") {
		if seenMCP[definition.ServerID] {
			continue
		}
		seenMCP[definition.ServerID] = true
		id := "mcp:" + definition.ServerID
		if !workspaceCatalogToolAllowed(id, workspacePolicy, workspaceMember) {
			continue
		}
		items = append(items, selectableToolResponse{
			ID: id, Name: definition.DisplayName,
			Description: definition.DisplayDescription, Icon: definition.Icon,
			Allowed:         toolPolicyAllowsID(permissions, id),
			DefaultSelected: mcpDefaultsErr == nil && mcpDefaultsConfigured && defaultMCPServerIDs[definition.ServerID],
		})
	}

	// §user MCP: one row per user-owned server. A row is offered only when it is enabled and
	// a successful sync stored tools — never-synced or broken endpoints would
	// otherwise create ghost selections. Workspace capability checks happen here
	// as well as in the runtime registry, so a member denied MCP never sees a
	// selectable row. The requesting user's personal and active-workspace rows
	// are the only ones the store scoping returns; group Tools policy still
	// applies to servers owned by teammates, while the requester's own servers
	// are exempt via the toolPolicyScope below.
	if user := authUser(r); user != nil {
		readScopes := []string{""}
		if workspaceID != "" {
			readScopes = append(readScopes, workspaceID)
		}
		scope := toolPolicyScope{ctx: r.Context(), db: d.DB, userID: user.ID, workspaceID: workspaceID}
		for _, scopeID := range readScopes {
			userServers, serversErr := store.ListUserMCPServersScoped(r.Context(), d.DB, user.ID, scopeID)
			if serversErr != nil {
				writeError(w, http.StatusInternalServerError, serversErr)
				return
			}
			for _, server := range userServers {
				if !server.Enabled || !mcpDiscoveredToolsPresent(server.DiscoveredTools) {
					continue
				}
				id := userMCPToolIDPrefix + server.ID
				if !workspaceCatalogToolAllowed(id, workspacePolicy, workspaceMember) {
					continue
				}
				items = append(items, selectableToolResponse{
					ID: id, Name: server.Name, Description: server.Description, Icon: server.Icon,
					Allowed: toolPolicyAllowsID(permissions, id, scope), DefaultSelected: false,
				})
			}
		}
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

// workspaceCatalogToolAllowed is deliberately stricter than the group
// `Allowed` flag. A workspace switch removes a capability from the catalog;
// it must not merely mark the row disabled because callers can otherwise keep
// stale selections and repeatedly attempt a forbidden operation. User MCP
// ids skip the administrator MCP allowlist (`AllowedMCPServerIDs`): those rows
// are governed by the explicit workspace MCP switch and the member capability
// and receive their group-level owner exemption separately.
func workspaceCatalogToolAllowed(
	id string,
	policy *store.WorkspacePolicy,
	member *store.Workspace,
) bool {
	if policy == nil {
		return true
	}
	if !policy.AllowToolCalling {
		return false
	}
	if strings.HasPrefix(id, "usermcp:") {
		return policy.AllowMCP && (member == nil || member.CanUseMCP)
	}
	if strings.HasPrefix(id, "mcp:") {
		return policy.AllowMCP && (member == nil || member.CanUseMCP) && !policy.ToolDeniedByPolicy(id)
	}
	if id == "builtin:use_skill" && (!policy.AllowSkills || (member != nil && !member.CanUseSkills)) {
		return false
	}
	return !policy.ToolDeniedByPolicy(id)
}
