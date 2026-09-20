package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

func adminListDomainsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListRegistrationDomains(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"domains": rows})
}
func adminSaveDomainHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req store.RegistrationDomain
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	create := r.Method == http.MethodPost
	if !create {
		req.Domain = pathParam(r, "domain")
	}
	err := store.SaveRegistrationDomain(r.Context(), d.DB, req, create)
	if err != nil {
		status := 409
		if errors.Is(err, store.ErrInvalidDomain) || errors.Is(err, store.ErrInvalidWorkspaceProfile) {
			status = 400
		}
		if errors.Is(err, store.ErrNotFound) {
			status = 404
		}
		writeError(w, status, err)
		return
	}
	domain, _ := store.NormalizeRegistrationDomain(req.Domain)
	notifyDomainUsers(d, r, domain)
	publishWorkspaceAccessEvent(d, r, req.WorkspaceID, "workspace.profile_updated")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func notifyDomainUsers(d Deps, r *http.Request, domain string) {
	if members, err := store.ListDomainUsers(r.Context(), d.DB, domain); err == nil {
		for _, m := range members {
			revokeUserPermissionSnapshots(d, m.UserID)
			publishUserEvent(d, r, m.UserID, "account.permissions_updated", "")
		}
	}
}
func adminDeleteDomainHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	domain := pathParam(r, "domain")
	members, err := store.ListDomainUsers(r.Context(), d.DB, domain)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := store.DeleteRegistrationDomain(r.Context(), d.DB, domain); err != nil {
		writeError(w, 404, err)
		return
	}
	for _, m := range members {
		revokeUserPermissionSnapshots(d, m.UserID)
		publishUserEvent(d, r, m.UserID, "account.permissions_updated", "")
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func adminDomainUsersHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	users, err := store.ListDomainUsers(r.Context(), d.DB, pathParam(r, "domain"))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"users": users})
}
func adminDomainUserCandidatesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	users, err := store.ListDomainUserCandidates(r.Context(), d.DB, pathParam(r, "domain"), r.URL.Query().Get("q"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrInvalidDomain) {
			status = http.StatusBadRequest
		} else if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}
func adminEnrollDomainUsersHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.UserIDs) == 0 || len(req.UserIDs) > store.MaxDomainUserEnrollmentBatch {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	added, err := store.EnrollExistingDomainUsers(r.Context(), d.DB, pathParam(r, "domain"), req.UserIDs)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, store.ErrInvalidDomain):
			status = http.StatusBadRequest
		case errors.Is(err, store.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, store.ErrForbidden):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	for _, userID := range added {
		revokeUserPermissionSnapshots(d, userID)
		publishUserEvent(d, r, userID, "account.permissions_updated", "")
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": len(added), "user_ids": added})
}
func adminDomainUserAccessHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		LockOverride *bool `json:"lock_override"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	uid := pathParam(r, "uid")
	if err := store.UpdateDomainUserAccess(r.Context(), d.DB, pathParam(r, "domain"), uid, req.LockOverride); err != nil {
		writeError(w, 404, err)
		return
	}
	revokeUserPermissionSnapshots(d, uid)
	publishUserEvent(d, r, uid, "account.permissions_updated", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func adminRemoveDomainUserHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	uid := pathParam(r, "uid")
	result, err := store.RemoveDomainUser(r.Context(), d.DB, pathParam(r, "domain"), uid)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrInvalidDomain) {
			status = http.StatusBadRequest
		} else if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	if err := revokeMessageGenerationStreams(d, result.RevokedMessageIDs); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if result.WorkspaceMembershipRemoved {
		revokeWorkspaceMemberGenerations(d, result.WorkspaceID, uid)
	}
	revokeUserPermissionSnapshots(d, uid)
	publishWorkspaceAccessEvent(d, r, result.WorkspaceID, "workspace.membership_updated", uid)
	publishUserEvent(d, r, uid, "account.permissions_updated", "")
	writeJSON(w, http.StatusOK, map[string]bool{
		"ok":                           true,
		"workspace_membership_removed": result.WorkspaceMembershipRemoved,
	})
}
func adminCreateWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		OwnerID string `json:"owner_id"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" || len([]rune(req.Name)) > 100 {
		writeError(w, 400, errInvalidInput)
		return
	}
	owner, err := store.FindUserByID(r.Context(), d.DB, req.OwnerID)
	if err != nil || owner.Status != "active" {
		writeError(w, 400, errors.New("choose an active user as workspace administrator"))
		return
	}
	access, err := store.GetDomainAccess(r.Context(), d.DB, req.OwnerID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if access != nil && access.Locked {
		writeError(w, 409, errors.New("unlock this user's space access before assigning another workspace"))
		return
	}
	ws, err := store.CreateWorkspace(r.Context(), d.DB, req.OwnerID, req.Name)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	publishWorkspaceAccessEvent(d, r, ws.ID, "workspace.membership_updated")
	writeJSON(w, 201, ws)
}
func adminTransferWorkspaceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.UserID == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	workspaceID := pathParam(r, "id")
	target, err := store.FindUserByID(r.Context(), d.DB, req.UserID)
	if err != nil || target.Status != "active" {
		writeError(w, 400, errors.New("choose an active workspace member"))
		return
	}
	access, err := store.GetDomainAccess(r.Context(), d.DB, req.UserID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if access != nil && access.Locked && access.WorkspaceID != workspaceID {
		writeError(w, 409, store.ErrForbidden)
		return
	}
	ws, err := store.AdminTransferWorkspaceOwnership(r.Context(), d.DB, workspaceID, authUser(r).ID, req.UserID)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.membership_updated")
	writeJSON(w, 200, ws)
}
