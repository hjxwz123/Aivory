package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// Admin workspace listing page-size knobs — overridable via env (see
// docs/config-reference.md); defaults preserve the previous hardcoded values.
var (
	adminWorkspaceListLimit                   = envcfg.Int("AIVORY_API_LIMIT", 200)
	adminWorkspaceDetailConversationsPageSize = envcfg.Int("AIVORY_API_ADMIN_WORKSPACE_DETAIL_CONVERSATIONS_PAGE_SIZE", 500)
)

// Workspaces (§workspaces) — fully-isolated collaborative spaces. Creation is
// gated by the group's 'workspaces' feature flag + max_workspaces cap; joining
// happens ONLY through the invite link; the owner can kick members, rotate the
// link, and delete the whole space (cascading every conversation/project/KB).

// createWorkspaceHandler makes a new workspace owned by the caller.
func createWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	// Group gate (§workspaces admin control): the 'workspaces' feature flag says
	// whether this tier may create spaces at all; max_workspaces caps how many
	// the user may OWN (0 = unlimited). Admins bypass (parity with research).
	if u.Role != "admin" {
		if !userGroupHasFeature(r.Context(), d, u.GroupID, "workspaces") {
			writeError(w, 403, errWorkspaceDisabled)
			return
		}
		gid := u.GroupID
		if gid == "" {
			gid = store.DefaultGroupID
		}
		if g, err := store.GetUserGroup(r.Context(), d.DB, gid); err == nil && g != nil && g.MaxWorkspaces > 0 {
			if n, err := store.CountOwnedWorkspaces(r.Context(), d.DB, u.ID); err == nil && n >= g.MaxWorkspaces {
				writeError(w, 403, errWorkspaceLimit)
				return
			}
		}
	}
	ws, err := store.CreateWorkspace(r.Context(), d.DB, u.ID, req.Name)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, ws)
}

// listWorkspacesHandler returns the caller's workspaces (role + member count;
// invite token only on owned ones).
func listWorkspacesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	list, err := store.ListWorkspacesForUser(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workspaces": list})
}

// workspaceMembersHandler lists members — visible to every current member.
// Emails are admin-only surface: members and guests see names/avatars/roles
// (§workspace RBAC).
func workspaceMembersHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: id, UserID: u.ID, Action: store.ActionWorkspaceMemberView,
	})
	if err != nil || !decision.Allowed {
		writeError(w, 404, errNotFound)
		return
	}
	members, err := store.ListWorkspaceMembers(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !decision.IsAdmin {
		for i := range members {
			members[i].Email = ""
		}
	}
	writeJSON(w, 200, map[string]any{"members": members})
}

// publishWorkspaceAccessEvent notifies every current member because workspace
// owners and knowledge-base creators can both have permission-management
// dialogs open. Notifying only the changed member leaves those manager views
// stale in other tabs. extraUserIDs covers a member who was just removed.
func publishWorkspaceAccessEvent(d Deps, r *http.Request, workspaceID, eventType string, extraUserIDs ...string) {
	recipients := map[string]struct{}{}
	if members, err := store.ListWorkspaceMembers(r.Context(), d.DB, workspaceID); err == nil {
		for _, member := range members {
			if id := strings.TrimSpace(member.UserID); id != "" {
				recipients[id] = struct{}{}
			}
		}
	}
	for _, userID := range extraUserIDs {
		if id := strings.TrimSpace(userID); id != "" {
			recipients[id] = struct{}{}
		}
	}
	for userID := range recipients {
		publishUserEvent(d, r, userID, eventType, "")
	}
}

// updateWorkspaceMemberPermissionsHandler changes the member-wide capability
// ceiling. Admins may tighten ordinary members and guests; the owner and
// per-knowledge-base permissions are managed separately. Per-knowledge-base
// permissions are managed separately on each KB.
func updateWorkspaceMemberPermissionsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	memberID := pathParam(r, "uid")
	var permissions store.WorkspaceMemberPermissions
	if err := decodeJSON(r, &permissions); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID,
		Action:   store.ActionWorkspaceMemberPermissions,
		Resource: "workspace_member", ResourceID: memberID,
	})
	if err != nil || !decision.Allowed {
		writeError(w, 404, errNotFound)
		return
	}
	member, err := store.UpdateWorkspaceMemberPermissions(
		r.Context(), d.DB, workspaceID, u.ID, memberID, permissions,
	)
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			// The store repeats the actor/target check inside the membership
			// transaction. A concurrent demotion can therefore race the handler's
			// optimistic authorization check; surface that as a policy denial rather
			// than leaking it as a generic server failure.
			writeError(w, http.StatusForbidden, errForbidden)
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Capability reductions can remove tool/file authority from an active turn.
	// Cancel the member's existing work; a fresh turn captures the new ceiling.
	revokeWorkspaceMemberGenerations(d, workspaceID, memberID)
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.permissions_updated", u.ID, memberID)
	writeJSON(w, http.StatusOK, member)
}

// updateWorkspaceMemberRoleHandler applies the §workspace RBAC role ladder:
// the owner may grant/revoke admin and move anyone between member and guest;
// ordinary admins may only switch ordinary users between member and guest.
func updateWorkspaceMemberRoleHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	memberID := pathParam(r, "uid")
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil || !store.ValidWorkspaceMemberRole(req.Role) {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID,
		Action:   store.ActionWorkspaceMemberRoleUpdate,
		Resource: "workspace_member", ResourceID: memberID,
		NewRole: req.Role,
	})
	if err != nil || !decision.Allowed {
		// Uniform 404 for non-members; 403 would leak which actors hold
		// which power only when they are already members, which is fine.
		if err == nil && !decision.Allowed {
			writeError(w, http.StatusForbidden, errForbidden)
			return
		}
		writeError(w, 404, errNotFound)
		return
	}
	member, err := store.UpdateWorkspaceMemberRole(r.Context(), d.DB, workspaceID, u.ID, memberID, req.Role)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	// The role update is committed before this broadcast. Any turn that began
	// with the old role is stopped, while a later promotion is free to start a
	// new turn without inheriting a permanent tombstone.
	revokeWorkspaceMemberGenerations(d, workspaceID, memberID)
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.permissions_updated", u.ID, memberID)
	writeJSON(w, http.StatusOK, member)
}

// kickWorkspaceMemberHandler removes a member — admins may remove ordinary
// members and guests; only the owner may remove another admin. The owner row
// itself is protected in the store.
func kickWorkspaceMemberHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	memberID := pathParam(r, "uid")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: id, UserID: u.ID,
		Action:   store.ActionWorkspaceMemberRemove,
		Resource: "workspace_member", ResourceID: memberID,
	})
	if err != nil || !decision.Allowed {
		if err == nil && !decision.Allowed {
			writeError(w, http.StatusForbidden, errForbidden)
			return
		}
		writeError(w, 404, errNotFound)
		return
	}
	revokedMessageIDs, err := store.RemoveWorkspaceMemberWithRevokedGenerations(r.Context(), d.DB, id, u.ID, memberID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		default:
			writeError(w, 404, errNotFound)
		}
		return
	}
	if err := revokeMessageGenerationStreams(d, revokedMessageIDs); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	revokeWorkspaceMemberGenerations(d, id, memberID)
	publishWorkspaceAccessEvent(d, r, id, "workspace.membership_updated", u.ID, memberID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// leaveWorkspaceHandler removes the CALLER's membership. Owners can't leave —
// they delete the workspace instead.
func leaveWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	revokedMessageIDs, err := store.LeaveWorkspaceWithRevokedGenerations(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	if err := revokeMessageGenerationStreams(d, revokedMessageIDs); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	revokeWorkspaceMemberGenerations(d, id, u.ID)
	publishWorkspaceAccessEvent(d, r, id, "workspace.membership_updated", u.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// rotateWorkspaceInviteHandler mints a fresh bounded quick-link. The store
// repeats the authorization after acquiring the workspace lock, preventing a
// stale handler check from succeeding after the caller is demoted or removed.
func rotateWorkspaceInviteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: id, UserID: u.ID, Action: store.ActionWorkspaceMemberInvite,
	})
	if err != nil || !decision.Allowed {
		writeError(w, 404, errNotFound)
		return
	}
	token, err := store.RotateWorkspaceInvite(r.Context(), d.DB, id, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, 200, map[string]string{"invite_token": token})
}

// workspaceInviteInfoHandler resolves a governed invite record to a join
// preview. Auth'd + rate-limited; uniform 404 on unknown/dead tokens so the
// space cannot be enumerated. The former permanent workspace token is never
// accepted as a fallback.
func workspaceInviteInfoHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	token := pathParam(r, "token")
	preview, err := store.GetWorkspaceInvitePreview(r.Context(), d.DB, token)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": preview.WorkspaceID, "name": preview.Name, "owner_name": preview.OwnerName,
		"member_count": preview.MemberCount, "role": preview.Role, "email_bound": preview.EmailBound,
		"kind": "invite",
	})
}

// joinWorkspaceHandler consumes a governed invite record. Expiry, revocation,
// use limits and email binding apply to every accepted token; the permanent
// legacy workspace token has no fallback path.
func joinWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	token := pathParam(r, "token")
	ws, role, err := store.JoinWorkspaceByInviteRecord(r.Context(), d.DB, token, u.ID, u.Email)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	publishWorkspaceAccessEvent(d, r, ws.ID, "workspace.membership_updated", u.ID, ws.OwnerID)
	writeJSON(w, 200, map[string]any{"id": ws.ID, "name": ws.Name, "role": role})
}

// createWorkspaceInviteHandler mints an invite record. Admins may invite
// members/guests; only the owner may mint admin invites (§workspace RBAC §11).
func createWorkspaceInviteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	var req struct {
		Role      string `json:"role"`
		Email     string `json:"email"`
		ExpiresAt int64  `json:"expires_at"`
		MaxUses   int64  `json:"max_uses"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	// §workspace RBAC: new invites default to the read-only guest role.
	if strings.TrimSpace(req.Role) == "" {
		req.Role = store.WorkspaceRoleGuest
	}
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID, Action: store.ActionWorkspaceMemberInvite,
	})
	if err != nil || !decision.Allowed {
		writeError(w, 404, errNotFound)
		return
	}
	if req.Role == store.WorkspaceRoleAdmin && !decision.IsOwner {
		writeError(w, http.StatusForbidden, errForbidden)
		return
	}
	invite, err := store.CreateWorkspaceInvite(r.Context(), d.DB, workspaceID, u.ID,
		req.Email, req.Role, req.ExpiresAt, req.MaxUses)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusBadRequest, err)
		}
		return
	}
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.permissions_updated", u.ID)
	writeJSON(w, 201, invite)
}

// listWorkspaceInvitesHandler returns the invite records (tokens included —
// admins share the links; member/guest callers are rejected in the store).
func listWorkspaceInvitesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	invites, err := store.ListWorkspaceInvites(r.Context(), d.DB, workspaceID, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, 200, map[string]any{"invites": invites})
}

// revokeWorkspaceInviteHandler kills an invite record.
func revokeWorkspaceInviteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	inviteID := pathParam(r, "inviteId")
	if err := store.RevokeWorkspaceInvite(r.Context(), d.DB, workspaceID, u.ID, inviteID); err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.permissions_updated", u.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// transferWorkspaceOwnershipHandler moves the canonical owner. Owner-only;
// the receiver must be a current member and becomes admin; the old owner
// keeps the admin role and immediately loses owner-exclusive authority.
func transferWorkspaceOwnershipHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.UserID) == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID, Action: store.ActionWorkspaceTransfer,
	})
	if err != nil || !decision.Allowed {
		if err == nil && !decision.Allowed {
			writeError(w, http.StatusForbidden, errForbidden)
			return
		}
		writeError(w, 404, errNotFound)
		return
	}
	ws, err := store.TransferWorkspaceOwnership(r.Context(), d.DB, workspaceID, u.ID, req.UserID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.membership_updated", u.ID, req.UserID)
	writeJSON(w, 200, ws)
}

// getWorkspacePolicyHandler returns the effective capability policy. Every
// current member may read it (the UI hides disabled capabilities with it);
// the backend re-checks it at every execution point regardless.
func getWorkspacePolicyHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	if _, err := store.GetWorkspaceForMember(r.Context(), d.DB, workspaceID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	policy, err := store.GetWorkspacePolicy(r.Context(), d.DB, workspaceID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, policy)
}

type workspacePolicyModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// listWorkspacePolicyModelsHandler returns the complete enabled model catalog
// needed to edit a workspace allowlist. It deliberately ignores the acting
// administrator's user-group drawing entitlement: this policy controls every
// workspace member, so using the actor's ordinary picker would silently omit
// image models and make an explicit allowlist destructive. Fast model identity
// remains hidden, matching the public model catalog; selecting an explicit
// allowlist therefore continues to disable fast mode for that workspace.
func listWorkspacePolicyModelsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID, Action: store.ActionWorkspaceSettingsUpdate,
	})
	if err != nil || !decision.Allowed {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	models, err := store.ListModels(r.Context(), d.DB, "", true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	response := make([]workspacePolicyModel, 0, len(models))
	for _, model := range models {
		if model.Fast || (model.Kind != "chat" && model.Kind != "image") {
			continue
		}
		response = append(response, workspacePolicyModel{
			ID: model.ID, Label: model.Label, Kind: model.Kind,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": response})
}

// updateWorkspacePolicyHandler narrows the workspace capability policy.
// Workspace admins only; the store re-authorizes inside the membership-lock
// transaction. Guests never gain from a permissive policy — their read-only
// boundary is role-based, not policy-based.
func updateWorkspacePolicyHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	var req struct {
		AllowedModelIDs          *[]string `json:"allowed_model_ids"`
		AllowedToolIDs           *[]string `json:"allowed_tool_ids"`
		AllowedMCPServerIDs      *[]string `json:"allowed_mcp_server_ids"`
		AllowToolCalling         *bool     `json:"allow_tool_calling"`
		AllowDrawing             *bool     `json:"allow_drawing"`
		AllowMCP                 *bool     `json:"allow_mcp"`
		AllowSkills              *bool     `json:"allow_skills"`
		AllowPrompts             *bool     `json:"allow_prompts"`
		AllowSandbox             *bool     `json:"allow_sandbox"`
		AllowImageGeneration     *bool     `json:"allow_image_generation"`
		AllowKnowledgeBases      *bool     `json:"allow_knowledge_bases"`
		AllowFileUpload          *bool     `json:"allow_file_upload"`
		MemberMonthlyCreditLimit *float64  `json:"member_monthly_credit_limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID, Action: store.ActionWorkspaceSettingsUpdate,
	})
	if err != nil || !decision.Allowed {
		writeError(w, 404, errNotFound)
		return
	}
	policy, err := store.UpdateWorkspacePolicy(r.Context(), d.DB, workspaceID, u.ID, store.WorkspacePolicyPatch{
		AllowedModelIDs:          req.AllowedModelIDs,
		AllowedToolIDs:           req.AllowedToolIDs,
		AllowedMCPServerIDs:      req.AllowedMCPServerIDs,
		AllowToolCalling:         req.AllowToolCalling,
		AllowDrawing:             req.AllowDrawing,
		AllowMCP:                 req.AllowMCP,
		AllowSkills:              req.AllowSkills,
		AllowPrompts:             req.AllowPrompts,
		AllowSandbox:             req.AllowSandbox,
		AllowImageGeneration:     req.AllowImageGeneration,
		AllowKnowledgeBases:      req.AllowKnowledgeBases,
		AllowFileUpload:          req.AllowFileUpload,
		MemberMonthlyCreditLimit: req.MemberMonthlyCreditLimit,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusBadRequest, err)
		}
		return
	}
	// A tool loop may be between provider rounds when policy changes. Broadcast
	// after the transactional write so current turns are cancelled and later
	// turns read the newly committed policy.
	revokeWorkspacePolicyGenerations(d, workspaceID)
	// Capability changes must reach clients without a re-login: notify every
	// member so model/tool catalogs and composer affordances refresh.
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.policy_updated", u.ID)
	writeJSON(w, 200, policy)
}

// workspaceUsageHandler is the admins' per-member usage rollup for the
// workspace (§workspace RBAC phase 4).
func workspaceUsageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID, Action: store.ActionUsageView,
	})
	if err != nil || !decision.Allowed {
		writeError(w, 404, errNotFound)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	rows, err := store.SumWorkspaceUsageByMember(r.Context(), d.DB, workspaceID, days)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "usage": rows})
}

// deleteWorkspaceHandler tears down the whole space — OWNER ONLY (§workspaces:
// 只有创建者可以删除). Every conversation/project/KB inside is deleted through
// the existing per-entity deleters (so vector-store cleanup and FK cascades all
// run), then the workspace row goes (members cascade with it).
func deleteWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	ws, err := store.GetWorkspaceForMember(r.Context(), d.DB, id, u.ID)
	if err != nil || ws.OwnerID != u.ID {
		writeError(w, 404, errNotFound)
		return
	}
	members, err := store.ListWorkspaceMembers(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := teardownWorkspace(d, r, ws); err != nil {
		writeError(w, 500, err)
		return
	}
	// The audit row outlives the workspace itself (no FK by design).
	_ = store.RecordWorkspaceDeleted(r.Context(), d.DB, ws.ID, u.ID, ws.Name)
	for _, member := range members {
		publishUserEvent(d, nil, member.UserID, "workspace.membership_updated", "")
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// teardownWorkspace deletes every conversation/project/KB inside the workspace
// through the existing per-entity deleters (vector cleanup included), then the
// workspace row itself. Acts AS THE OWNER (a member) so the member-aware
// deleters admit the operation regardless of which authorized caller (owner or
// admin) triggered it.
func teardownWorkspace(d Deps, r *http.Request, ws *store.Workspace) (returnErr error) {
	// Persist the fence before taking the worklist. Every scoped resource create
	// shares the workspace mutation lock and checks this flag, so no new child
	// can appear after WorkspaceContentIDs has taken its snapshot.
	if err := store.MarkWorkspaceDeleting(r.Context(), d.DB, ws.ID, ws.OwnerID); err != nil {
		return err
	}
	userMCPServerIDs, err := store.UserMCPServerIDsForWorkspace(r.Context(), d.DB, ws.ID)
	if err != nil {
		return err
	}
	// From this point every error must make the fence retryable, including a
	// failure before streaming messages have been scrubbed.
	teardownComplete := false
	defer func() {
		if !teardownComplete {
			d.Cache.Delete(workspaceGenerationRevocationKey(ws.ID))
			if clearErr := store.ClearWorkspaceDeleting(context.Background(), d.DB, ws.ID, ws.OwnerID); clearErr != nil && d.Logger != nil {
				d.Logger.Printf("workspace %s teardown fence reset: %v", ws.ID, clearErr)
			}
		}
	}()
	revokedMessageIDs, err := store.ScrubWorkspaceStreamingMessages(r.Context(), d.DB, ws.ID)
	if err != nil {
		return err
	}
	if !publishWorkspaceGenerationRevocation(d, ws.ID) {
		return errors.New("workspace generation revocation unavailable")
	}
	// If a later teardown step fails, the deferred fence reset restores the
	// workspace for new turns. Per-message tombstones never revive.
	if err := revokeMessageGenerationStreams(d, revokedMessageIDs); err != nil {
		return err
	}
	convIDs, projectIDs, kbIDs, err := store.WorkspaceContentIDs(r.Context(), d.DB, ws.ID)
	if err != nil {
		return err
	}
	var teardownErr error
	recordTeardownError := func(kind, id string, err error) {
		wrapped := fmt.Errorf("%s %s: %w", kind, id, err)
		teardownErr = errors.Join(teardownErr, wrapped)
		if d.Logger != nil {
			d.Logger.Printf("workspace %s teardown: %v", ws.ID, wrapped)
		}
	}
	for _, cid := range convIDs {
		ids, _ := store.ConversationTreeIDs(r.Context(), d.DB, cid)
		storagePaths, _ := store.StoragePathsForConversations(r.Context(), d.DB, ids)
		if _, err := store.DeleteConversationByID(r.Context(), d.DB, cid); err != nil {
			recordTeardownError("conversation", cid, err)
			continue
		}
		if len(ids) == 0 {
			ids = []string{cid}
		}
		for _, id := range ids {
			cleanupRAGConversation(r.Context(), d, id, "workspace "+ws.ID+" conversation "+cid)
		}
		cleanupStoragePaths(r.Context(), d, storagePaths, "workspace "+ws.ID+" conversation "+cid)
	}
	for _, kid := range kbIDs {
		// The owner is a member, so the member-aware DeleteKB admits them; it also
		// sweeps kb_ids references. Vector cleanup mirrors deleteKBHandler.
		docs, _ := store.ListDocuments(r.Context(), d.DB, "kb", kid)
		storagePaths := make([]string, 0, len(docs))
		for _, doc := range docs {
			storagePaths = append(storagePaths, doc.StoragePath)
		}
		if err := store.DeleteKB(r.Context(), d.DB, kid, ws.OwnerID, d.Config.UploadDir, d.Config.ArtifactDir); err != nil {
			recordTeardownError("knowledge base", kid, err)
			continue
		}
		cleanupRAGKB(r.Context(), d, kid, "workspace "+ws.ID+" kb "+kid)
		cleanupStoragePaths(r.Context(), d, storagePaths, "workspace "+ws.ID+" kb "+kid)
	}
	for _, pid := range projectIDs {
		deletion, err := store.DeleteProjectWithState(r.Context(), d.DB, pid, ws.OwnerID, d.Config.UploadDir, d.Config.ArtifactDir)
		if err != nil {
			recordTeardownError("project", pid, err)
			continue
		}
		for _, kbID := range deletion.KnowledgeBaseIDs {
			cleanupRAGKB(r.Context(), d, kbID, "workspace "+ws.ID+" project "+pid)
		}
		cleanupStoragePaths(r.Context(), d, deletion.StoragePaths, "workspace "+ws.ID+" project "+pid)
	}
	// workspace_id is intentionally additive on the content tables and has no
	// FK back to workspaces. Never remove the parent row after a child DB delete
	// failed, otherwise the remaining content becomes orphaned and unmanageable.
	if teardownErr != nil {
		return fmt.Errorf("workspace %s teardown incomplete: %w", ws.ID, teardownErr)
	}
	if err := store.DeleteWorkspaceRow(r.Context(), d.DB, ws.ID, ws.OwnerID); err != nil {
		return err
	}
	if d.Tools != nil {
		for _, serverID := range userMCPServerIDs {
			d.Tools.InvalidateMCPServer(serverID)
		}
	}
	teardownComplete = true
	return nil
}

// --- Admin (§workspaces 管理端) -------------------------------------------

// adminListWorkspacesHandler lists every workspace with owner + member count.
func adminListWorkspacesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	limit, offset := adminWorkspaceListLimit, 0
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n >= 0 {
		offset = n
	}
	list, err := store.ListAllWorkspaces(r.Context(), d.DB, limit, offset)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workspaces": list})
}

// adminWorkspaceDetailHandler returns one workspace with members, conversations,
// projects and KBs (triage view).
func adminWorkspaceDetailHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	ws, err := store.GetWorkspace(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	ws.InviteToken = "" // never leak the join capability to the admin UI
	members, _ := store.ListWorkspaceMembers(r.Context(), d.DB, id)
	convs, _ := store.ListWorkspaceConversations(r.Context(), d.DB, id, "", "any", adminWorkspaceDetailConversationsPageSize, 0)
	projects, _ := store.ListWorkspaceProjects(r.Context(), d.DB, id)
	kbs, _ := store.ListWorkspaceKBs(r.Context(), d.DB, id)
	for i := range convs {
		stripServerConvFields(&convs[i])
	}
	writeJSON(w, 200, map[string]any{
		"workspace": ws, "members": members, "conversations": convs, "projects": projects, "kbs": kbs,
	})
}

// adminDeleteWorkspaceHandler removes a workspace and all content (admin triage).
func adminDeleteWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	ws, err := store.GetWorkspace(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	members, err := store.ListWorkspaceMembers(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := teardownWorkspace(d, r, ws); err != nil {
		writeError(w, 500, err)
		return
	}
	_ = store.RecordWorkspaceDeleted(r.Context(), d.DB, ws.ID, u.ID, ws.Name)
	for _, member := range members {
		publishUserEvent(d, nil, member.UserID, "workspace.membership_updated", "")
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// workspaceAuditLogHandler serves the admin audit page (§workspace RBAC
// phase 5). Members and guests are rejected — the trail never leaks to
// ordinary users.
func workspaceAuditLogHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID, Action: store.ActionWorkspaceAuditView,
	})
	if err != nil || !decision.Allowed {
		writeError(w, 404, errNotFound)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	logs, err := store.ListWorkspaceAuditLogs(r.Context(), d.DB, workspaceID, u.ID, limit, offset)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, 200, map[string]any{"logs": logs})
}
