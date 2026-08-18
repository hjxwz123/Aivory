package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

func workspaceSettingsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: workspaceID, UserID: u.ID, Action: store.ActionWorkspaceSettingsUpdate,
	})
	if err != nil || !decision.Allowed {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	settings, err := store.GetWorkspaceSettings(r.Context(), d.DB, workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	// Site-level defaults are not exposed through the workspace-admin surface.
	settings.InitialSiteGroupID = ""
	settings.InitialPermanentCredits = 0
	writeJSON(w, http.StatusOK, settings)
}

func updateWorkspaceSettingsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	var patch store.WorkspaceSettingsPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	// Workspace admins may change only member defaults and personal-space policy.
	patch.InitialSiteGroupID = nil
	patch.InitialPermanentCredits = nil
	settings, err := store.UpdateWorkspaceSettings(r.Context(), d.DB, workspaceID, u.ID, patch)
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
	settings.InitialSiteGroupID = ""
	settings.InitialPermanentCredits = 0
	publishWorkspaceAccessEvent(d, r, workspaceID, "workspace.settings_updated", u.ID)
	writeJSON(w, http.StatusOK, settings)
}

func adminWorkspaceSettingsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	workspaceID := pathParam(r, "id")
	var patch store.WorkspaceSettingsPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	settings, err := store.UpdateWorkspaceSettingsAsAdmin(r.Context(), d.DB, workspaceID, u.ID, patch)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func adminWorkspaceDomainMappingsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	workspaceID := pathParam(r, "id")
	if r.Method == http.MethodGet {
		rows, err := store.ListWorkspaceDomainMappings(r.Context(), d.DB, workspaceID)
		if err != nil {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mappings": rows})
		return
	}
	u := authUser(r)
	var req struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Domain) == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	mapping, err := store.CreateWorkspaceDomainMapping(r.Context(), d.DB, workspaceID, u.ID, req.Domain)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrWorkspaceDomainExists):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, store.ErrWorkspaceDomainInvalid):
			writeError(w, http.StatusBadRequest, err)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, mapping)
}

func adminWorkspaceDomainMappingDeleteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if err := store.DeleteWorkspaceDomainMapping(r.Context(), d.DB, pathParam(r, "mappingId"), u.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
