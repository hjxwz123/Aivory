package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/msgcache"
	"aivory/server/internal/store"
)

// Switching is an explicit page action, never a timer. An empty target page
// disposes only existing drafts in other scopes; it does not create a new one.
func switchConversationDraftScopeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		ProjectID   string `json:"project_id"`
		Scope       string `json:"draft_scope"`
	}
	if err := decodeJSON(r, &req); err != nil || (req.Scope != "chat" && req.Scope != "draw") {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.WorkspaceID != "" {
		if role, err := store.IsWorkspaceMember(r.Context(), d.DB, req.WorkspaceID, u.ID); err != nil || role == "" {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
	}
	if req.ProjectID != "" {
		permissions, err := requestPermissions(d, r)
		if err != nil || !permissions.AllowKnowledgeBases {
			writeError(w, http.StatusForbidden, errKnowledgeBaseGroupPermission)
			return
		}
		project, err := store.GetProject(r.Context(), d.DB, req.ProjectID, u.ID)
		if err != nil || project.WorkspaceID != req.WorkspaceID {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
	}
	draft, deleted, err := store.SwitchConversationDraftScope(r.Context(), d.DB, u.ID, req.WorkspaceID, req.ProjectID, req.Scope)
	// Deletions may have committed even if the final recovery read fails. Finish
	// the committed file work regardless of whether that later read succeeded.
	if deleted != nil && len(deleted.ConversationIDs) > 0 {
		go finishConversationDraftDeletion(r.Context(), d, r, u.ID, deleted)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if draft != nil {
		stripServerConvFields(draft)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversation": draft, "deleted_ids": deleted.ConversationIDs,
	})
}

// Draft recovery is an on-demand read of one owner's scope, never a periodic
// scan or an implicit create when somebody opens the home page.
func findConversationDraftHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	scope := r.URL.Query().Get("draft_scope")
	if scope != "chat" && scope != "draw" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if workspaceID != "" {
		if role, err := store.IsWorkspaceMember(r.Context(), d.DB, workspaceID, u.ID); err != nil || role == "" {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
	}
	if projectID != "" {
		permissions, err := requestPermissions(d, r)
		if err != nil || !permissions.AllowKnowledgeBases {
			writeError(w, http.StatusForbidden, errKnowledgeBaseGroupPermission)
			return
		}
		project, err := store.GetProject(r.Context(), d.DB, projectID, u.ID)
		if err != nil || project.WorkspaceID != workspaceID {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
	}
	draft, err := store.FindConversationDraft(r.Context(), d.DB, u.ID, workspaceID, projectID, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	rows := []store.Conversation{}
	if draft != nil {
		stripServerConvFields(draft)
		rows = append(rows, *draft)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversations": rows, "limit": 1, "offset": 0, "has_more": false,
	})
}

func finishConversationDraftDeletion(ctx context.Context, d Deps, r *http.Request, userID string, deletion *store.ConversationDeletionState) {
	if deletion == nil || len(deletion.ConversationIDs) == 0 {
		return // no existing draft: no storage client, vectors, cancellation or events
	}
	// Once deletion commits, navigation/connection loss cannot cancel physical
	// cleanup. Keep it bounded and reuse the existing durable file-deletion ledger.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	for _, draftID := range deletion.ConversationIDs {
		cancelConversationGenerations(d, draftID)
		msgcache.Bump(d.Cache, draftID)
		cleanupRAGConversation(cleanupCtx, d, draftID, "discard conversation draft "+draftID)
		publishUserEvent(d, r, userID, "conversation.deleted", draftID)
	}
	if d.Tools != nil && d.Tools.Sandbox() != nil {
		for _, sandbox := range deletion.SandboxDiscards {
			if err := d.Tools.Sandbox().ReleaseDiscard(cleanupCtx, sandbox.SessionID, sandbox.ConversationID); err != nil {
				logStorageCleanup(d, "discard conversation draft sandbox %s: %v", sandbox.SessionID, err)
			}
		}
	}
	obj := objectStorageClient(d)
	for _, path := range deletion.StoragePaths {
		if _, err := cleanupOneStoragePath(cleanupCtx, d, obj, path); err != nil {
			logStorageCleanup(d, "discard conversation draft storage %s: %v", path, err)
			continue // durable ledger retries on a later sweep or restart
		}
		if err := store.DeletePendingStorageCleanup(cleanupCtx, d.DB, path); err != nil {
			logStorageCleanup(d, "discard conversation draft storage %s: forget path: %v", path, err)
		}
	}
}
