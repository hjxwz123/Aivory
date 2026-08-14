package api

import (
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

// workspaceMembersHandler lists members — visible to every member.
func workspaceMembersHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if _, err := store.GetWorkspaceForMember(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	members, err := store.ListWorkspaceMembers(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
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
// ceiling. Per-knowledge-base permissions are managed separately on each KB.
func updateWorkspaceMemberPermissionsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	memberID := pathParam(r, "uid")
	var permissions store.WorkspaceMemberPermissions
	if err := decodeJSON(r, &permissions); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	workspace, err := store.GetWorkspaceForMember(r.Context(), d.DB, workspaceID, u.ID)
	if err != nil || workspace.OwnerID != u.ID || memberID == workspace.OwnerID {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	member, err := store.UpdateWorkspaceMemberPermissions(
		r.Context(), d.DB, workspaceID, u.ID, memberID, permissions,
	)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.permissions_updated", u.ID, memberID)
	writeJSON(w, http.StatusOK, member)
}

// kickWorkspaceMemberHandler removes a member — owner only; the owner row
// itself is protected in the store.
func kickWorkspaceMemberHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	memberID := pathParam(r, "uid")
	ws, err := store.GetWorkspaceForMember(r.Context(), d.DB, id, u.ID)
	if err != nil || ws.OwnerID != u.ID {
		writeError(w, 404, errNotFound)
		return
	}
	revokedMessageIDs, err := store.RemoveWorkspaceMemberWithRevokedGenerations(r.Context(), d.DB, id, memberID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	if err := revokeMessageGenerationStreams(d, revokedMessageIDs); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
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
	publishWorkspaceAccessEvent(d, r, id, "workspace.membership_updated", u.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// rotateWorkspaceInviteHandler mints a fresh invite link (owner only), killing
// the old one.
func rotateWorkspaceInviteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	ws, err := store.GetWorkspaceForMember(r.Context(), d.DB, id, u.ID)
	if err != nil || ws.OwnerID != u.ID {
		writeError(w, 404, errNotFound)
		return
	}
	token, err := store.RotateWorkspaceInvite(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"invite_token": token})
}

// workspaceInviteInfoHandler resolves an invite token to a join preview
// (name / owner / member count). Auth'd + rate-limited; uniform 404 on
// unknown tokens so the space can't be enumerated.
func workspaceInviteInfoHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	token := pathParam(r, "token")
	ws, err := store.GetWorkspaceByInviteToken(r.Context(), d.DB, token)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": ws.ID, "name": ws.Name, "owner_name": ws.OwnerName, "member_count": ws.MemberCount,
	})
}

// joinWorkspaceHandler consumes an invite link: the caller becomes a member
// (idempotent for existing members).
func joinWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	token := pathParam(r, "token")
	ws, err := store.JoinWorkspaceByInviteToken(r.Context(), d.DB, token, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	publishWorkspaceAccessEvent(d, r, ws.ID, "workspace.membership_updated", u.ID, ws.OwnerID)
	writeJSON(w, 200, map[string]any{"id": ws.ID, "name": ws.Name})
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
	revokedMessageIDs, err := store.ScrubWorkspaceStreamingMessages(r.Context(), d.DB, ws.ID)
	if err != nil {
		return err
	}
	if !publishWorkspaceGenerationRevocation(d, ws.ID) {
		return errors.New("workspace generation revocation unavailable")
	}
	// If a later teardown step fails, restore the workspace for new turns. The
	// per-message tombstones remain, so generations scrubbed above never revive.
	teardownComplete := false
	defer func() {
		if !teardownComplete {
			d.Cache.Delete(workspaceGenerationRevocationKey(ws.ID))
		}
	}()
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
	if err := store.DeleteWorkspaceRow(r.Context(), d.DB, ws.ID); err != nil {
		return err
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
	for _, member := range members {
		publishUserEvent(d, nil, member.UserID, "workspace.membership_updated", "")
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
