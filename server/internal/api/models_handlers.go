package api

import (
	"context"
	"encoding/json"
	"errors"
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
	creditMultiplierDivisor = envcfg.F64("AIVORY_API_CREDIT_MULTIPLIER", 5.0)
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

// modelUsesCredits reports whether the model would be credit-charged for this
// user's group right now. A missing grant, including the all-toggles-off state,
// means there is no free allowance and every call uses credits.
func modelUsesCredits(ctx context.Context, d Deps, userID string, m store.Model, grants map[string]store.ModelGroupQuota) bool {
	q, granted := grants[m.ID]
	if !granted {
		return true
	}
	if q.LimitValue <= 0 {
		return false // granted unlimited free
	}
	if userID == "" || d.DB == nil {
		return true
	}
	scope, err := store.GetUserQuotaScope(ctx, d.DB, userID)
	if err != nil || scope.GroupID != q.GroupID {
		return true
	}
	start, _ := store.CreditCycleStart(scope.Anchor, q.PeriodSeconds, time.Now().Unix())
	scopeType := store.QuotaScopeModelChat
	if m.Kind == "image" {
		scopeType = store.QuotaScopeModelImage
	}
	used, err := store.ModelQuotaUsage(ctx, d.DB, userID, m.ID, scope.GroupID, scopeType, scope.Anchor, start)
	return err != nil || used >= q.LimitValue
}

// listModelsHandler returns chat models visible to all signed-in users.
func listModelsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	models, err := store.ListModels(r.Context(), d.DB, "chat", true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// §workspace RBAC phase 4: with a workspace scope, hide models the
	// workspace policy disables so pickers only offer allowed ones. Turn-time
	// enforcement remains authoritative — this is presentation only.
	var wsPolicy *store.WorkspacePolicy
	if workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id")); workspaceID != "" {
		u := authUser(r)
		role, memberErr := store.IsWorkspaceMember(r.Context(), d.DB, workspaceID, u.ID)
		if memberErr != nil || role == "" {
			writeError(w, 404, errNotFound)
			return
		}
		policy, policyErr := store.GetWorkspacePolicy(r.Context(), d.DB, workspaceID)
		if policyErr != nil {
			writeError(w, 500, policyErr)
			return
		}
		wsPolicy = &policy
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
		if wsPolicy != nil && !wsPolicy.ModelAllowedByPolicy(m.ID) {
			continue
		}
		filtered = append(filtered, m)
	}
	if wsPolicy != nil && fastAvailable {
		// The hidden fast model is subject to the same allowlist.
		if fastModel, fastErr := store.GetFastModel(r.Context(), d.DB); fastErr == nil && fastModel != nil &&
			!wsPolicy.ModelAllowedByPolicy(fastModel.ID) {
			fastAvailable = false
			fastVision = false
		}
	}
	resp := modelsResponse(d, r, filtered)
	resp["fast_available"] = fastAvailable
	resp["fast_vision"] = fastAvailable && fastVision
	writeJSON(w, 200, resp)
}

// listImageModelsHandler returns enabled image models.
func listImageModelsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	permissions, permissionErr := catalogPermissions(d, r)
	if permissionErr != nil || !permissions.AllowDrawing {
		writeJSON(w, http.StatusOK, map[string]any{"models": []any{}, "default_id": ""})
		return
	}
	var workspacePolicy *store.WorkspacePolicy
	// Drawing is a workspace capability as well as a group capability. Keep the
	// picker empty for members of a workspace that disabled direct image models;
	// enforceWorkspaceTurnPolicy remains the authoritative execution check.
	if workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id")); workspaceID != "" {
		u := authUser(r)
		if u == nil {
			writeJSON(w, http.StatusOK, map[string]any{"models": []any{}, "default_id": ""})
			return
		}
		if role, err := store.IsWorkspaceMember(r.Context(), d.DB, workspaceID, u.ID); err != nil || role == "" {
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
			} else {
				writeError(w, http.StatusNotFound, errNotFound)
			}
			return
		}
		policy, err := store.GetWorkspacePolicy(r.Context(), d.DB, workspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !policy.AllowDrawing {
			writeJSON(w, http.StatusOK, map[string]any{"models": []any{}, "default_id": ""})
			return
		}
		workspacePolicy = &policy
	}
	models, err := store.ListModels(r.Context(), d.DB, "image", true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// The execution gate rejects a model outside the workspace allowlist. Apply
	// the same ceiling to the picker so a member cannot select an image model
	// that will only fail after an SSE turn has already started.
	if workspacePolicy != nil && len(workspacePolicy.AllowedModelIDs) > 0 {
		filtered := models[:0]
		for _, model := range models {
			if workspacePolicy.ModelAllowedByPolicy(model.ID) {
				filtered = append(filtered, model)
			}
		}
		models = filtered
	}
	writeJSON(w, 200, modelsResponse(d, r, models))
}

type publicSkill struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	DisplayDescription string `json:"display_description"`
	Icon               string `json:"icon"`
	Enabled            bool   `json:"enabled"`
	SortOrder          int    `json:"sort_order"`
}

// listSkillsPublicHandler returns enabled skill display metadata. It excludes
// execution instructions, model-facing trigger descriptions, and asset paths.
func listSkillsPublicHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	permissions, permissionErr := catalogPermissions(d, r)
	if permissionErr != nil {
		writeError(w, http.StatusForbidden, errSkillGroupPermission)
		return
	}
	if workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id")); workspaceID != "" {
		allowed, err := workspaceLibraryUseEnabled(d, r, workspaceID, libraryCapabilitySkill)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, errNotFound)
			} else {
				writeError(w, http.StatusInternalServerError, err)
			}
			return
		}
		if !allowed {
			writeJSON(w, http.StatusOK, []publicSkill{})
			return
		}
	}
	skills, err := store.ListSkills(r.Context(), d.DB, true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	items := make([]publicSkill, 0, len(skills))
	for _, skill := range skills {
		if !store.ResourcePolicyAllows(permissions.Skills, skill.ID) {
			continue
		}
		items = append(items, publicSkill{
			ID: skill.ID, Name: skill.Name, DisplayDescription: strings.TrimSpace(skill.DisplayDescription),
			Icon: skill.Icon, Enabled: skill.Enabled, SortOrder: skill.SortOrder,
		})
	}
	writeJSON(w, 200, items)
}

// modelsResponse hides upstream credentials and only exports user-safe model
// fields. The default model id from settings is also returned so the
// frontend's model picker can default to it.
func modelsResponse(d Deps, r *http.Request, models []store.Model) map[string]any {
	type item struct {
		ID              string `json:"id"`
		Label           string `json:"label"`
		Description     string `json:"description"`
		Icon            string `json:"icon"`
		Kind            string `json:"kind"`
		Enabled         bool   `json:"enabled"`
		Vision          bool   `json:"vision"`
		Stream          bool   `json:"stream"`
		ResearchEnabled bool   `json:"research_enabled"`
		ToolMode        string `json:"tool_mode"`
		// BuiltinTools is the resolved, user-safe default selection. Unlike the
		// nullable admin policy, this always contains only live registry tools that
		// survive the global and user-group ceilings.
		BuiltinTools []string `json:"builtin_tools"`
		// ToolsAvailable is the unified user-facing capability bit. It remains true
		// when a model default is empty but the user may manually select a live tool.
		ToolsAvailable bool            `json:"tools_available"`
		ParamControls  json.RawMessage `json:"param_controls"`
		ChannelID      string          `json:"channel_id"`
		SortOrder      int             `json:"sort_order"`
		Currency       string          `json:"currency"`
		Tags           json.RawMessage `json:"tags"`
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

	// Resolve per-model free-allotment state for the caller's group. Missing rows
	// mean paid usage; an explicit row grants the configured free allowance.
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
	permissions, permissionErr := catalogPermissions(d, r)
	if permissionErr != nil {
		permissions = store.UserGroupPermissions{
			Prompts: store.ResourceAccessPolicy{Mode: store.ResourceAccessNone},
			Skills:  store.ResourceAccessPolicy{Mode: store.ResourceAccessNone},
			Tools:   store.ResourceAccessPolicy{Mode: store.ResourceAccessNone},
		}
	}
	// Workspace capability switches are presentation ceilings for model/tool
	// pickers. Execution re-checks the same policy in the orchestrator, but stale
	// clients should not be told that disabled tool classes are available.
	workspaceToolCallingAllowed := true
	workspaceMCPAllowed := true
	workspaceSkillsAllowed := true
	var workspacePolicy *store.WorkspacePolicy
	var workspaceMember *store.Workspace
	if workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id")); workspaceID != "" {
		policy, policyErr := store.GetWorkspacePolicy(r.Context(), d.DB, workspaceID)
		if policyErr != nil {
			workspaceToolCallingAllowed, workspaceMCPAllowed, workspaceSkillsAllowed = false, false, false
		} else {
			workspacePolicy = &policy
			workspaceToolCallingAllowed = policy.AllowToolCalling
			workspaceMCPAllowed = policy.AllowMCP
			workspaceSkillsAllowed = policy.AllowSkills
		}
		if workspace, memberErr := store.GetWorkspaceForMember(r.Context(), d.DB, workspaceID, userID); memberErr != nil || workspace == nil {
			workspaceToolCallingAllowed, workspaceMCPAllowed, workspaceSkillsAllowed = false, false, false
		} else {
			workspaceMember = workspace
			workspaceMCPAllowed = workspaceMCPAllowed && workspace.CanUseMCP
			workspaceSkillsAllowed = workspaceSkillsAllowed && workspace.CanUseSkills
		}
	}

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
		if names, _, parseErr := store.ParseBuiltinTools(raw); parseErr == nil {
			for _, name := range names {
				disabledBuiltinTools[name] = true
			}
		}
	}
	memoryEnabled := store.MemoryEnabledGlobal(d.DB)
	if memoryEnabled && userID != "" {
		memoryEnabled = store.MemoryEnabledForUser(r.Context(), d.DB, userID)
	}
	if !memoryEnabled {
		disabledBuiltinTools["save_memory"] = true
	}
	availableBuiltinTools := make(map[string]bool, len(registeredBuiltinTools))
	for _, name := range registeredBuiltinTools {
		if !workspaceToolCallingAllowed || (!workspaceSkillsAllowed && name == "use_skill") {
			continue
		}
		if workspacePolicy != nil && !workspaceCatalogToolAllowed("builtin:"+name, workspacePolicy, workspaceMember) {
			continue
		}
		if disabledBuiltinTools[name] || !toolPolicyAllowsID(permissions, "builtin:"+name) {
			continue
		}
		availableBuiltinTools[name] = true
	}
	mcpToolsAvailable := false
	if workspaceToolCallingAllowed && workspaceMCPAllowed && d.Tools != nil {
		workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
		mcpScope := toolPolicyScope{ctx: r.Context(), db: d.DB, userID: userID, workspaceID: workspaceID}
		for _, definition := range d.Tools.ListMCP("", userID, workspaceID) {
			id := "mcp:" + definition.ServerID
			if definition.UserOwned {
				id = userMCPToolIDPrefix + definition.ServerID
			}
			if workspacePolicy != nil && !workspaceCatalogToolAllowed(id, workspacePolicy, workspaceMember) {
				continue
			}
			if toolPolicyAllowsID(permissions, id, mcpScope) {
				mcpToolsAvailable = true
				break
			}
		}
	}

	items := []item{}
	for _, m := range models {
		tags := m.Tags
		if tags == nil {
			tags = json.RawMessage("[]")
		}
		usesCredits := !isAdmin && modelUsesCredits(r.Context(), d, userID, m, grants)
		creditsPerImage := 0.0
		if usesCredits {
			creditsPerImage = imageCreditCost(m, creditsPerUSD)
		}
		builtinTools := effectivePublicBuiltinTools(m, registeredBuiltinTools, disabledBuiltinTools)
		builtinDefaults := builtinTools[:0]
		for _, name := range builtinTools {
			if availableBuiltinTools[name] {
				builtinDefaults = append(builtinDefaults, name)
			}
		}
		hostedToolsAvailable := false
		if workspaceToolCallingAllowed && m.ToolMode != "none" {
			if definitions, err := store.ParseOfficialTools(m.OfficialTools); err == nil {
				for _, definition := range definitions {
					id := "hosted:" + strings.TrimSpace(definition.Name)
					if workspacePolicy != nil && !workspaceCatalogToolAllowed(id, workspacePolicy, workspaceMember) {
						continue
					}
					if toolPolicyAllowsID(permissions, id) {
						hostedToolsAvailable = true
						break
					}
				}
			}
		}
		items = append(items, item{
			ID: m.ID, Label: m.Label, Description: m.Description, Icon: m.Icon,
			Kind: m.Kind, Enabled: m.Enabled, Vision: m.Vision, Stream: m.Stream, ResearchEnabled: m.ResearchEnabled, ToolMode: m.ToolMode,
			BuiltinTools:   builtinDefaults,
			ToolsAvailable: m.ToolMode != "none" && (len(availableBuiltinTools) > 0 || hostedToolsAvailable || mcpToolsAvailable),
			ParamControls:  m.ParamControls, ChannelID: m.ChannelID, SortOrder: m.SortOrder,
			Currency:        m.Currency,
			Tags:            tags,
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
