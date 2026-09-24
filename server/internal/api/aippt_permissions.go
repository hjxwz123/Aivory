package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

var errAiPPTDisabled = errors.New("AI PPT is disabled for this user or workspace")

func aiPPTWorkspaceID(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("workspace_id"))
}

func aiPPTPermission(d Deps, r *http.Request) error {
	u := authUser(r)
	var role string
	err := d.DB.QueryRowContext(r.Context(), `SELECT role FROM users WHERE id=?`, u.ID).Scan(&role)
	if err != nil {
		return err
	}
	permissions, err := store.UserGroupPermissionsForUser(r.Context(), d.DB, u.ID)
	if err != nil {
		return err
	}
	if role != "admin" && !permissions.AllowAiPPT {
		return errAiPPTDisabled
	}
	workspaceID := aiPPTWorkspaceID(r)
	access, err := store.GetDomainAccess(r.Context(), d.DB, u.ID)
	if err != nil {
		return err
	}
	if access != nil && access.Locked && workspaceID != access.WorkspaceID {
		return errAiPPTDisabled
	}
	if workspaceID == "" {
		return nil
	}
	workspace, err := store.GetWorkspaceForMember(r.Context(), d.DB, workspaceID, u.ID)
	if err != nil {
		return err
	}
	if workspace.Role == "guest" {
		return errAiPPTDisabled
	}
	policy, err := store.GetWorkspacePolicy(r.Context(), d.DB, workspaceID)
	if err != nil {
		return err
	}
	if !policy.AllowAiPPT {
		return errAiPPTDisabled
	}
	if workspace.Role == "admin" {
		return nil
	}
	var allowed bool
	if err := d.DB.QueryRowContext(r.Context(), `SELECT can_use_ai_ppt=1 FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspaceID, u.ID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errAiPPTDisabled
	}
	return nil
}

func aiPPTAuthorized(_ Deps, next handler) handler {
	return func(d Deps, w http.ResponseWriter, r *http.Request) {
		if err := aiPPTPermission(d, r); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errAiPPTDisabled) {
				status = http.StatusForbidden
			}
			if errors.Is(err, store.ErrNotFound) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		if id := pathParam(r, "id"); id != "" && strings.Contains(r.URL.Path, "/decks/") {
			deck, err := store.GetAiPPTDeck(r.Context(), d.DB, id, authUser(r).ID)
			if errors.Is(err, store.ErrNotFound) || (err == nil && deck.WorkspaceID != aiPPTWorkspaceID(r)) {
				writeError(w, http.StatusNotFound, store.ErrNotFound)
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		next(d, w, r)
	}
}
