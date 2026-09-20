package api

import (
	"aivory/server/internal/store"
	"errors"
	"net/http"
)

func updateWorkspaceProfileHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var profile store.WorkspaceProfile
	if err := decodeJSON(r, &profile); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	id, userID := pathParam(r, "id"), authUser(r).ID
	if err := store.UpdateWorkspaceProfile(r.Context(), d.DB, id, userID, profile); err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		case errors.Is(err, store.ErrInvalidWorkspaceProfile):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	workspace, err := store.GetWorkspaceForMember(r.Context(), d.DB, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	publishWorkspaceAccessEvent(d, r, id, "workspace.profile_updated", userID)
	writeJSON(w, http.StatusOK, workspace)
}
